package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

func newTask(name, namespace, poolName string) *factoryv1alpha1.Task {
	return &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("task-uid-" + name),
		},
		Spec: factoryv1alpha1.TaskSpec{
			PoolRef: factoryv1alpha1.LocalObjectReference{Name: poolName},
			Prompt:  "test prompt",
		},
	}
}

func TestTaskReconciler_PendingNoSandbox(t *testing.T) {
	scheme := newScheme()
	task := newTask("test-task", "default", "test-pool")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task).
		WithStatusSubresource(&factoryv1alpha1.Task{}, &factoryv1alpha1.Sandbox{}).
		WithIndex(&factoryv1alpha1.Sandbox{}, "spec.poolRef.name", func(obj client.Object) []string {
			sb, ok := obj.(*factoryv1alpha1.Sandbox)
			if !ok {
				return nil
			}
			return []string{sb.Spec.PoolRef.Name}
		}).
		Build()

	r := &TaskReconciler{Client: fakeClient, Scheme: scheme}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != defaultRequeueDelay {
		t.Errorf("expected requeue after %v, got %v", defaultRequeueDelay, result.RequeueAfter)
	}
}

func TestTaskReconciler_PendingClaimsSandbox(t *testing.T) {
	scheme := newScheme()
	task := newTask("test-task", "default", "test-pool")

	sb := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sb-1",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SandboxSpec{
			PoolRef:        factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "agent"},
		},
		Status: factoryv1alpha1.SandboxStatus{
			Phase: factoryv1alpha1.SandboxPhaseReady,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task, sb).
		WithStatusSubresource(&factoryv1alpha1.Task{}, &factoryv1alpha1.Sandbox{}, &factoryv1alpha1.Session{}).
		WithIndex(&factoryv1alpha1.Sandbox{}, "spec.poolRef.name", func(obj client.Object) []string {
			sandbox, ok := obj.(*factoryv1alpha1.Sandbox)
			if !ok {
				return nil
			}
			return []string{sandbox.Spec.PoolRef.Name}
		}).
		Build()

	r := &TaskReconciler{Client: fakeClient, Scheme: scheme}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Requeue {
		t.Error("expected requeue after claiming sandbox")
	}

	// Verify sandbox was claimed.
	var updatedSb factoryv1alpha1.Sandbox
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "sb-1", Namespace: "default"}, &updatedSb); err != nil {
		t.Fatalf("fetching sandbox: %v", err)
	}
	if updatedSb.Status.Phase != factoryv1alpha1.SandboxPhaseAssigned {
		t.Errorf("expected sandbox phase Assigned, got %s", updatedSb.Status.Phase)
	}
	if updatedSb.Status.AssignedTask != "test-task" {
		t.Errorf("expected assigned task test-task, got %s", updatedSb.Status.AssignedTask)
	}

	// Verify task transitioned to Running.
	var updatedTask factoryv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("fetching task: %v", err)
	}
	if updatedTask.Status.Phase != factoryv1alpha1.TaskPhaseRunning {
		t.Errorf("expected task phase Running, got %s", updatedTask.Status.Phase)
	}
	if updatedTask.Status.SandboxRef == nil || updatedTask.Status.SandboxRef.Name != "sb-1" {
		t.Error("expected sandbox ref to be set")
	}
}

