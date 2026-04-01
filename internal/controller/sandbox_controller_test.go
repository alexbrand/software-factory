package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

func int32Ptr(i int32) *int32 { return &i }

func newAgentConfig(name, namespace string) *factoryv1alpha1.AgentConfig {
	return &factoryv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: factoryv1alpha1.AgentConfigSpec{
			AgentType:   "claude-code",
			DisplayName: "Claude Code",
			SDK: factoryv1alpha1.SDKConfig{
				Image: "sdk:latest",
				Port:  int32Ptr(2468),
			},
			Bridge: factoryv1alpha1.BridgeConfig{
				Image: "bridge:latest",
				Port:  int32Ptr(8080),
			},
		},
	}
}

func newAgentConfigWithCredentials(name, namespace string) *factoryv1alpha1.AgentConfig {
	ac := newAgentConfig(name, namespace)
	ac.Spec.Credentials = []factoryv1alpha1.CredentialConfig{
		{
			Name: "API_KEY",
			SecretRef: factoryv1alpha1.SecretKeyReference{
				Name: "agent-secrets",
				Key:  "api-key",
			},
		},
	}
	return ac
}

func newTestPool(name, namespace string) *factoryv1alpha1.Pool {
	return &factoryv1alpha1.Pool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("pool-uid-" + name),
		},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "test-agent"},
			Replicas: factoryv1alpha1.ReplicasConfig{
				Min: 2,
				Max: 10,
			},
			SandboxTemplate: factoryv1alpha1.SandboxTemplate{
				Storage: &factoryv1alpha1.StorageConfig{
					Size: "50Gi",
				},
			},
		},
	}
}

func newTestSandbox(name, namespace, poolName, agentConfigName string, phase factoryv1alpha1.SandboxPhase) *factoryv1alpha1.Sandbox {
	sb := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("sandbox-uid-" + name),
		},
		Spec: factoryv1alpha1.SandboxSpec{
			PoolRef:        factoryv1alpha1.LocalObjectReference{Name: poolName},
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: agentConfigName},
		},
		Status: factoryv1alpha1.SandboxStatus{
			Phase: phase,
		},
	}
	return sb
}

func newReadyPod(name, namespace, sandboxName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"factory.example.com/sandbox": sandboxName,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func newNotReadyPod(name, namespace, sandboxName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"factory.example.com/sandbox": sandboxName,
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}
}

