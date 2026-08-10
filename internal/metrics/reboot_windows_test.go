//go:build windows

package metrics

import (
	"strings"
	"testing"
)

// PendingFileRenameOperations must never be treated as a pending reboot.
//
// It is the signal everyone reaches for first, and it is nearly useless for
// this: browser updaters, antivirus definitions and any installer queueing a
// file swap all write it during normal operation. It clears at boot and
// repopulates within minutes, so a chip driven by it is permanently on — which
// is precisely what happened, on two freshly-rebooted machines, until a user
// reported it. Windows itself never tells anyone to restart on account of it.
func TestPendingFileRenameIsNotASignal(t *testing.T) {
	for _, s := range rebootSignals {
		if strings.Contains(s.value, "PendingFileRename") {
			t.Errorf("PendingFileRenameOperations is back in the signal list (%q); "+
				"it is present on healthy machines and pins the chip on forever", s.reason)
		}
	}
}

// Every signal must explain itself, or a stuck chip is undiagnosable without
// regedit.
func TestEverySignalHasAReason(t *testing.T) {
	if len(rebootSignals) == 0 {
		t.Fatal("no reboot signals are checked at all")
	}
	for _, s := range rebootSignals {
		if strings.TrimSpace(s.reason) == "" {
			t.Errorf("signal %q has no reason text", s.path)
		}
		if !strings.HasPrefix(s.path, `SOFTWARE\`) && !strings.HasPrefix(s.path, `SYSTEM\`) {
			t.Errorf("signal %q is not an HKLM path", s.path)
		}
	}
}

// The chip claims "the OS has updates staged", so the signals must be ones that
// actually clear when the machine restarts.
func TestSignalsAreServicingRelated(t *testing.T) {
	for _, s := range rebootSignals {
		if !strings.Contains(s.path, "Component Based Servicing") &&
			!strings.Contains(s.path, "WindowsUpdate") {
			t.Errorf("signal %q is neither servicing nor Windows Update; "+
				"it may not clear on restart", s.path)
		}
	}
}
