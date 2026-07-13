//go:build windows

package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Paco5687/autormm/agent"
)

func updateHTTP(insecure bool) *http.Client {
	c := &http.Client{Timeout: 90 * time.Second}
	if insecure {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return c
}

// hubVersion returns the version the hub was built at.
func hubVersion(cfg agent.Config) (string, error) {
	resp, err := updateHTTP(cfg.Insecure).Get(strings.TrimRight(cfg.Server, "/") + "/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hub /version: %s", resp.Status)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// selfUpdate downloads the current tray binary from the hub, swaps it in for the
// running exe (Windows allows renaming a running image), and relaunches. It does
// not return on success — the process exits so the new binary takes over.
func selfUpdate(cfg agent.Config) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dl := strings.TrimRight(cfg.Server, "/") +
		"/download/agent?os=windows&arch=amd64&kind=tray&token=" + url.QueryEscape(cfg.EnrollToken)
	resp, err := updateHTTP(cfg.Insecure).Get(dl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: %s", resp.Status)
	}

	newPath, oldPath := exe+".new", exe+".old"
	f, err := os.OpenFile(newPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(newPath)
		return err
	}
	f.Close()

	os.Remove(oldPath)
	if err := os.Rename(exe, oldPath); err != nil { // move the running exe aside
		os.Remove(newPath)
		return err
	}
	if err := os.Rename(newPath, exe); err != nil { // put the new one in place
		os.Rename(oldPath, exe) // roll back
		return err
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	log.Printf("updated to the hub's version — relaunching")
	os.Exit(0)
	return nil
}

// cleanupOldBinary removes leftovers from a previous self-update.
func cleanupOldBinary() {
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe + ".old")
		os.Remove(exe + ".new")
	}
}

// autoUpdateLoop keeps the agent matched to the hub: it checks the hub version
// shortly after logon and periodically after, self-updating when they differ.
func autoUpdateLoop(cfg agent.Config) {
	time.Sleep(15 * time.Second) // let the network settle at logon
	for {
		if hv, err := hubVersion(cfg); err == nil && hv != "" && hv != agent.Version {
			log.Printf("hub is %s, agent is %s — self-updating", hv, agent.Version)
			if err := selfUpdate(cfg); err != nil {
				log.Printf("auto-update failed: %v", err)
			}
		}
		time.Sleep(6 * time.Hour)
	}
}
