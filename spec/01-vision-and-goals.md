# 01 — Vision and Goals

**Status:** DRAFT

## Vision

Build a Kubernetes-native platform that orchestrates fleets of AI agents in stateful, isolated sandboxes. The platform treats agents as a first-class workload type — similar to how Kubernetes treats containers — providing primitives for lifecycle management, scheduling, observability, and coordination.

The system is **agent-agnostic**: it does not embed any specific coding agent. Instead, it provides a universal harness interface that adapts to any agent runtime (Claude Code, Codex, Pi, Aider, etc.), allowing operators to choose — or mix — agents based on the task at hand.

## Goals

### G1: Universal Agent Runtime
Support multiple coding agent harnesses through a common adapter interface. Operators should be able to swap agents without changing their orchestration logic. Initially target: Claude Code, Codex, and Pi.

### G2: Stateful Sandboxes
Provide persistent, isolated execution environments where agents can work across sessions without re-cloning repositories or reinstalling dependencies. Sandbox state (filesystem, installed packages, running processes) survives agent restarts and can be snapshotted/restored.

### G3: Kubernetes-Native
Express all system concepts as Kubernetes Custom Resources. Use the operator pattern for reconciliation. Leverage existing CNCF projects (containerd, CSI, CNI, NATS/CloudEvents, OpenTelemetry) rather than reinventing infrastructure.

### G4: Fleet Orchestration
Enable coordinating multiple agents working on related tasks — for example, decomposing a feature into subtasks and assigning them to a pool of agents, then collecting and integrating results. Support DAG-based workflows with dependencies, fan-out/fan-in, and conditional branching.

### G5: Observability by Default
Every agent session produces a structured event stream. Sessions are replayable. Costs (tokens, compute, time) are tracked per task. Integrate with OpenTelemetry for metrics, logs, and traces.

### G6: Secure Multi-Tenancy
Isolate tenants at the namespace level. Agents run in sandboxed containers with restricted capabilities. Secrets (API keys, tokens) are injected securely and never exposed to agent context. Network policies limit agent egress.

## Use Cases

### UC1: Fleet Software Development
An engineering team submits a feature specification. The system decomposes it into implementation tasks, assigns each to a coding agent, and coordinates integration. Human reviewers approve merge-ready results.

### UC2: Custom Tool-Using Agents
A platform team builds domain-specific agents that use MCP tools to interact with internal systems (databases, APIs, dashboards). The orchestration system manages their lifecycle and provides sandboxed execution.

### UC3: CI/CD Agent Integration
Coding agents are triggered by CI events (PR opened, review requested, test failure) and run within the orchestration system to suggest fixes, write tests, or perform code review.

### UC4: Batch Processing
A large-scale migration or refactoring task is split across hundreds of repositories. The system schedules agents across the fleet, throttling concurrency to respect API rate limits and cluster resources.

## Non-Goals

- **Building a coding agent.** We orchestrate existing agents; we don't build one.
- **Replacing Kubernetes.** We extend Kubernetes, not replace it.
- **LLM hosting.** We call LLM APIs; we don't serve models. (GPU scheduling for self-hosted models is a future consideration but out of scope for v1.)
- **General-purpose workflow engine.** The orchestration engine is purpose-built for agent workloads. Use Argo Workflows or Tekton for generic CI/CD pipelines.
- **UI/Frontend.** v1 is API-first. A web UI may follow but is not part of this spec.
