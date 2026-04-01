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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

func newWorkflow(name, namespace string, tasks []factoryv1alpha1.WorkflowTask, defaults *factoryv1alpha1.WorkflowDefaults) *factoryv1alpha1.Workflow {
	return &factoryv1alpha1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			UID:        types.UID("wf-uid-" + name),
			Finalizers: []string{workflowFinalizerName},
		},
		Spec: factoryv1alpha1.WorkflowSpec{
			Defaults: defaults,
			Tasks:    tasks,
		},
	}
}

func TestWorkflowReconciler_PendingValidDAG(t *testing.T) {
	scheme := newScheme()
	wf := newWorkflow("test-wf", "default", []factoryv1alpha1.WorkflowTask{
		{
			Name: "root",
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "do root work",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
			},
		},
		{
			Name:      "child",
			DependsOn: []string{"root"},
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "do child work",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "test-pool"},
			},
		},
	}, nil)

	var statusUpdates int
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wf).
		WithStatusSubresource(&factoryv1alpha1.Workflow{}, &factoryv1alpha1.Task{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				statusUpdates++
				return c.SubResource(subResourceName).Update(ctx, obj, opts...)
			},
		}).
		Build()

	r := &WorkflowReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-wf", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify workflow transitioned to Running.
	var updated factoryv1alpha1.Workflow
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-wf", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("fetching workflow: %v", err)
	}
	if updated.Status.Phase != factoryv1alpha1.WorkflowPhaseRunning {
		t.Errorf("expected phase Running, got %s", updated.Status.Phase)
	}
	if updated.Status.StartedAt == nil {
		t.Error("expected startedAt to be set")
	}

	// Verify root task was created.
	var taskList factoryv1alpha1.TaskList
	if err := fakeClient.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("expected 1 task, got %d", len(taskList.Items))
	}
	if taskList.Items[0].Name != "test-wf-root" {
		t.Errorf("expected task name test-wf-root, got %s", taskList.Items[0].Name)
	}
	if taskList.Items[0].Spec.Prompt != "do root work" {
		t.Errorf("expected prompt 'do root work', got %s", taskList.Items[0].Spec.Prompt)
	}
}

func TestWorkflowReconciler_InvalidDAG_Cycle(t *testing.T) {
	scheme := newScheme()
	wf := newWorkflow("cycle-wf", "default", []factoryv1alpha1.WorkflowTask{
		{
			Name:      "a",
			DependsOn: []string{"b"},
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "a",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "pool"},
			},
		},
		{
			Name:      "b",
			DependsOn: []string{"a"},
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "b",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "pool"},
			},
		},
	}, nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wf).
		WithStatusSubresource(&factoryv1alpha1.Workflow{}).
		Build()

	r := &WorkflowReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "cycle-wf", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated factoryv1alpha1.Workflow
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "cycle-wf", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("fetching workflow: %v", err)
	}
	if updated.Status.Phase != factoryv1alpha1.WorkflowPhaseFailed {
		t.Errorf("expected phase Failed for cyclic DAG, got %s", updated.Status.Phase)
	}
}

func TestWorkflowReconciler_InvalidDAG_UnknownDep(t *testing.T) {
	scheme := newScheme()
	wf := newWorkflow("bad-dep-wf", "default", []factoryv1alpha1.WorkflowTask{
		{
			Name:      "a",
			DependsOn: []string{"nonexistent"},
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "a",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "pool"},
			},
		},
	}, nil)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wf).
		WithStatusSubresource(&factoryv1alpha1.Workflow{}).
		Build()

	r := &WorkflowReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-dep-wf", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated factoryv1alpha1.Workflow
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "bad-dep-wf", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("fetching workflow: %v", err)
	}
	if updated.Status.Phase != factoryv1alpha1.WorkflowPhaseFailed {
		t.Errorf("expected phase Failed for unknown dep, got %s", updated.Status.Phase)
	}
}

