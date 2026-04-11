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

// TestPromptFailure_PropagatesSessionFailed tests that when the agent prompt
// RPC fails (e.g., invalid API key), the session moves to Failed with
// failureReason=AgentError instead of hanging in Active until timeout.
func TestPromptFailure_PropagatesSessionFailed(t *testing.T) {
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

	// Setup: AgentConfig + Pool + wait for sandbox.
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

	// Create session.
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prompt-fail-session",
			Namespace: "prompt-fail-test",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: sb.Name},
			AgentType:  "claude-code",
			Prompt:     "this will fail",
		},
	}
	if err := h.K8sClient().Create(ctx, session); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	// The session should move to Failed quickly (not hang in Active for 1h).
	t.Run("session enters Failed", func(t *testing.T) {
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseFailed
		})
	})

	t.Run("failureReason is AgentError", func(t *testing.T) {
		var s factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.FailureReason != factoryv1alpha1.FailureReasonAgentError {
			t.Errorf("expected failureReason 'AgentError', got %q", s.Status.FailureReason)
		}
	})
}
