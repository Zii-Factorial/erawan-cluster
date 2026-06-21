// Command api is the entry point for the erawan-cluster control-plane API.
//
// Its job here is intentionally tiny: establish a cancellable context that
// shuts the server down on SIGINT/SIGTERM, load all configuration from the
// environment (config.go), assemble every subsystem from that configuration
// (setup.go), and run the HTTP server (api.go). All the wiring detail lives in
// those files so that main reads as a high-level outline of start-up.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

/**
 * main is the process entry point. It establishes a cancellable context tied to
 * SIGINT/SIGTERM, loads configuration from the environment, assembles every
 * subsystem, and runs the HTTP server until the context is cancelled. On any
 * fatal start-up error it logs and exits non-zero.
 */
func main() {
	// Tie the process lifetime to OS shutdown signals: cancelling ctx triggers
	// graceful HTTP shutdown and stops in-flight cluster jobs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Resolve every tunable from the environment, once.
	cfg := loadConfig()

	// 2. Build the application graph (HAProxy + each cluster engine + crypto).
	app, err := buildApplication(ctx, cfg)
	if err != nil {
		log.Fatalf("init application: %v", err)
	}

	// 3. Mount routes and serve until the context is cancelled.
	mux := app.mount()
	log.Printf("erawan cluster api v%s started at %s", cfg.server.version, cfg.server.addr)
	if err := app.run(ctx, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
