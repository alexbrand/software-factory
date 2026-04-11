package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PermissionMode controls how the bridge handles runtime permission requests from the agent.
// +kubebuilder:validation:Enum=bypass;autoApprove;requireApproval
type PermissionMode string

const (
	// PermissionModeBypass sets bypassPermissions on the agent session.
	// The agent never emits permission requests. This is the default.
	PermissionModeBypass PermissionMode = "bypass"

	// PermissionModeAutoApprove makes the bridge auto-respond "allow" to every request.
	PermissionModeAutoApprove PermissionMode = "autoApprove"

	// PermissionModeRequireApproval makes the bridge publish requests to NATS
	// and wait for external approval.
	PermissionModeRequireApproval PermissionMode = "requireApproval"
)

// AgentConfigSpec defines the desired state of an AgentConfig.
type AgentConfigSpec struct {
	// AgentType is the agent identifier (must match SDK's agent identifier).
	AgentType string `json:"agentType"`

	// DisplayName is a human-readable name for the agent type.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// PermissionMode controls how runtime permission requests are handled.
	// Defaults to "bypass".
	// +optional
	PermissionMode PermissionMode `json:"permissionMode,omitempty"`

	// SDK configures the Sandbox Agent SDK container.
	SDK SDKConfig `json:"sdk"`

	// Bridge configures the bridge sidecar container.
	Bridge BridgeConfig `json:"bridge"`

	// AgentSettings contains agent-specific configuration.
	// +optional
	AgentSettings *AgentSettings `json:"agentSettings,omitempty"`

	// Credentials defines the credentials required by the agent.
	// +optional
	Credentials []CredentialConfig `json:"credentials,omitempty"`
}

// SDKConfig configures the Sandbox Agent SDK.
type SDKConfig struct {
	// Image is the container image for the SDK.
	Image string `json:"image"`

	// Port is the port the SDK listens on.
	// +optional
	Port *int32 `json:"port,omitempty"`
}

// BridgeConfig configures the bridge sidecar.
type BridgeConfig struct {
	// Image is the container image for the bridge sidecar.
	Image string `json:"image"`

	// Port is the port the bridge listens on.
	// +optional
	Port *int32 `json:"port,omitempty"`

	// HealthCheck defines the health check configuration for the bridge.
	// +optional
	HealthCheck *HealthCheckConfig `json:"healthCheck,omitempty"`
}

// HealthCheckConfig defines health check parameters.
type HealthCheckConfig struct {
	// HTTPGet specifies the HTTP request to perform.
	// +optional
	HTTPGet *HTTPGetConfig `json:"httpGet,omitempty"`

	// InitialDelaySeconds is the number of seconds after the container starts before health checks begin.
	// +optional
	InitialDelaySeconds *int32 `json:"initialDelaySeconds,omitempty"`

	// PeriodSeconds is how often (in seconds) to perform the health check.
	// +optional
	PeriodSeconds *int32 `json:"periodSeconds,omitempty"`
}

// HTTPGetConfig describes an HTTP health check.
type HTTPGetConfig struct {
	// Path is the path to access on the HTTP server.
	Path string `json:"path"`

	// Port is the port to access on the container.
	Port int32 `json:"port"`
}

// AgentSettings contains agent-specific configuration.
type AgentSettings struct {
	// ContextFile is the context file the agent reads (e.g., CLAUDE.md).
	// +optional
	ContextFile *string `json:"contextFile,omitempty"`

	// AllowedTools is an optional list of tools the agent is allowed to use.
	// +optional
	AllowedTools []string `json:"allowedTools,omitempty"`
}

// CredentialConfig defines a credential to inject into the sandbox.
type CredentialConfig struct {
	// Name is the environment variable name for the credential.
	Name string `json:"name"`

	// SecretRef references a Kubernetes Secret containing the credential.
	SecretRef SecretKeyReference `json:"secretRef"`

	// Host is the hostname this credential applies to (for proxy injection).
	// +optional
	Host *string `json:"host,omitempty"`

	// Header is the HTTP header to use for this credential.
	// +optional
	Header *string `json:"header,omitempty"`
}

// SecretKeyReference references a specific key in a Kubernetes Secret.
type SecretKeyReference struct {
	// Name is the name of the Secret.
	Name string `json:"name"`

	// Key is the key within the Secret.
	Key string `json:"key"`
}

// AgentConfigStatus defines the observed state of an AgentConfig.
type AgentConfigStatus struct {
	// Conditions represent the latest available observations of the AgentConfig's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// AgentConfig defines how to run a specific agent type.
type AgentConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentConfigSpec   `json:"spec,omitempty"`
	Status AgentConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentConfigList contains a list of AgentConfigs.
type AgentConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentConfig{}, &AgentConfigList{})
}
