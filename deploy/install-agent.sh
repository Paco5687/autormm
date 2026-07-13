#!/usr/bin/env bash
# Install the autormm agent on a Linux host.
#
# Usage:
#   sudo ./install-agent.sh --server http://HUB-IP:8765 --token ENROLL [--id NAME] [--tags a,b]
#   ./install-agent.sh --desktop --server ... --token ...     # graphical host (per-user, no sudo)
#
# Expects the autormm-agent binary next to this script (or pass --bin PATH).
set -euo pipefail

SERVER="" TOKEN="" ID="" TAGS="" DESKTOP=0 INSECURE=0
BIN="$(cd "$(dirname "$0")" && pwd)/autormm-agent"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server) SERVER="$2"; shift 2;;
    --token)  TOKEN="$2";  shift 2;;
    --id)     ID="$2";     shift 2;;
    --tags)   TAGS="$2";   shift 2;;
    --bin)    BIN="$2";    shift 2;;
    --desktop) DESKTOP=1;  shift;;
    --insecure) INSECURE=1; shift;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

[[ -n "$SERVER" && -n "$TOKEN" ]] || { echo "--server and --token are required" >&2; exit 2; }
[[ -f "$BIN" ]] || { echo "agent binary not found at $BIN (use --bin)" >&2; exit 2; }

write_env() {
  local f="$1"
  {
    echo "AUTORMM_SERVER=$SERVER"
    echo "AUTORMM_ENROLL_TOKEN=$TOKEN"
    [[ -n "$ID" ]]   && echo "AUTORMM_AGENT_ID=$ID"
    [[ -n "$TAGS" ]] && echo "AUTORMM_TAGS=$TAGS"
    [[ "$INSECURE" == 1 ]] && echo "AUTORMM_INSECURE=1"
  } > "$f"
  chmod 600 "$f"
}

if [[ "$DESKTOP" == 1 ]]; then
  # Per-user install into the graphical session (enables screen streaming).
  mkdir -p "$HOME/.local/bin" "$HOME/.config/autormm" "$HOME/.config/systemd/user"
  install -m 0755 "$BIN" "$HOME/.local/bin/autormm-agent"
  write_env "$HOME/.config/autormm/agent.env"
  install -m 0644 "$(dirname "$0")/systemd/autormm-agent-user.service" \
    "$HOME/.config/systemd/user/autormm-agent-user.service"
  loginctl enable-linger "$USER" >/dev/null 2>&1 || true
  systemctl --user daemon-reload
  systemctl --user enable --now autormm-agent-user
  echo "Installed desktop agent. Status: systemctl --user status autormm-agent-user"
else
  # System install (headless / metrics-only).
  [[ $EUID -eq 0 ]] || { echo "run as root for a system install (or use --desktop)" >&2; exit 1; }
  install -m 0755 "$BIN" /usr/local/bin/autormm-agent
  mkdir -p /etc/autormm /var/lib/autormm
  write_env /etc/autormm/agent.env
  install -m 0644 "$(dirname "$0")/systemd/autormm-agent.service" /etc/systemd/system/autormm-agent.service
  systemctl daemon-reload
  systemctl enable --now autormm-agent
  echo "Installed system agent. Status: systemctl status autormm-agent"
fi
