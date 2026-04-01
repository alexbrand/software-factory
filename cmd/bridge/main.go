package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("software-factory bridge sidecar")
	// TODO: Wire up bridge sidecar with SDK client, NATS, and credential proxy.
	// See spec/06-agent-adapter.md for the bridge architecture.
	os.Exit(0)
}
