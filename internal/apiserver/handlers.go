package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/pkg/events"
)

// EventSubscriber abstracts event subscription for SSE streaming.
type EventSubscriber interface {
	SubscribeSession(ctx context.Context, namespace, sessionID string, handler func(events.Event)) (Subscription, error)
}

// Subscription represents a NATS subscription that can be unsubscribed.
type Subscription interface {
	Unsubscribe() error
}

// PermissionPublisher publishes permission decisions to NATS reply subjects.
type PermissionPublisher interface {
	Publish(subject string, data []byte) error
}

// BridgeClientFactory creates bridge HTTP clients for a given base URL.
type BridgeClientFactory func(baseURL string) BridgeClient

// BridgeClient defines the bridge sidecar HTTP API for the API server.
type BridgeClient interface {
	SendMessage(ctx context.Context, sessionID string, msg string) error
	CancelSession(ctx context.Context, sessionID string) error
}

// Handlers holds dependencies for HTTP handlers.
type Handlers struct {
	client              client.Client
	subscriber          EventSubscriber
	permissionPublisher PermissionPublisher
	bridgeClientFactory BridgeClientFactory
	logger              *slog.Logger
	namespace           string
}

// NewHandlers creates a new Handlers instance.
func NewHandlers(c client.Client, subscriber EventSubscriber, logger *slog.Logger, namespace string) *Handlers {
	return &Handlers{
		client:     c,
		subscriber: subscriber,
		logger:     logger,
		namespace:  namespace,
	}
}

// SetBridgeClientFactory sets the factory for creating bridge clients.
func (h *Handlers) SetBridgeClientFactory(f BridgeClientFactory) {
	h.bridgeClientFactory = f
}

// SetPermissionPublisher sets the NATS publisher for permission decisions.
func (h *Handlers) SetPermissionPublisher(pp PermissionPublisher) {
	h.permissionPublisher = pp
}

func (h *Handlers) resolveNamespace(ns string) string {
	if ns != "" {
		return ns
	}
	return h.namespace
}

// CreateWorkflow handles POST /v1/workflows.
func (h *Handlers) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req CreateWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.Tasks) == 0 {
		writeError(w, http.StatusBadRequest, "at least one task is required")
		return
	}

	ns := h.resolveNamespace(req.Namespace)
	name := req.Name
	if name == "" {
		name = "workflow-" + uuid.New().String()[:8]
	}

	wf := &factoryv1alpha1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: factoryv1alpha1.WorkflowSpec{
			Defaults: req.Defaults,
			Context:  req.Context,
			Tasks:    req.Tasks,
		},
	}

	if err := h.client.Create(r.Context(), wf); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("creating workflow: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, workflowFromCR(wf))
}

// GetWorkflow handles GET /v1/workflows/{id}.
func (h *Handlers) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	if name == "" {
		writeError(w, http.StatusBadRequest, "workflow id is required")
		return
	}

	var wf factoryv1alpha1.Workflow
	key := types.NamespacedName{Name: name, Namespace: h.namespace}
	if err := h.client.Get(r.Context(), key, &wf); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting workflow: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, workflowFromCR(&wf))
}

// DeleteWorkflow handles DELETE /v1/workflows/{id}.
func (h *Handlers) DeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	if name == "" {
		writeError(w, http.StatusBadRequest, "workflow id is required")
		return
	}

	wf := &factoryv1alpha1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.namespace,
		},
	}

	if err := h.client.Delete(r.Context(), wf); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("deleting workflow: %v", err))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListWorkflowTasks handles GET /v1/workflows/{id}/tasks.
func (h *Handlers) ListWorkflowTasks(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	if name == "" {
		writeError(w, http.StatusBadRequest, "workflow id is required")
		return
	}

	var taskList factoryv1alpha1.TaskList
	if err := h.client.List(r.Context(), &taskList,
		client.InNamespace(h.namespace),
		client.MatchingLabels{"factory.example.com/workflow": name},
	); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("listing tasks: %v", err))
		return
	}

	tasks := make([]TaskResponse, 0, len(taskList.Items))
	for i := range taskList.Items {
		tasks = append(tasks, taskFromCR(&taskList.Items[i]))
	}

	writeJSON(w, http.StatusOK, tasks)
}

