You are building a Kubernetes-based agent orchestration system. Read spec/04-control-plane.md (Workflow Controller and Task Controller sections), spec/07-orchestration-engine.md, and the existing code.

This is Task 5: Workflow and Task Controllers.

## What to do

### Workflow Controller (`internal/controller/workflow_controller.go`)

1. **On Workflow create**: Validate the DAG (no cycles, all dependsOn references exist). Create `Task` CRs for root tasks (no dependencies). Set workflow phase to `Running`.
2. **On Task status change**: When a task succeeds, check if downstream tasks now have all dependencies met. Create their Task CRs. When all tasks succeed, set workflow to `Succeeded`. If any task fails beyond retry limit, set workflow to `Failed`.
3. **On Workflow delete**: Set all running tasks to cancelled.

### Task Controller (`internal/controller/task_controller.go`)

1. **On Task create**: Find an available sandbox in the referenced pool (phase = `Ready`). If none available, requeue. Claim the sandbox (set phase to `Assigned`, set `assignedTask`).
2. **On Sandbox assigned**: Create a `Session` CR to start the agent.
3. **On Session complete**: Update task status based on session result. Release sandbox back to pool (set phase to `Idle`).
4. **Retry logic**: If task failed and attempts < retries, increment attempts and create a new Session.
5. **Timeout**: If task exceeds timeout, cancel the session and mark task as `Failed`.

### DAG validation helper (`internal/controller/dag.go`)

- Implement topological sort to detect cycles
- Helper to find root nodes (no dependencies)
- Helper to find next runnable tasks given current task statuses

Also:
- Register both controllers in `cmd/controller-manager/main.go`
- Write table-driven tests for both controllers and the DAG helper

## Verification

```
go build ./...
go vet ./...
go test ./internal/controller/... -count=1 -v
```

## Completion

When all commands pass, create `.milestone-complete` and commit all changes.
