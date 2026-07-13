# autormm

[![CI](https://github.com/Paco5687/autormm/actions/workflows/ci.yml/badge.svg)](https://github.com/Paco5687/autormm/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8.svg)](https://go.dev)

A lightweight, self-hosted **RMM + remote-desktop** tool for a homelab, in three
Go binaries with no runtime dependencies:

| Piece | Binary | Runs on | Does |
|-------|--------|---------|------|
| **Hub / Server** | `autormm-server` | one always-on host (a NUC, Pi, or spare PC) | collects telemetry, serves the dashboard + REST API, relays remote-desktop sessions |
| **Agent** | `autormm-agent` | every monitored host (Linux + Windows) | dials out to the hub, pushes metrics, captures the screen + injects input on demand |
| **Client** | `autormm-client` | your workstation | CLI to list/watch hosts and open a remote-desktop session in the browser |

Key properties:

- **Agents dial out** over a single WebSocket — no inbound ports or port-forwards on the hosts, NAT-friendly.
- **Built-in screen streaming.** The agent captures the screen and sends changed tiles as JPEG; the browser viewer reconstructs them on a `<canvas>` and forwards mouse/keyboard. No VNC/RDP server required on the host. Multi-monitor hosts get a display picker (all displays or one), the remote cursor is drawn as an overlay, and an **opt-in H.264 codec** (WebCodecs, auto-fallback to JPEG) is offered when the host has ffmpeg.
- **Cross-platform, CGO-free.** Single static binary per platform; cross-compiles cleanly for Linux and Windows.
- **Token auth + signed session tickets.** Runs as plain `IP:port` on your LAN — no reverse proxy required; reach it from afar over a zero-trust overlay.

## Architecture

```
                    ┌───────────────────────── the hub ─────────────────────────────┐
   browser ───────► │  autormm-server                                                │
   dashboard        │    /                dashboard (RMM)                            │
   & viewer         │    /viewer          remote-desktop canvas                      │
                    │    /api/hosts       host list + metrics   (Bearer admin token) │
                    │    /api/session     start a session       (Bearer admin token) │
   autormm-client ─►│    /agent/ws        agent control socket  (Bearer enroll tok)  │
                    │    /agent/session   agent media socket     (signed ticket)      │
                    │    /client/session  viewer media socket    (signed ticket)      │
                    └───────▲───────────────────────────────▲────────────────────────┘
                            │ outbound WSS                   │ relayed frames / input
                    ┌───────┴────────┐              ┌────────┴─────────┐
                    │  autormm-agent │  ...          │  autormm-agent   │
                    │  linux desktop │               │  windows desktop │
                    └────────────────┘               └──────────────────┘
```

A remote session: the client `POST /api/session` → gets a short-lived signed
ticket → the viewer opens `/client/session` → the server tells the agent (over
its control socket) to open `/agent/session` → the server pairs the two sockets
and relays bytes. Screen frames flow agent→viewer (binary); input flows
viewer→agent (JSON).

## Get started (the easy way)

**1. Start the hub.** Copy the `autormm-server` binary to your always-on box and run it:

```bash
autormm-server
```

On first run it generates and saves its tokens, turns on history, and prints a
banner with the **dashboard URL** and **admin token**. (To keep it running, use
the systemd unit in `deploy/systemd/` — or just `./deploy/install-server.sh`.)

**2. Open the dashboard.** Go to `http://<hub-ip>:8765`, click the **🔑** icon,
and paste the admin token. You're in.

**3. Add a host.** Click **＋ Add host** and copy the one-liner for the machine's
OS. Run it on that machine — it downloads the agent *from your hub* and connects
it. No file hunting, no tokens to copy by hand.

```bash
# Linux (headless):   the dashboard gives you this, pre-filled
curl -fsSL "http://<hub-ip>:8765/install.sh?token=<enroll>" | sudo sh
# Linux desktop (adds screen sharing): same URL with &desktop=1, no sudo
# Windows:            iwr -useb "http://<hub-ip>:8765/install.ps1?token=<enroll>" | iex
```

**4. That's your client.** You don't install a separate client — the dashboard
*is* the client. Click a host for graphs, then **Terminal** for a shell or
**Remote** for the screen. (A CLI client, `autormm-client`, is optional.)

> Keep the hub on your LAN (`IP:port`), never on the open internet. For access
> from away, front it with a zero-trust overlay (Twingate, Tailscale/WireGuard) —
> see **Network & remote access** below.

## Build

Requires Go 1.26+.

```bash
make build        # native binaries into ./dist
make test         # unit + end-to-end relay tests
make dist         # cross-compiled release binaries for all targets
```

## Install from a release

Tagged releases publish per-platform archives (all binaries + installers),
`.deb`/`.rpm` packages for the server and agent, and a `SHA256SUMS` file.

One-line bootstrap (detects OS/arch, verifies checksum, runs the piece
installer):

```bash
curl -fsSL https://raw.githubusercontent.com/Paco5687/autormm/main/deploy/get.sh | bash -s -- server
curl -fsSL .../deploy/get.sh | bash -s -- agent --server http://HUB-IP:8765 --token ENROLL
curl -fsSL .../deploy/get.sh | bash -s -- client --server https://... --token ADMIN
```
```powershell
# Windows agent/client
.\get.ps1 -Piece agent -Server http://HUB-IP:8765 -Token ENROLL
```

Debian/Ubuntu packages:
```bash
sudo apt install ./autormm-agent_*_linux_amd64.deb    # or autormm-server_*.deb
```

Or build everything yourself with `make` / `goreleaser` (below).

## Quick start

### 1. Server (on the hub)

```bash
make build
./deploy/install-server.sh --bin dist/autormm-server
# generates strong tokens, installs the systemd user service, prints the tokens
```

<details><summary>Manual install</summary>

```bash
mkdir -p ~/.config/autormm ~/.local/bin
cp dist/autormm-server ~/.local/bin/
cp deploy/systemd/server.env.example ~/.config/autormm/server.env
# edit server.env: set AUTORMM_ADMIN_TOKEN and AUTORMM_ENROLL_TOKEN to strong random values
cp deploy/systemd/autormm-server.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now autormm-server
```
</details>

That's it — reach the dashboard at `http://<hub-ip>:8765`. No reverse proxy is
required or recommended.

**Network & remote access.** Keep the hub on your LAN (`IP:port`) and **never**
put it on the open internet — it grants full control of every enrolled host. For
access from outside your network, use a zero-trust overlay rather than a public
port:

- **Twingate** — define a Resource for the hub's `IP:port` (or a DNS alias) and
  assign it to a group; clients reach it through the Twingate app with no ports
  opened. (autormm is plain HTTP/WebSocket on a port, so it works as-is.)
- **Tailscale / WireGuard** — put the hub, agents and clients on the overlay;
  this also covers agents that roam off your LAN.

A reverse proxy (Traefik/nginx/Caddy) is entirely optional. If you want a
hostname, `deploy/traefik-autormm.yml` is a LAN-only example, but DNS in front is
your call.

### 2. Agents

**Linux desktop** (screen streaming):
```bash
./deploy/install-agent.sh --desktop \
  --server http://HUB-IP:8765 --token "$ENROLL" --tags desktop,linux
```

**Linux server** (metrics only):
```bash
sudo ./deploy/install-agent.sh \
  --server http://HUB-IP:8765 --token "$ENROLL"
```

**Windows** (PowerShell, as the console user):
```powershell
.\deploy\install-agent.ps1 -Server http://HUB-IP:8765 -Token ENROLL -Tags desktop,windows
```

Or run the binary directly:
```bash
autormm-agent --server http://HUB-IP:8765 --token ENROLL --tags test
```

### 3. Client

```bash
./deploy/install-client.sh --bin dist/autormm-client --server http://HUB-IP:8765 --token ADMIN
# installs the binary and logs in; omit --server/--token to log in interactively

autormm-client login          # prompts for server URL + admin token, saved to ~/.config/autormm/client.json
autormm-client hosts          # table of hosts + metrics
autormm-client watch          # live-refreshing view
autormm-client connect NAME   # opens the remote desktop in your browser
autormm-client exec NAME uname -a          # run a command on a host
autormm-client exec NAME --shell powershell "Get-Process"
autormm-client inventory NAME --filter ssh # list installed software
autormm-client shell NAME                  # interactive terminal (great for headless hosts)
```

Clicking a host in the dashboard also shows its installed-software inventory
(dpkg/rpm on Linux, installed programs on Windows, brew on macOS) with a filter,
and each online host has a **Terminal** button (browser terminal via xterm.js).

The terminal opens a real PTY shell through the agent's outbound tunnel — no SSH
daemon, keys, or open inbound ports required, so it works on headless boxes
behind NAT. It is gated by the admin token and honours `--allow-exec=false`.
(PTY terminal is Linux/macOS agents; Windows agent support is pending ConPTY.)

### Scripts and scheduling

Store reusable scripts on the hub and run them on hosts on demand or on a cron
schedule (requires the server to be started with `--db`). Runs are recorded with
their output and exit code.

```bash
echo 'apt list --upgradable' | autormm-client script add --name upgrades --shell bash
autormm-client script run upgrades web01              # run now, prints output
autormm-client script schedule upgrades web01 '0 6 * * *'   # daily at 06:00
autormm-client script runs --host web01               # run history
autormm-client script list | schedules | rm | unschedule
```

The dashboard's **📜 Scripts** panel does the same: edit scripts, run them on a
host, manage cron schedules, and browse recent runs. Scheduling uses standard
5-field cron and fires with one-minute granularity.

Remote command execution runs as the agent's user and is gated by the admin
token; every command is written to the server log as an `AUDIT exec` line.
Disable it per host with `--allow-exec=false` (or `AUTORMM_NO_EXEC=1`).

The dashboard at `/` does the same visually: it lists hosts with CPU/memory
bars, sparklines and alerts, and each streamable host has a **Remote** button.
Click a host to open a detail view with time-series graphs (CPU, memory, disk,
network — when history is enabled via `--db`) and its top processes.

## Configuration reference

**Server** (flags or env):

| Env | Flag | Default | Meaning |
|-----|------|---------|---------|
| `AUTORMM_ADDR` | `-addr` | `:8765` | listen address |
| `AUTORMM_ADMIN_TOKEN` | `-admin-token` | *(random, saved to config)* | client/dashboard bearer token |
| `AUTORMM_ENROLL_TOKEN` | `-enroll-token` | *(random, saved to config)* | agent enrollment token |
| `AUTORMM_SECRET` | `-secret` | derived from tokens | HMAC key for session tickets |
| `AUTORMM_DB` | `-db` | *(none)* | SQLite path for persisted history (enables dashboard time-series graphs) |
| | `-retention` | `168h` | how long to keep persisted samples |
| `AUTORMM_ALERT_CPU` / `_MEM` / `_DISK` | `-alert-cpu` / `-alert-mem` / `-alert-disk` | `90` | alert thresholds in percent (0 disables) |
| | `-alert-for` | `2m` | sustained duration before a resource alert fires |
| | `-alert-offline` | `1m` | offline duration before an offline alert fires |
| `AUTORMM_NOTIFY_WEBHOOK` / `_NTFY` / `_DISCORD` | `-notify-webhook` / `-notify-ntfy` / `-notify-discord` | *(none)* | alert notification sinks |
| `AUTORMM_TLS_CERT`/`_KEY` | `-tls-cert`/`-tls-key` | *(none)* | optional built-in TLS |

**Agent**:

| Env | Flag | Meaning |
|-----|------|---------|
| `AUTORMM_SERVER` | `-server` | hub base URL (`https://…`) |
| `AUTORMM_ENROLL_TOKEN` | `-token` | must match the server |
| `AUTORMM_AGENT_ID` | `-id` | stable id (default: hostname) |
| `AUTORMM_TAGS` | `-tags` | free-form labels |
| `AUTORMM_INSECURE` | `-insecure` | skip TLS verify (self-signed) |
| `AUTORMM_NO_EXEC=1` | `-allow-exec=false` | disable remote command execution on this host |
| | `-interval` | metrics interval (default 5s) |

## Security model

- **Transport:** run behind a TLS reverse proxy, or use `-tls-cert`/`-tls-key`, whenever traffic leaves a trusted link. Agents verify certs unless `--insecure`.
- **Agents** present the enroll token to open the control socket.
- **Clients/dashboard** present the admin token for `/api/*`.
- **Media sockets** require a short-lived HMAC-signed ticket (60s to open, bound to session id + agent id), so viewer URLs can't be replayed later.
- Keep the hub on your LAN (`IP:port`) or behind a zero-trust overlay — never on the open internet. This tool grants full remote control of hosts; treat the tokens like root passwords. See [SECURITY.md](SECURITY.md).

## Known limitations

- **Screen capture is X11 (Linux) / GDI (Windows).** Native Wayland capture isn't supported — use Xorg or Xwayland on Linux desktops. macOS builds run in **metrics-only** mode (capture returns "unsupported").
- The Linux agent needs access to the X session (`DISPLAY`/`XAUTHORITY`); the provided user service handles this. Headless servers register as **not streamable** and are monitor-only.
- **Keyboard** mapping covers the common physical keys (letters, digits, punctuation, modifiers, arrows, F-keys, numpad). Exotic keys/layouts may not map.
- The default codec is **JPEG tile-deltas** (changed 128px tiles only) — efficient for typical desktop use, not tuned for full-screen video/gaming. An **opt-in H.264 codec** does much better on video/motion; it needs `ffmpeg` on the host (encode) and a WebCodecs browser like Chrome/Edge (decode), and silently falls back to JPEG if either is missing.
- **Multi-monitor** hosts are supported: the viewer shows all displays by default, with a picker to isolate one.

## Layout

```
cmd/                entry points (server, agent, client)
server/             hub: hub/relay/API/dashboard + embedded web assets
agent/              host agent: control loop, metrics, media sessions
client/             CLI + API client
internal/protocol/  shared wire types + binary frame codec
internal/auth/      token compare + signed tickets
internal/metrics/   gopsutil snapshot collector
internal/capture/   screen capture + input injection (linux/windows) + tiling encoder
deploy/             systemd units, Traefik config, install scripts
```
