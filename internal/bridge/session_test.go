package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestSessionManagerStartSession(t *testing.T) {
	var mu sync.Mutex
	var writtenFiles []string
	var createdSession bool
	var sentMessage string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/file":
			var req WriteFileRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			writtenFiles = append(writtenFiles, req.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp":
			mu.Lock()
			createdSession = true
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ACPSessionResponse{ServerID: "test-srv"})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp/test-srv":
			var msg ACPMessage
			_ = json.NewDecoder(r.Body).Decode(&msg)
			mu.Lock()
			sentMessage = msg.Message
			mu.Unlock()
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/test-srv":
			// SSE stream — return immediately to unblock.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	sdk := NewSDKClientWithHTTP(server.URL, server.Client())
	sm := NewSessionManager(sdk, logger)

	var receivedEvents []SSEEvent
	var eventMu sync.Mutex

	id, err := sm.StartSession(context.Background(), StartSessionConfig{
		AgentType: "claude-code",
		Prompt:    "write tests",
		ContextFiles: []ContextFile{
			{Path: "/workspace/CLAUDE.md", Content: "# Instructions"},
		},
		WorkDir: "/workspace",
	}, func(ev SSEEvent) {
		eventMu.Lock()
		receivedEvents = append(receivedEvents, ev)
		eventMu.Unlock()
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if id != "test-srv" {
		t.Errorf("expected session ID 'test-srv', got %s", id)
	}

	mu.Lock()
	if !createdSession {
		t.Error("expected ACP session to be created")
	}
	if sentMessage != "write tests" {
		t.Errorf("expected prompt 'write tests', got %s", sentMessage)
	}
	if len(writtenFiles) != 1 || writtenFiles[0] != "/workspace/CLAUDE.md" {
		t.Errorf("expected 1 file written to /workspace/CLAUDE.md, got %v", writtenFiles)
	}
	mu.Unlock()

	if sm.ActiveSessionCount() != 1 {
		t.Errorf("expected 1 active session, got %d", sm.ActiveSessionCount())
	}
	if !sm.HasSession("test-srv") {
		t.Error("expected session 'test-srv' to be active")
	}
}

func TestSessionManagerSendMessage(t *testing.T) {
	var receivedMessage string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ACPSessionResponse{ServerID: "srv-msg"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp/srv-msg":
			var msg ACPMessage
			_ = json.NewDecoder(r.Body).Decode(&msg)
			receivedMessage = msg.Message
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/srv-msg":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	sdk := NewSDKClientWithHTTP(server.URL, server.Client())
	sm := NewSessionManager(sdk, logger)

	_, err := sm.StartSession(context.Background(), StartSessionConfig{
		AgentType: "claude-code",
		Prompt:    "initial",
	}, func(_ SSEEvent) {})
	if err != nil {
		t.Fatalf("unexpected error starting session: %v", err)
	}

	err = sm.SendMessage(context.Background(), "srv-msg", "follow-up")
	if err != nil {
		t.Fatalf("unexpected error sending message: %v", err)
	}
	if receivedMessage != "follow-up" {
		t.Errorf("expected message 'follow-up', got %s", receivedMessage)
	}
}

func TestSessionManagerSendMessageNotFound(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	sdk := NewSDKClient("http://localhost:9999")
	sm := NewSessionManager(sdk, logger)

	err := sm.SendMessage(context.Background(), "nonexistent", "hello")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestSessionManagerCancelSession(t *testing.T) {
	var deleted bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ACPSessionResponse{ServerID: "srv-cancel"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp/srv-cancel":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/srv-cancel":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			// Keep the connection open briefly.
			time.Sleep(50 * time.Millisecond)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/acp/srv-cancel":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	sdk := NewSDKClientWithHTTP(server.URL, server.Client())
	sm := NewSessionManager(sdk, logger)

	_, err := sm.StartSession(context.Background(), StartSessionConfig{
		AgentType: "claude-code",
		Prompt:    "do something",
	}, func(_ SSEEvent) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = sm.CancelSession(context.Background(), "srv-cancel")
	if err != nil {
		t.Fatalf("unexpected error cancelling: %v", err)
	}
	if !deleted {
		t.Error("expected DELETE to be called on SDK")
	}
	if sm.HasSession("srv-cancel") {
		t.Error("expected session to be removed after cancel")
	}
}
