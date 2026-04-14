package testharness_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/apiserver"
	"github.com/alexbrand/software-factory/internal/testharness"
)

// TestTaskSuccess_ViaAPI tests the golden path end-to-end through the API:
//
//  1. User submits POST /v1/tasks
//  2. Task controller claims a sandbox and creates a session
//  3. Session controller starts the agent via the bridge
//  4. Agent runs the prompt (fake SDK responds successfully)
//  5. Bridge publishes session.completed to NATS
//  6. Session moves to Completed
//  7. Task moves to Succeeded
//  8. User reads GET /v1/tasks/{id} and sees phase=Succeeded
func TestTaskSuccess_ViaAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := testharness.New(t, testharness.WithNamespace("task-success-test"))
	h.Start()

	ctx := context.Background()
	h.CreateNamespace(ctx, "task-success-test")

	// Setup: AgentConfig + Pool + wait for ready sandbox with pod IP.
	agentCfg := &factoryv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "task-success-test"},
		Spec: factoryv1alpha1.AgentConfigSpec{
			AgentType: "claude-code",
			SDK:       factoryv1alpha1.SDKConfig{Image: "sdk:latest"},
			Bridge:    factoryv1alpha1.BridgeConfig{Image: "bridge:latest"},
		},
	}
	if err := h.K8sClient().Create(ctx, agentCfg); err != nil {
		t.Fatalf("creating agent config: %v", err)
	}

	pool := &factoryv1alpha1.Pool{
		ObjectMeta: metav1.ObjectMeta{Name: "success-pool", Namespace: "task-success-test"},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "claude"},
			Replicas:       factoryv1alpha1.ReplicasConfig{Min: 1, Max: 5},
		},
	}
	if err := h.K8sClient().Create(ctx, pool); err != nil {
		t.Fatalf("creating pool: %v", err)
	}

	var sb factoryv1alpha1.Sandbox
	testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
		var sbList factoryv1alpha1.SandboxList
		_ = h.K8sClient().List(ctx, &sbList, client.InNamespace("task-success-test"))
		if len(sbList.Items) == 0 {
			return false
		}
		sb = sbList.Items[0]
		return sb.Status.PodName != ""
	})
	h.SetPodIP(ctx, "task-success-test", sb.Status.PodName, "10.0.0.10")

	// === USER ACTION: Submit a task via the API ===
	api := h.APIClient()
	taskResp, err := api.CreateTask(apiserver.CreateTaskRequest{
		Name:    "golden-path",
		PoolRef: "success-pool",
		Prompt:  "write a hello world program",
	})
	if err != nil {
		t.Fatalf("creating task via API: %v", err)
	}
	if taskResp.Name != "golden-path" {
		t.Fatalf("expected task name 'golden-path', got %s", taskResp.Name)
	}

	// === ASSERT: Task succeeds ===
	t.Run("task succeeds via API", func(t *testing.T) {
		testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
			got, getErr := api.GetTask("golden-path")
			return getErr == nil && got.Phase == "Succeeded"
		})
	})

	// === ASSERT: Session is Completed ===
	t.Run("session is completed", func(t *testing.T) {
		var task factoryv1alpha1.Task
		if err := h.K8sClient().Get(ctx, client.ObjectKey{Namespace: "task-success-test", Name: "golden-path"}, &task); err != nil {
			t.Fatalf("getting task: %v", err)
		}
		if task.Status.SessionRef == nil {
			t.Fatal("expected sessionRef to be set")
			return
		}

		var sess factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKey{Namespace: "task-success-test", Name: task.Status.SessionRef.Name}, &sess); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if sess.Status.Phase != factoryv1alpha1.SessionPhaseCompleted {
			t.Errorf("expected session phase Completed, got %s", sess.Status.Phase)
		}
		if sess.Status.CompletedAt == nil {
			t.Error("expected completedAt to be set")
		}
	})

	// === ASSERT: Fake SDK received the prompt ===
	t.Run("agent received the prompt", func(t *testing.T) {
		found := false
		for _, p := range h.FakeSDK().Prompts() {
			if p == "write a hello world program" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected fake SDK to receive the prompt")
		}
	})
}
