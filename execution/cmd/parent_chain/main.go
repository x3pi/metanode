package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var (
	configPath     = flag.String("config", "config.json", "Config path")
	rustConfigPath = flag.String("rust-config", "config.json", "Rust consensus config path")
	dataDir        = flag.String("data-dir", "./data", "Data directory")
)

func main() {
	flag.Parse()

	log.Println("Starting Parent Chain Node...")

	app, err := NewApp(*configPath, *rustConfigPath, *dataDir)
	if err != nil {
		log.Fatalf("Failed to initialize app: %v", err)
	}

	if err := app.Start(); err != nil {
		log.Fatalf("Failed to start app: %v", err)
	}

	// Wait for termination signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	log.Println("Shutting down Parent Chain Node...")
	app.Stop()
}
