package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultSDKTimeout = 30 * time.Second

// SDKClient is an HTTP client for the Sandbox Agent SDK API.
type SDKClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewSDKClient creates a new SDK client pointing at the given base URL.
func NewSDKClient(baseURL string) *SDKClient {
	return &SDKClient{
		httpClient: &http.Client{Timeout: defaultSDKTimeout},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// NewSDKClientWithHTTP creates a new SDK client with a custom HTTP client.
func NewSDKClientWithHTTP(baseURL string, httpClient *http.Client) *SDKClient {
	return &SDKClient{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// ACPConfig holds configuration for creating an ACP session.
type ACPConfig struct {
	Agent   string `json:"agent"`
	WorkDir string `json:"workDir,omitempty"`
}

// ACPSessionResponse is the response from creating an ACP session.
type ACPSessionResponse struct {
	ServerID string `json:"serverId"`
}

// ACPMessage is a message to send to an ACP session.
type ACPMessage struct {
	Message string `json:"message"`
}

// WriteFileRequest is the request body for writing a file via the SDK.
type WriteFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// SDKHealthResponse is the response from the SDK health endpoint.
type SDKHealthResponse struct {
	Status string `json:"status"`
}

// SSEEvent represents a parsed server-sent event.
type SSEEvent struct {
	Event string
	Data  string
	ID    string
}

// CreateACPSession creates a new ACP session on the SDK.
func (c *SDKClient) CreateACPSession(ctx context.Context, cfg ACPConfig) (string, error) {
	body, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshaling ACP config: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/acp", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating ACP session request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("creating ACP session: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("creating ACP session: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var result ACPSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding ACP session response: %w", err)
	}

	return result.ServerID, nil
}

// SendACPMessage sends a message to an active ACP session.
func (c *SDKClient) SendACPMessage(ctx context.Context, serverID string, msg string) error {
	payload := ACPMessage{Message: msg}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling ACP message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v1/acp/%s", c.baseURL, serverID), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating send ACP message request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending ACP message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sending ACP message: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// StreamACPEvents opens an SSE connection to stream ACP events.
// It returns a channel that emits SSE events until the context is cancelled or the stream ends.
func (c *SDKClient) StreamACPEvents(ctx context.Context, serverID string) (<-chan SSEEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/acp/%s", c.baseURL, serverID), nil)
	if err != nil {
		return nil, fmt.Errorf("creating SSE stream request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	// Use a client without timeout for long-lived SSE connections.
	sseClient := &http.Client{}
	resp, err := sseClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opening SSE stream: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("opening SSE stream: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan SSEEvent, 64)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		parseSSEStream(ctx, resp.Body, ch)
	}()

	return ch, nil
}

// parseSSEStream reads SSE events from a reader and sends them to the channel.
func parseSSEStream(ctx context.Context, r io.Reader, ch chan<- SSEEvent) {
	scanner := bufio.NewScanner(r)
	var current SSEEvent

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()

		if line == "" {
			// Empty line marks end of event.
			if current.Data != "" || current.Event != "" {
				ch <- current
				current = SSEEvent{}
			}
			continue
		}

		if strings.HasPrefix(line, "event:") {
			current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			if current.Data != "" {
				current.Data += "\n"
			}
			current.Data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		} else if strings.HasPrefix(line, "id:") {
			current.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
		// Lines starting with ':' are comments, ignored.
	}

	// Emit any remaining event.
	if current.Data != "" || current.Event != "" {
		ch <- current
	}
}

// CloseACPSession closes an ACP session.
func (c *SDKClient) CloseACPSession(ctx context.Context, serverID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/v1/acp/%s", c.baseURL, serverID), nil)
	if err != nil {
		return fmt.Errorf("creating close ACP session request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("closing ACP session: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("closing ACP session: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// WriteFile writes a file to the sandbox filesystem via the SDK.
func (c *SDKClient) WriteFile(ctx context.Context, path string, content string) error {
	payload := WriteFileRequest{Path: path, Content: content}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling write file request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/fs/file", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating write file request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("writing file: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetHealth checks the health of the SDK.
func (c *SDKClient) GetHealth(ctx context.Context) (*SDKHealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/health", nil)
	if err != nil {
		return nil, fmt.Errorf("creating SDK health request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checking SDK health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("checking SDK health: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var health SDKHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, fmt.Errorf("decoding SDK health response: %w", err)
	}

	return &health, nil
}
