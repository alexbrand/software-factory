package main

import (
	"context"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/controller"
	"github.com/alexbrand/software-factory/pkg/events"
)

// subscriberAdapter adapts events.Subscriber to controller.EventSubscriber.
type subscriberAdapter struct {
	sub *events.Subscriber
}

func (a *subscriberAdapter) SubscribeSession(ctx context.Context, namespace, sessionID string, handler func(events.Event)) (controller.EventSubscription, error) {
	return a.sub.SubscribeSession(ctx, namespace, sessionID, handler)
}

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(factoryv1alpha1.AddToScheme(scheme))
}

func main() {
	os.Exit(run())
}

func run() int {
	opts := zap.Options{Development: true}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		return 1
	}

	if err := (&controller.PoolReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Pool")
		return 1
	}

	if err := (&controller.SandboxReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Sandbox")
		return 1
	}

	if err := (&controller.WorkflowReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Workflow")
		return 1
	}

	if err := (&controller.TaskReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Task")
		return 1
	}

	// Connect to NATS for session event publishing and subscribing.
	var eventPublisher *events.Publisher
	var eventSubscriber *events.Subscriber
	natsURL := os.Getenv("NATS_URL")
	if natsURL != "" {
		opts := events.DefaultConnectOptions(natsURL)
		opts.Name = "controller-manager"
		conn, js, natsErr := events.Connect(opts)
		if natsErr != nil {
			setupLog.Info("NATS unavailable, session events disabled", "error", natsErr)
		} else {
			defer conn.Close()
			eventPublisher = events.NewPublisher(js)
			eventSubscriber = events.NewSubscriber(js)
			setupLog.Info("connected to NATS", "url", natsURL)
		}
	}

	if err := (&controller.SessionReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		EventPublisher:  eventPublisher,
		EventSubscriber: func() controller.EventSubscriber {
			if eventSubscriber != nil {
				return &subscriberAdapter{sub: eventSubscriber}
			}
			return nil
		}(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Session")
		return 1
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		return 1
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		return 1
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		return 1
	}
	return 0
}
