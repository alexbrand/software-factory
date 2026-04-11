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
	"sync"
	"sync/atomic"
	"time"
)

const defaultSDKTimeout = 30 * time.Second

// SDKClient is an HTTP client for the Sandbox Agent SDK's ACP protocol.
// It communicates using JSON-RPC 2.0 over HTTP, following the ACP spec:
//   - POST /v1/acp/{server_id}?agent=<agent>  — initialize + session/new + session/prompt
//   - GET  /v1/acp/{server_id}                 — SSE event stream
//   - DELETE /v1/acp/{server_id}               — close connection
type SDKClient struct {
	httpClient *http.Client
	baseURL    string
	nextID     atomic.Int64
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
	Agent          string `json:"agent"`
	WorkDir        string `json:"workDir,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
}

// ACPSessionResponse is the response from creating an ACP session.
// ServerID is the ACP connection ID (chosen by the bridge).
// SessionID is the agent-assigned session ID from session/new.
type ACPSessionResponse struct {
	ServerID  string `json:"serverId"`
	SessionID string `json:"sessionId"`
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

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError is a JSON-RPC 2.0 error.
type jsonRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// sendRPC sends a JSON-RPC request using the default HTTP client.
func (c *SDKClient) sendRPC(ctx context.Context, url string, rpcReq jsonRPCRequest) (*jsonRPCResponse, error) {
	return c.sendRPCWith(ctx, c.httpClient, url, rpcReq)
}

// sendRPCWith sends a JSON-RPC request using the given HTTP client.
func (c *SDKClient) sendRPCWith(ctx context.Context, httpClient *http.Client, url string, rpcReq jsonRPCRequest) (*jsonRPCResponse, error) {
	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling JSON-RPC request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating JSON-RPC request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending JSON-RPC request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decoding JSON-RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return &rpcResp, nil
}

// CreateACPSession initializes an ACP connection and creates a new agent session.
// It performs the two-step ACP handshake: initialize → session/new.
// Returns the server ID (ACP connection ID) used for subsequent requests.
func (c *SDKClient) CreateACPSession(ctx context.Context, cfg ACPConfig) (string, error) {
	serverID := fmt.Sprintf("bridge-%d", time.Now().UnixNano())
	acpURL := fmt.Sprintf("%s/v1/acp/%s", c.baseURL, serverID)

	// Step 1: Initialize the ACP connection.
	initResp, err := c.sendRPC(ctx, acpURL+"?agent="+cfg.Agent, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID.Add(1),
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": 1,
			"capabilities":   map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "software-factory-bridge",
				"version": "1.0.0",
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("ACP initialize: %w", err)
	}
	_ = initResp

	// Step 2: Create a new session.
	cwd := cfg.WorkDir
	if cwd == "" {
		cwd = "/workspace"
	}
	sessionParams := map[string]interface{}{
		"mcpServers": []interface{}{},
		"cwd":        cwd,
	}

	newResp, err := c.sendRPC(ctx, acpURL, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID.Add(1),
		Method:  "session/new",
		Params:  sessionParams,
	})
	if err != nil {
		return "", fmt.Errorf("ACP session/new: %w", err)
	}

	// Extract the agent-assigned session ID.
	var sessionResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newResp.Result, &sessionResult); err != nil {
		return "", fmt.Errorf("decoding session/new result: %w", err)
	}

	c.setSessionID(serverID, sessionResult.SessionID)

	// Step 3: Set permission mode. In bypass mode (default), set bypassPermissions
	// so tool calls don't block. In autoApprove/requireApproval modes, skip this
	// so the SDK emits permission request events.
	if cfg.PermissionMode == "" || cfg.PermissionMode == "bypass" {
		_, err = c.sendRPC(ctx, acpURL, jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      c.nextID.Add(1),
			Method:  "session/set_config_option",
			Params: map[string]interface{}{
				"sessionId": sessionResult.SessionID,
				"configId":  "mode",
				"value":     "bypassPermissions",
			},
		})
		if err != nil {
			// Non-fatal — the session can still work, just with permission prompts.
			_ = err
		}
	}

	return serverID, nil
}

// sessionIDMap maps server IDs to ACP session IDs. This is a simple in-process
// mapping since each bridge sidecar runs a small number of concurrent sessions.
var sessionIDMap = struct {
	sync.RWMutex
	m map[string]string
}{m: make(map[string]string)}

func (c *SDKClient) setSessionID(serverID, sessionID string) {
	sessionIDMap.Lock()
	sessionIDMap.m[serverID] = sessionID
	sessionIDMap.Unlock()
}

func (c *SDKClient) getSessionID(serverID string) string {
	sessionIDMap.RLock()
	defer sessionIDMap.RUnlock()
	return sessionIDMap.m[serverID]
}

// SendACPMessage sends a prompt to an active ACP session using the session/prompt method.
// This uses a client without timeout because the agent may take minutes to process.
func (c *SDKClient) SendACPMessage(ctx context.Context, serverID string, msg string) error {
	acpURL := fmt.Sprintf("%s/v1/acp/%s", c.baseURL, serverID)
	sessionID := c.getSessionID(serverID)

	_, err := c.sendRPCWith(ctx, &http.Client{}, acpURL, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextID.Add(1),
		Method:  "session/prompt",
		Params: map[string]interface{}{
			"sessionId": sessionID,
			"prompt": []map[string]string{
				{"type": "text", "text": msg},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("ACP session/prompt: %w", err)
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

		if strings.HasPrefix(line, ":") {
			// Comment line (heartbeat), ignore.
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
	}

	// Emit any remaining event.
	if current.Data != "" || current.Event != "" {
		ch <- current
	}
}

// CloseACPSession closes an ACP connection.
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

	// Clean up session ID mapping.
	sessionIDMap.Lock()
	delete(sessionIDMap.m, serverID)
	sessionIDMap.Unlock()

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
