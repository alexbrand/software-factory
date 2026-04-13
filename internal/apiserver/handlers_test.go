package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = factoryv1alpha1.AddToScheme(s)
	return s
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// mockPermissionPublisher captures published permission decisions.
type mockPermissionPublisher struct {
	published []struct {
		Subject string
		Data    []byte
	}
}

func (m *mockPermissionPublisher) Publish(subject string, data []byte) error {
	m.published = append(m.published, struct {
		Subject string
		Data    []byte
	}{Subject: subject, Data: data})
	return nil
}

// mockBridgeClient captures bridge calls.
type mockBridgeClient struct {
	messages []struct{ SessionID, Msg string }
	cancels  []string
}

func (m *mockBridgeClient) SendMessage(_ context.Context, sessionID, msg string) error {
	m.messages = append(m.messages, struct{ SessionID, Msg string }{sessionID, msg})
	return nil
}

func (m *mockBridgeClient) CancelSession(_ context.Context, sessionID string) error {
	m.cancels = append(m.cancels, sessionID)
	return nil
}

func testHandlers(objs ...client.Object) (*Handlers, *http.ServeMux) {
	s := testScheme()
	_ = corev1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).WithStatusSubresource(objs...).Build()
	h := NewHandlers(c, nil, testLogger(), "default")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/workflows", h.CreateWorkflow)
	mux.HandleFunc("GET /v1/workflows/{id}", h.GetWorkflow)
	mux.HandleFunc("DELETE /v1/workflows/{id}", h.DeleteWorkflow)
	mux.HandleFunc("GET /v1/workflows/{id}/tasks", h.ListWorkflowTasks)
	mux.HandleFunc("POST /v1/tasks", h.CreateTask)
	mux.HandleFunc("GET /v1/tasks/{id}", h.GetTask)
	mux.HandleFunc("GET /v1/tasks/{id}/events", h.StreamTaskEvents)
	mux.HandleFunc("POST /v1/sessions", h.CreateSession)
	mux.HandleFunc("POST /v1/sessions/{id}/messages", h.SendMessage)
	mux.HandleFunc("DELETE /v1/sessions/{id}", h.DeleteSession)
	mux.HandleFunc("POST /v1/sessions/{id}/permissions/{permissionId}", h.ApprovePermission)
	mux.HandleFunc("GET /v1/pools", h.ListPools)
	mux.HandleFunc("GET /v1/pools/{id}", h.GetPool)
	return h, mux
}

func TestCreateWorkflow(t *testing.T) {
	_, mux := testHandlers()

	body := CreateWorkflowRequest{
		Name: "test-wf",
		Tasks: []factoryv1alpha1.WorkflowTask{
			{
				Name: "task1",
				Spec: factoryv1alpha1.TaskInlineSpec{
					Prompt:  "do something",
					PoolRef: &factoryv1alpha1.LocalObjectReference{Name: "pool1"},
				},
			},
		},
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/workflows", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp WorkflowResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Name != "test-wf" {
		t.Errorf("expected name test-wf, got %s", resp.Name)
	}
	if resp.Namespace != "default" {
		t.Errorf("expected namespace default, got %s", resp.Namespace)
	}
}

func TestCreateWorkflow_NoTasks(t *testing.T) {
	_, mux := testHandlers()

	body := CreateWorkflowRequest{Name: "test-wf"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/workflows", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGetWorkflow(t *testing.T) {
	wf := &factoryv1alpha1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-wf",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.WorkflowSpec{
			Tasks: []factoryv1alpha1.WorkflowTask{
				{Name: "t1", Spec: factoryv1alpha1.TaskInlineSpec{Prompt: "do it"}},
			},
		},
	}
	_, mux := testHandlers(wf)

	req := httptest.NewRequest(http.MethodGet, "/v1/workflows/my-wf", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp WorkflowResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Name != "my-wf" {
		t.Errorf("expected name my-wf, got %s", resp.Name)
	}
}

