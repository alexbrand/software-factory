package testharness_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/testharness"
)

// TestPromptFailure tests the user experience when the agent fails at startup
// (e.g., invalid API key). The user should see the task fail quickly with a
// clear reason — not hang in Running for an hour.
//
// This test exercises the full signal path:
//   SDK returns error → bridge publishes session.failed → controller updates
//   Session CR → task controller reads session phase → Task CR moves to Failed
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

	// Setup: AgentConfig + Pool + wait for a ready sandbox with pod IP.
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

	// Create a Task and Session directly (simulating the task controller's
	// sandbox-claim → session-create flow). This avoids races with the sandbox
	// controller in envtest while still testing the failure propagation path.
	task := &factoryv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bad-key-task",
			Namespace: "prompt-fail-test",
		},
		Spec: factoryv1alpha1.TaskSpec{
			PoolRef: factoryv1alpha1.LocalObjectReference{Name: "prompt-fail-pool"},
			Prompt:  "this will fail because the API key is invalid",
		},
	}
	if err := h.K8sClient().Create(ctx, task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Set task to Running with sandbox and session refs (as the task controller would).
	now := metav1.Now()
	task.Status.Phase = factoryv1alpha1.TaskPhaseRunning
	task.Status.SandboxRef = &factoryv1alpha1.LocalObjectReference{Name: sb.Name}
	task.Status.SessionRef = &factoryv1alpha1.LocalObjectReference{Name: "bad-key-session"}
	task.Status.StartedAt = &now
	task.Status.Attempts = 1
	if err := h.K8sClient().Status().Update(ctx, task); err != nil {
		t.Fatalf("updating task status: %v", err)
	}

	// Create the session (as the task controller would).
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bad-key-session",
			Namespace: "prompt-fail-test",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: sb.Name},
			AgentType:  "claude-code",
			Prompt:     "this will fail because the API key is invalid",
		},
	}
	if err := h.K8sClient().Create(ctx, session); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	// === ASSERT: Session moves to Failed quickly (via NATS event from bridge) ===
	t.Run("session fails with AgentError", func(t *testing.T) {
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseFailed
		})

		var s factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.FailureReason != factoryv1alpha1.FailureReasonAgentError {
			t.Errorf("expected failureReason AgentError, got %q", s.Status.FailureReason)
		}
	})

	// === ASSERT: Task propagates the failure (via task controller polling session) ===
	t.Run("task fails via API", func(t *testing.T) {
		api := h.APIClient()
		testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
			got, getErr := api.GetTask("bad-key-task")
			return getErr == nil && got.Phase == "Failed"
		})
	})
}
