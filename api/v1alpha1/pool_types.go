package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PoolSpec defines the desired state of a Pool.
type PoolSpec struct {
	// AgentConfigRef references the AgentConfig to use for sandboxes in this pool.
	AgentConfigRef LocalObjectReference `json:"agentConfigRef"`

	// Replicas configures scaling behavior for the pool.
	Replicas ReplicasConfig `json:"replicas"`

	// SandboxTemplate defines the template for sandboxes created by this pool.
	SandboxTemplate SandboxTemplate `json:"sandboxTemplate"`
}

// ReplicasConfig defines scaling parameters for a pool.
type ReplicasConfig struct {
	// Min is the minimum number of sandboxes to keep in the pool.
	// +kubebuilder:validation:Minimum=0
	Min int32 `json:"min"`

	// Max is the maximum number of sandboxes allowed in the pool.
	// +kubebuilder:validation:Minimum=0
	Max int32 `json:"max"`

	// IdleTimeout is the duration after which idle sandboxes are terminated.
	// +optional
	IdleTimeout *metav1.Duration `json:"idleTimeout,omitempty"`

	// ScaleUpThreshold is the ratio of active/(active+ready) that triggers scale-up.
	// +optional
	ScaleUpThreshold *string `json:"scaleUpThreshold,omitempty"`
}

// SandboxTemplate defines the template for creating sandboxes.
type SandboxTemplate struct {
	// Resources defines the compute resources for sandbox pods.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Storage defines the persistent storage configuration.
	// +optional
	Storage *StorageConfig `json:"storage,omitempty"`

	// Warmup defines pre-warm configuration for sandboxes.
	// +optional
	Warmup *WarmupConfig `json:"warmup,omitempty"`

	// MCPTools configures MCP tool access via ToolHive.
	// +optional
	MCPTools *MCPToolsConfig `json:"mcpTools,omitempty"`

	// NetworkPolicy defines network access rules for sandboxes.
	// +optional
	NetworkPolicy *NetworkPolicyConfig `json:"networkPolicy,omitempty"`
}

// StorageConfig defines persistent storage for sandboxes.
type StorageConfig struct {
	// Size is the requested storage size (e.g., "50Gi").
	Size string `json:"size"`

	// StorageClassName is the name of the StorageClass to use.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// ReclaimPolicy determines what happens to the PV when the sandbox is terminated.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +optional
	ReclaimPolicy *string `json:"reclaimPolicy,omitempty"`
}

// WarmupConfig defines pre-warm settings for sandboxes.
type WarmupConfig struct {
	// Image is the container image to use for warmup.
	Image string `json:"image"`

	// Commands are shell commands to run during warmup.
	// +optional
	Commands []string `json:"commands,omitempty"`

	// GitRepos are repositories to clone during warmup.
	// +optional
	GitRepos []GitRepoConfig `json:"gitRepos,omitempty"`
}

// GitRepoConfig defines a git repository to clone.
type GitRepoConfig struct {
	// URL is the git repository URL.
	URL string `json:"url"`

	// Branch is the branch to clone.
	// +optional
	Branch *string `json:"branch,omitempty"`

	// Path is the local path to clone into.
	// +optional
	Path *string `json:"path,omitempty"`
}

// MCPToolsConfig configures MCP tool access.
type MCPToolsConfig struct {
	// VMCPRef references a VirtualMCPServer in the same namespace.
	VMCPRef LocalObjectReference `json:"vmcpRef"`
}

// NetworkPolicyConfig defines network access rules.
type NetworkPolicyConfig struct {
	// EgressRules defines allowed egress destinations.
	// +optional
	EgressRules []EgressRule `json:"egressRules,omitempty"`
}

// EgressRule defines an allowed egress destination.
type EgressRule struct {
	// To is a list of allowed destination hosts or patterns.
	To []string `json:"to"`

	// Ports is a list of allowed ports.
	Ports []int32 `json:"ports"`
}

// PoolStatus defines the observed state of a Pool.
type PoolStatus struct {
	// Ready is the number of sandboxes in Ready phase.
	Ready int32 `json:"ready"`

	// Active is the number of sandboxes currently running tasks.
	Active int32 `json:"active"`

	// Idle is the number of sandboxes in Idle phase.
	Idle int32 `json:"idle"`

	// Creating is the number of sandboxes being created.
	Creating int32 `json:"creating"`

	// Conditions represent the latest available observations of the pool's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Pool defines a template for pre-provisioned sandboxes.
type Pool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PoolSpec   `json:"spec,omitempty"`
	Status PoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PoolList contains a list of Pools.
type PoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Pool `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Pool{}, &PoolList{})
}
