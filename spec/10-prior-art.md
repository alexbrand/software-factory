# 10 — Prior Art

**Status:** DRAFT

## Overview

This document analyzes existing projects that solve related problems. Each is evaluated for what we can learn, what we might adopt, and where our requirements diverge.

## Sandbox Agent SDK

**Source**: [sandboxagent.dev](https://sandboxagent.dev/) / [GitHub](https://github.com/rivet-dev/sandbox-agent)

### What It Does

A Rust-based universal control layer for coding agents. It wraps six agents (Claude Code, Codex, OpenCode, Cursor, Amp, Pi) behind a single HTTP/SSE API with a normalized event schema. Runs inside sandbox environments from various providers (E2B, Daytona, Modal, Docker, etc.).

### Decision: Adopt as In-Sandbox Runtime

After deeper analysis of the SDK's OpenAPI spec, we decided to **adopt the Sandbox Agent SDK as the in-sandbox agent runtime** rather than building our own adapter layer. See [spec 06](06-agent-harness-interface.md) for the full rationale and integration design.

### API Surface (from OpenAPI spec)

The SDK provides a comprehensive HTTP API on port 2468:

| Endpoint Group | Capabilities |
|---------------|-------------|
| `/v1/agents` | List, get, install agents |
| `/v1/acp` | Agent Control Protocol — JSON-RPC + SSE streaming for session control |
| `/v1/processes` | Full process lifecycle, stdin/PTY, logs, WebSocket terminal |
| `/v1/desktop/*` | Xvfb/openbox stack: screenshots, keyboard/mouse, video recording, WebRTC |
| `/v1/fs` | Read, write, stat, mkdir, move, delete, batch upload |
| `/v1/config` | MCP servers, skills configuration |
| `/v1/health` | Health check |

This is far more capable than what we'd build ourselves. Key features we get for free:
- **6 agent adapters** with normalized session control
- **Desktop runtime** for GUI agents (Cursor) — we'd otherwise punt on this entirely
- **Process management** with PTY and WebSocket terminal access
- **Filesystem API** for workspace preparation and artifact extraction
- **OpenAPI spec** enabling type-safe Go client generation

### What the SDK Does NOT Provide (and We Build)

| Concern | Our Responsibility |
|---------|-------------------|
| Multi-agent orchestration | Workflow/Task controllers, DAG engine |
| Stateful sandbox lifecycle | PVs, snapshots, warmup, pool autoscaling |
| Event pipeline | Bridge sidecar: SDK SSE → NATS JetStream |
| Credential isolation | Bridge sidecar: HTTP proxy with secret injection |
| Kubernetes integration | CRDs, operators, RBAC, network policies |
| Observability | OpenTelemetry, metrics, tracing, alerting |

### Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| SDK development stalls | Apache 2.0 license — we can fork. Self-contained Rust binary. |
| API breaking changes | Pin version per HarnessConfig CR. Generated Go client catches breaks at build time. |
| Missing agent support | Contribute upstream, or use `/v1/processes` as a fallback for raw process management. |
| Rust binary is opaque | We only consume its HTTP API — no need to modify internals. |

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
| Universal agent interface | Yes (6 agents) | No (JS only) | No (single agent) | Yes (via SDK) |
| Stateful sandboxes | No | No | No | Yes |
| Kubernetes-native | No | No | No | Yes |
| Credential isolation | Partial | Yes (globalOutbound) | No | Yes |
| Event normalization | Yes | No | Partial (RPC events) | Yes (via SDK + bridge) |
| Workflow DAGs | No | No | No | Yes |
| Pool autoscaling | No | Yes (Cloudflare-managed) | No | Yes |

Our system fills the gap: **Kubernetes-native multi-agent orchestration with stateful sandboxes.** We adopt the Sandbox Agent SDK for the agent interface layer rather than rebuilding it, and focus our engineering effort on what no existing project provides: the K8s control plane, stateful sandbox lifecycle, DAG orchestration, and observability pipeline.
