package controller

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/bridge"
	"github.com/alexbrand/software-factory/pkg/events"
)

// fakeEventPublisher records published events for testing.
type fakeEventPublisher struct {
	events []events.Event
	err    error
}

func (f *fakeEventPublisher) Publish(_ context.Context, _ string, event events.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, event)
	return nil
}

// fakeBridgeClient implements BridgeClient for testing.
type fakeBridgeClient struct {
	startSessionID  string
	startSessionErr error
	cancelErr       error
	healthStatus    *bridge.HealthStatus
	healthErr       error

	startCalled  bool
	cancelCalled bool
	healthCalled bool
	lastConfig   bridge.SessionConfig
	lastCancel   string
}

func (f *fakeBridgeClient) StartSession(_ context.Context, cfg bridge.SessionConfig) (string, error) {
	f.startCalled = true
	f.lastConfig = cfg
	return f.startSessionID, f.startSessionErr
}

func (f *fakeBridgeClient) SendMessage(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *fakeBridgeClient) CancelSession(_ context.Context, sessionID string) error {
	f.cancelCalled = true
	f.lastCancel = sessionID
	return f.cancelErr
}

func (f *fakeBridgeClient) GetHealth(_ context.Context) (*bridge.HealthStatus, error) {
	f.healthCalled = true
	return f.healthStatus, f.healthErr
}

func newSessionReconciler(objs []client.Object, bc *fakeBridgeClient, ep *fakeEventPublisher) *SessionReconciler {
	scheme := newScheme()
	_ = corev1.AddToScheme(scheme)

	cb := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&factoryv1alpha1.Session{},
		&factoryv1alpha1.Sandbox{},
	)
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}

	r := &SessionReconciler{
		Client: cb.Build(),
		Scheme: scheme,
		BridgeClientFactory: func(_ string) BridgeClient {
			return bc
		},
	}
	if ep != nil {
		r.EventPublisher = ep
	}
	return r
}

func newPod(name, namespace, ip string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "agent", Image: "test:latest"},
			},
		},
	}
	pod.Status.PodIP = ip
	return pod
}

