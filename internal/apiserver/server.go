package apiserver

import (
	"log/slog"
	"net/http"
)

// Server is the API server.
type Server struct {
	handler http.Handler
	addr    string
}

// NewServer creates a new API server with all routes and middleware.
func NewServer(h *Handlers, addr string, logger *slog.Logger) *Server {
	mux := http.NewServeMux()

	// Workflow endpoints.
	mux.HandleFunc("POST /v1/workflows", h.CreateWorkflow)
	mux.HandleFunc("GET /v1/workflows/{id}", h.GetWorkflow)
	mux.HandleFunc("DELETE /v1/workflows/{id}", h.DeleteWorkflow)
	mux.HandleFunc("GET /v1/workflows/{id}/tasks", h.ListWorkflowTasks)

	// Task endpoints.
	mux.HandleFunc("POST /v1/tasks", h.CreateTask)
	mux.HandleFunc("GET /v1/tasks/{id}", h.GetTask)
	mux.HandleFunc("GET /v1/tasks/{id}/events", h.StreamTaskEvents)

	// Session endpoints.
	mux.HandleFunc("POST /v1/sessions", h.CreateSession)
	mux.HandleFunc("GET /v1/sessions/{id}/events", h.StreamSessionEvents)
	mux.HandleFunc("POST /v1/sessions/{id}/messages", h.SendMessage)
	mux.HandleFunc("DELETE /v1/sessions/{id}", h.DeleteSession)
	mux.HandleFunc("POST /v1/sessions/{id}/permissions/{permissionId}", h.ApprovePermission)

	// Pool endpoints.
	mux.HandleFunc("GET /v1/pools", h.ListPools)
	mux.HandleFunc("GET /v1/pools/{id}", h.GetPool)

	// Health check.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Apply middleware: recovery -> logging -> requestID -> routes.
	var handler http.Handler = mux
	handler = requestIDMiddleware(handler)
	handler = loggingMiddleware(logger)(handler)
	handler = recoveryMiddleware(logger)(handler)

	return &Server{
		handler: handler,
		addr:    addr,
	}
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.addr, s.handler)
}

// Handler returns the server's HTTP handler (useful for testing).
func (s *Server) Handler() http.Handler {
	return s.handler
}
