package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("software-factory controller-manager")
	// TODO: Wire up controller-runtime manager with all controllers.
	// See spec/04-control-plane.md for controller behaviors.
	os.Exit(0)
}
