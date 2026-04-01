# Software Factory

Kubernetes-based platform for orchestrating fleets of AI coding agents in stateful sandboxes.

See [spec/README.md](spec/README.md) for the full system specification.

## Project Layout

```
api/v1alpha1/       CRD type definitions (factory.example.com/v1alpha1)
cmd/                Binary entry points (one per deployable)
internal/           Private application code
config/             Kubernetes manifests (kustomize)
spec/               System specification (progressive disclosure)
hack/               Code generation and dev tooling
scripts/            Development and CI scripts
```

## Binaries

Three binaries, one Go module:

| Binary | Directory | Description |
|--------|-----------|-------------|
| `controller-manager` | `cmd/controller-manager/` | Runs all control plane controllers in one process |
| `apiserver` | `cmd/apiserver/` | gRPC + REST gateway for external clients |
| `bridge` | `cmd/bridge/` | Sidecar binary deployed inside each sandbox pod |

## CRDs

All types live in `api/v1alpha1/` under the `factory.example.com/v1alpha1` API group:

| CRD | Purpose |
|-----|---------|
| **Pool** | Warm sandbox pool with scaling config (min/max replicas, idle timeout) |
| **Sandbox** | Single isolated execution environment with lifecycle management |
| **AgentConfig** | Agent runtime configuration (SDK image, bridge image, credentials) |
| **Workflow** | DAG of tasks with shared context and dependency tracking |
| **Task** | Unit of work — claims a sandbox, runs a prompt, produces outputs |
| **Session** | Agent session lifecycle bridging control plane and sandbox |
| **WorkflowTemplate** | Reusable parameterized workflow patterns |
| **WebhookSubscription** | External webhook subscriptions for workflow/task events |

## Controllers

Each controller gets its own package under `internal/controller/` for isolation and independent testing:

| Controller | Package | Watches | Responsibility |
|------------|---------|---------|----------------|
| **Pool** | `internal/controller/pool/` | Pool, Sandbox | Maintain warm pool size, scale up on demand, terminate idle sandboxes |
| **Sandbox** | `internal/controller/sandbox/` | Sandbox, Pod | Create pods (init + SDK + bridge), provision PVCs, apply NetworkPolicy |
| **Workflow** | `internal/controller/workflow/` | Workflow, Task | Validate DAG, create root tasks, advance on task completion |
| **Task** | `internal/controller/task/` | Task, Sandbox | Claim sandbox from pool, create session, extract outputs on completion |
| **Session** | `internal/controller/session/` | Session | Connect to bridge sidecar, stream events via NATS, track token usage |

## Internal Packages

| Package | Description |
|---------|-------------|
| `internal/bridge/` | Bridge sidecar — session management, event normalization (SDK → NATS), credential proxy, workspace preparation. Only package that imports the SDK client. |
| `internal/apiserver/` | API server — gRPC service definitions, REST handlers, SSE event streaming |
| `internal/sdk/` | Generated Go client for Sandbox Agent SDK HTTP API (:2468) |
| `internal/nats/` | NATS JetStream client, stream/consumer definitions, event publishing |
| `internal/otel/` | OpenTelemetry metrics and tracing setup |
| `internal/webhook/` | Webhook dispatcher for external event subscriptions |

## Deployment Manifests

`config/` uses kustomize with generated CRDs:

| Directory | Contents |
|-----------|----------|
| `config/crd/` | Generated CRD YAML (via `controller-gen`) |
| `config/manager/` | Controller manager deployment |
| `config/apiserver/` | API server deployment |
| `config/rbac/` | Roles and bindings |
| `config/nats/` | NATS JetStream StatefulSet |
| `config/samples/` | Example CRs (Pool, AgentConfig, Workflow, Task) |
| `config/default/` | Default kustomization overlay |

## Build

```bash
make generate     # Run controller-gen (CRDs, RBAC) and oapi-codegen (SDK client)
make build        # Build all three binaries
make test         # Run unit tests
make lint         # Run golangci-lint
make manifests    # Generate CRD YAML into config/crd/bases/
make docker-build # Multi-stage Docker build
```

## Deployment

### Prerequisites

- A Kubernetes cluster (v1.28+)
- `kubectl` configured to talk to the cluster
- A NATS JetStream instance (for event streaming)
- Container images pushed to a registry accessible from the cluster

### 1. Build and push container images

Build the three container images using the multi-stage Dockerfile:

```bash
# Build images
make docker-build

# Tag and push to your registry
export REGISTRY=ghcr.io/your-org

docker tag software-factory-controller-manager:latest $REGISTRY/factory-controller-manager:latest
docker tag software-factory-apiserver:latest $REGISTRY/factory-apiserver:latest
docker tag software-factory-bridge:latest $REGISTRY/factory-bridge:latest

docker push $REGISTRY/factory-controller-manager:latest
docker push $REGISTRY/factory-apiserver:latest
docker push $REGISTRY/factory-bridge:latest
```

### 2. Install CRDs

Apply the generated Custom Resource Definitions to your cluster:

```bash
kubectl apply -f config/crd/bases/
```

This installs all six CRDs (`Pool`, `Sandbox`, `AgentConfig`, `Workflow`, `Task`, `Session`) under the `factory.example.com/v1alpha1` API group.

### 3. Set up RBAC

Apply the controller manager ClusterRole and bind it to the controller's service account:

```bash
kubectl apply -f config/rbac/role.yaml

# Create a service account and binding for the controller manager
kubectl create namespace factory-system

kubectl create serviceaccount controller-manager -n factory-system

kubectl create clusterrolebinding controller-manager-binding \
  --clusterrole=controller-manager-role \
  --serviceaccount=factory-system:controller-manager
```

