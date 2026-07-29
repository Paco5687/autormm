//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
)

// setupFileLog redirects the standard logger to a role-specific file under the
// process's LOCALAPPDATA. For SYSTEM processes (the elevated helper and the
// console worker) that resolves to
// C:\Windows\System32\config\systemprofile\AppData\Local\autormm, so their logs
// survive even though they have no console.
func setupFileLog(role string) {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		return
	}
	dir = filepath.Join(dir, "autormm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, role+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.Printf("=== %s log start (pid %d) ===", role, os.Getpid())
}
