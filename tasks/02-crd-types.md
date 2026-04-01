You are building a Kubernetes-based agent orchestration system. Read spec/04-control-plane.md, spec/05-sandbox-runtime.md, and spec/06-agent-adapter.md for the CRD definitions.

This is Task 2: CRD Type Definitions.

## What to do

Define the following CRD types in `api/v1alpha1/` with the API group `factory.example.com/v1alpha1`:

1. **Pool** — Defines a template for pre-provisioned sandboxes. Include fields for:
   - `agentConfigRef`, `replicas` (min/max/idleTimeout/scaleUpThreshold), `sandboxTemplate` (resources, storage, warmup, networkPolicy)
   - Status: ready, active, idle, creating counts

2. **Sandbox** — Represents a single isolated execution environment. Include fields for:
   - `poolRef`, `agentConfigRef`, resource overrides
   - Status: phase (Creating/Ready/Assigned/Active/Idle/Terminating), podName, volumeName, assignedTask, currentSession, conditions

3. **AgentConfig** — Defines how to run a specific agent type. Include fields for:
   - `agentType`, `displayName`, SDK config (image, port), bridge config (image, port), `agentSettings`, `credentials`

4. **Workflow** — Defines a DAG of tasks. Include fields for:
   - `defaults` (poolRef, timeout, retries), `context` (repository, files), `tasks` list with dependsOn
   - Status: phase, startedAt, completedAt, taskStatuses map

5. **Task** — A single unit of work. Include fields for:
   - `workflowRef`, `poolRef`, `prompt`, `inputs`, `outputs`, `timeout`, `retries`
   - Status: phase, sandboxRef, sessionRef, startedAt, attempts, tokenUsage

6. **Session** — Represents an agent session in a sandbox. Include fields for:
   - `sandboxRef`, `taskRef`, `agentType`, `prompt`, `contextFiles`
   - Status: phase, startedAt, completedAt, eventStreamSubject, tokenUsage

Also create:
- `api/v1alpha1/groupversion_info.go` with the SchemeBuilder and GroupVersion
- `api/v1alpha1/zz_generated.deepcopy.go` using controller-gen, OR manually implement DeepCopyObject/DeepCopyInto for each type

Register all types with the scheme.

## Conventions

- Use `metav1.ObjectMeta` and `metav1.TypeMeta` for all types
- Use `metav1.Condition` for condition arrays in status
- Use pointer types for optional fields
- Add JSON tags on all fields
- Add kubebuilder markers for validation where appropriate (e.g., `+kubebuilder:validation:Enum` for phase fields)

## Verification

```
go build ./...
go vet ./...
go test ./... -count=1
```

## Completion

When all three commands exit 0, create `.milestone-complete` and commit all changes.
