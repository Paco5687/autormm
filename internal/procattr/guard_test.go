package procattr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every process the agent starts must be spawned hidden, or Windows gives it a
// console window on the user's desktop.
//
// This is not a theoretical tidiness rule. The GPU probe runs nvidia-smi every
// five seconds, and without this a console window appeared and vanished on the
// user's desktop every five seconds for as long as the agent ran. The same
// applies to ffmpeg for every H.264 session, and to every remote command.
//
// The failure is invisible to anyone developing on Linux or macOS, which is
// where this is built, so a review will not catch it. A test can.
func TestEverySpawnIsHidden(t *testing.T) {
	roots := []string{"../../agent", "../../internal"}
	var offenders []string

	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			name := info.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			// Platform-specific files that cannot compile on Windows have no
			// console to worry about.
			for _, suffix := range []string{"_linux.go", "_unix.go", "_darwin.go"} {
				if strings.HasSuffix(name, suffix) {
					return nil
				}
			}
			if strings.Contains(path, filepath.Join("internal", "procattr")) {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(b)
			if !strings.Contains(src, "exec.Command") {
				return nil
			}
			// A file setting SysProcAttr itself has already made a deliberate
			// choice about how the child is created (DETACHED_PROCESS, say,
			// which likewise gets no console).
			if strings.Contains(src, "procattr.Hide") || strings.Contains(src, "SysProcAttr") {
				return nil
			}
			offenders = append(offenders, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	for _, f := range offenders {
		t.Errorf("%s spawns a process without procattr.Hide — on Windows that opens a console window on the user's desktop", f)
	}
}
