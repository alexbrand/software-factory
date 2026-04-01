package bridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()

	sdkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/file":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(ACPSessionResponse{ServerID: "test-session"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/acp/test-session":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/acp/test-session":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/acp/test-session":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	sdk := NewSDKClientWithHTTP(sdkServer.URL, sdkServer.Client())
	sm := NewSessionManager(sdk, logger)
	srv := NewServer(sm, nil, logger)

	t.Cleanup(func() { sdkServer.Close() })

	return srv, sdkServer
}

func TestServerHealthz(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var health HealthStatus
	if err := json.NewDecoder(w.Body).Decode(&health); err != nil {
		t.Fatalf("decoding health response: %v", err)
	}
	if health.Status != "healthy" {
		t.Errorf("expected healthy, got %s", health.Status)
	}
	if health.ActiveSessions != 0 {
		t.Errorf("expected 0 active sessions, got %d", health.ActiveSessions)
	}
}

func TestServerHealthzUnhealthy(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.SetSDKHealthy(false)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var health HealthStatus
	_ = json.NewDecoder(w.Body).Decode(&health)
	if health.Status != "unhealthy" {
		t.Errorf("expected unhealthy, got %s", health.Status)
	}
}

func TestServerStartSession(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(SessionConfig{
		AgentType: "claude-code",
		Prompt:    "hello",
	})

	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp SessionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.SessionID != "test-session" {
		t.Errorf("expected session ID 'test-session', got %s", resp.SessionID)
	}
}

func TestServerStartSessionMissingFields(t *testing.T) {
	srv, _ := newTestServer(t)

	tests := []struct {
		name string
		body SessionConfig
	}{
		{"missing agentType", SessionConfig{Prompt: "hello"}},
		{"missing prompt", SessionConfig{AgentType: "claude-code"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", w.Code)
			}
		})
	}
}

func TestServerSendMessage(t *testing.T) {
	srv, _ := newTestServer(t)

	// First start a session.
	body, _ := json.Marshal(SessionConfig{
		AgentType: "claude-code",
		Prompt:    "hello",
	})
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Send a message.
	msgBody, _ := json.Marshal(map[string]string{"message": "follow-up"})
	req = httptest.NewRequest(http.MethodPost, "/sessions/test-session/messages", bytes.NewReader(msgBody))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-session")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

func TestServerSendMessageMissingBody(t *testing.T) {
	srv, _ := newTestServer(t)

	// Start a session first.
	body, _ := json.Marshal(SessionConfig{AgentType: "claude-code", Prompt: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	// Send empty message.
	msgBody, _ := json.Marshal(map[string]string{"message": ""})
	req = httptest.NewRequest(http.MethodPost, "/sessions/test-session/messages", bytes.NewReader(msgBody))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "test-session")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestServerCancelSession(t *testing.T) {
	srv, _ := newTestServer(t)

	// Start a session.
	body, _ := json.Marshal(SessionConfig{AgentType: "claude-code", Prompt: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Cancel the session.
	req = httptest.NewRequest(http.MethodDelete, "/sessions/test-session", nil)
	req.SetPathValue("id", "test-session")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestServerSendMessageNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	msgBody, _ := json.Marshal(map[string]string{"message": "hello"})
	req := httptest.NewRequest(http.MethodPost, "/sessions/nonexistent/messages", bytes.NewReader(msgBody))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
