//go:build !windows

package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/Paco5687/autormm/agent"
)

// On non-Windows platforms there is no system tray; run the agent headless so
// the binary is still usable (mainly for local testing of the tray build).
func main() {
	cfg, err := parseFlags()
	if err != nil {
		log.Fatal(err)
	}
	log.Println("autormm-agent-tray: no system tray on this platform -- running headless")
	a := agent.New(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("agent: %v", err)
	}
}
