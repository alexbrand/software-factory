# 10 — Prior Art

**Status:** DRAFT

## Overview

This document analyzes existing projects that solve related problems. Each is evaluated for what we can learn, what we might adopt, and where our requirements diverge.

## Sandbox Agent SDK

**Source**: [sandboxagent.dev](https://sandboxagent.dev/) / [GitHub](https://github.com/rivet-dev/sandbox-agent)

### What It Does

A Rust-based universal control layer for coding agents. It wraps six agents (Claude Code, Codex, OpenCode, Cursor, Amp, Pi) behind a single HTTP/SSE API with a normalized event schema. Runs inside sandbox environments from various providers (E2B, Daytona, Modal, Docker, etc.).

### What We Should Adopt

| Concept | How It Applies |
|---------|---------------|
| **Universal session schema** | Our normalized event schema (spec 06) follows the same principle. Sandbox Agent SDK proves that normalizing across diverse agents is viable. |
| **Agent-swappable architecture** | The "write once, swap agents" model validates our Harness interface design. |
| **Credential extraction** | Their `credentials extract-env` approach — pulling API keys from local agent configs — is useful for development/testing workflows. |
| **Inspector UI** | A built-in debug UI for session inspection is valuable. We should consider this for a future iteration. |

### Where We Diverge

| Aspect | Sandbox Agent SDK | Our System |
|--------|-------------------|------------|
| **Orchestration** | None — single-agent sessions only | Multi-agent DAG workflows |
| **Scheduling** | Relies on external sandbox providers | Kubernetes-native scheduling and pool management |
| **Statefulness** | Ephemeral sessions; persistence is external | Stateful sandboxes with PV-backed workspaces |
| **Language** | Rust | Go (Kubernetes ecosystem alignment) |
| **Scope** | Agent adapter library | Full orchestration platform |

### Potential Integration

The Sandbox Agent SDK could serve as a **reference implementation** for our harness adapters. If we find that maintaining agent-specific adapters in Go is too burdensome, we could run the Sandbox Agent SDK binary inside our harness container and bridge to it via its HTTP API. This would give us instant support for all six agents at the cost of an additional process per sandbox.

---

## Cloudflare Dynamic Workers

**Source**: [developers.cloudflare.com/dynamic-workers](https://developers.cloudflare.com/dynamic-workers/)

### What It Does

A Cloudflare runtime feature that spins up V8 isolates at runtime to execute LLM-generated code. Dynamic Workers start in milliseconds, use minimal memory, and are sandboxed via V8's isolate model with additional hardware-level protections.

### What We Should Adopt

| Concept | How It Applies |
|---------|---------------|
| **Capability-based security via bindings** | Dynamic Workers have zero capabilities by default; the parent explicitly grants access. Our credential proxy and network policies follow the same principle — deny-all default, explicit allowlisting. |
| **`globalOutbound` for credential safety** | The parent intercepts outbound HTTP requests and injects credentials. This directly inspired our credential proxy sidecar design (spec 09). |
| **Code Mode** | The idea of having an LLM generate a program rather than making sequential tool calls is powerful. While not directly applicable to our orchestration model, it's relevant for custom tool-using agents (UC2). |
| **Virtual filesystem** | Their `@cloudflare/shell` package provides a transactional virtual filesystem. Our PV-backed workspace is the Kubernetes equivalent, but we should consider supporting similar file operation APIs in the harness. |

### Where We Diverge

| Aspect | Dynamic Workers | Our System |
|--------|----------------|------------|
| **Isolation model** | V8 isolates (process-level) | Container/VM isolation (OS-level) |
| **Startup time** | Milliseconds | Seconds (warm) to minutes (cold) |
| **Workload type** | Short-lived code execution | Long-running agent sessions |
| **State model** | Stateless (virtual FS backed by external storage) | Stateful (persistent volumes) |
| **Platform** | Cloudflare edge network | Kubernetes (any infrastructure) |
| **Language support** | JavaScript/TypeScript only | Any language (container-based) |

### Key Insight

Dynamic Workers optimize for **latency and density** (100x faster, 10-100x more memory efficient). Our system optimizes for **capability and statefulness** (full OS, persistent filesystems, long-running processes). These are complementary models for different parts of the agent spectrum. For lightweight tool-execution tasks, a Dynamic Workers-style approach could be a future optimization.

---

## Pi (pi-mono)

**Source**: [github.com/badlogic/pi-mono](https://github.com/badlogic/pi-mono)

### What It Does

A TypeScript monorepo providing a minimal coding agent (`pi-coding-agent`), a multi-provider LLM API (`pi-ai`), GPU infrastructure management (`pi-pods`), and extension/skill frameworks. The coding agent operates in four modes: interactive terminal, print/JSON output, RPC over stdin/stdout, and SDK embedding.

### What We Should Adopt

| Concept | How It Applies |
|---------|---------------|
| **RPC over stdin/stdout** | Pi's JSONL-framed RPC mode is ideal for harness integration. This is the cleanest agent control interface we've seen and directly maps to our Harness interface. |
| **Steering messages** | Messages queued and delivered after tool execution completes. This enables mid-task redirection without interrupting the agent's current operation. We adopted this in our Session interface (spec 06). |
| **Session tree with branching** | Sessions stored with `id`/`parentId` enabling branching. Useful for exploring alternative approaches to a task. We should consider this for advanced workflow patterns. |
| **Extension system** | Pi's minimal core + extension model (skills, extensions, themes) demonstrates how to keep the agent harness thin while enabling customization. Our HarnessConfig should support extension loading. |
| **Context file hierarchy** | `AGENTS.md`/`CLAUDE.md` loaded from home and parent directories. Our harness should write task context to these standard files rather than inventing a new mechanism. |
| **Automatic compaction** | Proactive context compaction on overflow. Long-running agent sessions in our system will hit context limits — the harness should support compaction strategies. |

### Where We Diverge

| Aspect | Pi | Our System |
|--------|-----|------------|
| **Sandboxing** | Explicitly none — user provides isolation | First-class sandboxing with containers, network policies, credential isolation |
| **Orchestration** | Single-agent only; orchestration via extensions | Built-in multi-agent DAG orchestration |
| **Scheduling** | None (runs locally or on GPU pods) | Kubernetes-native pool and sandbox scheduling |
| **Language** | TypeScript | Go (with TypeScript for agent-specific adapters if needed) |
| **LLM API** | Built-in multi-provider abstraction | Out of scope — agents bring their own LLM access |

### Potential Integration

Pi's RPC mode makes it an excellent first-class citizen in our system. The Pi harness adapter can communicate via stdin/stdout JSONL, getting native steering support and clean event normalization. Pi's `pi-ai` library could also be useful if we ever need to build meta-agents that orchestrate at the LLM level (e.g., task decomposition agents).

---

## CNCF Projects Under Consideration

### Already Adopted

| Project | Maturity | Usage in Our System |
|---------|----------|---------------------|
| **Kubernetes** | Graduated | Platform foundation |
| **containerd** | Graduated | Container runtime |
| **OpenTelemetry** | Incubating | Metrics, logs, traces |
| **NATS** | Incubating | Event bus (JetStream for persistence) |

### Under Evaluation

| Project | Maturity | Potential Usage | Decision |
|---------|----------|-----------------|----------|
| **Argo Workflows** | Graduated | DAG workflow execution | **Evaluate** — mature DAG engine. Could replace our Workflow Controller entirely. Trade-off: adds dependency but saves development effort. See analysis below. |
| **Tekton** | Graduated | CI/CD pipeline integration | **Use alongside** — for CI/CD triggers (UC3), not for agent orchestration. |
| **Kyverno** | Incubating | Policy enforcement, image verification | **Adopt** — enforce image signing, pod security standards. |
| **External Secrets Operator** | Incubating | External secret store integration | **Adopt** — for Vault/AWS Secrets Manager integration. |
| **Longhorn** | Incubating | Distributed storage | **Evaluate** — could provide PV storage without cloud-specific CSI drivers. |
| **Crossplane** | Incubating | Infrastructure provisioning | **Skip for v1** — adds complexity for multi-cloud provisioning we don't need yet. |
| **KubeVirt** | Incubating | VM-based sandboxes | **Evaluate** — alternative to gVisor/Kata for stronger isolation. |
| **Dapr** | Incubating | Service invocation, pub/sub | **Skip** — NATS + direct gRPC covers our needs. |

### Argo Workflows: Build vs. Adopt

The Workflow Controller is the most complex operator in our system. Argo Workflows provides a mature, battle-tested DAG engine on Kubernetes. Key considerations:

**Arguments for using Argo Workflows:**
- Mature DAG execution with retry, timeout, conditional logic
- Built-in artifact passing (S3, GCS, MinIO)
- UI for workflow visualization
- Large community, well-documented
- Handles fan-out/fan-in, loops, recursion

**Arguments for building our own:**
- Argo Workflows is generic — our workflows are agent-specific (sessions, harnesses, pools)
- Argo's step containers are short-lived; our sandboxes are long-lived and reusable
- Tight integration with sandbox pools and session management is easier in a custom controller
- Argo introduces significant operational overhead (its own set of CRDs, controllers, database)

**Recommendation**: Build a purpose-built Workflow Controller for v1. Argo's model (one container per step, ephemeral) conflicts with our stateful sandbox model. However, study Argo's DAG execution logic and artifact passing as reference implementations. Revisit if our workflow requirements grow beyond what a custom controller can handle.

---

## Summary Matrix

| Feature | Sandbox Agent SDK | Dynamic Workers | Pi | Our System |
|---------|-------------------|-----------------|-----|------------|
| Multi-agent orchestration | No | No | No | Yes |
| Universal agent interface | Yes (6 agents) | No (JS only) | No (single agent) | Yes |
| Stateful sandboxes | No | No | No | Yes |
| Kubernetes-native | No | No | No | Yes |
| Credential isolation | Partial | Yes (globalOutbound) | No | Yes |
| Event normalization | Yes | No | Partial (RPC events) | Yes |
| Workflow DAGs | No | No | No | Yes |
| Pool autoscaling | No | Yes (Cloudflare-managed) | No | Yes |

Our system fills the gap: **Kubernetes-native multi-agent orchestration with stateful sandboxes and a universal harness interface.** No existing project provides this combination.