// CreateTask handles POST /v1/tasks.
func (h *Handlers) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.PoolRef == "" {
		writeError(w, http.StatusBadRequest, "poolRef is required")
		return
	}
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	ns := h.resolveNamespace(req.Namespace)
	name := req.Name
	if name == "" {
		name = "task-" + uuid.New().String()[:8]
	}

	task := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: factoryv1alpha1.TaskSpec{
			PoolRef: factoryv1alpha1.LocalObjectReference{Name: req.PoolRef},
			Prompt:  req.Prompt,
			Inputs:  req.Inputs,
			Outputs: req.Outputs,
			Retries: req.Retries,
		},
	}

	if req.Timeout != "" {
		d, err := time.ParseDuration(req.Timeout)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid timeout duration: "+err.Error())
			return
		}
		task.Spec.Timeout = &metav1.Duration{Duration: d}
	}

	if err := h.client.Create(r.Context(), task); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("creating task: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, taskFromCR(task))
}

// GetTask handles GET /v1/tasks/{id}.
func (h *Handlers) GetTask(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	if name == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	var task factoryv1alpha1.Task
	key := types.NamespacedName{Name: name, Namespace: h.namespace}
	if err := h.client.Get(r.Context(), key, &task); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting task: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, taskFromCR(&task))
}

// StreamTaskEvents handles GET /v1/tasks/{id}/events (SSE).
func (h *Handlers) StreamTaskEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	if name == "" {
		writeError(w, http.StatusBadRequest, "task id is required")
		return
	}

	// Look up the task to find its session.
	var task factoryv1alpha1.Task
	key := types.NamespacedName{Name: name, Namespace: h.namespace}
	if err := h.client.Get(r.Context(), key, &task); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting task: %v", err))
		return
	}

	if task.Status.SessionRef == nil {
		writeError(w, http.StatusConflict, "task has no active session")
		return
	}

	// Look up the session to get the event stream subject.
	var session factoryv1alpha1.Session
	sessionKey := types.NamespacedName{Name: task.Status.SessionRef.Name, Namespace: h.namespace}
	if err := h.client.Get(r.Context(), sessionKey, &session); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting session: %v", err))
		return
	}

	if h.subscriber == nil {
		writeError(w, http.StatusServiceUnavailable, "event streaming not available")
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sub, err := h.subscriber.SubscribeSession(ctx, session.Namespace, session.Name, func(event events.Event) {
		data, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, data)
		flusher.Flush()
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("subscribing to events: %v", err))
		return
	}
	defer func() {
		_ = sub.Unsubscribe()
	}()

	// Block until client disconnects.
	<-ctx.Done()
}

// ApprovePermission handles POST /v1/sessions/{id}/permissions/{permissionId}.
func (h *Handlers) ApprovePermission(w http.ResponseWriter, r *http.Request) {
	sessionName := r.PathValue("id")
	permissionID := r.PathValue("permissionId")

	if sessionName == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}
	if permissionID == "" {
		writeError(w, http.StatusBadRequest, "permission id is required")
		return
	}

	var req PermissionDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Decision != "allow" && req.Decision != "deny" {
		writeError(w, http.StatusBadRequest, "decision must be 'allow' or 'deny'")
		return
	}

	// Look up the session to verify it exists and is waiting for approval.
	var session factoryv1alpha1.Session
	key := types.NamespacedName{Name: sessionName, Namespace: h.namespace}
	if err := h.client.Get(r.Context(), key, &session); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting session: %v", err))
		return
	}

	if session.Status.Phase != factoryv1alpha1.SessionPhaseWaitingForApproval {
		writeError(w, http.StatusConflict, fmt.Sprintf("session is not waiting for approval (phase: %s)", session.Status.Phase))
		return
	}

	if session.Status.PendingApproval == nil || session.Status.PendingApproval.ID != permissionID {
		writeError(w, http.StatusNotFound, "permission request not found")
		return
	}

	if h.permissionPublisher == nil {
		writeError(w, http.StatusServiceUnavailable, "permission publishing not available")
		return
	}

	// Publish the decision to the NATS reply subject.
	replySubject := fmt.Sprintf("permissions.%s", permissionID)
	decision := PermissionDecisionResponse{
		PermissionID: permissionID,
		Decision:     req.Decision,
		Remember:     req.Remember,
		RespondedBy:  "api",
	}
	data, err := json.Marshal(decision)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding decision")
		return
	}

	if err := h.permissionPublisher.Publish(replySubject, data); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("publishing decision: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, decision)
}

