---
name: spec-audit
description: Audit spec documents against implementation for a given topic
user-invocable: true
---

Audit the architecture spec against the current implementation for the topic the user provides (e.g., "session completion", "credential injection", "permission gating").

Steps:

1. Read `spec/README.md` to identify which spec documents are relevant to the topic.
2. Read each relevant spec document thoroughly. Key documents:
   - `spec/04-control-plane.md` — CRDs, controller behaviors, API endpoints
   - `spec/06-agent-adapter.md` — Bridge sidecar, SDK integration, event normalization
   - `spec/07-orchestration-engine.md` — Workflow DAG, task execution, approval
   - `spec/08-observability-and-events.md` — NATS streams, metrics, webhooks
   - `spec/09-security-model.md` — Isolation, secrets, RBAC, network policies
3. Read the corresponding implementation files (controllers, bridge, API server, event types).
4. For each area, report:

| Area | Spec Says | Code Does | Gap |
|------|-----------|-----------|-----|
| ... | ... | ... | ... |

5. Separate the gaps into two categories:
   - **User-facing gaps** — things that affect what a user sees or can do
   - **Internal gaps** — implementation details, optimizations, or future work

6. For each user-facing gap, assess severity: does it block usage, degrade the experience, or is it a missing nice-to-have?
