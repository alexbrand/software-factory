package apiserver

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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

func testHandlers(objs ...client.Object) (*Handlers, *http.ServeMux) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(objs...).WithStatusSubresource(objs...).Build()
	h := NewHandlers(c, nil, testLogger(), "default")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/workflows", h.CreateWorkflow)
	mux.HandleFunc("GET /v1/workflows/{id}", h.GetWorkflow)
	mux.HandleFunc("DELETE /v1/workflows/{id}", h.DeleteWorkflow)
	mux.HandleFunc("GET /v1/workflows/{id}/tasks", h.ListWorkflowTasks)
	mux.HandleFunc("POST /v1/tasks", h.CreateTask)
	mux.HandleFunc("GET /v1/tasks/{id}", h.GetTask)
	mux.HandleFunc("GET /v1/tasks/{id}/events", h.StreamTaskEvents)
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
