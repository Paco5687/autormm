package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The install commands ship as shell text, so a syntax error in one cannot fail
// a build. It fails on the machine being enrolled instead, halfway through, and
// on the desktop path that is after the agent binary has already been
// downloaded. `sh -n` is the cheapest thing that would catch it.
func TestGeneratedInstallScriptsParse(t *testing.T) {
	srv := testServer()
	for _, query := range []string{
		"?token=enroll-token",
		"?token=enroll-token&desktop=1",
		"?token=enroll-token&desktop=1&autologin=1",
		"?token=enroll-token&os=darwin",
	} {
		w := httptest.NewRecorder()
		srv.handleInstallScript(w, httptest.NewRequest(http.MethodGet, "/install.sh"+query, nil))
		script := w.Body.String()

		for _, placeholder := range []string{"__SERVER__", "__TOKEN__"} {
			if strings.Contains(script, placeholder) {
				t.Errorf("%s: %s was never substituted", query, placeholder)
			}
		}
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: sh -n rejected the script: %v\n%s", query, err, out)
		}
	}
}

// The unit is written through an *unquoted* heredoc so the detected values land
// in it. Quoted — which is how it used to be, and the obvious thing to restore
// when editing the block — systemd would be handed the literal text "$DISP",
// the agent would find no display, and the only symptom would be a greyed-out
// Remote button.
func TestDesktopUnitTakesTheDetectedDisplay(t *testing.T) {
	if strings.Contains(installShDesktop, "<<'UNIT'") {
		t.Error("the unit heredoc is quoted, so $DISP/$XAUTH would be written literally")
	}
	for _, want := range []string{"Environment=DISPLAY=$DISP", "Environment=XAUTHORITY=$XAUTH"} {
		if !strings.Contains(installShDesktop, want) {
			t.Errorf("the unit does not carry %q", want)
		}
	}
	for _, hardcoded := range []string{"Environment=DISPLAY=:0", "Environment=XAUTHORITY=%h/.Xauthority"} {
		if strings.Contains(installShDesktop, hardcoded) {
			t.Errorf("the unit hardcodes %q again", hardcoded)
		}
	}
}

// A headless agent on the same machine must be stopped before this one is
// installed, not after: both register under the hostname and claim the same
// slot on the hub, so any overlap is a host that flaps between streamable and
// not. Ordering is all there is to assert without a machine to install on.
func TestDesktopInstallStopsHeadlessAgentFirst(t *testing.T) {
	disable := strings.Index(installShDesktop, "sudo -n systemctl disable --now autormm-agent")
	download := strings.Index(installShDesktop, "/download/agent")
	switch {
	case disable < 0:
		t.Fatal("the desktop installer no longer disables a headless system agent")
	case download < 0:
		t.Fatal("the desktop installer no longer downloads the agent")
	case disable > download:
		t.Error("the headless agent is disabled after the download, leaving the two running together")
	}
}

// detected runs the detection snippet on its own, under `set -e` as the
// installer does, and reports what it left in DISP, XAUTH and SESSION_TYPE.
type detected struct{ display, authority, sessionType string }

func runDetect(t *testing.T, stub string, env map[string]string) detected {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// The stub shadows the real loginctl, which on a developer's own desktop
	// would otherwise answer with that desktop's session.
	if err := os.WriteFile(filepath.Join(bin, "loginctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	script := detectGraphicalSessionSh + "\nprintf '%s\\n%s\\n%s\\n' \"$DISP\" \"$XAUTH\" \"$SESSION_TYPE\"\n"
	cmd := exec.Command("sh", "-e")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"USER=tester",
		"HOME=" + dir,
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detection failed: %v\n%s", err, out)
	}
	// TrimSuffix, not TrimRight: an empty SESSION_TYPE is a result, and
	// trimming every trailing newline would swallow the line it printed on.
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected three lines of output, got %q", out)
	}
	return detected{display: lines[0], authority: lines[1], sessionType: lines[2]}
}

// The stub logind: one x11 session on :1, which is the shape that broke the
// hardcoded unit.
const logindX11 = `#!/bin/sh
case "$1" in
  show-user) echo "Display=7";;
  show-session)
    case "$4" in
      Type) echo "Type=x11";;
      Display) echo "Display=:1";;
      Name) echo "Name=tester";;
    esac;;
esac
`

