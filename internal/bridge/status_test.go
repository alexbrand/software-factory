package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestStatusReporterCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(SDKHealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	sdk := NewSDKClientWithHTTP(server.URL, httpClient)
	sm := NewSessionManager(sdk, logger)
	sr := NewStatusReporter(sdk, sm, logger)

	var mu sync.Mutex
	var healthyCalled bool
	var lastHealthy bool
	var sessionCountCalled bool
	var lastCount int

	sr.OnSDKHealthy(func(h bool) {
		mu.Lock()
		defer mu.Unlock()
		healthyCalled = true
		lastHealthy = h
	})
	sr.OnSessionCount(func(c int) {
		mu.Lock()
		defer mu.Unlock()
		sessionCountCalled = true
		lastCount = c
	})

	sr.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	sr.Run(ctx)

	mu.Lock()
	defer mu.Unlock()

	if !healthyCalled {
		t.Error("expected healthy callback to be called")
	}
	if !lastHealthy {
		t.Error("expected SDK to be healthy")
	}
	if !sessionCountCalled {
		t.Error("expected session count callback to be called")
	}
	if lastCount != 0 {
		t.Errorf("expected 0 sessions, got %d", lastCount)
	}
}

func TestStatusReporterUnhealthySDK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	httpClient := server.Client()
	httpClient.Timeout = 5 * time.Second
	sdk := NewSDKClientWithHTTP(server.URL, httpClient)
	sm := NewSessionManager(sdk, logger)
	sr := NewStatusReporter(sdk, sm, logger)

	var mu sync.Mutex
	var lastHealthy bool
	healthyCalled := false

	sr.OnSDKHealthy(func(h bool) {
		mu.Lock()
		defer mu.Unlock()
		healthyCalled = true
		lastHealthy = h
	})

	sr.SetInterval(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	sr.Run(ctx)

	mu.Lock()
	defer mu.Unlock()

	if !healthyCalled {
		t.Error("expected healthy callback to be called")
	}
	if lastHealthy {
		t.Error("expected SDK to be unhealthy")
	}
}
