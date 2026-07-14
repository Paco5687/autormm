//go:build windows

package main

import (
	"context"
	_ "embed"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"

	"fyne.io/systray"

	"github.com/Paco5687/autormm/agent"
	"github.com/Paco5687/autormm/internal/selfupdate"
)

//go:embed icon_on.ico
var iconOn []byte

//go:embed icon_off.ico
var iconOff []byte

var (
	trayAgent *agent.Agent
	trayCfg   agent.Config
	mStatus   *systray.MenuItem
	ready     atomic.Bool
	connected atomic.Bool
)

func main() {
	setupLogFile() // GUI app has no console -- keep a log on disk
	cleanupOldBinary()

	cfg, err := parseFlags()
	if err != nil {
		log.Fatalf("%v", err)
	}
	trayCfg = cfg

	// Start at logon (per-user Run key; idempotent, no admin required).
	if err := ensureAutostart(); err != nil {
		log.Printf("autostart registration failed: %v", err)
	}

	// Keep the agent matched to the hub (self-update on startup + periodically,
	// or immediately when the hub pushes an update).
	go autoUpdateLoop(cfg)

	trayAgent = agent.New(cfg)
	trayAgent.SetStatusHook(onStatus)
	trayAgent.SetUpdateHook(func() {
		if err := selfupdate.CheckOnce(updaterConfig(cfg)); err != nil {
			log.Printf("update check: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := trayAgent.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("agent stopped: %v", err)
		}
	}()

	// systray.Run blocks on the main goroutine until the process is killed.
	systray.Run(onReady, func() {})
}

func onReady() {
	systray.SetIcon(iconOff)
	systray.SetTooltip("autormm: connecting…")

	host := systray.AddMenuItem("autormm -- "+trayAgent.Hostname(), "")
	host.Disable()
	mStatus = systray.AddMenuItem("Connecting…", "Connection status")
	mStatus.Disable()
	systray.AddSeparator()
	refresh := systray.AddMenuItem("Refresh status", "Reconnect to the hub now")
	go func() {
		for range refresh.ClickedCh {
			trayAgent.Refresh()
		}
	}()
	update := systray.AddMenuItem("Update to latest", "Download the latest agent from the hub and restart")
	go func() {
		for range update.ClickedCh {
			go func() {
				if err := selfupdate.CheckOnce(updaterConfig(trayCfg)); err != nil {
					log.Printf("update failed: %v", err)
				}
			}()
		}
	}()
	// No Quit item on purpose: the agent isn't meant to be closed from the tray.

	if agent.Version != "dev" {
		v := systray.AddMenuItem("Version "+agent.Version, "")
		v.Disable()
	}

	ready.Store(true)
	applyStatus(connected.Load()) // reflect any state that arrived before the menu existed
}

// onStatus is the agent's connection-state callback (any goroutine).
func onStatus(isConnected bool) {
	connected.Store(isConnected)
	if ready.Load() {
		applyStatus(isConnected)
	}
}

func applyStatus(isConnected bool) {
	if isConnected {
		systray.SetIcon(iconOn)
		systray.SetTooltip("autormm: connected to hub")
		if mStatus != nil {
			mStatus.SetTitle("● Connected to hub")
		}
		return
	}
	systray.SetIcon(iconOff)
	systray.SetTooltip("autormm: disconnected -- retrying")
	if mStatus != nil {
		mStatus.SetTitle("○ Disconnected -- retrying")
	}
}

func setupLogFile() {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "autormm")
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		log.SetOutput(f)
	}
}
