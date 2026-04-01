package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// acpTestServer creates a mock SDK server that speaks the ACP JSON-RPC protocol.
// It tracks calls for assertions.
type acpTestServer struct {
	mu             sync.Mutex
	writtenFiles   []string
	initialized    bool
	sessionCreated bool
	promptText     string
	deleted        bool
	followUpTexts  []string
	sseDone        chan struct{} // closed to release SSE handlers
}

func newACPTestServer() *acpTestServer {
	return &acpTestServer{sseDone: make(chan struct{})}
}

func (a *acpTestServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// File write endpoint (not ACP).
		if r.Method == http.MethodPost && r.URL.Path == "/v1/fs/file" {
			var req WriteFileRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			a.mu.Lock()
			a.writtenFiles = append(a.writtenFiles, req.Path)
			a.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			return
		}

		// ACP DELETE — close connection.
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/acp/") {
			a.mu.Lock()
			a.deleted = true
			a.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// ACP GET — SSE stream. Keep connection open until test signals done.
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/acp/") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-a.sseDone
			return
		}

		// ACP POST — JSON-RPC requests.
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/acp/") {
			var rpcReq jsonRPCRequest
			_ = json.NewDecoder(r.Body).Decode(&rpcReq)

			switch rpcReq.Method {
			case "initialize":
				a.mu.Lock()
				a.initialized = true
				a.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(jsonRPCResponse{
					JSONRPC: "2.0", ID: rpcReq.ID,
					Result: json.RawMessage(`{"protocolVersion":1}`),
				})
			case "session/new":
				a.mu.Lock()
				a.sessionCreated = true
				a.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(jsonRPCResponse{
					JSONRPC: "2.0", ID: rpcReq.ID,
					Result: json.RawMessage(`{"sessionId":"sess-test"}`),
				})
			case "session/prompt":
				// Extract prompt text from params.
				params, _ := json.Marshal(rpcReq.Params)
				var p struct {
					Prompt []struct {
						Text string `json:"text"`
					} `json:"prompt"`
				}
				_ = json.Unmarshal(params, &p)
				a.mu.Lock()
				if a.promptText == "" {
					if len(p.Prompt) > 0 {
						a.promptText = p.Prompt[0].Text
					}
				} else {
					if len(p.Prompt) > 0 {
						a.followUpTexts = append(a.followUpTexts, p.Prompt[0].Text)
					}
				}
				a.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(jsonRPCResponse{
					JSONRPC: "2.0", ID: rpcReq.ID,
					Result: json.RawMessage(`{"stopReason":"end_turn"}`),
				})
			default:
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(jsonRPCResponse{
					JSONRPC: "2.0", ID: rpcReq.ID,
					Error: &jsonRPCError{Code: -32601, Message: "Method not found"},
				})
			}
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}
}

func TestSessionManagerStartSession(t *testing.T) {
	mock := newACPTestServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()
	defer close(mock.sseDone)

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

	if !strings.HasPrefix(id, "bridge-") {
		t.Errorf("expected server ID starting with bridge-, got %s", id)
	}

	// Wait briefly for the async prompt goroutine to complete.
	time.Sleep(50 * time.Millisecond)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !mock.initialized {
		t.Error("expected ACP initialize to be called")
	}
	if !mock.sessionCreated {
		t.Error("expected ACP session/new to be called")
	}
	if mock.promptText != "write tests" {
		t.Errorf("expected prompt 'write tests', got %s", mock.promptText)
	}
	if len(mock.writtenFiles) != 1 || mock.writtenFiles[0] != "/workspace/CLAUDE.md" {
		t.Errorf("expected 1 file written to /workspace/CLAUDE.md, got %v", mock.writtenFiles)
	}

	if sm.ActiveSessionCount() != 1 {
		t.Errorf("expected 1 active session, got %d", sm.ActiveSessionCount())
	}
	if !sm.HasSession(id) {
		t.Errorf("expected session %q to be active", id)
	}
}

func TestSessionManagerSendMessage(t *testing.T) {
	mock := newACPTestServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()
	defer close(mock.sseDone)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	sdk := NewSDKClientWithHTTP(server.URL, server.Client())
	sm := NewSessionManager(sdk, logger)

	id, err := sm.StartSession(context.Background(), StartSessionConfig{
		AgentType: "claude-code",
		Prompt:    "initial",
	}, func(_ SSEEvent) {})
	if err != nil {
		t.Fatalf("unexpected error starting session: %v", err)
	}

	// Wait for the async initial prompt to complete before sending follow-up.
	time.Sleep(50 * time.Millisecond)

	err = sm.SendMessage(context.Background(), id, "follow-up")
	if err != nil {
		t.Fatalf("unexpected error sending message: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.followUpTexts) != 1 || mock.followUpTexts[0] != "follow-up" {
		t.Errorf("expected follow-up message 'follow-up', got %v", mock.followUpTexts)
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
	mock := newACPTestServer()
	server := httptest.NewServer(mock.handler())
	defer server.Close()
	defer close(mock.sseDone)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	sdk := NewSDKClientWithHTTP(server.URL, server.Client())
	sm := NewSessionManager(sdk, logger)

	id, err := sm.StartSession(context.Background(), StartSessionConfig{
		AgentType: "claude-code",
		Prompt:    "do something",
	}, func(_ SSEEvent) {})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Give the SSE goroutine a moment to start.
	time.Sleep(10 * time.Millisecond)

	err = sm.CancelSession(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error cancelling: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !mock.deleted {
		t.Error("expected DELETE to be called on SDK")
	}
	if sm.HasSession(id) {
		t.Error("expected session to be removed after cancel")
	}
}
