package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SandboxPhase represents the current phase of a Sandbox.
// +kubebuilder:validation:Enum=Creating;Ready;Assigned;Active;Idle;Terminating
type SandboxPhase string

const (
	SandboxPhaseCreating    SandboxPhase = "Creating"
	SandboxPhaseReady       SandboxPhase = "Ready"
	SandboxPhaseAssigned    SandboxPhase = "Assigned"
	SandboxPhaseActive      SandboxPhase = "Active"
	SandboxPhaseIdle        SandboxPhase = "Idle"
	SandboxPhaseTerminating SandboxPhase = "Terminating"
)

// SandboxSpec defines the desired state of a Sandbox.
type SandboxSpec struct {
	// PoolRef references the Pool that owns this sandbox.
	PoolRef LocalObjectReference `json:"poolRef"`

	// AgentConfigRef references the AgentConfig for this sandbox.
	AgentConfigRef LocalObjectReference `json:"agentConfigRef"`

	// Resources overrides the pool's default resource requirements.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// SandboxStatus defines the observed state of a Sandbox.
type SandboxStatus struct {
	// Phase is the current phase of the sandbox.
	// +optional
	Phase SandboxPhase `json:"phase,omitempty"`

	// PodName is the name of the pod running this sandbox.
	// +optional
	PodName string `json:"podName,omitempty"`

	// VolumeName is the name of the PersistentVolume for this sandbox.
	// +optional
	VolumeName string `json:"volumeName,omitempty"`

	// AssignedTask is the name of the task currently assigned to this sandbox.
	// +optional
	AssignedTask string `json:"assignedTask,omitempty"`

	// CurrentSession is the name of the current active session.
	// +optional
	CurrentSession string `json:"currentSession,omitempty"`

	// LastActivityAt is the timestamp of the last activity in this sandbox.
	// +optional
	LastActivityAt *metav1.Time `json:"lastActivityAt,omitempty"`

	// Conditions represent the latest available observations of the sandbox's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Sandbox represents a single isolated execution environment.
type Sandbox struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SandboxSpec   `json:"spec,omitempty"`
	Status SandboxStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SandboxList contains a list of Sandboxes.
type SandboxList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Sandbox `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Sandbox{}, &SandboxList{})
}
