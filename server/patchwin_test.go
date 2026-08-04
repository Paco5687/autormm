package server

import (
	"strings"
	"testing"
)

func TestPatchPlanFor(t *testing.T) {
	for _, tc := range []struct {
		os            string
		want          bool
		shell         string
		needsElevated bool
	}{
		{"linux", true, "sh", false},
		{"windows", true, "powershell", true},
		// macOS: softwareupdate. No elevated helper concept — the agent is a user
		// LaunchAgent and the install path says plainly that it needs root.
		{"darwin", true, "sh", false},
		{"", false, "", false},
	} {
		plan, ok := patchPlanFor(tc.os)
		if ok != tc.want {
			t.Errorf("patchPlanFor(%q) supported = %v, want %v", tc.os, ok, tc.want)
			continue
		}
		if !ok {
			continue
		}
		if plan.shell != tc.shell {
			t.Errorf("patchPlanFor(%q) shell = %q, want %q", tc.os, plan.shell, tc.shell)
		}
		if plan.needsElevated != tc.needsElevated {
			t.Errorf("patchPlanFor(%q) needsElevated = %v, want %v", tc.os, plan.needsElevated, tc.needsElevated)
		}
		if plan.status == "" || plan.install == "" || plan.reboot == "" {
			t.Errorf("patchPlanFor(%q) has an empty script", tc.os)
		}
		// The hub caps exec at maxExecTimeoutSecs; a plan asking for more would
		// be silently shortened and cut the install off mid-flight.
		if plan.installTimeout > maxExecTimeoutSecs {
			t.Errorf("patchPlanFor(%q) installTimeout %d exceeds the exec cap %d",
				tc.os, plan.installTimeout, maxExecTimeoutSecs)
		}
	}
}

// These scripts can only fail on a live Windows host, so guard the mistakes
// that are easy to make while editing Go string literals full of PowerShell.
func TestWindowsPatchScriptsAreWellFormed(t *testing.T) {
	scripts := map[string]string{
		"status":  windowsPatchStatus,
		"install": windowsPatchInstall,
		"reboot":  windowsReboot,
	}
	for name, script := range scripts {
		// A backtick is PowerShell's escape character (and would also end the
		// Go raw string literal holding the script).
		if strings.Contains(script, "`") {
			t.Errorf("%s: contains a backtick", name)
		}
		// PowerShell comments start with #; a C-style // is a parse error.
		for i, line := range strings.Split(script, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				t.Errorf("%s line %d: // is not a PowerShell comment, use #", name, i+1)
			}
		}
		for _, pair := range []struct {
			name        string
			open, close rune
		}{{"braces", '{', '}'}, {"parens", '(', ')'}} {
			if got := strings.Count(script, string(pair.open)) - strings.Count(script, string(pair.close)); got != 0 {
				t.Errorf("%s: unbalanced %s (%+d)", name, pair.name, got)
			}
		}
		if strings.Count(script, "'")%2 != 0 {
			t.Errorf("%s: odd number of single quotes", name)
		}
	}
	// The status script must emit the key=value line parsePatchStatus expects.
	if !strings.Contains(windowsPatchStatus, "MGR=wu UPDATES=") {
		t.Error("status script no longer emits the MGR=wu UPDATES=... line")
	}
	// Both scripts must agree on skipping driver updates, or the count shown
	// would not match what actually installs.
	for _, name := range []string{"status", "install"} {
		if !strings.Contains(scripts[name], "$_.Type -eq 1") {
			t.Errorf("%s: missing the software-only (non-driver) filter", name)
		}
	}
}

// The status output must stay parsable by parsePatchStatus, which both the
// Linux and Windows scripts feed.
func TestParsePatchStatusWindowsShape(t *testing.T) {
	upd, sec, reboot := parsePatchStatus("MGR=wu UPDATES=7 SECURITY=3 REBOOT=yes")
	if upd != 7 || sec != 3 || !reboot {
		t.Fatalf("got updates=%d security=%d reboot=%v, want 7/3/true", upd, sec, reboot)
	}
}

func TestPatchError(t *testing.T) {
	for _, tc := range []struct {
		name              string
		stdout, stderr    string
		exitCode          int
		want              string
		wantSubstringOnly bool
	}{
		{name: "clean linux status", stdout: "MGR=apt UPDATES=2 SECURITY=1 REBOOT=no", want: ""},
		{name: "clean windows status", stdout: "MGR=wu UPDATES=0 SECURITY=0 REBOOT=no", want: ""},
		{
			name: "windows COM failure", stdout: "ERR=The service cannot be started", exitCode: 1,
			want: "The service cannot be started",
		},
		{
			name: "non-zero with stderr", stderr: "access denied", exitCode: 5,
			want: "access denied",
		},
		{
			name: "non-zero without output", exitCode: 5,
			want: "patch check failed (exit 5)",
		},
		{
			// A non-zero exit alongside usable output should not mask the counts.
			name: "non-zero but parsable", stdout: "MGR=apt UPDATES=1 SECURITY=0 REBOOT=no", exitCode: 1,
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := patchError(tc.stdout, tc.stderr, tc.exitCode); got != tc.want {
				t.Errorf("patchError = %q, want %q", got, tc.want)
			}
		})
	}
}