func TestGetWorkflow_NotFound(t *testing.T) {
	_, mux := testHandlers()

	req := httptest.NewRequest(http.MethodGet, "/v1/workflows/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDeleteWorkflow(t *testing.T) {
	wf := &factoryv1alpha1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "del-wf",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.WorkflowSpec{
			Tasks: []factoryv1alpha1.WorkflowTask{
				{Name: "t1", Spec: factoryv1alpha1.TaskInlineSpec{Prompt: "do it"}},
			},
		},
	}
	_, mux := testHandlers(wf)

	req := httptest.NewRequest(http.MethodDelete, "/v1/workflows/del-wf", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteWorkflow_NotFound(t *testing.T) {
	_, mux := testHandlers()

	req := httptest.NewRequest(http.MethodDelete, "/v1/workflows/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCreateTask(t *testing.T) {
	_, mux := testHandlers()

	body := CreateTaskRequest{
		Name:    "test-task",
		PoolRef: "pool1",
		Prompt:  "analyze this code",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp TaskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Name != "test-task" {
		t.Errorf("expected name test-task, got %s", resp.Name)
	}
}

func TestCreateTask_MissingPoolRef(t *testing.T) {
	_, mux := testHandlers()

	body := CreateTaskRequest{Prompt: "something"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateTask_MissingPrompt(t *testing.T) {
	_, mux := testHandlers()

	body := CreateTaskRequest{PoolRef: "pool1"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGetTask(t *testing.T) {
	task := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-task",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.TaskSpec{
			PoolRef: factoryv1alpha1.LocalObjectReference{Name: "pool1"},
			Prompt:  "do it",
		},
	}
	_, mux := testHandlers(task)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/my-task", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp TaskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if resp.Name != "my-task" {
		t.Errorf("expected name my-task, got %s", resp.Name)
	}
}

func TestGetTask_NotFound(t *testing.T) {
	_, mux := testHandlers()

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestListPools(t *testing.T) {
	pool := &factoryv1alpha1.Pool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool1",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "agent1"},
			Replicas:       factoryv1alpha1.ReplicasConfig{Min: 2, Max: 10},
		},
	}
	_, mux := testHandlers(pool)

	req := httptest.NewRequest(http.MethodGet, "/v1/pools", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var pools []PoolResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pools); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(pools))
	}
	if pools[0].Name != "pool1" {
		t.Errorf("expected pool1, got %s", pools[0].Name)
	}
	if pools[0].MinReplicas != 2 {
		t.Errorf("expected min 2, got %d", pools[0].MinReplicas)
	}
}

func TestGetPool(t *testing.T) {
	pool := &factoryv1alpha1.Pool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pool1",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "agent1"},
			Replicas:       factoryv1alpha1.ReplicasConfig{Min: 1, Max: 5},
		},
	}
	_, mux := testHandlers(pool)

	req := httptest.NewRequest(http.MethodGet, "/v1/pools/pool1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp PoolResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if resp.Name != "pool1" {
		t.Errorf("expected pool1, got %s", resp.Name)
	}
}

func TestGetPool_NotFound(t *testing.T) {
	_, mux := testHandlers()

	req := httptest.NewRequest(http.MethodGet, "/v1/pools/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestStreamTaskEvents_NoSession(t *testing.T) {
	task := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "task-no-session",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.TaskSpec{
			PoolRef: factoryv1alpha1.LocalObjectReference{Name: "pool1"},
			Prompt:  "do it",
		},
	}
	_, mux := testHandlers(task)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-no-session/events", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStreamTaskEvents_NotFound(t *testing.T) {
	_, mux := testHandlers()

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/nonexistent/events", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestListWorkflowTasks(t *testing.T) {
	wf := &factoryv1alpha1.Workflow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wf1",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.WorkflowSpec{
			Tasks: []factoryv1alpha1.WorkflowTask{
				{Name: "t1", Spec: factoryv1alpha1.TaskInlineSpec{Prompt: "p"}},
			},
		},
	}
	task := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wf1-t1",
			Namespace: "default",
			Labels:    map[string]string{"factory.example.com/workflow": "wf1"},
		},
		Spec: factoryv1alpha1.TaskSpec{
			PoolRef: factoryv1alpha1.LocalObjectReference{Name: "pool1"},
			Prompt:  "do it",
		},
	}
	_, mux := testHandlers(wf, task)

	req := httptest.NewRequest(http.MethodGet, "/v1/workflows/wf1/tasks", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var tasks []TaskResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Name != "wf1-t1" {
		t.Errorf("expected wf1-t1, got %s", tasks[0].Name)
	}
}

