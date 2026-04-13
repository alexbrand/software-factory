package apiserver

import (
	"time"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

// CreateWorkflowRequest is the request body for POST /v1/workflows.
type CreateWorkflowRequest struct {
	Name      string                       `json:"name"`
	Namespace string                       `json:"namespace,omitempty"`
	Defaults  *factoryv1alpha1.WorkflowDefaults `json:"defaults,omitempty"`
	Context   *factoryv1alpha1.WorkflowContext   `json:"context,omitempty"`
	Tasks     []factoryv1alpha1.WorkflowTask     `json:"tasks"`
}

// WorkflowResponse is the response for workflow endpoints.
type WorkflowResponse struct {
	ID           string                    `json:"id"`
	Name         string                    `json:"name"`
	Namespace    string                    `json:"namespace"`
	Phase        string                    `json:"phase"`
	StartedAt    *time.Time                `json:"startedAt,omitempty"`
	CompletedAt  *time.Time                `json:"completedAt,omitempty"`
	TaskStatuses map[string]string         `json:"taskStatuses,omitempty"`
	CreatedAt    time.Time                 `json:"createdAt"`
}

// CreateTaskRequest is the request body for POST /v1/tasks.
type CreateTaskRequest struct {
	Name      string                          `json:"name"`
	Namespace string                          `json:"namespace,omitempty"`
	PoolRef   string                          `json:"poolRef"`
	Prompt    string                          `json:"prompt"`
	Inputs    []factoryv1alpha1.ArtifactReference `json:"inputs,omitempty"`
	Outputs   []factoryv1alpha1.ArtifactReference `json:"outputs,omitempty"`
	Timeout   string                          `json:"timeout,omitempty"`
	Retries   *int32                          `json:"retries,omitempty"`
}

// TaskResponse is the response for task endpoints.
type TaskResponse struct {
	ID          string                       `json:"id"`
	Name        string                       `json:"name"`
	Namespace   string                       `json:"namespace"`
	Phase       string                       `json:"phase"`
	SandboxRef  string                       `json:"sandboxRef,omitempty"`
	SessionRef  string                       `json:"sessionRef,omitempty"`
	Attempts    int32                        `json:"attempts"`
	StartedAt   *time.Time                   `json:"startedAt,omitempty"`
	CompletedAt *time.Time                   `json:"completedAt,omitempty"`
	TokenUsage  *factoryv1alpha1.TokenUsage  `json:"tokenUsage,omitempty"`
	CreatedAt   time.Time                    `json:"createdAt"`
}

// PoolResponse is the response for pool endpoints.
type PoolResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Ready     int32     `json:"ready"`
	Active    int32     `json:"active"`
	Idle      int32     `json:"idle"`
	Creating  int32     `json:"creating"`
	MinReplicas int32   `json:"minReplicas"`
	MaxReplicas int32   `json:"maxReplicas"`
	CreatedAt time.Time `json:"createdAt"`
}

// CreateSessionRequest is the request body for POST /v1/sessions.
type CreateSessionRequest struct {
	PoolRef   string `json:"poolRef"`
	AgentType string `json:"agentType,omitempty"` // defaults to pool's agentConfig
	Prompt    string `json:"prompt,omitempty"`    // optional initial message
	Timeout   string `json:"timeout,omitempty"`   // idle timeout (default: 1h)
}

// SessionResponse is the response for session endpoints.
type SessionResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Namespace string  `json:"namespace"`
	Mode      string  `json:"mode"`
	Phase     string  `json:"phase"`
	AgentType string  `json:"agentType"`
	SandboxRef string `json:"sandboxRef,omitempty"`
	CreatedAt  string `json:"createdAt"`
}

// SendMessageRequest is the request body for POST /v1/sessions/{id}/messages.
type SendMessageRequest struct {
	Message string `json:"message"`
}

// PermissionDecisionRequest is the request body for POST /v1/sessions/{id}/permissions/{permissionId}.
type PermissionDecisionRequest struct {
	Decision string `json:"decision"` // "allow" or "deny"
	Remember string `json:"remember,omitempty"` // "once", "session", or "always"
}

// PermissionDecisionResponse is the response for permission approval.
type PermissionDecisionResponse struct {
	PermissionID string `json:"permissionId"`
	Decision     string `json:"decision"`
	Remember     string `json:"remember,omitempty"`
	RespondedBy  string `json:"respondedBy,omitempty"`
}

// ErrorResponse is returned on errors.
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// workflowFromCR converts a Workflow CR to a WorkflowResponse.
func workflowFromCR(w *factoryv1alpha1.Workflow) WorkflowResponse {
	resp := WorkflowResponse{
		ID:           string(w.UID),
		Name:         w.Name,
		Namespace:    w.Namespace,
		Phase:        string(w.Status.Phase),
		TaskStatuses: w.Status.TaskStatuses,
		CreatedAt:    w.CreationTimestamp.Time,
	}
	if w.Status.StartedAt != nil {
		t := w.Status.StartedAt.Time
		resp.StartedAt = &t
	}
	if w.Status.CompletedAt != nil {
		t := w.Status.CompletedAt.Time
		resp.CompletedAt = &t
	}
	return resp
}

// taskFromCR converts a Task CR to a TaskResponse.
func taskFromCR(t *factoryv1alpha1.Task) TaskResponse {
	resp := TaskResponse{
		ID:         string(t.UID),
		Name:       t.Name,
		Namespace:  t.Namespace,
		Phase:      string(t.Status.Phase),
		Attempts:   t.Status.Attempts,
		TokenUsage: t.Status.TokenUsage,
		CreatedAt:  t.CreationTimestamp.Time,
	}
	if t.Status.SandboxRef != nil {
		resp.SandboxRef = t.Status.SandboxRef.Name
	}
	if t.Status.SessionRef != nil {
		resp.SessionRef = t.Status.SessionRef.Name
	}
	if t.Status.StartedAt != nil {
		ti := t.Status.StartedAt.Time
		resp.StartedAt = &ti
	}
	if t.Status.CompletedAt != nil {
		ti := t.Status.CompletedAt.Time
		resp.CompletedAt = &ti
	}
	return resp
}

// poolFromCR converts a Pool CR to a PoolResponse.
func poolFromCR(p *factoryv1alpha1.Pool) PoolResponse {
	return PoolResponse{
		ID:          string(p.UID),
		Name:        p.Name,
		Namespace:   p.Namespace,
		Ready:       p.Status.Ready,
		Active:      p.Status.Active,
		Idle:        p.Status.Idle,
		Creating:    p.Status.Creating,
		MinReplicas: p.Spec.Replicas.Min,
		MaxReplicas: p.Spec.Replicas.Max,
		CreatedAt:   p.CreationTimestamp.Time,
	}
}