// CreateSession handles POST /v1/sessions — creates an interactive session.
func (h *Handlers) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.PoolRef == "" {
		writeError(w, http.StatusBadRequest, "poolRef is required")
		return
	}

	ns := h.namespace

	// Look up the pool to get the agentConfig.
	var pool factoryv1alpha1.Pool
	if err := h.client.Get(r.Context(), types.NamespacedName{Name: req.PoolRef, Namespace: ns}, &pool); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "pool not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting pool: %v", err))
		return
	}

	agentType := req.AgentType
	if agentType == "" {
		// Look up the AgentConfig referenced by the pool.
		var agentConfig factoryv1alpha1.AgentConfig
		if err := h.client.Get(r.Context(), types.NamespacedName{
			Name: pool.Spec.AgentConfigRef.Name, Namespace: ns,
		}, &agentConfig); err == nil {
			agentType = agentConfig.Spec.AgentType
		}
	}

	// Find a ready sandbox in the pool.
	var sandboxList factoryv1alpha1.SandboxList
	if err := h.client.List(r.Context(), &sandboxList, client.InNamespace(ns)); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("listing sandboxes: %v", err))
		return
	}
	var sandboxName string
	for _, sb := range sandboxList.Items {
		if sb.Spec.PoolRef.Name == req.PoolRef && sb.Status.Phase == factoryv1alpha1.SandboxPhaseReady {
			sandboxName = sb.Name
			break
		}
	}
	if sandboxName == "" {
		writeError(w, http.StatusServiceUnavailable, "no ready sandboxes in pool")
		return
	}

	// Create the Session CR.
	name := "session-" + uuid.New().String()[:8]
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: sandboxName},
			Mode:       factoryv1alpha1.SessionModeInteractive,
			AgentType:  agentType,
			Prompt:     req.Prompt,
		},
	}

	if req.Timeout != "" {
		d, err := time.ParseDuration(req.Timeout)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid timeout: "+err.Error())
			return
		}
		session.Spec.Timeout = &metav1.Duration{Duration: d}
	}

	if err := h.client.Create(r.Context(), session); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("creating session: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, SessionResponse{
		ID:         string(session.UID),
		Name:       session.Name,
		Namespace:  session.Namespace,
		Mode:       string(session.Spec.Mode),
		Phase:      string(session.Status.Phase),
		AgentType:  session.Spec.AgentType,
		SandboxRef: sandboxName,
		CreatedAt:  session.CreationTimestamp.Format(time.RFC3339),
	})
}

