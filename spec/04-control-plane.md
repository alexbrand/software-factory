# 04 — Control Plane

**Status:** DRAFT

## Overview

The control plane consists of Kubernetes operators that manage the lifecycle of all system resources. Each operator watches one or more Custom Resource Definitions (CRDs) and reconciles desired state with actual state.

All operators are built with [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) in Go and follow the standard operator pattern: watch → reconcile → update status.

## Custom Resource Definitions

### Pool

A Pool defines a template for pre-provisioned sandboxes.

```yaml
apiVersion: factory.example.com/v1alpha1
kind: Pool
metadata:
  name: claude-code-pool
  namespace: team-alpha
spec:
  # Which agent type to use
  agentConfigRef:
    name: claude-code

  # Scaling configuration
  replicas:
    min: 2
    max: 20
    idleTimeout: 30m        # Terminate idle sandboxes after this duration
    scaleUpThreshold: 0.8   # Scale up when 80% of sandboxes are active

  # Sandbox template
  sandboxTemplate:
    resources:
      requests:
        cpu: "2"
        memory: "4Gi"
      limits:
        cpu: "4"
        memory: "8Gi"
    storage:
      size: 50Gi
      storageClassName: fast-ssd
      reclaimPolicy: Retain  # Keep PV for reuse

    # Pre-warm configuration
    warmup:
      image: ghcr.io/example/sandbox-base:latest
      commands:
        - "apt-get update && apt-get install -y build-essential"
        - "npm install -g typescript"
      gitRepos:
        - url: https://github.com/example/monorepo.git
          branch: main
          path: /workspace/monorepo

    # MCP tool access (optional, via ToolHive)
    mcpTools:
      vmcpRef:
        name: team-alpha-tools          # VirtualMCPServer in same namespace
      # Bridge sidecar configures agent's MCP client to point at this endpoint

    # Network policy
    networkPolicy:
      egressRules:
        - to: ["api.anthropic.com", "api.openai.com"]
          ports: [443]
        - to: ["github.com", "*.githubusercontent.com"]
          ports: [443]
        - to: ["registry.npmjs.org", "pypi.org"]
          ports: [443]

status:
  ready: 5
  active: 3
  idle: 2
  creating: 1
```

### Sandbox

A Sandbox represents a single isolated execution environment.

```yaml
apiVersion: factory.example.com/v1alpha1
kind: Sandbox
metadata:
  name: claude-code-pool-abc123
  namespace: team-alpha
  labels:
    factory.example.com/pool: claude-code-pool
spec:
  poolRef:
    name: claude-code-pool
  agentConfigRef:
    name: claude-code

  # Override pool defaults if needed
  resources:
    requests:
      cpu: "2"
      memory: "4Gi"

status:
  phase: Active          # Creating | Ready | Assigned | Active | Idle | Terminating
  podName: sandbox-abc123-pod
  volumeName: sandbox-abc123-pv
  assignedTask: build-auth-module
  currentSession: session-xyz789
  lastActivityAt: "2026-03-27T10:30:00Z"
  conditions:
    - type: Ready
      status: "True"
    - type: AgentHealthy
      status: "True"
```

### AgentConfig

Defines how to run a specific agent type. Configures the Sandbox Agent SDK and bridge sidecar for this agent.

```yaml
apiVersion: factory.example.com/v1alpha1
kind: AgentConfig
metadata:
  name: claude-code
  namespace: team-alpha
spec:
  # Agent identification
  agentType: claude-code        # Must match SDK's agent identifier
  displayName: "Claude Code"

  # Sandbox Agent SDK configuration
  sdk:
    image: rivetdev/sandbox-agent:0.4.2-full
    port: 2468

  # Bridge sidecar configuration
  bridge:
    image: ghcr.io/example/factory-bridge:v0.1.0
    port: 8080
    healthCheck:
      httpGet:
        path: /healthz
        port: 8080
      initialDelaySeconds: 5
      periodSeconds: 10

  # Permission handling for runtime tool-approval prompts
  permissionMode: bypass        # bypass | autoApprove | requireApproval
  # bypass:          Set bypassPermissions on the agent session (no prompts)
  # autoApprove:     Bridge auto-responds "allow" to every permission request
  # requireApproval: Bridge publishes request to NATS and waits for external approval

  # Agent-specific settings
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
      host: api.anthropic.com
      header: x-api-key
```

