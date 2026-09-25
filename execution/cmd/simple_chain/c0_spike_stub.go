//go:build !c0spike

package main

import (
	"fmt"
	"os"
)

func runC0Spike(mode, configPath, dataDir, outPath string, blocksCount int, isRestart bool) {
	fmt.Fprintln(os.Stderr, "Error: C0 spike is not compiled into this production binary. Build with '-tags c0spike' to enable (e.g. go run -tags c0spike ./cmd/simple_chain --tool-c0-spike=verify).")
	os.Exit(1)
}
