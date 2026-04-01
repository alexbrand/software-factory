package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

// waitForCondition polls until the condition function returns true or the timeout expires.
func waitForCondition(t *testing.T, timeout time.Duration, interval time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatal("timed out waiting for condition")
}

// createNamespace creates a namespace for test isolation.
func createNamespace(t *testing.T, ctx context.Context, k8sClient client.Client, name string) {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
}

// TestIntegration runs all envtest-based integration tests under a single envtest environment.
// This avoids controller name conflicts from starting multiple managers in the same process.
func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration tests in short mode")
	}

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	defer testEnv.Stop() //nolint:errcheck

	err = factoryv1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatalf("adding scheme: %v", err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		t.Fatalf("creating manager: %v", err)
	}

	// Register all controllers.
	if err := (&PoolReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		t.Fatalf("setting up pool controller: %v", err)
	}
	if err := (&SandboxReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		t.Fatalf("setting up sandbox controller: %v", err)
	}
	if err := (&WorkflowReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		t.Fatalf("setting up workflow controller: %v", err)
	}
	if err := (&TaskReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		t.Fatalf("setting up task controller: %v", err)
	}
	if err := (&SessionReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		t.Fatalf("setting up session controller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := mgr.Start(ctx); err != nil {
			_ = err
		}
	}()

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("waiting for cache sync")
	}

	k8sClient := mgr.GetClient()

	t.Run("PoolCreatesSandboxes", func(t *testing.T) {
		ns := "test-pool-creates"
		createNamespace(t, ctx, k8sClient, ns)

		agentConfig := &factoryv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: ns},
			Spec: factoryv1alpha1.AgentConfigSpec{
				AgentType: "test",
				SDK:       factoryv1alpha1.SDKConfig{Image: "test-sdk:latest"},
				Bridge:    factoryv1alpha1.BridgeConfig{Image: "test-bridge:latest"},
			},
		}
		if err := k8sClient.Create(ctx, agentConfig); err != nil {
			t.Fatalf("creating agent config: %v", err)
		}

		pool := &factoryv1alpha1.Pool{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pool", Namespace: ns},
			Spec: factoryv1alpha1.PoolSpec{
				AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "test-agent"},
				Replicas:       factoryv1alpha1.ReplicasConfig{Min: 2, Max: 5},
			},
		}
		if err := k8sClient.Create(ctx, pool); err != nil {
			t.Fatalf("creating pool: %v", err)
		}

		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var sandboxList factoryv1alpha1.SandboxList
			if err := k8sClient.List(ctx, &sandboxList, client.InNamespace(ns)); err != nil {
				return false
			}
			return len(sandboxList.Items) >= 2
		})

		var sandboxList factoryv1alpha1.SandboxList
		if err := k8sClient.List(ctx, &sandboxList, client.InNamespace(ns)); err != nil {
			t.Fatalf("listing sandboxes: %v", err)
		}
		if len(sandboxList.Items) < 2 {
			t.Errorf("expected at least 2 sandboxes, got %d", len(sandboxList.Items))
		}
		for _, sb := range sandboxList.Items {
			if sb.Spec.PoolRef.Name != "test-pool" {
				t.Errorf("sandbox %s has poolRef %s, want test-pool", sb.Name, sb.Spec.PoolRef.Name)
			}
			if sb.Spec.AgentConfigRef.Name != "test-agent" {
				t.Errorf("sandbox %s has agentConfigRef %s, want test-agent", sb.Name, sb.Spec.AgentConfigRef.Name)
			}
		}
	})

	t.Run("SandboxCreatesPod", func(t *testing.T) {
		ns := "test-sandbox-pod"
		createNamespace(t, ctx, k8sClient, ns)

		agentConfig := &factoryv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: ns},
			Spec: factoryv1alpha1.AgentConfigSpec{
				AgentType: "test",
				SDK:       factoryv1alpha1.SDKConfig{Image: "test-sdk:latest"},
				Bridge:    factoryv1alpha1.BridgeConfig{Image: "test-bridge:latest"},
			},
		}
		if err := k8sClient.Create(ctx, agentConfig); err != nil {
			t.Fatalf("creating agent config: %v", err)
		}

		pool := &factoryv1alpha1.Pool{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pool", Namespace: ns},
			Spec: factoryv1alpha1.PoolSpec{
				AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "test-agent"},
				Replicas:       factoryv1alpha1.ReplicasConfig{Min: 0, Max: 5},
			},
		}
		if err := k8sClient.Create(ctx, pool); err != nil {
			t.Fatalf("creating pool: %v", err)
		}

		sandbox := &factoryv1alpha1.Sandbox{
			ObjectMeta: metav1.ObjectMeta{Name: "test-sandbox", Namespace: ns},
			Spec: factoryv1alpha1.SandboxSpec{
				PoolRef:        factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
				AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "test-agent"},
			},
		}
		if err := k8sClient.Create(ctx, sandbox); err != nil {
			t.Fatalf("creating sandbox: %v", err)
		}

		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var podList corev1.PodList
			if err := k8sClient.List(ctx, &podList, client.InNamespace(ns)); err != nil {
				return false
			}
			return len(podList.Items) >= 1
		})

		var podList corev1.PodList
		if err := k8sClient.List(ctx, &podList, client.InNamespace(ns)); err != nil {
			t.Fatalf("listing pods: %v", err)
		}
		if len(podList.Items) == 0 {
			t.Fatal("expected at least 1 pod")
		}

		pod := podList.Items[0]
		containerNames := make(map[string]bool)
		for _, c := range pod.Spec.Containers {
			containerNames[c.Name] = true
		}
		if !containerNames["sandbox-agent-sdk"] {
			t.Error("expected sandbox-agent-sdk container in pod")
		}
		if !containerNames["bridge"] {
			t.Error("expected bridge container in pod")
		}

		hasWorkspaceVolume := false
		for _, v := range pod.Spec.Volumes {
			if v.Name == "workspace" {
				hasWorkspaceVolume = true
				break
			}
		}
		if !hasWorkspaceVolume {
			t.Error("expected workspace volume in pod")
		}
	})

	t.Run("WorkflowDAGExecution", func(t *testing.T) {
		ns := "test-workflow-dag"
		createNamespace(t, ctx, k8sClient, ns)

		workflow := &factoryv1alpha1.Workflow{
			ObjectMeta: metav1.ObjectMeta{Name: "diamond", Namespace: ns},
			Spec: factoryv1alpha1.WorkflowSpec{
				Defaults: &factoryv1alpha1.WorkflowDefaults{
					PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
				},
				Tasks: []factoryv1alpha1.WorkflowTask{
					{Name: "a", Spec: factoryv1alpha1.TaskInlineSpec{Prompt: "task A"}},
					{Name: "b", DependsOn: []string{"a"}, Spec: factoryv1alpha1.TaskInlineSpec{Prompt: "task B"}},
					{Name: "c", DependsOn: []string{"a"}, Spec: factoryv1alpha1.TaskInlineSpec{Prompt: "task C"}},
					{Name: "d", DependsOn: []string{"b", "c"}, Spec: factoryv1alpha1.TaskInlineSpec{Prompt: "task D"}},
				},
			},
		}
		if err := k8sClient.Create(ctx, workflow); err != nil {
			t.Fatalf("creating workflow: %v", err)
		}

		// Wait for workflow to transition to Running.
		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var wf factoryv1alpha1.Workflow
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "diamond", Namespace: ns}, &wf); err != nil {
				return false
			}
			return wf.Status.Phase == factoryv1alpha1.WorkflowPhaseRunning
		})

		// Verify root task A was created.
		waitForCondition(t, 10*time.Second, 500*time.Millisecond, func() bool {
			var taskList factoryv1alpha1.TaskList
			if err := k8sClient.List(ctx, &taskList, client.InNamespace(ns)); err != nil {
				return false
			}
			return len(taskList.Items) >= 1
		})

		var taskList factoryv1alpha1.TaskList
		if err := k8sClient.List(ctx, &taskList, client.InNamespace(ns)); err != nil {
			t.Fatalf("listing tasks: %v", err)
		}

		taskNames := make(map[string]bool)
		for _, task := range taskList.Items {
			taskNames[task.Name] = true
		}
		if !taskNames["diamond-a"] {
			t.Error("expected task diamond-a to be created")
		}
		if taskNames["diamond-d"] {
			t.Error("task diamond-d should not be created yet")
		}

		// Simulate task A succeeding.
		var taskA factoryv1alpha1.Task
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "diamond-a", Namespace: ns}, &taskA); err != nil {
			t.Fatalf("getting task A: %v", err)
		}
		taskA.Status.Phase = factoryv1alpha1.TaskPhaseSucceeded
		now := metav1.Now()
		taskA.Status.CompletedAt = &now
		if err := k8sClient.Status().Update(ctx, &taskA); err != nil {
			t.Fatalf("updating task A status: %v", err)
		}

		// Wait for B and C to be created.
		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var tl factoryv1alpha1.TaskList
			if err := k8sClient.List(ctx, &tl, client.InNamespace(ns)); err != nil {
				return false
			}
			names := make(map[string]bool)
			for _, task := range tl.Items {
				names[task.Name] = true
			}
			return names["diamond-b"] && names["diamond-c"]
		})

		// Simulate B and C succeeding.
		for _, taskName := range []string{"diamond-b", "diamond-c"} {
			var task factoryv1alpha1.Task
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: taskName, Namespace: ns}, &task); err != nil {
				t.Fatalf("getting task %s: %v", taskName, err)
			}
			task.Status.Phase = factoryv1alpha1.TaskPhaseSucceeded
			completedAt := metav1.Now()
			task.Status.CompletedAt = &completedAt
			if err := k8sClient.Status().Update(ctx, &task); err != nil {
				t.Fatalf("updating task %s status: %v", taskName, err)
			}
		}

		// Wait for task D to be created.
		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var tl factoryv1alpha1.TaskList
			if err := k8sClient.List(ctx, &tl, client.InNamespace(ns)); err != nil {
				return false
			}
			for _, task := range tl.Items {
				if task.Name == "diamond-d" {
					return true
				}
			}
			return false
		})

		// Simulate D succeeding.
		var taskD factoryv1alpha1.Task
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "diamond-d", Namespace: ns}, &taskD); err != nil {
			t.Fatalf("getting task D: %v", err)
		}
		taskD.Status.Phase = factoryv1alpha1.TaskPhaseSucceeded
		doneAt := metav1.Now()
		taskD.Status.CompletedAt = &doneAt
		if err := k8sClient.Status().Update(ctx, &taskD); err != nil {
			t.Fatalf("updating task D status: %v", err)
		}

		// Wait for workflow to succeed.
		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var wf factoryv1alpha1.Workflow
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "diamond", Namespace: ns}, &wf); err != nil {
				return false
			}
			return wf.Status.Phase == factoryv1alpha1.WorkflowPhaseSucceeded
		})
	})

	t.Run("TaskClaimsSandbox", func(t *testing.T) {
		ns := "test-task-claim"
		createNamespace(t, ctx, k8sClient, ns)

		agentConfig := &factoryv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: ns},
			Spec: factoryv1alpha1.AgentConfigSpec{
				AgentType: "test",
				SDK:       factoryv1alpha1.SDKConfig{Image: "test-sdk:latest"},
				Bridge:    factoryv1alpha1.BridgeConfig{Image: "test-bridge:latest"},
			},
		}
		if err := k8sClient.Create(ctx, agentConfig); err != nil {
			t.Fatalf("creating agent config: %v", err)
		}

		pool := &factoryv1alpha1.Pool{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pool", Namespace: ns},
			Spec: factoryv1alpha1.PoolSpec{
				AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "test-agent"},
				Replicas:       factoryv1alpha1.ReplicasConfig{Min: 1, Max: 5},
			},
		}
		if err := k8sClient.Create(ctx, pool); err != nil {
			t.Fatalf("creating pool: %v", err)
		}

		// Wait for a sandbox to be created.
		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var sbList factoryv1alpha1.SandboxList
			if err := k8sClient.List(ctx, &sbList, client.InNamespace(ns)); err != nil {
				return false
			}
			return len(sbList.Items) >= 1
		})

		// Set sandbox to Ready (simulating pod becoming ready).
		var sbList factoryv1alpha1.SandboxList
		if err := k8sClient.List(ctx, &sbList, client.InNamespace(ns)); err != nil {
			t.Fatalf("listing sandboxes: %v", err)
		}
		sb := &sbList.Items[0]
		sb.Status.Phase = factoryv1alpha1.SandboxPhaseReady
		if err := k8sClient.Status().Update(ctx, sb); err != nil {
			t.Fatalf("updating sandbox to Ready: %v", err)
		}
		sandboxName := sb.Name

		task := &factoryv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "test-task", Namespace: ns},
			Spec: factoryv1alpha1.TaskSpec{
				PoolRef: factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
				Prompt:  "do some work",
			},
		}
		if err := k8sClient.Create(ctx, task); err != nil {
			t.Fatalf("creating task: %v", err)
		}

		// Wait for task to claim sandbox (transition to Running).
		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var tk factoryv1alpha1.Task
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-task", Namespace: ns}, &tk); err != nil {
				return false
			}
			return tk.Status.Phase == factoryv1alpha1.TaskPhaseRunning
		})

		var updatedTask factoryv1alpha1.Task
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-task", Namespace: ns}, &updatedTask); err != nil {
			t.Fatalf("getting task: %v", err)
		}
		if updatedTask.Status.SandboxRef == nil {
			t.Fatal("expected task to have sandbox ref")
		}
		if updatedTask.Status.SandboxRef.Name != sandboxName {
			t.Errorf("expected sandbox ref %s, got %s", sandboxName, updatedTask.Status.SandboxRef.Name)
		}

		var updatedSandbox factoryv1alpha1.Sandbox
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: sandboxName, Namespace: ns}, &updatedSandbox); err != nil {
			t.Fatalf("getting sandbox: %v", err)
		}
		if updatedSandbox.Status.Phase != factoryv1alpha1.SandboxPhaseAssigned {
			t.Errorf("expected sandbox phase Assigned, got %s", updatedSandbox.Status.Phase)
		}
	})

	t.Run("ScaleUp", func(t *testing.T) {
		ns := "test-scale-up"
		createNamespace(t, ctx, k8sClient, ns)

		agentConfig := &factoryv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: ns},
			Spec: factoryv1alpha1.AgentConfigSpec{
				AgentType: "test",
				SDK:       factoryv1alpha1.SDKConfig{Image: "test-sdk:latest"},
				Bridge:    factoryv1alpha1.BridgeConfig{Image: "test-bridge:latest"},
			},
		}
		if err := k8sClient.Create(ctx, agentConfig); err != nil {
			t.Fatalf("creating agent config: %v", err)
		}

		threshold := "0.5"
		pool := &factoryv1alpha1.Pool{
			ObjectMeta: metav1.ObjectMeta{Name: "scale-pool", Namespace: ns},
			Spec: factoryv1alpha1.PoolSpec{
				AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "test-agent"},
				Replicas: factoryv1alpha1.ReplicasConfig{
					Min:              2,
					Max:              10,
					ScaleUpThreshold: &threshold,
				},
			},
		}
		if err := k8sClient.Create(ctx, pool); err != nil {
			t.Fatalf("creating pool: %v", err)
		}

		// Wait for initial 2 sandboxes.
		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var sbList factoryv1alpha1.SandboxList
			if err := k8sClient.List(ctx, &sbList, client.InNamespace(ns)); err != nil {
				return false
			}
			return len(sbList.Items) >= 2
		})

		// Set all sandboxes to Active to trigger scale-up (ratio = 1.0 > 0.5).
		var sbList factoryv1alpha1.SandboxList
		if err := k8sClient.List(ctx, &sbList, client.InNamespace(ns)); err != nil {
			t.Fatalf("listing sandboxes: %v", err)
		}
		for i := range sbList.Items {
			sb := &sbList.Items[i]
			sb.Status.Phase = factoryv1alpha1.SandboxPhaseActive
			if err := k8sClient.Status().Update(ctx, sb); err != nil {
				t.Fatalf("updating sandbox %s to Active: %v", sb.Name, err)
			}
		}

		// Wait for more sandboxes to be created.
		waitForCondition(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var sbList factoryv1alpha1.SandboxList
			if err := k8sClient.List(ctx, &sbList, client.InNamespace(ns)); err != nil {
				return false
			}
			return len(sbList.Items) > 2
		})

		var finalList factoryv1alpha1.SandboxList
		if err := k8sClient.List(ctx, &finalList, client.InNamespace(ns)); err != nil {
			t.Fatalf("listing sandboxes: %v", err)
		}
		if len(finalList.Items) <= 2 {
			t.Errorf("expected more than 2 sandboxes after scale-up, got %d", len(finalList.Items))
		}
	})
}
