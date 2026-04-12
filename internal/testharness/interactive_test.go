package testharness_test

import (
	"context"
	"encoding/json"
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
//  1. User creates an interactive session via POST /v1/sessions
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

	// Use a prompt delay so the session stays open between messages.
	h.FakeSDK().SetBehavior(testharness.SessionBehavior{
		PromptDelay: 30 * time.Second,
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

	api := h.APIClient()

	// === ACT: Create an interactive session via the API ===
	t.Run("create interactive session", func(t *testing.T) {
		resp, err := api.Raw(http.MethodPost, "/v1/sessions", apiserver.CreateSessionRequest{
			PoolRef: "interactive-pool",
			Prompt:  "Help me debug the auth module",
		})
		if err != nil {
			t.Fatalf("creating session: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
		}

		var sessResp apiserver.SessionResponse
		if err := json.NewDecoder(resp.Body).Decode(&sessResp); err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if sessResp.Mode != "interactive" {
			t.Errorf("expected mode 'interactive', got %s", sessResp.Mode)
		}
	})

	// === ASSERT: Session is Active ===
	t.Run("session is active", func(t *testing.T) {
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var sessions factoryv1alpha1.SessionList
			_ = h.K8sClient().List(ctx, &sessions, client.InNamespace("interactive-test"))
			for _, s := range sessions.Items {
				if s.Spec.Mode == factoryv1alpha1.SessionModeInteractive &&
					s.Status.Phase == factoryv1alpha1.SessionPhaseActive {
					return true
				}
			}
			return false
		})
	})

	// === ACT: Send a follow-up message ===
	t.Run("send follow-up message", func(t *testing.T) {
		// Find the session name.
		var sessions factoryv1alpha1.SessionList
		if err := h.K8sClient().List(ctx, &sessions, client.InNamespace("interactive-test")); err != nil {
			t.Fatalf("listing sessions: %v", err)
		}
		var sessName string
		for _, s := range sessions.Items {
			if s.Spec.Mode == factoryv1alpha1.SessionModeInteractive {
				sessName = s.Name
				break
			}
		}
		if sessName == "" {
			t.Fatal("no interactive session found")
			return
		}

		resp, err := api.Raw(http.MethodPost,
			fmt.Sprintf("/v1/sessions/%s/messages", sessName),
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

		// Verify the fake SDK received the follow-up message.
		testharness.WaitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
			for _, p := range h.FakeSDK().Prompts() {
				if p == "Now add error handling" {
					return true
				}
			}
			return false
		})
	})

	// === ACT: Close the session ===
	t.Run("close session", func(t *testing.T) {
		var sessions factoryv1alpha1.SessionList
		if err := h.K8sClient().List(ctx, &sessions, client.InNamespace("interactive-test")); err != nil {
			t.Fatalf("listing sessions: %v", err)
		}
		var sessName string
		for _, s := range sessions.Items {
			if s.Spec.Mode == factoryv1alpha1.SessionModeInteractive {
				sessName = s.Name
				break
			}
		}

		resp, err := api.Raw(http.MethodDelete,
			fmt.Sprintf("/v1/sessions/%s", sessName), nil)
		if err != nil {
			t.Fatalf("closing session: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 204, got %d: %s", resp.StatusCode, string(body))
		}

		// Session should move to Completed.
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKey{Namespace: "interactive-test", Name: sessName}, &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseCompleted
		})
	})
}
