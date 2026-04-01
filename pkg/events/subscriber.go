package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
)

// JetStreamSubscriber defines the subset of nats.JetStreamContext used for subscribing.
type JetStreamSubscriber interface {
	Subscribe(subj string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error)
	QueueSubscribe(subj, queue string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error)
}

// Subscriber consumes events from NATS JetStream.
type Subscriber struct {
	js JetStreamSubscriber
}

// NewSubscriber creates a Subscriber with the given JetStream context.
func NewSubscriber(js JetStreamSubscriber) *Subscriber {
	return &Subscriber{js: js}
}

// Subscribe listens for events on the given subject and calls handler for each.
func (s *Subscriber) Subscribe(ctx context.Context, subject string, handler func(Event)) (*nats.Subscription, error) {
	sub, err := s.js.Subscribe(subject, func(msg *nats.Msg) {
		var event Event
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			// Skip malformed events.
			_ = msg.Ack()
			return
		}
		handler(event)
		_ = msg.Ack()
	}, nats.DeliverAll(), nats.AckExplicit())
	if err != nil {
		return nil, fmt.Errorf("subscribing to %s: %w", subject, err)
	}

	// Unsubscribe when context is cancelled.
	go func() {
		<-ctx.Done()
		_ = sub.Unsubscribe()
	}()

	return sub, nil
}

// SubscribeSession subscribes to all events for a specific session.
func (s *Subscriber) SubscribeSession(ctx context.Context, namespace, sessionID string, handler func(Event)) (*nats.Subscription, error) {
	subject := Subject(namespace, sessionID)
	return s.Subscribe(ctx, subject, handler)
}

// QueueSubscribe subscribes with a queue group for load-balanced consumption.
func (s *Subscriber) QueueSubscribe(ctx context.Context, subject, queue string, handler func(Event)) (*nats.Subscription, error) {
	sub, err := s.js.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		var event Event
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			_ = msg.Ack()
			return
		}
		handler(event)
		_ = msg.Ack()
	}, nats.DeliverAll(), nats.AckExplicit())
	if err != nil {
		return nil, fmt.Errorf("queue subscribing to %s: %w", subject, err)
	}

	go func() {
		<-ctx.Done()
		_ = sub.Unsubscribe()
	}()

	return sub, nil
}
