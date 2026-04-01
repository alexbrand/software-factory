package controller

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

const workflowFinalizerName = "factory.example.com/workflow-cleanup"

// WorkflowReconciler reconciles a Workflow object.
type WorkflowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=factory.example.com,resources=workflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=factory.example.com,resources=workflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=tasks,verbs=get;list;watch;create;update;patch;delete

func (r *WorkflowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var workflow factoryv1alpha1.Workflow
	if err := r.Get(ctx, req.NamespacedName, &workflow); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching workflow: %w", err)
	}

	// Handle deletion: cancel running tasks.
	if !workflow.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&workflow, workflowFinalizerName) {
			if err := r.cancelAllTasks(ctx, &workflow); err != nil {
				return ctrl.Result{}, fmt.Errorf("cancelling tasks on deletion: %w", err)
			}
			controllerutil.RemoveFinalizer(&workflow, workflowFinalizerName)
			if err := r.Update(ctx, &workflow); err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer.
	if !controllerutil.ContainsFinalizer(&workflow, workflowFinalizerName) {
		controllerutil.AddFinalizer(&workflow, workflowFinalizerName)
		if err := r.Update(ctx, &workflow); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	switch workflow.Status.Phase {
	case "", factoryv1alpha1.WorkflowPhasePending:
		return r.reconcilePending(ctx, &workflow)
	case factoryv1alpha1.WorkflowPhaseRunning:
		return r.reconcileRunning(ctx, &workflow)
	case factoryv1alpha1.WorkflowPhaseSucceeded, factoryv1alpha1.WorkflowPhaseFailed, factoryv1alpha1.WorkflowPhaseCancelled:
		return ctrl.Result{}, nil
	default:
		logger.Info("unknown workflow phase", "phase", workflow.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *WorkflowReconciler) reconcilePending(ctx context.Context, workflow *factoryv1alpha1.Workflow) (ctrl.Result, error) {
	// Build and validate the DAG.
	dag, err := r.buildDAG(workflow)
	if err != nil {
		return r.setWorkflowFailed(ctx, workflow, fmt.Sprintf("invalid DAG: %v", err))
	}

	if err := dag.Validate(); err != nil {
		return r.setWorkflowFailed(ctx, workflow, fmt.Sprintf("invalid DAG: %v", err))
	}

	// Create Task CRs for root tasks.
	roots := dag.RootNodes()
	sort.Strings(roots)
	for _, taskName := range roots {
		if err := r.createTask(ctx, workflow, taskName); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating root task %q: %w", taskName, err)
		}
	}

	// Update workflow status.
	now := metav1.Now()
	workflow.Status.Phase = factoryv1alpha1.WorkflowPhaseRunning
	workflow.Status.StartedAt = &now
	if workflow.Status.TaskStatuses == nil {
		workflow.Status.TaskStatuses = make(map[string]string)
	}
	for _, t := range workflow.Spec.Tasks {
		if workflow.Status.TaskStatuses[t.Name] == "" {
			workflow.Status.TaskStatuses[t.Name] = string(factoryv1alpha1.TaskPhasePending)
		}
	}

	if err := r.Status().Update(ctx, workflow); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating workflow status to Running: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) reconcileRunning(ctx context.Context, workflow *factoryv1alpha1.Workflow) (ctrl.Result, error) {
	// List all tasks owned by this workflow.
	var taskList factoryv1alpha1.TaskList
	if err := r.List(ctx, &taskList,
		client.InNamespace(workflow.Namespace),
		client.MatchingFields{"spec.workflowRef.name": workflow.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing tasks: %w", err)
	}

	// Build current status map from actual Task CRs.
	taskPhases := make(map[string]factoryv1alpha1.TaskPhase)
	for i := range taskList.Items {
		task := &taskList.Items[i]
		// Extract the workflow task name from the task's labels.
		taskName := task.Labels["factory.example.com/workflow-task"]
		if taskName != "" {
			taskPhases[taskName] = task.Status.Phase
		}
	}

	// Update workflow task statuses.
	if workflow.Status.TaskStatuses == nil {
		workflow.Status.TaskStatuses = make(map[string]string)
	}
	for name, phase := range taskPhases {
		workflow.Status.TaskStatuses[name] = string(phase)
	}

	// Check for failure.
	for name, phase := range taskPhases {
		if phase == factoryv1alpha1.TaskPhaseFailed {
			_ = name
			now := metav1.Now()
			workflow.Status.Phase = factoryv1alpha1.WorkflowPhaseFailed
			workflow.Status.CompletedAt = &now
			if err := r.Status().Update(ctx, workflow); err != nil {
				return ctrl.Result{}, fmt.Errorf("updating workflow status to Failed: %w", err)
			}
			return ctrl.Result{}, nil
		}
	}

	// Check if all tasks succeeded.
	allSucceeded := true
	for _, wfTask := range workflow.Spec.Tasks {
		if taskPhases[wfTask.Name] != factoryv1alpha1.TaskPhaseSucceeded {
			allSucceeded = false
			break
		}
	}
	if allSucceeded {
		now := metav1.Now()
		workflow.Status.Phase = factoryv1alpha1.WorkflowPhaseSucceeded
		workflow.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, workflow); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating workflow status to Succeeded: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Find tasks that are now runnable (all deps met).
	dag, err := r.buildDAG(workflow)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building DAG: %w", err)
	}

	completed := make(map[string]bool)
	for name, phase := range taskPhases {
		if phase == factoryv1alpha1.TaskPhaseSucceeded {
			completed[name] = true
		}
	}

	runnable := dag.RunnableTasks(completed)
	sort.Strings(runnable)
	for _, taskName := range runnable {
		// Only create if not already created.
		if _, exists := taskPhases[taskName]; !exists {
			if err := r.createTask(ctx, workflow, taskName); err != nil {
				return ctrl.Result{}, fmt.Errorf("creating task %q: %w", taskName, err)
			}
			workflow.Status.TaskStatuses[taskName] = string(factoryv1alpha1.TaskPhasePending)
		}
	}

	if err := r.Status().Update(ctx, workflow); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating workflow task statuses: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) buildDAG(workflow *factoryv1alpha1.Workflow) (*DAG, error) {
	tasks := make(map[string][]string, len(workflow.Spec.Tasks))
	for _, t := range workflow.Spec.Tasks {
		if _, exists := tasks[t.Name]; exists {
			return nil, fmt.Errorf("duplicate task name %q", t.Name)
		}
		tasks[t.Name] = t.DependsOn
	}
	return NewDAG(tasks), nil
}

func (r *WorkflowReconciler) createTask(ctx context.Context, workflow *factoryv1alpha1.Workflow, taskName string) error {
	// Find the task spec in the workflow.
	var taskSpec *factoryv1alpha1.WorkflowTask
	for i := range workflow.Spec.Tasks {
		if workflow.Spec.Tasks[i].Name == taskName {
			taskSpec = &workflow.Spec.Tasks[i]
			break
		}
	}
	if taskSpec == nil {
		return fmt.Errorf("task %q not found in workflow spec", taskName)
	}

	// Determine pool ref.
	poolRef := taskSpec.Spec.PoolRef
	if poolRef == nil && workflow.Spec.Defaults != nil {
		poolRef = workflow.Spec.Defaults.PoolRef
	}
	if poolRef == nil {
		return fmt.Errorf("task %q has no pool reference and no default pool is set", taskName)
	}

	// Determine timeout and retries.
	timeout := taskSpec.Spec.Timeout
	if timeout == nil && workflow.Spec.Defaults != nil {
		timeout = workflow.Spec.Defaults.Timeout
	}
	retries := taskSpec.Spec.Retries
	if retries == nil && workflow.Spec.Defaults != nil {
		retries = workflow.Spec.Defaults.Retries
	}

	task := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", workflow.Name, taskName),
			Namespace: workflow.Namespace,
			Labels: map[string]string{
				"factory.example.com/workflow":      workflow.Name,
				"factory.example.com/workflow-task": taskName,
			},
		},
		Spec: factoryv1alpha1.TaskSpec{
			WorkflowRef: &factoryv1alpha1.LocalObjectReference{Name: workflow.Name},
			PoolRef:     *poolRef,
			Prompt:      taskSpec.Spec.Prompt,
			Inputs:      taskSpec.Spec.Inputs,
			Outputs:     taskSpec.Spec.Outputs,
			Timeout:     timeout,
			Retries:     retries,
		},
	}

	// Set owner reference so tasks are cleaned up with the workflow.
	if err := controllerutil.SetControllerReference(workflow, task, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference: %w", err)
	}

	if err := r.Create(ctx, task); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating task CR: %w", err)
	}

	return nil
}

