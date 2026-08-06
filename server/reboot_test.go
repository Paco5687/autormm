package server

import (
	"strings"
	"testing"
)

func TestRebootCommandPerOS(t *testing.T) {
	for _, tc := range []struct {
		os, shell string
		ok        bool
	}{
		{os: "linux", shell: "sh", ok: true},
		{os: "darwin", shell: "sh", ok: true},
		{os: "windows", shell: "powershell", ok: true},
		{os: "freebsd", ok: false},
		{os: "", ok: false},
	} {
		shell, cmd, ok := rebootCommand(tc.os)
		if ok != tc.ok {
			t.Errorf("rebootCommand(%q) ok = %v, want %v", tc.os, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if shell != tc.shell {
			t.Errorf("rebootCommand(%q) shell = %q, want %q", tc.os, shell, tc.shell)
		}
		if cmd == "" {
			t.Errorf("rebootCommand(%q) returned an empty command", tc.os)
		}
	}
}

// The reply has to reach the hub before the machine goes away, or every
// successful reboot is reported as a failed command. On Unix that means
// backgrounding with a delay; on Windows, shutdown.exe's own timer.
func TestRebootCommandsReturnBeforeTheHostGoesDown(t *testing.T) {
	_, unix, _ := rebootCommand("linux")
	if !strings.Contains(unix, "sleep 2") || !strings.Contains(unix, "&") {
		t.Error("the unix reboot does not defer, so the host may drop the connection before replying")
	}
	_, win, _ := rebootCommand("windows")
	if !strings.Contains(win, "/t ") {
		t.Error("the windows reboot has no delay, so it may cut the reply off")
	}
}

// Someone may be sitting at the machine. Windows can warn them; say something
// useful when it does.
func TestWindowsRebootWarnsTheUser(t *testing.T) {
	_, win, _ := rebootCommand("windows")
	if !strings.Contains(win, "/c ") {
		t.Error("no message is shown to a signed-in user")
	}
	if !strings.Contains(strings.ToLower(win), "autormm") {
		t.Error("the warning does not say what is rebooting the machine")
	}
}

// An unprivileged agent frequently cannot reboot at all. It must say so rather
// than exiting zero and leaving the dashboard reporting a reboot that never
// happened.
func TestUnixRebootExplainsWhenItLacksPrivilege(t *testing.T) {
	_, unix, _ := rebootCommand("linux")
	if !strings.Contains(unix, "exit 1") {
		t.Error("the unprivileged path does not fail, so a no-op looks like success")
	}
	if !strings.Contains(unix, "sudo") && !strings.Contains(unix, "elevated helper") {
		t.Error("the failure message does not tell the operator how to fix it")
	}
}
