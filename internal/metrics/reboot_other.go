//go:build !windows

package metrics

import "os"

// rebootPending reports whether the OS says a restart is outstanding.
//
// Debian and Ubuntu drop /var/run/reboot-required when a package (a kernel,
// typically) needs one. Other distributions have no common convention, and
// guessing at one would be worse than saying nothing — a false "needs reboot"
// on every host teaches people to ignore the chip.
func rebootPending() bool {
	for _, p := range []string{"/var/run/reboot-required", "/run/reboot-required"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
