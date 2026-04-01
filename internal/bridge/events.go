package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexbrand/software-factory/pkg/events"
)

// EventForwarder consumes SSE events from the SDK and publishes normalized
// events to NATS JetStream.
type EventForwarder struct {
	publisher *events.Publisher
	namespace string
	logger    *slog.Logger
}

// NewEventForwarder creates a new event forwarder.
func NewEventForwarder(publisher *events.Publisher, namespace string, logger *slog.Logger) *EventForwarder {
	return &EventForwarder{
		publisher: publisher,
		namespace: namespace,
		logger:    logger,
	}
}

// ForwardEvent normalizes an SSE event from the SDK and publishes it to NATS.
func (f *EventForwarder) ForwardEvent(ctx context.Context, sessionID string, sseEvent SSEEvent) {
	normalized := f.normalizeEvent(sessionID, sseEvent)
	if normalized == nil {
		return
	}

	if f.publisher == nil {
		f.logger.Warn("event publisher not configured, dropping event",
			"sessionID", sessionID,
			"eventType", normalized.Type,
		)
		return
	}

	if err := f.publisher.Publish(ctx, f.namespace, *normalized); err != nil {
		f.logger.Error("failed to publish event",
			"sessionID", sessionID,
			"eventType", normalized.Type,
			"error", err,
		)
	}
}

// normalizeEvent translates an SDK SSE event into our normalized event schema.
func (f *EventForwarder) normalizeEvent(sessionID string, sseEvent SSEEvent) *events.Event {
	eventType := mapEventType(sseEvent.Event)
	if eventType == "" {
		// Unknown event type — log and skip.
		f.logger.Debug("skipping unknown SSE event type", "event", sseEvent.Event)
		return nil
	}

	var data json.RawMessage
	if sseEvent.Data != "" {
		data = json.RawMessage(sseEvent.Data)
	} else {
		data = json.RawMessage("{}")
	}

	eventID := sseEvent.ID
	if eventID == "" {
		eventID = uuid.New().String()
	}

	return &events.Event{
		ID:        eventID,
		SessionID: sessionID,
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Data:      data,
	}
}

// mapEventType maps SDK SSE event types to our normalized EventType.
func mapEventType(sseEventType string) events.EventType {
	// Normalize to lowercase for matching.
	normalized := strings.ToLower(sseEventType)

	switch normalized {
	case "session.started", "session_started":
		return events.EventSessionStarted
	case "session.completed", "session_completed":
		return events.EventSessionCompleted
	case "session.failed", "session_failed":
		return events.EventSessionFailed
	case "thinking", "agent.thinking":
		return events.EventAgentThinking
	case "message", "agent.message":
		return events.EventAgentMessage
	case "tool.call", "tool_call":
		return events.EventToolCall
	case "tool.result", "tool_result":
		return events.EventToolResult
	case "token.usage", "token_usage":
		return events.EventTokenUsage
	case "error":
		return events.EventError
	default:
		return ""
	}
}

// MakeEventCallback returns a callback function suitable for SessionManager.StartSession
// that forwards each SSE event through this EventForwarder.
func (f *EventForwarder) MakeEventCallback(sessionID string) func(SSEEvent) {
	return func(event SSEEvent) {
		f.ForwardEvent(context.Background(), sessionID, event)
	}
}

// EnsureStream creates or verifies the NATS JetStream stream for events.
func (f *EventForwarder) EnsureStream(ctx context.Context) error {
	if f.publisher == nil {
		return fmt.Errorf("publisher not configured")
	}
	return f.publisher.EnsureStream(ctx, events.DefaultStreamName)
}
