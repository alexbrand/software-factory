# Agent Orchestration System

Kubernetes-based platform for orchestrating fleets of AI agents in stateful sandboxes.

## Spec Structure

The spec uses progressive disclosure — read `spec/README.md` for the reading guide and layer map.
When working on a feature, read the relevant spec documents before making changes. Key references:

- **Domain model & terminology**: `spec/02-concepts-and-terminology.md`
- **CRDs (Pool, Sandbox, AgentConfig, Workflow, Task)**: `spec/04-control-plane.md`
- **Sandbox pod architecture (SDK + bridge sidecar)**: `spec/06-agent-adapter.md`
- **MCP tool provisioning (ToolHive)**: `spec/10-prior-art.md` (ToolHive section)

## Tech Stack

- **Language**: Go (use controller-runtime for operators)
- **Platform**: Kubernetes with CRDs and operator pattern
- **API group**: `factory.example.com/v1alpha1`
- **Event bus**: NATS JetStream
- **Observability**: OpenTelemetry
- **MCP tools**: ToolHive (Stacklok) with VirtualMCPServer (vMCP)
- **In-sandbox runtime**: Sandbox Agent SDK (Rust) + bridge sidecar (Go)

## Conventions

- CRD types go in `api/v1alpha1/`
- Controllers go in `internal/controller/`
- Use `sigs.k8s.io/controller-runtime` patterns (Reconciler interface, client.Client, etc.)
- Errors: wrap with `fmt.Errorf("context: %w", err)` — no naked returns
- Linting: `golangci-lint run` must pass
- Testing: table-driven tests, use envtest for controller tests

## Verification Loop

After every change, run the full verification loop:

```bash
make all    # runs: generate → build → lint → test
```

Individual steps:
```bash
make generate   # regenerate CRD manifests, RBAC, and deepcopy methods
make build      # compile all three binaries (controller-manager, apiserver, bridge)
make lint       # run golangci-lint (install with: make golangci-lint)
make test       # run all unit tests with race detector
```

**A component is done when `make all` passes with no errors.**

If `make lint` fails because golangci-lint is not installed, run `make golangci-lint` first.
If `make generate` fails because controller-gen is not installed, run `make controller-gen` first.

## Implementation Phases

Work through these phases in order. Each phase must pass `make all` before moving to the next.

### Phase 1: CRD Types
Define all CRD types in `api/v1alpha1/`. Read `spec/04-control-plane.md` for each CRD's spec and status fields.

Files to create:
- `api/v1alpha1/pool_types.go` — Pool spec, status, scaling config
- `api/v1alpha1/sandbox_types.go` — Sandbox spec, status, phase enum
- `api/v1alpha1/agentconfig_types.go` — AgentConfig spec (SDK, bridge, credentials)
- `api/v1alpha1/workflow_types.go` — Workflow spec, status, task DAG
- `api/v1alpha1/task_types.go` — Task spec, status, artifacts, token usage
- `api/v1alpha1/session_types.go` — Session spec, status, event stream ref

After creating types, run `make generate` to produce deepcopy methods and CRD YAML.

### Phase 2: Pool and Sandbox Controllers
Implement the controllers that manage the warm pool and sandbox lifecycle. Read `spec/04-control-plane.md` (Pool Controller, Sandbox Controller sections).

- `internal/controller/pool/` — maintain min replicas, scale up/down
- `internal/controller/sandbox/` — create pods with SDK + bridge containers, provision PVCs, apply NetworkPolicy

### Phase 3: Workflow and Task Controllers
Implement orchestration. Read `spec/04-control-plane.md` (Workflow Controller, Task Controller sections) and `spec/07-orchestration-engine.md`.

- `internal/controller/workflow/` — validate DAG, create root tasks, advance on completion
- `internal/controller/task/` — claim sandbox, create session, extract outputs

### Phase 4: Session Controller and NATS
Implement session management and event streaming. Read `spec/04-control-plane.md` (Session Controller section) and `spec/08-observability-and-events.md`.

- `internal/controller/session/` — connect to bridge, stream events via NATS
- `internal/nats/` — JetStream client, stream/consumer definitions

### Phase 5: Bridge Sidecar
Implement the bridge sidecar binary. Read `spec/06-agent-adapter.md` for the full architecture.

- `internal/bridge/` — session management, event normalization, credential proxy
- `internal/sdk/` — generated Go client for Sandbox Agent SDK

### Phase 6: API Server
Implement the external API. Read `spec/04-control-plane.md` (API Server section).

- `internal/apiserver/` — gRPC server, REST gateway, SSE streaming
