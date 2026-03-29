# Software Factory

Kubernetes-based platform for orchestrating fleets of AI coding agents in stateful sandboxes.

See [spec/README.md](spec/README.md) for the full system specification.

## Directory Structure

```
software-factory/
├── api/                            # CRD type definitions
│   └── v1alpha1/                   # factory.example.com/v1alpha1
│       ├── pool_types.go
│       ├── sandbox_types.go
│       ├── agentconfig_types.go
│       ├── workflow_types.go
│       ├── task_types.go
│       ├── session_types.go
│       ├── workflowtemplate_types.go
│       ├── webhooksubscription_types.go
│       ├── groupversion_info.go
│       └── zz_generated.deepcopy.go
│
├── cmd/                            # Binary entry points
│   ├── controller-manager/         # All control plane controllers in one binary
│   │   └── main.go
│   ├── apiserver/                  # gRPC + REST API server
│   │   └── main.go
│   └── bridge/                     # Bridge sidecar (runs in sandbox pods)
│       └── main.go
│
├── internal/                       # Private application code
│   ├── controller/                 # Kubernetes controllers
│   │   ├── pool/
│   │   │   ├── reconciler.go       # Pool scaling logic
│   │   │   └── reconciler_test.go
│   │   ├── sandbox/
│   │   │   ├── reconciler.go       # Pod/PVC/NetworkPolicy creation
│   │   │   └── reconciler_test.go
│   │   ├── workflow/
│   │   │   ├── reconciler.go       # DAG validation, task creation
│   │   │   └── reconciler_test.go
│   │   ├── task/
│   │   │   ├── reconciler.go       # Sandbox claiming, session creation
│   │   │   └── reconciler_test.go
│   │   └── session/
│   │       ├── reconciler.go       # Bridge connection, event streaming
│   │       └── reconciler_test.go
│   │
│   ├── bridge/                     # Bridge sidecar implementation
│   │   ├── bridge.go               # Core Bridge struct and lifecycle
│   │   ├── session.go              # Session management via SDK
│   │   ├── events.go               # Event normalization (SDK → NATS)
│   │   ├── credentials.go          # HTTP credential proxy
│   │   └── workspace.go            # Workspace preparation and artifact extraction
│   │
│   ├── apiserver/                  # API server implementation
│   │   ├── server.go               # gRPC server setup
│   │   ├── handlers/               # Request handlers per resource
│   │   │   ├── workflow.go
│   │   │   ├── task.go
│   │   │   ├── pool.go
│   │   │   └── session.go
│   │   └── stream/                 # SSE event streaming
│   │       └── stream.go
│   │
│   ├── nats/                       # NATS JetStream client and stream config
│   │   ├── client.go
│   │   ├── streams.go              # Stream/consumer definitions
│   │   └── publisher.go
│   │
│   ├── sdk/                        # Generated Sandbox Agent SDK client
│   │   └── client.go               # Go client wrapping SDK HTTP API (:2468)
│   │
│   ├── webhook/                    # Webhook dispatcher
│   │   └── dispatcher.go
│   │
│   └── otel/                       # OpenTelemetry setup and exporters
│       ├── metrics.go
│       └── tracing.go
│
├── config/                         # Kubernetes manifests (kustomize)
│   ├── crd/                        # Generated CRD YAML
│   │   └── bases/
│   ├── manager/                    # Controller manager deployment
│   ├── apiserver/                  # API server deployment
│   ├── rbac/                       # RBAC roles and bindings
│   ├── nats/                       # NATS JetStream StatefulSet
│   ├── samples/                    # Example CRs
│   │   ├── pool.yaml
│   │   ├── agentconfig-claude.yaml
│   │   ├── workflow.yaml
│   │   └── task.yaml
│   └── default/                    # Default kustomization overlay
│
├── scripts/                        # Development and CI scripts
│   ├── setup.sh                    # Install Go toolchain and dependencies
│   ├── pre-commit.sh               # Pre-commit checks (vet, lint, build, spec)
│   └── pre-push.sh                 # Pre-push checks (test, spec consistency)
│
├── spec/                           # System specification (progressive disclosure)
│   ├── README.md
│   ├── 01-vision-and-goals.md
│   ├── 02-concepts-and-terminology.md
│   ├── 03-architecture-overview.md
│   ├── 04-control-plane.md
│   ├── 05-sandbox-runtime.md
│   ├── 06-agent-adapter.md
│   ├── 07-orchestration-engine.md
│   ├── 08-observability-and-events.md
│   ├── 09-security-model.md
│   └── 10-prior-art.md
│
├── hack/                           # Code generation and dev tooling
│   ├── boilerplate.go.txt          # License header for generated code
│   └── generate.sh                 # Run controller-gen, oapi-codegen
│
├── .claude/
│   └── settings.json               # Claude Code hooks (pre-commit, pre-push)
│
├── CLAUDE.md                       # Claude Code project context
├── Dockerfile                      # Multi-stage build (controller-manager, apiserver, bridge)
├── Makefile                        # Build, test, generate, deploy targets
├── go.mod
└── go.sum
```

## Key Design Decisions

**One module, three binaries.** All Go code lives in a single module. The `cmd/` directory has three entry points:
- `controller-manager` — runs all five controllers (pool, sandbox, workflow, task, session) in one process, standard for controller-runtime projects
- `apiserver` — gRPC + REST gateway for external clients
- `bridge` — sidecar binary deployed inside each sandbox pod

**CRDs in `api/`, controllers in `internal/controller/`.** Follows kubebuilder conventions. Each controller gets its own package to keep reconciliation logic isolated and independently testable.

**Bridge sidecar as a separate package.** `internal/bridge/` contains the Go code that runs inside sandbox pods. It talks to the Sandbox Agent SDK over HTTP (:2468), normalizes events, and publishes to NATS. This is the only code that imports the SDK client.

**Generated SDK client in `internal/sdk/`.** The Sandbox Agent SDK exposes an OpenAPI spec. We generate a Go client from it rather than hand-rolling HTTP calls.

**Shared infrastructure packages.** `internal/nats/` and `internal/otel/` are used by multiple binaries (controllers publish events, bridge publishes events, apiserver consumes events).

**Kustomize for deployment.** `config/` follows the standard kustomize layout with base manifests and overlays. CRD YAML is generated from the Go types via `controller-gen`.

## Build

```bash
make generate    # Run controller-gen (CRDs, RBAC) and oapi-codegen (SDK client)
make build       # Build all three binaries
make test        # Run unit tests
make lint        # Run golangci-lint
make manifests   # Generate CRD YAML into config/crd/bases/
make docker-build # Multi-stage Docker build
```
