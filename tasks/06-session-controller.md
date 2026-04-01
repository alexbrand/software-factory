You are building a Kubernetes-based agent orchestration system. Read spec/04-control-plane.md (Session Controller section), spec/06-agent-adapter.md, and the existing code.

This is Task 6: Session Controller and Bridge Client.

## What to do

### Bridge client (`internal/bridge/client.go`)

Implement an HTTP client for the bridge sidecar running in sandbox pods:

```go
type Client struct {
    httpClient *http.Client
    baseURL    string
}

func (c *Client) StartSession(ctx context.Context, cfg SessionConfig) (string, error)
func (c *Client) SendMessage(ctx context.Context, sessionID string, msg string) error
func (c *Client) CancelSession(ctx context.Context, sessionID string) error
func (c *Client) GetHealth(ctx context.Context) (*HealthStatus, error)
```

### Session Controller (`internal/controller/session_controller.go`)

1. **On Session create**: Look up the sandbox pod's bridge endpoint. Call `StartSession` via the bridge client with the prompt and context files.
2. **Monitor session**: Periodically check session health. Update Session CR status.
3. **On session complete**: Set Session status to final state (Succeeded/Failed). Record token usage.
4. **On Session delete / cancel**: Call `CancelSession` on the bridge.

Also:
- Register the controller in `cmd/controller-manager/main.go`
- Write table-driven tests using an HTTP test server to mock the bridge

## Verification

```
go build ./...
go vet ./...
go test ./... -count=1 -v
```

## Completion

When all commands pass, create `.milestone-complete` and commit all changes.
