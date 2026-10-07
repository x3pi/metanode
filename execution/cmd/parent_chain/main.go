package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

var (
	configPath     = flag.String("config", "config.json", "Config path")
	rustConfigPath = flag.String("rust-config", "config.json", "Rust consensus config path")
	dataDir        = flag.String("data-dir", "./data", "Data directory")
	httpAddr       = flag.String("http", ":8545", "HTTP RPC listen address")
	genesisPath    = flag.String("genesis", "", "Parent chain genesis file path (REQUIRED)")
)

func main() {
	// Ignore SIGHUP and SIGPIPE immediately to prevent background process death when launching subshell exits or pipe breaks
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)

	flag.Parse()

	if *genesisPath == "" {
		candidate1 := filepath.Join(*dataDir, "parent_genesis.json")
		if _, err := os.Stat(candidate1); err == nil {
			*genesisPath = candidate1
		} else if _, err := os.Stat("parent_genesis.json"); err == nil {
			*genesisPath = "parent_genesis.json"
		} else {
			log.Fatalf("Fatal: -genesis flag is required. You must specify the path to genesis JSON (e.g. -genesis parent_genesis.json)")
		}
	}

	log.Println("Starting Parent Chain Node...")

	app, err := NewApp(*configPath, *rustConfigPath, *dataDir, *httpAddr, *genesisPath)
	if err != nil {
		log.Fatalf("Failed to initialize app: %v", err)
	}

	if err := app.Start(); err != nil {
		log.Fatalf("Failed to start app: %v", err)
	}

	// Ignore SIGHUP and wait for termination signal
	signal.Ignore(syscall.SIGHUP)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	log.Println("Shutting down Parent Chain Node...")
	app.Stop()
}
