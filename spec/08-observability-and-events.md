# 08 — Observability and Events

**Status:** DRAFT

## Overview

Every agent session, task, and workflow produces structured telemetry. Observability is not an afterthought — it is a core system capability that enables debugging, cost tracking, auditing, and operational excellence.

## Event Architecture

### Event Flow

```
Agent Process
    │
    ▼ (native events)
Sandbox Agent SDK → Bridge Sidecar (Event Normalizer)
    │
    ▼ (normalized events)
NATS JetStream
    │
    ├──► Session Controller (status updates → K8s CRs)
    ├──► Event Store (persistence for replay)
    ├──► OpenTelemetry Collector (metrics, traces)
    └──► External consumers (webhooks, Slack, dashboards)
```

### NATS Stream Design

Events are published to NATS JetStream with subject-based routing:

```
events.{tenant}.sessions.{session-id}    # Per-session events
events.{tenant}.tasks.{task-id}          # Task lifecycle events
events.{tenant}.workflows.{workflow-id}  # Workflow lifecycle events
```

Stream configuration:
- **Retention**: Limits-based (max age: 30 days, max bytes: configurable per tenant)
- **Replicas**: 3 (for HA in production)
- **Storage**: File-based (survives NATS restarts)
- **Deduplication**: Enabled (window: 2 minutes)

### Consumer Groups

| Consumer | Purpose | Delivery |
|----------|---------|----------|
| `session-controller` | Updates Session CR status | Queue group (load-balanced) |
| `event-store` | Persists events to long-term storage | Single consumer |
| `otel-exporter` | Exports metrics and traces to OTel Collector | Queue group |
| `webhook-dispatcher` | Fires external webhooks on key events | Queue group |
| `api-streamer` | Serves SSE to API clients | Per-subscription |

## Metrics

All metrics are exported via OpenTelemetry and scraped by Prometheus.

### System Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `factory_sandboxes_total` | Gauge | pool, phase | Sandboxes by pool and lifecycle phase |
| `factory_sandbox_startup_seconds` | Histogram | pool, warmup_type | Time to sandbox readiness |
| `factory_tasks_total` | Counter | pool, status | Tasks by completion status |
| `factory_task_duration_seconds` | Histogram | pool, agent_type | Task wall-clock time |
| `factory_sessions_active` | Gauge | agent_type | Currently active sessions |
| `factory_workflows_total` | Counter | status | Workflows by completion status |
| `factory_workflow_duration_seconds` | Histogram | — | Workflow wall-clock time |

### Cost Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `factory_tokens_total` | Counter | agent_type, model, direction | Tokens consumed (input/output) |
| `factory_token_cost_dollars` | Counter | agent_type, model | Estimated cost in USD |
| `factory_compute_cost_seconds` | Counter | pool | CPU-seconds consumed by sandboxes |

### Agent Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `factory_tool_calls_total` | Counter | agent_type, tool | Tool invocations by type |
| `factory_tool_duration_seconds` | Histogram | agent_type, tool | Tool execution time |
| `factory_tool_errors_total` | Counter | agent_type, tool | Failed tool invocations |
| `factory_agent_errors_total` | Counter | agent_type, error_type | Agent-level errors |

## Distributed Tracing

Each workflow execution produces a trace with the following span hierarchy:

```
Workflow: implement-auth-feature (trace root)
├── Task: design-api
│   └── Session: sess-001
│       ├── tool.call: bash (go test)
│       ├── tool.call: file.write (handler.go)
│       └── tool.call: bash (go build)
├── Task: implement-handlers (parallel)
│   └── Session: sess-002
│       └── ...
├── Task: implement-storage (parallel)
│   └── Session: sess-003
│       └── ...
└── Task: write-tests
    └── Session: sess-004
        └── ...
```

Trace context is propagated:
- Workflow → Task: via `Task` CR annotations
- Task → Session: via session config headers
- Session → NATS events: via W3C traceparent header in event metadata

### Integration

The OpenTelemetry Collector receives spans from:
- Control plane operators (Go instrumentation via `go.opentelemetry.io/otel`)
- Bridge sidecar processes (span per tool call)
- NATS event metadata (correlated by trace ID)

Exporters: Jaeger, Tempo, or any OTLP-compatible backend.

## Logging

### Structured Logging

All components emit structured JSON logs:

```json
{
  "level": "info",
  "ts": "2026-03-27T10:30:15.123Z",
  "logger": "task-controller",
  "msg": "task scheduled",
  "workflow": "implement-auth-feature",
  "task": "design-api",
  "sandbox": "claude-code-pool-abc123",
  "traceId": "abc123def456"
}
```

Log library: `go.uber.org/zap` (standard in the K8s operator ecosystem).

### Log Levels

| Level | Usage |
|-------|-------|
| `error` | Failures requiring operator attention |
| `warn` | Degraded behavior, approaching limits |
| `info` | Key lifecycle events (task started, session completed) |
| `debug` | Detailed internal state (scheduling decisions, event processing) |

## Session Replay

Session events stored in NATS (and optionally in long-term storage) enable full replay of agent sessions:

### Replay API

```
GET /v1/sessions/{id}/replay
Accept: text/event-stream

# Returns the same SSE stream as the live session,
# but replayed from stored events.
# Supports ?speed=2x for faster replay.
```

### Storage Backend Options

| Backend | Use Case | Retention |
|---------|----------|-----------|
| NATS JetStream | Hot storage, recent sessions | 7-30 days |
| S3/MinIO | Cold storage, compliance | Configurable (months/years) |
| PostgreSQL | Queryable storage, analytics | Configurable |

An event archiver CronJob moves events from NATS to cold storage based on age.

## Alerting

### Built-in Alert Rules (Prometheus)

```yaml
# Task failure rate exceeding threshold
- alert: HighTaskFailureRate
  expr: rate(factory_tasks_total{status="failed"}[15m]) / rate(factory_tasks_total[15m]) > 0.3
  for: 5m
  labels:
    severity: warning

# Pool running out of sandboxes
- alert: PoolExhausted
  expr: factory_sandboxes_total{phase="ready"} == 0 and factory_sandboxes_total{phase="active"} > 0
  for: 2m
  labels:
    severity: critical

# High token spend rate
- alert: HighTokenSpendRate
  expr: rate(factory_token_cost_dollars[1h]) > 100
  for: 10m
  labels:
    severity: warning
```

## Webhooks

External systems can subscribe to workflow and task events:

```yaml
apiVersion: factory.example.com/v1alpha1
kind: WebhookSubscription
metadata:
  name: slack-notifications
spec:
  events:
    - workflow.completed
    - workflow.failed
    - task.failed
  endpoint: https://hooks.slack.com/services/...
  secret:
    secretRef:
      name: webhook-signing-key
  filter:
    namespaces: [team-alpha]
```

Webhooks are delivered with HMAC signatures for verification and include exponential backoff retry on failure.