### 4. Deploy NATS JetStream

The platform uses NATS JetStream as its event bus. Deploy a NATS instance if you don't have one already:

```bash
helm repo add nats https://nats-io.github.io/k8s/helm/charts/
helm repo update

helm install nats nats/nats \
  --namespace factory-system \
  --set config.jetstream.enabled=true \
  --set config.jetstream.memStorage.size=1Gi \
  --set config.jetstream.fileStorage.size=10Gi
```

### 5. Deploy the controller manager

Create a Deployment for the controller manager:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
  namespace: factory-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: controller-manager
  template:
    metadata:
      labels:
        app: controller-manager
    spec:
      serviceAccountName: controller-manager
      containers:
        - name: manager
          image: ghcr.io/your-org/factory-controller-manager:latest
          args:
            - --nats-url=nats://nats.factory-system.svc:4222
          ports:
            - containerPort: 8081
              name: health
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8081
          readinessProbe:
            httpGet:
              path: /readyz
              port: 8081
```

### 6. Deploy the API server (optional)

The API server provides a gRPC + REST gateway for external clients:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: apiserver
  namespace: factory-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: apiserver
  template:
    metadata:
      labels:
        app: apiserver
    spec:
      containers:
        - name: apiserver
          image: ghcr.io/your-org/factory-apiserver:latest
          args:
            - --addr=:8080
            - --nats-url=nats://nats.factory-system.svc:4222
          ports:
            - containerPort: 8080
              name: grpc
---
apiVersion: v1
kind: Service
metadata:
  name: apiserver
  namespace: factory-system
spec:
  selector:
    app: apiserver
  ports:
    - port: 8080
      targetPort: 8080
```

### Verify the deployment

```bash
# Check that CRDs are installed
kubectl get crds | grep factory.example.com

# Check that the controller manager is running
kubectl get pods -n factory-system

# Verify the API resources are available
kubectl api-resources | grep factory
```

## Quick Start

This example walks through setting up a coding agent that performs a task in a sandbox.

### 1. Create a namespace and API key secret

```bash
kubectl create namespace demo

# Store your Anthropic API key as a Kubernetes secret
kubectl create secret generic anthropic-credentials \
  --namespace demo \
  --from-literal=api-key=$ANTHROPIC_API_KEY
```

### 2. Define an AgentConfig

The `AgentConfig` tells the factory what agent runtime to use and how to configure it:

```yaml
# agentconfig.yaml
apiVersion: factory.example.com/v1alpha1
kind: AgentConfig
metadata:
  name: claude-code
  namespace: demo
spec:
  agentType: claude-code
  displayName: "Claude Code"

  sdk:
    image: ghcr.io/rivet-dev/sandbox-agent:v0.4.2
    port: 2468

  bridge:
    image: ghcr.io/your-org/factory-bridge:latest
    port: 8080
    healthCheck:
      httpGet:
        path: /healthz
        port: 8080
      initialDelaySeconds: 5
      periodSeconds: 10

  agentSettings:
    allowedTools:
      - bash
      - read
      - write
      - edit
      - glob
      - grep

  credentials:
    - name: ANTHROPIC_API_KEY
      secretRef:
        name: anthropic-credentials
        key: api-key
      host: api.anthropic.com
      header: x-api-key
```

### 3. Create a Pool

The `Pool` maintains warm sandboxes ready to accept work:

```yaml
# pool.yaml
apiVersion: factory.example.com/v1alpha1
kind: Pool
metadata:
  name: coding-pool
  namespace: demo
spec:
  agentConfigRef:
    name: claude-code

  replicas:
    min: 1
    max: 3
    idleTimeout: 10m
    scaleUpThreshold: 0.8

  sandboxTemplate:
    resources:
      requests:
        cpu: "1"
        memory: "2Gi"
      limits:
        cpu: "2"
        memory: "4Gi"

    storage:
      size: 10Gi
      storageClassName: standard
      reclaimPolicy: Delete

    networkPolicy:
      egressRules:
        - to: ["api.anthropic.com"]
          ports: [443]
```

### 4. Submit a Task

A `Task` assigns a prompt to an agent in the pool. The controller claims an available sandbox, creates a session, and streams the agent's work:

```yaml
# task.yaml
apiVersion: factory.example.com/v1alpha1
kind: Task
metadata:
  name: hello-world
  namespace: demo
spec:
  poolRef:
    name: coding-pool

  prompt: |
    Create a Go program in /workspace/output/ that prints "Hello from the
    Software Factory!" and includes a unit test. Make sure the test passes.

  outputs:
    - path: /workspace/output/
      artifact: hello-app

  timeout: 10m
  retries: 1
```

### 5. Apply and watch

```bash
kubectl apply -f agentconfig.yaml -f pool.yaml
kubectl apply -f task.yaml

# Watch the task progress through its lifecycle
kubectl get tasks -n demo -w

# Check task status and details
kubectl describe task hello-world -n demo
```

The task will transition through these phases: `Pending` → `Running` → `Succeeded`.

### 6. Inspect results

Once the task completes, you can inspect the sandbox that ran it:

```bash
# See which sandbox was assigned
kubectl get task hello-world -n demo -o jsonpath='{.status.sandboxRef.name}'

# View the task's token usage
kubectl get task hello-world -n demo -o jsonpath='{.status.tokenUsage}'
```

For more examples, see the [`examples/`](examples/) directory, which includes a [coding agent with Kubernetes access](examples/coding-agent-with-k8s-access/) and a [personal assistant with MCP tools](examples/personal-assistant/).
