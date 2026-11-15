# Configuration reference

Install location: `/etc/traefik-proxmenux/config.yaml`, mode 0600.
Unknown keys are rejected (`KnownFields(true)`) — a typo fails loudly
instead of silently defaulting. `--check` validates and exits.

Full annotated example: [`config.example.yaml`](../config.example.yaml).

## provider

| Key | Default | Notes |
|-----|---------|-------|
| `poll_interval` | `"30s"` | floor 10s; every sync re-reads the token file too |
| `log_level` | `"info"` | slog level |

## proxmenux

| Key | Default | Notes |
|-----|---------|-------|
| `api_url` | – (required) | monitor API, e.g. `http://<pve-host>:8008`; fixed at runtime — no redirects followed |
| `token_file` | – | Bearer token, 0600; re-read every poll (rotation without restart); sent only after auth detection demands it |
| `timeout` | `"10s"` | per-request |
| `max_response_bytes` | `5242880` | response cap |
| `source` | `oci` | `oci` = OCI-catalog watches only; `watches` = all app_watches; `all` = + discovery |
| `lan_cidr` | auto | prefer guest IPs in this subnet; when unset it is auto-resolved in order: (1) the PVE host's own network — the monitor API runs on the host, so its address is a host IP (ProxDnx `resolveSubnet` parity), (2) the provider CT's own /24 |

## domain

| Key | Default | Notes |
|-----|---------|-------|
| `base` | – (required) | validated hostname |
| `alternates` | – | extra domains, pickable per app; each validated; deduped; base first |
| `slug_source` | `watch` | `watch` (name → slug) or `hostname` |
| `slug_overrides` | – | map `vmid → slug`; dashboard edits (overrides.json) win |
| `include_vmid_suffix` | `false` | `<slug>-<vmid>` disambiguation helper |

## tls

| Key | Default | Notes |
|-----|---------|-------|
| `mode` | `internal` | `internal` = Traefik default CA, LAN-only, zero deps; `per-app` = ACME TLS-ALPN (needs public 443 to Traefik); `wildcard` = DNS-01 `*.<domain>` (creds in separate 0600 env file) |
| `cert_resolver` | `letsencrypt` | used by non-internal modes |
| `email` | – | ACME contact (required unless internal) |

## routing

| Key | Default | Notes |
|-----|---------|-------|
| `entrypoint` | `websecure` | entryPoint for generated routers |
| `middlewares` | – | extra middlewares chained on every generated router (e.g. `[secure-headers@file]`); `crowdsec@file` is added automatically when `conf.d/zz-crowdsec.yml` exists |
| `probe` | `true` | reachability probe (TCP / verified TLS) before publish |
| `probe_timeout` | `"1.2s"` | per-target |
| `health_check` | `false` | adds Traefik LB health check from `web_path` |
| `default_enabled` | `true` | tri-state default: apps with no explicit toggle are served (`true`) or disabled/grayed (`false` — opt-in mode) |

## expose

| Key | Default | Notes |
|-----|---------|-------|
| `deny` | – | slugs never published (opt-out) |
| `allow` | – | when non-empty, ONLY these slugs are published (opt-in) |
| `dashboard.enabled` | `false` | Traefik's own dashboard router (secure `api.insecure` replacement) |
| `dashboard.hostname` | – | its hostname label |
| `dashboard.allow_cidrs` | – | required with `enabled: true`; renders `ipAllowList` |

## infra

| Key | Default | Notes |
|-----|---------|-------|
| `enabled` | `true` | static entries: PVE UI (`<host>`) + ProxMenux (`menux`, `proxmenux`) on the base domain; editable + toggleable like apps (keys `infra:<slug>`) |

## dashboard

| Key | Default | Notes |
|-----|---------|-------|
| `enabled` | `true` | provider status UI |
| `listen` | `":8082"` | LAN bind |
| `via_traefik` | `false` | also publish `https://provider.<domain>` — **requires `allow_cidrs`**, render fails closed otherwise |
| `allow_cidrs` | – | `ipAllowList` sources for the routed dashboard |
| `hostname` | `provider` | sublabel for the routed dashboard |
| `edit` | `true` | allow rename/domain/toggle endpoints |

## output

| Key | Default | Notes |
|-----|---------|-------|
| `file` | `/etc/traefik/conf.d/proxmenux-generated.yaml` | the ONLY file written under conf.d |
| `mode` | `"0644"` | octal string |
| `backup` | `true` | keep previous copy as `.bak` |

## overrides.json (dashboard edits)

Next to the config, mode 0600, atomic:

```json
{
  "slug_overrides": {"100": "dns", "infra:menux": "proxmox"},
  "domains":        {"100": "example.org"},
  "disabled":       {"100": true, "infra:menux": false},
  "domains_off":    {"example.com": true},
  "infra_off":      false
}
```

Keys are vmids (apps) or `infra:<default-slug>` (infra entries — the key is
stable even after a rename; edits change the rendered slug/hostname only).
Values in `disabled` are **explicit states** (`true` = off, `false` = on); a
missing key falls back to `routing.default_enabled`. `domains_off` and
`infra_off` are global multipliers applied on top of per-app state.