func (r *WorkflowReconciler) setWorkflowFailed(ctx context.Context, workflow *factoryv1alpha1.Workflow, reason string) (ctrl.Result, error) {
	now := metav1.Now()
	workflow.Status.Phase = factoryv1alpha1.WorkflowPhaseFailed
	workflow.Status.CompletedAt = &now
	workflow.Status.Conditions = append(workflow.Status.Conditions, metav1.Condition{
		Type:               "Failed",
		Status:             metav1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "ValidationFailed",
		Message:            reason,
	})
	if err := r.Status().Update(ctx, workflow); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating workflow status to Failed: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *WorkflowReconciler) cancelAllTasks(ctx context.Context, workflow *factoryv1alpha1.Workflow) error {
	var taskList factoryv1alpha1.TaskList
	if err := r.List(ctx, &taskList,
		client.InNamespace(workflow.Namespace),
		client.MatchingLabels{"factory.example.com/workflow": workflow.Name},
	); err != nil {
		return fmt.Errorf("listing tasks for cancellation: %w", err)
	}

	now := metav1.Now()
	for i := range taskList.Items {
		task := &taskList.Items[i]
		phase := task.Status.Phase
		if phase == factoryv1alpha1.TaskPhaseSucceeded ||
			phase == factoryv1alpha1.TaskPhaseFailed ||
			phase == factoryv1alpha1.TaskPhaseCancelled {
			continue
		}
		task.Status.Phase = factoryv1alpha1.TaskPhaseCancelled
		task.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, task); err != nil {
			return fmt.Errorf("cancelling task %q: %w", task.Name, err)
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkflowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index tasks by workflow ref for efficient lookups.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &factoryv1alpha1.Task{}, "spec.workflowRef.name", func(obj client.Object) []string {
		task, ok := obj.(*factoryv1alpha1.Task)
		if !ok || task.Spec.WorkflowRef == nil {
			return nil
		}
		return []string{task.Spec.WorkflowRef.Name}
	}); err != nil {
		return fmt.Errorf("setting up field indexer: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1alpha1.Workflow{}).
		Owns(&factoryv1alpha1.Task{}).
		Complete(r)
}