func TestTaskReconciler_RunningSessionCompleted(t *testing.T) {
	scheme := newScheme()
	now := metav1.Now()
	task := newTask("test-task", "default", "test-pool")
	task.Status = factoryv1alpha1.TaskStatus{
		Phase:      factoryv1alpha1.TaskPhaseRunning,
		SandboxRef: &factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
		SessionRef: &factoryv1alpha1.LocalObjectReference{Name: "test-task-session-0"},
		StartedAt:  &now,
		Attempts:   1,
	}

	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-task-session-0",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "default",
			Prompt:     "test",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase: factoryv1alpha1.SessionPhaseCompleted,
		},
	}

	sb := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sb-1",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SandboxSpec{
			PoolRef:        factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "agent"},
		},
		Status: factoryv1alpha1.SandboxStatus{
			Phase:        factoryv1alpha1.SandboxPhaseAssigned,
			AssignedTask: "test-task",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task, session, sb).
		WithStatusSubresource(&factoryv1alpha1.Task{}, &factoryv1alpha1.Sandbox{}, &factoryv1alpha1.Session{}).
		Build()

	r := &TaskReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify task succeeded.
	var updatedTask factoryv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("fetching task: %v", err)
	}
	if updatedTask.Status.Phase != factoryv1alpha1.TaskPhaseSucceeded {
		t.Errorf("expected task phase Succeeded, got %s", updatedTask.Status.Phase)
	}
	if updatedTask.Status.CompletedAt == nil {
		t.Error("expected completedAt to be set")
	}

	// Verify sandbox was released.
	var updatedSb factoryv1alpha1.Sandbox
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "sb-1", Namespace: "default"}, &updatedSb); err != nil {
		t.Fatalf("fetching sandbox: %v", err)
	}
	if updatedSb.Status.Phase != factoryv1alpha1.SandboxPhaseIdle {
		t.Errorf("expected sandbox phase Idle, got %s", updatedSb.Status.Phase)
	}
}

func TestTaskReconciler_RunningSessionFailedWithRetry(t *testing.T) {
	scheme := newScheme()
	now := metav1.Now()
	retries := int32(2)
	task := newTask("retry-task", "default", "test-pool")
	task.Spec.Retries = &retries
	task.Status = factoryv1alpha1.TaskStatus{
		Phase:      factoryv1alpha1.TaskPhaseRunning,
		SandboxRef: &factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
		SessionRef: &factoryv1alpha1.LocalObjectReference{Name: "retry-task-session-0"},
		StartedAt:  &now,
		Attempts:   1,
	}

	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retry-task-session-0",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "default",
			Prompt:     "test",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase: factoryv1alpha1.SessionPhaseFailed,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task, session).
		WithStatusSubresource(&factoryv1alpha1.Task{}, &factoryv1alpha1.Session{}).
		Build()

	r := &TaskReconciler{Client: fakeClient, Scheme: scheme}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "retry-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Requeue {
		t.Error("expected requeue for retry")
	}

	// Verify task is still Running (retrying), session ref cleared.
	var updatedTask factoryv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "retry-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("fetching task: %v", err)
	}
	if updatedTask.Status.Phase != factoryv1alpha1.TaskPhaseRunning {
		t.Errorf("expected task phase Running (retry), got %s", updatedTask.Status.Phase)
	}
	if updatedTask.Status.SessionRef != nil {
		t.Error("expected session ref to be cleared for retry")
	}
}

func TestTaskReconciler_RunningSessionFailedNoRetries(t *testing.T) {
	scheme := newScheme()
	now := metav1.Now()
	task := newTask("fail-task", "default", "test-pool")
	task.Status = factoryv1alpha1.TaskStatus{
		Phase:      factoryv1alpha1.TaskPhaseRunning,
		SandboxRef: &factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
		SessionRef: &factoryv1alpha1.LocalObjectReference{Name: "fail-task-session-0"},
		StartedAt:  &now,
		Attempts:   1,
	}

	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fail-task-session-0",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "default",
			Prompt:     "test",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase: factoryv1alpha1.SessionPhaseFailed,
		},
	}

	sb := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sb-1",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SandboxSpec{
			PoolRef:        factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "agent"},
		},
		Status: factoryv1alpha1.SandboxStatus{
			Phase: factoryv1alpha1.SandboxPhaseAssigned,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task, session, sb).
		WithStatusSubresource(&factoryv1alpha1.Task{}, &factoryv1alpha1.Sandbox{}, &factoryv1alpha1.Session{}).
		Build()

	r := &TaskReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "fail-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updatedTask factoryv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "fail-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("fetching task: %v", err)
	}
	if updatedTask.Status.Phase != factoryv1alpha1.TaskPhaseFailed {
		t.Errorf("expected task phase Failed, got %s", updatedTask.Status.Phase)
	}
}

