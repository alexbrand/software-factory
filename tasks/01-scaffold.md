You are building a Kubernetes-based agent orchestration system. Read the spec files in spec/ to understand the full system, especially spec/03-architecture-overview.md and spec/04-control-plane.md.

This is Task 1: Project Scaffolding.

## What to do

1. Initialize a Go module: `go mod init github.com/alexbrand/software-factory`
2. Create the directory structure:
   - `api/v1alpha1/` — CRD types
   - `internal/controller/` — controllers
   - `internal/bridge/` — bridge sidecar
   - `cmd/controller-manager/` — operator entrypoint
   - `cmd/apiserver/` — API server entrypoint
   - `cmd/bridge/` — bridge sidecar entrypoint
   - `pkg/events/` — NATS event types and client
3. Create a Makefile with targets: `build`, `test`, `lint`, `generate`, `manifests`
4. Add a basic `main.go` in `cmd/controller-manager/` that sets up a controller-runtime manager (no controllers registered yet)
5. Run `go mod tidy` to resolve dependencies

## Key dependencies

- `sigs.k8s.io/controller-runtime`
- `k8s.io/apimachinery`
- `k8s.io/client-go`

## Verification

Run these commands and ensure they all pass:
```
go build ./...
go vet ./...
```

## Completion

When `go build ./...` and `go vet ./...` both exit 0, create a file called `.milestone-complete` and commit all changes.
