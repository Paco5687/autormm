// Command autormm-server is the central hub: agents connect to it, clients view
// the dashboard, and it relays remote-desktop sessions.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Paco5687/autormm/server"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return def
}

func main() {
	addr := flag.String("addr", env("AUTORMM_ADDR", ":8765"), "listen address")
	admin := flag.String("admin-token", env("AUTORMM_ADMIN_TOKEN", ""), "client/dashboard bearer token")
	enroll := flag.String("enroll-token", env("AUTORMM_ENROLL_TOKEN", ""), "agent enrollment token")
	secret := flag.String("secret", env("AUTORMM_SECRET", ""), "HMAC secret for session tickets")
	db := flag.String("db", env("AUTORMM_DB", ""), "SQLite path for persisted history (empty = disabled)")
	retention := flag.Duration("retention", 7*24*time.Hour, "how long to keep persisted history")
	alertCPU := flag.Float64("alert-cpu", envFloat("AUTORMM_ALERT_CPU", 90), "CPU% alert threshold (0 disables)")
	alertMem := flag.Float64("alert-mem", envFloat("AUTORMM_ALERT_MEM", 90), "memory% alert threshold (0 disables)")
	alertDisk := flag.Float64("alert-disk", envFloat("AUTORMM_ALERT_DISK", 90), "disk% alert threshold (0 disables)")
	alertFor := flag.Duration("alert-for", 2*time.Minute, "sustained duration before a resource alert fires")
	alertOffline := flag.Duration("alert-offline", time.Minute, "offline duration before an offline alert fires")
	notifyWebhook := flag.String("notify-webhook", env("AUTORMM_NOTIFY_WEBHOOK", ""), "generic JSON webhook URL for alerts")
	notifyNtfy := flag.String("notify-ntfy", env("AUTORMM_NOTIFY_NTFY", ""), "ntfy topic URL for alerts")
	notifyDiscord := flag.String("notify-discord", env("AUTORMM_NOTIFY_DISCORD", ""), "Discord webhook URL for alerts")
	tlsCert := flag.String("tls-cert", env("AUTORMM_TLS_CERT", ""), "TLS certificate file (optional)")
	tlsKey := flag.String("tls-key", env("AUTORMM_TLS_KEY", ""), "TLS key file (optional)")
	allowPublic := flag.Bool("allow-public-bind", os.Getenv("AUTORMM_ALLOW_PUBLIC_BIND") == "1", "allow binding to a public IP (dangerous — the hub must not be exposed to the internet)")
	flag.Parse()

	// Safe by default: never let the hub come up on a public address by accident.
	if err := server.CheckBindSafety(*addr, *allowPublic, func(m string) { log.Printf("WARNING: %s", m) }); err != nil {
		log.Fatalf("%v", err)
	}

	// Fill any unset admin/enroll/db from a saved config, generating + persisting
	// what's still missing so a bare `autormm-server` just works and stays stable.
	firstRun := resolveConfig(admin, enroll, db)
	if *secret == "" {
		// Stable across restarts as long as the tokens are stable.
		*secret = *admin + ":" + *enroll
	}
	if firstRun {
		welcomeBanner(*addr, *admin)
	}

	srv := server.New(server.Config{
		Addr:         *addr,
		AdminToken:   *admin,
		EnrollToken:  *enroll,
		SecretPhrase: *secret,
		OfflineAfter: 30 * time.Second,
		HistoryLen:   60,
		DBPath:       *db,
		Retention:    *retention,
		Alerts: server.AlertConfig{
			CPU: *alertCPU, Mem: *alertMem, Disk: *alertDisk,
			For: *alertFor, OfflineAfter: *alertOffline,
			Webhook: *notifyWebhook, Ntfy: *notifyNtfy, Discord: *notifyDiscord,
		},
		TLSCert: *tlsCert,
		TLSKey:  *tlsKey,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil && err.Error() != "http: Server closed" {
		log.Fatalf("server: %v", err)
	}
}
