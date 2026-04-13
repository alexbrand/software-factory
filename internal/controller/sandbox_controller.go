package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

const (
	defaultSDKPort    int32 = 2468
	defaultBridgePort int32 = 8080

	workspaceMountPath = "/workspace"
	cacheMountPath     = "/cache"
	secretsMountPath   = "/var/run/secrets/sandbox"

	conditionTypeReady        = "Ready"
	conditionTypeAgentHealthy = "AgentHealthy"
)

// SandboxReconciler reconciles a Sandbox object.
type SandboxReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=factory.example.com,resources=sandboxes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=sandboxes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=pools,verbs=get;list;watch
// +kubebuilder:rbac:groups=factory.example.com,resources=agentconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;delete

func (r *SandboxReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var sandbox factoryv1alpha1.Sandbox
	if err := r.Get(ctx, req.NamespacedName, &sandbox); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Fetch the owning Pool
	var pool factoryv1alpha1.Pool
	if err := r.Get(ctx, types.NamespacedName{
		Name:      sandbox.Spec.PoolRef.Name,
		Namespace: sandbox.Namespace,
	}, &pool); err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching pool %q: %w", sandbox.Spec.PoolRef.Name, err)
	}

	// Fetch the AgentConfig
	var agentConfig factoryv1alpha1.AgentConfig
	if err := r.Get(ctx, types.NamespacedName{
		Name:      sandbox.Spec.AgentConfigRef.Name,
		Namespace: sandbox.Namespace,
	}, &agentConfig); err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching agent config %q: %w", sandbox.Spec.AgentConfigRef.Name, err)
	}

	switch sandbox.Status.Phase {
	case "", factoryv1alpha1.SandboxPhaseCreating:
		return r.reconcileCreating(ctx, &sandbox, &pool, &agentConfig)
	case factoryv1alpha1.SandboxPhaseReady, factoryv1alpha1.SandboxPhaseAssigned, factoryv1alpha1.SandboxPhaseActive:
		return r.reconcileRunning(ctx, &sandbox)
	case factoryv1alpha1.SandboxPhaseIdle:
		return r.reconcileIdle(ctx, &sandbox, &pool)
	case factoryv1alpha1.SandboxPhaseTerminating:
		return r.reconcileTerminating(ctx, &sandbox, &pool)
	default:
		logger.Info("unknown sandbox phase", "phase", sandbox.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *SandboxReconciler) reconcileCreating(ctx context.Context, sandbox *factoryv1alpha1.Sandbox, pool *factoryv1alpha1.Pool, agentConfig *factoryv1alpha1.AgentConfig) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	pvcName := sandbox.Name + "-workspace"
	podName := sandbox.Name

	// Create workspace PVC if it doesn't exist
	if err := r.ensureWorkspacePVC(ctx, sandbox, pool, pvcName); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring workspace PVC: %w", err)
	}

	// Create NetworkPolicy if it doesn't exist
	if err := r.ensureNetworkPolicy(ctx, sandbox, pool); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring network policy: %w", err)
	}

	// Create Pod if it doesn't exist
	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: sandbox.Namespace}, &pod)
	if errors.IsNotFound(err) {
		pod := r.buildPod(sandbox, pool, agentConfig, podName, pvcName)
		if err := ctrl.SetControllerReference(sandbox, pod, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting pod controller reference: %w", err)
		}
		if err := r.Create(ctx, pod); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating pod: %w", err)
		}
		logger.Info("created pod", "pod", podName)
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting pod: %w", err)
	}

	// Check if pod is ready
	var existingPod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: sandbox.Namespace}, &existingPod); err != nil {
		return ctrl.Result{}, fmt.Errorf("getting pod for readiness check: %w", err)
	}

	sandbox.Status.PodName = podName
	sandbox.Status.VolumeName = pvcName

	if isPodReady(&existingPod) {
		sandbox.Status.Phase = factoryv1alpha1.SandboxPhaseReady
		now := metav1.Now()
		sandbox.Status.LastActivityAt = &now
		r.updateConditions(sandbox, true)
		meta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{
			Type:               conditionTypeAgentHealthy,
			Status:             metav1.ConditionTrue,
			Reason:             "AgentReady",
			Message:            "Agent SDK is healthy",
			ObservedGeneration: sandbox.Generation,
			LastTransitionTime: metav1.Now(),
		})
		logger.Info("sandbox is ready", "sandbox", sandbox.Name)
	} else {
		sandbox.Status.Phase = factoryv1alpha1.SandboxPhaseCreating
		r.updateConditions(sandbox, false)
	}

	if err := r.Status().Update(ctx, sandbox); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating sandbox status: %w", err)
	}

	if sandbox.Status.Phase == factoryv1alpha1.SandboxPhaseCreating {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *SandboxReconciler) reconcileRunning(ctx context.Context, sandbox *factoryv1alpha1.Sandbox) (ctrl.Result, error) {
	// Check if pod is ready
	if sandbox.Status.PodName == "" {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{
		Name:      sandbox.Status.PodName,
		Namespace: sandbox.Namespace,
	}, &pod); err != nil {
		if errors.IsNotFound(err) {
			// Pod was deleted externally; mark sandbox as terminating
			sandbox.Status.Phase = factoryv1alpha1.SandboxPhaseTerminating
			if err := r.Status().Update(ctx, sandbox); err != nil {
				return ctrl.Result{}, fmt.Errorf("updating sandbox status: %w", err)
			}
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting pod: %w", err)
	}

	podReady := isPodReady(&pod)
	if sandbox.Status.Phase == factoryv1alpha1.SandboxPhaseReady ||
		sandbox.Status.Phase == factoryv1alpha1.SandboxPhaseAssigned ||
		sandbox.Status.Phase == factoryv1alpha1.SandboxPhaseActive {
		// Already in a running phase; update conditions
		r.updateConditions(sandbox, podReady)
		if err := r.Status().Update(ctx, sandbox); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating sandbox status: %w", err)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *SandboxReconciler) reconcileIdle(ctx context.Context, sandbox *factoryv1alpha1.Sandbox, pool *factoryv1alpha1.Pool) (ctrl.Result, error) {
	if pool.Spec.Replicas.IdleTimeout == nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	timeout := pool.Spec.Replicas.IdleTimeout.Duration
	idleSince := sandbox.Status.LastActivityAt
	if idleSince == nil {
		idleSince = &sandbox.CreationTimestamp
	}

	elapsed := time.Since(idleSince.Time)
	if elapsed > timeout {
		sandbox.Status.Phase = factoryv1alpha1.SandboxPhaseTerminating
		if err := r.Status().Update(ctx, sandbox); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating sandbox status to terminating: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	remaining := timeout - elapsed
	return ctrl.Result{RequeueAfter: remaining}, nil
}

func (r *SandboxReconciler) reconcileTerminating(ctx context.Context, sandbox *factoryv1alpha1.Sandbox, pool *factoryv1alpha1.Pool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Delete the pod
	if sandbox.Status.PodName != "" {
		var pod corev1.Pod
		err := r.Get(ctx, types.NamespacedName{
			Name:      sandbox.Status.PodName,
			Namespace: sandbox.Namespace,
		}, &pod)
		if err == nil {
			if err := r.Delete(ctx, &pod); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("deleting pod: %w", err)
			}
			logger.Info("deleted pod", "pod", sandbox.Status.PodName)
		} else if !errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("getting pod for deletion: %w", err)
		}
	}

	// Handle PVC based on reclaim policy
	if sandbox.Status.VolumeName != "" {
		reclaimPolicy := "Delete"
		if pool.Spec.SandboxTemplate.Storage != nil && pool.Spec.SandboxTemplate.Storage.ReclaimPolicy != nil {
			reclaimPolicy = *pool.Spec.SandboxTemplate.Storage.ReclaimPolicy
		}

		if reclaimPolicy == "Delete" {
			var pvc corev1.PersistentVolumeClaim
			err := r.Get(ctx, types.NamespacedName{
				Name:      sandbox.Status.VolumeName,
				Namespace: sandbox.Namespace,
			}, &pvc)
			if err == nil {
				if err := r.Delete(ctx, &pvc); err != nil && !errors.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("deleting PVC: %w", err)
				}
				logger.Info("deleted PVC", "pvc", sandbox.Status.VolumeName)
			} else if !errors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("getting PVC for deletion: %w", err)
			}
		} else {
			logger.Info("retaining PVC per reclaim policy", "pvc", sandbox.Status.VolumeName)
		}
	}

	// Delete NetworkPolicy
	netpolName := sandbox.Name + "-netpol"
	var netpol networkingv1.NetworkPolicy
	err := r.Get(ctx, types.NamespacedName{
		Name:      netpolName,
		Namespace: sandbox.Namespace,
	}, &netpol)
	if err == nil {
		if err := r.Delete(ctx, &netpol); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting network policy: %w", err)
		}
	} else if !errors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("getting network policy for deletion: %w", err)
	}

	// Update conditions
	meta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Terminated",
		Message:            "Sandbox has been terminated",
		ObservedGeneration: sandbox.Generation,
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Update(ctx, sandbox); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating sandbox status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *SandboxReconciler) ensureWorkspacePVC(ctx context.Context, sandbox *factoryv1alpha1.Sandbox, pool *factoryv1alpha1.Pool, pvcName string) error {
	var pvc corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: sandbox.Namespace}, &pvc)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("getting PVC: %w", err)
	}

	storageSize := "50Gi"
	var storageClassName *string
	if pool.Spec.SandboxTemplate.Storage != nil {
		if pool.Spec.SandboxTemplate.Storage.Size != "" {
			storageSize = pool.Spec.SandboxTemplate.Storage.Size
		}
		storageClassName = pool.Spec.SandboxTemplate.Storage.StorageClassName
	}

	pvc = corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: sandbox.Namespace,
			Labels: map[string]string{
				"factory.example.com/sandbox": sandbox.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(storageSize),
				},
			},
			StorageClassName: storageClassName,
		},
	}

	if err := ctrl.SetControllerReference(sandbox, &pvc, r.Scheme); err != nil {
		return fmt.Errorf("setting PVC controller reference: %w", err)
	}

	if err := r.Create(ctx, &pvc); err != nil {
		return fmt.Errorf("creating PVC: %w", err)
	}

	return nil
}

