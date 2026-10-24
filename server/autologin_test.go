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

// Enabling autologin means rewriting a file that decides whether the machine
// can be logged into at all, on a host the operator may not be sitting at. A
// mangled [daemon] section is a desktop that comes back to a broken greeter, so
// the three shapes GDM's config turns up in are each tested, as is a re-run.

// sudoRuns stands in for passwordless sudo: it drops -n and executes the rest,
// so the snippet's real code path is exercised rather than a seam added for the
// test.
const sudoRuns = `#!/bin/sh
if [ "$1" = "-n" ]; then shift; fi
exec "$@"
`

// sudoRefuses is a host where sudo would ask for a password.
const sudoRefuses = `#!/bin/sh
exit 1
`

// ubuntuGdmConf is what Ubuntu ships: a [daemon] section with both keys present
// but commented out, which must not be mistaken for them being set.
const ubuntuGdmConf = `# GDM configuration storage

[daemon]
# Uncomment the line below to force the login screen to use Xorg
#WaylandEnable=false

# Enabling automatic login
#  AutomaticLoginEnable = true
#  AutomaticLogin = user1

[security]

[xdmcp]
`

type autologinRun struct {
	conf, backup, out string
	backupExists      bool
}

func runAutologin(t *testing.T, conf, sudo string) autologinRun {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(sudo), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "custom.conf")
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", "-e")
	cmd.Stdin = strings.NewReader(enableAutologinSh)
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"USER=tester",
		"HOME=" + dir,
		"AUTORMM_GDM_CONFS=" + path,
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enabling autologin failed: %v\n%s", err, out)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run := autologinRun{conf: string(after), out: string(out)}
	if b, err := os.ReadFile(path + ".autormm.bak"); err == nil {
		run.backup, run.backupExists = string(b), true
	}
	return run
}

// countKey is how many lines begin with the given key, so a second copy of it
// is a failure rather than something a later reader happens to shadow.
func countKey(conf, key string) int {
	n := 0
	for _, line := range strings.Split(conf, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key) {
			n++
		}
	}
	return n
}

func TestAutologinFillsUbuntusCommentedSection(t *testing.T) {
	got := runAutologin(t, ubuntuGdmConf, sudoRuns)

	if countKey(got.conf, "AutomaticLoginEnable=true") != 1 {
		t.Errorf("want exactly one enabled key, got:\n%s", got.conf)
	}
	if countKey(got.conf, "AutomaticLogin=tester") != 1 {
		t.Errorf("want exactly one login key for the installing user, got:\n%s", got.conf)
	}
	// It has to land inside [daemon]: GDM ignores it anywhere else, and the
	// commented examples sit in that section for a reason.
	daemon := strings.Index(got.conf, "[daemon]")
	security := strings.Index(got.conf, "[security]")
	enable := strings.Index(got.conf, "AutomaticLoginEnable=true")
	if !(daemon < enable && enable < security) {
		t.Errorf("the keys were not written inside [daemon]:\n%s", got.conf)
	}
	// The commented examples are left alone rather than rewritten.
	if !strings.Contains(got.conf, "#  AutomaticLogin = user1") {
		t.Errorf("the commented example was altered:\n%s", got.conf)
	}
	if !got.backupExists || got.backup != ubuntuGdmConf {
		t.Error("the original was not kept as a backup")
	}
}

func TestAutologinReplacesKeysAlreadySet(t *testing.T) {
	conf := "[daemon]\nAutomaticLoginEnable=false\nAutomaticLogin=someoneelse\n"
	got := runAutologin(t, conf, sudoRuns)

	if countKey(got.conf, "AutomaticLoginEnable") != 1 || !strings.Contains(got.conf, "AutomaticLoginEnable=true") {
		t.Errorf("the existing key was not set to true exactly once:\n%s", got.conf)
	}
	if countKey(got.conf, "AutomaticLogin=") != 1 || !strings.Contains(got.conf, "AutomaticLogin=tester") {
		t.Errorf("the existing user was not replaced exactly once:\n%s", got.conf)
	}
	if strings.Contains(got.conf, "someoneelse") {
		t.Errorf("the previous user is still configured:\n%s", got.conf)
	}
}

