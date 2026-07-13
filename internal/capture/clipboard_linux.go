//go:build linux

package capture

import (
	"os/exec"
	"strings"
	"sync"
)

// Linux clipboard access goes through xclip or xsel (whichever is installed).
// Headless/Wayland-only hosts without either simply have no clipboard sync.
var clip struct {
	once sync.Once
	get  []string // command + args to read the clipboard
	set  []string // command + args to write it (text on stdin)
}

func resolveClip() {
	if p, err := exec.LookPath("xclip"); err == nil {
		clip.get = []string{p, "-selection", "clipboard", "-o"}
		clip.set = []string{p, "-selection", "clipboard", "-i"}
		return
	}
	if p, err := exec.LookPath("xsel"); err == nil {
		clip.get = []string{p, "--clipboard", "--output"}
		clip.set = []string{p, "--clipboard", "--input"}
	}
}

func getClipboard() (string, bool) {
	clip.once.Do(resolveClip)
	if clip.get == nil {
		return "", false
	}
	out, err := exec.Command(clip.get[0], clip.get[1:]...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func setClipboard(text string) error {
	clip.once.Do(resolveClip)
	if clip.set == nil {
		return nil // no clipboard tool; ignore rather than fail the session
	}
	cmd := exec.Command(clip.set[0], clip.set[1:]...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
