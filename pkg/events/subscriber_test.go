package events

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// mockSubscription implements a minimal nats.Subscription-like behavior for testing.
type mockMsg struct {
	data []byte
	acked bool
	mu    sync.Mutex
}

func (m *mockMsg) Ack(_ ...nats.AckOpt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = true
	return nil
}

// mockJetStreamSubscriber implements JetStreamSubscriber for testing.
type mockJetStreamSubscriber struct {
	subscribeCallback func(subj string, cb nats.MsgHandler)
	subscribeErr      error
	queueCallback     func(subj, queue string, cb nats.MsgHandler)
	queueErr          error
}

func (m *mockJetStreamSubscriber) Subscribe(subj string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error) {
	if m.subscribeErr != nil {
		return nil, m.subscribeErr
	}
	if m.subscribeCallback != nil {
		m.subscribeCallback(subj, cb)
	}
	// Return a non-nil subscription. We can't create a real one without a connection,
	// so we return nil and the test verifies behavior through the callback.
	return &nats.Subscription{}, nil
}

func (m *mockJetStreamSubscriber) QueueSubscribe(subj, queue string, cb nats.MsgHandler, opts ...nats.SubOpt) (*nats.Subscription, error) {
	if m.queueErr != nil {
		return nil, m.queueErr
	}
	if m.queueCallback != nil {
		m.queueCallback(subj, queue, cb)
	}
	return &nats.Subscription{}, nil
}

func TestSubscriber_Subscribe(t *testing.T) {
	event := Event{
		ID:        "evt-1",
		SessionID: "sess-1",
		Timestamp: time.Now().UTC(),
		Type:      EventSessionStarted,
		Data:      json.RawMessage(`{"agentType":"claude"}`),
	}
	eventData, _ := json.Marshal(event)

	var received []Event
	var mu sync.Mutex

	mock := &mockJetStreamSubscriber{
		subscribeCallback: func(subj string, cb nats.MsgHandler) {
			if subj != "events.default.sessions.sess-1" {
				t.Errorf("unexpected subject %q", subj)
			}
			// Simulate a message delivery.
			cb(&nats.Msg{Data: eventData})
		},
	}

	sub := NewSubscriber(mock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := sub.Subscribe(ctx, "events.default.sessions.sess-1", func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, e)
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 event, got %d", len(received))
	}
	if received[0].ID != "evt-1" {
		t.Errorf("expected event ID evt-1, got %q", received[0].ID)
	}
	if received[0].Type != EventSessionStarted {
		t.Errorf("expected event type %q, got %q", EventSessionStarted, received[0].Type)
	}
}

func TestSubscriber_SubscribeSession(t *testing.T) {
	var subscribedSubject string
	mock := &mockJetStreamSubscriber{
		subscribeCallback: func(subj string, cb nats.MsgHandler) {
			subscribedSubject = subj
		},
	}

	sub := NewSubscriber(mock)
	ctx := context.Background()

	_, err := sub.SubscribeSession(ctx, "team-alpha", "sess-42", func(e Event) {})
	if err != nil {
		t.Fatalf("SubscribeSession() error = %v", err)
	}

	want := "events.team-alpha.sessions.sess-42"
	if subscribedSubject != want {
		t.Errorf("SubscribeSession() subscribed to %q, want %q", subscribedSubject, want)
	}
}

func TestSubscriber_Subscribe_Error(t *testing.T) {
	mock := &mockJetStreamSubscriber{
		subscribeErr: nats.ErrConnectionClosed,
	}

	sub := NewSubscriber(mock)
	_, err := sub.Subscribe(context.Background(), "events.>", func(e Event) {})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSubscriber_QueueSubscribe(t *testing.T) {
	var gotSubject, gotQueue string
	mock := &mockJetStreamSubscriber{
		queueCallback: func(subj, queue string, cb nats.MsgHandler) {
			gotSubject = subj
			gotQueue = queue
		},
	}

	sub := NewSubscriber(mock)
	_, err := sub.QueueSubscribe(context.Background(), "events.>", "session-controller", func(e Event) {})
	if err != nil {
		t.Fatalf("QueueSubscribe() error = %v", err)
	}
	if gotSubject != "events.>" {
		t.Errorf("expected subject events.>, got %q", gotSubject)
	}
	if gotQueue != "session-controller" {
		t.Errorf("expected queue session-controller, got %q", gotQueue)
	}
}

func TestSubscriber_SkipsMalformedMessages(t *testing.T) {
	var received []Event
	mock := &mockJetStreamSubscriber{
		subscribeCallback: func(subj string, cb nats.MsgHandler) {
			// Send a malformed message — should be skipped.
			cb(&nats.Msg{Data: []byte("not json")})
			// Send a valid message.
			validEvent := Event{
				ID:        "evt-2",
				SessionID: "sess-1",
				Timestamp: time.Now().UTC(),
				Type:      EventAgentMessage,
				Data:      json.RawMessage(`{}`),
			}
			data, _ := json.Marshal(validEvent)
			cb(&nats.Msg{Data: data})
		},
	}

	sub := NewSubscriber(mock)
	_, err := sub.Subscribe(context.Background(), "events.>", func(e Event) {
		received = append(received, e)
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	if len(received) != 1 {
		t.Fatalf("expected 1 event (malformed skipped), got %d", len(received))
	}
	if received[0].ID != "evt-2" {
		t.Errorf("expected event ID evt-2, got %q", received[0].ID)
	}
}
