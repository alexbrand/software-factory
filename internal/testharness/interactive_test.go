package testharness_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/apiserver"
	"github.com/alexbrand/software-factory/internal/testharness"
)

// TestInteractiveSession tests the multi-turn conversation flow:
//
//  1. Create an interactive session (via K8s client, working around #34)
//  2. Session becomes Active
//  3. User sends a follow-up message via POST /v1/sessions/{id}/messages
//  4. Agent processes the message (fake SDK receives it)
//  5. User closes the session via DELETE /v1/sessions/{id}
//  6. Session moves to Completed
func TestInteractiveSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := testharness.New(t, testharness.WithNamespace("interactive-test"))
	h.Start()

	ctx := context.Background()
	h.CreateNamespace(ctx, "interactive-test")

	// Use a short prompt delay so the session stays active long enough to
	// verify it doesn't auto-close, but short enough that follow-up messages
	// can be sent after the initial prompt completes.
	h.FakeSDK().SetBehavior(testharness.SessionBehavior{
		PromptDelay: 2 * time.Second,
	})

	// Setup: AgentConfig + Pool + wait for ready sandbox.
	agentCfg := &factoryv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "interactive-test"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "interactive-pool", Namespace: "interactive-test"},
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
		_ = h.K8sClient().List(ctx, &sbList, client.InNamespace("interactive-test"))
		if len(sbList.Items) == 0 {
			return false
		}
		sb = sbList.Items[0]
		return sb.Status.PodName != ""
	})
	h.SetPodIP(ctx, "interactive-test", sb.Status.PodName, "10.0.0.6")

	// Create interactive session directly (working around #34 sandbox claiming race).
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "interactive-session",
			Namespace: "interactive-test",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: sb.Name},
			Mode:       factoryv1alpha1.SessionModeInteractive,
			AgentType:  "claude-code",
			Prompt:     "Help me debug the auth module",
		},
	}
	if err := h.K8sClient().Create(ctx, session); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	// === ASSERT: Session becomes Active ===
	t.Run("session is active", func(t *testing.T) {
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseActive
		})
	})

	// === ASSERT: Session stays Active (interactive mode doesn't auto-close) ===
	t.Run("session stays active after prompt", func(t *testing.T) {
		// Verify the fake SDK received the initial prompt.
		testharness.WaitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
			for _, p := range h.FakeSDK().Prompts() {
				if p == "Help me debug the auth module" {
					return true
				}
			}
			return false
		})

		// Session should still be Active (not Completed).
		var s factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.Phase != factoryv1alpha1.SessionPhaseActive {
			t.Errorf("expected Active, got %s (interactive sessions should stay open)", s.Status.Phase)
		}
	})

	// === ACT: Send a follow-up message via the API ===
	t.Run("send follow-up message via API", func(t *testing.T) {
		api := h.APIClient()
		resp, err := api.Raw(http.MethodPost,
			"/v1/sessions/interactive-session/messages",
			apiserver.SendMessageRequest{Message: "Now add error handling"},
		)
		if err != nil {
			t.Fatalf("sending message: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 202, got %d: %s", resp.StatusCode, string(body))
		}

		// Verify the fake SDK received the follow-up.
		testharness.WaitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
			for _, p := range h.FakeSDK().Prompts() {
				if p == "Now add error handling" {
					return true
				}
			}
			return false
		})
	})

	// === ACT: Close the session via the API ===
	t.Run("close session via API", func(t *testing.T) {
		api := h.APIClient()
		resp, err := api.Raw(http.MethodDelete,
			fmt.Sprintf("/v1/sessions/%s", session.Name), nil)
		if err != nil {
			t.Fatalf("closing session: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusNoContent {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 204, got %d: %s", resp.StatusCode, string(body))
		}

		// Session should move to Completed.
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseCompleted
		})
	})
}
