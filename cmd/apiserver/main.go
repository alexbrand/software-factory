package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1alpha1 "github.com/alexbrand/software-factory/api/v1alpha1"
	"github.com/alexbrand/software-factory/internal/apiserver"
	"github.com/alexbrand/software-factory/pkg/events"
)

func main() {
	var (
		addr      string
		namespace string
		natsURL   string
	)
	flag.StringVar(&addr, "addr", ":8080", "HTTP listen address")
	flag.StringVar(&namespace, "namespace", "default", "Kubernetes namespace to operate in")
	flag.StringVar(&natsURL, "nats-url", "", "NATS server URL (optional, enables event streaming)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(factoryv1alpha1.AddToScheme(scheme))

	config, err := ctrl.GetConfig()
	if err != nil {
		logger.Error("getting kubeconfig", "error", err)
		os.Exit(1)
	}

	// The apiserver only needs a cached client to read CRs — no controllers,
	// so we create a cache and client directly instead of a full manager.
	informerCache, err := cache.New(config, cache.Options{Scheme: scheme})
	if err != nil {
		logger.Error("creating cache", "error", err)
		os.Exit(1)
	}

	k8sClient, err := client.New(config, client.Options{Scheme: scheme, Cache: &client.CacheOptions{Reader: informerCache}})
	if err != nil {
		logger.Error("creating client", "error", err)
		os.Exit(1)
	}

	// Set up event subscriber if NATS URL is provided.
	var subscriber apiserver.EventSubscriber
	if natsURL != "" {
		opts := events.DefaultConnectOptions(natsURL)
		opts.Name = "apiserver"
		nc, js, natsErr := events.Connect(opts)
		if natsErr != nil {
			logger.Error("connecting to NATS", "error", natsErr)
			os.Exit(1)
		}
		defer nc.Close()
		subscriber = newNATSSubscriber(events.NewSubscriber(js))
	}

	// Set up signal handling.
	ctx := ctrl.SetupSignalHandler()

	// Start the informer cache in the background.
	go func() {
		if startErr := informerCache.Start(ctx); startErr != nil {
			logger.Error("cache failed", "error", startErr)
			os.Exit(1)
		}
	}()

	// Wait for cache to sync.
	if !informerCache.WaitForCacheSync(ctx) {
		logger.Error("cache sync failed")
		os.Exit(1)
	}

	handlers := apiserver.NewHandlers(k8sClient, subscriber, logger, namespace)
	srv := apiserver.NewServer(handlers, addr, logger)

	logger.Info("starting apiserver", "addr", addr, "namespace", namespace)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// natsSubscriberAdapter adapts events.Subscriber to the apiserver.EventSubscriber interface.
type natsSubscriberAdapter struct {
	sub *events.Subscriber
}

func newNATSSubscriber(sub *events.Subscriber) *natsSubscriberAdapter {
	return &natsSubscriberAdapter{sub: sub}
}

func (a *natsSubscriberAdapter) SubscribeSession(ctx context.Context, namespace, sessionID string, handler func(events.Event)) (apiserver.Subscription, error) {
	return a.sub.SubscribeSession(ctx, namespace, sessionID, handler)
}
