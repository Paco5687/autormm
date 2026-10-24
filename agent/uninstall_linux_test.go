//go:build linux

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points the uninstaller at a temporary machine: an empty system path
// and an empty home, so the test sees neither this developer's own agent nor
// anything left by another test.
func isolate(t *testing.T) (home string) {
	t.Helper()
	dir := t.TempDir()
	home = filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	oldUnit, oldConf := systemUnit, systemConf
	systemUnit = filepath.Join(dir, "etc-systemd", "autormm-agent.service")
	systemConf = filepath.Join(dir, "etc-autormm")
	t.Cleanup(func() { systemUnit, systemConf = oldUnit, oldConf })
	return home
}

func writeUnit(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readPlan builds the plan and returns the script it wrote, cleaning up after.
func readPlan(t *testing.T) (*uninstaller, string) {
	t.Helper()
	u, err := (&Agent{}).planUninstall()
	if err != nil {
		t.Fatalf("planUninstall: %v", err)
	}
	t.Cleanup(func() { os.Remove(u.script) })
	b, err := os.ReadFile(u.script)
	if err != nil {
		t.Fatalf("reading the script back: %v", err)
	}
	return u, string(b)
}

// An agent nobody's installer put there has no install to undo. Guessing at a
// layout and deleting by that guess is far worse than saying so and leaving
// the machine alone — the hub reports the refusal and keeps the host.
func TestUninstallRefusesWhenThereIsNoInstall(t *testing.T) {
	isolate(t)
	if _, err := (&Agent{}).planUninstall(); err == nil {
		t.Fatal("planUninstall succeeded with no unit file anywhere")
	}
}

// The per-user desktop install: everything lives under the home directory and
// every systemctl call has to carry --user, or it addresses the system manager
// and silently does nothing.
func TestUninstallUserServicePlan(t *testing.T) {
	home := isolate(t)
	unit := filepath.Join(home, userUnitRel)
	writeUnit(t, unit)

	u, script := readPlan(t)

	if u.what != "user service" {
		t.Errorf("what = %q, want %q", u.what, "user service")
	}
	for _, want := range []string{
		"systemctl --user disable --now autormm-agent",
		"systemctl --user daemon-reload",
		unit,
		filepath.Join(home, userConfRel),
		`rm -f "$0"`, // the script clears itself up last
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q\n--- script ---\n%s", want, script)
		}
	}
	// Linger belongs to the account, not to this agent, and other user
	// services may be relying on it.
	if strings.Contains(script, "disable-linger") {
		t.Error("the script turns off linger, which is not this agent's to switch off")
	}
}

// The root install addresses the system manager, so no --user anywhere: a
// stray one would send every command to a manager that has never heard of
// this unit, and the uninstall would quietly do nothing at all.
func TestUninstallSystemServicePlan(t *testing.T) {
	isolate(t)
	writeUnit(t, systemUnit)
	if os.Geteuid() != 0 {
		if _, err := (&Agent{}).planUninstall(); err == nil {
			t.Fatal("a non-root process was allowed to plan a system uninstall")
		}
		return
	}

	u, script := readPlan(t)
	if u.what != "system service" {
		t.Errorf("what = %q, want %q", u.what, "system service")
	}
	if strings.Contains(script, "--user") {
		t.Errorf("a system uninstall carries --user\n--- script ---\n%s", script)
	}
	if !strings.Contains(script, systemConf) {
		t.Errorf("the script leaves %s behind", systemConf)
	}
}

// The script must not run as a child of the agent. `systemctl disable --now`
// kills everything in the unit's cgroup, so a plain child would be killed by
// the very stop it had just issued, leaving the binary and unit file behind on
// a host the hub had already let go of. systemd-run puts it in a unit of its
// own, out of reach of that.
func TestTeardownRunsOutsideTheAgentsCgroup(t *testing.T) {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("no systemd-run on this machine")
	}
	home := isolate(t)
	writeUnit(t, filepath.Join(home, userUnitRel))

	u, _ := readPlan(t)
	args := strings.Join(u.launch.Args, " ")
	if !strings.Contains(u.launch.Path, "systemd-run") {
		t.Fatalf("launch is %q, want systemd-run", u.launch.Path)
	}
	for _, want := range []string{"--user", "--collect", "--no-block"} {
		if !strings.Contains(args, want) {
			t.Errorf("launch args missing %q: %s", want, args)
		}
	}
}

// The script is run as root on a system install. World-writable, it would be a
// way to get code executed as root out of the temp directory.
func TestTeardownScriptIsNotWorldWritable(t *testing.T) {
	home := isolate(t)
	writeUnit(t, filepath.Join(home, userUnitRel))

	u, _ := readPlan(t)
	st, err := os.Stat(u.script)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("script mode is %04o; it should not be readable or writable by anyone else", mode)
	}
}

// A home directory with a space in it is ordinary, and an unquoted path in a
// generated shell script turns one into two arguments — here, an `rm -rf` of
// something that was never meant to be touched.
func TestTeardownQuotesPathsWithSpaces(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home dir")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	oldUnit, oldConf := systemUnit, systemConf
	systemUnit = filepath.Join(dir, "none", "autormm-agent.service")
	systemConf = filepath.Join(dir, "none-conf")
	t.Cleanup(func() { systemUnit, systemConf = oldUnit, oldConf })
	writeUnit(t, filepath.Join(home, userUnitRel))

	_, script := readPlan(t)
	if !strings.Contains(script, "'"+filepath.Join(home, userConfRel)+"'") {
		t.Errorf("the config path is not quoted\n--- script ---\n%s", script)
	}
}
