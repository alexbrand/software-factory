package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/alexbrand/software-factory/pkg/events"
)

// mockJetStream implements events.JetStreamPublisher for testing.
type mockJetStream struct {
	published []publishedMsg
}

type publishedMsg struct {
	Subject string
	Data    []byte
}

func (m *mockJetStream) Publish(subj string, data []byte, _ ...interface{}) (interface{}, error) {
	m.published = append(m.published, publishedMsg{Subject: subj, Data: data})
	return nil, nil
}

func TestMapEventType(t *testing.T) {
	tests := []struct {
		input    string
		expected events.EventType
	}{
		{"session.started", events.EventSessionStarted},
		{"session_started", events.EventSessionStarted},
		{"session.completed", events.EventSessionCompleted},
		{"session.failed", events.EventSessionFailed},
		{"thinking", events.EventAgentThinking},
		{"agent.thinking", events.EventAgentThinking},
		{"message", events.EventAgentMessage},
		{"agent.message", events.EventAgentMessage},
		{"tool.call", events.EventToolCall},
		{"tool_call", events.EventToolCall},
		{"tool.result", events.EventToolResult},
		{"tool_result", events.EventToolResult},
		{"token.usage", events.EventTokenUsage},
		{"error", events.EventError},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapEventType(tt.input)
			if got != tt.expected {
				t.Errorf("mapEventType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestEventForwarderNormalize(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	// publisher is nil since we're testing normalizeEvent directly.
	ef := NewEventForwarder(nil, "test-ns", logger)

	t.Run("known event type", func(t *testing.T) {
		event := ef.normalizeEvent("sess-1", SSEEvent{
			Event: "agent.message",
			Data:  `{"text":"hello"}`,
			ID:    "ev-1",
		})
		if event == nil {
			t.Fatal("expected non-nil event")
		}
		if event.Type != events.EventAgentMessage {
			t.Errorf("expected type %s, got %s", events.EventAgentMessage, event.Type)
		}
		if event.SessionID != "sess-1" {
			t.Errorf("expected sessionID sess-1, got %s", event.SessionID)
		}
		if event.ID != "ev-1" {
			t.Errorf("expected ID ev-1, got %s", event.ID)
		}
	})

	t.Run("unknown event type", func(t *testing.T) {
		event := ef.normalizeEvent("sess-1", SSEEvent{
			Event: "unknown.event",
			Data:  "{}",
		})
		if event != nil {
			t.Error("expected nil for unknown event type")
		}
	})

	t.Run("empty data gets default", func(t *testing.T) {
		event := ef.normalizeEvent("sess-1", SSEEvent{
			Event: "error",
		})
		if event == nil {
			t.Fatal("expected non-nil event")
		}
		if string(event.Data) != "{}" {
			t.Errorf("expected empty JSON object, got %s", string(event.Data))
		}
	})

	t.Run("generates ID if missing", func(t *testing.T) {
		event := ef.normalizeEvent("sess-1", SSEEvent{
			Event: "error",
			Data:  "{}",
		})
		if event == nil {
			t.Fatal("expected non-nil event")
		}
		if event.ID == "" {
			t.Error("expected generated ID")
		}
	})
}

func TestEventForwarderForward(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// We need a real publisher backed by a mock JetStream.
	// Since events.Publisher uses nats.JetStreamContext which has specific types,
	// we test at the normalizeEvent level and trust the publisher's own tests.
	ef := NewEventForwarder(nil, "test-ns", logger)

	// Test MakeEventCallback doesn't panic with nil publisher.
	cb := ef.MakeEventCallback("sess-1")
	// Should not panic — ForwardEvent logs error but doesn't crash.
	cb(SSEEvent{Event: "agent.message", Data: `{"text":"hi"}`})
}

func TestEventForwarderEnsureStreamNilPublisher(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ef := NewEventForwarder(nil, "test-ns", logger)

	err := ef.EnsureStream(context.Background())
	if err == nil {
		t.Error("expected error with nil publisher")
	}
}

func TestNormalizeEventPreservesData(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ef := NewEventForwarder(nil, "test-ns", logger)

	data := `{"tool":"bash","args":["ls","-la"]}`
	event := ef.normalizeEvent("sess-1", SSEEvent{
		Event: "tool.call",
		Data:  data,
	})

	if event == nil {
		t.Fatal("expected non-nil event")
	}

	// Verify data is preserved as-is.
	var parsed map[string]interface{}
	if err := json.Unmarshal(event.Data, &parsed); err != nil {
		t.Fatalf("failed to parse event data: %v", err)
	}
	if parsed["tool"] != "bash" {
		t.Errorf("expected tool 'bash', got %v", parsed["tool"])
	}
}
