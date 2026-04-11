package testharness_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/testharness"
	"github.com/alexbrand/software-factory/pkg/events"
)

func TestSessionCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := testharness.New(t, testharness.WithNamespace("completion-test"))
	h.Start()

	ctx := context.Background()
	h.CreateNamespace(ctx, "completion-test")

	// Setup: AgentConfig + Pool + wait for sandbox + set pod IP.
	agentCfg := &factoryv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "completion-test"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "completion-pool", Namespace: "completion-test"},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "claude"},
			Replicas:       factoryv1alpha1.ReplicasConfig{Min: 2, Max: 5},
		},
	}
	if err := h.K8sClient().Create(ctx, pool); err != nil {
		t.Fatalf("creating pool: %v", err)
	}

	// Wait for two sandboxes (one per test).
	testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
		var sbList factoryv1alpha1.SandboxList
		_ = h.K8sClient().List(ctx, &sbList, client.InNamespace("completion-test"))
		count := 0
		for _, sb := range sbList.Items {
			if sb.Status.PodName != "" {
				count++
			}
		}
		return count >= 2
	})

	var sbList factoryv1alpha1.SandboxList
	if err := h.K8sClient().List(ctx, &sbList, client.InNamespace("completion-test")); err != nil {
		t.Fatalf("listing sandboxes: %v", err)
	}

	// Set pod IPs for both sandboxes.
	for i := range sbList.Items {
		h.SetPodIP(ctx, "completion-test", sbList.Items[i].Status.PodName, "10.0.0."+string(rune('3'+i)))
	}

	t.Run("Success", func(t *testing.T) {
		sb := &sbList.Items[0]

		session := &factoryv1alpha1.Session{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "success-session",
				Namespace: "completion-test",
			},
			Spec: factoryv1alpha1.SessionSpec{
				SandboxRef: factoryv1alpha1.LocalObjectReference{Name: sb.Name},
				AgentType:  "claude-code",
				Prompt:     "write hello world",
			},
		}
		if err := h.K8sClient().Create(ctx, session); err != nil {
			t.Fatalf("creating session: %v", err)
		}

		// Wait for Active.
		testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseActive
		})

		// Find server ID.
		var serverID string
		testharness.WaitFor(t, 5*time.Second, 200*time.Millisecond, func() bool {
			for _, info := range h.FakeSDK().Sessions() {
				for _, p := range info.Prompts {
					if p == "write hello world" {
						serverID = info.ServerID
						return true
					}
				}
			}
			return false
		})

		// Push session.completed with token data, then close stream.
		completedData, _ := json.Marshal(events.SessionCompletedData{
			InputTokens: 1500, OutputTokens: 800,
		})
		completedSSE := "event: session.completed\ndata: " + string(completedData) + "\n\n"
		if err := h.FakeSDK().PushSSEEvent(serverID, completedSSE); err != nil {
			t.Fatalf("pushing completed event: %v", err)
		}
		h.FakeSDK().CloseSSEStream(serverID)

		t.Run("session enters Completed", func(t *testing.T) {
			testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
				var s factoryv1alpha1.Session
				err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
				return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseCompleted
			})
		})

		t.Run("completedAt is set", func(t *testing.T) {
			var s factoryv1alpha1.Session
			if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
				t.Fatalf("getting session: %v", err)
			}
			if s.Status.CompletedAt == nil {
				t.Fatal("expected completedAt to be set")
			}
		})

		t.Run("tokenUsage is populated", func(t *testing.T) {
			var s factoryv1alpha1.Session
			if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
				t.Fatalf("getting session: %v", err)
			}
			if s.Status.TokenUsage == nil {
				t.Fatal("expected tokenUsage to be set")
				return
			}
			if s.Status.TokenUsage.Input != 1500 {
				t.Errorf("expected input tokens 1500, got %d", s.Status.TokenUsage.Input)
			}
			if s.Status.TokenUsage.Output != 800 {
				t.Errorf("expected output tokens 800, got %d", s.Status.TokenUsage.Output)
			}
		})
	})

	t.Run("Failure", func(t *testing.T) {
		sb := &sbList.Items[1]

		session := &factoryv1alpha1.Session{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "failure-session",
				Namespace: "completion-test",
			},
			Spec: factoryv1alpha1.SessionSpec{
				SandboxRef: factoryv1alpha1.LocalObjectReference{Name: sb.Name},
				AgentType:  "claude-code",
				Prompt:     "do something risky",
			},
		}
		if err := h.K8sClient().Create(ctx, session); err != nil {
			t.Fatalf("creating session: %v", err)
		}

		// Wait for Active.
		testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseActive
		})

		// Find server ID by prompt.
		var serverID string
		testharness.WaitFor(t, 5*time.Second, 200*time.Millisecond, func() bool {
			for _, info := range h.FakeSDK().Sessions() {
				for _, p := range info.Prompts {
					if p == "do something risky" {
						serverID = info.ServerID
						return true
					}
				}
			}
			return false
		})

		// Push session.failed and close stream.
		failedData, _ := json.Marshal(events.SessionFailedData{Reason: "out of memory"})
		failedSSE := "event: session.failed\ndata: " + string(failedData) + "\n\n"
		if err := h.FakeSDK().PushSSEEvent(serverID, failedSSE); err != nil {
			t.Fatalf("pushing failed event: %v", err)
		}
		h.FakeSDK().CloseSSEStream(serverID)

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

		t.Run("completedAt is set on failure", func(t *testing.T) {
			var s factoryv1alpha1.Session
			if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
				t.Fatalf("getting session: %v", err)
			}
			if s.Status.CompletedAt == nil {
				t.Fatal("expected completedAt to be set on failure")
			}
		})
	})
}
