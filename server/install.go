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

func agentBin(os, arch string) ([]byte, bool) {
	b, err := agentBinsFS.ReadFile("agentbins/" + agentBinName(os, arch))
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
	if os == "" {
		os = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	bin, ok := agentBin(os, arch)
	if !ok {
		http.Error(w, fmt.Sprintf("agent binary for %s/%s is not bundled in this build", os, arch), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+agentBinName(os, arch))
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
			"linux":         fmt.Sprintf(`curl -fsSL "%s/install.sh?token=%s" | sudo sh`, base, tok),
			"linux_desktop": fmt.Sprintf(`curl -fsSL "%s/install.sh?token=%s&desktop=1" | sh`, base, tok),
			"windows":       fmt.Sprintf(`iwr -useb "%s/install.ps1?token=%s" | iex`, base, tok),
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
systemctl enable --now autormm-agent
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
systemctl --user enable --now autormm-agent
echo "autormm-agent (desktop) installed and running."
`

const installPs1 = `# autormm agent installer for Windows. Run in PowerShell.
$ErrorActionPreference = 'Stop'
$Server = '__SERVER__'
$Token  = '__TOKEN__'
$dir = Join-Path $env:LOCALAPPDATA 'autormm'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$dest = Join-Path $dir 'autormm-agent.exe'
Write-Host "Downloading autormm-agent from $Server ..."
Invoke-WebRequest -Uri "$Server/download/agent?os=windows&arch=amd64&token=$Token" -OutFile $dest -UseBasicParsing
$action  = New-ScheduledTaskAction -Execute $dest -Argument "-server $Server -token $Token"
$trigger = New-ScheduledTaskTrigger -AtLogOn
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName 'autormm-agent' -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
Start-ScheduledTask -TaskName 'autormm-agent'
Write-Host 'autormm-agent installed and started.'
`
