//go:build !windows && !linux

package capture

import "github.com/Paco5687/autormm/internal/protocol"

func displayModes(int) []protocol.Mode { return nil }
func setDisplayMode(int, int, int) error {
	return errUnsupportedRes
}
