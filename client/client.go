// Package client is the autormm command-line client: it talks to the server's
// REST API to list hosts and to open remote-desktop sessions in a browser.
package client

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Paco5687/autormm/internal/protocol"
)

// Config is persisted to the user's config dir.
type Config struct {
	Server   string `json:"server"`
	Token    string `json:"token"`
	Insecure bool   `json:"insecure,omitempty"`
}

// ConfigPath returns the path to the client config file.
func ConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autormm", "client.json")
}

// Load reads the saved config (missing file yields an empty config).
func Load() (*Config, error) {
	b, err := os.ReadFile(ConfigPath())
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the config with 0600 permissions.
func (c *Config) Save() error {
	p := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

// Client talks to the server API.
type Client struct {
	cfg  *Config
	http *http.Client
}

// New builds an API client.
func New(cfg *Config) *Client {
	tr := &http.Transport{}
	if cfg.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second, Transport: tr}}
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.cfg.Server, "/") + path
}

func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.url(path), rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// Hosts fetches the host list.
func (c *Client) Hosts() ([]protocol.HostView, error) {
	res, err := c.do(http.MethodGet, "/api/hosts", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized (check token; run `autormm-client login`)")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", res.Status)
	}
	var hosts []protocol.HostView
	if err := json.NewDecoder(res.Body).Decode(&hosts); err != nil {
		return nil, err
	}
	return hosts, nil
}

// CreateSession requests a session (screen or terminal) and returns the ticket.
func (c *Client) CreateSession(agentID, kind string, fps, quality int) (*protocol.SessionResponse, error) {
	res, err := c.do(http.MethodPost, "/api/session", protocol.SessionRequest{
		AgentID: agentID, Kind: kind, FPS: fps, Quality: quality,
	})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	var sr protocol.SessionResponse
	if err := json.NewDecoder(res.Body).Decode(&sr); err != nil {
		return nil, err
	}
	return &sr, nil
}

// StartSession requests a remote-desktop session and returns the viewer URL.
func (c *Client) StartSession(agentID string, fps, quality int) (string, error) {
	sr, err := c.CreateSession(agentID, protocol.SessionScreen, fps, quality)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(c.cfg.Server, "/")
	return fmt.Sprintf("%s/viewer?token=%s", base, sr.Token), nil
}

// ExecResult is the outcome of a remote command.
type ExecResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Error     string `json:"error"`
	Truncated bool   `json:"truncated"`
}

// Exec runs a command on a host and returns its captured output.
func (c *Client) Exec(agentID, command, shell string, timeout int) (*ExecResult, error) {
	body := map[string]any{"agent_id": agentID, "command": command, "shell": shell, "timeout_secs": timeout}
	res, err := c.do(http.MethodPost, "/api/exec", body)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	var out ExecResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Inventory is a host's installed-software listing.
type Inventory struct {
	Source   string             `json:"source"`
	Packages []protocol.Package `json:"packages"`
	Count    int                `json:"count"`
	Error    string             `json:"error"`
}

// Inventory fetches installed software from a host.
func (c *Client) Inventory(agentID string) (*Inventory, error) {
	res, err := c.do(http.MethodGet, "/api/inventory?agent="+url.QueryEscape(agentID), nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	var inv Inventory
	if err := json.NewDecoder(res.Body).Decode(&inv); err != nil {
		return nil, err
	}
	return &inv, nil
}

// FindHost resolves a name (hostname or agent id, case-insensitive) to a host.
func FindHost(hosts []protocol.HostView, name string) (*protocol.HostView, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	var matches []*protocol.HostView
	for i := range hosts {
		h := &hosts[i]
		if strings.ToLower(h.AgentID) == name || strings.ToLower(h.Hostname) == name {
			return h, nil
		}
		if strings.Contains(strings.ToLower(h.Hostname), name) || strings.Contains(strings.ToLower(h.AgentID), name) {
			matches = append(matches, h)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no host matching %q", name)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Hostname
		}
		return nil, fmt.Errorf("ambiguous %q, matches: %s", name, strings.Join(names, ", "))
	}
}