func TestSandboxReconciler_Reconcile(t *testing.T) {
	tests := []struct {
		name           string
		sandbox        *factoryv1alpha1.Sandbox
		pool           *factoryv1alpha1.Pool
		agentConfig    *factoryv1alpha1.AgentConfig
		existingObjs   []client.Object
		wantPhase      factoryv1alpha1.SandboxPhase
		wantPodCreated bool
		wantPVCCreated bool
		wantErr        bool
		wantRequeue    bool
	}{
		{
			name:           "create resources for new sandbox",
			sandbox:        newTestSandbox("sb-1", "default", "test-pool", "test-agent", ""),
			pool:           newTestPool("test-pool", "default"),
			agentConfig:    newAgentConfig("test-agent", "default"),
			wantPhase:      factoryv1alpha1.SandboxPhaseCreating,
			wantPodCreated: true,
			wantPVCCreated: true,
			wantRequeue:    true,
		},
		{
			name:    "transition to ready when pod is ready",
			sandbox: newTestSandbox("sb-2", "default", "test-pool", "test-agent", factoryv1alpha1.SandboxPhaseCreating),
			pool:    newTestPool("test-pool", "default"),
			agentConfig: newAgentConfig("test-agent", "default"),
			existingObjs: []client.Object{
				newReadyPod("sb-2", "default", "sb-2"),
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "sb-2-workspace", Namespace: "default"},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("50Gi")}},
					},
				},
			},
			wantPhase:   factoryv1alpha1.SandboxPhaseReady,
			wantRequeue: true,
		},
		{
			name:    "stay creating when pod not ready",
			sandbox: newTestSandbox("sb-3", "default", "test-pool", "test-agent", factoryv1alpha1.SandboxPhaseCreating),
			pool:    newTestPool("test-pool", "default"),
			agentConfig: newAgentConfig("test-agent", "default"),
			existingObjs: []client.Object{
				newNotReadyPod("sb-3", "default", "sb-3"),
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "sb-3-workspace", Namespace: "default"},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("50Gi")}},
					},
				},
			},
			wantPhase:   factoryv1alpha1.SandboxPhaseCreating,
			wantRequeue: true,
		},
		{
			name: "idle sandbox transitions to terminating after timeout",
			sandbox: func() *factoryv1alpha1.Sandbox {
				sb := newTestSandbox("sb-4", "default", "test-pool", "test-agent", factoryv1alpha1.SandboxPhaseIdle)
				oldTime := metav1.NewTime(time.Now().Add(-10 * time.Minute))
				sb.Status.LastActivityAt = &oldTime
				return sb
			}(),
			pool: func() *factoryv1alpha1.Pool {
				p := newTestPool("test-pool", "default")
				p.Spec.Replicas.IdleTimeout = &metav1.Duration{Duration: 5 * time.Minute}
				return p
			}(),
			agentConfig: newAgentConfig("test-agent", "default"),
			wantPhase:   factoryv1alpha1.SandboxPhaseTerminating,
			wantRequeue: true,
		},
		{
			name: "idle sandbox stays idle before timeout",
			sandbox: func() *factoryv1alpha1.Sandbox {
				sb := newTestSandbox("sb-5", "default", "test-pool", "test-agent", factoryv1alpha1.SandboxPhaseIdle)
				recentTime := metav1.NewTime(time.Now().Add(-1 * time.Minute))
				sb.Status.LastActivityAt = &recentTime
				return sb
			}(),
			pool: func() *factoryv1alpha1.Pool {
				p := newTestPool("test-pool", "default")
				p.Spec.Replicas.IdleTimeout = &metav1.Duration{Duration: 5 * time.Minute}
				return p
			}(),
			agentConfig: newAgentConfig("test-agent", "default"),
			wantPhase:   factoryv1alpha1.SandboxPhaseIdle,
			wantRequeue: true,
		},
		{
			name: "terminating sandbox deletes pod and PVC with Delete policy",
			sandbox: func() *factoryv1alpha1.Sandbox {
				sb := newTestSandbox("sb-6", "default", "test-pool", "test-agent", factoryv1alpha1.SandboxPhaseTerminating)
				sb.Status.PodName = "sb-6"
				sb.Status.VolumeName = "sb-6-workspace"
				return sb
			}(),
			pool: func() *factoryv1alpha1.Pool {
				p := newTestPool("test-pool", "default")
				deletePolicy := "Delete"
				p.Spec.SandboxTemplate.Storage.ReclaimPolicy = &deletePolicy
				return p
			}(),
			agentConfig: newAgentConfig("test-agent", "default"),
			existingObjs: []client.Object{
				newReadyPod("sb-6", "default", "sb-6"),
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "sb-6-workspace", Namespace: "default"},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("50Gi")}},
					},
				},
			},
			wantPhase: factoryv1alpha1.SandboxPhaseTerminating,
		},
		{
			name: "terminating sandbox retains PVC with Retain policy",
			sandbox: func() *factoryv1alpha1.Sandbox {
				sb := newTestSandbox("sb-7", "default", "test-pool", "test-agent", factoryv1alpha1.SandboxPhaseTerminating)
				sb.Status.PodName = "sb-7"
				sb.Status.VolumeName = "sb-7-workspace"
				return sb
			}(),
			pool: func() *factoryv1alpha1.Pool {
				p := newTestPool("test-pool", "default")
				retainPolicy := "Retain"
				p.Spec.SandboxTemplate.Storage.ReclaimPolicy = &retainPolicy
				return p
			}(),
			agentConfig: newAgentConfig("test-agent", "default"),
			existingObjs: []client.Object{
				newReadyPod("sb-7", "default", "sb-7"),
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "sb-7-workspace", Namespace: "default"},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("50Gi")}},
					},
				},
			},
			wantPhase: factoryv1alpha1.SandboxPhaseTerminating,
		},
		{
			name:        "sandbox not found returns no error",
			sandbox:     nil,
			pool:        newTestPool("test-pool", "default"),
			agentConfig: newAgentConfig("test-agent", "default"),
		},
		{
			name:           "sandbox with credentials creates projected volume",
			sandbox:        newTestSandbox("sb-8", "default", "test-pool", "test-agent-cred", ""),
			pool:           newTestPool("test-pool", "default"),
			agentConfig:    newAgentConfigWithCredentials("test-agent-cred", "default"),
			wantPhase:      factoryv1alpha1.SandboxPhaseCreating,
			wantPodCreated: true,
			wantPVCCreated: true,
			wantRequeue:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newScheme()
			// Register networking types
			_ = networkingv1.AddToScheme(scheme)

			var objs []client.Object
			if tt.sandbox != nil {
				objs = append(objs, tt.sandbox)
			}
			if tt.pool != nil {
				objs = append(objs, tt.pool)
			}
			if tt.agentConfig != nil {
				objs = append(objs, tt.agentConfig)
			}
			objs = append(objs, tt.existingObjs...)

			podCreated := false
			pvcCreated := false
			podDeleted := false
			pvcDeleted := false

			fc := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&factoryv1alpha1.Sandbox{}).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						switch obj.(type) {
						case *corev1.Pod:
							podCreated = true
						case *corev1.PersistentVolumeClaim:
							pvcCreated = true
						}
						return c.Create(ctx, obj, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						switch obj.(type) {
						case *corev1.Pod:
							podDeleted = true
						case *corev1.PersistentVolumeClaim:
							pvcDeleted = true
						}
						return c.Delete(ctx, obj, opts...)
					},
				}).
				Build()

			reconciler := &SandboxReconciler{
				Client: fc,
				Scheme: scheme,
			}

			sandboxName := "sb-nonexistent"
			sandboxNamespace := "default"
			if tt.sandbox != nil {
				sandboxName = tt.sandbox.Name
				sandboxNamespace = tt.sandbox.Namespace
			}

			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      sandboxName,
					Namespace: sandboxNamespace,
				},
			})

			if (err != nil) != tt.wantErr {
				t.Fatalf("Reconcile() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.sandbox == nil {
				return
			}

			if tt.wantRequeue && result.RequeueAfter == 0 && !result.Requeue {
				t.Error("expected requeue but got none")
			}

			if tt.wantPodCreated && !podCreated {
				t.Error("expected pod to be created")
			}
			if tt.wantPVCCreated && !pvcCreated {
				t.Error("expected PVC to be created")
			}

			// Check updated phase
			var updated factoryv1alpha1.Sandbox
			if err := fc.Get(context.Background(), types.NamespacedName{
				Name: tt.sandbox.Name, Namespace: tt.sandbox.Namespace,
			}, &updated); err != nil {
				t.Fatalf("getting sandbox: %v", err)
			}

			if updated.Status.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", updated.Status.Phase, tt.wantPhase)
			}

			// Verify terminating behavior
			if tt.sandbox.Status.Phase == factoryv1alpha1.SandboxPhaseTerminating {
				if !podDeleted {
					t.Error("expected pod to be deleted during termination")
				}
				if tt.pool.Spec.SandboxTemplate.Storage != nil &&
					tt.pool.Spec.SandboxTemplate.Storage.ReclaimPolicy != nil &&
					*tt.pool.Spec.SandboxTemplate.Storage.ReclaimPolicy == "Delete" {
					if !pvcDeleted {
						t.Error("expected PVC to be deleted with Delete reclaim policy")
					}
				}
				if tt.pool.Spec.SandboxTemplate.Storage != nil &&
					tt.pool.Spec.SandboxTemplate.Storage.ReclaimPolicy != nil &&
					*tt.pool.Spec.SandboxTemplate.Storage.ReclaimPolicy == "Retain" {
					if pvcDeleted {
						t.Error("PVC should be retained with Retain reclaim policy")
					}
				}
			}
		})
	}
}