func TestDetectTakesDisplayAndGdmAuthorityFromLogind(t *testing.T) {
	run := t.TempDir()
	gdm := filepath.Join(run, "gdm")
	if err := os.MkdirAll(gdm, 0o755); err != nil {
		t.Fatal(err)
	}
	authority := filepath.Join(gdm, "Xauthority")
	if err := os.WriteFile(authority, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := runDetect(t, logindX11, map[string]string{
		"XDG_RUNTIME_DIR":     run,
		"AUTORMM_X11_SOCKETS": filepath.Join(t.TempDir(), "none"),
	})
	if got.display != ":1" {
		t.Errorf("display = %q, want :1 (the session logind reported)", got.display)
	}
	if got.authority != authority {
		t.Errorf("authority = %q, want %q (GDM's, not ~/.Xauthority)", got.authority, authority)
	}
	if got.sessionType != "x11" {
		t.Errorf("session type = %q, want x11", got.sessionType)
	}
}

// A Wayland session has no X display to report, so detection has nothing to
// find — but it must still say what kind of session it is, which is the only
// way the installer can warn instead of silently installing something that
// cannot capture.
func TestDetectReportsAWaylandSession(t *testing.T) {
	stub := strings.ReplaceAll(logindX11, "Type=x11", "Type=wayland")
	stub = strings.ReplaceAll(stub, `Display) echo "Display=:1";;`, `Display) ;;`)

	got := runDetect(t, stub, map[string]string{
		"XDG_RUNTIME_DIR":     t.TempDir(),
		"AUTORMM_X11_SOCKETS": filepath.Join(t.TempDir(), "none"),
	})
	if got.sessionType != "wayland" {
		t.Errorf("session type = %q, want wayland", got.sessionType)
	}
	if got.display != ":0" {
		t.Errorf("display = %q, want the :0 fallback", got.display)
	}
}

// With nothing to go on, the old defaults are still the best guess — an install
// that lands on :0 and ~/.Xauthority is no worse than the one this replaced.
func TestDetectFallsBackWhenLogindKnowsNothing(t *testing.T) {
	silent := "#!/bin/sh\nexit 0\n"
	got := runDetect(t, silent, map[string]string{
		"XDG_RUNTIME_DIR":     t.TempDir(),
		"AUTORMM_X11_SOCKETS": filepath.Join(t.TempDir(), "none"),
	})
	if got.display != ":0" {
		t.Errorf("display = %q, want :0", got.display)
	}
	if !strings.HasSuffix(got.authority, "/.Xauthority") {
		t.Errorf("authority = %q, want ~/.Xauthority", got.authority)
	}
	if got.sessionType != "" {
		t.Errorf("session type = %q, want it empty when logind said nothing", got.sessionType)
	}
}

// No logind answer, but an X server is plainly running: its socket names the
// display.
func TestDetectReadsTheDisplayFromAnXSocket(t *testing.T) {
	sockets := t.TempDir()
	if err := os.WriteFile(filepath.Join(sockets, "X3"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := runDetect(t, "#!/bin/sh\nexit 0\n", map[string]string{
		"XDG_RUNTIME_DIR":     t.TempDir(),
		"AUTORMM_X11_SOCKETS": sockets,
	})
	if got.display != ":3" {
		t.Errorf("display = %q, want :3 from the socket", got.display)
	}
}

// Run from a terminal inside the desktop, the environment is already right and
// is what the session itself says — so it wins over anything guessed.
func TestDetectPrefersTheSessionEnvironment(t *testing.T) {
	dir := t.TempDir()
	authority := filepath.Join(dir, "authority")
	if err := os.WriteFile(authority, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runDetect(t, "#!/bin/sh\nexit 0\n", map[string]string{
		"DISPLAY":             ":9",
		"XAUTHORITY":          authority,
		"XDG_RUNTIME_DIR":     t.TempDir(),
		"AUTORMM_X11_SOCKETS": filepath.Join(t.TempDir(), "none"),
	})
	if got.display != ":9" {
		t.Errorf("display = %q, want :9 from the environment", got.display)
	}
	if got.authority != authority {
		t.Errorf("authority = %q, want %q from the environment", got.authority, authority)
	}
}

// The case this installer is actually run in: an ssh login (a session of its
// own) alongside the desktop session. logind answers show-user with one of
// them and it may well be the tty, so a detection that trusts that answer
// installs an agent pointed at no display at all.
const logindSshAndDesktop = `#!/bin/sh
case "$1" in
  show-user) echo "Display=9";;
  list-sessions)
    echo " 9 1000 tester -     -     active no -"
    echo " 4 1000 tester seat0 -     active no -";;
  show-session)
    case "$2" in
      9) case "$4" in
           Name) echo "Name=tester";;
           Type) echo "Type=tty";;
         esac;;
      4) case "$4" in
           Name) echo "Name=tester";;
           Type) echo "Type=x11";;
           Display) echo "Display=:0";;
         esac;;
    esac;;
esac
`

func TestDetectSkipsAnSshSessionForTheDesktopOne(t *testing.T) {
	got := runDetect(t, logindSshAndDesktop, map[string]string{
		"XDG_RUNTIME_DIR":     t.TempDir(),
		"AUTORMM_X11_SOCKETS": filepath.Join(t.TempDir(), "none"),
	})
	if got.sessionType != "x11" {
		t.Errorf("session type = %q, want x11 (the tty session was taken instead)", got.sessionType)
	}
	if got.display != ":0" {
		t.Errorf("display = %q, want :0 from the graphical session", got.display)
	}
}
