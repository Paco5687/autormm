//go:build windows

package metrics

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// rebootSignal is one place Windows records that a restart is owed.
type rebootSignal struct {
	reason string
	path   string
	value  string // "" => the key merely existing is the signal
}

// rebootSignals are the places Windows records an outstanding restart.
//
// Notably absent: PendingFileRenameOperations. It is the signal everyone reaches
// for first and it is nearly useless as a *pending reboot* indicator, because
// routine software writes it constantly — browser updaters, antivirus
// definitions, any installer queueing a file swap. It does clear at boot, then
// repopulates within minutes, so a chip driven by it is on permanently and
// therefore says nothing. Windows itself does not tell the user to restart on
// account of it.
//
// What remains are the signals that mean what the chip claims and that actually
// clear when the machine comes back: servicing and Windows Update, plus a
// pending computer rename.
var rebootSignals = []rebootSignal{
	{"a serviced update", `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`, ""},
	{"servicing in progress", `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootInProgress`, ""},
	{"Windows Update", `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`, ""},
	{"Windows Update reporting", `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\PostRebootReporting`, ""},
}

// rebootPending reports whether Windows says a restart is outstanding, and why.
//
// The reason is carried so the dashboard can show what is actually asking. A bare
// flag left "why is this host still saying that?" answerable only by someone
// opening regedit, which is how a permanently-stuck chip went unnoticed.
func rebootPending() (bool, string) {
	for _, s := range rebootSignals {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, s.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if s.value == "" {
			key.Close()
			return true, s.reason
		}
		vals, _, err := key.GetStringsValue(s.value)
		key.Close()
		// An empty value is not a pending operation. Windows routinely leaves a
		// zero-length list behind, and counting its mere existence was part of
		// what kept this stuck on.
		if err == nil && len(vals) > 0 {
			return true, s.reason
		}
	}
	if pendingRename() {
		return true, "a pending computer rename"
	}
	return false, ""
}

// pendingRename reports whether the machine has been renamed but not restarted:
// the active name and the configured name disagree until it is.
func pendingRename() bool {
	get := func(path, value string) string {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		if err != nil {
			return ""
		}
		defer key.Close()
		s, _, err := key.GetStringValue(value)
		if err != nil {
			return ""
		}
		return s
	}
	active := get(`SYSTEM\CurrentControlSet\Control\ComputerName\ActiveComputerName`, "ComputerName")
	configured := get(`SYSTEM\CurrentControlSet\Control\ComputerName\ComputerName`, "ComputerName")
	return active != "" && configured != "" && !strings.EqualFold(active, configured)
}
