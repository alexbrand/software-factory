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

func TestHarness_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	h := testharness.New(t, testharness.WithNamespace("smoke-test"))
	h.Start()

	ctx := context.Background()
	h.CreateNamespace(ctx, "smoke-test")

	// Verify all subsystems are up.
	t.Run("K8sClient works", func(t *testing.T) {
		var pools factoryv1alpha1.PoolList
		if err := h.K8sClient().List(ctx, &pools, client.InNamespace("smoke-test")); err != nil {
			t.Fatalf("listing pools: %v", err)
		}
	})

	t.Run("FakeSDK responds to health", func(t *testing.T) {
		resp, err := h.APIClient().Raw("GET", "/healthz", nil)
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("API create and get task", func(t *testing.T) {
		// Create the prerequisites: AgentConfig + Pool.
		agentCfg := &factoryv1alpha1.AgentConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "smoke-test"},
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
			ObjectMeta: metav1.ObjectMeta{Name: "default-pool", Namespace: "smoke-test"},
			Spec: factoryv1alpha1.PoolSpec{
				AgentConfigRef: factoryv1alpha1.LocalObjectReference{Name: "claude"},
				Replicas:       factoryv1alpha1.ReplicasConfig{Min: 1, Max: 5},
			},
		}
		if err := h.K8sClient().Create(ctx, pool); err != nil {
			t.Fatalf("creating pool: %v", err)
		}

		// Create a task via the API server.
		api := h.APIClient()
		taskResp, err := api.CreateTask(apiserver.CreateTaskRequest{
			Name:    "smoke-task",
			PoolRef: "default-pool",
			Prompt:  "hello world",
		})
		if err != nil {
			t.Fatalf("creating task: %v", err)
		}
		if taskResp.Name != "smoke-task" {
			t.Errorf("expected task name 'smoke-task', got %s", taskResp.Name)
		}

		// Verify we can GET the task back.
		got, err := api.GetTask("smoke-task")
		if err != nil {
			t.Fatalf("getting task: %v", err)
		}
		if got.Name != "smoke-task" {
			t.Errorf("expected task name 'smoke-task', got %s", got.Name)
		}
	})

	t.Run("Pool controller creates sandboxes", func(t *testing.T) {
		// The pool created above has min=1. Wait for the pool controller
		// to create at least one sandbox.
		testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var sbList factoryv1alpha1.SandboxList
			_ = h.K8sClient().List(ctx, &sbList, client.InNamespace("smoke-test"))
			return len(sbList.Items) >= 1
		})
	})

	t.Run("Session reaches bridge and fake SDK", func(t *testing.T) {
		// Get the sandbox created by the pool controller.
		var sbList factoryv1alpha1.SandboxList
		if err := h.K8sClient().List(ctx, &sbList, client.InNamespace("smoke-test")); err != nil {
			t.Fatalf("listing sandboxes: %v", err)
		}
		if len(sbList.Items) == 0 {
			t.Fatal("expected at least one sandbox")
		}
		sb := &sbList.Items[0]

		// Mark sandbox as Ready with a pod name.
		sb.Status.Phase = factoryv1alpha1.SandboxPhaseReady
		sb.Status.PodName = "fake-pod"
		if err := h.K8sClient().Status().Update(ctx, sb); err != nil {
			t.Fatalf("marking sandbox ready: %v", err)
		}

		// Create a fake pod so the session controller can resolve the bridge endpoint.
		h.CreateFakePod(ctx, "smoke-test", "fake-pod", "10.0.0.1")

		// Create a Session CR directly (as the task controller would).
		session := &factoryv1alpha1.Session{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "smoke-session",
				Namespace: "smoke-test",
			},
			Spec: factoryv1alpha1.SessionSpec{
				SandboxRef: factoryv1alpha1.LocalObjectReference{Name: sb.Name},
				AgentType:  "claude-code",
				Prompt:     "write a hello world function",
			},
		}
		if err := h.K8sClient().Create(ctx, session); err != nil {
			t.Fatalf("creating session: %v", err)
		}

		// Wait for the session controller to move the session to Active.
		testharness.WaitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
			var s factoryv1alpha1.Session
			err := h.K8sClient().Get(ctx, client.ObjectKeyFromObject(session), &s)
			return err == nil && s.Status.Phase == factoryv1alpha1.SessionPhaseActive
		})

		// Verify the fake SDK received the prompt through the bridge.
		testharness.WaitFor(t, 10*time.Second, 200*time.Millisecond, func() bool {
			for _, p := range h.FakeSDK().Prompts() {
				if p == "write a hello world function" {
					return true
				}
			}
			return false
		})

		// Verify events were published to NATS.
		data := testharness.WaitForNATSMessage(t, h.JetStream(), "events.smoke-test.sessions.>", 10*time.Second)
		if len(data) == 0 {
			t.Fatal("expected non-empty NATS message")
		}
	})
}