func TestSandboxReconciler_PodSpec(t *testing.T) {
	scheme := newScheme()
	_ = networkingv1.AddToScheme(scheme)

	agentConfig := newAgentConfigWithCredentials("test-agent", "default")
	agentConfig.Spec.Bridge.HealthCheck = &factoryv1alpha1.HealthCheckConfig{
		HTTPGet: &factoryv1alpha1.HTTPGetConfig{
			Path: "/healthz",
			Port: 8080,
		},
		InitialDelaySeconds: int32Ptr(5),
		PeriodSeconds:       int32Ptr(10),
	}

	pool := newTestPool("test-pool", "default")
	pool.Spec.SandboxTemplate.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}

	sandbox := newTestSandbox("sb-pod", "default", "test-pool", "test-agent", "")

	objs := []client.Object{sandbox, pool, agentConfig}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&factoryv1alpha1.Sandbox{}).
		Build()

	reconciler := &SandboxReconciler{
		Client: fc,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "sb-pod", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify the pod was created with correct spec
	var pod corev1.Pod
	if err := fc.Get(context.Background(), types.NamespacedName{
		Name: "sb-pod", Namespace: "default",
	}, &pod); err != nil {
		t.Fatalf("getting pod: %v", err)
	}

	// Check init container
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(pod.Spec.InitContainers))
	}
	if pod.Spec.InitContainers[0].Name != "workspace-init" {
		t.Errorf("init container name = %q, want %q", pod.Spec.InitContainers[0].Name, "workspace-init")
	}

	// Check containers
	if len(pod.Spec.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(pod.Spec.Containers))
	}

	sdkContainer := pod.Spec.Containers[0]
	if sdkContainer.Name != "sandbox-agent-sdk" {
		t.Errorf("sdk container name = %q, want %q", sdkContainer.Name, "sandbox-agent-sdk")
	}
	if sdkContainer.Image != "sdk:latest" {
		t.Errorf("sdk container image = %q, want %q", sdkContainer.Image, "sdk:latest")
	}
	if sdkContainer.Ports[0].ContainerPort != 2468 {
		t.Errorf("sdk container port = %d, want %d", sdkContainer.Ports[0].ContainerPort, 2468)
	}

	bridgeContainer := pod.Spec.Containers[1]
	if bridgeContainer.Name != "bridge" {
		t.Errorf("bridge container name = %q, want %q", bridgeContainer.Name, "bridge")
	}
	if bridgeContainer.Image != "bridge:latest" {
		t.Errorf("bridge container image = %q, want %q", bridgeContainer.Image, "bridge:latest")
	}

	// Check volumes
	if len(pod.Spec.Volumes) != 3 {
		t.Fatalf("expected 3 volumes, got %d", len(pod.Spec.Volumes))
	}

	// Check workspace volume uses PVC
	if pod.Spec.Volumes[0].PersistentVolumeClaim == nil {
		t.Error("workspace volume should use PVC")
	}

	// Check secrets volume is projected (because credentials exist)
	if pod.Spec.Volumes[2].Projected == nil {
		t.Error("secrets volume should be projected when credentials exist")
	}

	// Check resource limits on SDK container
	cpuReq := sdkContainer.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "2" {
		t.Errorf("sdk container CPU request = %q, want %q", cpuReq.String(), "2")
	}

	// Check bridge has credential env vars
	foundCredEnv := false
	for _, env := range bridgeContainer.Env {
		if env.Name == "API_KEY" {
			foundCredEnv = true
			break
		}
	}
	if !foundCredEnv {
		t.Error("bridge container should have credential env var API_KEY")
	}

	// Check bridge has health check
	if bridgeContainer.ReadinessProbe == nil {
		t.Fatal("bridge container should have readiness probe")
	}
	if bridgeContainer.ReadinessProbe.HTTPGet == nil {
		t.Fatal("bridge readiness probe should be HTTPGet")
	}
	if bridgeContainer.ReadinessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("bridge health check path = %q, want %q", bridgeContainer.ReadinessProbe.HTTPGet.Path, "/healthz")
	}

	// Check labels
	if pod.Labels["factory.example.com/sandbox"] != "sb-pod" {
		t.Errorf("pod sandbox label = %q, want %q", pod.Labels["factory.example.com/sandbox"], "sb-pod")
	}
	if pod.Labels["factory.example.com/pool"] != "test-pool" {
		t.Errorf("pod pool label = %q, want %q", pod.Labels["factory.example.com/pool"], "test-pool")
	}

	// Verify PVC was created
	var pvc corev1.PersistentVolumeClaim
	if err := fc.Get(context.Background(), types.NamespacedName{
		Name: "sb-pod-workspace", Namespace: "default",
	}, &pvc); err != nil {
		t.Fatalf("getting PVC: %v", err)
	}

	storageReq := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storageReq.String() != "50Gi" {
		t.Errorf("PVC storage request = %q, want %q", storageReq.String(), "50Gi")
	}

	// Verify NetworkPolicy was created
	var netpol networkingv1.NetworkPolicy
	if err := fc.Get(context.Background(), types.NamespacedName{
		Name: "sb-pod-netpol", Namespace: "default",
	}, &netpol); err != nil {
		t.Fatalf("getting network policy: %v", err)
	}

	if netpol.Spec.PodSelector.MatchLabels["factory.example.com/sandbox"] != "sb-pod" {
		t.Error("network policy should select sandbox pod")
	}
}

