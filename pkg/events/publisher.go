package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// JetStreamPublisher defines the subset of nats.JetStreamContext used for publishing.
// This interface enables mock-based testing without a running NATS server.
type JetStreamPublisher interface {
	Publish(subj string, data []byte, opts ...nats.PubOpt) (*nats.PubAck, error)
	AddStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error)
	StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error)
}

// Publisher publishes events to NATS JetStream.
type Publisher struct {
	js JetStreamPublisher
}

// NewPublisher creates a Publisher with the given JetStream context.
func NewPublisher(js JetStreamPublisher) *Publisher {
	return &Publisher{js: js}
}

// Publish serializes and publishes an event to the appropriate NATS subject.
// Subject format: events.{namespace}.sessions.{session-id}
func (p *Publisher) Publish(ctx context.Context, namespace string, event Event) error {
	if event.ID == "" {
		return fmt.Errorf("publishing event: event ID is required")
	}
	if event.SessionID == "" {
		return fmt.Errorf("publishing event: session ID is required")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling event: %w", err)
	}

	subject := Subject(namespace, event.SessionID)
	_, err = p.js.Publish(subject, data, nats.MsgId(event.ID))
	if err != nil {
		return fmt.Errorf("publishing to %s: %w", subject, err)
	}

	return nil
}

// EnsureStream creates or verifies the JetStream stream for events.
func (p *Publisher) EnsureStream(ctx context.Context, streamName string) error {
	_, err := p.js.StreamInfo(streamName)
	if err == nil {
		return nil
	}

	_, err = p.js.AddStream(&nats.StreamConfig{
		Name:       streamName,
		Subjects:   []string{"events.>"},
		Retention:  nats.LimitsPolicy,
		MaxAge:     30 * 24 * time.Hour, // 30 days
		Storage:    nats.FileStorage,
		Replicas:   1,
		Duplicates: 2 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("creating stream %s: %w", streamName, err)
	}

	return nil
}

// Subject returns the NATS subject for a session's events.
func Subject(namespace, sessionID string) string {
	return fmt.Sprintf("events.%s.sessions.%s", namespace, sessionID)
}
