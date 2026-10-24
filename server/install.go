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
// user-session agent instead of a headless system service, and ?autologin=1
// additionally has that desktop log itself in at boot, so the host is reachable
// without someone having signed in at its screen.
func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	w.Header().Set("Content-Type", "text/x-shellscript")
	if token == "" {
		w.Write([]byte("#!/bin/sh\necho 'error: missing enroll token — copy the command from the dashboard Add-host button' >&2\nexit 1\n"))
		return
	}
	tmpl := installSh
	switch {
	case r.URL.Query().Get("os") == "darwin":
		tmpl = installShMac
	case r.URL.Query().Get("desktop") == "1":
		tmpl = installShDesktop
	}
	out := strings.ReplaceAll(tmpl, "__SERVER__", baseURL(r))
	out = strings.ReplaceAll(out, "__TOKEN__", token)
	// Only the desktop template carries this, and only ?autologin=1 turns it on:
	// the install must not quietly change how the machine boots.
	autologin := "0"
	if r.URL.Query().Get("autologin") == "1" {
		autologin = "1"
	}
	out = strings.ReplaceAll(out, "__AUTOLOGIN__", autologin)
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
			"linux":         fmt.Sprintf(`curl -fsSL "%s/install.sh?token=%s" | sudo sh`, base, tok),
			"linux_desktop": fmt.Sprintf(`curl -fsSL "%s/install.sh?token=%s&desktop=1" | sh`, base, tok),
			// Same install, plus GDM autologin: a Linux desktop has no display to
			// capture until someone signs in, so without this a rebooted host comes
			// back monitor-only.
			"linux_desktop_autologin": fmt.Sprintf(`curl -fsSL "%s/install.sh?token=%s&desktop=1&autologin=1" | sh`, base, tok),
			"windows":                 fmt.Sprintf(`iwr -useb "%s/install.ps1?token=%s" | iex`, base, tok),
			"windows_elevated":        fmt.Sprintf(`iwr -useb "%s/install-elevated.ps1?token=%s" | iex`, base, tok),
			// No sudo: a LaunchAgent belongs to the user's login session.
			"macos": fmt.Sprintf(`curl -fsSL "%s/install.sh?token=%s&os=darwin" | sh`, base, tok),
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

// installShMac installs the agent as a per-user LaunchAgent.
//
// A LaunchAgent, not a LaunchDaemon, on purpose: it runs inside the user's Aqua
// session, which is the only context that can capture the screen or inject
// input on macOS. A root LaunchDaemon has no such access no matter what
// permissions are granted, so starting there would mean migrating later.
const installShMac = `#!/bin/sh
# autormm agent installer for macOS (per-user LaunchAgent). Run WITHOUT sudo.
set -e
SERVER="__SERVER__"
TOKEN="__TOKEN__"

if [ "$(id -u)" = "0" ]; then
  echo "Run this without sudo: a LaunchAgent belongs to your login session," >&2
  echo "and installing it as root would put it in the wrong place." >&2
  exit 1
fi

case "$(uname -m)" in
  arm64) ARCH=arm64;;
  x86_64) ARCH=amd64;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;;
esac

BIN_DIR="$HOME/.local/bin"
PLIST="$HOME/Library/LaunchAgents/com.autormm.agent.plist"
LABEL="com.autormm.agent"
mkdir -p "$BIN_DIR" "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"

echo "Downloading autormm-agent (darwin/$ARCH) from $SERVER ..."
curl -fsSL "$SERVER/download/agent?os=darwin&arch=$ARCH&token=$TOKEN" -o "$BIN_DIR/autormm-agent.new"
chmod 0755 "$BIN_DIR/autormm-agent.new"
# Replace by rename so an already-running agent is not overwritten in place.
mv -f "$BIN_DIR/autormm-agent.new" "$BIN_DIR/autormm-agent"
# Downloaded binaries carry the quarantine flag, which stops launchd running it.
xattr -d com.apple.quarantine "$BIN_DIR/autormm-agent" 2>/dev/null || true

cat > "$PLIST" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN_DIR/autormm-agent</string>
    <string>-server</string><string>$SERVER</string>
    <string>-token</string><string>$TOKEN</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/autormm-agent.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/autormm-agent.log</string>
</dict>
</plist>
PLISTEOF
chmod 0600 "$PLIST"   # it contains the enroll token

UID_NUM="$(id -u)"
# bootout first so re-running this upgrades in place. Older macOS lacks
# bootstrap/bootout, so fall back to load/unload.
if launchctl bootout "gui/$UID_NUM/$LABEL" 2>/dev/null; then :; fi
if ! launchctl bootstrap "gui/$UID_NUM" "$PLIST" 2>/dev/null; then
  launchctl unload "$PLIST" 2>/dev/null || true
  launchctl load -w "$PLIST"
fi
launchctl enable "gui/$UID_NUM/$LABEL" 2>/dev/null || true

echo "autormm-agent installed and running. It should appear on the dashboard shortly."
echo "Log: ~/Library/Logs/autormm-agent.log"
`

