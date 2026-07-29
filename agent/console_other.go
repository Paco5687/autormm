//go:build !windows

package agent

import "context"

// superviseConsole is a no-op off Windows: only Windows has a separate secure
// desktop that requires a SYSTEM worker inside the console session. On Linux the
// elevated (root) helper is exec-only.
func (a *Agent) superviseConsole(ctx context.Context) {}
