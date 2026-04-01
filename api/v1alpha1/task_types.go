package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskPhase represents the current phase of a Task.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Cancelled
type TaskPhase string

const (
	TaskPhasePending   TaskPhase = "Pending"
	TaskPhaseRunning   TaskPhase = "Running"
	TaskPhaseSucceeded TaskPhase = "Succeeded"
	TaskPhaseFailed    TaskPhase = "Failed"
	TaskPhaseCancelled TaskPhase = "Cancelled"
)

// TaskSpec defines the desired state of a Task.
type TaskSpec struct {
	// WorkflowRef references the parent workflow, if any.
	// +optional
	WorkflowRef *LocalObjectReference `json:"workflowRef,omitempty"`

	// PoolRef references the pool to use for this task.
	PoolRef LocalObjectReference `json:"poolRef"`

	// Prompt is the prompt to send to the agent.
	Prompt string `json:"prompt"`

	// Inputs defines artifacts to inject before the task runs.
	// +optional
	Inputs []ArtifactReference `json:"inputs,omitempty"`

	// Outputs defines artifacts to extract after the task completes.
	// +optional
	Outputs []ArtifactReference `json:"outputs,omitempty"`

	// Timeout is the maximum duration for this task.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Retries is the number of times to retry the task on failure.
	// +optional
	Retries *int32 `json:"retries,omitempty"`
}

// ArtifactReference defines an input or output artifact.
type ArtifactReference struct {
	// Artifact is the logical name of the artifact.
	Artifact string `json:"artifact"`

	// Path is the filesystem path for the artifact.
	Path string `json:"path"`
}

// TokenUsage tracks token consumption for a task or session.
type TokenUsage struct {
	// Input is the number of input tokens consumed.
	Input int64 `json:"input"`

	// Output is the number of output tokens generated.
	Output int64 `json:"output"`

	// Cost is the estimated cost as a string (e.g., "0.42").
	// +optional
	Cost *string `json:"cost,omitempty"`
}

// TaskStatus defines the observed state of a Task.
type TaskStatus struct {
	// Phase is the current phase of the task.
	// +optional
	Phase TaskPhase `json:"phase,omitempty"`

	// SandboxRef references the sandbox running this task.
	// +optional
	SandboxRef *LocalObjectReference `json:"sandboxRef,omitempty"`

	// SessionRef references the active session for this task.
	// +optional
	SessionRef *LocalObjectReference `json:"sessionRef,omitempty"`

	// StartedAt is the time the task started executing.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is the time the task finished executing.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Attempts is the number of attempts made so far.
	Attempts int32 `json:"attempts"`

	// TokenUsage tracks token consumption for this task.
	// +optional
	TokenUsage *TokenUsage `json:"tokenUsage,omitempty"`

	// Conditions represent the latest available observations of the task's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Task represents a single unit of work.
type Task struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TaskSpec   `json:"spec,omitempty"`
	Status TaskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskList contains a list of Tasks.
type TaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Task `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Task{}, &TaskList{})
}