func TestWorkflowReconciler_RunningAllTasksSucceeded(t *testing.T) {
	scheme := newScheme()
	wf := newWorkflow("done-wf", "default", []factoryv1alpha1.WorkflowTask{
		{
			Name: "a",
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "a",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "pool"},
			},
		},
		{
			Name:      "b",
			DependsOn: []string{"a"},
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "b",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "pool"},
			},
		},
	}, nil)
	wf.Status.Phase = factoryv1alpha1.WorkflowPhaseRunning
	now := metav1.Now()
	wf.Status.StartedAt = &now
	wf.Status.TaskStatuses = map[string]string{
		"a": string(factoryv1alpha1.TaskPhaseSucceeded),
		"b": string(factoryv1alpha1.TaskPhaseSucceeded),
	}

	taskA := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "done-wf-a",
			Namespace: "default",
			Labels: map[string]string{
				"factory.example.com/workflow":      "done-wf",
				"factory.example.com/workflow-task": "a",
			},
		},
		Spec: factoryv1alpha1.TaskSpec{
			WorkflowRef: &factoryv1alpha1.LocalObjectReference{Name: "done-wf"},
			PoolRef:     factoryv1alpha1.LocalObjectReference{Name: "pool"},
			Prompt:      "a",
		},
		Status: factoryv1alpha1.TaskStatus{Phase: factoryv1alpha1.TaskPhaseSucceeded},
	}
	taskB := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "done-wf-b",
			Namespace: "default",
			Labels: map[string]string{
				"factory.example.com/workflow":      "done-wf",
				"factory.example.com/workflow-task": "b",
			},
		},
		Spec: factoryv1alpha1.TaskSpec{
			WorkflowRef: &factoryv1alpha1.LocalObjectReference{Name: "done-wf"},
			PoolRef:     factoryv1alpha1.LocalObjectReference{Name: "pool"},
			Prompt:      "b",
		},
		Status: factoryv1alpha1.TaskStatus{Phase: factoryv1alpha1.TaskPhaseSucceeded},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wf, taskA, taskB).
		WithStatusSubresource(&factoryv1alpha1.Workflow{}, &factoryv1alpha1.Task{}).
		WithIndex(&factoryv1alpha1.Task{}, "spec.workflowRef.name", func(obj client.Object) []string {
			task, ok := obj.(*factoryv1alpha1.Task)
			if !ok || task.Spec.WorkflowRef == nil {
				return nil
			}
			return []string{task.Spec.WorkflowRef.Name}
		}).
		Build()

	r := &WorkflowReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "done-wf", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated factoryv1alpha1.Workflow
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "done-wf", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("fetching workflow: %v", err)
	}
	if updated.Status.Phase != factoryv1alpha1.WorkflowPhaseSucceeded {
		t.Errorf("expected phase Succeeded, got %s", updated.Status.Phase)
	}
	if updated.Status.CompletedAt == nil {
		t.Error("expected completedAt to be set")
	}
}

func TestWorkflowReconciler_RunningTaskFailed(t *testing.T) {
	scheme := newScheme()
	wf := newWorkflow("fail-wf", "default", []factoryv1alpha1.WorkflowTask{
		{
			Name: "a",
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "a",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "pool"},
			},
		},
	}, nil)
	wf.Status.Phase = factoryv1alpha1.WorkflowPhaseRunning
	now := metav1.Now()
	wf.Status.StartedAt = &now

	taskA := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fail-wf-a",
			Namespace: "default",
			Labels: map[string]string{
				"factory.example.com/workflow":      "fail-wf",
				"factory.example.com/workflow-task": "a",
			},
		},
		Spec: factoryv1alpha1.TaskSpec{
			WorkflowRef: &factoryv1alpha1.LocalObjectReference{Name: "fail-wf"},
			PoolRef:     factoryv1alpha1.LocalObjectReference{Name: "pool"},
			Prompt:      "a",
		},
		Status: factoryv1alpha1.TaskStatus{Phase: factoryv1alpha1.TaskPhaseFailed},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wf, taskA).
		WithStatusSubresource(&factoryv1alpha1.Workflow{}, &factoryv1alpha1.Task{}).
		WithIndex(&factoryv1alpha1.Task{}, "spec.workflowRef.name", func(obj client.Object) []string {
			task, ok := obj.(*factoryv1alpha1.Task)
			if !ok || task.Spec.WorkflowRef == nil {
				return nil
			}
			return []string{task.Spec.WorkflowRef.Name}
		}).
		Build()

	r := &WorkflowReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "fail-wf", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated factoryv1alpha1.Workflow
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "fail-wf", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("fetching workflow: %v", err)
	}
	if updated.Status.Phase != factoryv1alpha1.WorkflowPhaseFailed {
		t.Errorf("expected phase Failed, got %s", updated.Status.Phase)
	}
}

