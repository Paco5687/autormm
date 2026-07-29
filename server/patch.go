package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Linux patch scripts. They auto-detect apt vs dnf so we don't need to know the
// exact distro. The Linux agent runs as root, so these just work.
const linuxPatchStatus = `
if command -v apt-get >/dev/null 2>&1; then
  apt-get -qq update >/dev/null 2>&1
  N=$(apt-get -s upgrade 2>/dev/null | grep -c '^Inst')
  S=$(apt-get -s upgrade 2>/dev/null | grep '^Inst' | grep -ci security)
  R=no; [ -f /var/run/reboot-required ] && R=yes
  echo "MGR=apt UPDATES=$N SECURITY=$S REBOOT=$R"
elif command -v dnf >/dev/null 2>&1; then
  N=$(dnf -q check-update 2>/dev/null | grep -cE '^[[:alnum:]][[:alnum:]._+-]*[[:space:]]')
  R=no; if command -v needs-restarting >/dev/null 2>&1; then needs-restarting -r >/dev/null 2>&1 || R=yes; fi
  echo "MGR=dnf UPDATES=$N SECURITY=0 REBOOT=$R"
else
  echo "MGR=none UPDATES=0 SECURITY=0 REBOOT=no"
fi`

const linuxPatchInstall = `
export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null 2>&1; then
  apt-get -qq update && apt-get -y -o Dpkg::Options::=--force-confold upgrade
elif command -v dnf >/dev/null 2>&1; then
  dnf -y upgrade
else
  echo "no supported package manager"; exit 1
fi`

const linuxReboot = `( sleep 2; systemctl reboot || reboot ) >/dev/null 2>&1 & echo "reboot scheduled"`

// Windows patch scripts drive the Windows Update Agent COM API, which ships
// with the OS — no PSWindowsUpdate module or other download required. Searching
// works as a normal user; downloading and installing need SYSTEM, which is what
// the elevated helper (#49) provides, so those are gated on it being installed.
const windowsPatchStatus = `
$ErrorActionPreference = 'Stop'
try {
  $s = New-Object -ComObject Microsoft.Update.Session
  $found = $s.CreateUpdateSearcher().Search('IsInstalled=0 and IsHidden=0')
} catch {
  Write-Output ('ERR=' + $_.Exception.Message); exit 1
}
# Software updates only (UpdateType 1); driver updates (2) are deliberately
# left to the console user, since a bad remote driver push can strand a host.
$updates = @($found.Updates | Where-Object { $_.Type -eq 1 })
$sec = 0
foreach ($u in $updates) {
  foreach ($c in $u.Categories) { if ($c.Name -like '*Security*') { $sec++; break } }
}
$reboot = 'no'
$keys = @(
  'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired',
  'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending'
)
foreach ($k in $keys) { if (Test-Path $k) { $reboot = 'yes' } }
Write-Output ('MGR=wu UPDATES=' + $updates.Count + ' SECURITY=' + $sec + ' REBOOT=' + $reboot)`

const windowsPatchInstall = `
$ErrorActionPreference = 'Stop'
try {
  $s = New-Object -ComObject Microsoft.Update.Session
  $found = $s.CreateUpdateSearcher().Search('IsInstalled=0 and IsHidden=0')
  # Software updates only -- see the status script for why drivers are skipped.
  $updates = @($found.Updates | Where-Object { $_.Type -eq 1 })
  if ($updates.Count -eq 0) { Write-Output 'no updates available'; exit 0 }

  $wanted = New-Object -ComObject Microsoft.Update.UpdateColl
  foreach ($u in $updates) {
    if (-not $u.EulaAccepted) { try { $u.AcceptEula() } catch {} }
    $null = $wanted.Add($u)
  }
  Write-Output ('downloading ' + $wanted.Count + ' update(s)...')
  $d = $s.CreateUpdateDownloader(); $d.Updates = $wanted
  $null = $d.Download()

  $ready = New-Object -ComObject Microsoft.Update.UpdateColl
  foreach ($u in $updates) { if ($u.IsDownloaded) { $null = $ready.Add($u) } }
  if ($ready.Count -eq 0) { Write-Output 'no updates could be downloaded'; exit 1 }
  foreach ($u in $ready) { Write-Output ('  ' + $u.Title) }

  $i = $s.CreateUpdateInstaller(); $i.Updates = $ready
  $res = $i.Install()
  Write-Output ('installed=' + $ready.Count + ' result=' + $res.ResultCode + ' reboot=' + $res.RebootRequired)
  # ResultCode 2 = succeeded, 3 = succeeded with errors; anything else failed.
  if ($res.ResultCode -eq 2 -or $res.ResultCode -eq 3) { exit 0 } else { exit 1 }
} catch {
  # Common here: 0x80240016 (another install in progress) and access denied when
  # this ran without the elevated helper.
  Write-Output ('install failed: ' + $_.Exception.Message)
  exit 1
}`

