# 03 — Architecture Overview

**Status:** DRAFT

## System Architecture

The system follows a layered architecture built on Kubernetes primitives. Each layer has a clear responsibility boundary.

```
┌─────────────────────────────────────────────────────────────────┐
│                        External Clients                         │
│              (CLI, CI/CD triggers, API consumers)                │
└──────────────────────────┬──────────────────────────────────────┘
                           │ gRPC / REST
┌──────────────────────────▼──────────────────────────────────────┐
│                         API Server                              │
│           (Workflow submission, status, results)                 │
└──────────────────────────┬──────────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────────┐
│                    Control Plane (Operators)                     │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────────────────┐ │
│  │  Workflow     │ │   Sandbox    │ │   Pool                   │ │
│  │  Controller   │ │  Controller  │ │   Controller (Autoscaler)│ │
│  └──────┬───────┘ └──────┬───────┘ └──────────┬───────────────┘ │
│         │                │                     │                 │
│  ┌──────▼───────┐ ┌──────▼───────┐ ┌──────────▼───────────────┐ │
│  │  Task        │ │   Session    │ │   Agent                  │ │
│  │  Controller  │ │  Controller  │ │   Registry               │ │
│  └──────────────┘ └──────────────┘ └──────────────────────────┘ │
└──────────────────────────┬──────────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────────┐
│                      Data Plane                                  │
│  ┌────────────────────────────────────────────────────────────┐  │
│  │  Sandbox Pods                                              │  │
│  │  ┌────────────────────────┐ ┌────────────────────────┐    │  │
│  │  │ Sandbox Agent SDK      │ │ Sandbox Agent SDK      │    │  │
│  │  │  + Bridge Sidecar (Go) │ │  + Bridge Sidecar (Go) │    │  │
│  │  │  + Agent Process       │ │  + Agent Process       │    │  │
│  │  │  + PV                  │ │  + PV                  │    │  │
│  │  └────────────────────────┘ └────────────────────────┘    │  │
│  └────────────────────────────────────────────────────────────┘  │
└──────────────────────────┬──────────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────────┐
│                   Infrastructure Layer                           │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌───────────────────┐  │
│  │ Container│ │   CSI    │ │   CNI    │ │  Event Bus        │  │
│  │ Runtime  │ │ (Volumes)│ │(Network) │ │  (NATS/CloudEvts) │  │
│  └──────────┘ └──────────┘ └──────────┘ └───────────────────┘  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  ToolHive (MCP Tool Layer)                                │   │
│  │  vMCP Services ← MCPGroups ← MCPServer pods              │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

## Component Descriptions

### API Server
A Go service exposing gRPC and REST (via grpc-gateway) endpoints for external clients. Responsibilities:
- Accept workflow and task submissions
- Query status and results
- Stream session events (via gRPC server-streaming or WebSocket)
- Authenticate and authorize requests

The API server is stateless — all state lives in Kubernetes resources and the event bus.

### Control Plane Operators

All operators are written in Go using [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) (the same library underpinning kubebuilder and operator-sdk).

| Operator | Watches | Reconciles |
|----------|---------|------------|
| **Workflow Controller** | `Workflow` CRs | Decomposes workflows into `Task` CRs, manages DAG execution order |
| **Task Controller** | `Task` CRs | Claims a sandbox from the pool, creates a `Session` CR |
| **Sandbox Controller** | `Sandbox` CRs | Manages pod lifecycle, volume provisioning, network policy |
| **Session Controller** | `Session` CRs | Invokes the bridge sidecar, streams events, captures results |
| **Pool Controller** | `Pool` CRs | Autoscales sandboxes based on demand and pool configuration |

### Agent Registry
A configuration store (`AgentConfig` CRs) mapping agent types to their container images and configuration. Each entry specifies the Sandbox Agent SDK version and bridge sidecar image for that agent type. See [spec 06](06-agent-adapter.md) for details.

### Data Plane
The data plane consists of sandbox pods running actual agent workloads. Each sandbox pod contains:
- **Init container**: Sets up the working directory (clone repo, restore cache)
- **Sandbox Agent SDK container**: Runs the [Sandbox Agent SDK](https://sandboxagent.dev/) (Rust binary), which manages the agent process (Claude Code, Codex, Pi, etc.) and exposes an HTTP API for session control, process management, and filesystem operations
- **Bridge sidecar container** (Go): Connects the SDK to the control plane — forwards events to NATS, manages credential injection, updates K8s CRD status

### Event Bus
NATS JetStream provides durable, ordered event delivery for:
- Session event streaming (tool calls, outputs, errors)
- Task status updates
- Workflow progress notifications
- Observability data forwarding

We chose NATS because it is a CNCF incubating project with excellent Go support, low latency, and built-in persistence via JetStream.

## Deployment Topology

### Minimal (Development)
- Single Kubernetes cluster (kind, k3s, or minikube)
- In-cluster NATS
- Local PVs for sandbox storage
- Single-replica operators

### Production
- Multi-node Kubernetes cluster
- NATS cluster (3+ nodes)
- CSI-backed storage (e.g., EBS, Ceph, Longhorn)
- HA operator replicas with leader election
- Ingress controller for API access
- Prometheus + Grafana for monitoring

## Technology Stack

| Component | Technology | Rationale |
|-----------|------------|-----------|
| Language | Go | Kubernetes ecosystem native, excellent concurrency |
| Operators | controller-runtime | Standard for K8s operators, used by kubebuilder |
| API | gRPC + grpc-gateway | Typed contracts, streaming, auto-generated REST |
| Event bus | NATS JetStream | CNCF project, Go-native, durable streams |
| Observability | OpenTelemetry | CNCF standard for metrics, logs, traces |
| Container runtime | containerd | CNCF graduated, standard K8s runtime |
| Storage | CSI-compatible | Pluggable persistent volumes |
| Networking | CNI + NetworkPolicy | Standard K8s network isolation |
| MCP tools | ToolHive (optional) | K8s-native MCP server management, vMCP aggregation, tool-level RBAC |
| Agent adapter | Sandbox Agent SDK | Universal agent interface (6 agents), runs as Rust binary in sandbox |
| CI/CD | Tekton (optional) | CNCF project for pipeline integration |

## Key Design Decisions

### DD1: Operators over Custom Schedulers
We use the Kubernetes operator pattern rather than building a custom scheduler. Operators leverage existing K8s scheduling for pod placement while adding domain-specific reconciliation logic. This reduces complexity and benefits from K8s ecosystem tooling (kubectl, Lens, etc.).

### DD2: Adopt Sandbox Agent SDK, Don't Rebuild
Rather than building our own agent adapter framework in Go, we adopt the [Sandbox Agent SDK](https://sandboxagent.dev/) as the in-sandbox runtime. It already supports 6 agents (Claude Code, Codex, Pi, OpenCode, Amp, Cursor) with a normalized HTTP API. We build a thin Go bridge sidecar that connects it to our K8s control plane (NATS, CRDs, credential proxy). This saves months of adapter development and gives us desktop runtime support (for GUI agents like Cursor) that we'd otherwise punt on.

### DD3: CRDs as the Source of Truth
All system state is represented as Kubernetes Custom Resources. This provides:
- Declarative state management via the K8s API
- Built-in etcd-backed persistence
- Watch-based event notification
- Standard RBAC and admission control

### DD4: Event Sourcing for Sessions
Session events are published to NATS as an append-only stream. The Session CR stores a reference to the stream, not the events themselves. This keeps etcd lean while enabling replay, auditing, and real-time streaming.
