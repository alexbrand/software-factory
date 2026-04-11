package testharness

import (
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// startNATS boots an embedded NATS server with JetStream enabled.
// Each test gets its own server on a random port with isolated storage.
func (h *Harness) startNATS() {
	h.t.Helper()

	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random available port
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  h.t.TempDir(),
	}

	ns, err := natsserver.NewServer(opts)
	if err != nil {
		h.t.Fatalf("creating embedded NATS server: %v", err)
	}
	ns.Start()

	if !ns.ReadyForConnections(5 * time.Second) {
		h.t.Fatal("NATS server not ready within 5 seconds")
	}
	h.natsServer = ns

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		h.t.Fatalf("connecting to embedded NATS: %v", err)
	}
	h.natsConn = nc

	js, err := nc.JetStream()
	if err != nil {
		h.t.Fatalf("creating JetStream context: %v", err)
	}
	h.js = js
}

// NATSConn returns the NATS connection for direct assertions.
func (h *Harness) NATSConn() *nats.Conn { return h.natsConn }

// JetStream returns the JetStream context for direct assertions.
func (h *Harness) JetStream() nats.JetStreamContext { return h.js }

// WaitForNATSMessage subscribes to a subject and waits for a message.
// Returns the message data or fails the test on timeout.
func WaitForNATSMessage(t *testing.T, js nats.JetStreamContext, subject string, timeout time.Duration) []byte {
	t.Helper()

	sub, err := js.SubscribeSync(subject, nats.DeliverAll())
	if err != nil {
		t.Fatalf("subscribing to %s: %v", subject, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	msg, err := sub.NextMsg(timeout)
	if err != nil {
		t.Fatalf("waiting for message on %s: %v", subject, err)
	}
	return msg.Data
}