func TestSandboxReconciler_NetworkPolicyWithEgressRules(t *testing.T) {
	scheme := newScheme()
	_ = networkingv1.AddToScheme(scheme)

	pool := newTestPool("test-pool", "default")
	pool.Spec.SandboxTemplate.NetworkPolicy = &factoryv1alpha1.NetworkPolicyConfig{
		EgressRules: []factoryv1alpha1.EgressRule{
			{
				To:    []string{"api.example.com"},
				Ports: []int32{443},
			},
		},
	}

	sandbox := newTestSandbox("sb-netpol", "default", "test-pool", "test-agent", "")
	agentConfig := newAgentConfig("test-agent", "default")

	objs := []client.Object{sandbox, pool, agentConfig}
	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&factoryv1alpha1.Sandbox{}).
		Build()

	reconciler := &SandboxReconciler{
		Client: fc,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "sb-netpol", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var netpol networkingv1.NetworkPolicy
	if err := fc.Get(context.Background(), types.NamespacedName{
		Name: "sb-netpol-netpol", Namespace: "default",
	}, &netpol); err != nil {
		t.Fatalf("getting network policy: %v", err)
	}

	// Should have DNS rule + configured egress rule
	if len(netpol.Spec.Egress) != 2 {
		t.Fatalf("expected 2 egress rules, got %d", len(netpol.Spec.Egress))
	}

	// Second rule should be the configured egress
	egressRule := netpol.Spec.Egress[1]
	if len(egressRule.Ports) != 1 || egressRule.Ports[0].Port.IntValue() != 443 {
		t.Error("expected egress rule with port 443")
	}
}

