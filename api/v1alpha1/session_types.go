package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SessionPhase represents the current phase of a Session.
// +kubebuilder:validation:Enum=Pending;Active;WaitingForApproval;Completed;Failed;Cancelled
type SessionPhase string

const (
	SessionPhasePending            SessionPhase = "Pending"
	SessionPhaseActive             SessionPhase = "Active"
	SessionPhaseWaitingForApproval SessionPhase = "WaitingForApproval"
	SessionPhaseCompleted          SessionPhase = "Completed"
	SessionPhaseFailed             SessionPhase = "Failed"
	SessionPhaseCancelled          SessionPhase = "Cancelled"
)

// PendingApproval contains summary info about a pending permission request.
// Full request details are in NATS, not etcd.
type PendingApproval struct {
	// ID is the unique identifier for this permission request.
	ID string `json:"id"`

	// ToolName is the tool the agent wants to execute.
	ToolName string `json:"toolName"`

	// Title is a human-readable summary of the request.
	Title string `json:"title"`

	// RequestedAt is when the permission was requested.
	RequestedAt metav1.Time `json:"requestedAt"`
}

// FailureReason indicates why a session entered the Failed phase.
// +kubebuilder:validation:Enum=AgentError;Timeout;BridgeLost
type FailureReason string

const (
	// FailureReasonAgentError means the agent reported an error.
	FailureReasonAgentError FailureReason = "AgentError"

	// FailureReasonTimeout means the session exceeded its spec.timeout.
	FailureReasonTimeout FailureReason = "Timeout"

	// FailureReasonBridgeLost means the bridge became unreachable.
	FailureReasonBridgeLost FailureReason = "BridgeLost"
)

// SessionSpec defines the desired state of a Session.
type SessionSpec struct {
	// SandboxRef references the sandbox this session runs in.
	SandboxRef LocalObjectReference `json:"sandboxRef"`

	// TaskRef references the task this session is executing.
	// +optional
	TaskRef *LocalObjectReference `json:"taskRef,omitempty"`

	// AgentType identifies the type of agent for this session.
	AgentType string `json:"agentType"`

	// Prompt is the prompt to send to the agent.
	Prompt string `json:"prompt"`

	// ContextFiles is a list of file paths to provide as context.
	// +optional
	ContextFiles []string `json:"contextFiles,omitempty"`

	// Timeout is the maximum duration for this session.
	// Inherited from the Task's spec.timeout. Default: 1h.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

// SessionStatus defines the observed state of a Session.
type SessionStatus struct {
	// Phase is the current phase of the session.
	// +optional
	Phase SessionPhase `json:"phase,omitempty"`

	// StartedAt is the time the session started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is the time the session completed.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// FailureReason indicates why the session failed.
	// Set only when Phase is Failed.
	// +optional
	FailureReason FailureReason `json:"failureReason,omitempty"`

	// EventStreamSubject is the NATS subject for this session's event stream.
	// +optional
	EventStreamSubject string `json:"eventStreamSubject,omitempty"`

	// PendingApproval contains summary info about a pending permission request.
	// Present only when Phase is WaitingForApproval.
	// +optional
	PendingApproval *PendingApproval `json:"pendingApproval,omitempty"`

	// TokenUsage tracks token consumption for this session.
	// +optional
	TokenUsage *TokenUsage `json:"tokenUsage,omitempty"`

	// Conditions represent the latest available observations of the session's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Session represents an agent session in a sandbox.
type Session struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SessionSpec   `json:"spec,omitempty"`
	Status SessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SessionList contains a list of Sessions.
type SessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Session `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Session{}, &SessionList{})
}