// detectGraphicalSessionSh leaves the X display this user is logged into in
// DISP, the authority file that opens it in XAUTH, and the session's type in
// SESSION_TYPE.
//
// The desktop unit used to hardcode :0 and ~/.Xauthority, and a stock Ubuntu
// GDM desktop contradicts both often enough to matter: GDM keeps its authority
// file under /run/user/<uid>/gdm, and a session that was not the first on its
// seat lands on :1. Getting either wrong is quiet rather than loud — the agent
// starts, finds no display, registers can_stream=false, and the dashboard greys
// out the Remote button with nothing on screen to say why.
//
// The installer is run from an ssh login as often as from a terminal inside the
// desktop, so the environment is only the first guess: logind is the source
// that knows about a session this shell is not part of.
const detectGraphicalSessionSh = `
UID_NUM="$(id -u)"
RUNDIR="${XDG_RUNTIME_DIR:-/run/user/$UID_NUM}"
# A variable rather than a literal so the test can point the socket scan at a
# directory it is allowed to create. Nothing else sets it.
X11_SOCKETS="${AUTORMM_X11_SOCKETS:-/tmp/.X11-unix}"

# session_prop <session-id> <property> — one property of one logind session.
session_prop() {
  loginctl show-session "$1" -p "$2" 2>/dev/null | sed -n "s/^$2=//p"
}

SESSION_TYPE=""
DISP="$DISPLAY"
if command -v loginctl >/dev/null 2>&1; then
  SID=""
  # logind calls a user's graphical session their "display" session, and that is
  # the right answer when it has one -- but it names a session even when that
  # session is a tty, so the type has to be checked rather than trusted. This
  # installer is usually run over ssh, and an ssh login is a session too: it
  # must not shadow the desktop the agent is being installed for. Hence the
  # preferred id first, then every session this user owns, taking the first
  # graphical one.
  for s in $(loginctl show-user "$USER" -p Display 2>/dev/null | sed -n 's/^Display=//p') \
           $(loginctl list-sessions --no-legend 2>/dev/null | awk '{print $1}'); do
    [ "$(session_prop "$s" Name)" = "$USER" ] || continue
    case "$(session_prop "$s" Type)" in
      x11|wayland) SID="$s"; break;;
    esac
  done
  if [ -n "$SID" ]; then
    SESSION_TYPE="$(session_prop "$SID" Type)"
    sid_disp="$(session_prop "$SID" Display)"
    if [ -n "$sid_disp" ]; then DISP="$sid_disp"; fi
  fi
fi
# Nothing from logind: take the first X socket on the machine (normally the
# only one; a box with several has no way to say which is this user's).
if [ -z "$DISP" ]; then
  for sock in "$X11_SOCKETS"/X*; do
    [ -e "$sock" ] || continue
    DISP=":${sock##*/X}"
    break
  done
fi
[ -n "$DISP" ] || DISP=":0"

# The authority file: whatever the session exported, else the usual places. The
# glob covers display managers that keep theirs in a directory of their own
# under the runtime dir (lightdm, sddm) without having to name each one.
XAUTH=""
for cand in "$XAUTHORITY" "$RUNDIR/gdm/Xauthority" "$HOME/.Xauthority" "$RUNDIR"/*/Xauthority "$RUNDIR/Xauthority"; do
  if [ -n "$cand" ] && [ -r "$cand" ]; then
    XAUTH="$cand"
    break
  fi
done
[ -n "$XAUTH" ] || XAUTH="$HOME/.Xauthority"
`

