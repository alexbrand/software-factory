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
	"github.com/alexbrand/software-factory/internal/testharness"
	"github.com/alexbrand/software-factory/pkg/events"
)

// TestPermissionGating_RequireApproval tests the full permission request flow:
//
//  1. AgentConfig has permissionMode=requireApproval
//  2. Session starts, agent runs normally
//  3. SDK emits a permission request (via SSE)
//  4. Bridge publishes EventPermissionRequested to NATS
//  5. Session controller sets session phase to WaitingForApproval
//  6. Session status has pendingApproval summary
//  7. External client approves via NATS reply subject
//  8. Bridge forwards approval to SDK, publishes EventPermissionResponded
//  9. Session controller sets session phase back to Active
//  10. Session status pendingApproval is cleared
func TestPermissionGating_RequireApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := testharness.New(t, testharness.WithNamespace("perm-test"))
	h.Start()

	ctx := context.Background()
	h.CreateNamespace(ctx, "perm-test")

	// Create AgentConfig with requireApproval mode.
	agentCfg := &factoryv1alpha1.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "claude-approval", Namespace: "perm-test"},
		Spec: factoryv1alpha1.AgentConfigSpec{
			AgentType:      "claude-code",
			PermissionMode: factoryv1alpha1.PermissionModeRequireApproval,
			SDK:            factoryv1alpha1.SDKConfig{Image: "sdk:latest"},
			Bridge:         factoryv1alpha1.BridgeConfig{Image: "bridge:latest"},
		},
	}
	if err := h.K8sClient().Create(ctx, agentCfg); err != nil {
		t.Fatalf("creating agent config: %v", err)
	}

	// Create Pool and wait for sandbox.
	pool := &factoryv1alpha1.Pool{
		ObjectMeta: metav1.ObjectMeta{Name: "approval-pool", Namespace: "perm-test"},
		Spec: factoryv1alpha1.PoolSpec{
			AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "claude-approval"},
			Replicas:       factoryv1alpha1.ReplicasConfig{Min: 1, Max: 5},
		},
	}
	if err := h.K8sClient().Create(ctx, pool); err != nil {
		t.Fatalf("creating pool: %v", err)
	}

	testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
		var sbList factoryv1alpha1.SandboxList
		_ = h.K8sClient().List(ctx, &sbList, client.InNamespace("perm-test"))
		return len(sbList.Items) >= 1
	})

	// Get the sandbox and wait for its pod to be created by the controller.
	var sbList factoryv1alpha1.SandboxList
	if err := h.K8sClient().List(ctx, &sbList, client.InNamespace("perm-test")); err != nil {
		t.Fatalf("listing sandboxes: %v", err)
	}
	sb := &sbList.Items[0]

	testharness.WaitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(sb), sb); err != nil {
			return false
		}
		return sb.Status.PodName != ""
	})

	// Set pod IP (envtest has no kubelet).
	h.SetPodIP(ctx, "perm-test", sb.Status.PodName, "10.0.0.2")

	// Create a Session CR.
	session := &factoryv1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "perm-session",
			Namespace: "perm-test",
		},
		Spec: factoryv1alpha1.SessionSpec{
			SandboxRef: factoryv1alpha1.LocalObjectReference{Name: sb.Name},
			AgentType:  "claude-code",
			Prompt:     "implement the auth module",
		},
	}
	if err := h.K8sClient().Create(ctx, session); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	// Wait for session to become Active.
	testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
		var s factoryv1alpha1.Session
		err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
		return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseActive
	})

	// Find the SDK server ID so we can push SSE events.
	var serverID string
	testharness.WaitFor(t, 5*time.Second, 200*time.Millisecond, func() bool {
		ids := h.FakeSDK().SessionServerIDs()
		if len(ids) > 0 {
			serverID = ids[0]
			return true
		}
		return false
	})

	// Subscribe to NATS to capture permission events.
	var permissionEvents []events.Event
	permCtx, permCancel := context.WithCancel(ctx)
	defer permCancel()
	_, err := h.Subscriber().SubscribeSession(permCtx, "perm-test", ">", func(ev events.Event) {
		if ev.Type == events.EventPermissionRequested || ev.Type == events.EventPermissionResponded {
			permissionEvents = append(permissionEvents, ev)
		}
	})
	if err != nil {
		t.Fatalf("subscribing to events: %v", err)
	}

	// === ACT: Push a permission request from the fake SDK ===
	// This simulates the agent asking "can I run mkdir -p /workspace/output?"
	permReqData, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "session/request_permission",
		"params": map[string]interface{}{
			"toolName": "Bash",
			"title":    "mkdir -p /workspace/output",
		},
	})
	sseEvent := "event: message\ndata: " + string(permReqData) + "\n\n"
	if err := h.FakeSDK().PushSSEEvent(serverID, sseEvent); err != nil {
		t.Fatalf("pushing SSE event: %v", err)
	}

	// === ASSERT: Session moves to WaitingForApproval ===
	t.Run("session enters WaitingForApproval", func(t *testing.T) {
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseWaitingForApproval
		})
	})

	// === ASSERT: PendingApproval is populated ===
	t.Run("pendingApproval is set", func(t *testing.T) {
		var s factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.PendingApproval == nil {
			t.Fatal("expected pendingApproval to be set")
		}
		if s.Status.PendingApproval.ToolName != "Bash" {
			t.Errorf("expected toolName 'Bash', got %s", s.Status.PendingApproval.ToolName)
		}
	})

	// === ASSERT: EventPermissionRequested published to NATS ===
	t.Run("permission requested event in NATS", func(t *testing.T) {
		testharness.WaitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
			for _, ev := range permissionEvents {
				if ev.Type == events.EventPermissionRequested {
					return true
				}
			}
			return false
		})
	})

	// === ACT: Approve the permission via the API endpoint ===
	t.Run("approve and session resumes", func(t *testing.T) {
		// Get the session to read the permission ID.
		var s factoryv1alpha1.Session
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.PendingApproval == nil {
			t.Fatal("expected pendingApproval to be set")
			return
		}
		permID := s.Status.PendingApproval.ID

		// Approve via the API server endpoint.
		api := h.APIClient()
		resp, err := api.Raw(http.MethodPost,
			fmt.Sprintf("/v1/sessions/perm-session/permissions/%s", permID),
			map[string]string{"decision": "allow", "remember": "once"},
		)
		if err != nil {
			t.Fatalf("approving permission: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		// === ASSERT: Session moves back to Active ===
		testharness.WaitFor(t, 15*time.Second, 500*time.Millisecond, func() bool {
			var sess factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &sess)
			return err == nil && sess.Status.Phase == factoryv1alpha1.SessionPhaseActive
		})

		// === ASSERT: PendingApproval is cleared ===
		if err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s); err != nil {
			t.Fatalf("getting session: %v", err)
		}
		if s.Status.PendingApproval != nil {
			t.Error("expected pendingApproval to be cleared after approval")
		}
	})

	// === ASSERT: EventPermissionResponded published to NATS ===
	t.Run("permission responded event in NATS", func(t *testing.T) {
		testharness.WaitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
			for _, ev := range permissionEvents {
				if ev.Type == events.EventPermissionResponded {
					return true
				}
			}
			return false
		})
	})
}
