//go:build !windows

// Package procattr keeps subprocesses from flashing console windows on Windows.
package procattr

import "os/exec"

// Hide does nothing off Windows: no other platform conjures a window for a
// child process. It exists so callers need no build tags of their own.
func Hide(*exec.Cmd) {}
