package server

import (
	"embed"
	"fmt"
	"net/http"
	"strings"

	"github.com/Paco5687/autormm/internal/auth"
)

// agentBinsFS holds agent binaries embedded at build time (populated by `make`
// / goreleaser). Empty in a plain `go build`, in which case /download 404s.
//
//go:embed all:agentbins
var agentBinsFS embed.FS

func agentBinName(os, arch string) string {
	name := fmt.Sprintf("autormm-agent_%s_%s", os, arch)
	if os == "windows" {
		name += ".exe"
	}
	return name
}

// agentBinFile picks the embedded file for the requested platform and kind.
// kind=="tray" serves the Windows GUI (system-tray) build; otherwise the plain
// console agent.
func agentBinFile(os, arch, kind string) string {
	if kind == "tray" && os == "windows" {
		return "autormm-agent-tray_windows_amd64.exe"
	}
	return agentBinName(os, arch)
}

func agentBin(os, arch string) ([]byte, bool) { return agentBinKind(os, arch, "") }

func agentBinKind(os, arch, kind string) ([]byte, bool) {
	b, err := agentBinsFS.ReadFile("agentbins/" + agentBinFile(os, arch, kind))
	if err != nil {
		return nil, false
	}
	return b, true
}

// baseURL reconstructs the URL a client used to reach the hub, so served
// install scripts point agents back at the same address.
func baseURL(r *http.Request) string {
	scheme := "http"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// handleDownloadAgent serves an embedded agent binary (enroll-token gated).
func (s *Server) handleDownloadAgent(w http.ResponseWriter, r *http.Request) {
	if !auth.TokenEqual(r.URL.Query().Get("token"), s.cfg.EnrollToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	os := r.URL.Query().Get("os")
	arch := r.URL.Query().Get("arch")
	kind := r.URL.Query().Get("kind") // "tray" for the Windows GUI build
	if os == "" {
		os = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	bin, ok := agentBinKind(os, arch, kind)
	if !ok {
		http.Error(w, fmt.Sprintf("agent binary for %s/%s is not bundled in this build", os, arch), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+agentBinFile(os, arch, kind))
	w.Write(bin)
}

// handleInstallScript serves a self-contained agent installer with the hub URL
// and enroll token baked in. ?desktop=1 installs a graphical (screen-sharing)
// user-session agent instead of a headless system service.
func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	w.Header().Set("Content-Type", "text/x-shellscript")
	if token == "" {
		w.Write([]byte("#!/bin/sh\necho 'error: missing enroll token — copy the command from the dashboard Add-host button' >&2\nexit 1\n"))
		return
	}
	tmpl := installSh
	if r.URL.Query().Get("desktop") == "1" {
		tmpl = installShDesktop
	}
	out := strings.ReplaceAll(tmpl, "__SERVER__", baseURL(r))
	out = strings.ReplaceAll(out, "__TOKEN__", token)
	w.Write([]byte(out))
}

func (s *Server) handleInstallPS1(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	w.Header().Set("Content-Type", "text/plain")
	if token == "" {
		w.Write([]byte("Write-Error 'missing enroll token — copy the command from the dashboard Add-host button'\n"))
		return
	}
	out := strings.ReplaceAll(installPs1, "__SERVER__", baseURL(r))
	out = strings.ReplaceAll(out, "__TOKEN__", token)
	w.Write([]byte(out))
}

func (s *Server) handleInstallElevatedPS1(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	w.Header().Set("Content-Type", "text/plain")
	if token == "" {
		w.Write([]byte("Write-Error 'missing enroll token'\n"))
		return
	}
	out := strings.ReplaceAll(installElevatedPs1, "__SERVER__", baseURL(r))
	out = strings.ReplaceAll(out, "__TOKEN__", token)
	w.Write([]byte(out))
}

// handleEnroll returns the ready-to-paste install commands for the dashboard.
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	base := baseURL(r)
	tok := s.cfg.EnrollToken
	_, haveLinux := agentBin("linux", "amd64")
	_, haveWin := agentBin("windows", "amd64")
	writeJSON(w, http.StatusOK, map[string]any{
		"server_url":   base,
		"enroll_token": tok,
		"bundled":      haveLinux || haveWin,
		"commands": map[string]string{
			"linux":            fmt.Sprintf(`curl -fsSL "%s/install.sh?token=%s" | sudo sh`, base, tok),
			"linux_desktop":    fmt.Sprintf(`curl -fsSL "%s/install.sh?token=%s&desktop=1" | sh`, base, tok),
			"windows":          fmt.Sprintf(`iwr -useb "%s/install.ps1?token=%s" | iex`, base, tok),
			"windows_elevated": fmt.Sprintf(`iwr -useb "%s/install-elevated.ps1?token=%s" | iex`, base, tok),
		},
	})
}

const installSh = `#!/bin/sh
# autormm agent installer (headless system service). Run with root/sudo.
set -e
SERVER="__SERVER__"
TOKEN="__TOKEN__"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64;;
  aarch64|arm64) ARCH=arm64;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;;
esac
echo "Downloading autormm-agent ($ARCH) from $SERVER ..."
curl -fsSL "$SERVER/download/agent?os=linux&arch=$ARCH&token=$TOKEN" -o /tmp/autormm-agent.new
install -m 0755 /tmp/autormm-agent.new /usr/local/bin/autormm-agent
rm -f /tmp/autormm-agent.new
mkdir -p /etc/autormm
printf 'AUTORMM_SERVER=%s\nAUTORMM_ENROLL_TOKEN=%s\n' "$SERVER" "$TOKEN" > /etc/autormm/agent.env
chmod 600 /etc/autormm/agent.env
cat > /etc/systemd/system/autormm-agent.service <<'UNIT'
[Unit]
Description=autormm agent
After=network-online.target
Wants=network-online.target
[Service]
EnvironmentFile=/etc/autormm/agent.env
ExecStart=/usr/local/bin/autormm-agent
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable autormm-agent
# restart (not just enable --now) so re-running this command upgrades in place.
systemctl restart autormm-agent
echo "autormm-agent installed and running. It should appear on the dashboard shortly."
`

const installShDesktop = `#!/bin/sh
# autormm agent installer (graphical desktop, per-user; enables screen sharing).
set -e
SERVER="__SERVER__"
TOKEN="__TOKEN__"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64;;
  aarch64|arm64) ARCH=arm64;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;;
esac
mkdir -p "$HOME/.local/bin" "$HOME/.config/autormm" "$HOME/.config/systemd/user"
echo "Downloading autormm-agent ($ARCH) from $SERVER ..."
curl -fsSL "$SERVER/download/agent?os=linux&arch=$ARCH&token=$TOKEN" -o "$HOME/.local/bin/autormm-agent"
chmod 0755 "$HOME/.local/bin/autormm-agent"
printf 'AUTORMM_SERVER=%s\nAUTORMM_ENROLL_TOKEN=%s\n' "$SERVER" "$TOKEN" > "$HOME/.config/autormm/agent.env"
chmod 600 "$HOME/.config/autormm/agent.env"
cat > "$HOME/.config/systemd/user/autormm-agent.service" <<'UNIT'
[Unit]
Description=autormm agent (desktop)
After=graphical-session.target
[Service]
EnvironmentFile=%h/.config/autormm/agent.env
Environment=DISPLAY=:0
Environment=XAUTHORITY=%h/.Xauthority
ExecStart=%h/.local/bin/autormm-agent
Restart=always
RestartSec=5
[Install]
WantedBy=default.target
UNIT
loginctl enable-linger "$USER" >/dev/null 2>&1 || true
systemctl --user daemon-reload
systemctl --user enable autormm-agent
# restart (not just enable --now) so re-running this command upgrades in place.
systemctl --user restart autormm-agent
echo "autormm-agent (desktop) installed and running."
`

// installPs1 installs the Windows desktop agent as a system-tray app that starts
// at logon via a per-user Run key. No admin, no scheduled task — the tray app
// registers its own autostart on first launch.
const installPs1 = `# autormm agent installer for Windows (system-tray app, starts at logon). No admin needed.
$ErrorActionPreference = 'Stop'
$Server = '__SERVER__'
$Token  = '__TOKEN__'
$dir = Join-Path $env:LOCALAPPDATA 'autormm'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$dest = Join-Path $dir 'autormm-agent-tray.exe'
# Stop any previous instance so the exe isn't locked while we overwrite it.
Get-Process autormm-agent-tray -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 300
Write-Host "Downloading autormm agent from $Server ..."
Invoke-WebRequest -Uri "$Server/download/agent?os=windows&arch=amd64&kind=tray&token=$Token" -OutFile $dest -UseBasicParsing
Start-Process -FilePath $dest -ArgumentList "-server $Server -token $Token"
Write-Host 'autormm agent installed. A tray icon will appear, and it will start automatically at logon.'
`

// installElevatedPs1 installs the privileged helper as a LocalSystem Windows
// service, so the hub can run admin actions (service control, elevated commands)
// on this host. Requires a one-time elevated PowerShell.
const installElevatedPs1 = `# autormm elevated helper installer for Windows. Run in an ELEVATED PowerShell.
$ErrorActionPreference = 'Stop'
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) { Write-Warning 'autormm: run this in an ELEVATED PowerShell (Start > right-click Windows PowerShell > Run as administrator).'; return }
$Server = '__SERVER__'
$Token  = '__TOKEN__'
$dir = Join-Path $env:ProgramData 'autormm'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$dest = Join-Path $dir 'autormm-agent-svc.exe'
if (Get-Service autormm-elevated -ErrorAction SilentlyContinue) { Stop-Service autormm-elevated -Force -ErrorAction SilentlyContinue; sc.exe delete autormm-elevated | Out-Null; Start-Sleep -Milliseconds 700 }
Write-Host "Downloading autormm helper from $Server ..."
Invoke-WebRequest -Uri "$Server/download/agent?os=windows&arch=amd64&token=$Token" -OutFile $dest -UseBasicParsing
$bin = '"' + $dest + '" -server ' + $Server + ' -token ' + $Token + ' -elevated'
New-Service -Name autormm-elevated -DisplayName 'autormm elevated helper' -BinaryPathName $bin -StartupType Automatic | Out-Null
# Auto-restart on exit (e.g. after a self-update swaps the binary).
sc.exe failure autormm-elevated reset= 86400 actions= restart/5000/restart/5000/restart/5000 | Out-Null
Start-Service autormm-elevated
Write-Host 'autormm elevated helper installed (runs as SYSTEM). Admin actions (service control, elevated commands) are now available for this host.'
`
