package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultTimeout = 30 * time.Second

// SessionConfig holds the configuration for starting a new session.
type SessionConfig struct {
	// AgentType identifies the type of agent to use.
	AgentType string `json:"agentType"`

	// Prompt is the prompt to send to the agent.
	Prompt string `json:"prompt"`

	// ContextFiles is a list of file paths to provide as context.
	ContextFiles []string `json:"contextFiles,omitempty"`

	// PermissionMode controls how permission requests are handled.
	// Values: "bypass" (default), "autoApprove", "requireApproval".
	PermissionMode string `json:"permissionMode,omitempty"`
}

// SessionResponse is the response from the bridge when starting a session.
type SessionResponse struct {
	// SessionID is the unique identifier for the session.
	SessionID string `json:"sessionId"`
}

// HealthStatus represents the health of the bridge sidecar.
type HealthStatus struct {
	// Status is the health status (e.g., "healthy", "unhealthy").
	Status string `json:"status"`

	// ActiveSessions is the number of currently active sessions.
	ActiveSessions int `json:"activeSessions"`

	// Uptime is the bridge uptime in seconds.
	Uptime int64 `json:"uptime"`
}

// Client is an HTTP client for the bridge sidecar running in sandbox pods.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a new bridge client pointing at the given base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		baseURL:    baseURL,
	}
}

// NewClientWithHTTP creates a new bridge client with a custom HTTP client.
func NewClientWithHTTP(baseURL string, httpClient *http.Client) *Client {
	return &Client{
		httpClient: httpClient,
		baseURL:    baseURL,
	}
}

// StartSession starts a new agent session on the bridge.
func (c *Client) StartSession(ctx context.Context, cfg SessionConfig) (string, error) {
	body, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshaling session config: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sessions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating start session request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("starting session: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("starting session: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var result SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding start session response: %w", err)
	}

	return result.SessionID, nil
}

// SendMessage sends a message to an active session.
func (c *Client) SendMessage(ctx context.Context, sessionID string, msg string) error {
	payload := struct {
		Message string `json:"message"`
	}{Message: msg}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/sessions/%s/messages", c.baseURL, sessionID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating send message request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sending message: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// CancelSession cancels an active session.
func (c *Client) CancelSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/sessions/%s", c.baseURL, sessionID), nil)
	if err != nil {
		return fmt.Errorf("creating cancel session request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("cancelling session: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cancelling session: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetHealth checks the health of the bridge sidecar.
func (c *Client) GetHealth(ctx context.Context) (*HealthStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return nil, fmt.Errorf("creating health request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checking health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("checking health: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var health HealthStatus
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decoding health response: %w", err)
	}

	return &health, nil
}
