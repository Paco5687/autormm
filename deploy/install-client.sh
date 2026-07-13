#!/usr/bin/env bash
# Install the autormm client CLI (Linux/macOS) and optionally log in.
#
# Usage:
#   ./install-client.sh [--server URL] [--token TOKEN] [--prefix ~/.local]
#
# Expects the autormm-client binary next to this script (or pass --bin PATH).
set -euo pipefail

SERVER="" TOKEN="" PREFIX="$HOME/.local"
BIN="$(cd "$(dirname "$0")" && pwd)/autormm-client"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server) SERVER="$2"; shift 2;;
    --token)  TOKEN="$2";  shift 2;;
    --prefix) PREFIX="$2"; shift 2;;
    --bin)    BIN="$2";    shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

[[ -f "$BIN" ]] || { echo "client binary not found at $BIN (use --bin)" >&2; exit 2; }

BINDIR="$PREFIX/bin"
mkdir -p "$BINDIR"
install -m 0755 "$BIN" "$BINDIR/autormm-client"
echo "Installed autormm-client to $BINDIR/autormm-client"

case ":$PATH:" in
  *":$BINDIR:"*) ;;
  *) echo "note: $BINDIR is not on your PATH — add it to use 'autormm-client' directly.";;
esac

if [[ -n "$SERVER" && -n "$TOKEN" ]]; then
  "$BINDIR/autormm-client" login --server "$SERVER" --token "$TOKEN"
else
  echo "Run 'autormm-client login' to configure the server URL and token."
fi
