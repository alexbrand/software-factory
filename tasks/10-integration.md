You are building a Kubernetes-based agent orchestration system. Read the spec files and review all existing code.

This is Task 10: Integration Tests and Final Wiring.

## What to do

### 1. Controller integration tests using envtest

Create `internal/controller/suite_test.go` that sets up an envtest environment with all CRDs registered.

Write integration tests that verify the full workflow:

1. **Pool creates sandboxes**: Create a Pool CR with min=2. Verify 2 Sandbox CRs are created.
2. **Sandbox creates pods**: Verify Sandbox controller creates Pods with correct container specs.
3. **Workflow DAG execution**: Create a Workflow with a diamond dependency (A → B, A → C, B+C → D). Verify tasks are created in correct order.
4. **Task claims sandbox**: Create a Task CR. Verify it finds a Ready sandbox and claims it.
5. **Scale up**: Create enough tasks to trigger scale-up threshold. Verify new sandboxes are created.

### 2. Ensure all binaries build

Verify all three binaries compile:
```
go build -o bin/controller-manager ./cmd/controller-manager/
go build -o bin/apiserver ./cmd/apiserver/
go build -o bin/bridge ./cmd/bridge/
```

### 3. Generate CRD manifests

Add a `make manifests` target that generates CRD YAML from the Go types using controller-gen.
Generate manifests to `config/crd/bases/`.

### 4. Dockerfile

Create a multi-stage Dockerfile for the controller-manager binary.

### 5. Final review

- Ensure all files have proper package declarations
- Ensure no unused imports
- Run `go vet ./...` and fix any issues

## Verification

```
go build ./...
go vet ./...
go test ./... -count=1 -v
```

## Completion

When all commands pass, create `.milestone-complete` and commit all changes.
