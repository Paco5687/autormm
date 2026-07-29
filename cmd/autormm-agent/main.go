// Command autormm-agent runs on a monitored host. It connects out to the
// server, pushes telemetry, and serves remote-desktop sessions on demand.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/Paco5687/autormm/agent"
	"github.com/Paco5687/autormm/internal/selfupdate"
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
	elevated := flag.Bool("elevated", os.Getenv("AUTORMM_ELEVATED") == "1", "run as the privileged (SYSTEM/root) helper channel")
	consoleWorker := flag.Bool("console-worker", false, "internal: SYSTEM screen worker launched into the console session by the elevated helper")
	showVersion := flag.Bool("version", false, "print the version and exit")
	noUpdate := flag.Bool("no-auto-update", os.Getenv("AUTORMM_NO_AUTO_UPDATE") == "1", "disable self-update to match the hub")
	flag.Parse()

	if *showVersion {
		fmt.Println(agent.Version)
		return
	}
	if *server == "" || *token == "" {
		log.Fatal("both --server and --token are required (or set AUTORMM_SERVER / AUTORMM_ENROLL_TOKEN)")
	}

	// The elevated helper and console worker run as SYSTEM services/children with
	// no console, so their stderr is discarded. Send logs to a role-specific file
	// so they can actually be inspected.
	switch {
	case *consoleWorker:
		setupFileLog("console-worker")
	case *elevated:
		setupFileLog("elevated")
	}

	cfg := agent.Config{
		Server:      *server,
		EnrollToken: *token,
		AgentID:     *id,
		Tags:        *tags,
		Interval:    *interval,
		Insecure:    *insecure,
		AllowExec:   *allowExec,
		Elevated:    *elevated,
		Console:     *consoleWorker,
	}
	a := agent.New(cfg)

	// Keep the agent matched to the hub. Applying an update replaces the binary
	// and exits; the service manager (systemd Restart=always) relaunches it.
	updateCfg := selfupdate.Config{
		Server: cfg.Server, Token: cfg.EnrollToken, Insecure: cfg.Insecure,
		DownloadQuery:  fmt.Sprintf("os=%s&arch=%s", runtime.GOOS, runtime.GOARCH),
		CurrentVersion: agent.Version,
		Log:            log.Printf,
		Apply: func(newBinary string) error {
			if _, err := selfupdate.ReplaceRunningBinary(newBinary); err != nil {
				return err
			}
			log.Println("updated; exiting for the service manager to relaunch")
			os.Exit(0)
			return nil
		},
	}
	// The console worker is a transient child of the elevated helper, which owns
	// updates for the shared binary; a second updater would fight it.
	if !*noUpdate && !*consoleWorker {
		a.SetUpdateHook(func() {
			if err := selfupdate.CheckOnce(updateCfg); err != nil {
				log.Printf("update check: %v", err)
			}
		})
		go selfupdate.Loop(updateCfg, 20*time.Second, 6*time.Hour)
	}

	runAgent(a) // on Windows, runs under the SCM when launched as a service
}

// runInteractive runs the agent until SIGINT/SIGTERM. Used for foreground runs
// and (on Windows) when not launched by the service control manager.
func runInteractive(a *agent.Agent) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("agent: %v", err)
	}
}
