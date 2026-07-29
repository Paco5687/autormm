//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
)

// setupFileLog redirects the standard logger to a role-specific file, so the
// elevated helper and console worker (which have no console) leave a trail.
//
// It writes next to the executable — C:\ProgramData\autormm — rather than under
// LOCALAPPDATA: the console worker inherits the SYSTEM helper's environment
// (LOCALAPPDATA points at the systemprofile) but runs as the signed-in user, who
// can't write there. ProgramData is created by the installer and readable by
// everyone, so the logs are both writable by the worker and easy to fetch.
func setupFileLog(role string) {
	var dir string
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	} else if la := os.Getenv("LOCALAPPDATA"); la != "" {
		dir = filepath.Join(la, "autormm")
	} else {
		return
	}
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
