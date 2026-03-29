# 02 — Concepts and Terminology

**Status:** DRAFT

This document defines the core domain model and key abstractions used throughout the specification. Understanding these concepts is a prerequisite for the architecture and subsystem documents.

## Core Concepts

### Agent
An AI-powered program that can perform tasks by reasoning, using tools, and producing artifacts. Agents are opaque to the system — the platform manages their lifecycle but does not control their internal reasoning. Examples: Claude Code, Codex, Pi.

### Harness (Industry Term)
In industry usage, a *harness* refers to the complete runtime wrapping an LLM that makes it a functional agent — the tools, context management, feedback loops, and execution environment. **Claude Code, Codex, and Pi are all agent harnesses.** We do not use this term as a system concept in our platform, but reference it for alignment with industry terminology.

### Adapter
The translation layer (provided by the [Sandbox Agent SDK](https://sandboxagent.dev/)) that normalizes different agent harnesses behind a universal HTTP API. The SDK handles the per-agent differences; our platform consumes the SDK's unified interface via a bridge sidecar.

Think of it as: **The agent harness is the car. The adapter (SDK) is the OBD port that lets any diagnostic tool talk to any car.**

### Sandbox
An isolated, stateful execution environment in which an agent runs. A sandbox consists of:
- A **container** (or set of containers) providing process and filesystem isolation
- A **persistent volume** holding the working directory, cloned repos, installed dependencies
- **Network policies** controlling egress
- **Injected credentials** (API keys, git tokens)

Sandboxes can be **warm** (pre-provisioned with common dependencies) or **cold** (created on demand).

### Session
A single interaction between the platform and an agent within a sandbox. A session has:
- A **prompt** (the task instruction)
- An **event stream** (tool calls, outputs, errors, messages)
- A **result** (success/failure, artifacts produced)
- **Metadata** (token usage, duration, cost)

Sessions are immutable once completed and can be replayed for debugging.

### Task
A unit of work assigned to an agent. Tasks are defined by:
- A **specification** (natural language instruction, context files, constraints)
- **Inputs** (artifacts from upstream tasks, configuration)
- **Expected outputs** (files modified, reports generated, API calls made)
- **Resource requirements** (sandbox size, timeout, agent type preference)

### Workflow
A directed acyclic graph (DAG) of tasks with dependency edges. Workflows enable multi-agent coordination:

```
         ┌──── Task B ────┐
Task A ──┤                 ├── Task D
         └──── Task C ────┘
```

Tasks B and C run in parallel after A completes. Task D waits for both B and C.

### Pool
A set of pre-provisioned sandboxes ready to accept tasks. Pools are defined by:
- **Sandbox template** (base image, pre-installed tools, volume size)
- **Scale bounds** (min/max replicas)
- **Agent type** (via `AgentConfig` reference)

Pools amortize sandbox startup cost by keeping warm instances available.

### Tenant
An isolation boundary for multi-tenant deployments. Each tenant maps to a Kubernetes namespace and has:
- Dedicated resource quotas
- Isolated network policies
- Separate secret stores
- Independent RBAC

## Resource Hierarchy

```
Tenant (Namespace)
├── Pool
│   └── Sandbox (0..N)
│       └── Session (0..N)
├── Workflow
│   └── Task (1..N)
└── AgentConfig
```

## Lifecycle States

### Sandbox Lifecycle

```
Creating → Ready → Assigned → Active → Idle → [Assigned | Terminating]
                                                        ↓
                                                   Terminated
```

| State | Description |
|-------|-------------|
| Creating | Container and volume being provisioned |
| Ready | Sandbox is warm and available in the pool |
| Assigned | Sandbox has been claimed by a task but session not yet started |
| Active | An agent session is running |
| Idle | Session completed; sandbox awaits reuse or cleanup |
| Terminating | Sandbox is being torn down |
| Terminated | Resources released |

### Task Lifecycle

```
Pending → Scheduled → Running → [Succeeded | Failed | Cancelled]
```

| State | Description |
|-------|-------------|
| Pending | Task is waiting for dependencies or scheduling |
| Scheduled | A sandbox has been assigned |
| Running | Agent session is active |
| Succeeded | Agent completed the task successfully |
| Failed | Agent failed or timed out |
| Cancelled | Task was explicitly cancelled |

## Key Relationships

- A **Workflow** contains one or more **Tasks** connected by dependency edges.
- A **Task** runs in exactly one **Sandbox** (but a Sandbox may be reused across Tasks).
- A **Sandbox** belongs to a **Pool** and runs one **Session** at a time.
- An **AgentConfig** is configured per-Pool (all sandboxes in a pool use the same agent type).
- A **Tenant** owns Pools, Workflows, and all resources within its namespace.
