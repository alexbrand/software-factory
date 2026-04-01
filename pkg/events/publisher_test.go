package events

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// mockJetStream implements JetStreamPublisher for testing.
type mockJetStream struct {
	published    []publishedMsg
	publishErr   error
	streamInfo   *nats.StreamInfo
	streamErr    error
	addStreamErr error
}

type publishedMsg struct {
	Subject string
	Data    []byte
	MsgID   string
}

func (m *mockJetStream) Publish(subj string, data []byte, opts ...nats.PubOpt) (*nats.PubAck, error) {
	if m.publishErr != nil {
		return nil, m.publishErr
	}
	msg := publishedMsg{Subject: subj, Data: data}
	// Extract MsgId from opts by checking if the dedup header would be set.
	// Since we can't easily inspect PubOpt, store the subject instead.
	m.published = append(m.published, msg)
	return &nats.PubAck{Stream: "EVENTS"}, nil
}

func (m *mockJetStream) AddStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	if m.addStreamErr != nil {
		return nil, m.addStreamErr
	}
	return &nats.StreamInfo{Config: *cfg}, nil
}

func (m *mockJetStream) StreamInfo(stream string, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	return m.streamInfo, nil
}

func TestPublisher_Publish(t *testing.T) {
	tests := []struct {
		name       string
		namespace  string
		event      Event
		publishErr error
		wantErr    bool
		wantSubj   string
	}{
		{
			name:      "publishes event to correct subject",
			namespace: "team-alpha",
			event: Event{
				ID:        "evt-1",
				SessionID: "sess-1",
				Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				Type:      EventSessionStarted,
				Data:      json.RawMessage(`{"agentType":"claude"}`),
			},
			wantSubj: "events.team-alpha.sessions.sess-1",
		},
		{
			name:      "returns error when event ID is empty",
			namespace: "default",
			event: Event{
				SessionID: "sess-1",
				Type:      EventSessionStarted,
			},
			wantErr: true,
		},
		{
			name:      "returns error when session ID is empty",
			namespace: "default",
			event: Event{
				ID:   "evt-1",
				Type: EventSessionStarted,
			},
			wantErr: true,
		},
		{
			name:      "returns error on publish failure",
			namespace: "default",
			event: Event{
				ID:        "evt-1",
				SessionID: "sess-1",
				Type:      EventSessionStarted,
				Data:      json.RawMessage(`{}`),
			},
			publishErr: fmt.Errorf("connection lost"),
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockJetStream{publishErr: tt.publishErr}
			pub := NewPublisher(mock)

			err := pub.Publish(context.Background(), tt.namespace, tt.event)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Publish() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && len(mock.published) != 1 {
				t.Fatalf("expected 1 published message, got %d", len(mock.published))
			}

			if tt.wantSubj != "" && len(mock.published) > 0 {
				if mock.published[0].Subject != tt.wantSubj {
					t.Errorf("expected subject %q, got %q", tt.wantSubj, mock.published[0].Subject)
				}

				// Verify the published data is valid JSON containing the event.
				var decoded Event
				if err := json.Unmarshal(mock.published[0].Data, &decoded); err != nil {
					t.Fatalf("published data is not valid Event JSON: %v", err)
				}
				if decoded.ID != tt.event.ID {
					t.Errorf("expected event ID %q, got %q", tt.event.ID, decoded.ID)
				}
				if decoded.Type != tt.event.Type {
					t.Errorf("expected event type %q, got %q", tt.event.Type, decoded.Type)
				}
			}
		})
	}
}

func TestPublisher_EnsureStream(t *testing.T) {
	tests := []struct {
		name         string
		streamInfo   *nats.StreamInfo
		streamErr    error
		addStreamErr error
		wantErr      bool
	}{
		{
			name:       "stream already exists",
			streamInfo: &nats.StreamInfo{},
		},
		{
			name:      "creates stream when not found",
			streamErr: nats.ErrStreamNotFound,
		},
		{
			name:         "returns error when create fails",
			streamErr:    nats.ErrStreamNotFound,
			addStreamErr: fmt.Errorf("insufficient resources"),
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockJetStream{
				streamInfo:   tt.streamInfo,
				streamErr:    tt.streamErr,
				addStreamErr: tt.addStreamErr,
			}
			pub := NewPublisher(mock)

			err := pub.EnsureStream(context.Background(), "EVENTS")
			if (err != nil) != tt.wantErr {
				t.Fatalf("EnsureStream() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSubject(t *testing.T) {
	got := Subject("team-alpha", "sess-123")
	want := "events.team-alpha.sessions.sess-123"
	if got != want {
		t.Errorf("Subject() = %q, want %q", got, want)
	}
}
