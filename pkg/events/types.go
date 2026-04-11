package events

import (
	"encoding/json"
	"time"
)

// EventType identifies the kind of event.
type EventType string

const (
	// Session lifecycle events.
	EventSessionStarted   EventType = "session.started"
	EventSessionCompleted EventType = "session.completed"
	EventSessionFailed    EventType = "session.failed"

	// Agent activity events.
	EventAgentThinking EventType = "agent.thinking"
	EventAgentMessage  EventType = "agent.message"

	// Tool events.
	EventToolCall   EventType = "tool.call"
	EventToolResult EventType = "tool.result"

	// Permission gating events.
	EventPermissionRequested EventType = "session.permission_requested"
	EventPermissionResponded EventType = "session.permission_responded"

	// Token usage events.
	EventTokenUsage EventType = "token.usage"

	// Error events.
	EventError EventType = "error"
)

// PermissionRequestData is the payload for session.permission_requested events.
type PermissionRequestData struct {
	PermissionID string          `json:"permissionId"`
	ToolName     string          `json:"toolName"`
	Title        string          `json:"title"`
	Arguments    json.RawMessage `json:"arguments"`
}

// PermissionResponseData is the payload for session.permission_responded events.
type PermissionResponseData struct {
	PermissionID string `json:"permissionId"`
	Decision     string `json:"decision"`
	Remember     string `json:"remember,omitempty"`
	RespondedBy  string `json:"respondedBy,omitempty"`
}

// Event is the normalized event envelope published to NATS JetStream.
type Event struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	Timestamp time.Time       `json:"timestamp"`
	Type      EventType       `json:"type"`
	Data      json.RawMessage `json:"data"`
}

// SessionStartedData is the payload for session.started events.
type SessionStartedData struct {
	AgentType string `json:"agentType"`
	Prompt    string `json:"prompt"`
	Namespace string `json:"namespace"`
}

// SessionCompletedData is the payload for session.completed events.
type SessionCompletedData struct {
	InputTokens  int64  `json:"inputTokens,omitempty"`
	OutputTokens int64  `json:"outputTokens,omitempty"`
	Cost         string `json:"cost,omitempty"`
}

// SessionFailedData is the payload for session.failed events.
type SessionFailedData struct {
	Reason string `json:"reason"`
}
