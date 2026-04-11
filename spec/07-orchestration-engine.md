# 07 — Orchestration Engine

**Status:** DRAFT

## Overview

The orchestration engine coordinates multi-agent workflows: decomposing work into tasks, managing dependencies, routing tasks to sandboxes, and collecting results. It is the brain of the system, implemented as a set of Kubernetes controllers.

## Workflow Execution Model

### DAG-Based Execution

Workflows are directed acyclic graphs where nodes are tasks and edges are dependencies. The engine guarantees:

- A task only starts when **all** its dependencies have succeeded.
- Independent tasks run **in parallel** (bounded by available sandboxes).
- A failed task can be **retried** up to a configured limit.
- A workflow fails fast if a task fails beyond its retry limit (configurable: fail-fast vs. best-effort).

### Execution Phases

```
Submission → Validation → Scheduling → Execution → Collection → Completion
```

1. **Submission**: Client submits a `Workflow` CR via the API.
2. **Validation**: DAG structure validated (no cycles, valid references). Task specs validated.
3. **Scheduling**: Root tasks (no dependencies) are created as `Task` CRs. The Task Controller finds available sandboxes.
4. **Execution**: Sessions run in sandboxes. Events stream to NATS.
5. **Collection**: Task outputs (artifacts) are extracted and staged for downstream tasks.
6. **Completion**: All tasks done → workflow status updated. Results aggregated.

## Artifact Passing

Tasks communicate results through **artifacts** — named file paths or directories that are extracted from one sandbox and injected into another.

### Mechanism

```
Task A (Sandbox 1)          Task B (Sandbox 2)
     │                           │
     ├─ writes /workspace/out/   │
     │  api-spec.yaml            │
     │                           │
     └──── artifact: api-spec ───┤
           (extracted to         │
            object storage)      ├─ reads /workspace/in/
                                 │  api-spec.yaml
                                 │  (injected from
                                 │   object storage)
```

Implementation options (in order of preference):

1. **Shared PVC**: If tasks run in the same pool, a shared ReadWriteMany volume can hold artifacts. Simple but requires compatible storage.
2. **Object storage**: Extract artifacts to S3-compatible storage (MinIO in-cluster). Upload after task completion, download before task start. Works across pools and clusters.
3. **ConfigMap/Secret**: For small artifacts (<1MB), store as ConfigMap data. Quick but size-limited.

### Artifact Spec

```yaml
# In Task A (producer)
outputs:
  - path: /workspace/api-spec.yaml
    artifact: api-spec
  - path: /workspace/src/handlers/
    artifact: handler-code
    compress: true   # tar.gz the directory

# In Task B (consumer)
inputs:
  - artifact: api-spec
    path: /workspace/api-spec.yaml
  - artifact: handler-code
    path: /workspace/src/handlers/
    decompress: true
```

## Scheduling Strategies

### Pool Affinity

By default, tasks run in the pool specified in the workflow defaults. Individual tasks can override:

```yaml
tasks:
  - name: run-python-analysis
    spec:
      poolRef:
        name: python-agent-pool  # Override default pool
```

### Sandbox Reuse within Workflows

When consecutive tasks in a workflow share the same pool, the scheduler **prefers** to reuse the same sandbox. This avoids re-cloning repos and preserves filesystem state from previous tasks.

```yaml
spec:
  defaults:
    sandboxReuse: prefer   # prefer | require | none
```

- `prefer`: Reuse if the sandbox is still available; allocate new if not.
- `require`: Fail the task if the previous sandbox is unavailable.
- `none`: Always use a fresh sandbox.

### Concurrency Control

Workflows can limit how many tasks run in parallel:

```yaml
spec:
  maxParallelTasks: 5      # Limit concurrent tasks
  rateLimiting:
    requestsPerMinute: 30  # Throttle API calls (e.g., LLM rate limits)
```

## Error Handling

### Retry Policy

```yaml
spec:
  defaults:
    retries: 2
    retryBackoff: exponential   # none | linear | exponential
    retryBackoffBase: 30s

  tasks:
    - name: flaky-integration-test
      spec:
        retries: 3              # Override for this task
```

On retry, the task gets a **fresh session** in the same sandbox (if reuse is enabled) or a new sandbox. The retry prompt can include context about the previous failure:

```
Previous attempt failed with: <error summary>
Please try a different approach.
```

### Failure Modes

| Mode | Behavior |
|------|----------|
| `fail-fast` (default) | Cancel all running tasks when any task fails beyond retry limit |
| `best-effort` | Continue running independent tasks; only dependent tasks are skipped |
| `ignore-failures` | Continue all tasks; failed tasks are marked but don't block |