func TestApprovePermission(t *testing.T) {
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sess-1",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "claude-code",
			Prompt:     "do it",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase: factoryv1alpha1.SessionPhaseWaitingForApproval,
			PendingApproval: &factoryv1alpha1.PendingApproval{
				ID:       "perm-abc",
				ToolName: "Bash",
				Title:    "rm -rf /tmp",
			},
		},
	}

	pub := &mockPermissionPublisher{}
	h, mux := testHandlers(session)
	h.SetPermissionPublisher(pub)

	body := PermissionDecisionRequest{Decision: "allow", Remember: "session"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-1/permissions/perm-abc", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PermissionDecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Decision != "allow" {
		t.Errorf("expected decision 'allow', got %s", resp.Decision)
	}
	if resp.PermissionID != "perm-abc" {
		t.Errorf("expected permissionId 'perm-abc', got %s", resp.PermissionID)
	}

	// Verify NATS publish was called.
	if len(pub.published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.published))
	}
	if pub.published[0].Subject != "permissions.perm-abc" {
		t.Errorf("expected subject 'permissions.perm-abc', got %s", pub.published[0].Subject)
	}
}

func TestApprovePermission_SessionNotFound(t *testing.T) {
	_, mux := testHandlers()

	body := PermissionDecisionRequest{Decision: "allow"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/nonexistent/permissions/perm-1", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestApprovePermission_NotWaiting(t *testing.T) {
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sess-active",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "claude-code",
			Prompt:     "do it",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase: factoryv1alpha1.SessionPhaseActive,
		},
	}

	h, mux := testHandlers(session)
	h.SetPermissionPublisher(&mockPermissionPublisher{})

	body := PermissionDecisionRequest{Decision: "allow"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-active/permissions/perm-1", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestApprovePermission_WrongPermissionID(t *testing.T) {
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sess-2",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "claude-code",
			Prompt:     "do it",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase: factoryv1alpha1.SessionPhaseWaitingForApproval,
			PendingApproval: &factoryv1alpha1.PendingApproval{
				ID:       "perm-real",
				ToolName: "Bash",
				Title:    "ls",
			},
		},
	}

	h, mux := testHandlers(session)
	h.SetPermissionPublisher(&mockPermissionPublisher{})

	body := PermissionDecisionRequest{Decision: "allow"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-2/permissions/perm-wrong", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestApprovePermission_InvalidDecision(t *testing.T) {
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sess-3",
			Namespace: "default",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "claude-code",
			Prompt:     "do it",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase: factoryv1alpha1.SessionPhaseWaitingForApproval,
			PendingApproval: &factoryv1alpha1.PendingApproval{
				ID:       "perm-x",
				ToolName: "Bash",
				Title:    "ls",
			},
		},
	}

	h, mux := testHandlers(session)
	h.SetPermissionPublisher(&mockPermissionPublisher{})

	body := PermissionDecisionRequest{Decision: "maybe"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-3/permissions/perm-x", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMiddleware_RequestID(t *testing.T) {
	_, mux := testHandlers()
	logger := testLogger()
	handler := requestIDMiddleware(loggingMiddleware(logger)(mux))

	req := httptest.NewRequest(http.MethodGet, "/v1/pools", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header")
	}
}

func TestMiddleware_PanicRecovery(t *testing.T) {
	logger := testLogger()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panic", func(_ http.ResponseWriter, _ *http.Request) {
		panic("test panic")
	})
	handler := recoveryMiddleware(logger)(mux)

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestCreateTask_WithTimeout(t *testing.T) {
	_, mux := testHandlers()

	body := CreateTaskRequest{
		Name:    "task-timeout",
		PoolRef: "pool1",
		Prompt:  "do it",
		Timeout: "30m",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateTask_InvalidTimeout(t *testing.T) {
	_, mux := testHandlers()

	body := CreateTaskRequest{
		Name:    "task-bad-timeout",
		PoolRef: "pool1",
		Prompt:  "do it",
		Timeout: "not-a-duration",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// --- Interactive Session Tests ---

func TestCreateSession(t *testing.T) {
	pool := &factoryv1alpha1.Pool{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pool", Namespace: "default"},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "claude"},
			Replicas:       factoryv1alpha1.ReplicasConfig{Min: 1, Max: 5},
		},
	}
	agentCfg := &factoryv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "default"},
		Spec: factoryv1alpha1.AgentConfigSpec{
			AgentType: "claude-code",
			SDK:       factoryv1alpha1.SDKConfig{Image: "sdk:latest"},
			Bridge:    factoryv1alpha1.BridgeConfig{Image: "bridge:latest"},
		},
	}
	sandbox := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "sb-1", Namespace: "default"},
		Spec: factoryv1alpha1.SandboxSpec{
			PoolRef: factoryv1alpha1.LocalObjectReference{Name: "my-pool"},
		},
		Status: factoryv1alpha1.SandboxStatus{
			Phase: factoryv1alpha1.SandboxPhaseReady,
		},
	}
	_, mux := testHandlers(pool, agentCfg, sandbox)

	body := CreateSessionRequest{PoolRef: "my-pool", Prompt: "hello"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp SessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Mode != "interactive" {
		t.Errorf("expected mode 'interactive', got %s", resp.Mode)
	}
	if resp.SandboxRef != "sb-1" {
		t.Errorf("expected sandboxRef 'sb-1', got %s", resp.SandboxRef)
	}
	if resp.AgentType != "claude-code" {
		t.Errorf("expected agentType 'claude-code', got %s", resp.AgentType)
	}
}