func TestTaskReconciler_Timeout(t *testing.T) {
	scheme := newScheme()
	pastTime := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	timeout := metav1.Duration{Duration: 1 * time.Hour}
	task := newTask("timeout-task", "default", "test-pool")
	task.Spec.Timeout = &timeout
	task.Status = factoryv1alpha1.TaskStatus{
		Phase:      factoryv1alpha1.TaskPhaseRunning,
		SandboxRef: &factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
		SessionRef: &factoryv1alpha1.LocalObjectReference{Name: "timeout-task-session-0"},
		StartedAt:  &pastTime,
		Attempts:   1,
	}

	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "timeout-task-session-0",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "default",
			Prompt:     "test",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase: factoryv1alpha1.SessionPhaseActive,
		},
	}

	sb := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sb-1",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SandboxSpec{
			PoolRef:        factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "agent"},
		},
		Status: factoryv1alpha1.SandboxStatus{
			Phase: factoryv1alpha1.SandboxPhaseAssigned,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(task, session, sb).
		WithStatusSubresource(&factoryv1alpha1.Task{}, &factoryv1alpha1.Sandbox{}, &factoryv1alpha1.Session{}).
		Build()

	r := &TaskReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "timeout-task", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify task failed.
	var updatedTask factoryv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "timeout-task", Namespace: "default"}, &updatedTask); err != nil {
		t.Fatalf("fetching task: %v", err)
	}
	if updatedTask.Status.Phase != factoryv1alpha1.TaskPhaseFailed {
		t.Errorf("expected task phase Failed on timeout, got %s", updatedTask.Status.Phase)
	}

	// Verify session was cancelled.
	var updatedSession factoryv1alpha1.Session
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "timeout-task-session-0", Namespace: "default"}, &updatedSession); err != nil {
		t.Fatalf("fetching session: %v", err)
	}
	if updatedSession.Status.Phase != factoryv1alpha1.SessionPhaseCancelled {
		t.Errorf("expected session phase Cancelled, got %s", updatedSession.Status.Phase)
	}

	// Verify sandbox was released.
	var updatedSb factoryv1alpha1.Sandbox
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "sb-1", Namespace: "default"}, &updatedSb); err != nil {
		t.Fatalf("fetching sandbox: %v", err)
	}
	if updatedSb.Status.Phase != factoryv1alpha1.SandboxPhaseIdle {
		t.Errorf("expected sandbox phase Idle, got %s", updatedSb.Status.Phase)
	}
}

func TestTaskReconciler_NotFound(t *testing.T) {
	scheme := newScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &TaskReconciler{Client: fakeClient, Scheme: scheme}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "missing", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not found")
	}
}

func TestTaskReconciler_TerminalPhaseNoOp(t *testing.T) {
	scheme := newScheme()
	for _, phase := range []factoryv1alpha1.TaskPhase{
		factoryv1alpha1.TaskPhaseSucceeded,
		factoryv1alpha1.TaskPhaseFailed,
		factoryv1alpha1.TaskPhaseCancelled,
	} {
		t.Run(string(phase), func(t *testing.T) {
			task := newTask("terminal-task", "default", "pool")
			task.Status.Phase = phase

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(task).
				WithStatusSubresource(&factoryv1alpha1.Task{}).
				Build()

			r := &TaskReconciler{Client: fakeClient, Scheme: scheme}
			result, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "terminal-task", Namespace: "default"},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Requeue || result.RequeueAfter > 0 {
				t.Error("expected no requeue for terminal phase")
			}
		})
	}
}
