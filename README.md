# Traefik ProxMenux Provider

<p align="center">
  <img src=".assets/logo.png" alt="ProxMenux + Traefik Proxmox Provider"/>
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

[`config.example.yaml`](config.example.yaml) lists every key with comments;
[`docs/configuration.md`](docs/configuration.md) is the full reference.
Required inputs: `domain.base` (+ `tls.email` unless `tls.mode: internal`).
Everything else has a safe default — the minimal config is three lines.

<details open>
<summary><b>All keys</b> — every configurable option, with defaults</summary>

| Section | Key | Default | Purpose |
|---------|-----|---------|---------|
| `provider` | `poll_interval` | `30s` | re-sync cadence (min 10s) |
| | `log_level` | `info` | `debug`·`info`·`warn`·`error` |
| `proxmenux` | `api_url` | – (required) | monitor API on the PVE host |
| | `token_file` | – | 0600 Bearer token file, iff-auth |
| | `timeout` | `10s` | per-request timeout |
| | `max_response_bytes` | `5242880` | response size cap |
| | `bridge` | `vmbr0` | PVE host adapter for guest-IP matching |
| | `lan_cidr` | auto | override the auto-resolved subnet |
| `domain` | `base` | – (required) | default domain: `<slug>.base` |
| | `alternates` | – | extra domains, pickable per app |
| | `slug_source` | `watch` | `watch` (app name) or `hostname` |
| | `slug_overrides` | – | static slug fixes |
| | `include_vmid_suffix` | `false` | `<slug>-<vmid>` slugs |
| `tls` | `mode` | `internal` | `internal`·`per-app`·`wildcard` |
| | `email` | – | ACME account (non-internal modes) |
| | `cert_resolver` | `letsencrypt` | Traefik resolver for ACME modes |
| | `dns_provider` | – | lego provider for `wildcard` (DNS-01) |
| | `dns_credentials_file` | – | 0600 DNS provider credentials |
| `routing` | `default_enabled` | `true` | `false` = apps start disabled |
| | `entrypoint` | `websecure` | entryPoint for generated routers |
| | `middlewares` | `[]` | middlewares on every router |
| | `probe` | `true` | TCP/TLS reachability check before publish |
| | `probe_timeout` | `1.2s` | per-target probe timeout |
| | `health_check` | `false` | Traefik health checks from watch paths |
| `expose` | `allow` | `[]` (=all) | slugs to expose |
| | `deny` | – | slugs never exposed (wins) |
| | `dashboard.*` | off | publish the provider UI via Traefik (fail-closed: needs `allow_cidrs`) |
| `dashboard` | `enabled` | `true` | built-in status UI |
| | `listen` | `:8082` | bind address |
| | `via_traefik` | `false` | also publish `provider.<base>` |
| | `hostname` | `provider` | subdomain for `via_traefik` |
| | `allow_cidrs` | – | **required** for any Traefik-published UI |
| | `edit` | `true` | rename/domain-picker/toggles from the UI |
| `infra` | `enabled` | `true` | `<host>` (PVE UI) + `menux` (ProxMenux UI) |
| `output` | `file` | `conf.d/proxmenux-generated.yaml` | the only file written |
| | `mode` | `"0644"` | output file mode |
| | `backup` | `true` | keep `.bak` before each change |

</details>

Minimal config:

```yaml
proxmenux:
  api_url: http://<pve-host>:8008
domain:
  base: home.example.com
```

Detailed walkthroughs per scenario live in [`docs/use-cases.md`](docs/use-cases.md).

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

Detailed docs live in [`docs/`](docs/): [architecture](docs/architecture.md),
[route pipeline](docs/route-pipeline.md), [configuration
reference](docs/configuration.md), [dashboard](docs/dashboard.md),
[HTTP API](docs/api.md), [security model](docs/security.md), and
[operations](docs/operations.md).

Full engineering detail (threat model, decisions, test plan):
[PLAN.md](PLAN.md).

## License

MIT — derived from
[NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider)
(MIT).