func TestSandboxReconciler_ResourceOverride(t *testing.T) {
	scheme := newScheme()
	_ = networkingv1.AddToScheme(scheme)

	pool := newTestPool("test-pool", "default")
	pool.Spec.SandboxTemplate.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("2"),
		},
	}

	// Sandbox overrides pool resources
	sandbox := newTestSandbox("sb-override", "default", "test-pool", "test-agent", "")
	sandbox.Spec.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("4"),
		},
	}

	agentConfig := newAgentConfig("test-agent", "default")
	objs := []client.Object{sandbox, pool, agentConfig}

	fc := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&factoryv1alpha1.Sandbox{}).
		Build()

	reconciler := &SandboxReconciler{
		Client: fc,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "sb-override", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var pod corev1.Pod
	if err := fc.Get(context.Background(), types.NamespacedName{
		Name: "sb-override", Namespace: "default",
	}, &pod); err != nil {
		t.Fatalf("getting pod: %v", err)
	}

	// SDK container should use sandbox's resource override, not pool's
	sdkContainer := pod.Spec.Containers[0]
	cpuReq := sdkContainer.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "4" {
		t.Errorf("expected CPU request 4 (from sandbox override), got %q", cpuReq.String())
	}
}

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "pod is ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					},
				},
			},
			want: true,
		},
		{
			name: "pod is not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			},
			want: false,
		},
		{
			name: "pod has no conditions",
			pod:  &corev1.Pod{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPodReady(tt.pod); got != tt.want {
				t.Errorf("isPodReady() = %v, want %v", got, tt.want)
			}
		})
	}
}
