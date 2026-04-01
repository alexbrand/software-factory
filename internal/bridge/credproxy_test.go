package bridge

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialProxyAddMapping(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	proxy := NewCredentialProxy(logger)

	proxy.AddMapping(CredentialMapping{
		Host:   "api.anthropic.com",
		Header: "x-api-key",
		Value:  "sk-test-123",
	})

	proxy.mu.RLock()
	defer proxy.mu.RUnlock()

	m, ok := proxy.mappings["api.anthropic.com"]
	if !ok {
		t.Fatal("expected mapping for api.anthropic.com")
	}
	if m.Header != "x-api-key" {
		t.Errorf("expected header x-api-key, got %s", m.Header)
	}
	if m.Value != "sk-test-123" {
		t.Errorf("expected value sk-test-123, got %s", m.Value)
	}
}

func TestCredentialProxyLoadCredentialsFromDir(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	proxy := NewCredentialProxy(logger)

	// Create a temp dir with a secret file.
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "api-key")
	if err := os.WriteFile(secretPath, []byte("sk-secret-456\n"), 0o600); err != nil {
		t.Fatalf("writing secret file: %v", err)
	}

	err := proxy.LoadCredentialsFromDir(dir, []CredentialMappingConfig{
		{
			Host:       "api.openai.com",
			Header:     "Authorization",
			SecretFile: "api-key",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	proxy.mu.RLock()
	defer proxy.mu.RUnlock()

	m, ok := proxy.mappings["api.openai.com"]
	if !ok {
		t.Fatal("expected mapping for api.openai.com")
	}
	if m.Value != "sk-secret-456" {
		t.Errorf("expected value 'sk-secret-456', got %s", m.Value)
	}
}

func TestCredentialProxyLoadMissingFile(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	proxy := NewCredentialProxy(logger)

	err := proxy.LoadCredentialsFromDir("/nonexistent", []CredentialMappingConfig{
		{Host: "example.com", Header: "X-Key", SecretFile: "missing"},
	})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCredentialProxyHTTPInjection(t *testing.T) {
	// Create a backend server that checks for injected headers.
	var receivedHeader string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	proxy := NewCredentialProxy(logger)

	backendHost := extractHost(backend.Listener.Addr().String())
	proxy.AddMapping(CredentialMapping{
		Host:   backendHost,
		Header: "x-api-key",
		Value:  "sk-injected",
	})

	// Create a proxy server.
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Use the proxy by configuring an HTTP client with the proxy transport.
	proxyURL, _ := http.NewRequest(http.MethodGet, proxyServer.URL, nil)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL.URL),
	}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(backend.URL + "/test")
	if err != nil {
		t.Fatalf("request through proxy failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if receivedHeader != "sk-injected" {
		t.Errorf("expected x-api-key 'sk-injected', got %q", receivedHeader)
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"api.anthropic.com:443", "api.anthropic.com"},
		{"api.anthropic.com", "api.anthropic.com"},
		{"localhost:8080", "localhost"},
		{"[::1]:8080", "::1"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractHost(tt.input)
			if got != tt.expected {
				t.Errorf("extractHost(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
