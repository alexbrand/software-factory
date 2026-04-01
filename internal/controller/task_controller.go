package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

const defaultRequeueDelay = 10 * time.Second

// TaskReconciler reconciles a Task object.
type TaskReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=factory.example.com,resources=tasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=factory.example.com,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=sandboxes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=sandboxes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=sessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=factory.example.com,resources=agentconfigs,verbs=get;list;watch

func (r *TaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var task factoryv1alpha1.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching task: %w", err)
	}

	switch task.Status.Phase {
	case "", factoryv1alpha1.TaskPhasePending:
		return r.reconcilePending(ctx, &task)
	case factoryv1alpha1.TaskPhaseRunning:
		return r.reconcileRunning(ctx, &task)
	case factoryv1alpha1.TaskPhaseSucceeded, factoryv1alpha1.TaskPhaseFailed, factoryv1alpha1.TaskPhaseCancelled:
		return ctrl.Result{}, nil
	default:
		logger.Info("unknown task phase", "phase", task.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *TaskReconciler) reconcilePending(ctx context.Context, task *factoryv1alpha1.Task) (ctrl.Result, error) {
	// If we already have a sandbox assigned, create the session.
	if task.Status.SandboxRef != nil {
		return r.ensureSession(ctx, task)
	}

	// Find an available sandbox in the referenced pool.
	sandbox, err := r.findReadySandbox(ctx, task)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("finding ready sandbox: %w", err)
	}
	if sandbox == nil {
		// No sandbox available, requeue.
		return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
	}

	// Claim the sandbox.
	sandbox.Status.Phase = factoryv1alpha1.SandboxPhaseAssigned
	sandbox.Status.AssignedTask = task.Name
	if err := r.Status().Update(ctx, sandbox); err != nil {
		return ctrl.Result{}, fmt.Errorf("claiming sandbox: %w", err)
	}

	// Update task with sandbox ref and transition to Running.
	now := metav1.Now()
	task.Status.SandboxRef = &factoryv1alpha1.LocalObjectReference{Name: sandbox.Name}
	task.Status.Phase = factoryv1alpha1.TaskPhaseRunning
	task.Status.StartedAt = &now
	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating task status to Running: %w", err)
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *TaskReconciler) reconcileRunning(ctx context.Context, task *factoryv1alpha1.Task) (ctrl.Result, error) {
	// Check timeout.
	if task.Spec.Timeout != nil && task.Status.StartedAt != nil {
		deadline := task.Status.StartedAt.Add(task.Spec.Timeout.Duration)
		if time.Now().After(deadline) {
			return r.handleTimeout(ctx, task)
		}
	}

	// Ensure a session exists.
	if task.Status.SessionRef == nil {
		return r.ensureSession(ctx, task)
	}

	// Check the session status.
	var session factoryv1alpha1.Session
	sessionName := task.Status.SessionRef.Name
	if err := r.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: sessionName}, &session); err != nil {
		if errors.IsNotFound(err) {
			// Session was deleted, treat as failure.
			return r.handleSessionResult(ctx, task, factoryv1alpha1.SessionPhaseFailed)
		}
		return ctrl.Result{}, fmt.Errorf("fetching session: %w", err)
	}

	switch session.Status.Phase {
	case factoryv1alpha1.SessionPhaseCompleted:
		return r.handleSessionResult(ctx, task, factoryv1alpha1.SessionPhaseCompleted)
	case factoryv1alpha1.SessionPhaseFailed:
		return r.handleSessionResult(ctx, task, factoryv1alpha1.SessionPhaseFailed)
	case factoryv1alpha1.SessionPhaseCancelled:
		return r.handleSessionResult(ctx, task, factoryv1alpha1.SessionPhaseCancelled)
	default:
		// Session still in progress, check again later.
		requeueAfter := defaultRequeueDelay
		if task.Spec.Timeout != nil && task.Status.StartedAt != nil {
			remaining := time.Until(task.Status.StartedAt.Add(task.Spec.Timeout.Duration))
			if remaining < requeueAfter {
				requeueAfter = remaining
			}
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
}

func (r *TaskReconciler) findReadySandbox(ctx context.Context, task *factoryv1alpha1.Task) (*factoryv1alpha1.Sandbox, error) {
	var sandboxList factoryv1alpha1.SandboxList
	if err := r.List(ctx, &sandboxList,
		client.InNamespace(task.Namespace),
		client.MatchingFields{"spec.poolRef.name": task.Spec.PoolRef.Name},
	); err != nil {
		return nil, fmt.Errorf("listing sandboxes: %w", err)
	}

	for i := range sandboxList.Items {
		sb := &sandboxList.Items[i]
		if sb.Status.Phase == factoryv1alpha1.SandboxPhaseReady {
			return sb, nil
		}
	}

	return nil, nil
}

func (r *TaskReconciler) ensureSession(ctx context.Context, task *factoryv1alpha1.Task) (ctrl.Result, error) {
	if task.Status.SandboxRef == nil {
		return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
	}

	// Resolve agent type from the sandbox's AgentConfig.
	agentType, err := r.resolveAgentType(ctx, task.Namespace, task.Status.SandboxRef.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolving agent type: %w", err)
	}

	sessionName := fmt.Sprintf("%s-session-%d", task.Name, task.Status.Attempts)
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sessionName,
			Namespace: task.Namespace,
			Labels: map[string]string{
				"factory.example.com/task": task.Name,
			},
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: task.Status.SandboxRef.Name},
			TaskRef:    &factoryv1alpha1.LocalObjectReference{Name: task.Name},
			AgentType:  agentType,
			Prompt:     task.Spec.Prompt,
		},
	}

	if err := controllerutil.SetControllerReference(task, session, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting session owner reference: %w", err)
	}

	if err := r.Create(ctx, session); err != nil {
		if errors.IsAlreadyExists(err) {
			// Session already exists, just update ref.
		} else {
			return ctrl.Result{}, fmt.Errorf("creating session: %w", err)
		}
	}

	// Update task status.
	task.Status.SessionRef = &factoryv1alpha1.LocalObjectReference{Name: sessionName}
	task.Status.Attempts++
	if task.Status.Phase == factoryv1alpha1.TaskPhasePending {
		now := metav1.Now()
		task.Status.Phase = factoryv1alpha1.TaskPhaseRunning
		task.Status.StartedAt = &now
	}
	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating task session ref: %w", err)
	}

	return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
}