func (r *SandboxReconciler) ensureNetworkPolicy(ctx context.Context, sandbox *factoryv1alpha1.Sandbox, pool *factoryv1alpha1.Pool) error {
	netpolName := sandbox.Name + "-netpol"

	var existing networkingv1.NetworkPolicy
	err := r.Get(ctx, types.NamespacedName{Name: netpolName, Namespace: sandbox.Namespace}, &existing)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("getting network policy: %w", err)
	}

	protocolUDP := corev1.ProtocolUDP
	protocolTCP := corev1.ProtocolTCP
	dnsPort := intstr.FromInt32(53)

	natsPort := intstr.FromInt32(4222)

	// Always allow DNS and NATS (cross-namespace).
	egressRules := []networkingv1.NetworkPolicyEgressRule{
		{
			To: []networkingv1.NetworkPolicyPeer{
				{NamespaceSelector: &metav1.LabelSelector{}},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Port: &dnsPort, Protocol: &protocolUDP},
				{Port: &natsPort, Protocol: &protocolTCP},
			},
		},
	}

	// Add egress rules from pool spec
	if pool.Spec.SandboxTemplate.NetworkPolicy != nil {
		for _, rule := range pool.Spec.SandboxTemplate.NetworkPolicy.EgressRules {
			var ports []networkingv1.NetworkPolicyPort
			for _, p := range rule.Ports {
				port := intstr.FromInt32(p)
				ports = append(ports, networkingv1.NetworkPolicyPort{
					Port:     &port,
					Protocol: &protocolTCP,
				})
			}
			egressRules = append(egressRules, networkingv1.NetworkPolicyEgressRule{
				To: []networkingv1.NetworkPolicyPeer{
					{
						IPBlock: &networkingv1.IPBlock{
							CIDR: "0.0.0.0/0",
						},
					},
				},
				Ports: ports,
			})
		}
	}

	policyTypes := []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
	netpol := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      netpolName,
			Namespace: sandbox.Namespace,
			Labels: map[string]string{
				"factory.example.com/sandbox": sandbox.Name,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"factory.example.com/sandbox": sandbox.Name,
				},
			},
			PolicyTypes: policyTypes,
			Egress:      egressRules,
		},
	}

	if err := ctrl.SetControllerReference(sandbox, netpol, r.Scheme); err != nil {
		return fmt.Errorf("setting network policy controller reference: %w", err)
	}

	if err := r.Create(ctx, netpol); err != nil {
		return fmt.Errorf("creating network policy: %w", err)
	}

	return nil
}

