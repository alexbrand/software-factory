package events

import (
	"testing"
)

func TestDefaultConnectOptions(t *testing.T) {
	opts := DefaultConnectOptions("nats://localhost:4222")
	if opts.URL != "nats://localhost:4222" {
		t.Errorf("expected URL nats://localhost:4222, got %q", opts.URL)
	}
	if opts.MaxReconnects != -1 {
		t.Errorf("expected MaxReconnects -1, got %d", opts.MaxReconnects)
	}
	if opts.ReconnectWait == 0 {
		t.Error("expected non-zero ReconnectWait")
	}
	if opts.Name == "" {
		t.Error("expected non-empty Name")
	}
}

func TestConnect_EmptyURL(t *testing.T) {
	_, _, err := Connect(ConnectOptions{})
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}
