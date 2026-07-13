// Command autormm-agent runs on a monitored host. It connects out to the
// server, pushes telemetry, and serves remote-desktop sessions on demand.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Paco5687/autormm/agent"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	server := flag.String("server", env("AUTORMM_SERVER", ""), "server base URL, e.g. https://rmm.example.com")
	token := flag.String("token", env("AUTORMM_ENROLL_TOKEN", ""), "enrollment token")
	id := flag.String("id", env("AUTORMM_AGENT_ID", ""), "stable agent id (default: hostname)")
	tags := flag.String("tags", env("AUTORMM_TAGS", ""), "comma-separated tags")
	interval := flag.Duration("interval", 5*time.Second, "metrics interval")
	insecure := flag.Bool("insecure", os.Getenv("AUTORMM_INSECURE") == "1", "skip TLS verification (self-signed certs)")
	allowExec := flag.Bool("allow-exec", os.Getenv("AUTORMM_NO_EXEC") != "1", "permit remote command execution from the server")
	flag.Parse()

	if *server == "" || *token == "" {
		log.Fatal("both --server and --token are required (or set AUTORMM_SERVER / AUTORMM_ENROLL_TOKEN)")
	}

	a := agent.New(agent.Config{
		Server:      *server,
		EnrollToken: *token,
		AgentID:     *id,
		Tags:        *tags,
		Interval:    *interval,
		Insecure:    *insecure,
		AllowExec:   *allowExec,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("agent: %v", err)
	}
}
