You are building a Kubernetes-based agent orchestration system. Read spec/04-control-plane.md (API Server section) and the existing code.

This is Task 8: API Server.

## What to do

Implement a REST API server in `cmd/apiserver/` and `internal/apiserver/`:

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/workflows` | Submit a workflow (create Workflow CR) |
| GET | `/v1/workflows/{id}` | Get workflow status |
| DELETE | `/v1/workflows/{id}` | Cancel a workflow |
| GET | `/v1/workflows/{id}/tasks` | List tasks in a workflow |
| POST | `/v1/tasks` | Submit a standalone task |
| GET | `/v1/tasks/{id}` | Get task status |
| GET | `/v1/tasks/{id}/events` | Stream task events (SSE) |
| GET | `/v1/pools` | List pools |
| GET | `/v1/pools/{id}` | Get pool status |

### Implementation

1. Use `net/http` with a router (e.g., standard library `http.ServeMux` or a lightweight router)
2. The API server is a Kubernetes client — it creates/reads CRs via `client.Client`
3. For SSE endpoint (`/v1/tasks/{id}/events`), subscribe to NATS using the event subscriber from pkg/events/
4. Add middleware for: request logging, panic recovery, request ID
5. Create `cmd/apiserver/main.go` that starts the HTTP server

### Request/Response types

Define them in `internal/apiserver/types.go`. Map between API types and CRD types.

## Verification

```
go build ./...
go vet ./...
go test ./... -count=1 -v
```

## Completion

When all commands pass, create `.milestone-complete` and commit all changes.
