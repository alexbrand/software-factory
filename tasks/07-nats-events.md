You are building a Kubernetes-based agent orchestration system. Read spec/08-observability-and-events.md and the existing code.

This is Task 7: NATS Event Bus Integration.

## What to do

### Event types (`pkg/events/types.go`)

Define the normalized event schema:

```go
type Event struct {
    ID        string          `json:"id"`
    SessionID string          `json:"sessionId"`
    Timestamp time.Time       `json:"timestamp"`
    Type      EventType       `json:"type"`
    Data      json.RawMessage `json:"data"`
}
```

With all event types from the spec: session.started, session.completed, session.failed, agent.thinking, agent.message, tool.call, tool.result, etc.

### Event publisher (`pkg/events/publisher.go`)

```go
type Publisher struct {
    js nats.JetStreamContext
}

func (p *Publisher) Publish(ctx context.Context, event Event) error
func (p *Publisher) EnsureStream(ctx context.Context, streamName string) error
```

Publish events to NATS JetStream subjects: `events.{namespace}.sessions.{session-id}`

### Event subscriber (`pkg/events/subscriber.go`)

```go
type Subscriber struct {
    js nats.JetStreamContext
}

func (s *Subscriber) Subscribe(ctx context.Context, subject string, handler func(Event)) error
func (s *Subscriber) SubscribeSession(ctx context.Context, sessionID string, handler func(Event)) error
```

### NATS connection helper (`pkg/events/nats.go`)

Helper to connect to NATS with retry logic and connection options.

### Integration with controllers

Update the Session Controller to publish session lifecycle events via the publisher.

## Verification

```
go build ./...
go vet ./...
go test ./... -count=1 -v
```

Note: Tests should use mock/interface-based NATS interactions, not require a running NATS server.

## Completion

When all commands pass, create `.milestone-complete` and commit all changes.
