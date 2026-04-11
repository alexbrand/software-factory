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

// TestSessionCompletion_Success tests the happy path: agent finishes,
// SSE stream closes, session moves to Completed with metadata.
func TestSessionCompletion_Success(t *testing.T) {
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
			Replicas:       factoryv1alpha1.ReplicasConfig{Min: 1, Max: 5},
		},
	}
	if err := h.K8sClient().Create(ctx, pool); err != nil {
		t.Fatalf("creating pool: %v", err)
	}

	// Wait for sandbox, then set pod IP.
	var sb factoryv1alpha1.Sandbox
	testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
		var sbList factoryv1alpha1.SandboxList
		_ = h.K8sClient().List(ctx, &sbList, client.InNamespace("completion-test"))
		if len(sbList.Items) == 0 {
			return false
		}
		sb = sbList.Items[0]
		return sb.Status.PodName != ""
	})
	h.SetPodIP(ctx, "completion-test", sb.Status.PodName, "10.0.0.3")

	// Create a Session.
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "completion-session",
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

	// Find the SDK server ID and push a token usage event before closing.
	var serverID string
	testharness.WaitFor(t, 5*time.Second, 200*time.Millisecond, func() bool {
		ids := h.FakeSDK().SessionServerIDs()
		if len(ids) > 0 {
			serverID = ids[0]
			return true
		}
		return false
	})

	// Push a token.usage event.
	tokenData, _ := json.Marshal(map[string]interface{}{
		"inputTokens": 1500, "outputTokens": 800,
	})
	tokenSSE := "event: token.usage\ndata: " + string(tokenData) + "\n\n"
	if err := h.FakeSDK().PushSSEEvent(serverID, tokenSSE); err != nil {
		t.Fatalf("pushing token event: %v", err)
	}

	// Push a session.completed event, then close the SSE stream.
	completedData, _ := json.Marshal(map[string]interface{}{
		"inputTokens": 1500, "outputTokens": 800,
	})
	completedSSE := "event: session.completed\ndata: " + string(completedData) + "\n\n"
	if err := h.FakeSDK().PushSSEEvent(serverID, completedSSE); err != nil {
		t.Fatalf("pushing completed event: %v", err)
	}
	h.FakeSDK().CloseSSEStream(serverID)

	// === ASSERT: Session moves to Completed ===
	t.Run("session enters Completed", func(t *testing.T) {
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseCompleted
		})
	})

	// === ASSERT: CompletedAt is set ===
	t.Run("completedAt is set", func(t *testing.T) {
		var s factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.CompletedAt == nil {
			t.Fatal("expected completedAt to be set")
		}
	})

	// === ASSERT: TokenUsage is populated ===
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

	// === ASSERT: session.completed event in NATS ===
	t.Run("completion event in NATS", func(t *testing.T) {
		data := testharness.WaitForNATSMessage(t, h.JetStream(), "events.completion-test.sessions.>", 10*time.Second)
		var ev events.Event
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("unmarshaling event: %v", err)
		}
		// We should find at least one session lifecycle event.
		if ev.Type == "" {
			t.Fatal("expected event type to be set")
		}
	})
}

// TestSessionCompletion_Failure tests: agent sends session.failed,
// session moves to Failed with failureReason=AgentError.
func TestSessionCompletion_Failure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := testharness.New(t, testharness.WithNamespace("failure-test"))
	h.Start()

	ctx := context.Background()
	h.CreateNamespace(ctx, "failure-test")

	// Setup.
	agentCfg := &factoryv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "failure-test"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "failure-pool", Namespace: "failure-test"},
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
		_ = h.K8sClient().List(ctx, &sbList, client.InNamespace("failure-test"))
		if len(sbList.Items) == 0 {
			return false
		}
		sb = sbList.Items[0]
		return sb.Status.PodName != ""
	})
	h.SetPodIP(ctx, "failure-test", sb.Status.PodName, "10.0.0.4")

	// Create session.
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "failure-session",
			Namespace: "failure-test",
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

	// Find server ID.
	var serverID string
	testharness.WaitFor(t, 5*time.Second, 200*time.Millisecond, func() bool {
		ids := h.FakeSDK().SessionServerIDs()
		if len(ids) > 0 {
			serverID = ids[0]
			return true
		}
		return false
	})

	// Push a session.failed event and close the stream.
	failedData, _ := json.Marshal(map[string]string{"reason": "out of memory"})
	failedSSE := "event: session.failed\ndata: " + string(failedData) + "\n\n"
	if err := h.FakeSDK().PushSSEEvent(serverID, failedSSE); err != nil {
		t.Fatalf("pushing failed event: %v", err)
	}
	h.FakeSDK().CloseSSEStream(serverID)

	// === ASSERT: Session moves to Failed ===
	t.Run("session enters Failed", func(t *testing.T) {
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseFailed
		})
	})

	// === ASSERT: failureReason is AgentError ===
	t.Run("failureReason is AgentError", func(t *testing.T) {
		var s factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.FailureReason != factoryv1alpha1.FailureReasonAgentError {
			t.Errorf("expected failureReason 'AgentError', got %q", s.Status.FailureReason)
		}
	})

	// === ASSERT: CompletedAt is set ===
	t.Run("completedAt is set on failure", func(t *testing.T) {
		var s factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.CompletedAt == nil {
			t.Fatal("expected completedAt to be set on failure")
		}
	})
}
