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
