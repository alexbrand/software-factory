package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStartSession(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantID     string
		wantErr    bool
	}{
		{
			name: "successful start",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/sessions" {
					t.Errorf("expected /sessions, got %s", r.URL.Path)
				}
				var cfg SessionConfig
				if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
					t.Errorf("decoding request body: %v", err)
				}
				if cfg.Prompt != "test prompt" {
					t.Errorf("expected prompt 'test prompt', got %q", cfg.Prompt)
				}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(SessionResponse{SessionID: "sess-123"})
			},
			wantID: "sess-123",
		},
		{
			name: "server error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("internal error"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := NewClient(srv.URL)
			id, err := c.StartSession(context.Background(), SessionConfig{
				AgentType: "test",
				Prompt:    "test prompt",
			})

			if (err != nil) != tt.wantErr {
				t.Fatalf("StartSession() error = %v, wantErr %v", err, tt.wantErr)
			}
			if id != tt.wantID {
				t.Errorf("StartSession() = %q, want %q", id, tt.wantID)
			}
		})
	}
}

func TestSendMessage(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr bool
	}{
		{
			name: "successful send",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/sessions/sess-123/messages" {
					t.Errorf("expected /sessions/sess-123/messages, got %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusOK)
			},
		},
		{
			name: "not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("session not found"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := NewClient(srv.URL)
			err := c.SendMessage(context.Background(), "sess-123", "hello")

			if (err != nil) != tt.wantErr {
				t.Fatalf("SendMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCancelSession(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr bool
	}{
		{
			name: "successful cancel",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("expected DELETE, got %s", r.Method)
				}
				if r.URL.Path != "/sessions/sess-123" {
					t.Errorf("expected /sessions/sess-123, got %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusNoContent)
			},
		},
		{
			name: "server error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("error"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := NewClient(srv.URL)
			err := c.CancelSession(context.Background(), "sess-123")

			if (err != nil) != tt.wantErr {
				t.Fatalf("CancelSession() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetHealth(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    *HealthStatus
		wantErr bool
	}{
		{
			name: "healthy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if r.URL.Path != "/healthz" {
					t.Errorf("expected /healthz, got %s", r.URL.Path)
				}
				json.NewEncoder(w).Encode(HealthStatus{
					Status:         "healthy",
					ActiveSessions: 1,
					Uptime:         3600,
				})
			},
			want: &HealthStatus{
				Status:         "healthy",
				ActiveSessions: 1,
				Uptime:         3600,
			},
		},
		{
			name: "unhealthy status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := NewClient(srv.URL)
			got, err := c.GetHealth(context.Background())

			if (err != nil) != tt.wantErr {
				t.Fatalf("GetHealth() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.want != nil {
				if got.Status != tt.want.Status {
					t.Errorf("Status = %q, want %q", got.Status, tt.want.Status)
				}
				if got.ActiveSessions != tt.want.ActiveSessions {
					t.Errorf("ActiveSessions = %d, want %d", got.ActiveSessions, tt.want.ActiveSessions)
				}
				if got.Uptime != tt.want.Uptime {
					t.Errorf("Uptime = %d, want %d", got.Uptime, tt.want.Uptime)
				}
			}
		})
	}
}
