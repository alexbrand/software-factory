package testharness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// FakeSDK simulates the Sandbox Agent SDK's ACP endpoints for integration testing.
// Tests program it with behaviors, then the bridge talks to it as if it were a real SDK.
type FakeSDK struct {
	server *httptest.Server

	mu           sync.Mutex
	sessions     map[string]*fakeSession
	behavior     SessionBehavior
	writtenFiles []WriteRecord
	healthOK     bool
}

// WriteRecord tracks files written via the SDK /v1/fs/file endpoint.
type WriteRecord struct {
	Path    string
	Content string
}

// fakeSession tracks state of a single ACP session.
type fakeSession struct {
	serverID  string
	sessionID string
	agentType string
	prompts   []string
	sseEvents chan string
	sseDone   chan struct{}
	closed    bool
}

// SessionBehavior controls how the fake SDK responds to ACP requests.
type SessionBehavior struct {
	// SessionID to return from session/new. Auto-generated if empty.
	SessionID string

	// PromptDelay makes session/prompt block for this duration.
	PromptDelay time.Duration

	// PromptError, if non-nil, makes session/prompt return this JSON-RPC error.
	PromptError *JSONRPCError

	// Events are SSE-formatted strings pushed to the stream on session start.
	// Each should be a complete SSE block, e.g. "event: message\ndata: {...}\n\n"
	Events []string
}

// JSONRPCError represents a JSON-RPC error for fake responses.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// FakeSessionInfo exposes session state for test assertions.
type FakeSessionInfo struct {
	ServerID  string
	SessionID string
	AgentType string
	Prompts   []string
	Closed    bool
}

// NewFakeSDK creates and starts a new fake SDK server.
func NewFakeSDK() *FakeSDK {
	f := &FakeSDK{
		sessions: make(map[string]*fakeSession),
		healthOK: true,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

// URL returns the base URL of the fake SDK server.
func (f *FakeSDK) URL() string { return f.server.URL }

// Close shuts down the fake SDK server.
func (f *FakeSDK) Close() {
	f.mu.Lock()
	for _, s := range f.sessions {
		select {
		case <-s.sseDone:
		default:
			close(s.sseDone)
		}
	}
	f.mu.Unlock()
	f.server.Close()
}

// SetBehavior sets the default session behavior.
func (f *FakeSDK) SetBehavior(b SessionBehavior) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.behavior = b
}

// PushSSEEvent sends an SSE event to an active session's stream.
func (f *FakeSDK) PushSSEEvent(serverID string, sseText string) error {
	f.mu.Lock()
	sess, ok := f.sessions[serverID]
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %s not found", serverID)
	}
	select {
	case sess.sseEvents <- sseText:
		return nil
	case <-sess.sseDone:
		return fmt.Errorf("session %s already closed", serverID)
	}
}

// CloseSSEStream closes the SSE stream for a session (simulates agent completion).
func (f *FakeSDK) CloseSSEStream(serverID string) {
	f.mu.Lock()
	sess, ok := f.sessions[serverID]
	f.mu.Unlock()
	if ok {
		select {
		case <-sess.sseDone:
		default:
			close(sess.sseDone)
		}
	}
}

// WrittenFiles returns all files written via /v1/fs/file.
func (f *FakeSDK) WrittenFiles() []WriteRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]WriteRecord, len(f.writtenFiles))
	copy(out, f.writtenFiles)
	return out
}

// Sessions returns info about all sessions that were created.
func (f *FakeSDK) Sessions() []FakeSessionInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeSessionInfo, 0, len(f.sessions))
	for _, s := range f.sessions {
		prompts := make([]string, len(s.prompts))
		copy(prompts, s.prompts)
		out = append(out, FakeSessionInfo{
			ServerID:  s.serverID,
			SessionID: s.sessionID,
			AgentType: s.agentType,
			Prompts:   prompts,
			Closed:    s.closed,
		})
	}
	return out
}

// Prompts returns all prompts received across all sessions.
func (f *FakeSDK) Prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sessions {
		out = append(out, s.prompts...)
	}
	return out
}

