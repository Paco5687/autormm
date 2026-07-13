package capture

// GetClipboard returns the host's clipboard text. ok is false when the
// clipboard is empty, non-text, or unavailable on this platform.
func GetClipboard() (string, bool) { return getClipboard() }

// SetClipboard sets the host clipboard to text.
func SetClipboard(text string) error { return setClipboard(text) }
