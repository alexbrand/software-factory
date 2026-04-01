package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewSDKClient(t *testing.T) {
	c := NewSDKClient("http://localhost:2468")
	if c.baseURL != "http://localhost:2468" {
		t.Errorf("expected baseURL http://localhost:2468, got %s", c.baseURL)
	}
}

func TestNewSDKClientTrimsTrailingSlash(t *testing.T) {
	c := NewSDKClient("http://localhost:2468/")
	if c.baseURL != "http://localhost:2468" {
		t.Errorf("expected baseURL http://localhost:2468, got %s", c.baseURL)
	}
}

func TestCreateACPSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/acp" {
			t.Errorf("expected /v1/acp, got %s", r.URL.Path)
		}

		var cfg ACPConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if cfg.Agent != "claude-code" {
			t.Errorf("expected agent claude-code, got %s", cfg.Agent)
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ACPSessionResponse{ServerID: "srv-123"})
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	id, err := client.CreateACPSession(context.Background(), ACPConfig{Agent: "claude-code"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "srv-123" {
		t.Errorf("expected srv-123, got %s", id)
	}
}

func TestCreateACPSessionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	_, err := client.CreateACPSession(context.Background(), ACPConfig{Agent: "test"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSendACPMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/acp/srv-123" {
			t.Errorf("expected /v1/acp/srv-123, got %s", r.URL.Path)
		}

		var msg ACPMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if msg.Message != "hello" {
			t.Errorf("expected message 'hello', got %s", msg.Message)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	err := client.SendACPMessage(context.Background(), "srv-123", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloseACPSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/v1/acp/srv-123" {
			t.Errorf("expected /v1/acp/srv-123, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	err := client.CloseACPSession(context.Background(), "srv-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/fs/file" {
			t.Errorf("expected /v1/fs/file, got %s", r.URL.Path)
		}

		var req WriteFileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if req.Path != "/workspace/CLAUDE.md" {
			t.Errorf("expected path /workspace/CLAUDE.md, got %s", req.Path)
		}
		if req.Content != "# Instructions" {
			t.Errorf("expected content '# Instructions', got %s", req.Content)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	err := client.WriteFile(context.Background(), "/workspace/CLAUDE.md", "# Instructions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSDKGetHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			t.Errorf("expected /v1/health, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(SDKHealthResponse{Status: "ok"})
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	health, err := client.GetHealth(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if health.Status != "ok" {
		t.Errorf("expected status ok, got %s", health.Status)
	}
}

func TestStreamACPEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/acp/srv-456" {
			t.Errorf("expected /v1/acp/srv-456, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		fmt.Fprint(w, "event: message\ndata: {\"text\":\"hello\"}\nid: ev-1\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: session.completed\ndata: {}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	ch, err := client.StreamACPEvents(context.Background(), "srv-456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var received []SSEEvent
	for ev := range ch {
		received = append(received, ev)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}
	if received[0].Event != "message" {
		t.Errorf("expected event 'message', got %s", received[0].Event)
	}
	if received[0].ID != "ev-1" {
		t.Errorf("expected ID 'ev-1', got %s", received[0].ID)
	}
	if received[1].Event != "session.completed" {
		t.Errorf("expected event 'session.completed', got %s", received[1].Event)
	}
}

func TestParseSSEStream(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []SSEEvent
	}{
		{
			name:  "single event",
			input: "event: test\ndata: hello\n\n",
			expected: []SSEEvent{
				{Event: "test", Data: "hello"},
			},
		},
		{
			name:  "multiline data",
			input: "data: line1\ndata: line2\n\n",
			expected: []SSEEvent{
				{Data: "line1\nline2"},
			},
		},
		{
			name:  "event with id",
			input: "id: 42\nevent: msg\ndata: content\n\n",
			expected: []SSEEvent{
				{ID: "42", Event: "msg", Data: "content"},
			},
		},
		{
			name:  "comment lines ignored",
			input: ": this is a comment\nevent: test\ndata: value\n\n",
			expected: []SSEEvent{
				{Event: "test", Data: "value"},
			},
		},
		{
			name:     "empty input",
			input:    "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan SSEEvent, 10)
			ctx := context.Background()
			r := stringReader(tt.input)
			parseSSEStream(ctx, r, ch)
			close(ch)

			var got []SSEEvent
			for ev := range ch {
				got = append(got, ev)
			}

			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d events, got %d", len(tt.expected), len(got))
			}
			for i, exp := range tt.expected {
				if got[i].Event != exp.Event {
					t.Errorf("event[%d]: expected Event %q, got %q", i, exp.Event, got[i].Event)
				}
				if got[i].Data != exp.Data {
					t.Errorf("event[%d]: expected Data %q, got %q", i, exp.Data, got[i].Data)
				}
				if got[i].ID != exp.ID {
					t.Errorf("event[%d]: expected ID %q, got %q", i, exp.ID, got[i].ID)
				}
			}
		})
	}
}

type stringReaderImpl struct {
	data []byte
	pos  int
}

func stringReader(s string) *stringReaderImpl {
	return &stringReaderImpl{data: []byte(s)}
}

func (r *stringReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
