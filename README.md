# Traefik ProxMenux Provider

<p align="center">
  <img src="https://cdn.simpleicons.org/proxmox/E57000" width="110" alt="Proxmox VE"/>
  &nbsp;&nbsp;<span style="font-size:36px;font-weight:700;">+</span>&nbsp;&nbsp;
  <img src="https://raw.githubusercontent.com/MacRimi/ProxMenux/main/images/ProxMenux_128.png" width="110" alt="ProxMenux"/>
</p>

A companion service for [ProxMenux](https://github.com/MacRimi/ProxMenux) that
automatically publishes a Traefik route for every app ProxMenux installs.
`https://<app>.<your-domain>` works seconds later — zero per-app configuration.

Converted from
[NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider)
(MIT): same poll-provider idea, but the data source is the ProxMenux monitor
API instead of the raw Proxmox VE API — **no PVE token, no cluster privileges,
no per-guest label editing.** Your only input: a domain.

## Features

- Automatic HTTPS routes for every ProxMenux OCI-managed app
- Status dashboard: rename + per-app domain picker, on/off toggles
  (🟢 / 🟠 / 🔴), routes grouped per domain, foldable sections
- TCP/TLS reachability probing — dead targets are never published;
  self-signed backends are served through a dedicated insecure transport
- Atomic single-file generation (`conf.d/proxmenux-generated.yaml`) —
  Traefik's file provider hot-reloads it, no restart
- If-and-only-if authentication: a monitor token is sent only when the
  monitor itself requires one
- Strict config validation, stdlib-only Go, `gosec` + race tests in CI

## Installation

Inside the machine running Traefik (LXC, VM, or bare metal):

```bash
./install.sh   # asks for your domain; detects the monitor API; TLS = local CA
```

Or build and run it yourself:

```bash
make build
./traefik-proxmenux-provider --config /etc/traefik-proxmenux/config.yaml
```

The systemd unit ships hardened (`ProtectSystem=strict`, no capabilities).
Rollback: stop the unit and delete `conf.d/proxmenux-generated.yaml`.

## Configuration

[`config.example.yaml`](config.example.yaml) documents every key. Required
inputs: `domain.base` (+ `tls.email` unless `tls.mode: internal`).

| Key | Values | Default |
|-----|--------|---------|
| `domain.base` | your domain | – (required) |
| `domain.alternates` | extra domains, pickable per app | – |
| `tls.mode` | `internal` · `per-app` · `wildcard` | `internal` |
| `proxmenux.source` | `oci` · `watches` · `all` | `oci` |
| `routing.probe` | reachability probe before publish | `true` |
| `routing.default_enabled` | `false` = opt-in: apps start disabled | `true` |
| `dashboard.edit` | allow renames/toggles from the UI | `true` |
| `proxmenux.token_file` | Bearer token (0600) if the monitor needs one | – |

## Dashboard

Embedded status UI on `http://<traefik-host>:8082`, or routed through Traefik
as `https://provider.<domain>` (**requires** `dashboard.allow_cidrs` — the UI
has edit endpoints and is never published without an allowlist).

- double-click a card to rename the slug / pick the domain
- click the status dot to enable/disable an app
- routes grouped per domain, foldable; infrastructure entries (PVE, ProxMenux)
  pinned to the top

Same data as JSON: `GET /api/routes`.

## Security

- Least privilege: read-only monitor API, no PVE token, no guest agent
- Secrets only in 0600 files, never in YAML, logs, or CLI args
- Bounded egress: fixed API URL, no redirects, size caps, request timeouts
- Atomic writes with `.bak`; no other file under `conf.d` is ever touched
- `gosec` + `go vet` + race tests in CI

Full engineering detail (threat model, decisions, test plan):
[PLAN.md](PLAN.md).

## License

MIT — derived from
[NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider)
(MIT).
