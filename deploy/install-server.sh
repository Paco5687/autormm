#!/usr/bin/env bash
# Install the autormm hub server as a systemd *user* service.
# Generates strong tokens on first run.
#
# Usage:
#   ./install-server.sh [--addr 0.0.0.0:8765] [--admin-token T] [--enroll-token T]
#
# Expects the autormm-server binary next to this script (or pass --bin PATH).
set -euo pipefail

ADDR="0.0.0.0:8765" ADMIN="" ENROLL=""
BIN="$(cd "$(dirname "$0")" && pwd)/autormm-server"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --addr)         ADDR="$2";   shift 2;;
    --admin-token)  ADMIN="$2";  shift 2;;
    --enroll-token) ENROLL="$2"; shift 2;;
    --bin)          BIN="$2";    shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

[[ -f "$BIN" ]] || { echo "server binary not found at $BIN (use --bin)" >&2; exit 2; }

gen() { openssl rand -base64 24 2>/dev/null | tr -d '/+=' | cut -c1-28 || head -c 18 /dev/urandom | base64 | tr -d '/+='; }
[[ -n "$ADMIN"  ]] || ADMIN="$(gen)"
[[ -n "$ENROLL" ]] || ENROLL="$(gen)"

BINDIR="$HOME/.local/bin"
CFGDIR="$HOME/.config/autormm"
UNITDIR="$HOME/.config/systemd/user"
mkdir -p "$BINDIR" "$CFGDIR" "$UNITDIR"

install -m 0755 "$BIN" "$BINDIR/autormm-server"

ENVFILE="$CFGDIR/server.env"
if [[ -f "$ENVFILE" ]]; then
  echo "Keeping existing $ENVFILE (delete it to regenerate tokens)."
else
  cat > "$ENVFILE" <<EOF
AUTORMM_ADMIN_TOKEN=$ADMIN
AUTORMM_ENROLL_TOKEN=$ENROLL
EOF
  chmod 600 "$ENVFILE"
fi

# Install the unit with the requested listen address.
sed "s#-addr 0.0.0.0:8765#-addr ${ADDR}#" \
  "$(dirname "$0")/systemd/autormm-server.service" > "$UNITDIR/autormm-server.service"

systemctl --user daemon-reload
loginctl enable-linger "$USER" >/dev/null 2>&1 || true
systemctl --user enable autormm-server
# restart (not just `enable --now`) so re-running this to upgrade actually
# swaps the running binary instead of leaving the old process serving.
systemctl --user restart autormm-server

echo
echo "autormm-server installed and running on ${ADDR}."
echo "  status:  systemctl --user status autormm-server"
echo "  config:  $ENVFILE"
if grep -q "$ADMIN" "$ENVFILE"; then
  echo
  echo "  ADMIN TOKEN  (clients/dashboard): $(grep AUTORMM_ADMIN_TOKEN "$ENVFILE" | cut -d= -f2)"
  echo "  ENROLL TOKEN (agents):            $(grep AUTORMM_ENROLL_TOKEN "$ENVFILE" | cut -d= -f2)"
fi
echo
echo "Next: put it behind Traefik with deploy/traefik-autormm.yml (LAN-only)."
