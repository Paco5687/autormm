//go:build !windows && !linux

package capture

func getClipboard() (string, bool) { return "", false }
func setClipboard(string) error    { return nil }
