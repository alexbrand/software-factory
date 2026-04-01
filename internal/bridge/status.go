package bridge

import (
	"context"
	"log/slog"
	"time"
)

// StatusReporter periodically checks the SDK health and reports status.
type StatusReporter struct {
	sdk            *SDKClient
	sessionManager *SessionManager
	logger         *slog.Logger
	interval       time.Duration

	// Callbacks for status updates — set by the bridge server.
	onSDKHealthy   func(bool)
	onSessionCount func(int)
}

// NewStatusReporter creates a new status reporter.
func NewStatusReporter(sdk *SDKClient, sm *SessionManager, logger *slog.Logger) *StatusReporter {
	return &StatusReporter{
		sdk:            sdk,
		sessionManager: sm,
		logger:         logger,
		interval:       10 * time.Second,
	}
}

// SetInterval sets the reporting interval.
func (r *StatusReporter) SetInterval(d time.Duration) {
	r.interval = d
}

// OnSDKHealthy sets a callback that is invoked with the SDK health state.
func (r *StatusReporter) OnSDKHealthy(fn func(bool)) {
	r.onSDKHealthy = fn
}

// OnSessionCount sets a callback that is invoked with the active session count.
func (r *StatusReporter) OnSessionCount(fn func(int)) {
	r.onSessionCount = fn
}

// Run starts the periodic status reporting loop. It blocks until ctx is cancelled.
func (r *StatusReporter) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Do an initial check immediately.
	r.check(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.check(ctx)
		}
	}
}

func (r *StatusReporter) check(ctx context.Context) {
	// Check SDK health.
	_, err := r.sdk.GetHealth(ctx)
	healthy := err == nil

	if r.onSDKHealthy != nil {
		r.onSDKHealthy(healthy)
	}

	if !healthy {
		r.logger.Warn("SDK health check failed", "error", err)
	}

	// Report session count.
	count := r.sessionManager.ActiveSessionCount()
	if r.onSessionCount != nil {
		r.onSessionCount(count)
	}
}