### Workflow

A Workflow defines a DAG of tasks.

```yaml
apiVersion: factory.example.com/v1alpha1
kind: Workflow
metadata:
  name: implement-auth-feature
  namespace: team-alpha
spec:
  # Global configuration
  defaults:
    poolRef:
      name: claude-code-pool
    timeout: 1h
    retries: 1

  # Context shared across all tasks
  context:
    repository:
      url: https://github.com/example/app.git
      branch: feature/auth
    files:
      - name: feature-spec
        configMapRef:
          name: auth-feature-spec

  # Task DAG
  tasks:
    - name: design-api
      spec:
        prompt: |
          Read the feature spec and design the REST API for the authentication module.
          Output an OpenAPI spec to /workspace/api-spec.yaml.
        outputs:
          - path: /workspace/api-spec.yaml
            artifact: api-spec

    - name: implement-handlers
      dependsOn: [design-api]
      spec:
        prompt: |
          Implement the HTTP handlers based on the API spec.
        inputs:
          - artifact: api-spec
            path: /workspace/api-spec.yaml

    - name: implement-storage
      dependsOn: [design-api]
      spec:
        prompt: |
          Implement the database storage layer based on the API spec.
        inputs:
          - artifact: api-spec
            path: /workspace/api-spec.yaml

    - name: write-tests
      dependsOn: [implement-handlers, implement-storage]
      spec:
        prompt: |
          Write integration tests for the auth module.
          Run the tests and ensure they pass.

    - name: create-pr
      dependsOn: [write-tests]
      spec:
        prompt: |
          Create a pull request with all changes.
          Include a summary of what was implemented.

status:
  phase: Running          # Pending | Running | Succeeded | Failed | Cancelled
  startedAt: "2026-03-27T10:00:00Z"
  taskStatuses:
    design-api: Succeeded
    implement-handlers: Running
    implement-storage: Running
    write-tests: Pending
    create-pr: Pending
```

### Task

A single unit of work within a workflow (or standalone).

```yaml
apiVersion: factory.example.com/v1alpha1
kind: Task
metadata:
  name: implement-handlers
  namespace: team-alpha
  labels:
    factory.example.com/workflow: implement-auth-feature
spec:
  workflowRef:
    name: implement-auth-feature

  poolRef:
    name: claude-code-pool

  prompt: |
    Implement the HTTP handlers based on the API spec.

  inputs:
    - artifact: api-spec
      path: /workspace/api-spec.yaml

  outputs:
    - path: /workspace/src/handlers/
      artifact: handler-code

  timeout: 45m
  retries: 1

status:
  phase: Running              # Pending | Running | WaitingForApproval | Succeeded | Failed | Cancelled
  sandboxRef:
    name: claude-code-pool-abc123
  sessionRef:
    name: session-xyz789
  startedAt: "2026-03-27T10:15:00Z"
  attempts: 1
  tokenUsage:
    input: 15000
    output: 8000
    cost: "0.42"
```

The `WaitingForApproval` task phase is set when the underlying session enters `WaitingForApproval`. It is informational — the Task Controller does not act on it directly; it simply mirrors the session phase so that workflow-level consumers have visibility without reading Session CRs.

### Session

A Session represents a single agent conversation within a sandbox. The Session Controller creates sessions when a Task is assigned to a sandbox and manages them through completion.

