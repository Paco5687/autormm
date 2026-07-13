package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/Paco5687/autormm/internal/auth"
)

// persisted holds auto-generated settings so they stay stable across restarts.
type persisted struct {
	Admin  string `json:"admin_token"`
	Enroll string `json:"enroll_token"`
	DB     string `json:"db"`
}

func configDir() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autormm")
}

func configPath() string { return filepath.Join(configDir(), "autormm.json") }

func loadPersisted() persisted {
	var p persisted
	if b, err := os.ReadFile(configPath()); err == nil {
		json.Unmarshal(b, &p)
	}
	return p
}

func savePersisted(p persisted) {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	os.WriteFile(configPath(), b, 0o600)
}

// resolveConfig fills empty admin/enroll/db from a saved config file, generating
// and persisting any that are still missing. Returns true if it generated new
// tokens (first run) so the caller can print a welcome banner.
func resolveConfig(admin, enroll, db *string) (generated bool) {
	p := loadPersisted()
	if *admin == "" {
		*admin = p.Admin
	}
	if *enroll == "" {
		*enroll = p.Enroll
	}
	if *db == "" {
		*db = p.DB
	}
	if *admin == "" {
		*admin = auth.RandomID(18)
		generated = true
	}
	if *enroll == "" {
		*enroll = auth.RandomID(18)
		generated = true
	}
	if *db == "" {
		*db = filepath.Join(configDir(), "autormm.db")
		generated = true
	}
	if generated {
		os.MkdirAll(filepath.Dir(*db), 0o700)
		savePersisted(persisted{Admin: *admin, Enroll: *enroll, DB: *db})
	}
	return generated
}

// welcomeBanner prints the dashboard URL and admin token on first run.
func welcomeBanner(addr, admin string) {
	host, port := hostPortFromAddr(addr)
	url := fmt.Sprintf("http://%s:%s", host, port)
	fmt.Printf(`
  ┌───────────────────────────────────────────────────────────┐
  │  autormm is ready                                           │
  ├───────────────────────────────────────────────────────────┤
  │  Dashboard:   %-45s │
  │  Admin token: %-45s │
  │                                                             │
  │  Open the dashboard, click the 🔑 icon, paste the token.    │
  │  Add hosts from the dashboard's "Add host" button.          │
  │  Config saved to: %-41s │
  └───────────────────────────────────────────────────────────┘

`, url, admin, configPath())
}

// hostPortFromAddr turns a listen address into a best-guess reachable URL.
func hostPortFromAddr(addr string) (host, port string) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "localhost", "8765"
	}
	if p == "" {
		p = "8765"
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		h = firstLANIP()
	}
	return h, p
}

// firstLANIP returns a non-loopback IPv4 to show in the banner.
func firstLANIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return "localhost"
}
