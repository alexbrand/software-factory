package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkflowPhase represents the current phase of a Workflow.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Cancelled
type WorkflowPhase string

const (
	WorkflowPhasePending   WorkflowPhase = "Pending"
	WorkflowPhaseRunning   WorkflowPhase = "Running"
	WorkflowPhaseSucceeded WorkflowPhase = "Succeeded"
	WorkflowPhaseFailed    WorkflowPhase = "Failed"
	WorkflowPhaseCancelled WorkflowPhase = "Cancelled"
)

// WorkflowSpec defines the desired state of a Workflow.
type WorkflowSpec struct {
	// Defaults defines global configuration applied to all tasks.
	// +optional
	Defaults *WorkflowDefaults `json:"defaults,omitempty"`

	// Context defines shared context across all tasks.
	// +optional
	Context *WorkflowContext `json:"context,omitempty"`

	// Tasks defines the DAG of tasks to execute.
	Tasks []WorkflowTask `json:"tasks"`
}

// WorkflowDefaults defines default settings for all tasks in a workflow.
type WorkflowDefaults struct {
	// PoolRef references the default pool for tasks.
	// +optional
	PoolRef *LocalObjectReference `json:"poolRef,omitempty"`

	// Timeout is the default timeout for tasks.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Retries is the default number of retries for tasks.
	// +optional
	Retries *int32 `json:"retries,omitempty"`
}

// WorkflowContext defines context shared across tasks.
type WorkflowContext struct {
	// Repository defines a git repository to clone for all tasks.
	// +optional
	Repository *RepositoryConfig `json:"repository,omitempty"`

	// Files defines additional files available to all tasks.
	// +optional
	Files []FileReference `json:"files,omitempty"`
}

// RepositoryConfig defines a git repository.
type RepositoryConfig struct {
	// URL is the git repository URL.
	URL string `json:"url"`

	// Branch is the branch to clone.
	// +optional
	Branch *string `json:"branch,omitempty"`
}

// FileReference defines a reference to additional files.
type FileReference struct {
	// Name is a logical name for the file reference.
	Name string `json:"name"`

	// ConfigMapRef references a ConfigMap containing file content.
	// +optional
	ConfigMapRef *LocalObjectReference `json:"configMapRef,omitempty"`
}

// WorkflowTask defines a task within a workflow DAG.
type WorkflowTask struct {
	// Name is the unique name of the task within the workflow.
	Name string `json:"name"`

	// DependsOn is a list of task names that must complete before this task starts.
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Spec defines the task specification.
	Spec TaskInlineSpec `json:"spec"`
}

// TaskInlineSpec defines the inline specification for a task within a workflow.
type TaskInlineSpec struct {
	// Prompt is the prompt to send to the agent.
	Prompt string `json:"prompt"`

	// PoolRef overrides the workflow default pool for this task.
	// +optional
	PoolRef *LocalObjectReference `json:"poolRef,omitempty"`

	// Inputs defines artifacts to inject before the task runs.
	// +optional
	Inputs []ArtifactReference `json:"inputs,omitempty"`

	// Outputs defines artifacts to extract after the task completes.
	// +optional
	Outputs []ArtifactReference `json:"outputs,omitempty"`

	// Timeout overrides the workflow default timeout for this task.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Retries overrides the workflow default retry count for this task.
	// +optional
	Retries *int32 `json:"retries,omitempty"`
}

// WorkflowStatus defines the observed state of a Workflow.
type WorkflowStatus struct {
	// Phase is the current phase of the workflow.
	// +optional
	Phase WorkflowPhase `json:"phase,omitempty"`

	// StartedAt is the time the workflow started executing.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is the time the workflow finished executing.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// TaskStatuses maps task names to their current phase.
	// +optional
	TaskStatuses map[string]string `json:"taskStatuses,omitempty"`

	// Conditions represent the latest available observations of the workflow's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Workflow defines a DAG of tasks.
type Workflow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkflowSpec   `json:"spec,omitempty"`
	Status WorkflowStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkflowList contains a list of Workflows.
type WorkflowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workflow `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workflow{}, &WorkflowList{})
}
