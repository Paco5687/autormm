//go:build windows

package metrics

import "golang.org/x/sys/windows/registry"

// rebootPending reports whether Windows says a restart is outstanding.
//
// There is no single flag; Windows scatters the signal across several keys and
// any one of them means a restart is owed. These are the three that matter in
// practice: Component Based Servicing (a serviced update), Windows Update's own
// flag, and a rename queued for the next boot.
func rebootPending() bool {
	for _, k := range []struct {
		path  string
		value string // "" means the key merely existing is the signal
	}{
		{`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`, ""},
		{`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`, ""},
		{`SYSTEM\CurrentControlSet\Control\Session Manager`, "PendingFileRenameOperations"},
	} {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		if k.value == "" {
			key.Close()
			return true
		}
		_, _, err = key.GetStringsValue(k.value)
		key.Close()
		if err == nil {
			return true
		}
	}
	return false
}
