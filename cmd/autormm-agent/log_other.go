//go:build !windows

package main

// setupFileLog is a no-op off Windows: the elevated helper and console worker
// are Windows-only, and Linux agents log to journald via the service manager.
func setupFileLog(role string) {}
