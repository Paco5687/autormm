package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Validate exposes the download validator (size + magic + `-version` smoke test).
func Validate(path, wantVersion string) error { return validate(path, wantVersion) }

func ghClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

// GitHubLatest returns the latest release version (without a leading "v") for a
// "owner/repo" GitHub repository.
func GitHubLatest(repo string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := ghClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: %s", resp.Status)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	return strings.TrimPrefix(r.TagName, "v"), nil
}

// FetchGitHubServer downloads the release tarball for repo@version matching this
// OS/arch, verifies its SHA-256 against SHA256SUMS, extracts autormm-server, and
// writes it to a temp file in destDir. Returns the temp path.
func FetchGitHubServer(repo, version, destDir string) (string, error) {
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s", repo, version)
	asset := fmt.Sprintf("autormm_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)

	tgz, err := httpGetBytes(base + "/" + asset)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	if sums, err := httpGetBytes(base + "/SHA256SUMS"); err == nil {
		want := sumFor(sums, asset)
		if want == "" {
			return "", fmt.Errorf("no checksum for %s in SHA256SUMS", asset)
		}
		got := sha256.Sum256(tgz)
		if hex.EncodeToString(got[:]) != want {
			return "", fmt.Errorf("checksum mismatch for %s", asset)
		}
	}
	bin, err := extractFromTarGz(tgz, "autormm-server")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(destDir, "autormm-stage-*")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(bin); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	os.Chmod(f.Name(), 0o755)
	return f.Name(), nil
}

func httpGetBytes(url string) ([]byte, error) {
	resp, err := ghClient().Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20)) // 200 MiB cap
}

// sumFor returns the hex sha256 for filename from a SHA256SUMS body.
func sumFor(sums []byte, filename string) string {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == filename {
			return f[0]
		}
	}
	return ""
}

// extractFromTarGz returns the bytes of the entry whose base name is name.
func extractFromTarGz(tgz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == name {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}