```yaml
spec:
  failureMode: best-effort
```

### Timeout Handling

Timeouts apply at two levels:

- **Task timeout**: Maximum time for a single task (including retries). Default: 1h.
- **Workflow timeout**: Maximum time for the entire workflow. Default: 24h.

When a timeout fires, the session is cancelled gracefully (the bridge sidecar sends a cancel signal to the agent via the SDK), then forcefully killed after a grace period.

## Advanced Patterns

### Fan-Out / Fan-In

Process multiple items in parallel, then aggregate:

```yaml
tasks:
  - name: analyze-repos
    forEach:
      items: ["repo-a", "repo-b", "repo-c", "repo-d"]
      itemVar: REPO_NAME
    spec:
      prompt: "Analyze {{REPO_NAME}} for security vulnerabilities."
      outputs:
        - path: /workspace/report.json
          artifact: "report-{{REPO_NAME}}"

  - name: aggregate-reports
    dependsOn: [analyze-repos]
    spec:
      prompt: "Combine all security reports into a summary."
      inputs:
        - artifact: "report-*"
          path: /workspace/reports/
```

The `forEach` directive creates one task instance per item, all running in parallel (subject to concurrency limits). The downstream `aggregate-reports` task waits for all instances.

### Conditional Tasks

```yaml
tasks:
  - name: run-tests
    spec:
      prompt: "Run the test suite."
      outputs:
        - path: /workspace/test-results.json
          artifact: test-results

  - name: fix-failures
    dependsOn: [run-tests]
    when:
      taskStatus: failed     # Only run if run-tests failed
    spec:
      prompt: "Fix the failing tests."
      inputs:
        - artifact: test-results

  - name: deploy
    dependsOn: [run-tests]
    when:
      taskStatus: succeeded  # Only run if run-tests succeeded
    spec:
      prompt: "Deploy to staging."
```

### Human-in-the-Loop

Tasks can pause for human approval before proceeding:

```yaml
tasks:
  - name: implement-feature
    spec:
      prompt: "Implement the feature."

  - name: human-review
    dependsOn: [implement-feature]
    type: approval           # Special task type
    spec:
      approvers: ["alice@example.com", "bob@example.com"]
      timeout: 48h
      message: "Review the implementation before deployment."

  - name: deploy
    dependsOn: [human-review]
    spec:
      prompt: "Deploy the approved changes."
```

Approval tasks don't consume a sandbox. They create a notification (webhook, email, Slack) and wait for an explicit approval via the API.

#### Workflow-Level vs. Session-Level Approval

The system supports two distinct approval mechanisms at different granularities:

| | Workflow-Level Approval | Session-Level Permission Gating |
|---|---|---|
| **Scope** | Between tasks in a DAG | During a single agent session |
| **Granularity** | Coarse — gates entire workflow stages | Fine — individual tool executions |
| **Trigger** | `type: approval` task in the DAG | Agent emits `session/request_permission` |
| **Blocks** | Downstream tasks from starting | The agent from executing a specific tool |
| **Configuration** | Workflow spec (`tasks[].type`) | AgentConfig (`permissionMode`) |
| **Response path** | API → Task Controller → workflow resumes | API → NATS → Bridge → SDK → agent resumes |
| **Spec reference** | This document (07) | [04 - Control Plane](04-control-plane.md) (Session CR), [06 - Agent Adapter](06-agent-adapter.md) (Bridge handling) |

These mechanisms are **complementary**. A workflow can use approval tasks to gate stages (e.g., "review before deploy") while individual sessions within those stages use `requireApproval` to gate tool execution (e.g., "approve before running `rm -rf`"). They do not overlap — approval tasks are DAG-level concerns; permission gating is session-level.

## Workflow Templates

Reusable workflow patterns can be defined as templates:

```yaml
apiVersion: factory.example.com/v1alpha1
kind: WorkflowTemplate
metadata:
  name: implement-and-test
spec:
  parameters:
    - name: FEATURE_SPEC
      description: "Path to the feature specification"
    - name: REPO_URL
      description: "Git repository URL"

  tasks:
    - name: implement
      spec:
        prompt: "Implement the feature described in {{FEATURE_SPEC}}."
    - name: test
      dependsOn: [implement]
      spec:
        prompt: "Write and run tests for the implementation."
    - name: review
      dependsOn: [test]
      type: approval
```

Instantiated via:

```yaml
apiVersion: factory.example.com/v1alpha1
kind: Workflow
metadata:
  name: auth-feature
spec:
  templateRef:
    name: implement-and-test
  arguments:
    FEATURE_SPEC: /specs/auth.md
    REPO_URL: https://github.com/example/app.git
```
