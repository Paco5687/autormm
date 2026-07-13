#!/usr/bin/env bash
# autormm one-line bootstrap installer (Linux/macOS).
#
# Downloads the latest release archive for this platform, verifies its checksum,
# and runs the installer for the requested piece.
#
#   curl -fsSL <url>/deploy/get.sh | bash -s -- server
#   curl -fsSL <url>/deploy/get.sh | bash -s -- agent --server https://... --token ENROLL
#   curl -fsSL <url>/deploy/get.sh | bash -s -- agent-desktop --server https://... --token ENROLL
#   curl -fsSL <url>/deploy/get.sh | bash -s -- client --server https://... --token ADMIN
#
# If `gh` is authenticated it is used to fetch assets (also works for private
# forks); otherwise it falls back to the public GitHub API. Set
# AUTORMM_VERSION=vX.Y.Z to pin a release (default: latest).
set -euo pipefail

REPO="${AUTORMM_REPO:-Paco5687/autormm}"
PIECE="${1:-}"; shift || true
[[ -n "$PIECE" ]] || { echo "usage: get.sh <server|agent|agent-desktop|client> [installer args...]" >&2; exit 2; }

os="$(uname -s | tr '[:upper:]' '[:lower:]')"   # linux | darwin
case "$(uname -m)" in
  x86_64|amd64) arch=amd64;;
  aarch64|arm64) arch=arm64;;
  *) echo "unsupported arch: $(uname -m)" >&2; exit 1;;
esac
[[ "$os" == "linux" || "$os" == "darwin" ]] || { echo "unsupported OS: $os" >&2; exit 1; }

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
pattern="autormm_*_${os}_${arch}.tar.gz"

echo "Fetching autormm (${os}/${arch})…"
if command -v gh >/dev/null 2>&1; then
  args=(release download --repo "$REPO" --dir "$tmp" --pattern "$pattern" --pattern "SHA256SUMS" --clobber)
  [[ -n "${AUTORMM_VERSION:-}" ]] && args+=("$AUTORMM_VERSION")
  gh "${args[@]}"
else
  # curl fallback (works once the repo/release is public, or with a token).
  api="https://api.github.com/repos/${REPO}/releases/${AUTORMM_VERSION:+tags/$AUTORMM_VERSION}"
  [[ -n "${AUTORMM_VERSION:-}" ]] || api="https://api.github.com/repos/${REPO}/releases/latest"
  auth=(); [[ -n "${GITHUB_TOKEN:-}" ]] && auth=(-H "Authorization: Bearer $GITHUB_TOKEN")
  urls="$(curl -fsSL "${auth[@]}" "$api" | grep -oE '"browser_download_url": *"[^"]+"' | cut -d'"' -f4)"
  for u in $urls; do
    case "$u" in
      *_${os}_${arch}.tar.gz|*/SHA256SUMS) (cd "$tmp" && curl -fsSLO "${auth[@]}" "$u");;
    esac
  done
fi

archive="$(ls "$tmp"/autormm_*_${os}_${arch}.tar.gz 2>/dev/null | head -1)"
[[ -f "$archive" ]] || { echo "no matching release asset found" >&2; exit 1; }

if [[ -f "$tmp/SHA256SUMS" ]]; then
  echo "Verifying checksum…"
  (cd "$tmp" && grep " $(basename "$archive")\$" SHA256SUMS | sha256sum -c -) \
    || { echo "checksum verification FAILED" >&2; exit 1; }
else
  echo "warning: SHA256SUMS not found, skipping checksum verification" >&2
fi

tar -C "$tmp" -xzf "$archive"
cd "$tmp"

case "$PIECE" in
  server)        exec ./deploy/install-server.sh --bin ./autormm-server "$@";;
  agent)         exec sudo ./deploy/install-agent.sh --bin ./autormm-agent "$@";;
  agent-desktop) exec ./deploy/install-agent.sh --desktop --bin ./autormm-agent "$@";;
  client)        exec ./deploy/install-client.sh --bin ./autormm-client "$@";;
  *) echo "unknown piece: $PIECE (want server|agent|agent-desktop|client)" >&2; exit 2;;
esac
