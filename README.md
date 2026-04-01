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
- [kustomize](https://kubectl.docs.kubernetes.io/installation/kustomize/) (or `kubectl` v1.14+ which includes it)
- Container images pushed to a registry accessible from the cluster

### 1. Build and push container images

Build the three container images using the multi-stage Dockerfile:

```bash
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

### 2. Deploy everything with kustomize

The `config/default/` overlay deploys all components (CRDs, RBAC, controller manager, API server, NATS) into the `factory-system` namespace.

Update the image references in [`config/default/kustomization.yaml`](config/default/kustomization.yaml) to point to your registry, then apply:

```bash
kubectl create namespace factory-system
kubectl apply -k config/default/
```

This installs:

| Component | Manifest directory | Description |
|-----------|-------------------|-------------|
| CRDs | [`config/crd/`](config/crd/) | All six CRDs under `factory.example.com/v1alpha1` |
| RBAC | [`config/rbac/`](config/rbac/) | ClusterRole and ClusterRoleBinding for the controller manager |
| Controller manager | [`config/manager/`](config/manager/) | Deployment, Service, and ServiceAccount |
| API server | [`config/apiserver/`](config/apiserver/) | Deployment, Service, and ServiceAccount |
| NATS JetStream | [`config/nats/`](config/nats/) | StatefulSet with persistent storage and headless Service |

### Deploying individual components

You can also deploy components individually:

```bash
# CRDs only
kubectl apply -k config/crd/

# RBAC only
kubectl apply -k config/rbac/

# Controller manager only
kubectl apply -k config/manager/

# NATS only
kubectl apply -k config/nats/
```

### Verify the deployment

```bash
# Check that CRDs are installed
kubectl get crds | grep factory.example.com

# Check that all pods are running
kubectl get pods -n factory-system

# Verify the API resources are available
kubectl api-resources | grep factory
```

## Quick Start

This example walks through setting up a coding agent that performs a task in a sandbox. Sample manifests are in [`config/samples/`](config/samples/).

### 1. Create a namespace and API key secret

```bash
kubectl create namespace demo

# Store your Anthropic API key as a Kubernetes secret
kubectl create secret generic anthropic-credentials \
  --namespace demo \
  --from-literal=api-key=$ANTHROPIC_API_KEY
```

### 2. Deploy the agent config, pool, and task

Review the sample manifests to understand each resource:

- [`config/samples/agentconfig.yaml`](config/samples/agentconfig.yaml) — configures a Claude Code agent with the Sandbox Agent SDK, bridge sidecar, and Anthropic API credentials
- [`config/samples/pool.yaml`](config/samples/pool.yaml) — creates a warm pool of 1–3 sandboxes with CPU/memory limits and a network policy
- [`config/samples/task.yaml`](config/samples/task.yaml) — submits a prompt that asks the agent to write and test a Go program

Apply the agent config and pool first, then submit the task:

```bash
kubectl apply -f config/samples/agentconfig.yaml -f config/samples/pool.yaml -n demo
kubectl apply -f config/samples/task.yaml -n demo
```

### 3. Watch the task

```bash
# Watch the task progress through its lifecycle
kubectl get tasks -n demo -w

# Check task status and details
kubectl describe task hello-world -n demo
```

The task will transition through these phases: `Pending` → `Running` → `Succeeded`.

### 4. Inspect results

Once the task completes, you can inspect the sandbox that ran it:

```bash
# See which sandbox was assigned
kubectl get task hello-world -n demo -o jsonpath='{.status.sandboxRef.name}'

# View the task's token usage
kubectl get task hello-world -n demo -o jsonpath='{.status.tokenUsage}'
```

For more examples, see the [`examples/`](examples/) directory, which includes a [coding agent with Kubernetes access](examples/coding-agent-with-k8s-access/) and a [personal assistant with MCP tools](examples/personal-assistant/).
