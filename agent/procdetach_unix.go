//go:build !windows

package agent

import "syscall"

// detachedSysProcAttr starts the relaunched process in its own session so it
// outlives the agent.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
