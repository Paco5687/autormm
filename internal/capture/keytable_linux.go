//go:build linux

package capture

// keyTable exposes the platform's code→keycode map for tests, so the keys the
// viewer offers can be checked against what this platform can actually inject.
func keyTable() map[string]bool {
	out := make(map[string]bool, len(codeToKeysym))
	for k := range codeToKeysym {
		out[k] = true
	}
	return out
}
