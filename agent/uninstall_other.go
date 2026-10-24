//go:build !linux && !windows

package agent

import "fmt"

// planUninstall has nothing to undo on platforms the hub has no installer for.
//
// macOS agents are built and run, but they are put in place by hand rather
// than by an installer, so there is no layout to reverse and guessing at one
// would be worse than saying so. The hub reports this as a refusal and leaves
// the host where it is.
func (a *Agent) planUninstall() (*uninstaller, error) {
	return nil, fmt.Errorf("self-uninstall is not supported on this platform; " +
		"remove the agent on the machine, then remove the host from the dashboard")
}
