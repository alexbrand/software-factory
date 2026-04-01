package bridge

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	mock := newACPTestServer()
	sdkServer := httptest.NewServer(mock.handler())

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	sdk := NewSDKClientWithHTTP(sdkServer.URL, sdkServer.Client())
	sm := NewSessionManager(sdk, logger)
	srv := NewServer(sm, nil, logger)

	t.Cleanup(func() {
		close(mock.sseDone)
		sdkServer.Close()
	})

	return srv
}

func TestServerHealthz(t *testing.T) {
	srv := newTestServer(t)

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
	srv := newTestServer(t)
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
	srv := newTestServer(t)

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
	if !strings.HasPrefix(resp.SessionID, "bridge-") {
		t.Errorf("expected session ID starting with bridge-, got %s", resp.SessionID)
	}
}

func TestServerStartSessionMissingFields(t *testing.T) {
	srv := newTestServer(t)

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
	srv := newTestServer(t)

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

	var resp SessionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	sessionID := resp.SessionID

	// Send a message.
	msgBody, _ := json.Marshal(map[string]string{"message": "follow-up"})
	req = httptest.NewRequest(http.MethodPost, "/sessions/"+sessionID+"/messages", bytes.NewReader(msgBody))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", sessionID)
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

func TestServerSendMessageMissingBody(t *testing.T) {
	srv := newTestServer(t)

	// Start a session first.
	body, _ := json.Marshal(SessionConfig{AgentType: "claude-code", Prompt: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	var resp SessionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Send empty message.
	msgBody, _ := json.Marshal(map[string]string{"message": ""})
	req = httptest.NewRequest(http.MethodPost, "/sessions/"+resp.SessionID+"/messages", bytes.NewReader(msgBody))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", resp.SessionID)
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestServerCancelSession(t *testing.T) {
	srv := newTestServer(t)

	// Start a session.
	body, _ := json.Marshal(SessionConfig{AgentType: "claude-code", Prompt: "hello"})
	req := httptest.NewRequest(http.MethodPost, "/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp SessionResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Cancel the session.
	req = httptest.NewRequest(http.MethodDelete, "/sessions/"+resp.SessionID, nil)
	req.SetPathValue("id", resp.SessionID)
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestServerSendMessageNotFound(t *testing.T) {
	srv := newTestServer(t)

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
