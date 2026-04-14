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

// TestPromptFailure tests the user experience when the agent fails at startup
// (e.g., invalid API key). The user submits a task via the API and should see
// the task fail quickly with a clear reason.
//
// This is a true end-to-end UAT: task submitted via API → task controller
// claims sandbox → creates session → agent fails → session Failed →
// task Failed — all through real controllers.
func TestPromptFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := testharness.New(t, testharness.WithNamespace("prompt-fail-test"))
	h.Start()

	ctx := context.Background()
	h.CreateNamespace(ctx, "prompt-fail-test")

	// Configure the fake SDK to return an error on session/prompt.
	h.FakeSDK().SetBehavior(testharness.SessionBehavior{
		PromptError: &testharness.JSONRPCError{
			Code:    -32603,
			Message: "Internal error: Invalid API key",
		},
	})

	// Setup: AgentConfig + Pool + wait for ready sandbox with pod IP.
	agentCfg := &factoryv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "prompt-fail-test"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "prompt-fail-pool", Namespace: "prompt-fail-test"},
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
		_ = h.K8sClient().List(ctx, &sbList, client.InNamespace("prompt-fail-test"))
		if len(sbList.Items) == 0 {
			return false
		}
		sb = sbList.Items[0]
		return sb.Status.PodName != ""
	})
	h.SetPodIP(ctx, "prompt-fail-test", sb.Status.PodName, "10.0.0.5")

	// === USER ACTION: Submit a task via the API ===
	api := h.APIClient()
	taskResp, err := api.CreateTask(apiserver.CreateTaskRequest{
		Name:    "bad-key-task",
		PoolRef: "prompt-fail-pool",
		Prompt:  "this will fail because the API key is invalid",
	})
	if err != nil {
		t.Fatalf("creating task via API: %v", err)
	}
	if taskResp.Name != "bad-key-task" {
		t.Fatalf("expected task name 'bad-key-task', got %s", taskResp.Name)
	}

	// === USER EXPECTATION: Task fails quickly ===
	t.Run("task fails via API", func(t *testing.T) {
		testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
			got, getErr := api.GetTask("bad-key-task")
			return getErr == nil && got.Phase == "Failed"
		})
	})

	// === USER EXPECTATION: Session has a clear failure reason ===
	t.Run("session has failureReason AgentError", func(t *testing.T) {
		var sessions factoryv1alpha1.SessionList
		if err := h.K8sClient().List(ctx, &sessions, client.InNamespace("prompt-fail-test")); err != nil {
			t.Fatalf("listing sessions: %v", err)
		}
		if len(sessions.Items) == 0 {
			t.Fatal("expected at least one session")
			return
		}

		sess := sessions.Items[0]
		if sess.Status.Phase != factoryv1alpha1.SessionPhaseFailed {
			t.Errorf("expected session phase Failed, got %s", sess.Status.Phase)
		}
		if sess.Status.FailureReason != factoryv1alpha1.FailureReasonAgentError {
			t.Errorf("expected failureReason AgentError, got %q", sess.Status.FailureReason)
		}
	})
}