func (r *TaskReconciler) handleSessionResult(ctx context.Context, task *factoryv1alpha1.Task, sessionPhase factoryv1alpha1.SessionPhase) (ctrl.Result, error) {
	switch sessionPhase {
	case factoryv1alpha1.SessionPhaseCompleted:
		// Task succeeded.
		now := metav1.Now()
		task.Status.Phase = factoryv1alpha1.TaskPhaseSucceeded
		task.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating task to Succeeded: %w", err)
		}
		// Release sandbox back to pool.
		if err := r.releaseSandbox(ctx, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("releasing sandbox: %w", err)
		}
		return ctrl.Result{}, nil

	case factoryv1alpha1.SessionPhaseFailed:
		// Check if we can retry.
		maxRetries := int32(0)
		if task.Spec.Retries != nil {
			maxRetries = *task.Spec.Retries
		}
		if task.Status.Attempts <= maxRetries {
			// Retry: clear session ref and create new session.
			task.Status.SessionRef = nil
			if err := r.Status().Update(ctx, task); err != nil {
				return ctrl.Result{}, fmt.Errorf("clearing session ref for retry: %w", err)
			}
			return ctrl.Result{Requeue: true}, nil
		}

		// Exceeded retries, fail the task.
		now := metav1.Now()
		task.Status.Phase = factoryv1alpha1.TaskPhaseFailed
		task.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating task to Failed: %w", err)
		}
		if err := r.releaseSandbox(ctx, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("releasing sandbox: %w", err)
		}
		return ctrl.Result{}, nil

	default:
		// Cancelled or other terminal state.
		now := metav1.Now()
		task.Status.Phase = factoryv1alpha1.TaskPhaseCancelled
		task.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating task to Cancelled: %w", err)
		}
		if err := r.releaseSandbox(ctx, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("releasing sandbox: %w", err)
		}
		return ctrl.Result{}, nil
	}
}

func (r *TaskReconciler) handleTimeout(ctx context.Context, task *factoryv1alpha1.Task) (ctrl.Result, error) {
	// Cancel the session if one exists.
	if task.Status.SessionRef != nil {
		var session factoryv1alpha1.Session
		err := r.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Status.SessionRef.Name}, &session)
		if err == nil {
			now := metav1.Now()
			session.Status.Phase = factoryv1alpha1.SessionPhaseCancelled
			session.Status.CompletedAt = &now
			if err := r.Status().Update(ctx, &session); err != nil {
				return ctrl.Result{}, fmt.Errorf("cancelling session on timeout: %w", err)
			}
		} else if !errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("fetching session for timeout: %w", err)
		}
	}

	now := metav1.Now()
	task.Status.Phase = factoryv1alpha1.TaskPhaseFailed
	task.Status.CompletedAt = &now
	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating task to Failed on timeout: %w", err)
	}

	if err := r.releaseSandbox(ctx, task); err != nil {
		return ctrl.Result{}, fmt.Errorf("releasing sandbox on timeout: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *TaskReconciler) releaseSandbox(ctx context.Context, task *factoryv1alpha1.Task) error {
	if task.Status.SandboxRef == nil {
		return nil
	}

	var sandbox factoryv1alpha1.Sandbox
	if err := r.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Status.SandboxRef.Name}, &sandbox); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetching sandbox for release: %w", err)
	}

	sandbox.Status.Phase = factoryv1alpha1.SandboxPhaseIdle
	sandbox.Status.AssignedTask = ""
	sandbox.Status.CurrentSession = ""
	now := metav1.Now()
	sandbox.Status.LastActivityAt = &now
	if err := r.Status().Update(ctx, &sandbox); err != nil {
		return fmt.Errorf("releasing sandbox: %w", err)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index sandboxes by pool ref for efficient lookups.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &factoryv1alpha1.Sandbox{}, "spec.poolRef.name", func(obj client.Object) []string {
		sandbox, ok := obj.(*factoryv1alpha1.Sandbox)
		if !ok {
			return nil
		}
		return []string{sandbox.Spec.PoolRef.Name}
	}); err != nil {
		// Index may already be registered by pool controller.
		_ = err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1alpha1.Task{}).
		Owns(&factoryv1alpha1.Session{}).
		Complete(r)
}

// resolveAgentType looks up the agent type from the sandbox's AgentConfig.
func (r *TaskReconciler) resolveAgentType(ctx context.Context, namespace, sandboxName string) (string, error) {
	sandbox := &factoryv1alpha1.Sandbox{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: sandboxName}, sandbox); err != nil {
		return "", fmt.Errorf("getting sandbox: %w", err)
	}

	agentConfig := &factoryv1alpha1.AgentConfig{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: sandbox.Spec.AgentConfigRef.Name}, agentConfig); err != nil {
		return "", fmt.Errorf("getting agent config: %w", err)
	}

	return agentConfig.Spec.AgentType, nil
}
