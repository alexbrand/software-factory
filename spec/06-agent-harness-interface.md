# 06 — Agent Harness Interface

**Status:** DRAFT

## Overview

The harness is the universal adapter layer between the orchestration system and individual agent runtimes. It solves the fundamental problem that every coding agent has a different CLI, event format, and session model.

This design is heavily inspired by the [Sandbox Agent SDK](https://sandboxagent.dev/), which demonstrates the viability of a universal agent adapter. Our harness builds on similar principles but is designed for Kubernetes-native deployment rather than generic sandbox providers.

## Design Goals

1. **Agent-agnostic**: Adding support for a new agent should require implementing a single Go interface, not modifying the control plane.
2. **Streaming-first**: All session interactions produce a real-time event stream.
3. **Normalized events**: Regardless of the underlying agent, the event stream follows a common schema.
4. **Lifecycle control**: The harness manages agent process startup, health, and graceful shutdown.
5. **Credential isolation**: The harness handles credential injection; the agent never sees raw API keys in its prompt context.

## Harness Architecture

```
┌─────────────────────────────────────────────────┐
│  Sandbox Pod                                     │
│                                                  │
│  ┌──────────────────────────────────────────┐   │
│  │  Harness Process                          │   │
│  │                                           │   │
│  │  ┌─────────────┐    ┌──────────────────┐ │   │
│  │  │  HTTP/gRPC   │    │  Agent Adapter   │ │   │
│  │  │  Server      │◄──►│  (per-agent      │ │   │
│  │  │              │    │   implementation) │ │   │
│  │  └──────┬───────┘    └────────┬─────────┘ │   │
│  │         │                     │            │   │
│  │         │              ┌──────▼─────────┐  │   │
│  │         │              │  Agent Process  │  │   │
│  │         │              │  (claude, codex │  │   │
│  │         │              │   pi, etc.)     │  │   │
│  │         │              └────────────────┘  │   │
│  │  ┌──────▼───────────────────────────────┐  │   │
│  │  │  Event Normalizer                     │  │   │
│  │  │  (agent-specific → common schema)     │  │   │
│  │  └──────────────────────────────────────┘  │   │
│  └──────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```

## Harness Interface (Go)

```go
// Harness is the interface every agent adapter must implement.
type Harness interface {
    // Info returns metadata about this agent type.
    Info() AgentInfo

    // StartSession begins a new agent session with the given configuration.
    // It returns a Session handle for interaction.
    StartSession(ctx context.Context, cfg SessionConfig) (Session, error)

    // Healthy returns true if the agent process is running and responsive.
    Healthy(ctx context.Context) (bool, error)

    // Shutdown gracefully stops the agent process.
    Shutdown(ctx context.Context) error
}

// Session represents an active agent interaction.
type Session interface {
    // ID returns the unique session identifier.
    ID() string

    // SendMessage sends a prompt or follow-up message to the agent.
    SendMessage(ctx context.Context, msg Message) error

    // Events returns a channel of normalized events from the agent.
    Events() <-chan Event

    // Steer sends a steering message that is delivered after the current
    // tool execution completes (inspired by Pi's steering messages).
    Steer(ctx context.Context, msg Message) error

    // Cancel requests graceful cancellation of the current session.
    Cancel(ctx context.Context) error

    // Result blocks until the session completes and returns the outcome.
    Result(ctx context.Context) (*SessionResult, error)
}

type AgentInfo struct {
    Type        string   // e.g., "claude-code", "codex", "pi"
    Version     string   // Agent version
    Protocols   []string // Supported communication protocols
}

type SessionConfig struct {
    Prompt       string            // The task instruction
    Context      []ContextFile     // Files to make available
    Environment  map[string]string // Environment variables
    WorkDir      string            // Working directory
    Timeout      time.Duration     // Session timeout
    SystemPrompt string            // Optional system prompt override
}

type Message struct {
    Role    string // "user", "system", "steering"
    Content string
}
```

## Normalized Event Schema

All agent events are normalized to a common schema before being published. This enables agent-agnostic tooling for monitoring, replay, and analysis.

```go
type Event struct {
    ID        string          `json:"id"`
    SessionID string          `json:"sessionId"`
    Timestamp time.Time       `json:"timestamp"`
    Type      EventType       `json:"type"`
    Data      json.RawMessage `json:"data"`
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

### Event Data Examples

```json
{
  "id": "evt-001",
  "sessionId": "sess-abc123",
  "timestamp": "2026-03-27T10:30:15Z",
  "type": "tool.call",
  "data": {
    "tool": "bash",
    "input": {"command": "go test ./..."},
    "agentNativeTool": "Bash"
  }
}
```

```json
{
  "id": "evt-002",
  "sessionId": "sess-abc123",
  "timestamp": "2026-03-27T10:30:20Z",
  "type": "token.usage",
  "data": {
    "inputTokens": 1500,
    "outputTokens": 300,
    "cacheReadTokens": 500,
    "model": "claude-opus-4-6",
    "estimatedCost": 0.045
  }
}
```

## Agent Adapter Implementations

### Claude Code Adapter

Communication: stdin/stdout (JSONL) or HTTP via `--server` mode.

```go
type ClaudeCodeAdapter struct {
    binaryPath string
    process    *os.Process
}

// StartSession launches claude with --print --output-format=json
// and streams structured output from stdout.
```

Key considerations:
- Claude Code supports `CLAUDE.md` context files — the harness writes task context to this file before starting a session.
- Uses `--allowedTools` to restrict tool access per task configuration.
- Token usage extracted from the JSON output stream.

### Codex Adapter

Communication: CLI with structured output.

```go
type CodexAdapter struct {
    binaryPath string
    process    *os.Process
}
```

Key considerations:
- Codex uses a sandbox-within-sandbox model — our outer sandbox provides the persistent state, Codex's inner sandbox can be disabled or configured to use the outer filesystem directly.
- Event normalization maps Codex's tool calls to the common schema.

### Pi Adapter

Communication: stdin/stdout RPC (JSONL framing) or SDK embedding.

```go
type PiAdapter struct {
    binaryPath string
    rpcConn    *jsonrpc.Conn
}
```

Key considerations:
- Pi's RPC mode over stdin/stdout is ideal for harness integration.
- Supports steering messages natively — our `Steer()` method maps directly.
- Session tree branching could be exposed as an advanced feature.
- Pi loads `AGENTS.md` or `CLAUDE.md` — the harness writes context to these files.

## HTTP API

The harness exposes an HTTP API within the sandbox pod for the Session Controller to interact with.

### Endpoints

```
POST   /sessions              Create a new session
GET    /sessions/{id}         Get session status
POST   /sessions/{id}/messages  Send a message to the session
GET    /sessions/{id}/events  Stream events (SSE)
POST   /sessions/{id}/steer   Send a steering message
DELETE /sessions/{id}         Cancel a session
GET    /healthz               Health check
GET    /info                  Agent info
```

### Session Creation

```
POST /sessions
Content-Type: application/json

{
  "prompt": "Implement the authentication module...",
  "context": [
    {"path": "AGENTS.md", "content": "...project context..."},
    {"path": "api-spec.yaml", "content": "..."}
  ],
  "workDir": "/workspace/repo",
  "timeout": "45m",
  "environment": {
    "GITHUB_TOKEN": "***"
  }
}

Response:
{
  "id": "sess-abc123",
  "status": "running",
  "eventStreamUrl": "/sessions/sess-abc123/events"
}
```

### Event Streaming

```
GET /sessions/sess-abc123/events
Accept: text/event-stream

data: {"id":"evt-001","type":"session.started","timestamp":"...","data":{}}
data: {"id":"evt-002","type":"agent.thinking","timestamp":"...","data":{"content":"Let me analyze..."}}
data: {"id":"evt-003","type":"tool.call","timestamp":"...","data":{"tool":"bash","input":{"command":"ls"}}}
data: {"id":"evt-004","type":"tool.result","timestamp":"...","data":{"tool":"bash","output":"..."}}
...
data: {"id":"evt-099","type":"session.completed","timestamp":"...","data":{"result":"success"}}
```

## Adding a New Agent

To add support for a new agent:

1. Implement the `Harness` interface in a new Go package under `pkg/harness/<agent-name>/`.
2. Implement event normalization mapping the agent's native events to the common `Event` schema.
3. Create a container image with the harness binary and agent binary.
4. Register the agent type by creating a `HarnessConfig` CR.

The control plane requires zero changes — it only interacts with the harness HTTP API.
