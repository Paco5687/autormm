//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/Paco5687/autormm/agent"
	"github.com/Paco5687/autormm/internal/selfupdate"
)

func updaterConfig(cfg agent.Config) selfupdate.Config {
	return selfupdate.Config{
		Server:         cfg.Server,
		Token:          cfg.EnrollToken,
		Insecure:       cfg.Insecure,
		DownloadQuery:  "os=windows&arch=amd64&kind=tray",
		CurrentVersion: agent.Version,
		Log:            log.Printf,
		Apply:          applyAndRelaunch,
	}
}

// applyAndRelaunch swaps the validated new binary in for the running tray and
// relaunches it. The binary was already smoke-tested, so a start failure is
// unlikely — but roll back if it happens.
func applyAndRelaunch(newBinary string) error {
	restore, err := selfupdate.ReplaceRunningBinary(newBinary)
	if err != nil {
		return err
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, os.Args[1:]...)
	if err := cmd.Start(); err != nil {
		restore()
		return err
	}
	log.Printf("updated -- relaunching")
	os.Exit(0)
	return nil
}

func autoUpdateLoop(cfg agent.Config) {
	selfupdate.Loop(updaterConfig(cfg), 15*time.Second, 6*time.Hour)
}

// cleanupOldBinary removes the rollback copy left by a previous update.
func cleanupOldBinary() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".old")
	}
}
