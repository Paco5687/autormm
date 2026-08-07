package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Recent errors and warnings from the host's own log. Read on demand through
// the existing exec plumbing rather than collected continuously: this is the
// "what happened just before it went wrong" question, asked occasionally, and
// shipping every host's log to the hub forever would be a different and much
// larger feature.
const (
	// -p 3..4 is errors and warnings; -b limits to this boot so a machine up for
	// months does not return last spring. No pager, and a bounded line count so
	// a screaming service cannot return a hundred megabytes.
	unixEventLog = `
if command -v journalctl >/dev/null 2>&1; then
  journalctl -p 3..4 -b --no-pager -n 200 --output=short-iso 2>/dev/null | tail -n 200
elif [ -f /var/log/syslog ]; then
  grep -iE 'error|warn|fail' /var/log/syslog 2>/dev/null | tail -n 200
elif [ -f /var/log/messages ]; then
  grep -iE 'error|warn|fail' /var/log/messages 2>/dev/null | tail -n 200
else
  echo "no journalctl or syslog on this host"
fi`

	// System and Application, warnings and up, last 24 hours. Format-Table with
	// an explicit width because PowerShell otherwise truncates the message to
	// the console width it imagines it has, which is exactly the column that
	// matters.
	windowsEventLog = `
$since = (Get-Date).AddDays(-1)
$ev = Get-WinEvent -FilterHashtable @{LogName='System','Application'; Level=1,2,3; StartTime=$since} -MaxEvents 200 -ErrorAction SilentlyContinue
if (-not $ev) { 'No errors or warnings in the last 24 hours.'; exit 0 }
$ev | ForEach-Object {
  '{0:yyyy-MM-dd HH:mm}  {1,-8} {2,-22} {3}' -f $_.TimeCreated, $_.LevelDisplayName, $_.ProviderName, ($_.Message -replace '\s+', ' ')
} | Out-String -Width 400`
)

// eventLogCommand returns the shell and command that dump recent problems.
func eventLogCommand(osName string) (shell, cmd string, ok bool) {
	switch osName {
	case "linux", "darwin":
		return "sh", unixEventLog, true
	case "windows":
		return "powershell", windowsEventLog, true
	}
	return "", "", false
}

// handleEventLog returns the host's recent errors and warnings.
func (s *Server) handleEventLog(w http.ResponseWriter, r *http.Request) {
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
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	shell, cmd, ok := eventLogCommand(s.store.osFor(req.AgentID))
	if !ok {
		http.Error(w, "log reading is not supported on this host", http.StatusBadRequest)
		return
	}
	// Generous timeout: Get-WinEvent over a busy Application log is not quick.
	res, err := s.runOnAgent(req.AgentID, cmd, shell, 120)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "output": err.Error()})
		return
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		out = strings.TrimSpace(res.Stderr)
	}
	if out == "" {
		out = "Nothing recent."
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out, "truncated": res.Truncated})
}
