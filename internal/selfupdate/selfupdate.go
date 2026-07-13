// Package selfupdate lets an agent keep itself matched to its hub: it checks the
// hub's version, downloads the replacement binary, and — only after validating
// it — hands it to a platform-specific apply step. Validation includes a smoke
// test (run "<binary> -version" and require the hub's exact version), which
// proves the download is intact, executable, and the right build, and makes
// update loops impossible.
package selfupdate

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config drives an update check.
type Config struct {
	Server         string // hub base URL
	Token          string // enroll token (for the download endpoint)
	Insecure       bool   // skip TLS verify
	DownloadQuery  string // e.g. "os=windows&arch=amd64&kind=tray" or "os=linux&arch=amd64"
	CurrentVersion string // this build's version
	Apply          func(newBinary string) error
	Log            func(format string, args ...any)
}

func (c Config) logf(f string, a ...any) {
	if c.Log != nil {
		c.Log(f, a...)
	}
}

func httpClient(insecure bool) *http.Client {
	c := &http.Client{Timeout: 120 * time.Second}
	if insecure {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return c
}

// HubVersion returns the version the hub was built at.
func HubVersion(server string, insecure bool) (string, error) {
	resp, err := httpClient(insecure).Get(strings.TrimRight(server, "/") + "/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("/version: %s", resp.Status)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// CheckOnce updates the agent if the hub reports a different version and the
// replacement binary passes validation. It's a no-op when already current.
func CheckOnce(cfg Config) error {
	hv, err := HubVersion(cfg.Server, cfg.Insecure)
	if err != nil {
		return err
	}
	if hv == "" || hv == cfg.CurrentVersion {
		return nil
	}
	cfg.logf("hub is %s, agent is %s -- fetching update", hv, cfg.CurrentVersion)

	tmp, err := download(cfg)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(tmp)
		}
	}()

	if err := validate(tmp, hv); err != nil {
		return fmt.Errorf("update failed validation: %w", err)
	}
	cfg.logf("update to %s validated -- applying", hv)
	keep = true // Apply takes ownership of tmp
	return cfg.Apply(tmp)
}

// Loop runs CheckOnce shortly after start and periodically after.
func Loop(cfg Config, first, every time.Duration) {
	time.Sleep(first)
	for {
		if err := CheckOnce(cfg); err != nil {
			cfg.logf("auto-update: %v", err)
		}
		time.Sleep(every)
	}
}

// download fetches the replacement into a temp file next to the current binary
// (same filesystem, so the later rename is atomic) with the right extension.
func download(cfg Config) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	u := strings.TrimRight(cfg.Server, "/") + "/download/agent?" + cfg.DownloadQuery +
		"&token=" + url.QueryEscape(cfg.Token)
	resp, err := httpClient(cfg.Insecure).Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.CreateTemp(filepath.Dir(exe), "autormm-update-*"+ext)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	os.Chmod(f.Name(), 0o755)
	return f.Name(), nil
}

// validate checks size + executable magic, then runs the binary's -version and
// requires it to report wantVersion.
func validate(path, wantVersion string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() < 1<<20 {
		return fmt.Errorf("suspiciously small (%d bytes)", info.Size())
	}
	if err := checkMagic(path); err != nil {
		return err
	}
	out, err := exec.Command(path, "-version").Output()
	if err != nil {
		return fmt.Errorf("binary won't run: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != wantVersion {
		return fmt.Errorf("version mismatch: binary reports %q, hub wants %q", got, wantVersion)
	}
	return nil
}

func checkMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var b [4]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return err
	}
	switch {
	case b[0] == 'M' && b[1] == 'Z': // PE
		return nil
	case b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F': // ELF
		return nil
	}
	return fmt.Errorf("not an executable (bad magic %x)", b)
}

// ReplaceRunningBinary swaps newBinary in for the current executable, keeping a
// .old backup for rollback. It does not restart — the caller decides.
func ReplaceRunningBinary(newBinary string) (restore func(), err error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return nil, err
	}
	if err := os.Rename(newBinary, exe); err != nil {
		os.Rename(old, exe) // roll back
		return nil, err
	}
	return func() { // restore the previous binary if the new one won't start
		os.Rename(exe, newBinary)
		os.Rename(old, exe)
	}, nil
}