// SessionServerIDs returns the server IDs of all sessions (useful for PushSSEEvent).
func (f *FakeSDK) SessionServerIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.sessions))
	for id := range f.sessions {
		ids = append(ids, id)
	}
	return ids
}

// handle dispatches incoming HTTP requests to the appropriate handler.
func (f *FakeSDK) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/fs/file":
		f.handleWriteFile(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
		f.handleHealth(w)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/acp/"):
		f.handleACPPost(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/acp/"):
		f.handleACPGet(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/acp/"):
		f.handleACPDelete(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *FakeSDK) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.writtenFiles = append(f.writtenFiles, WriteRecord{Path: req.Path, Content: req.Content})
	f.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (f *FakeSDK) handleHealth(w http.ResponseWriter) {
	f.mu.Lock()
	ok := f.healthOK
	f.mu.Unlock()
	status := "healthy"
	if !ok {
		status = "unhealthy"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (f *FakeSDK) handleACPPost(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimPrefix(r.URL.Path, "/v1/acp/")

	var rpcReq jsonRPCRequest
	_ = json.NewDecoder(r.Body).Decode(&rpcReq)

	switch rpcReq.Method {
	case "initialize":
		agentType := r.URL.Query().Get("agent")
		f.mu.Lock()
		sessID := f.behavior.SessionID
		if sessID == "" {
			sessID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		}
		f.sessions[serverID] = &fakeSession{
			serverID:  serverID,
			sessionID: sessID,
			agentType: agentType,
			sseEvents: make(chan string, 64),
			sseDone:   make(chan struct{}),
		}
		f.mu.Unlock()

		writeRPCResult(w, rpcReq.ID, `{"protocolVersion":1}`)

	case "session/new":
		f.mu.Lock()
		sess := f.sessions[serverID]
		sessID := ""
		if sess != nil {
			sessID = sess.sessionID
		}
		f.mu.Unlock()

		writeRPCResult(w, rpcReq.ID, fmt.Sprintf(`{"sessionId":"%s"}`, sessID))

	case "session/set_config_option":
		writeRPCResult(w, rpcReq.ID, `{}`)

	case "session/respond_permission":
		writeRPCResult(w, rpcReq.ID, `{}`)

	case "session/prompt":
		// Extract prompt text.
		params, _ := json.Marshal(rpcReq.Params)
		var p struct {
			Prompt []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		_ = json.Unmarshal(params, &p)

		f.mu.Lock()
		sess := f.sessions[serverID]
		delay := f.behavior.PromptDelay
		promptErr := f.behavior.PromptError
		if sess != nil && len(p.Prompt) > 0 {
			sess.prompts = append(sess.prompts, p.Prompt[0].Text)
		}
		f.mu.Unlock()

		if delay > 0 {
			time.Sleep(delay)
		}

		if promptErr != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: rpcReq.ID,
				Error: &jsonRPCError{Code: promptErr.Code, Message: promptErr.Message},
			})
			return
		}

		writeRPCResult(w, rpcReq.ID, `{"stopReason":"end_turn"}`)

	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: rpcReq.ID,
			Error: &jsonRPCError{Code: -32601, Message: "Method not found"},
		})
	}
}

func (f *FakeSDK) handleACPGet(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimPrefix(r.URL.Path, "/v1/acp/")

	f.mu.Lock()
	sess, ok := f.sessions[serverID]
	behavior := f.behavior
	f.mu.Unlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()

	// Send pre-configured events.
	for _, ev := range behavior.Events {
		_, _ = fmt.Fprint(w, ev)
		flusher.Flush()
	}

	// Stream events pushed by test code until done.
	for {
		select {
		case ev, open := <-sess.sseEvents:
			if !open {
				return
			}
			_, _ = fmt.Fprint(w, ev)
			flusher.Flush()
		case <-sess.sseDone:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (f *FakeSDK) handleACPDelete(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimPrefix(r.URL.Path, "/v1/acp/")

	f.mu.Lock()
	sess, ok := f.sessions[serverID]
	if ok {
		sess.closed = true
		select {
		case <-sess.sseDone:
		default:
			close(sess.sseDone)
		}
	}
	f.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func writeRPCResult(w http.ResponseWriter, id int64, result string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  json.RawMessage(result),
	})
}