// enableAutologinSh turns on GDM autologin for the installing user.
//
// This is what makes a desktop host reachable after a reboot. X is the login
// session on Linux: with nobody logged in there is no display to capture, so
// the agent registers can_stream=false and the hub can only grey the Remote
// button out — not a misconfiguration, just the shape of the platform. A
// machine that logs itself in has a session waiting whether or not anyone is
// sitting at it.
//
// Opt-in (?autologin=1) rather than part of every desktop install: it means the
// machine comes up past its login screen, which is a trade the operator has to
// make deliberately, not a side effect of enrolling a host.
const enableAutologinSh = `
AUTOLOGIN_CONF=""
# A variable rather than a literal list so the test can point this at a file it
# is allowed to rewrite. Nothing else sets it.
for c in ${AUTORMM_GDM_CONFS:-/etc/gdm3/custom.conf /etc/gdm/custom.conf}; do
  if [ -f "$c" ]; then AUTOLOGIN_CONF="$c"; break; fi
done
if [ -z "$AUTOLOGIN_CONF" ]; then
  echo "autormm: no GDM config found (looked in /etc/gdm3 and /etc/gdm), so autologin is not set." >&2
  echo "         Turn it on in your own display manager's configuration instead." >&2
elif ! sudo -n true 2>/dev/null; then
  echo "autormm: autologin needs root, and sudo here wants a password. Set it yourself:" >&2
  echo "           sudo nano $AUTOLOGIN_CONF" >&2
  echo "         and under [daemon] put AutomaticLoginEnable=true and AutomaticLogin=$USER" >&2
else
  # Keep one backup, from the first time this ran: re-running the installer must
  # not overwrite the original with an already-modified copy.
  if [ ! -f "$AUTOLOGIN_CONF.autormm.bak" ]; then
    sudo -n cp "$AUTOLOGIN_CONF" "$AUTOLOGIN_CONF.autormm.bak"
  fi
  # Three shapes: the keys are already there (set them), there is a [daemon]
  # section to put them in (Ubuntu ships one, with both keys commented out), or
  # neither and the section has to be written.
  if grep -qE '^[[:space:]]*AutomaticLoginEnable[[:space:]]*=' "$AUTOLOGIN_CONF"; then
    sudo -n sed -i "s|^[[:space:]]*AutomaticLoginEnable[[:space:]]*=.*|AutomaticLoginEnable=true|" "$AUTOLOGIN_CONF"
    if grep -qE '^[[:space:]]*AutomaticLogin[[:space:]]*=' "$AUTOLOGIN_CONF"; then
      sudo -n sed -i "s|^[[:space:]]*AutomaticLogin[[:space:]]*=.*|AutomaticLogin=$USER|" "$AUTOLOGIN_CONF"
    else
      sudo -n sed -i "/^[[:space:]]*AutomaticLoginEnable/a AutomaticLogin=$USER" "$AUTOLOGIN_CONF"
    fi
  elif grep -q '^\[daemon\]' "$AUTOLOGIN_CONF"; then
    sudo -n sed -i "/^\[daemon\]/a AutomaticLoginEnable=true\nAutomaticLogin=$USER" "$AUTOLOGIN_CONF"
  else
    printf '[daemon]\nAutomaticLoginEnable=true\nAutomaticLogin=%s\n' "$USER" | sudo -n tee -a "$AUTOLOGIN_CONF" >/dev/null
  fi
  echo "autormm: autologin enabled for $USER in $AUTOLOGIN_CONF"
  echo "         (original kept as $AUTOLOGIN_CONF.autormm.bak)."
  echo "         This machine now reaches its desktop at boot with no password typed."
  echo "         Lock the screen when you leave it: a locked X session still streams, so"
  echo "         you can unlock it from the viewer."
  if [ "$SESSION_TYPE" = "wayland" ]; then
    echo "autormm: autologin will start the same Wayland session, which cannot be captured." >&2
    echo "         Put WaylandEnable=false in $AUTOLOGIN_CONF as well." >&2
  fi
fi
`

