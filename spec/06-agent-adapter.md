# 06 — Agent Adapter Layer

**Status:** DRAFT

## Overview

The adapter layer provides the interface between the orchestration control plane and the agent harness running inside a sandbox. Rather than building our own agent adapter framework, we adopt the [Sandbox Agent SDK](https://sandboxagent.dev/) as the in-sandbox runtime and build a thin Go bridge that connects it to our Kubernetes control plane.

### Why Sandbox Agent SDK

The Sandbox Agent SDK already solves the hardest part of this problem: normalizing across diverse agent harnesses. Building our own would mean:

- Writing and maintaining individual adapters for Claude Code, Codex, Pi, OpenCode, Amp, Cursor (and future agents)
- Designing a session protocol, event schema, and process management layer
- Building desktop runtime support for GUI-based agents like Cursor

The SDK provides all of this as a single Rust binary with an OpenAPI-defined HTTP API. It is Apache 2.0 licensed, actively maintained (~1.2k stars, 18 contributors, frequent releases), and purpose-built for exactly this use case.

**What we build:** A Go bridge sidecar that connects the Sandbox Agent SDK's HTTP API to our Kubernetes control plane (NATS event bus, CRD status updates, credential injection).

**What we adopt:** The Sandbox Agent SDK binary running inside each sandbox pod, managing the actual agent process.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  Sandbox Pod                                                     │
│                                                                  │
│  ┌──────────────────────────────┐  ┌──────────────────────────┐ │
│  │  Sandbox Agent SDK (Rust)     │  │  Bridge Sidecar (Go)     │ │
│  │                               │  │                          │ │
│  │  ┌─────────┐ ┌─────────────┐ │  │  ┌────────────────────┐ │ │
│  │  │ Agent   │ │ Process     │ │  │  │ SSE Consumer       │ │ │
│  │  │ Control │ │ Management  │ │  │  │ (SDK events →      │ │ │
│  │  │ (ACP)   │ │             │ │  │  │  NATS JetStream)   │ │ │
│  │  ├─────────┤ ├─────────────┤ │  │  ├────────────────────┤ │ │
│  │  │ Desktop │ │ Filesystem  │ │  │  │ Session Manager    │ │ │
│  │  │ Runtime │ │ Operations  │ │  │  │ (K8s CR ↔ SDK API) │ │ │
│  │  ├─────────┤ ├─────────────┤ │  │  ├────────────────────┤ │ │
│  │  │ Config  │ │ Health      │ │  │  │ Credential Proxy   │ │ │
│  │  │ (MCP,   │ │ (/v1/health)│ │  │  │ (injects secrets   │ │ │
│  │  │ Skills) │ │             │ │  │  │  into outbound)    │ │ │
│  │  └─────────┘ └─────────────┘ │  │  └────────────────────┘ │ │
│  │                               │  │                          │ │
│  │  HTTP API on :2468            │  │  gRPC on :8080           │ │
│  └──────────────────────────────┘  └──────────────────────────┘ │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────────┐│
│  │  Agent Process (managed by SDK)                               ││
│  │  claude / codex / pi / opencode / amp / cursor               ││
│  └──────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

## Sandbox Agent SDK API Surface

The SDK exposes a comprehensive HTTP API on port 2468. Key endpoint groups:

### Agent Management (`/v1/agents`)
- `GET /v1/agents` — List available agents with config details
- `GET /v1/agents/{agent}` — Get specific agent info
- `POST /v1/agents/{agent}/install` — Install or configure an agent

### Agent Control Protocol (`/v1/acp`)
- `GET /v1/acp` — List active ACP server instances
- `GET /v1/acp/{server_id}` — SSE stream of ACP envelopes (JSON-RPC)
- `POST /v1/acp/{server_id}` — Send JSON-RPC requests/notifications to agent
- `DELETE /v1/acp/{server_id}` — Close ACP server connection

### Process Management (`/v1/processes`)
- `POST /v1/processes` — Create a managed process
- `POST /v1/processes/{id}/input` — Write to stdin/PTY
- `POST /v1/processes/{id}/stop` — SIGTERM
- `POST /v1/processes/{id}/kill` — SIGKILL
- `GET /v1/processes/{id}/logs` — Fetch logs (supports SSE follow)
- `GET /v1/processes/{id}/terminal/ws` — WebSocket PTY session
- `POST /v1/processes/run` — One-shot command execution

### Desktop Runtime (`/v1/desktop/*`)
Full Xvfb/openbox stack for GUI agents:
- Display management, screenshots, window control
- Keyboard/mouse input
- Video recording and WebRTC streaming

### Filesystem (`/v1/fs`)
- Read, write, stat, mkdir, move, delete files
- Batch upload via tar archive

### Health
- `GET /v1/health` — Service health check

Full OpenAPI spec available from the SDK repository.

## Bridge Sidecar (Go)

The bridge sidecar is the component we build. It is a Go binary that runs alongside the Sandbox Agent SDK in each sandbox pod.

### Responsibilities

1. **Session lifecycle management**: Translates Session CR spec into SDK API calls (create session via ACP, send messages, cancel).
2. **Event forwarding**: Consumes SSE streams from the SDK and publishes normalized events to NATS JetStream.
3. **Credential injection**: Intercepts outbound HTTPS from the agent via HTTP proxy, injecting API keys from Kubernetes secrets.
4. **Status reporting**: Updates Session and Sandbox CR status based on SDK health and session state.
5. **Workspace preparation**: Uses the SDK's filesystem API to stage context files, write `CLAUDE.md`/`AGENTS.md`, and extract task artifacts.

### Bridge Interface (Go)

```go
// Bridge connects the Sandbox Agent SDK to the Kubernetes control plane.
type Bridge struct {
    sdkClient   *sandboxagent.Client  // Generated from OpenAPI spec
    natsConn    *nats.Conn
    k8sClient   client.Client
    credProxy   *CredentialProxy
    sandboxName string
}

// StartSession creates a new agent session via the SDK.
func (b *Bridge) StartSession(ctx context.Context, cfg SessionConfig) (string, error) {
    // 1. Write context files to sandbox filesystem via SDK
    for _, f := range cfg.ContextFiles {
        b.sdkClient.WriteFile(ctx, f.Path, f.Content)
    }

    // 2. Start agent session via ACP
    sessionID, err := b.sdkClient.CreateACPSession(ctx, sandboxagent.ACPConfig{
        Agent:   cfg.AgentType,
        WorkDir: cfg.WorkDir,
    })

    // 3. Send the task prompt
    b.sdkClient.SendACPMessage(ctx, sessionID, cfg.Prompt)

    // 4. Start event forwarding in background
    go b.forwardEvents(ctx, sessionID)

    return sessionID, err
}

// forwardEvents consumes SSE from the SDK and publishes to NATS.
func (b *Bridge) forwardEvents(ctx context.Context, sessionID string) {
    stream := b.sdkClient.StreamACPEvents(ctx, sessionID)
    for event := range stream {
        normalized := b.normalizeEvent(sessionID, event)
        subject := fmt.Sprintf("events.%s.sessions.%s", b.namespace, sessionID)
        b.natsConn.Publish(subject, normalized)
    }
}

// SendMessage sends a follow-up message to an active session.
func (b *Bridge) SendMessage(ctx context.Context, sessionID string, msg string) error {
    return b.sdkClient.SendACPMessage(ctx, sessionID, msg)
}

// CancelSession gracefully stops an active session.
func (b *Bridge) CancelSession(ctx context.Context, sessionID string) error {
    return b.sdkClient.CloseACPSession(ctx, sessionID)
}
```

### SDK Client Generation

We generate a Go client from the Sandbox Agent SDK's OpenAPI spec:

```bash
# Generate Go client from OpenAPI spec
oapi-codegen -package sandboxagent -generate types,client openapi.yaml > pkg/sandboxagent/client.go
```

This gives us type-safe access to all SDK endpoints without maintaining manual HTTP code.

## Event Normalization

The bridge translates SDK ACP events into our normalized event schema before publishing to NATS. The SDK's events arrive as JSON-RPC envelopes over SSE; we map them to our common schema:

```go
type Event struct {
    ID        string          `json:"id"`
    SessionID string          `json:"sessionId"`
    Timestamp time.Time       `json:"timestamp"`
    Type      EventType       `json:"type"`
    Data      json.RawMessage `json:"data"`
    // Preserve the original SDK event for debugging/replay
    Raw       json.RawMessage `json:"raw,omitempty"`
}

type EventType string

const (
    // Session lifecycle
    EventSessionStarted   EventType = "session.started"
    EventSessionCompleted EventType = "session.completed"
    EventSessionFailed    EventType = "session.failed"

    // Agent reasoning
    EventThinking  EventType = "agent.thinking"
    EventMessage   EventType = "agent.message"

    // Tool usage
    EventToolCall   EventType = "tool.call"
    EventToolResult EventType = "tool.result"

    // File operations
    EventFileRead    EventType = "file.read"
    EventFileWrite   EventType = "file.write"
    EventFileEdit    EventType = "file.edit"

    // Shell operations
    EventShellExec   EventType = "shell.exec"
    EventShellOutput EventType = "shell.output"

    // Permission gating (see spec/04 Session CR for lifecycle details)
    EventPermissionRequested  EventType = "session.permission_requested"
    EventPermissionResponded  EventType = "session.permission_responded"

    // Token tracking
    EventTokenUsage EventType = "token.usage"

    // Errors
    EventError EventType = "error"
)
```

We retain the `Raw` field so we can always fall back to the SDK's native event format for debugging or agent-specific analysis.

### Permission Event Data

Permission events carry structured data payloads:

```go
// PermissionRequestData is the Data payload for EventPermissionRequested.
type PermissionRequestData struct {
    PermissionID string `json:"permissionId"` // Unique ID for correlation
    ToolName     string `json:"toolName"`     // e.g. "Bash", "Write", "Edit"
    Title        string `json:"title"`        // Human-readable summary
    // Full tool arguments (command text, file contents, etc.) are included
    // so SSE consumers can render an approval UI without polling etcd.
    Arguments    json.RawMessage `json:"arguments"`
}

// PermissionResponseData is the Data payload for EventPermissionResponded.
type PermissionResponseData struct {
    PermissionID string `json:"permissionId"`
    Decision     string `json:"decision"`  // "allow" or "deny"
    Remember     string `json:"remember"`  // "once", "session", or "always"
    RespondedBy  string `json:"respondedBy,omitempty"` // user or "bridge:autoApprove"
}
```

## Permission Handling in the Bridge

The bridge sidecar handles runtime permission requests differently based on the AgentConfig's `permissionMode`:

### Mode: `bypass`

The bridge calls `session/set_config_option` with `bypassPermissions: true` immediately after creating the ACP session. The agent never emits permission requests. This is the default.

### Mode: `autoApprove`

The bridge does **not** set `bypassPermissions`. When the SDK's SSE stream contains a `session/request_permission` JSON-RPC request:

1. The bridge immediately responds with `allow_always` via `POST /v1/acp/{server_id}`.
2. The bridge publishes an `EventPermissionRequested` event followed by an `EventPermissionResponded` event to NATS (for audit trail).
3. The session stays `Active` — no phase change, no etcd write beyond normal event summaries.

### Mode: `requireApproval`

When the SSE stream contains a `session/request_permission` JSON-RPC request:

1. The bridge publishes an `EventPermissionRequested` event to NATS with full request details.
2. The bridge subscribes to the NATS reply subject `permissions.{session-id}.{permissionId}` and **blocks** (with a configurable timeout, default 1h).
3. When a response arrives on the reply subject, the bridge sends the corresponding JSON-RPC response to the SDK via `POST /v1/acp/{server_id}`.
4. The bridge publishes an `EventPermissionResponded` event to NATS.
5. If the timeout expires with no response, the bridge responds with `deny` and publishes a timeout event.

```go
// handlePermissionRequest processes a permission request from the SDK.
func (b *Bridge) handlePermissionRequest(ctx context.Context, sessionID string, req ACPPermissionRequest) error {
    permID := uuid.NewString()

    // Publish request event to NATS
    b.publishEvent(ctx, sessionID, Event{
        Type: EventPermissionRequested,
        Data: PermissionRequestData{
            PermissionID: permID,
            ToolName:     req.ToolName,
            Title:        req.Title,
            Arguments:    req.Arguments,
        },
    })

    // Wait for approval on reply subject
    replySubject := fmt.Sprintf("permissions.%s.%s", sessionID, permID)
    msg, err := b.natsConn.RequestWithContext(ctx, replySubject, nil)
    if err != nil {
        // Timeout or context cancelled — deny by default
        b.respondToSDK(ctx, sessionID, req.ID, "deny")
        return err
    }

    var decision PermissionResponseData
    json.Unmarshal(msg.Data, &decision)

    // Forward decision to SDK
    b.respondToSDK(ctx, sessionID, req.ID, decision.Decision)

    // Publish response event
    b.publishEvent(ctx, sessionID, Event{
        Type: EventPermissionResponded,
        Data: decision,
    })
    return nil
}
```

## Session Lifecycle

```
Session Controller                Bridge Sidecar              Sandbox Agent SDK
      │                                │                              │
      │  Create Session CR             │                              │
      ├───────────────────────────────►│                              │
      │                                │  Write context files         │
      │                                ├─────────────────────────────►│
      │                                │  POST /v1/acp (create)       │
      │                                ├─────────────────────────────►│
      │                                │  POST /v1/acp/{id} (prompt)  │
      │                                ├─────────────────────────────►│
      │                                │                              │
      │                                │  GET /v1/acp/{id} (SSE)     │
      │                                │◄─────────────────────────────┤
      │  Events via NATS               │                              │
      │◄───────────────────────────────┤  (normalized + forwarded)    │
      │                                │                              │
      │  Send follow-up message        │                              │
      ├───────────────────────────────►│  POST /v1/acp/{id}          │
      │                                ├─────────────────────────────►│
      │                                │                              │
      │  Cancel session                │                              │
      ├───────────────────────────────►│  DELETE /v1/acp/{id}        │
      │                                ├─────────────────────────────►│
      │                                │                              │
      │  Session complete event        │                              │
      │◄───────────────────────────────┤                              │
      │  Update Session CR status      │                              │
```

### Session Completion Flow

```
Session Controller         NATS          Bridge Sidecar          SDK
      │                     │                  │                   │
      │                     │                  │  prompt RPC returns│
      │                     │                  │◄──────────────────┤
      │                     │                  │  (agent finished) │
      │                     │                  │                   │
      │                     │                  │  DELETE /v1/acp/{id}
      │                     │                  ├──────────────────►│
      │                     │                  │  SSE stream closes │
      │                     │                  │◄──────────────────┤
      │                     │                  │                   │
      │                     │  session.completed                   │
      │                     │  {tokenUsage, duration}              │
      │                     │◄─────────────────┤                   │
      │  lifecycle event    │                  │                   │
      │◄────────────────────┤                  │                   │
      │  Update Session CR: │                  │                   │
      │  phase=Completed    │                  │                   │
      │  tokenUsage=...     │                  │                   │
      │                     │                  │                   │
```

**Completion detection in the bridge:**

The ACP protocol keeps sessions open for follow-up messages — the SDK does not close the SSE stream when a prompt completes. The bridge drives session completion differently depending on the session mode:

**Task mode (single-shot):**

1. The bridge sends the prompt via `session/prompt` JSON-RPC in a background goroutine. This blocks until the agent finishes.
2. When the prompt returns successfully, the bridge publishes `session.completed` to NATS and closes the ACP session (`DELETE /v1/acp/{id}`).
3. The bridge removes the session from its active session map.

If the prompt returns with an error, the bridge publishes `session.failed` instead and follows the same cleanup path.

**Interactive mode (multi-turn):**

1. The bridge sends the initial prompt (if provided) via `session/prompt`. When it returns, the session stays open — the bridge does **not** close the ACP session or publish `session.completed`.
2. Follow-up messages arrive via `POST /sessions/{id}/messages` on the bridge HTTP API. Each message calls `session/prompt` again. The SSE stream stays open for the duration, streaming events for every turn.
3. The session closes when:
   - The API server sends a cancel request (`DELETE /sessions/{id}`) → bridge closes ACP session, publishes `session.completed`
   - The idle timeout fires (no messages for `spec.timeout`) → bridge closes ACP session, publishes `session.failed` with `failureReason: Timeout`

**Failure detection in the bridge:**

- If the SDK SSE stream emits a `session.failed` event, the bridge publishes `session.failed` to NATS with the error reason from the event payload, then removes the session.
- If the bridge detects that the session has exceeded its timeout, it cancels the SDK session (sends `DELETE /v1/acp/{id}`), publishes `session.failed` with `failureReason: Timeout`, and removes the session.

**The Session Controller uses NATS lifecycle events as the primary completion signal.** The `/healthz` endpoint serves as a secondary signal, covering cases where NATS is unavailable or the event was missed.

### Permission Request Flow (requireApproval mode)

```
External Client         API Server         NATS          Bridge Sidecar          SDK
      │                     │               │                  │                   │
      │                     │               │                  │  permission_request│
      │                     │               │                  │◄──────────────────┤
      │                     │               │  publish event   │                   │
      │                     │               │◄─────────────────┤                   │
      │                     │               │  (subscribe to   │                   │
      │                     │               │   reply subject)  │                   │
      │                     │  SSE event    │                  │                   │
      │  SSE: permission    │◄──────────────┤                  │                   │
      │  requested          │               │                  │                   │
      │◄────────────────────┤               │                  │                   │
      │                     │               │                  │                   │
      │  POST /permissions  │               │                  │                   │
      │  {decision: allow}  │               │                  │                   │
      ├────────────────────►│  publish to   │                  │                   │
      │                     │  reply subject│                  │                   │
      │                     ├──────────────►│  deliver reply   │                   │
      │                     │               ├─────────────────►│  JSON-RPC response│
      │                     │               │                  ├──────────────────►│
      │                     │               │                  │  agent resumes    │
      │                     │               │                  │                   │
```

## Credential Injection

The bridge sidecar runs an HTTP/HTTPS proxy that intercepts outbound requests from the agent process:

1. The sandbox environment is configured with `HTTP_PROXY`/`HTTPS_PROXY` pointing to the bridge sidecar.
2. The bridge matches outbound requests against credential mappings loaded from Kubernetes secrets.
3. API keys are injected into request headers at the proxy layer.
4. The agent never sees raw credentials.

```go
type CredentialMapping struct {
    Host      string // e.g., "api.anthropic.com"
    Header    string // e.g., "x-api-key"
    SecretRef SecretKeyRef
}
```

This is the same pattern as Cloudflare Dynamic Workers' `globalOutbound` — proven at scale.

## Workspace Preparation

Before starting a session, the bridge uses the SDK's filesystem API to prepare the workspace:

1. **Write context files**: Task specs, `CLAUDE.md`/`AGENTS.md`, configuration.
2. **Stage input artifacts**: Download from object storage, write via `/v1/fs/file`.
3. **Configure MCP tools**: If the Pool defines `mcpServers`, the bridge passes the server endpoints to the SDK when creating the ACP session via the `mcpServers` parameter in the `session/new` JSON-RPC call. This gives the agent access to all MCP tools available at those endpoints.

After a session completes:

1. **Extract output artifacts**: Read specified paths via `/v1/fs/file`, upload to object storage.
2. **Collect metadata**: Token usage, duration, tool call counts from the event stream.

## MCP Tool Configuration

The bridge configures MCP tools by passing server endpoints to the SDK during session creation. The system is MCP-provider-agnostic — it works with any MCP-compatible HTTP endpoint.

### How MCP servers reach the agent

1. The operator defines `mcpServers` in the Pool's `sandboxTemplate` — a list of `{name, url}` entries pointing to MCP server endpoints (Kubernetes Services, external URLs, etc.).
2. The Sandbox Controller reads these entries and injects them into the bridge container as the `MCP_SERVERS` environment variable (JSON-encoded list).
3. When starting a session, the bridge parses `MCP_SERVERS` and passes the endpoints to the SDK via the `mcpServers` parameter in the `session/new` JSON-RPC call.
4. The SDK's agent process discovers tools from the MCP servers and can call them during the session.

### NetworkPolicy

The sandbox NetworkPolicy must allow egress to MCP server endpoints. The Sandbox Controller automatically adds egress rules for each `mcpServers` entry's host and port, alongside the existing DNS and NATS rules.

### MCP server provisioning

The spec does not prescribe how MCP servers are deployed. Options include:

- **[ToolHive](https://github.com/stacklok/toolhive)** — Kubernetes operator for MCP server lifecycle management. Provides `VirtualMCPServer` (vMCP) for tool aggregation, conflict resolution, token optimization, and tool-level RBAC. Recommended for production deployments with many MCP servers. See [spec/10 — Prior Art](10-prior-art.md) for details.
- **Manual deployment** — deploy MCP servers as standard Kubernetes Deployments + Services. Suitable for simple setups with a small number of tools.
- **External endpoints** — point at MCP servers running outside the cluster.

### Failure handling

If an MCP server is unavailable when a session starts, the session starts without those tools — the agent can still run but won't have access to the unavailable tools. MCP tool call failures during a session surface as `tool.result` events with error payloads in the SSE stream. The agent handles these like any tool error — it can retry, use an alternative approach, or report the failure.

## AgentConfig CR

The `AgentConfig` CR is simplified — it no longer defines how to build an adapter, just how to configure the SDK for a specific agent:

```yaml
apiVersion: factory.example.com/v1alpha1
kind: AgentConfig
metadata:
  name: claude-code
  namespace: team-alpha
spec:
  agentType: claude-code        # Must match SDK's agent identifier
  displayName: "Claude Code"

  # Sandbox Agent SDK configuration
  sdk:
    image: rivetdev/sandbox-agent:0.4.2-full   # SDK binary image
    port: 2468

  # Bridge sidecar configuration
  bridge:
    image: ghcr.io/example/factory-bridge:v0.1.0
    port: 8080

  # Permission handling (see "Permission Handling in the Bridge" above)
  permissionMode: bypass        # bypass | autoApprove | requireApproval

  # Agent-specific configuration
  agentSettings:
    contextFile: CLAUDE.md      # Which context file the agent reads
    allowedTools:               # Tool restrictions (optional)
      - bash
      - read
      - write
      - edit

  # Credential requirements
  credentials:
    - name: ANTHROPIC_API_KEY
      secretRef:
        name: anthropic-credentials
        key: api-key
      host: api.anthropic.com        # For credential proxy matching
      header: x-api-key
```

## Adding Support for a New Agent

Because we use the Sandbox Agent SDK, adding a new agent is straightforward:

1. **If the SDK already supports the agent** (Claude Code, Codex, Pi, OpenCode, Amp, Cursor): Create a `AgentConfig` CR with the agent type. No code changes needed.

2. **If the SDK doesn't support the agent yet**: Either contribute an adapter upstream to the SDK (Rust), or request it from the SDK maintainers. The SDK's adapter model is designed for this.

3. **If we need to support an agent before the SDK does**: Run the agent as a managed process via the SDK's `/v1/processes` API. The bridge can still manage it, but without the normalized ACP event stream — we'd consume raw process logs instead.

The control plane never changes — it only talks to the bridge sidecar.

## Trade-offs and Risks

### Risks of Adopting the SDK

| Risk | Mitigation |
|------|-----------|
| SDK development stalls | Apache 2.0 — we can fork. Single Rust binary is self-contained. |
| SDK API changes break us | Pin SDK version per `AgentConfig` CR. Generated Go client catches breaking changes at build time. |
| Rust binary is opaque | We don't need to modify it — only consume its HTTP API. We can contribute upstream for features we need. |
| Performance overhead of extra process | Rust binary is ~5MB, starts in <1s, uses minimal memory. Negligible compared to the agent process. |

### What We Gain

| Benefit | Impact |
|---------|--------|
| 6 agents supported immediately | Months of adapter development saved |
| Desktop runtime for GUI agents | Cursor support we'd otherwise punt on |
| Battle-tested agent management | Process supervision, PTY support, log buffering |
| OpenAPI-defined contract | Type-safe Go client generation, clear API boundary |
| Active upstream maintenance | Security fixes, new agent support from community |
