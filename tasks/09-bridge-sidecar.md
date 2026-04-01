You are building a Kubernetes-based agent orchestration system. Read spec/06-agent-adapter.md and the existing code.

This is Task 9: Bridge Sidecar.

## What to do

Implement the bridge sidecar binary in `cmd/bridge/` and `internal/bridge/`:

### Core components

1. **Session manager** (`internal/bridge/session.go`):
   - StartSession: Write context files to sandbox via SDK filesystem API, start ACP session, send prompt
   - SendMessage: Forward follow-up messages to SDK
   - CancelSession: Gracefully close ACP session

2. **Event forwarder** (`internal/bridge/events.go`):
   - Connect to SDK's SSE endpoint for ACP events
   - Normalize SDK events into our Event schema
   - Publish normalized events to NATS JetStream

3. **Credential proxy** (`internal/bridge/credproxy.go`):
   - HTTP/HTTPS forward proxy
   - Match outbound requests against credential mappings
   - Inject API keys from mounted secrets into request headers
   - Load credentials from Kubernetes secret volume mount

4. **Status reporter** (`internal/bridge/status.go`):
   - Periodically update Sandbox and Session CR status
   - Report health via `/healthz` endpoint

5. **HTTP server** (`internal/bridge/server.go`):
   - Expose gRPC or HTTP endpoints for the Session Controller to call
   - Endpoints: POST /sessions, POST /sessions/{id}/messages, DELETE /sessions/{id}, GET /healthz

6. **Main** (`cmd/bridge/main.go`):
   - Parse config (SDK URL, NATS URL, K8s in-cluster config)
   - Start credential proxy, HTTP server, and status reporter

### SDK client

Create a basic HTTP client for the Sandbox Agent SDK API in `internal/bridge/sdkclient.go`:
- POST /v1/acp — create ACP session
- POST /v1/acp/{id} — send message
- GET /v1/acp/{id} — SSE stream
- DELETE /v1/acp/{id} — close session
- POST /v1/fs/file — write file
- GET /v1/health — health check

## Verification

```
go build ./...
go vet ./...
go test ./... -count=1 -v
```

## Completion

When all commands pass, create `.milestone-complete` and commit all changes.