func TestSessionReconcile(t *testing.T) {
	tests := []struct {
		name           string
		session        *factoryv1alpha1.Session
		sandbox        *factoryv1alpha1.Sandbox
		pod            *corev1.Pod
		bridgeClient   *fakeBridgeClient
		wantPhase      factoryv1alpha1.SessionPhase
		wantRequeue    bool
		wantErr        bool
		checkBridge    func(t *testing.T, bc *fakeBridgeClient)
		wantEventTypes []events.EventType
	}{
		{
			name: "pending session starts on bridge",
			session: &factoryv1alpha1.Session{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-session",
					Namespace: "default",
				},
				Spec: factoryv1alpha1.SessionSpec{
					SandboxRef:   factoryv1alpha1.LocalObjectReference{Name: "test-sandbox"},
					AgentType:    "claude",
					Prompt:       "write tests",
					ContextFiles: []string{"/src/main.go"},
				},
				Status: factoryv1alpha1.SessionStatus{
					Phase: factoryv1alpha1.SessionPhasePending,
				},
			},
			sandbox: &factoryv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sandbox",
					Namespace: "default",
				},
				Status: factoryv1alpha1.SandboxStatus{
					Phase:   factoryv1alpha1.SandboxPhaseAssigned,
					PodName: "test-pod",
				},
			},
			pod:          newPod("test-pod", "default", "10.0.0.1"),
			bridgeClient:   &fakeBridgeClient{startSessionID: "bridge-sess-1"},
			wantPhase:      factoryv1alpha1.SessionPhaseActive,
			wantRequeue:    true,
			wantEventTypes: []events.EventType{events.EventSessionStarted},
			checkBridge: func(t *testing.T, bc *fakeBridgeClient) {
				if !bc.startCalled {
					t.Error("expected StartSession to be called")
				}
				if bc.lastConfig.Prompt != "write tests" {
					t.Errorf("expected prompt 'write tests', got %q", bc.lastConfig.Prompt)
				}
				if bc.lastConfig.AgentType != "claude" {
					t.Errorf("expected agent type 'claude', got %q", bc.lastConfig.AgentType)
				}
			},
		},
		{
			name: "pending session requeues when pod not ready",
			session: &factoryv1alpha1.Session{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-session",
					Namespace: "default",
				},
				Spec: factoryv1alpha1.SessionSpec{
					SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "test-sandbox"},
					AgentType:  "claude",
					Prompt:     "do stuff",
				},
				Status: factoryv1alpha1.SessionStatus{
					Phase: factoryv1alpha1.SessionPhasePending,
				},
			},
			sandbox: &factoryv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sandbox",
					Namespace: "default",
				},
				Status: factoryv1alpha1.SandboxStatus{
					Phase:   factoryv1alpha1.SandboxPhaseCreating,
					PodName: "",
				},
			},
			bridgeClient: &fakeBridgeClient{},
			wantPhase:    factoryv1alpha1.SessionPhasePending,
			wantRequeue:  true,
			checkBridge: func(t *testing.T, bc *fakeBridgeClient) {
				if bc.startCalled {
					t.Error("StartSession should not be called when pod not ready")
				}
			},
		},
		{
			name: "pending session requeues on bridge start error",
			session: &factoryv1alpha1.Session{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-session",
					Namespace: "default",
				},
				Spec: factoryv1alpha1.SessionSpec{
					SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "test-sandbox"},
					AgentType:  "claude",
					Prompt:     "do stuff",
				},
				Status: factoryv1alpha1.SessionStatus{
					Phase: factoryv1alpha1.SessionPhasePending,
				},
			},
			sandbox: &factoryv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sandbox",
					Namespace: "default",
				},
				Status: factoryv1alpha1.SandboxStatus{
					Phase:   factoryv1alpha1.SandboxPhaseAssigned,
					PodName: "test-pod",
				},
			},
			pod:          newPod("test-pod", "default", "10.0.0.1"),
			bridgeClient: &fakeBridgeClient{startSessionErr: fmt.Errorf("connection refused")},
			wantPhase:    factoryv1alpha1.SessionPhasePending,
			wantRequeue:  true,
		},
		{
			name: "active session requeues when bridge healthy (completion via NATS events)",
			session: &factoryv1alpha1.Session{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-session",
					Namespace: "default",
				},
				Spec: factoryv1alpha1.SessionSpec{
					SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "test-sandbox"},
					AgentType:  "claude",
					Prompt:     "do stuff",
				},
				Status: factoryv1alpha1.SessionStatus{
					Phase:              factoryv1alpha1.SessionPhaseActive,
					EventStreamSubject: "sessions.bridge-sess-1",
				},
			},
			sandbox: &factoryv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sandbox",
					Namespace: "default",
				},
				Status: factoryv1alpha1.SandboxStatus{
					Phase:   factoryv1alpha1.SandboxPhaseActive,
					PodName: "test-pod",
				},
			},
			pod: newPod("test-pod", "default", "10.0.0.1"),
			bridgeClient: &fakeBridgeClient{
				healthStatus: &bridge.HealthStatus{
					Status:         "healthy",
					ActiveSessions: 0,
				},
			},
			wantPhase:   factoryv1alpha1.SessionPhaseActive,
			wantRequeue: true,
		},
		{
			name: "active session stays active when sessions still running",
			session: &factoryv1alpha1.Session{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-session",
					Namespace: "default",
				},
				Spec: factoryv1alpha1.SessionSpec{
					SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "test-sandbox"},
					AgentType:  "claude",
					Prompt:     "do stuff",
				},
				Status: factoryv1alpha1.SessionStatus{
					Phase:              factoryv1alpha1.SessionPhaseActive,
					EventStreamSubject: "sessions.bridge-sess-1",
				},
			},
			sandbox: &factoryv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sandbox",
					Namespace: "default",
				},
				Status: factoryv1alpha1.SandboxStatus{
					Phase:   factoryv1alpha1.SandboxPhaseActive,
					PodName: "test-pod",
				},
			},
			pod: newPod("test-pod", "default", "10.0.0.1"),
			bridgeClient: &fakeBridgeClient{
				healthStatus: &bridge.HealthStatus{
					Status:         "healthy",
					ActiveSessions: 1,
				},
			},
			wantPhase:   factoryv1alpha1.SessionPhaseActive,
			wantRequeue: true,
		},
		{
			name: "active session fails when pod disappears",
			session: &factoryv1alpha1.Session{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-session",
					Namespace: "default",
				},
				Spec: factoryv1alpha1.SessionSpec{
					SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "test-sandbox"},
					AgentType:  "claude",
					Prompt:     "do stuff",
				},
				Status: factoryv1alpha1.SessionStatus{
					Phase:              factoryv1alpha1.SessionPhaseActive,
					EventStreamSubject: "sessions.bridge-sess-1",
				},
			},
			sandbox: &factoryv1alpha1.Sandbox{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sandbox",
					Namespace: "default",
				},
				Status: factoryv1alpha1.SandboxStatus{
					Phase:   factoryv1alpha1.SandboxPhaseTerminating,
					PodName: "",
				},
			},
			bridgeClient:   &fakeBridgeClient{},
			wantPhase:      factoryv1alpha1.SessionPhaseFailed,
			wantRequeue:    false,
			wantEventTypes: []events.EventType{events.EventSessionFailed},
		},
		{
			name: "completed session is a no-op",
			session: &factoryv1alpha1.Session{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-session",
					Namespace: "default",
				},
				Spec: factoryv1alpha1.SessionSpec{
					SandboxRef: factoryv1alpha1.LocalObjectReference{Name: "test-sandbox"},
					AgentType:  "claude",
					Prompt:     "do stuff",
				},
				Status: factoryv1alpha1.SessionStatus{
					Phase: factoryv1alpha1.SessionPhaseCompleted,
				},
			},
			bridgeClient: &fakeBridgeClient{},
			wantPhase:    factoryv1alpha1.SessionPhaseCompleted,
			wantRequeue:  false,
		},
		{
			name: "session not found is a no-op",
			// No session object provided.
			bridgeClient: &fakeBridgeClient{},
			wantRequeue:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []client.Object
			if tt.session != nil {
				objs = append(objs, tt.session)
			}
			if tt.sandbox != nil {
				objs = append(objs, tt.sandbox)
			}
			if tt.pod != nil {
				objs = append(objs, tt.pod)
			}

			ep := &fakeEventPublisher{}
			r := newSessionReconciler(objs, tt.bridgeClient, ep)

			reqName := "test-session"
			if tt.session != nil {
				reqName = tt.session.Name
			}

			result, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      reqName,
					Namespace: "default",
				},
			})

			if (err != nil) != tt.wantErr {
				t.Fatalf("Reconcile() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantRequeue {
				if result.RequeueAfter == 0 && !result.Requeue {
					t.Error("expected requeue, got none")
				}
			} else {
				if result.RequeueAfter != 0 || result.Requeue {
					t.Errorf("expected no requeue, got RequeueAfter=%v Requeue=%v", result.RequeueAfter, result.Requeue)
				}
			}

			// Check final phase.
			if tt.session != nil && tt.wantPhase != "" {
				var updated factoryv1alpha1.Session
				if err := r.Get(context.Background(), types.NamespacedName{
					Name:      tt.session.Name,
					Namespace: tt.session.Namespace,
				}, &updated); err != nil {
					t.Fatalf("getting updated session: %v", err)
				}
				if updated.Status.Phase != tt.wantPhase {
					t.Errorf("expected phase %q, got %q", tt.wantPhase, updated.Status.Phase)
				}
			}

			if tt.checkBridge != nil {
				tt.checkBridge(t, tt.bridgeClient)
			}

			// Verify published events.
			if len(tt.wantEventTypes) != len(ep.events) {
				t.Errorf("expected %d events, got %d", len(tt.wantEventTypes), len(ep.events))
			} else {
				for i, wantType := range tt.wantEventTypes {
					if ep.events[i].Type != wantType {
						t.Errorf("event[%d] type = %q, want %q", i, ep.events[i].Type, wantType)
					}
					if ep.events[i].ID == "" {
						t.Errorf("event[%d] ID should not be empty", i)
					}
					if ep.events[i].SessionID == "" {
						t.Errorf("event[%d] SessionID should not be empty", i)
					}
				}
			}
		})
	}
}