func TestCreateSession_MissingPoolRef(t *testing.T) {
	_, mux := testHandlers()

	body := CreateSessionRequest{Prompt: "hello"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateSession_PoolNotFound(t *testing.T) {
	_, mux := testHandlers()

	body := CreateSessionRequest{PoolRef: "nonexistent"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateSession_NoReadySandbox(t *testing.T) {
	pool := &factoryv1alpha1.Pool{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-pool", Namespace: "default"},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "claude"},
			Replicas:       factoryv1alpha1.ReplicasConfig{Min: 0, Max: 5},
		},
	}
	_, mux := testHandlers(pool)

	body := CreateSessionRequest{PoolRef: "empty-pool"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSendMessage(t *testing.T) {
	sandbox := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "sb-1", Namespace: "default"},
		Status: factoryv1alpha1.SandboxStatus{
			Phase:   factoryv1alpha1.SandboxPhaseActive,
			PodName: "pod-1",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
		Status:     corev1.PodStatus{PodIP: "10.0.0.1"},
	}
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "sess-1", Namespace: "default"},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			Mode:       factoryv1alpha1.SessionModeInteractive,
			AgentType:  "claude-code",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase:              factoryv1alpha1.SessionPhaseActive,
			EventStreamSubject: "sessions.bridge-123",
		},
	}

	bc := &mockBridgeClient{}
	h, mux := testHandlers(sandbox, pod, session)
	h.SetBridgeClientFactory(func(_ string) BridgeClient { return bc })

	body := SendMessageRequest{Message: "do something"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-1/messages", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(bc.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(bc.messages))
	}
	if bc.messages[0].SessionID != "bridge-123" {
		t.Errorf("expected session ID 'bridge-123', got %s", bc.messages[0].SessionID)
	}
	if bc.messages[0].Msg != "do something" {
		t.Errorf("expected message 'do something', got %s", bc.messages[0].Msg)
	}
}

func TestSendMessage_SessionNotFound(t *testing.T) {
	h, mux := testHandlers()
	h.SetBridgeClientFactory(func(_ string) BridgeClient { return &mockBridgeClient{} })

	body := SendMessageRequest{Message: "hello"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/nonexistent/messages", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestSendMessage_SessionNotActive(t *testing.T) {
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "sess-done", Namespace: "default"},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			AgentType:  "claude-code",
		},
		Status: factoryv1alpha1.SessionStatus{Phase: factoryv1alpha1.SessionPhaseCompleted},
	}
	h, mux := testHandlers(session)
	h.SetBridgeClientFactory(func(_ string) BridgeClient { return &mockBridgeClient{} })

	body := SendMessageRequest{Message: "hello"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-done/messages", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSendMessage_EmptyMessage(t *testing.T) {
	_, mux := testHandlers()

	body := SendMessageRequest{Message: ""}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/sess-1/messages", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestDeleteSession(t *testing.T) {
	sandbox := &factoryv1alpha1.Sandbox{
		ObjectMeta: metav1.ObjectMeta{Name: "sb-1", Namespace: "default"},
		Status: factoryv1alpha1.SandboxStatus{
			Phase:   factoryv1alpha1.SandboxPhaseActive,
			PodName: "pod-1",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
		Status:     corev1.PodStatus{PodIP: "10.0.0.1"},
	}
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "sess-close", Namespace: "default"},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "sb-1"},
			Mode:       factoryv1alpha1.SessionModeInteractive,
			AgentType:  "claude-code",
		},
		Status: factoryv1alpha1.SessionStatus{
			Phase:              factoryv1alpha1.SessionPhaseActive,
			EventStreamSubject: "sessions.bridge-456",
		},
	}

	bc := &mockBridgeClient{}
	h, mux := testHandlers(sandbox, pod, session)
	h.SetBridgeClientFactory(func(_ string) BridgeClient { return bc })

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/sess-close", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	_, mux := testHandlers()

	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
