package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// H.264 needs ffmpeg (with libx264) on the host. We deliberately do NOT ship it:
// an ffmpeg built with libx264 is GPL, and bundling it would impose that on this
// MIT project's binaries. Instead the operator asks the hub to fetch it from
// upstream onto their own machines — the licensing stays where it belongs and
// the agent download stays small.
//
// Override with -ffmpeg-url / AUTORMM_FFMPEG_URL to pin a build or use a mirror.
const DefaultFFmpegURL = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"

// windowsFFmpegInstall drops ffmpeg.exe beside the agent in ProgramData, which
// is where the agent looks before consulting PATH. Nothing is added to PATH and
// no package manager is involved — the agent that streams runs as SYSTEM, where
// winget is unreliable.
//
// It deliberately does not restart the agent: this script runs *through* the
// agent's exec channel, so a restart would kill it mid-run. None is needed —
// the viewer's codec list comes from the per-session caps message, so the next
// remote session sees H.264.
const windowsFFmpegInstall = `
$ErrorActionPreference = 'Stop'
$dir = Join-Path $env:ProgramData 'autormm'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$dest = Join-Path $dir 'ffmpeg.exe'

if (Test-Path $dest) {
  $have = & $dest -version 2>$null | Select-Object -First 1
  if ($have) { Write-Output ('already installed: ' + $have); exit 0 }
  Remove-Item $dest -Force -ErrorAction SilentlyContinue   # present but broken
}

$tmp = Join-Path $env:TEMP ('autormm-ffmpeg-' + [guid]::NewGuid().ToString() + '.zip')
$ex  = Join-Path $env:TEMP ('autormm-ffmpeg-' + [guid]::NewGuid().ToString())
try {
  Write-Output 'downloading ffmpeg...'
  [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
  Invoke-WebRequest -Uri '__URL__' -OutFile $tmp -UseBasicParsing
  Expand-Archive -Path $tmp -DestinationPath $ex -Force
  $src = Get-ChildItem -Path $ex -Recurse -Filter 'ffmpeg.exe' | Select-Object -First 1
  if (-not $src) { Write-Output 'no ffmpeg.exe inside the archive'; exit 1 }
  Copy-Item $src.FullName $dest -Force
} finally {
  Remove-Item $tmp -Force -ErrorAction SilentlyContinue
  Remove-Item $ex -Recurse -Force -ErrorAction SilentlyContinue
}

# Only keep it if it runs and can actually encode H.264.
$ver = & $dest -version 2>$null | Select-Object -First 1
if (-not $ver) { Remove-Item $dest -Force -ErrorAction SilentlyContinue; Write-Output 'the downloaded binary would not run'; exit 1 }
$enc = & $dest -hide_banner -encoders 2>$null | Select-String -Pattern 'libx264' -Quiet
if (-not $enc) { Remove-Item $dest -Force -ErrorAction SilentlyContinue; Write-Output 'that build has no libx264 encoder'; exit 1 }
Write-Output ('installed: ' + $ver)
`

// linuxFFmpegInstall uses the distro package, which keeps it updated with the
// system rather than pinning a private copy.
const linuxFFmpegInstall = `
if command -v ffmpeg >/dev/null 2>&1; then echo "already installed: $(ffmpeg -version 2>/dev/null | head -1)"; exit 0; fi
export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null 2>&1; then apt-get -qq update && apt-get -y install ffmpeg
elif command -v dnf >/dev/null 2>&1; then dnf -y install ffmpeg
else echo "no supported package manager"; exit 1
fi
ffmpeg -version 2>/dev/null | head -1`

// ffmpegInstallFor returns the script and shell that install ffmpeg on an OS.
func (s *Server) ffmpegInstallFor(osName string) (script, shell string, ok bool) {
	switch osName {
	case "windows":
		url := s.cfg.FFmpegURL
		if url == "" {
			url = DefaultFFmpegURL
		}
		return strings.ReplaceAll(windowsFFmpegInstall, "__URL__", url), "powershell", true
	case "linux":
		return linuxFFmpegInstall, "sh", true
	}
	return "", "", false
}

// handleFFmpegInstall installs the H.264 encoder on one host.
func (s *Server) handleFFmpegInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.AgentID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out, err := s.installFFmpeg(req.AgentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFFmpegInstallAll installs it on every online host that can stream.
func (s *Server) handleFFmpegInstallAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	results := []map[string]any{}
	for _, v := range s.store.views() {
		// Only streaming hosts benefit, and only while they are reachable.
		if !v.Online || !v.CanStream {
			continue
		}
		if _, _, ok := s.ffmpegInstallFor(v.OS); !ok {
			continue
		}
		out, err := s.installFFmpeg(v.AgentID)
		if err != nil {
			results = append(results, map[string]any{
				"agent_id": v.AgentID, "hostname": v.Hostname, "ok": false, "detail": err.Error(),
			})
			continue
		}
		out["agent_id"] = v.AgentID
		out["hostname"] = v.Hostname
		results = append(results, out)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "count": len(results)})
}

// installFFmpeg runs the per-OS install and summarises the outcome.
func (s *Server) installFFmpeg(agentID string) (map[string]any, error) {
	osName := s.store.osFor(agentID)
	script, shell, ok := s.ffmpegInstallFor(osName)
	if !ok {
		if osName == "" {
			return nil, fmt.Errorf("host is offline")
		}
		return nil, fmt.Errorf("installing the H.264 encoder is not supported on %s hosts", osName)
	}
	// Fetching and unpacking ffmpeg is slow on a cold link.
	res, err := s.runOnAgent(agentID, script, shell, 900)
	if err != nil {
		return nil, err
	}
	out := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
	return map[string]any{
		"ok":        res.ExitCode == 0,
		"os":        osName,
		"exit_code": res.ExitCode,
		"detail":    tailLines(out, 6),
	}, nil
}
