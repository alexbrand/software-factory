package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/bridge"
)

const (
	sessionHealthCheckInterval = 15 * time.Second
	bridgePort                 = 8080
)

// BridgeClientFactory creates bridge clients for a given base URL.
// This allows injection of test clients.
type BridgeClientFactory func(baseURL string) BridgeClient

// BridgeClient defines the interface for communicating with the bridge sidecar.
type BridgeClient interface {
	StartSession(ctx context.Context, cfg bridge.SessionConfig) (string, error)
	SendMessage(ctx context.Context, sessionID string, msg string) error
	CancelSession(ctx context.Context, sessionID string) error
	GetHealth(ctx context.Context) (*bridge.HealthStatus, error)
}

// SessionReconciler reconciles a Session object.
type SessionReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	BridgeClientFactory BridgeClientFactory
}

// +kubebuilder:rbac:groups=factory.example.com,resources=sessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=factory.example.com,resources=sessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=factory.example.com,resources=sandboxes,verbs=get;list;watch
// +kubebuilder:rbac:groups=factory.example.com,resources=sandboxes/status,verbs=get
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var session factoryv1alpha1.Session
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching session: %w", err)
	}

	// Handle deletion: cancel the session on the bridge.
	if !session.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &session)
	}

	switch session.Status.Phase {
	case "", factoryv1alpha1.SessionPhasePending:
		return r.reconcilePending(ctx, &session)
	case factoryv1alpha1.SessionPhaseActive:
		return r.reconcileActive(ctx, &session)
	case factoryv1alpha1.SessionPhaseCompleted, factoryv1alpha1.SessionPhaseFailed, factoryv1alpha1.SessionPhaseCancelled:
		return ctrl.Result{}, nil
	default:
		logger.Info("unknown session phase", "phase", session.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *SessionReconciler) reconcilePending(ctx context.Context, session *factoryv1alpha1.Session) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Look up the sandbox to find the bridge endpoint.
	bridgeURL, err := r.getBridgeEndpoint(ctx, session)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting bridge endpoint: %w", err)
	}
	if bridgeURL == "" {
		logger.Info("sandbox pod not ready yet, requeueing")
		return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
	}

	// Create a bridge client and start the session.
	bridgeClient := r.newBridgeClient(bridgeURL)

	cfg := bridge.SessionConfig{
		AgentType:    session.Spec.AgentType,
		Prompt:       session.Spec.Prompt,
		ContextFiles: session.Spec.ContextFiles,
	}

	bridgeSessionID, err := bridgeClient.StartSession(ctx, cfg)
	if err != nil {
		logger.Error(err, "failed to start session on bridge")
		return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
	}

	// Update session status to Active.
	now := metav1.Now()
	session.Status.Phase = factoryv1alpha1.SessionPhaseActive
	session.Status.StartedAt = &now
	session.Status.EventStreamSubject = fmt.Sprintf("sessions.%s", bridgeSessionID)
	if err := r.Status().Update(ctx, session); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating session status to Active: %w", err)
	}

	return ctrl.Result{RequeueAfter: sessionHealthCheckInterval}, nil
}

func (r *SessionReconciler) reconcileActive(ctx context.Context, session *factoryv1alpha1.Session) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	bridgeURL, err := r.getBridgeEndpoint(ctx, session)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting bridge endpoint: %w", err)
	}
	if bridgeURL == "" {
		// Pod gone while session active — mark as failed.
		now := metav1.Now()
		session.Status.Phase = factoryv1alpha1.SessionPhaseFailed
		session.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, session); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating session to Failed (pod gone): %w", err)
		}
		return ctrl.Result{}, nil
	}

	bridgeClient := r.newBridgeClient(bridgeURL)

	health, err := bridgeClient.GetHealth(ctx)
	if err != nil {
		logger.Error(err, "bridge health check failed")
		// Don't fail immediately — bridge might be temporarily unreachable.
		return ctrl.Result{RequeueAfter: sessionHealthCheckInterval}, nil
	}

	if health.ActiveSessions == 0 {
		// Session has completed on the bridge side.
		now := metav1.Now()
		session.Status.Phase = factoryv1alpha1.SessionPhaseCompleted
		session.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, session); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating session to Completed: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Session still running, check again later.
	return ctrl.Result{RequeueAfter: sessionHealthCheckInterval}, nil
}

func (r *SessionReconciler) handleDeletion(ctx context.Context, session *factoryv1alpha1.Session) (ctrl.Result, error) {
	// If session was active, try to cancel it on the bridge.
	if session.Status.Phase == factoryv1alpha1.SessionPhaseActive && session.Status.EventStreamSubject != "" {
		bridgeURL, err := r.getBridgeEndpoint(ctx, session)
		if err == nil && bridgeURL != "" {
			bridgeClient := r.newBridgeClient(bridgeURL)
			// Extract session ID from event stream subject ("sessions.<id>").
			sessionID := session.Status.EventStreamSubject[len("sessions."):]
			_ = bridgeClient.CancelSession(ctx, sessionID)
		}
	}
	return ctrl.Result{}, nil
}

func (r *SessionReconciler) getBridgeEndpoint(ctx context.Context, session *factoryv1alpha1.Session) (string, error) {
	// Get the sandbox to find the pod name.
	var sandbox factoryv1alpha1.Sandbox
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: session.Namespace,
		Name:      session.Spec.SandboxRef.Name,
	}, &sandbox); err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("fetching sandbox: %w", err)
	}

	if sandbox.Status.PodName == "" {
		return "", nil
	}

	// Get the pod to find its IP.
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: session.Namespace,
		Name:      sandbox.Status.PodName,
	}, &pod); err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("fetching pod: %w", err)
	}

	if pod.Status.PodIP == "" {
		return "", nil
	}

	return fmt.Sprintf("http://%s:%d", pod.Status.PodIP, bridgePort), nil
}

func (r *SessionReconciler) newBridgeClient(baseURL string) BridgeClient {
	if r.BridgeClientFactory != nil {
		return r.BridgeClientFactory(baseURL)
	}
	return bridge.NewClient(baseURL)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1alpha1.Session{}).
		Complete(r)
}
