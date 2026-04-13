package testharness

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/apiserver"
	"github.com/alexbrand/software-factory/internal/bridge"
	"github.com/alexbrand/software-factory/internal/controller"
	"github.com/alexbrand/software-factory/pkg/events"
)

// Harness wires together all subsystems for integration testing:
// embedded NATS, envtest (K8s API), fake SDK, real bridge, and real API server.
type Harness struct {
	t      *testing.T
	logger *slog.Logger

	// NATS
	natsServer *natsserver.Server
	natsConn   *nats.Conn
	js         nats.JetStreamContext

	// Kubernetes (envtest)
	testEnv   *envtest.Environment
	k8sClient client.Client
	mgr       ctrl.Manager
	mgrCancel context.CancelFunc

	// Bridge stack
	fakeSDK    *FakeSDK
	bridgeHTTP *httptest.Server

	// API server
	apiHTTP *httptest.Server

	// Events
	publisher  *events.Publisher
	subscriber *events.Subscriber

	// Options
	namespace string
}

// Option configures the harness.
type Option func(*Harness)

// WithNamespace sets the default namespace for tests.
func WithNamespace(ns string) Option {
	return func(h *Harness) { h.namespace = ns }
}

// New creates a new harness. Call Start() to boot all subsystems.
func New(t *testing.T, opts ...Option) *Harness {
	t.Helper()
	h := &Harness{
		t:         t,
		namespace: "test-harness",
		logger:    slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Start boots all subsystems in dependency order.
func (h *Harness) Start() {
	h.t.Helper()

	// 1. Embedded NATS with JetStream.
	h.startNATS()

	// 2. Event publisher + subscriber backed by real NATS.
	h.publisher = events.NewPublisher(h.js)
	h.subscriber = events.NewSubscriber(h.js)
	if err := h.publisher.EnsureStream(context.Background(), events.DefaultStreamName); err != nil {
		h.t.Fatalf("ensuring NATS stream: %v", err)
	}

	// 3. Fake SDK.
	h.fakeSDK = NewFakeSDK()

	// 4. Bridge stack: SDKClient → SessionManager → EventForwarder → PermissionHandler → Server.
	sdkClient := bridge.NewSDKClient(h.fakeSDK.URL())
	sessionManager := bridge.NewSessionManager(sdkClient, h.logger)
	eventForwarder := bridge.NewEventForwarder(h.publisher, h.namespace, h.logger)
	permHandler := bridge.NewPermissionHandler(h.publisher, h.natsConn, h.namespace, h.logger)
	bridgeServer := bridge.NewServer(sessionManager, eventForwarder, h.logger)
	bridgeServer.SetPermissionHandler(permHandler)
	h.bridgeHTTP = httptest.NewServer(bridgeServer.Handler())

	// 5. Envtest + controllers.
	h.startKubernetes()

	// 6. API server.
	h.startAPIServer()

	h.t.Cleanup(h.Stop)
}

// startKubernetes boots envtest and registers all controllers.
func (h *Harness) startKubernetes() {
	h.t.Helper()

	// Resolve CRD directory relative to this source file.
	_, thisFile, _, _ := runtime.Caller(0)
	crdDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "config", "crd", "bases")

	h.testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdDir},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := h.testEnv.Start()
	if err != nil {
		h.t.Fatalf("starting envtest: %v", err)
	}

	if err := factoryv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		h.t.Fatalf("adding scheme: %v", err)
	}

	skipNameValidation := true
	h.mgr, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Controller: ctrlconfig.Controller{
			SkipNameValidation: &skipNameValidation,
		},
	})
	if err != nil {
		h.t.Fatalf("creating manager: %v", err)
	}

	// Register all controllers.
	bridgeURL := h.bridgeHTTP.URL
	if err := (&controller.PoolReconciler{
		Client: h.mgr.GetClient(), Scheme: h.mgr.GetScheme(),
	}).SetupWithManager(h.mgr); err != nil {
		h.t.Fatalf("setting up pool controller: %v", err)
	}
	if err := (&controller.SandboxReconciler{
		Client: h.mgr.GetClient(), Scheme: h.mgr.GetScheme(),
	}).SetupWithManager(h.mgr); err != nil {
		h.t.Fatalf("setting up sandbox controller: %v", err)
	}
	if err := (&controller.WorkflowReconciler{
		Client: h.mgr.GetClient(), Scheme: h.mgr.GetScheme(),
	}).SetupWithManager(h.mgr); err != nil {
		h.t.Fatalf("setting up workflow controller: %v", err)
	}
	if err := (&controller.TaskReconciler{
		Client: h.mgr.GetClient(), Scheme: h.mgr.GetScheme(),
	}).SetupWithManager(h.mgr); err != nil {
		h.t.Fatalf("setting up task controller: %v", err)
	}
	if err := (&controller.SessionReconciler{
		Client: h.mgr.GetClient(),
		Scheme: h.mgr.GetScheme(),
		BridgeClientFactory: func(_ string) controller.BridgeClient {
			// Always route to our test bridge, ignoring the pod IP-based URL.
			return bridge.NewClient(bridgeURL)
		},
		EventPublisher:  h.publisher,
		EventSubscriber: &controllerSubscriberAdapter{sub: h.subscriber},
	}).SetupWithManager(h.mgr); err != nil {
		h.t.Fatalf("setting up session controller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.mgrCancel = cancel

	go func() {
		if err := h.mgr.Start(ctx); err != nil {
			h.logger.Error("manager stopped", "error", err)
		}
	}()

	if !h.mgr.GetCache().WaitForCacheSync(ctx) {
		h.t.Fatal("waiting for cache sync")
	}

	h.k8sClient = h.mgr.GetClient()
}

// startAPIServer boots the API server backed by envtest + real NATS.
func (h *Harness) startAPIServer() {
	h.t.Helper()

	subscriber := &natsSubscriberAdapter{sub: h.subscriber}
	handlers := apiserver.NewHandlers(h.k8sClient, subscriber, h.logger, h.namespace)
	handlers.SetPermissionPublisher(&natsPermissionPublisher{conn: h.natsConn})
	bridgeURL := h.bridgeHTTP.URL
	handlers.SetBridgeClientFactory(func(_ string) apiserver.BridgeClient {
		return bridge.NewClient(bridgeURL)
	})
	srv := apiserver.NewServer(handlers, ":0", h.logger)
	h.apiHTTP = httptest.NewServer(srv.Handler())
}

// Stop tears down all subsystems in reverse order.
func (h *Harness) Stop() {
	if h.apiHTTP != nil {
		h.apiHTTP.Close()
	}
	if h.mgrCancel != nil {
		h.mgrCancel()
	}
	if h.testEnv != nil {
		_ = h.testEnv.Stop()
	}
	if h.bridgeHTTP != nil {
		h.bridgeHTTP.Close()
	}
	if h.fakeSDK != nil {
		h.fakeSDK.Close()
	}
	if h.natsConn != nil {
		h.natsConn.Close()
	}
	if h.natsServer != nil {
		h.natsServer.Shutdown()
	}
}

// K8sClient returns the envtest Kubernetes client.
func (h *Harness) K8sClient() client.Client { return h.k8sClient }

// FakeSDK returns the programmable SDK test double.
func (h *Harness) FakeSDK() *FakeSDK { return h.fakeSDK }

// APIClient returns a client pointing at the test API server.
func (h *Harness) APIClient() *APIClient {
	return &APIClient{baseURL: h.apiHTTP.URL, http: &http.Client{}}
}

// Publisher returns the NATS event publisher.
func (h *Harness) Publisher() *events.Publisher { return h.publisher }

// Subscriber returns the NATS event subscriber.
func (h *Harness) Subscriber() *events.Subscriber { return h.subscriber }

// BridgeURL returns the bridge server's URL.
func (h *Harness) BridgeURL() string { return h.bridgeHTTP.URL }

// Namespace returns the default test namespace.
func (h *Harness) Namespace() string { return h.namespace }

// natsSubscriberAdapter adapts events.Subscriber to apiserver.EventSubscriber.
type natsSubscriberAdapter struct {
	sub *events.Subscriber
}

func (a *natsSubscriberAdapter) SubscribeSession(ctx context.Context, namespace, sessionID string, handler func(events.Event)) (apiserver.Subscription, error) {
	return a.sub.SubscribeSession(ctx, namespace, sessionID, handler)
}

// natsPermissionPublisher adapts *nats.Conn to apiserver.PermissionPublisher.
type natsPermissionPublisher struct {
	conn *nats.Conn
}

func (p *natsPermissionPublisher) Publish(subject string, data []byte) error {
	return p.conn.Publish(subject, data)
}

// controllerSubscriberAdapter adapts events.Subscriber to controller.EventSubscriber.
type controllerSubscriberAdapter struct {
	sub *events.Subscriber
}

func (a *controllerSubscriberAdapter) SubscribeSession(ctx context.Context, namespace, sessionID string, handler func(events.Event)) (controller.EventSubscription, error) {
	return a.sub.SubscribeSession(ctx, namespace, sessionID, handler)
}
