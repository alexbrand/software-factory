package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("software-factory apiserver")
	// TODO: Wire up gRPC server with REST gateway.
	// See spec/04-control-plane.md (API Server section) for endpoints.
	os.Exit(0)
}
