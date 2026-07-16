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
	if osName != "linux" {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false, "os": osName,
			"note": "patching is Linux-only for now — Windows needs an elevated agent (#49)"})
		return
	}
	res, err := s.runOnAgent(agentID, linuxPatchStatus, "sh", 120)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	upd, sec, reboot := parsePatchStatus(res.Stdout)
	writeJSON(w, http.StatusOK, map[string]any{
		"supported": true, "os": osName,
		"updates": upd, "security": sec, "reboot_required": reboot,
	})
}

func (s *Server) handlePatchInstall(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.patchTarget(w, r)
	if !ok {
		return
	}
	res, err := s.runOnAgent(agentID, linuxPatchInstall, "sh", 600)
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
	agentID, ok := s.patchTarget(w, r)
	if !ok {
		return
	}
	_, err := s.runOnAgent(agentID, linuxReboot, "sh", 30)
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil})
}

// patchTarget validates a POST patch request and returns the (Linux) agent id.
func (s *Server) patchTarget(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return "", false
	}
	if !s.checkAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return "", false
	}
	if s.store.osFor(req.AgentID) != "linux" {
		http.Error(w, "patching is only supported on Linux hosts for now", http.StatusBadRequest)
		return "", false
	}
	return req.AgentID, true
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