const windowsReboot = `shutdown.exe /r /t 5 /c "autormm: rebooting to finish updates" | Out-Null; Write-Output 'reboot scheduled'`

// patchPlan is the per-OS recipe for the three patch operations.
type patchPlan struct {
	shell          string
	status         string
	install        string
	reboot         string
	installTimeout int
	// needsElevated marks platforms where installing requires the privileged
	// helper rather than the ordinary user-session agent.
	needsElevated bool
}

func patchPlanFor(osName string) (patchPlan, bool) {
	switch osName {
	case "linux":
		return patchPlan{
			shell: "sh", status: linuxPatchStatus, install: linuxPatchInstall, reboot: linuxReboot,
			installTimeout: 600,
		}, true
	case "windows":
		return patchPlan{
			shell: "powershell", status: windowsPatchStatus, install: windowsPatchInstall, reboot: windowsReboot,
			installTimeout: 3600, needsElevated: true,
		}, true
	}
	return patchPlan{}, false
}

const elevatedHelperHint = "install the Windows elevated helper on this host (Add host → Windows elevated helper) — Windows Update needs SYSTEM"

func (s *Server) handlePatchStatus(w http.ResponseWriter, r *http.Request) {
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	osName := s.store.osFor(agentID)
	if osName == "" {
		http.Error(w, "host offline", http.StatusConflict)
		return
	}
	plan, ok := patchPlanFor(osName)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false, "os": osName,
			"note": "patching is not supported on " + osName + " hosts"})
		return
	}
	// Searching for updates works without the helper, so report status either
	// way and let the client show why the buttons are disabled.
	canInstall := !plan.needsElevated || s.store.hasElevated(agentID)
	res, err := s.runOnAgent(agentID, plan.status, plan.shell, 300)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if e := patchError(res.Stdout, res.Stderr, res.ExitCode); e != "" {
		writeJSON(w, http.StatusOK, map[string]any{"supported": true, "os": osName,
			"can_install": canInstall, "error": e})
		return
	}
	upd, sec, reboot := parsePatchStatus(res.Stdout)
	out := map[string]any{
		"supported": true, "os": osName,
		"updates": upd, "security": sec, "reboot_required": reboot,
		"can_install": canInstall,
	}
	if !canInstall {
		out["note"] = elevatedHelperHint
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePatchInstall(w http.ResponseWriter, r *http.Request) {
	agentID, plan, ok := s.patchTarget(w, r)
	if !ok {
		return
	}
	res, err := s.runOnAgent(agentID, plan.install, plan.shell, plan.installTimeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": res.ExitCode == 0, "exit_code": res.ExitCode,
		"output": tailLines(strings.TrimSpace(res.Stdout+"\n"+res.Stderr), 50),
	})
}

func (s *Server) handlePatchReboot(w http.ResponseWriter, r *http.Request) {
	agentID, plan, ok := s.patchTarget(w, r)
	if !ok {
		return
	}
	_, err := s.runOnAgent(agentID, plan.reboot, plan.shell, 30)
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil})
}

// patchTarget validates a POST patch request and returns the target host's id
// and patch recipe.
func (s *Server) patchTarget(w http.ResponseWriter, r *http.Request) (string, patchPlan, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return "", patchPlan{}, false
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", patchPlan{}, false
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return "", patchPlan{}, false
	}
	osName := s.store.osFor(req.AgentID)
	plan, ok := patchPlanFor(osName)
	if !ok {
		http.Error(w, "patching is not supported on this host", http.StatusBadRequest)
		return "", patchPlan{}, false
	}
	if plan.needsElevated && !s.store.hasElevated(req.AgentID) {
		http.Error(w, elevatedHelperHint, http.StatusConflict)
		return "", patchPlan{}, false
	}
	return req.AgentID, plan, true
}

// patchError extracts a failure reason from a status run: the Windows script
// reports COM failures as ERR=..., and a non-zero exit with no parsable output
// means something else went wrong.
func patchError(stdout, stderr string, exitCode int) string {
	for _, line := range strings.Split(stdout, "\n") {
		if v, found := strings.CutPrefix(strings.TrimSpace(line), "ERR="); found {
			return v
		}
	}
	if exitCode != 0 && !strings.Contains(stdout, "UPDATES=") {
		if msg := strings.TrimSpace(stderr); msg != "" {
			return tailLines(msg, 3)
		}
		return "patch check failed (exit " + strconv.Itoa(exitCode) + ")"
	}
	return ""
}

func parsePatchStatus(out string) (updates, security int, reboot bool) {
	for _, f := range strings.Fields(out) {
		kv := strings.SplitN(f, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "UPDATES":
			updates, _ = strconv.Atoi(kv[1])
		case "SECURITY":
			security, _ = strconv.Atoi(kv[1])
		case "REBOOT":
			reboot = kv[1] == "yes"
		}
	}
	return
}

// tailLines returns the last n lines of s.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
