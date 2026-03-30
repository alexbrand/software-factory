# Personal Assistant with MCP Tools

This example sets up a Pi-based personal assistant agent with access to MCP servers for calendar management and email. ToolHive manages the MCP servers, and a VirtualMCPServer aggregates them behind a single endpoint.

## Use Case

A user wants a personal assistant agent that can:

1. Read and manage their Google Calendar (check availability, create events)
2. Read and draft emails via Gmail
3. Answer questions that require cross-referencing both (e.g., "Schedule a follow-up meeting with everyone from yesterday's email thread")

The agent does not write code — it uses MCP tools to interact with external services.

## Architecture

```
┌───────────────────────────────────────────────────────────────┐
│  assistant-ns namespace                                       │
│                                                               │
│  ┌────────────┐    ┌─────────────────────┐                    │
│  │ Pool       │───►│ Sandbox Pod         │                    │
│  │ (assistant │    │  ├ SDK              │                    │
│  │  -pool)    │    │  ├ Bridge ──────────┼──► vMCP endpoint   │
│  └────────────┘    │  └ Agent (Pi)       │         │          │
│                    └─────────────────────┘         │          │
│  ┌────────────┐                                    │          │
│  │ AgentConfig│         ┌──────────────────────────┘          │
│  │ (pi-asst)  │         ▼                                     │
│  └────────────┘    ┌─────────────────────────────────┐        │
│                    │ VirtualMCPServer (ToolHive)      │        │
│                    │  ├ google-calendar MCP server    │        │
│                    │  └ gmail MCP server              │        │
│                    └─────────────────────────────────┘        │
└───────────────────────────────────────────────────────────────┘
```

The agent connects to a single vMCP endpoint. ToolHive handles routing to the correct backend MCP server, tool namespacing (to avoid conflicts), and credential management for Google APIs.

## Manifests

### 1. ToolHive MCP servers

Two ToolHive `MCPServer` resources — one for Google Calendar, one for Gmail — grouped by an `MCPGroup`, and exposed through a `VirtualMCPServer`.

See [`manifests/toolhive.yaml`](manifests/toolhive.yaml).

### 2. AgentConfig

Configures Pi as the agent runtime. Pi is a lightweight agent framework well-suited for non-coding tasks. No special tools needed beyond MCP access.

See [`manifests/agentconfig.yaml`](manifests/agentconfig.yaml).

### 3. Pool

A minimal pool — personal assistant workloads are lightweight. MCP tools are provided via the vMCP reference. Network access is limited to Google APIs and the Anthropic API.

See [`manifests/pool.yaml`](manifests/pool.yaml).

### 4. Task

An example task showing how a user would interact with the assistant.

See [`manifests/task.yaml`](manifests/task.yaml).

## How It Works

1. The **platform operator** deploys the ToolHive MCP servers for Google Calendar and Gmail. ToolHive runs each MCP server in its own container with secret injection for Google OAuth tokens. The `VirtualMCPServer` aggregates both behind a single HTTP endpoint with namespaced tool names (e.g., `calendar_list_events`, `gmail_search_messages`).

2. The **Pool** references the vMCP via `mcpTools.vmcpRef`. When a sandbox is provisioned, the bridge sidecar configures the agent's MCP client to connect to the vMCP Service endpoint.

3. When a **Task** is submitted, the agent (Pi) receives the prompt and can discover available MCP tools. It calls `calendar_list_events` to check tomorrow's schedule, `gmail_search_messages` to find relevant threads, and `calendar_create_event` to schedule meetings.

4. The agent never handles OAuth tokens directly — ToolHive injects credentials at the MCP server layer. The bridge sidecar's credential proxy handles the Anthropic API key separately.

## Key Points

- **ToolHive manages MCP lifecycle**: MCP servers run as containers managed by the ToolHive operator. Secrets (OAuth tokens) are injected by ToolHive, not by our platform.
- **vMCP aggregation**: The agent sees a single MCP endpoint with all tools namespaced. No per-tool configuration in the AgentConfig.
- **Lightweight sandboxes**: Personal assistant workloads need minimal compute (1 CPU, 2Gi). No build tools, no large repos.
- **Pi as agent runtime**: Pi's minimal core and extension model make it a good fit for non-coding agent tasks. Its RPC mode integrates cleanly with the SDK.
- **Credential separation**: Google API credentials are managed by ToolHive. The Anthropic API key is managed by the credential proxy. Neither is visible to the agent.
