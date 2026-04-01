package events

import (
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// DefaultStreamName is the name of the JetStream stream for events.
const DefaultStreamName = "EVENTS"

// ConnectOptions configures the NATS connection.
type ConnectOptions struct {
	// URL is the NATS server URL (e.g., "nats://localhost:4222").
	URL string

	// MaxReconnects is the maximum number of reconnect attempts (-1 for unlimited).
	MaxReconnects int

	// ReconnectWait is the delay between reconnect attempts.
	ReconnectWait time.Duration

	// Name is an optional connection name for debugging.
	Name string
}

// DefaultConnectOptions returns sensible defaults for connecting to NATS.
func DefaultConnectOptions(url string) ConnectOptions {
	return ConnectOptions{
		URL:           url,
		MaxReconnects: -1, // unlimited
		ReconnectWait: 2 * time.Second,
		Name:          "software-factory",
	}
}

// Connect establishes a connection to NATS and returns a JetStream context.
func Connect(opts ConnectOptions) (*nats.Conn, nats.JetStreamContext, error) {
	if opts.URL == "" {
		return nil, nil, fmt.Errorf("connecting to NATS: URL is required")
	}

	natsOpts := []nats.Option{
		nats.MaxReconnects(opts.MaxReconnects),
		nats.ReconnectWait(opts.ReconnectWait),
		nats.RetryOnFailedConnect(true),
	}
	if opts.Name != "" {
		natsOpts = append(natsOpts, nats.Name(opts.Name))
	}

	nc, err := nats.Connect(opts.URL, natsOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to NATS at %s: %w", opts.URL, err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("creating JetStream context: %w", err)
	}

	return nc, js, nil
}
