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

    // Token tracking
    EventTokenUsage EventType = "token.usage"

    // Errors
    EventError EventType = "error"
)
```

We retain the `Raw` field so we can always fall back to the SDK's native event format for debugging or agent-specific analysis.

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
3. **Configure MCP tools**: If the Pool references a ToolHive `VirtualMCPServer`, the bridge configures the agent's MCP client via the SDK's `/v1/config/mcp` endpoint to connect to the vMCP Service endpoint. This gives the agent access to all tools curated for its team/tenant.

After a session completes:

1. **Extract output artifacts**: Read specified paths via `/v1/fs/file`, upload to object storage.
2. **Collect metadata**: Token usage, duration, tool call counts from the event stream.

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
