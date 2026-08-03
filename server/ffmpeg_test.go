package server

import (
	"strings"
	"testing"
)

func TestFFmpegInstallFor(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		os, shell string
		supported bool
	}{
		{"windows", "powershell", true},
		{"linux", "sh", true},
		{"darwin", "", false},
		{"", "", false},
	} {
		script, shell, ok := s.ffmpegInstallFor(tc.os)
		if ok != tc.supported {
			t.Errorf("%q: supported = %v, want %v", tc.os, ok, tc.supported)
			continue
		}
		if !ok {
			continue
		}
		if shell != tc.shell {
			t.Errorf("%q: shell = %q, want %q", tc.os, shell, tc.shell)
		}
		if strings.TrimSpace(script) == "" {
			t.Errorf("%q: empty script", tc.os)
		}
	}
}

// The Windows script must end up with a real download URL substituted, or hosts
// would fetch the literal placeholder.
func TestWindowsScriptGetsURL(t *testing.T) {
	def := &Server{}
	script, _, _ := def.ffmpegInstallFor("windows")
	if strings.Contains(script, "__URL__") {
		t.Error("placeholder left unsubstituted")
	}
	if !strings.Contains(script, DefaultFFmpegURL) {
		t.Error("default download URL not used")
	}

	custom := &Server{cfg: Config{FFmpegURL: "https://mirror.internal/ffmpeg.zip"}}
	script, _, _ = custom.ffmpegInstallFor("windows")
	if !strings.Contains(script, "https://mirror.internal/ffmpeg.zip") {
		t.Error("configured URL not used")
	}
	if strings.Contains(script, DefaultFFmpegURL) {
		t.Error("default URL still present after override")
	}
}

// The installer must verify what it downloaded rather than trusting the archive:
// a build without libx264 cannot encode H.264 and would leave the host
// advertising a codec it can't produce.
func TestWindowsScriptValidatesDownload(t *testing.T) {
	script, _, _ := (&Server{}).ffmpegInstallFor("windows")
	for _, want := range []string{"-version", "libx264", "Remove-Item $dest"} {
		if !strings.Contains(script, want) {
			t.Errorf("install script is missing its %q validation", want)
		}
	}
	// Restarting would kill the very exec channel carrying this script.
	if strings.Contains(script, "Restart-Service") {
		t.Error("script restarts the agent, which would kill its own exec channel")
	}
}
