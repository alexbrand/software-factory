You are building a Kubernetes-based agent orchestration system. Read spec/04-control-plane.md (Pool Controller section) and the existing code in api/v1alpha1/ and internal/controller/.

This is Task 3: Pool Controller.

## What to do

Implement the Pool Controller in `internal/controller/pool_controller.go`:

1. **Reconcile loop**: Watch `Pool` CRs
2. **Ensure minimum replicas**: If `ready + creating` sandboxes < `min`, create new `Sandbox` CRs from the pool's template
3. **Scale up**: When `active / (active + ready)` exceeds `scaleUpThreshold`, create additional sandboxes up to `max`
4. **Scale down**: When sandboxes are idle beyond `idleTimeout` and count > `min`, set excess sandboxes to `Terminating` (oldest first)
5. **Update Pool status**: Set ready/active/idle/creating counts

Also:
- Register the controller in `cmd/controller-manager/main.go`
- Write table-driven unit tests in `internal/controller/pool_controller_test.go` using fake client from controller-runtime

## Controller pattern

```go
type PoolReconciler struct {
    client.Client
    Scheme *runtime.Scheme
}

func (r *PoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // ...
}

func (r *PoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&v1alpha1.Pool{}).
        Owns(&v1alpha1.Sandbox{}).
        Complete(r)
}
```

## Verification

```
go build ./...
go vet ./...
go test ./internal/controller/... -count=1 -v
```

## Completion

When all commands pass, create `.milestone-complete` and commit all changes.
