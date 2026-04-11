package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// SessionManager manages agent sessions via the Sandbox Agent SDK.
type SessionManager struct {
	sdk    *SDKClient
	logger *slog.Logger

	mu       sync.RWMutex
	sessions map[string]*sessionState
}

type sessionState struct {
	serverID  string
	agentType string
	cancel    context.CancelFunc
}

// NewSessionManager creates a new session manager.
func NewSessionManager(sdk *SDKClient, logger *slog.Logger) *SessionManager {
	return &SessionManager{
		sdk:      sdk,
		logger:   logger,
		sessions: make(map[string]*sessionState),
	}
}

// StartSessionConfig holds parameters for starting a session.
type StartSessionConfig struct {
	AgentType      string
	Prompt         string
	ContextFiles   []ContextFile
	WorkDir        string
	PermissionMode string
}

// ContextFile represents a file to write to the sandbox before starting the session.
type ContextFile struct {
	Path    string
	Content string
}

// EventCallbackFactory creates an event callback for a given server ID.
// This allows the callback to be created after the server ID is known.
type EventCallbackFactory func(serverID string) func(SSEEvent)

// StartSession creates a new ACP session, writes context files, sends the prompt,
// and starts event forwarding. Returns the session ID.
// The makeCallback factory is called with the server ID to create the event callback.
func (m *SessionManager) StartSession(ctx context.Context, cfg StartSessionConfig, makeCallback EventCallbackFactory) (string, error) {
	// Write context files to sandbox filesystem.
	for _, f := range cfg.ContextFiles {
		if err := m.sdk.WriteFile(ctx, f.Path, f.Content); err != nil {
			return "", fmt.Errorf("writing context file %s: %w", f.Path, err)
		}
	}

	// Create ACP session.
	serverID, err := m.sdk.CreateACPSession(ctx, ACPConfig{
		Agent:          cfg.AgentType,
		WorkDir:        cfg.WorkDir,
		PermissionMode: cfg.PermissionMode,
	})
	if err != nil {
		return "", fmt.Errorf("creating ACP session: %w", err)
	}

	// Start event forwarding before sending the prompt so we capture all events.
	eventCtx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	m.sessions[serverID] = &sessionState{
		serverID:  serverID,
		agentType: cfg.AgentType,
		cancel:    cancel,
	}
	m.mu.Unlock()

	onEvent := makeCallback(serverID)
	go m.streamEvents(eventCtx, serverID, onEvent)

	// Send the prompt in a goroutine. The ACP session/prompt RPC blocks until
	// the agent finishes, which can take minutes. We return immediately so the
	// bridge HTTP handler doesn't time out. Prompt errors are logged and will
	// surface as a session timeout to the task controller.
	go func() {
		if err := m.sdk.SendACPMessage(context.Background(), serverID, cfg.Prompt); err != nil {
			m.logger.Error("failed to send prompt", "serverID", serverID, "error", err)
		}
	}()

	return serverID, nil
}

// streamEvents reads SSE events from the SDK and calls onEvent for each.
func (m *SessionManager) streamEvents(ctx context.Context, serverID string, onEvent func(SSEEvent)) {
	ch, err := m.sdk.StreamACPEvents(ctx, serverID)
	if err != nil {
		m.logger.Error("failed to open SSE stream", "serverID", serverID, "error", err)
		return
	}

	for event := range ch {
		select {
		case <-ctx.Done():
			return
		default:
			onEvent(event)
		}
	}

	// Stream ended — remove from active sessions.
	m.mu.Lock()
	delete(m.sessions, serverID)
	m.mu.Unlock()
}

// SendMessage sends a follow-up message to an active session.
func (m *SessionManager) SendMessage(ctx context.Context, sessionID string, msg string) error {
	m.mu.RLock()
	_, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	return m.sdk.SendACPMessage(ctx, sessionID, msg)
}

// CancelSession gracefully closes an ACP session.
func (m *SessionManager) CancelSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	state, ok := m.sessions[sessionID]
	if ok {
		state.cancel()
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	return m.sdk.CloseACPSession(ctx, sessionID)
}

// ActiveSessionCount returns the number of currently active sessions.
func (m *SessionManager) ActiveSessionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// HasSession returns true if the given session ID is active.
func (m *SessionManager) HasSession(sessionID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.sessions[sessionID]
	return ok
}
