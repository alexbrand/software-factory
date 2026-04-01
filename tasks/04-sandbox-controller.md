You are building a Kubernetes-based agent orchestration system. Read spec/04-control-plane.md (Sandbox Controller section), spec/05-sandbox-runtime.md, and the existing code.

This is Task 4: Sandbox Controller.

## What to do

Implement the Sandbox Controller in `internal/controller/sandbox_controller.go`:

1. **On Sandbox create** (phase empty or Creating):
   - Build a Pod spec with containers: init (workspace-init), sandbox-agent-sdk, bridge sidecar
   - Use images and ports from the referenced AgentConfig CR
   - Mount workspace PVC, cache PVC (ReadOnlyMany), and secrets projected volume
   - Apply resource requests/limits from Sandbox spec
   - Create a NetworkPolicy for the sandbox with egress rules from the Pool spec
   - Create the Pod and PVC
   - Set phase to `Creating`

2. **On Pod ready**: Set sandbox phase to `Ready`

3. **On sandbox idle**: Track idle time via `lastActivityAt`. When idle exceeds pool's `idleTimeout`, set phase to `Terminating`

4. **On sandbox terminating**: Delete the Pod. Handle PVC based on pool reclaim policy (Retain or Delete)

5. **Update Sandbox status**: podName, volumeName, conditions (Ready, AgentHealthy)

Also:
- Register the controller in `cmd/controller-manager/main.go`
- Write table-driven unit tests in `internal/controller/sandbox_controller_test.go`

## Verification

```
go build ./...
go vet ./...
go test ./internal/controller/... -count=1 -v
```

## Completion

When all commands pass, create `.milestone-complete` and commit all changes.