func TestAutologinWritesTheSectionWhenThereIsNone(t *testing.T) {
	got := runAutologin(t, "[security]\nDisallowTCP=true\n", sudoRuns)

	if !strings.Contains(got.conf, "[daemon]") {
		t.Errorf("no [daemon] section was written:\n%s", got.conf)
	}
	if countKey(got.conf, "AutomaticLoginEnable=true") != 1 || countKey(got.conf, "AutomaticLogin=tester") != 1 {
		t.Errorf("the keys were not written once each:\n%s", got.conf)
	}
	if !strings.Contains(got.conf, "DisallowTCP=true") {
		t.Errorf("an existing section was lost:\n%s", got.conf)
	}
}

// Re-running the installer is the documented way to upgrade an agent in place,
// so it must not accumulate keys — and must not replace the backup with a copy
// that already has autologin in it, which would make the original unrecoverable.
func TestAutologinIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(sudoRuns), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "custom.conf")
	if err := os.WriteFile(path, []byte(ubuntuGdmConf), 0o644); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		cmd := exec.Command("sh", "-e")
		cmd.Stdin = strings.NewReader(enableAutologinSh)
		cmd.Env = []string{
			"PATH=" + bin + ":" + os.Getenv("PATH"),
			"USER=tester",
			"HOME=" + dir,
			"AUTORMM_GDM_CONFS=" + path,
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("run %d failed: %v\n%s", i+1, err, out)
		}
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := countKey(string(after), "AutomaticLoginEnable=true"); n != 1 {
		t.Errorf("after two runs there are %d enabled keys:\n%s", n, after)
	}
	if n := countKey(string(after), "AutomaticLogin=tester"); n != 1 {
		t.Errorf("after two runs there are %d login keys:\n%s", n, after)
	}
	backup, err := os.ReadFile(path + ".autormm.bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != ubuntuGdmConf {
		t.Errorf("the second run overwrote the backup with a modified copy:\n%s", backup)
	}
}

// Without passwordless sudo there is nothing this can do, and the one thing it
// must not do is half-write the file. It says what to set instead.
func TestAutologinLeavesTheConfigAloneWithoutSudo(t *testing.T) {
	got := runAutologin(t, ubuntuGdmConf, sudoRefuses)

	if got.conf != ubuntuGdmConf {
		t.Errorf("the config was modified without sudo:\n%s", got.conf)
	}
	if got.backupExists {
		t.Error("a backup was written even though nothing was changed")
	}
	if !strings.Contains(got.out, "AutomaticLoginEnable=true") {
		t.Errorf("the output does not say what to set by hand:\n%s", got.out)
	}
}

// The machine logging itself in is a decision, not a detail of enrolling a
// host: it happens only when asked for.
func TestAutologinIsOptIn(t *testing.T) {
	srv := testServer()
	script := func(query string) string {
		w := httptest.NewRecorder()
		srv.handleInstallScript(w, httptest.NewRequest(http.MethodGet, "/install.sh"+query, nil))
		return w.Body.String()
	}

	plain := script("?token=t&desktop=1")
	if !strings.Contains(plain, `AUTOLOGIN="0"`) {
		t.Error("the plain desktop install does not have autologin switched off")
	}
	if strings.Contains(plain, "__AUTOLOGIN__") {
		t.Error("the placeholder was never substituted")
	}
	if asked := script("?token=t&desktop=1&autologin=1"); !strings.Contains(asked, `AUTOLOGIN="1"`) {
		t.Error("?autologin=1 did not switch it on")
	}
	// The headless install has no desktop to log into, so it must not carry any
	// of this however it is called.
	if headless := script("?token=t&autologin=1"); strings.Contains(headless, "AutomaticLoginEnable") {
		t.Error("the headless installer carries the autologin code")
	}
}