func (r *SandboxReconciler) buildPod(sandbox *factoryv1alpha1.Sandbox, pool *factoryv1alpha1.Pool, agentConfig *factoryv1alpha1.AgentConfig, podName, pvcName string) *corev1.Pod {
	sdkPort := defaultSDKPort
	if agentConfig.Spec.SDK.Port != nil {
		sdkPort = *agentConfig.Spec.SDK.Port
	}

	bridgePort := defaultBridgePort
	if agentConfig.Spec.Bridge.Port != nil {
		bridgePort = *agentConfig.Spec.Bridge.Port
	}

	// Build volumes
	volumes := []corev1.Volume{
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		},
		{
			Name: "cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	// Build secrets projected volume from credentials
	if len(agentConfig.Spec.Credentials) > 0 {
		sources := make([]corev1.VolumeProjection, 0, len(agentConfig.Spec.Credentials))
		for _, cred := range agentConfig.Spec.Credentials {
			sources = append(sources, corev1.VolumeProjection{
				Secret: &corev1.SecretProjection{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cred.SecretRef.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key:  cred.SecretRef.Key,
							Path: cred.Name,
						},
					},
				},
			})
		}
		volumes = append(volumes, corev1.Volume{
			Name: "secrets",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: sources,
				},
			},
		})
	} else {
		volumes = append(volumes, corev1.Volume{
			Name: "secrets",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	// Common volume mounts for SDK container
	sdkVolumeMounts := []corev1.VolumeMount{
		{Name: "workspace", MountPath: workspaceMountPath},
		{Name: "cache", MountPath: cacheMountPath, ReadOnly: true},
	}

	// Bridge gets secrets mount
	bridgeVolumeMounts := []corev1.VolumeMount{
		{Name: "workspace", MountPath: workspaceMountPath},
		{Name: "secrets", MountPath: secretsMountPath, ReadOnly: true},
	}

	// Resource requirements
	resources := corev1.ResourceRequirements{}
	if sandbox.Spec.Resources != nil {
		resources = *sandbox.Spec.Resources
	} else if pool.Spec.SandboxTemplate.Resources != nil {
		resources = *pool.Spec.SandboxTemplate.Resources
	}

	// Init container for workspace initialization
	initContainers := []corev1.Container{
		{
			Name:            "workspace-init",
			Image:           agentConfig.Spec.SDK.Image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command: []string{
				"sh", "-c", "echo 'Workspace initialized'",
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "workspace", MountPath: workspaceMountPath},
			},
		},
	}

	// Apply warmup commands if configured
	if pool.Spec.SandboxTemplate.Warmup != nil {
		warmup := pool.Spec.SandboxTemplate.Warmup
		if warmup.Image != "" {
			initContainers[0].Image = warmup.Image
		}
		if len(warmup.Commands) > 0 {
			script := ""
			for _, cmd := range warmup.Commands {
				script += cmd + " && "
			}
			script += "echo 'Warmup complete'"
			initContainers[0].Command = []string{"sh", "-c", script}
		}
	}

	// SDK container
	sdkContainer := corev1.Container{
		Name:            "sandbox-agent-sdk",
		Image:           agentConfig.Spec.SDK.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports:           []corev1.ContainerPort{{ContainerPort: sdkPort, Name: "sdk", Protocol: corev1.ProtocolTCP}},
		VolumeMounts:    sdkVolumeMounts,
		Resources:       resources,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt32(sdkPort),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
		},
	}

	// Bridge sidecar container
	bridgeContainer := corev1.Container{
		Name:            "bridge",
		Image:           agentConfig.Spec.Bridge.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports:           []corev1.ContainerPort{{ContainerPort: bridgePort, Name: "bridge", Protocol: corev1.ProtocolTCP}},
		VolumeMounts:    bridgeVolumeMounts,
		Env: []corev1.EnvVar{
			{Name: "SDK_HOST", Value: "localhost"},
			{Name: "SDK_PORT", Value: fmt.Sprintf("%d", sdkPort)},
			{Name: "SANDBOX_NAME", Value: sandbox.Name},
			{Name: "SANDBOX_NAMESPACE", Value: sandbox.Namespace},
			{Name: "NATS_URL", Value: fmt.Sprintf("nats://nats.%s.svc.cluster.local:4222", "factory-system")},
		},
	}

	// Apply health check from AgentConfig
	if agentConfig.Spec.Bridge.HealthCheck != nil && agentConfig.Spec.Bridge.HealthCheck.HTTPGet != nil {
		hc := agentConfig.Spec.Bridge.HealthCheck
		initialDelay := int32(10)
		period := int32(15)
		if hc.InitialDelaySeconds != nil {
			initialDelay = *hc.InitialDelaySeconds
		}
		if hc.PeriodSeconds != nil {
			period = *hc.PeriodSeconds
		}
		bridgeContainer.ReadinessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: hc.HTTPGet.Path,
					Port: intstr.FromInt32(hc.HTTPGet.Port),
				},
			},
			InitialDelaySeconds: initialDelay,
			PeriodSeconds:       period,
		}
	}

	// Inject credential env vars into both bridge and SDK containers.
	// The bridge needs them for the credential proxy; the SDK container
	// needs them because the agent process requires API keys directly.
	for _, cred := range agentConfig.Spec.Credentials {
		envVar := corev1.EnvVar{
			Name: cred.Name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: cred.SecretRef.Name},
					Key:                  cred.SecretRef.Key,
				},
			},
		}
		bridgeContainer.Env = append(bridgeContainer.Env, envVar)
		sdkContainer.Env = append(sdkContainer.Env, envVar)
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: sandbox.Namespace,
			Labels: map[string]string{
				"factory.example.com/sandbox":     sandbox.Name,
				"factory.example.com/pool":        sandbox.Spec.PoolRef.Name,
				"factory.example.com/agent-config": sandbox.Spec.AgentConfigRef.Name,
			},
		},
		Spec: corev1.PodSpec{
			InitContainers: initContainers,
			Containers:     []corev1.Container{sdkContainer, bridgeContainer},
			Volumes:        volumes,
			RestartPolicy:  corev1.RestartPolicyNever,
		},
	}
}

func (r *SandboxReconciler) updateConditions(sandbox *factoryv1alpha1.Sandbox, podReady bool) {
	readyStatus := metav1.ConditionFalse
	readyReason := "PodNotReady"
	readyMessage := "Sandbox pod is not yet ready"
	if podReady {
		readyStatus = metav1.ConditionTrue
		readyReason = "PodReady"
		readyMessage = "Sandbox pod is ready"
	}

	meta.SetStatusCondition(&sandbox.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             readyStatus,
		Reason:             readyReason,
		Message:            readyMessage,
		ObservedGeneration: sandbox.Generation,
		LastTransitionTime: metav1.Now(),
	})
}

func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1alpha1.Sandbox{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}
