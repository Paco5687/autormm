# Security policy

## Trust model — please read

autormm grants **full remote control** of every enrolled host (screen, shell,
command and script execution). Treat it accordingly:

- **The admin and enroll tokens are root-equivalent over your whole fleet.**
  Guard them like root passwords; rotate them if exposed.
- **Keep the hub on your LAN (`IP:port`) — never expose it directly to the open
  internet.** For access from outside your network, front it with a zero-trust
  overlay (Twingate, Tailscale/WireGuard) or a reverse proxy with strong auth.
  See the README's *Network & remote access* section.
- **Transport:** run behind TLS (a reverse proxy, or the built-in
  `-tls-cert`/`-tls-key`) whenever the traffic leaves a trusted link.
- **Media sockets** are authorised by short-lived, HMAC-signed tickets bound to
  a session + agent, so viewer URLs can't be replayed.
- Remote command execution can be disabled per host with `--allow-exec=false`
  (or `AUTORMM_NO_EXEC=1`); every command is written to the server log as an
  `AUDIT exec` line.

## Reporting a vulnerability

Please **do not** open a public issue for security problems. Instead, open a
[GitHub security advisory](https://github.com/Paco5687/autormm/security/advisories/new)
(Security → Report a vulnerability) so it can be handled privately. Include
steps to reproduce and the affected version.

This is a hobby/homelab project maintained on a best-effort basis; there is no
formal SLA, but reports are appreciated and taken seriously.
