//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// setupFileLog redirects the standard logger to a role-specific file, so the
// elevated helper and console worker (which have no console) leave a trail.
//
// It writes next to the executable — C:\ProgramData\autormm — rather than under
// LOCALAPPDATA, which for these SYSTEM-launched roles points into the
// systemprofile and is awkward to read back from an ordinary terminal.
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
	// Tag every line with the pid: the roles share a log directory, and a
	// duplicated (orphaned) worker is otherwise invisible in interleaved output.
	log.SetPrefix(fmt.Sprintf("[%d] ", os.Getpid()))
	log.Printf("=== %s log start ===", role)
}
