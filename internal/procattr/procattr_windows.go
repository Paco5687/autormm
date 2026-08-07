//go:build windows

// Package procattr keeps subprocesses from flashing console windows on Windows.
package procattr

import (
	"os/exec"
	"syscall"
)

// createNoWindow runs a console program without allocating a console for it.
// HideWindow alone is not enough for console applications: without this flag
// Windows gives the child its own console, and a parent with no console of its
// own — which the tray agent is, being a GUI binary — means that console is a
// real window that appears on the user's desktop.
const createNoWindow = 0x08000000

// Hide suppresses the console window a child process would otherwise get.
//
// This matters far more than it sounds. The agent samples GPU metrics every few
// seconds by running nvidia-smi, and without this a console window opened and
// closed on the user's desktop every five seconds, forever. Output redirection
// is unaffected — the caller still reads stdout and stderr as normal.
func Hide(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
