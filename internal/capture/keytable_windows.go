//go:build windows

package capture

// keyTable exposes the platform's code→keycode map for tests, so the keys the
// viewer offers can be checked against what this platform can actually inject.
func keyTable() map[string]bool {
	out := make(map[string]bool, len(codeToVK))
	for k := range codeToVK {
		out[k] = true
	}
	return out
}
