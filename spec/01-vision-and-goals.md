# 01 — Vision and Goals

**Status:** DRAFT

## Vision

Build a Kubernetes-native platform that orchestrates fleets of AI agents in stateful, isolated sandboxes. The platform treats agents as a first-class workload type — similar to how Kubernetes treats containers — providing primitives for lifecycle management, scheduling, observability, and coordination.

The system is **agent-agnostic**: it does not embed any specific agent. Instead, it provides a universal adapter interface (via the [Sandbox Agent SDK](https://sandboxagent.dev/)) that supports any agent harness (Claude Code, Codex, Pi, Aider, etc.), allowing operators to choose — or mix — agents based on the task at hand.

## Goals

### G1: Universal Agent Runtime
Support multiple agent harnesses through a common adapter interface. Operators should be able to swap agents without changing their orchestration logic. Initially target: Claude Code, Codex, and Pi.

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

### UC1: Fleet Task Execution
An organization submits a complex goal (feature specification, research brief, data migration plan). The system decomposes it into tasks, assigns each to an agent, and coordinates results. Human reviewers approve final outputs. Example: an engineering team decomposes a feature spec into implementation subtasks across a pool of coding agents.

### UC2: Custom Tool-Using Agents
A platform team builds domain-specific agents that use MCP tools to interact with internal systems (databases, APIs, dashboards). The orchestration system manages their lifecycle and provides sandboxed execution.

### UC3: Event-Triggered Agents
Agents are triggered by external events (webhooks, schedules, CI signals) and run within the orchestration system to perform tasks autonomously. Examples: a coding agent responds to CI failures with fixes, a personal assistant agent processes incoming emails, or a monitoring agent triages alerts.

### UC4: Batch Processing
A large-scale migration or refactoring task is split across hundreds of repositories. The system schedules agents across the fleet, throttling concurrency to respect API rate limits and cluster resources.

## Non-Goals

- **Building an agent.** We orchestrate existing agents; we don't build one.
- **Replacing Kubernetes.** We extend Kubernetes, not replace it.
- **LLM hosting.** We call LLM APIs; we don't serve models. (GPU scheduling for self-hosted models is a future consideration but out of scope for v1.)
- **General-purpose workflow engine.** The orchestration engine is purpose-built for agent workloads. Use Argo Workflows or Tekton for generic CI/CD pipelines.
- **UI/Frontend.** v1 is API-first. A web UI may follow but is not part of this spec.
