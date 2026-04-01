package bridge

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server exposes HTTP endpoints for the Session Controller to call.
type Server struct {
	sessionManager *SessionManager
	eventForwarder *EventForwarder
	logger         *slog.Logger
	startTime      time.Time
	httpServer     *http.Server

	mu         sync.RWMutex
	sdkHealthy bool
}

// NewServer creates a new bridge HTTP server.
func NewServer(sm *SessionManager, ef *EventForwarder, logger *slog.Logger) *Server {
	return &Server{
		sessionManager: sm,
		eventForwarder: ef,
		logger:         logger,
		startTime:      time.Now(),
		sdkHealthy:     true,
	}
}

// SetSDKHealthy updates the SDK health state for the /healthz endpoint.
func (s *Server) SetSDKHealthy(healthy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sdkHealthy = healthy
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sessions", s.handleStartSession)
	mux.HandleFunc("POST /sessions/{id}/messages", s.handleSendMessage)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleCancelSession)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return mux
}

// Start starts the HTTP server on the given address.
func (s *Server) Start(addr string) error {
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.logger.Info("starting bridge server", "addr", addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown() error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Close()
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	var req SessionConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	if req.AgentType == "" {
		writeError(w, http.StatusBadRequest, "agentType is required")
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	// Build context files.
	var ctxFiles []ContextFile
	for _, path := range req.ContextFiles {
		ctxFiles = append(ctxFiles, ContextFile{Path: path})
	}

	cfg := StartSessionConfig{
		AgentType:    req.AgentType,
		Prompt:       req.Prompt,
		ContextFiles: ctxFiles,
	}

	onEvent := func(SSEEvent) {}
	_ = s.eventForwarder // TODO: wire up event forwarding with actual session ID

	sessionID, err := s.sessionManager.StartSession(r.Context(), cfg, onEvent)
	if err != nil {
		s.logger.Error("failed to start session", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start session: %v", err)
		return
	}

	// Now wire up real event forwarding with the actual session ID.
	if s.eventForwarder != nil {
		// Events are already flowing from StreamACPEvents. The onEvent callback
		// above was a no-op placeholder. Future events will use the forwarder
		// since we re-register in the session manager isn't needed — the goroutine
		// is already running. For the bridge architecture, events are forwarded
		// by the EventForwarder through its MakeEventCallback, but since we need
		// the session ID first, we set a no-op and rely on session.go's goroutine.
		// In practice, the session manager should use the forwarder callback.
		_ = sessionID
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(SessionResponse{SessionID: sessionID})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := extractSessionID(r)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session ID is required")
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %v", err)
		return
	}

	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	if err := s.sessionManager.SendMessage(r.Context(), sessionID, req.Message); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "session not found: %s", sessionID)
			return
		}
		s.logger.Error("failed to send message", "sessionID", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send message: %v", err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleCancelSession(w http.ResponseWriter, r *http.Request) {
	sessionID := extractSessionID(r)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session ID is required")
		return
	}

	if err := s.sessionManager.CancelSession(r.Context(), sessionID); err != nil {
		s.logger.Error("failed to cancel session", "sessionID", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to cancel session: %v", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	healthy := s.sdkHealthy
	s.mu.RUnlock()

	status := "healthy"
	if !healthy {
		status = "unhealthy"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(HealthStatus{
		Status:         status,
		ActiveSessions: s.sessionManager.ActiveSessionCount(),
		Uptime:         int64(time.Since(s.startTime).Seconds()),
	})
}

// extractSessionID extracts the session ID from the URL path.
func extractSessionID(r *http.Request) string {
	return r.PathValue("id")
}

func writeError(w http.ResponseWriter, code int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": fmt.Sprintf(format, args...),
	})
}
