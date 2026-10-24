//go:build windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Paco5687/autormm/internal/procattr"
)

// What the hub's two Windows installers leave behind.
//
//   - the desktop agent: a tray executable under %LOCALAPPDATA%\autormm,
//     started at logon by an HKCU Run value it writes itself. No admin, no
//     service.
//   - the elevated helper: autormm-agent-svc.exe under %ProgramData%\autormm,
//     registered as the LocalSystem service "autormm-elevated".
//
// A host can have both at once, and they are separate connections to the hub,
// so each is asked to remove itself and each removes only its own half. That
// is also the only arrangement that can work: the Run value lives in the
// *user's* registry hive, which the SYSTEM service cannot see, while the
// service can only be deleted by something with admin rights, which the tray
// app does not have.
const (
	elevatedService = "autormm-elevated"
	runKeyPath      = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue        = "autormm-agent"
)

// planUninstall writes the batch file that removes whichever half this is.
func (a *Agent) planUninstall() (*uninstaller, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate this agent's own binary: %w", err)
	}
	dir := filepath.Dir(exe)
	if !ownedDir(dir) {
		return nil, fmt.Errorf("this agent runs from %s, which is not one of the directories "+
			"the hub's installer uses; it was installed some other way, so there is no install to undo", dir)
	}

	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString("rem autormm agent self-uninstall. Written and launched by the agent itself.\r\n")
	// timeout rather than ping: it is the documented way to sleep in a batch
	// file, and /nobreak stops a stray keypress cutting the wait short.
	fmt.Fprintf(&b, "timeout /t %d /nobreak >nul 2>&1\r\n", int(delayBeforeTeardown.Seconds()))

	what := "tray agent"
	if a.cfg.Elevated {
		what = "elevated helper"
		// Stop before delete: sc delete on a running service only marks it for
		// deletion, and it would come back on the next boot.
		fmt.Fprintf(&b, "sc stop %s >nul 2>&1\r\n", elevatedService)
		fmt.Fprintf(&b, "timeout /t 2 /nobreak >nul 2>&1\r\n")
		fmt.Fprintf(&b, "sc delete %s >nul 2>&1\r\n", elevatedService)
	} else {
		// The tray app's own autostart entry, in this user's hive.
		fmt.Fprintf(&b, "reg delete \"HKCU\\%s\" /v %s /f >nul 2>&1\r\n", runKeyPath, runValue)
	}

	// Kill last, and from the script rather than from the agent: Windows will
	// not let a running image be deleted, so the executable can only go after
	// the process holding it has gone.
	fmt.Fprintf(&b, "taskkill /f /im %s >nul 2>&1\r\n", filepath.Base(exe))
	b.WriteString("timeout /t 2 /nobreak >nul 2>&1\r\n")
	fmt.Fprintf(&b, "rmdir /s /q \"%s\" >nul 2>&1\r\n", dir)
	// A batch file can delete itself: cmd reads it a line at a time and never
	// comes back for more after the last one.
	b.WriteString("del /f /q \"%~f0\" >nul 2>&1\r\n")

	path, err := writeScript(b.String(), "autormm-uninstall-*.cmd")
	if err != nil {
		return nil, err
	}

	// Detached from this process's console and from its lifetime: /b keeps it
	// windowless, and `start` means cmd returns as soon as the child is going.
	cmd := exec.Command("cmd.exe", "/c", "start", "", "/b", "cmd.exe", "/c", path)
	// Without this the launcher gets a console of its own, and since the tray
	// agent is a GUI binary with no console to inherit, that console is a real
	// window appearing on the user's desktop as they unenroll the machine.
	procattr.Hide(cmd)
	return &uninstaller{script: path, launch: cmd, what: what}, nil
}

// ownedDir reports whether a path is one of the directories the hub's
// installers write to. It is a guard against a hand-placed agent taking its
// whole parent directory with it — rmdir /s is not something to point at a
// directory chosen by accident.
func ownedDir(dir string) bool {
	for _, base := range []string{os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramData")} {
		if base == "" {
			continue
		}
		if strings.EqualFold(filepath.Clean(dir), filepath.Clean(filepath.Join(base, "autormm"))) {
			return true
		}
	}
	return false
}
