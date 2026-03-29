# Agent Orchestration System

Kubernetes-based platform for orchestrating fleets of AI coding agents in stateful sandboxes.

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
