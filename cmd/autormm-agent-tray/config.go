// Command autormm-agent-tray is the Windows desktop agent: the same host agent
// as autormm-agent, but built as a GUI app that shows a system-tray status
// indicator and starts itself at logon. On non-Windows platforms it runs the
// agent headless (there is no tray).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Paco5687/autormm/agent"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// parseFlags builds an agent config from the same flags/env as autormm-agent.
func parseFlags() (agent.Config, error) {
	server := flag.String("server", env("AUTORMM_SERVER", ""), "server base URL, e.g. https://rmm.example.com")
	token := flag.String("token", env("AUTORMM_ENROLL_TOKEN", ""), "enrollment token")
	id := flag.String("id", env("AUTORMM_AGENT_ID", ""), "stable agent id (default: hostname)")
	tags := flag.String("tags", env("AUTORMM_TAGS", ""), "comma-separated tags")
	interval := flag.Duration("interval", 5*time.Second, "metrics interval")
	insecure := flag.Bool("insecure", os.Getenv("AUTORMM_INSECURE") == "1", "skip TLS verification (self-signed certs)")
	allowExec := flag.Bool("allow-exec", os.Getenv("AUTORMM_NO_EXEC") != "1", "permit remote command execution from the server")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(agent.Version)
		os.Exit(0)
	}
	if *server == "" || *token == "" {
		return agent.Config{}, fmt.Errorf("both --server and --token are required (or set AUTORMM_SERVER / AUTORMM_ENROLL_TOKEN)")
	}
	return agent.Config{
		Server:      *server,
		EnrollToken: *token,
		AgentID:     *id,
		Tags:        *tags,
		Interval:    *interval,
		Insecure:    *insecure,
		AllowExec:   *allowExec,
	}, nil
}
