package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/bridge"
	"github.com/alexbrand/software-factory/pkg/events"
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

// EventPublisher defines the interface for publishing events.
// This allows injection of mock publishers for testing.
type EventPublisher interface {
	Publish(ctx context.Context, namespace string, event events.Event) error
}

// EventSubscriber defines the interface for subscribing to events.
type EventSubscriber interface {
	SubscribeSession(ctx context.Context, namespace, sessionID string, handler func(events.Event)) (EventSubscription, error)
}

// EventSubscription can be unsubscribed.
type EventSubscription interface {
	Unsubscribe() error
}

// SessionReconciler reconciles a Session object.
type SessionReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	BridgeClientFactory BridgeClientFactory
	EventPublisher      EventPublisher
	EventSubscriber     EventSubscriber
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
	case factoryv1alpha1.SessionPhaseWaitingForApproval:
		// Permission events are handled by the NATS watcher goroutine.
		// Check for timeout as a fallback.
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

	// Look up the AgentConfig to get the permission mode.
	permissionMode := r.getPermissionMode(ctx, session)

	// Create a bridge client and start the session.
	bridgeClient := r.newBridgeClient(bridgeURL)

	cfg := bridge.SessionConfig{
		AgentType:      session.Spec.AgentType,
		Prompt:         session.Spec.Prompt,
		ContextFiles:   session.Spec.ContextFiles,
		PermissionMode: permissionMode,
		Mode:           string(session.Spec.Mode),
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

	r.publishEvent(ctx, session, events.EventSessionStarted, events.SessionStartedData{
		AgentType: session.Spec.AgentType,
		Prompt:    session.Spec.Prompt,
		Namespace: session.Namespace,
	})

	// Start a NATS watcher for session lifecycle events.
	if r.EventSubscriber != nil {
		go r.watchSessionEvents(session.Namespace, session.Name, bridgeSessionID)
	}

	return ctrl.Result{RequeueAfter: sessionHealthCheckInterval}, nil
}

// getPermissionMode looks up the AgentConfig for the session's agent type
// and returns the configured permission mode.
func (r *SessionReconciler) getPermissionMode(ctx context.Context, session *factoryv1alpha1.Session) string {
	var agentConfigs factoryv1alpha1.AgentConfigList
	if err := r.List(ctx, &agentConfigs, client.InNamespace(session.Namespace)); err != nil {
		return ""
	}
	for _, ac := range agentConfigs.Items {
		if ac.Spec.AgentType == session.Spec.AgentType {
			return string(ac.Spec.PermissionMode)
		}
	}
	return ""
}

// watchSessionEvents subscribes to NATS lifecycle events for a session and updates
// the Session CR on permission requests, completion, and failure.
func (r *SessionReconciler) watchSessionEvents(namespace, sessionName, bridgeSessionID string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := r.EventSubscriber.SubscribeSession(ctx, namespace, bridgeSessionID, func(ev events.Event) {
		switch ev.Type {
		case events.EventPermissionRequested:
			r.handlePermissionRequested(namespace, sessionName, ev)
		case events.EventPermissionResponded:
			r.handlePermissionResponded(namespace, sessionName)
		case events.EventSessionCompleted:
			r.handleSessionCompleted(namespace, sessionName, ev)
			cancel()
		case events.EventSessionFailed:
			r.handleSessionFailed(namespace, sessionName, ev)
			cancel()
		}
	})
	if err != nil {
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	<-ctx.Done()
}

// handlePermissionRequested updates the Session CR to WaitingForApproval.
func (r *SessionReconciler) handlePermissionRequested(namespace, sessionName string, ev events.Event) {
	var data events.PermissionRequestData
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}

	ctx := context.Background()
	var session factoryv1alpha1.Session
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: sessionName}, &session); err != nil {
		return
	}

	now := metav1.Now()
	session.Status.Phase = factoryv1alpha1.SessionPhaseWaitingForApproval
	session.Status.PendingApproval = &factoryv1alpha1.PendingApproval{
		ID:          data.PermissionID,
		ToolName:    data.ToolName,
		Title:       data.Title,
		RequestedAt: now,
	}
	_ = r.Status().Update(ctx, &session)
}

// handlePermissionResponded clears the pending approval and sets phase back to Active.
func (r *SessionReconciler) handlePermissionResponded(namespace, sessionName string) {
	ctx := context.Background()
	var session factoryv1alpha1.Session
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: sessionName}, &session); err != nil {
		return
	}

	session.Status.Phase = factoryv1alpha1.SessionPhaseActive
	session.Status.PendingApproval = nil
	_ = r.Status().Update(ctx, &session)
}