const installShDesktop = `#!/bin/sh
# autormm agent installer (graphical desktop, per-user; enables screen sharing).
set -e
SERVER="__SERVER__"
TOKEN="__TOKEN__"
AUTOLOGIN="__AUTOLOGIN__"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64;;
  aarch64|arm64) ARCH=arm64;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1;;
esac
` + detectGraphicalSessionSh + `
if [ "$SESSION_TYPE" = "wayland" ]; then
  echo "autormm: this desktop is a Wayland session, and screen capture is X11-only." >&2
  echo "         The agent installs and reports metrics, but Remote stays greyed out." >&2
  echo "         Log in with 'Ubuntu on Xorg' (the gear on the login screen) to stream." >&2
fi

# A headless install on this same machine registers under the same agent id (the
# hostname) and claims the same connection slot on the hub, so the two evict each
# other on every reconnect and the host flaps between streamable and not. On a
# desktop, the agent that can actually see the screen is the one that should win.
if [ -f /etc/systemd/system/autormm-agent.service ]; then
  echo "autormm: a headless system agent is installed here; disabling it so the two do not fight."
  if sudo -n systemctl disable --now autormm-agent >/dev/null 2>&1; then
    echo "autormm: headless agent stopped."
  else
    echo "autormm: could not stop it without a password. Run this, then re-run the installer:" >&2
    echo "         sudo systemctl disable --now autormm-agent" >&2
  fi
fi

mkdir -p "$HOME/.local/bin" "$HOME/.config/autormm" "$HOME/.config/systemd/user"
echo "Downloading autormm-agent ($ARCH) from $SERVER ..."
curl -fsSL "$SERVER/download/agent?os=linux&arch=$ARCH&token=$TOKEN" -o "$HOME/.local/bin/autormm-agent"
chmod 0755 "$HOME/.local/bin/autormm-agent"
printf 'AUTORMM_SERVER=%s\nAUTORMM_ENROLL_TOKEN=%s\n' "$SERVER" "$TOKEN" > "$HOME/.config/autormm/agent.env"
chmod 600 "$HOME/.config/autormm/agent.env"
# Unquoted heredoc, so the detected display and authority are written in while
# %h is left for systemd to expand.
cat > "$HOME/.config/systemd/user/autormm-agent.service" <<UNIT
[Unit]
Description=autormm agent (desktop)
After=graphical-session.target
[Service]
EnvironmentFile=%h/.config/autormm/agent.env
Environment=DISPLAY=$DISP
Environment=XAUTHORITY=$XAUTH
ExecStart=%h/.local/bin/autormm-agent
Restart=always
RestartSec=5
[Install]
WantedBy=default.target
UNIT
loginctl enable-linger "$USER" >/dev/null 2>&1 || true
systemctl --user daemon-reload
systemctl --user enable autormm-agent
# Note the time before the restart, so the check below reads this run's log and
# not a verdict left by an earlier install.
STARTED="$(date '+%Y-%m-%d %H:%M:%S')"
# restart (not just enable --now) so re-running this command upgrades in place.
systemctl --user restart autormm-agent
echo "autormm-agent (desktop) installed: display $DISP, authority $XAUTH"

if [ "$AUTOLOGIN" = "1" ]; then
` + enableAutologinSh + `
elif [ -z "$SESSION_TYPE" ]; then
  # Nobody is logged in, so there is nothing to capture and a reboot will leave
  # it that way. Say so here rather than leaving a greyed-out button to explain
  # itself on the dashboard.
  echo "autormm: nobody is logged in at this machine's screen, so there is no display to"
  echo "         capture and Remote stays greyed out -- after every reboot, too."
  echo "         Re-run this command with '&autologin=1' on the end and the machine will"
  echo "         log itself in at boot. The Add-host dialog offers that version as well."
fi

# Read the verdict back rather than claiming success: the agent logs whether it
# found a capturable screen when it registers, and a wrong display is not a
# failure to start — it is stream=false, which is otherwise visible only as a
# greyed-out button on the dashboard.
if ! command -v journalctl >/dev/null 2>&1; then
  exit 0
fi
i=0
while [ "$i" -lt 15 ]; do
  state="$(journalctl --user -u autormm-agent --since "$STARTED" -n 30 --no-pager 2>/dev/null | grep -o 'stream=[a-z]*' | tail -1 || true)"
  case "$state" in
    stream=true)
      echo "autormm: screen sharing is live — Remote is available for this host."
      exit 0;;
    stream=false)
      echo "autormm: the agent is connected but reports it cannot capture this screen." >&2
      echo "         It is using DISPLAY=$DISP XAUTHORITY=$XAUTH." >&2
      echo "         Run 'echo \$DISPLAY; echo \$XAUTHORITY' in a terminal on the desktop" >&2
      echo "         itself, put those values in" >&2
      echo "         ~/.config/systemd/user/autormm-agent.service, and then:" >&2
      echo "           systemctl --user daemon-reload && systemctl --user restart autormm-agent" >&2
      exit 0;;
  esac
  i=$((i + 1))
  sleep 1
done
echo "autormm: installed, but the agent has not registered yet. Check it with:"
echo "           journalctl --user -u autormm-agent -n 30"
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