// SendMessage handles POST /v1/sessions/{id}/messages — sends a message to an interactive session.
func (h *Handlers) SendMessage(w http.ResponseWriter, r *http.Request) {
	sessionName := r.PathValue("id")
	if sessionName == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}

	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	// Look up the session.
	var session factoryv1alpha1.Session
	key := types.NamespacedName{Name: sessionName, Namespace: h.namespace}
	if err := h.client.Get(r.Context(), key, &session); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting session: %v", err))
		return
	}

	if session.Status.Phase != factoryv1alpha1.SessionPhaseActive {
		writeError(w, http.StatusConflict, fmt.Sprintf("session is not active (phase: %s)", session.Status.Phase))
		return
	}

	// Get the bridge endpoint for this session's sandbox.
	bridgeURL, err := h.getBridgeEndpoint(r.Context(), &session)
	if err != nil || bridgeURL == "" {
		writeError(w, http.StatusServiceUnavailable, "bridge not available")
		return
	}

	// Forward the message to the bridge.
	bridgeClient := h.bridgeClientFactory(bridgeURL)
	serverID := session.Status.EventStreamSubject
	if len(serverID) > len("sessions.") {
		serverID = serverID[len("sessions."):]
	}

	if err := bridgeClient.SendMessage(r.Context(), serverID, req.Message); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("sending message: %v", err))
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// DeleteSession handles DELETE /v1/sessions/{id} — closes an interactive session.
func (h *Handlers) DeleteSession(w http.ResponseWriter, r *http.Request) {
	sessionName := r.PathValue("id")
	if sessionName == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}

	var session factoryv1alpha1.Session
	key := types.NamespacedName{Name: sessionName, Namespace: h.namespace}
	if err := h.client.Get(r.Context(), key, &session); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting session: %v", err))
		return
	}

	// Cancel the session on the bridge if it's still active.
	if session.Status.Phase == factoryv1alpha1.SessionPhaseActive ||
		session.Status.Phase == factoryv1alpha1.SessionPhaseWaitingForApproval {
		bridgeURL, _ := h.getBridgeEndpoint(r.Context(), &session)
		if bridgeURL != "" && h.bridgeClientFactory != nil {
			bridgeClient := h.bridgeClientFactory(bridgeURL)
			serverID := session.Status.EventStreamSubject
			if len(serverID) > len("sessions.") {
				serverID = serverID[len("sessions."):]
			}
			_ = bridgeClient.CancelSession(r.Context(), serverID)
		}
	}

	// Update session to Completed.
	now := metav1.Now()
	session.Status.Phase = factoryv1alpha1.SessionPhaseCompleted
	session.Status.CompletedAt = &now
	if err := h.client.Status().Update(r.Context(), &session); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("updating session: %v", err))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// getBridgeEndpoint resolves the bridge sidecar URL for a session's sandbox.
func (h *Handlers) getBridgeEndpoint(ctx context.Context, session *factoryv1alpha1.Session) (string, error) {
	var sandbox factoryv1alpha1.Sandbox
	if err := h.client.Get(ctx, types.NamespacedName{
		Name: session.Spec.SandboxRef.Name, Namespace: session.Namespace,
	}, &sandbox); err != nil {
		return "", err
	}
	if sandbox.Status.PodName == "" {
		return "", nil
	}
	var pod corev1.Pod
	if err := h.client.Get(ctx, types.NamespacedName{
		Name: sandbox.Status.PodName, Namespace: session.Namespace,
	}, &pod); err != nil {
		return "", err
	}
	if pod.Status.PodIP == "" {
		return "", nil
	}
	return fmt.Sprintf("http://%s:8080", pod.Status.PodIP), nil
}

// ListPools handles GET /v1/pools.
func (h *Handlers) ListPools(w http.ResponseWriter, r *http.Request) {
	var poolList factoryv1alpha1.PoolList
	if err := h.client.List(r.Context(), &poolList, client.InNamespace(h.namespace)); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("listing pools: %v", err))
		return
	}

	pools := make([]PoolResponse, 0, len(poolList.Items))
	for i := range poolList.Items {
		pools = append(pools, poolFromCR(&poolList.Items[i]))
	}

	writeJSON(w, http.StatusOK, pools)
}

// GetPool handles GET /v1/pools/{id}.
func (h *Handlers) GetPool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	if name == "" {
		writeError(w, http.StatusBadRequest, "pool id is required")
		return
	}

	var pool factoryv1alpha1.Pool
	key := types.NamespacedName{Name: name, Namespace: h.namespace}
	if err := h.client.Get(r.Context(), key, &pool); err != nil {
		if client.IgnoreNotFound(err) == nil {
			writeError(w, http.StatusNotFound, "pool not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("getting pool: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, poolFromCR(&pool))
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, `{"error":"encoding response"}`, http.StatusInternalServerError)
	}
}

// writeError writes an error response.
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error:   http.StatusText(code),
		Code:    code,
		Message: message,
	})
}