func TestWorkflowReconciler_Deletion(t *testing.T) {
	scheme := newScheme()
	now := metav1.Now()
	wf := newWorkflow("del-wf", "default", []factoryv1alpha1.WorkflowTask{
		{
			Name: "a",
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt:  "a",
				PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "pool"},
			},
		},
	}, nil)
	wf.DeletionTimestamp = &now
	wf.Status.Phase = factoryv1alpha1.WorkflowPhaseRunning

	taskA := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "del-wf-a",
			Namespace: "default",
			Labels: map[string]string{
				"factory.example.com/workflow":      "del-wf",
				"factory.example.com/workflow-task": "a",
			},
		},
		Spec: factoryv1alpha1.TaskSpec{
			WorkflowRef: &factoryv1alpha1.LocalObjectReference{Name: "del-wf"},
			PoolRef:     factoryv1alpha1.LocalObjectReference{Name: "pool"},
			Prompt:      "a",
		},
		Status: factoryv1alpha1.TaskStatus{Phase: factoryv1alpha1.TaskPhaseRunning},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wf, taskA).
		WithStatusSubresource(&factoryv1alpha1.Workflow{}, &factoryv1alpha1.Task{}).
		Build()

	r := &WorkflowReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "del-wf", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the task was cancelled.
	var task factoryv1alpha1.Task
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "del-wf-a", Namespace: "default"}, &task); err != nil {
		t.Fatalf("fetching task: %v", err)
	}
	if task.Status.Phase != factoryv1alpha1.TaskPhaseCancelled {
		t.Errorf("expected task phase Cancelled, got %s", task.Status.Phase)
	}
}

func TestWorkflowReconciler_DefaultsApplied(t *testing.T) {
	scheme := newScheme()
	timeout := metav1.Duration{Duration: 30 * time.Minute}
	retries := int32(3)
	wf := newWorkflow("defaults-wf", "default", []factoryv1alpha1.WorkflowTask{
		{
			Name: "a",
			Spec: factoryv1alpha1.TaskInlineSpec{
				Prompt: "do work",
			},
		},
	}, &factoryv1alpha1.WorkflowDefaults{
		PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "default-pool"},
		Timeout: &timeout,
		Retries: &retries,
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wf).
		WithStatusSubresource(&factoryv1alpha1.Workflow{}, &factoryv1alpha1.Task{}).
		Build()

	r := &WorkflowReconciler{Client: fakeClient, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "defaults-wf", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var taskList factoryv1alpha1.TaskList
	if err := fakeClient.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("expected 1 task, got %d", len(taskList.Items))
	}
	task := taskList.Items[0]
	if task.Spec.PoolRef.Name != "default-pool" {
		t.Errorf("expected pool ref default-pool, got %s", task.Spec.PoolRef.Name)
	}
	if task.Spec.Timeout == nil || task.Spec.Timeout.Duration != 30*time.Minute {
		t.Errorf("expected timeout 30m, got %v", task.Spec.Timeout)
	}
	if task.Spec.Retries == nil || *task.Spec.Retries != 3 {
		t.Errorf("expected retries 3, got %v", task.Spec.Retries)
	}
}

func TestWorkflowReconciler_NotFound(t *testing.T) {
	scheme := newScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &WorkflowReconciler{Client: fakeClient, Scheme: scheme}
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
