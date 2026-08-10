//go:build !windows

package metrics

import "os"

// rebootPending reports whether the OS says a restart is outstanding, and why.
//
// Debian and Ubuntu drop /var/run/reboot-required when a package (a kernel,
// typically) needs one, and remove it on the way back up. Other distributions
// have no common convention, and inventing one would be worse than saying
// nothing — a chip that is always on teaches people to ignore it, which is
// exactly what happened on Windows.
func rebootPending() (bool, string) {
	for _, p := range []string{"/var/run/reboot-required", "/run/reboot-required"} {
		if _, err := os.Stat(p); err == nil {
			return true, "a package update"
		}
	}
	return false, ""
}
