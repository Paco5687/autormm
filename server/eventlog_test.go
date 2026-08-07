package server

import (
	"strings"
	"testing"
)

func TestEventLogCommandPerOS(t *testing.T) {
	for _, tc := range []struct {
		os, shell string
		ok        bool
	}{
		{"linux", "sh", true}, {"darwin", "sh", true},
		{"windows", "powershell", true}, {"plan9", "", false},
	} {
		shell, cmd, ok := eventLogCommand(tc.os)
		if ok != tc.ok || (ok && (shell != tc.shell || cmd == "")) {
			t.Errorf("eventLogCommand(%q) = %q, ok=%v", tc.os, shell, ok)
		}
	}
}

// Both commands must bound their output. An unbounded log dump from a host with
// a screaming service would be megabytes over a websocket for nothing.
func TestEventLogCommandsAreBounded(t *testing.T) {
	_, unix, _ := eventLogCommand("linux")
	if !strings.Contains(unix, "-n 200") && !strings.Contains(unix, "tail") {
		t.Error("the unix log dump is unbounded")
	}
	if !strings.Contains(unix, "-b") {
		t.Error("journalctl is not limited to this boot, so a long-running host returns months of log")
	}
	_, win, _ := eventLogCommand("windows")
	if !strings.Contains(win, "MaxEvents") {
		t.Error("the windows log dump is unbounded")
	}
	// PowerShell truncates to an imagined console width unless told otherwise,
	// and the message is the column that matters.
	if !strings.Contains(win, "-Width") {
		t.Error("windows output will be truncated mid-message")
	}
}