```yaml
apiVersion: factory.example.com/v1alpha1
kind: Session
metadata:
  name: session-xyz789
  namespace: team-alpha
  labels:
    factory.example.com/task: implement-handlers
    factory.example.com/sandbox: claude-code-pool-abc123
spec:
  sandboxRef:
    name: claude-code-pool-abc123
  taskRef:
    name: implement-handlers
  prompt: |
    Implement the HTTP handlers based on the API spec.
  contextFiles:
    - path: /workspace/api-spec.yaml
      configMapRef:
        name: api-spec-artifact
  timeout: 45m

status:
  phase: Active             # Pending | Active | WaitingForApproval | Completed | Failed | Cancelled
  acpServerID: acp-abc123   # SDK ACP server identifier
  startedAt: "2026-03-27T10:15:00Z"
  lastEventAt: "2026-03-27T10:30:00Z"

  # Present only when phase is WaitingForApproval
  pendingApproval:
    id: "perm-001"          # Unique ID for this permission request
    toolName: Bash           # Tool the agent wants to execute
    title: "mkdir -p /workspace/output"  # Human-readable summary
    requestedAt: "2026-03-27T10:30:00Z"
    # Full request details (arguments, context) are in NATS, not etcd.
    # Use the SSE stream or NATS subject to get the complete request.

  tokenUsage:
    input: 15000
    output: 8000
    cost: "0.42"
  conditions:
    - type: Ready
      status: "True"
    - type: AgentHealthy
      status: "True"
```

#### Session Phases

| Phase | Description |
|-------|-------------|
| `Pending` | Session CR created, bridge not yet connected |
| `Active` | Agent is executing, events are streaming |
| `WaitingForApproval` | Agent blocked on a permission request (only when `permissionMode: requireApproval`) |
| `Completed` | Agent finished successfully |
| `Failed` | Agent errored or timed out |
| `Cancelled` | Session was cancelled by the user or workflow controller |

The `WaitingForApproval` phase is only reachable when the AgentConfig's `permissionMode` is `requireApproval`. In `bypass` mode, the agent never emits permission requests. In `autoApprove` mode, the bridge responds immediately and the session stays `Active`.

#### Permission Request Lifecycle

When `permissionMode: requireApproval`:

1. The SDK agent emits a `session/request_permission` ACP JSON-RPC request.
2. The bridge normalizes it and publishes to NATS (`events.{tenant}.sessions.{session-id}`).
3. The bridge publishes a lifecycle event: `session.permission_requested`.
4. The Session Controller updates the Session CR: phase → `WaitingForApproval`, populates `status.pendingApproval`.
5. The Task Controller propagates the phase to the Task CR: phase → `WaitingForApproval`.
6. The API server streams the permission details to external clients via SSE (sourced from NATS, not the CR).

**Approval response:**

1. An external client calls `POST /v1/sessions/{id}/permissions/{permissionId}` with `allow` or `deny`.
2. The API server publishes the decision to a NATS reply subject.
3. The bridge receives the decision, sends a JSON-RPC response to the SDK.
4. The bridge publishes a lifecycle event: `session.permission_responded`.
5. The Session Controller updates the Session CR: phase → `Active`, clears `status.pendingApproval`.
6. The Task Controller propagates the phase back to `Running`.

**Design principle:** Only summary data (`toolName`, `title`, `requestedAt`) is stored in etcd via the Session CR. Full permission request payloads (tool arguments, context, rendered diffs) flow through NATS and are served via SSE. This keeps etcd writes low-frequency — one write on request, one on response.

## Operator Behaviors

### Workflow Controller

1. **On Workflow create**: Validate the DAG (no cycles, all dependencies exist). Create `Task` CRs for tasks with no dependencies (roots of the DAG). Set workflow phase to `Running`.

2. **On Task status change**: When a task reaches `Succeeded`, check if any downstream tasks now have all dependencies met. If so, create their `Task` CRs. When all tasks succeed, set workflow to `Succeeded`. If any task fails beyond retry limit, set workflow to `Failed`.

3. **On Workflow delete**: Cancel all running tasks, release sandboxes.

### Task Controller

1. **On Task create**: Find an available sandbox in the referenced pool (phase = `Ready`). If none available, wait (the Pool Controller will scale up). Claim the sandbox by setting its phase to `Assigned`.

2. **On Sandbox assigned**: Prepare the sandbox (inject inputs, credentials). Create a `Session` CR to start the agent.

