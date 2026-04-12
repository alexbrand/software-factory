package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alexbrand/software-factory/internal/bridge"
	"github.com/alexbrand/software-factory/pkg/events"
)

func main() {
	var (
		addr         = flag.String("addr", ":8080", "bridge HTTP server listen address")
		sdkURL       = flag.String("sdk-url", "http://localhost:2468", "Sandbox Agent SDK base URL")
		natsURL      = flag.String("nats-url", "", "NATS server URL (optional)")
		proxyAddr    = flag.String("proxy-addr", ":8888", "credential proxy listen address")
		secretDir    = flag.String("secret-dir", "/var/run/secrets/sandbox", "directory containing mounted secrets")
		sandboxName  = flag.String("sandbox-name", "", "name of the sandbox this bridge runs in")
		namespace    = flag.String("namespace", "default", "Kubernetes namespace")
	)
	flag.Parse()

	// Read from environment if flags not set.
	if envSDK := os.Getenv("SDK_HOST"); envSDK != "" {
		sdkPort := os.Getenv("SDK_PORT")
		if sdkPort == "" {
			sdkPort = "2468"
		}
		u := fmt.Sprintf("http://%s:%s", envSDK, sdkPort)
		sdkURL = &u
	}
	if envNATS := os.Getenv("NATS_URL"); envNATS != "" {
		natsURL = &envNATS
	}
	if envName := os.Getenv("SANDBOX_NAME"); envName != "" {
		sandboxName = &envName
	}
	if envNS := os.Getenv("SANDBOX_NAMESPACE"); envNS != "" {
		namespace = &envNS
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("starting bridge sidecar",
		"addr", *addr,
		"sdkURL", *sdkURL,
		"sandboxName", *sandboxName,
		"namespace", *namespace,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Create SDK client.
	sdkClient := bridge.NewSDKClient(*sdkURL)

	// Create session manager.
	sessionManager := bridge.NewSessionManager(sdkClient, logger)

	// Set up event forwarding if NATS is configured.
	var eventForwarder *bridge.EventForwarder
	if *natsURL != "" {
		opts := events.DefaultConnectOptions(*natsURL)
		opts.Name = fmt.Sprintf("bridge-%s", *sandboxName)
		conn, js, err := events.Connect(opts)
		if err != nil {
			logger.Warn("NATS unavailable, events will not be published", "error", err)
		} else {
			defer conn.Close()
			logger.Info("connected to NATS", "url", *natsURL)

			publisher := events.NewPublisher(js)
			if err := publisher.EnsureStream(ctx, events.DefaultStreamName); err != nil {
				logger.Warn("NATS stream setup failed, events will not be published", "error", err)
			} else {
				eventForwarder = bridge.NewEventForwarder(publisher, *namespace, logger)
			}
		}
	}

	// Create and start credential proxy.
	credProxy := bridge.NewCredentialProxy(logger)

	// Load credentials from secret mount if directory exists.
	if info, err := os.Stat(*secretDir); err == nil && info.IsDir() {
		logger.Info("secret directory found, credentials will be loaded on demand", "dir", *secretDir)
	}
	_ = credProxy
	_ = secretDir

	go func() {
		if err := credProxy.Start(*proxyAddr); err != nil {
			logger.Error("credential proxy stopped", "error", err)
		}
	}()

	// Create status reporter.
	statusReporter := bridge.NewStatusReporter(sdkClient, sessionManager, logger)

	// Create bridge server.
	server := bridge.NewServer(sessionManager, eventForwarder, logger)

	// Wire status reporter callbacks.
	statusReporter.OnSDKHealthy(server.SetSDKHealthy)

	// Start status reporter in background.
	go statusReporter.Run(ctx)

	// Start HTTP server (blocks until shutdown).
	go func() {
		if err := server.Start(*addr); err != nil {
			logger.Error("bridge server stopped", "error", err)
			cancel()
		}
	}()

	// Wait for shutdown signal.
	<-ctx.Done()
	logger.Info("shutting down bridge sidecar")

	_ = server.Shutdown()
	_ = credProxy.Shutdown()
}
