package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSDKClient(t *testing.T) {
	c := NewSDKClient("http://localhost:2468")
	if c.baseURL != "http://localhost:2468" {
		t.Errorf("expected baseURL http://localhost:2468, got %s", c.baseURL)
	}
}

func TestNewSDKClientTrimsTrailingSlash(t *testing.T) {
	c := NewSDKClient("http://localhost:2468/")
	if c.baseURL != "http://localhost:2468" {
		t.Errorf("expected baseURL http://localhost:2468, got %s", c.baseURL)
	}
}

func TestCreateACPSession(t *testing.T) {
	var callCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/acp/bridge-") {
			t.Errorf("expected /v1/acp/bridge-*, got %s", r.URL.Path)
		}

		var rpcReq jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}

		callCount++
		switch rpcReq.Method {
		case "initialize":
			if r.URL.Query().Get("agent") != "claude-code" {
				t.Errorf("expected agent=claude-code query param, got %s", r.URL.Query().Get("agent"))
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result:  json.RawMessage(`{"protocolVersion":1}`),
			})
		case "session/new":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result:  json.RawMessage(`{"sessionId":"sess-abc"}`),
			})
		case "session/set_config_option":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result:  json.RawMessage(`{}`),
			})
		default:
			t.Errorf("unexpected method: %s", rpcReq.Method)
		}
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	id, err := client.CreateACPSession(context.Background(), ACPConfig{Agent: "claude-code"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(id, "bridge-") {
		t.Errorf("expected server ID starting with bridge-, got %s", id)
	}
	if callCount != 3 {
		t.Errorf("expected 3 RPC calls (initialize + session/new + set_config_option), got %d", callCount)
	}
	// Verify session ID was stored.
	if client.getSessionID(id) != "sess-abc" {
		t.Errorf("expected stored session ID sess-abc, got %s", client.getSessionID(id))
	}
}

func TestCreateACPSession_Authenticate(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")

	var sawAuthenticate bool
	var authParams map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		switch rpc.Method {
		case "initialize":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: rpc.ID,
				Result: json.RawMessage(`{
					"protocolVersion": 1,
					"authMethods": [
						{"id":"chatgpt","name":"Login with ChatGPT"},
						{"id":"openai-api-key","type":"env_var","vars":[{"name":"OPENAI_API_KEY"}]}
					]
				}`),
			})
		case "authenticate":
			sawAuthenticate = true
			authParams = rpc.Params.(map[string]interface{})
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{}`)})
		case "session/new":
			if !sawAuthenticate {
				t.Errorf("session/new called before authenticate")
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{"sessionId":"s"}`)})
		case "session/set_config_option":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{}`)})
		}
	}))
	defer server.Close()

	c := NewSDKClientWithHTTP(server.URL, server.Client())
	if _, err := c.CreateACPSession(context.Background(), ACPConfig{Agent: "codex"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawAuthenticate {
		t.Fatal("expected authenticate to be called")
	}
	if got := authParams["methodId"]; got != "openai-api-key" {
		t.Errorf("methodId = %v, want openai-api-key", got)
	}
}

func TestCreateACPSession_NoAuthMethods(t *testing.T) {
	// Claude: empty authMethods list — should NOT call authenticate.
	var sawAuthenticate bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		switch rpc.Method {
		case "initialize":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{"protocolVersion":1,"authMethods":[]}`)})
		case "authenticate":
			sawAuthenticate = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{}`)})
		case "session/new":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{"sessionId":"s"}`)})
		case "session/set_config_option":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Result: json.RawMessage(`{}`)})
		}
	}))
	defer server.Close()

	c := NewSDKClientWithHTTP(server.URL, server.Client())
	if _, err := c.CreateACPSession(context.Background(), ACPConfig{Agent: "claude"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawAuthenticate {
		t.Error("authenticate must not be called when authMethods is empty")
	}
}

func TestChooseAuthMethod(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("CODEX_API_KEY", "")

	tests := []struct {
		name string
		init string
		want string
	}{
		{"empty list", `{"authMethods":[]}`, ""},
		{"only chatgpt", `{"authMethods":[{"id":"chatgpt","name":"Login"}]}`, ""},
		{"prefer env var with set value", `{"authMethods":[
			{"id":"codex-api-key","type":"env_var","vars":[{"name":"CODEX_API_KEY"}]},
			{"id":"openai-api-key","type":"env_var","vars":[{"name":"OPENAI_API_KEY"}]}
		]}`, "openai-api-key"},
		{"fall back to first env_var when none set", `{"authMethods":[
			{"id":"chatgpt"},
			{"id":"codex-api-key","type":"env_var","vars":[{"name":"CODEX_API_KEY"}]}
		]}`, "codex-api-key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseAuthMethod(json.RawMessage(tc.init))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCreateACPSessionForwardsMCPServers(t *testing.T) {
	var sessionNewParams map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		switch rpcReq.Method {
		case "initialize":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpcReq.ID, Result: json.RawMessage(`{"protocolVersion":1}`)})
		case "session/new":
			sessionNewParams = rpcReq.Params.(map[string]interface{})
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpcReq.ID, Result: json.RawMessage(`{"sessionId":"sess-mcp"}`)})
		case "session/set_config_option":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpcReq.ID, Result: json.RawMessage(`{}`)})
		}
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	_, err := client.CreateACPSession(context.Background(), ACPConfig{
		Agent: "claude-code",
		MCPServers: []MCPServer{
			{Name: "github", URL: "http://github-mcp.svc:8080"},
			{Name: "postgres", URL: "https://postgres-mcp.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := sessionNewParams["mcpServers"].([]interface{})
	if !ok {
		t.Fatalf("expected mcpServers to be a JSON array, got %T", sessionNewParams["mcpServers"])
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 mcpServers, got %d", len(got))
	}
	first := got[0].(map[string]interface{})
	if first["name"] != "github" || first["url"] != "http://github-mcp.svc:8080" {
		t.Errorf("first mcpServer = %+v, want {name:github, url:http://github-mcp.svc:8080}", first)
	}
}

func TestCreateACPSessionEmptyMCPServers(t *testing.T) {
	var sessionNewParams map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		switch rpcReq.Method {
		case "initialize":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpcReq.ID, Result: json.RawMessage(`{"protocolVersion":1}`)})
		case "session/new":
			sessionNewParams = rpcReq.Params.(map[string]interface{})
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpcReq.ID, Result: json.RawMessage(`{"sessionId":"sess"}`)})
		case "session/set_config_option":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: rpcReq.ID, Result: json.RawMessage(`{}`)})
		}
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	if _, err := client.CreateACPSession(context.Background(), ACPConfig{Agent: "x"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nil MCPServers must marshal to [] not null — the SDK rejects null here.
	got, ok := sessionNewParams["mcpServers"].([]interface{})
	if !ok {
		t.Fatalf("expected mcpServers to be a JSON array, got %T (%v)", sessionNewParams["mcpServers"], sessionNewParams["mcpServers"])
	}
	if len(got) != 0 {
		t.Errorf("expected empty mcpServers array, got %v", got)
	}
}

func TestCreateACPSessionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	_, err := client.CreateACPSession(context.Background(), ACPConfig{Agent: "test"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSendACPMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/acp/srv-123" {
			t.Errorf("expected /v1/acp/srv-123, got %s", r.URL.Path)
		}

		var rpcReq jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if rpcReq.Method != "session/prompt" {
			t.Errorf("expected method session/prompt, got %s", rpcReq.Method)
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      rpcReq.ID,
			Result:  json.RawMessage(`{"stopReason":"end_turn"}`),
		})
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	client.setSessionID("srv-123", "sess-abc")
	err := client.SendACPMessage(context.Background(), "srv-123", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloseACPSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/v1/acp/srv-123" {
			t.Errorf("expected /v1/acp/srv-123, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	err := client.CloseACPSession(context.Background(), "srv-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/fs/file" {
			t.Errorf("expected /v1/fs/file, got %s", r.URL.Path)
		}

		var req WriteFileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if req.Path != "/workspace/CLAUDE.md" {
			t.Errorf("expected path /workspace/CLAUDE.md, got %s", req.Path)
		}
		if req.Content != "# Instructions" {
			t.Errorf("expected content '# Instructions', got %s", req.Content)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	err := client.WriteFile(context.Background(), "/workspace/CLAUDE.md", "# Instructions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSDKGetHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			t.Errorf("expected /v1/health, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(SDKHealthResponse{Status: "ok"})
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	health, err := client.GetHealth(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if health.Status != "ok" {
		t.Errorf("expected status ok, got %s", health.Status)
	}
}

func TestStreamACPEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/acp/srv-456" {
			t.Errorf("expected /v1/acp/srv-456, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		_, _ = fmt.Fprint(w, "event: message\ndata: {\"text\":\"hello\"}\nid: ev-1\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "event: message\ndata: {\"method\":\"session/update\"}\nid: ev-2\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	client := NewSDKClientWithHTTP(server.URL, server.Client())
	ch, err := client.StreamACPEvents(context.Background(), "srv-456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var received []SSEEvent
	for ev := range ch {
		received = append(received, ev)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}
	if received[0].Event != "message" {
		t.Errorf("expected event 'message', got %s", received[0].Event)
	}
	if received[0].ID != "ev-1" {
		t.Errorf("expected ID 'ev-1', got %s", received[0].ID)
	}
}

func TestParseSSEStream(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []SSEEvent
	}{
		{
			name:  "single event",
			input: "event: test\ndata: hello\n\n",
			expected: []SSEEvent{
				{Event: "test", Data: "hello"},
			},
		},
		{
			name:  "multiline data",
			input: "data: line1\ndata: line2\n\n",
			expected: []SSEEvent{
				{Data: "line1\nline2"},
			},
		},
		{
			name:  "event with id",
			input: "id: 42\nevent: msg\ndata: content\n\n",
			expected: []SSEEvent{
				{ID: "42", Event: "msg", Data: "content"},
			},
		},
		{
			name:  "comment lines ignored",
			input: ": this is a comment\nevent: test\ndata: value\n\n",
			expected: []SSEEvent{
				{Event: "test", Data: "value"},
			},
		},
		{
			name:  "heartbeat comments ignored",
			input: ": heartbeat\nevent: msg\ndata: hi\n\n",
			expected: []SSEEvent{
				{Event: "msg", Data: "hi"},
			},
		},
		{
			name:     "empty input",
			input:    "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan SSEEvent, 10)
			ctx := context.Background()
			r := stringReader(tt.input)
			parseSSEStream(ctx, r, ch)
			close(ch)

			var got []SSEEvent
			for ev := range ch {
				got = append(got, ev)
			}

			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d events, got %d", len(tt.expected), len(got))
			}
			for i, exp := range tt.expected {
				if got[i].Event != exp.Event {
					t.Errorf("event[%d]: expected Event %q, got %q", i, exp.Event, got[i].Event)
				}
				if got[i].Data != exp.Data {
					t.Errorf("event[%d]: expected Data %q, got %q", i, exp.Data, got[i].Data)
				}
				if got[i].ID != exp.ID {
					t.Errorf("event[%d]: expected ID %q, got %q", i, exp.ID, got[i].ID)
				}
			}
		})
	}
}

type stringReaderImpl struct {
	data []byte
	pos  int
}

func stringReader(s string) *stringReaderImpl {
	return &stringReaderImpl{data: []byte(s)}
}

func (r *stringReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
