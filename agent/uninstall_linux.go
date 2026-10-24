//go:build linux

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The two shapes the hub's installer leaves behind: a root-owned system
// service, and a per-user one for a desktop that shares its screen.
const (
	userUnitRel = ".config/systemd/user/autormm-agent.service"
	userConfRel = ".config/autormm"
	unitName    = "autormm-agent"
)

// Variables rather than constants so a test can point them somewhere it is
// allowed to write. Nothing else reassigns them.
var (
	systemUnit = "/etc/systemd/system/autormm-agent.service"
	systemConf = "/etc/autormm"
)

// planUninstall works out which of the two installs this is and writes the
// script that undoes it.
//
// Which one it is cannot be read off the user id alone — a desktop agent may
// well be running as a user who can also sudo — so it is decided by the unit
// file that actually exists, and the euid only settles a machine that somehow
// has both.
func (a *Agent) planUninstall() (*uninstaller, error) {
	home, _ := os.UserHomeDir()
	userUnit, userConf := "", ""
	if home != "" {
		userUnit = filepath.Join(home, userUnitRel)
		userConf = filepath.Join(home, userConfRel)
	}

	hasSystem := fileExists(systemUnit)
	hasUser := userUnit != "" && fileExists(userUnit)

	var system bool
	switch {
	case hasSystem && hasUser:
		system = os.Geteuid() == 0 // both present: whichever this process could own
	case hasSystem:
		system = true
	case hasUser:
		system = false
	default:
		return nil, fmt.Errorf("no autormm-agent service found (looked for %s and %s); "+
			"this agent was not installed by the hub's installer, so there is no install to undo",
			systemUnit, userUnit)
	}

	if system && os.Geteuid() != 0 {
		return nil, fmt.Errorf("the agent is installed as a system service but this process is not root")
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot locate this agent's own binary: %w", err)
	}

	unit, conf, userFlag, what := systemUnit, systemConf, "", "system service"
	if !system {
		unit, conf, userFlag, what = userUnit, userConf, " --user", "user service"
	}

	// Note what is deliberately left alone: `loginctl enable-linger`, which the
	// desktop installer turns on. Linger is a property of the account, not of
	// this agent, and other user services may be relying on it — switching it
	// off on the way out could stop something unrelated.
	script := fmt.Sprintf(`#!/bin/sh
# autormm agent self-uninstall. Written and launched by the agent itself.
sleep %d
systemctl%s disable --now %s >/dev/null 2>&1
rm -f %s
systemctl%s daemon-reload >/dev/null 2>&1
rm -rf %s
rm -f %s
rm -f "$0"
`, int(delayBeforeTeardown.Seconds()), userFlag, unitName,
		shellQuote(unit), userFlag, shellQuote(conf), shellQuote(exe))

	path, err := writeScript(script, "autormm-uninstall-*.sh")
	if err != nil {
		return nil, err
	}

	return &uninstaller{script: path, launch: launchDetached(path, system), what: what}, nil
}

// launchDetached builds the command that runs the script somewhere this
// process's death cannot follow it.
//
// systemd-run is not a convenience here, it is the whole trick. A plain child
// process stays in the agent's own cgroup, and `systemctl disable --now` kills
// every process in that cgroup — so the script would stop the service and then
// be killed by the stop it had just asked for, leaving the unit file, the
// binary and the config behind on a host the hub had already let go of. A
// transient unit of its own puts the script out of reach of that.
func launchDetached(path string, system bool) *exec.Cmd {
	if run, err := exec.LookPath("systemd-run"); err == nil {
		args := []string{}
		if !system {
			args = append(args, "--user")
		}
		// --collect so the transient unit does not linger in a failed state if
		// the script exits non-zero; --no-block so this returns at once.
		args = append(args, "--collect", "--no-block",
			"--unit", "autormm-uninstall",
			"--description", "autormm agent self-uninstall",
			"/bin/sh", path)
		return exec.Command(run, args...)
	}
	// No systemd-run: fall back to setsid, which at least detaches the session.
	// It does not escape the cgroup, so on a systemd host this is the weaker
	// path — but a host without systemd-run very likely has no unit to stop
	// either, and then there is nothing to be killed by.
	if sid, err := exec.LookPath("setsid"); err == nil {
		return exec.Command(sid, "/bin/sh", path)
	}
	return exec.Command("/bin/sh", path)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// shellQuote wraps a path for /bin/sh. Paths here come from the installer and
// from os.Executable(), not from the hub, but a home directory with a space in
// it is ordinary enough to be worth not breaking on.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
