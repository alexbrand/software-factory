package bridge

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CredentialMapping maps an outbound host to credential injection config.
type CredentialMapping struct {
	// Host is the hostname to match (e.g., "api.anthropic.com").
	Host string
	// Header is the HTTP header to inject (e.g., "x-api-key").
	Header string
	// Value is the secret value to inject.
	Value string
}

// CredentialProxy is an HTTP/HTTPS forward proxy that intercepts outbound
// requests and injects API keys from mounted secrets.
type CredentialProxy struct {
	mappings map[string]CredentialMapping
	mu       sync.RWMutex
	logger   *slog.Logger
	server   *http.Server
}

// NewCredentialProxy creates a new credential proxy.
func NewCredentialProxy(logger *slog.Logger) *CredentialProxy {
	return &CredentialProxy{
		mappings: make(map[string]CredentialMapping),
		logger:   logger,
	}
}

// AddMapping adds a credential mapping for a host.
func (p *CredentialProxy) AddMapping(mapping CredentialMapping) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mappings[mapping.Host] = mapping
}

// LoadCredentialsFromDir loads credentials from a directory of secret files.
// Each file is expected to contain a raw secret value.
// The mappings parameter provides the host→header mapping; secret values are
// read from secretDir/{secret-name}.
func (p *CredentialProxy) LoadCredentialsFromDir(secretDir string, mappings []CredentialMappingConfig) error {
	for _, m := range mappings {
		path := filepath.Join(secretDir, m.SecretFile)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading secret %s: %w", m.SecretFile, err)
		}
		value := strings.TrimSpace(string(data))
		p.AddMapping(CredentialMapping{
			Host:   m.Host,
			Header: m.Header,
			Value:  value,
		})
		p.logger.Info("loaded credential", "host", m.Host, "header", m.Header)
	}
	return nil
}

// CredentialMappingConfig is a configuration entry for credential loading.
type CredentialMappingConfig struct {
	Host       string
	Header     string
	SecretFile string
}

// ServeHTTP implements http.Handler for the forward proxy.
func (p *CredentialProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleHTTP handles plain HTTP proxy requests.
func (p *CredentialProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	// Inject credentials if we have a mapping for this host.
	host := extractHost(r.Host)
	p.mu.RLock()
	mapping, ok := p.mappings[host]
	p.mu.RUnlock()

	if ok {
		r.Header.Set(mapping.Header, mapping.Value)
	}

	// Forward the request.
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	resp, err := transport.RoundTrip(r)
	if err != nil {
		p.logger.Error("proxy request failed", "host", r.Host, "error", err)
		http.Error(w, "proxy error", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Copy response headers and body.
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

// handleConnect handles HTTPS CONNECT tunneling with credential injection.
func (p *CredentialProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := extractHost(r.Host)

	destConn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		p.logger.Error("CONNECT dial failed", "host", r.Host, "error", err)
		http.Error(w, "proxy error", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = destConn.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		_ = destConn.Close()
		return
	}

	_ = host // credential injection for CONNECT requires MITM which we don't do
	// For CONNECT, we simply tunnel — credential injection only works for plain HTTP.
	go transfer(destConn, clientConn)
	go transfer(clientConn, destConn)
}

func transfer(dst net.Conn, src net.Conn) {
	defer func() { _ = dst.Close() }()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// extractHost returns the hostname without port.
func extractHost(hostPort string) string {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort
	}
	return host
}

// Start starts the credential proxy on the given address.
func (p *CredentialProxy) Start(addr string) error {
	p.server = &http.Server{
		Addr:              addr,
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}
	p.logger.Info("starting credential proxy", "addr", addr)
	return p.server.ListenAndServe()
}

// Shutdown gracefully shuts down the credential proxy.
func (p *CredentialProxy) Shutdown() error {
	if p.server == nil {
		return nil
	}
	return p.server.Close()
}