3. **On Session complete**: Extract outputs, update task status. Release the sandbox back to the pool (phase = `Idle`).

### Sandbox Controller

1. **On Sandbox create**: Create a Pod with the SDK and bridge containers, mount the PV, apply network policies, inject credentials from the AgentConfig.

2. **On Pod ready**: Set sandbox phase to `Ready`.

3. **On Sandbox idle**: Start the idle timeout timer. If the timer expires and the sandbox is still idle, set phase to `Terminating`.

4. **On Sandbox terminating**: Depending on pool `reclaimPolicy`, either delete the PV (clean start next time) or retain it (fast reuse). Delete the pod.

### Pool Controller

1. **Periodic reconciliation**: Compare `ready + creating` sandboxes against `min` replicas. Create sandboxes if below minimum.

2. **Scale-up trigger**: When the ratio of `active / (active + ready)` exceeds `scaleUpThreshold`, create additional sandboxes up to `max`.

3. **Scale-down**: When sandboxes are idle beyond `idleTimeout` and total count exceeds `min`, terminate excess sandboxes (oldest first).

### Session Controller

1. **On Session create**: Connect to the bridge sidecar endpoint in the sandbox pod. Send the prompt. Begin streaming events to NATS.

2. **On event received**: Publish to NATS stream `sessions.<session-id>`. Update Session CR status with latest event summary.

3. **On `session.permission_requested` lifecycle event** (only when `permissionMode: requireApproval`): Set Session CR phase to `WaitingForApproval`. Populate `status.pendingApproval` with summary fields (`id`, `toolName`, `title`, `requestedAt`). This is a single etcd write — the full request payload stays in NATS.

4. **On `session.permission_responded` lifecycle event**: Clear `status.pendingApproval`. Set Session CR phase back to `Active`.

5. **On session complete**: Set Session CR status to final state. Record token usage and cost metadata.

**Important:** The Session Controller subscribes only to **lifecycle events** (started, completed, failed, permission_requested, permission_responded) — not the high-frequency tool call and output events. This keeps etcd write volume bounded by session state transitions, not agent activity.

## API Server

The API server is a Go service that provides a client-facing interface over the CRD-based system.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/workflows` | Submit a workflow |
| GET | `/v1/workflows/{id}` | Get workflow status |
| DELETE | `/v1/workflows/{id}` | Cancel a workflow |
| GET | `/v1/workflows/{id}/tasks` | List tasks in a workflow |
| POST | `/v1/tasks` | Submit a standalone task |
| GET | `/v1/tasks/{id}` | Get task status |
| GET | `/v1/tasks/{id}/events` | Stream task events (SSE) |
| GET | `/v1/sessions/{id}` | Get session status |
| GET | `/v1/sessions/{id}/events` | Stream session events (SSE) |
| POST | `/v1/sessions/{id}/permissions/{permissionId}` | Approve or deny a permission request |
| GET | `/v1/pools` | List pools |
| GET | `/v1/pools/{id}` | Get pool status |
| PATCH | `/v1/pools/{id}/scale` | Adjust pool scaling |

#### Permission Approval Endpoint

```
POST /v1/sessions/{id}/permissions/{permissionId}
Content-Type: application/json

{
  "decision": "allow",        // "allow" or "deny"
  "remember": "session"       // optional: "once" | "session" | "always"
}
```

- `decision`: Whether to allow or deny the tool execution.
- `remember`: Scope of the decision. `once` applies to this request only. `session` auto-applies the same decision for the same tool for the rest of the session. `always` persists to future sessions (stored in AgentConfig annotation).

The API server publishes the decision to the NATS reply subject `permissions.{session-id}.{permissionId}`. The bridge subscribes to this subject and forwards the response to the SDK as a JSON-RPC reply. The SSE stream on `/v1/sessions/{id}/events` includes permission request and response events, so clients can build interactive approval UIs.

All endpoints require authentication (bearer token or mTLS) and respect Kubernetes RBAC via impersonation.