// handleSessionCompleted updates the Session CR to Completed with metadata.
func (r *SessionReconciler) handleSessionCompleted(namespace, sessionName string, ev events.Event) {
	var data events.SessionCompletedData
	_ = json.Unmarshal(ev.Data, &data)

	ctx := context.Background()
	var session factoryv1alpha1.Session
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: sessionName}, &session); err != nil {
		return
	}

	// Skip if already in a terminal phase (avoid overwriting).
	if session.Status.Phase == factoryv1alpha1.SessionPhaseCompleted ||
		session.Status.Phase == factoryv1alpha1.SessionPhaseFailed ||
		session.Status.Phase == factoryv1alpha1.SessionPhaseCancelled {
		return
	}

	now := metav1.Now()
	session.Status.Phase = factoryv1alpha1.SessionPhaseCompleted
	session.Status.CompletedAt = &now
	if data.InputTokens > 0 || data.OutputTokens > 0 {
		session.Status.TokenUsage = &factoryv1alpha1.TokenUsage{
			Input:  data.InputTokens,
			Output: data.OutputTokens,
		}
		if data.Cost != "" {
			session.Status.TokenUsage.Cost = &data.Cost
		}
	}
	_ = r.Status().Update(ctx, &session)
}

// handleSessionFailed updates the Session CR to Failed with failure reason.
func (r *SessionReconciler) handleSessionFailed(namespace, sessionName string, ev events.Event) {
	var data events.SessionFailedData
	_ = json.Unmarshal(ev.Data, &data)

	ctx := context.Background()
	var session factoryv1alpha1.Session
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: sessionName}, &session); err != nil {
		return
	}

	// Skip if already in a terminal phase.
	if session.Status.Phase == factoryv1alpha1.SessionPhaseCompleted ||
		session.Status.Phase == factoryv1alpha1.SessionPhaseFailed ||
		session.Status.Phase == factoryv1alpha1.SessionPhaseCancelled {
		return
	}

	now := metav1.Now()
	session.Status.Phase = factoryv1alpha1.SessionPhaseFailed
	session.Status.CompletedAt = &now
	session.Status.FailureReason = factoryv1alpha1.FailureReasonAgentError
	_ = r.Status().Update(ctx, &session)
}

const defaultSessionTimeout = 1 * time.Hour

func (r *SessionReconciler) reconcileActive(ctx context.Context, session *factoryv1alpha1.Session) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check for timeout (fallback — primary detection is via NATS events).
	if r.isSessionTimedOut(session) {
		now := metav1.Now()
		session.Status.Phase = factoryv1alpha1.SessionPhaseFailed
		session.Status.CompletedAt = &now
		session.Status.FailureReason = factoryv1alpha1.FailureReasonTimeout
		if err := r.Status().Update(ctx, session); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating session to Failed (timeout): %w", err)
		}
		r.publishEvent(ctx, session, events.EventSessionFailed, events.SessionFailedData{
			Reason: "session timed out",
		})
		return ctrl.Result{}, nil
	}

	// Check if the bridge is still reachable.
	bridgeURL, err := r.getBridgeEndpoint(ctx, session)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting bridge endpoint: %w", err)
	}
	if bridgeURL == "" {
		// Pod gone while session active — bridge lost.
		now := metav1.Now()
		session.Status.Phase = factoryv1alpha1.SessionPhaseFailed
		session.Status.CompletedAt = &now
		session.Status.FailureReason = factoryv1alpha1.FailureReasonBridgeLost
		if err := r.Status().Update(ctx, session); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating session to Failed (bridge lost): %w", err)
		}
		r.publishEvent(ctx, session, events.EventSessionFailed, events.SessionFailedData{
			Reason: "sandbox pod disappeared",
		})
		return ctrl.Result{}, nil
	}

	// Health check — verify the bridge is responding.
	bridgeClient := r.newBridgeClient(bridgeURL)
	_, err = bridgeClient.GetHealth(ctx)
	if err != nil {
		logger.Error(err, "bridge health check failed")
	}

	// Session completion is detected via NATS events (watchSessionEvents goroutine).
	// This reconciliation loop serves as a fallback for timeout and bridge-lost detection.
	return ctrl.Result{RequeueAfter: sessionHealthCheckInterval}, nil
}

// isSessionTimedOut checks if the session has exceeded its configured timeout.
func (r *SessionReconciler) isSessionTimedOut(session *factoryv1alpha1.Session) bool {
	if session.Status.StartedAt == nil {
		return false
	}

	timeout := defaultSessionTimeout
	if session.Spec.Timeout != nil {
		timeout = session.Spec.Timeout.Duration
	}

	return time.Since(session.Status.StartedAt.Time) > timeout
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

// publishEvent publishes a session lifecycle event if a publisher is configured.
func (r *SessionReconciler) publishEvent(ctx context.Context, session *factoryv1alpha1.Session, eventType events.EventType, data any) {
	if r.EventPublisher == nil {
		return
	}
	logger := log.FromContext(ctx)

	dataBytes, err := json.Marshal(data)
	if err != nil {
		logger.Error(err, "marshalling event data", "eventType", eventType)
		return
	}

	sessionID := session.Name
	if session.Status.EventStreamSubject != "" {
		sessionID = session.Status.EventStreamSubject[len("sessions."):]
	}

	event := events.Event{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Data:      dataBytes,
	}

	if err := r.EventPublisher.Publish(ctx, session.Namespace, event); err != nil {
		logger.Error(err, "publishing event", "eventType", eventType, "sessionID", sessionID)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&factoryv1alpha1.Session{}).
		Complete(r)
}
