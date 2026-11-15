# traefik-proxmenux-provider

**One HTTPS route per ProxMenux app, with zero per-app configuration.**

A companion service for the [ProxMenux](https://github.com/MacRaisin/ProxMenux)
Traefik OCI app: it watches the ProxMenux monitor API and automatically
generates Traefik routes for every OCI-managed app. Install an app in
ProxMenux → `https://<app>.<your-domain>` works seconds later. Remove the
app → the route disappears.

Converted from [NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider)
(MIT): same poll-provider idea, but the data source is ProxMenux instead of
the raw Proxmox VE API — **no PVE API token, no cluster privileges, no
per-guest label editing.** Your only inputs: a domain (+ ACME email for
public TLS).

> **Status: planning.** Architecture decided, no implementation yet — see
> [Milestones](#milestones). Private during initial development. Working
> copy: `/root/traefik-proxmenux-provider` on pve-host (dev + build, Go
> 1.24.4 on the host); no local checkout kept.

## How it works

```
ProxMenux monitor API (PVE host :8008)
  GET /api/vms  (Bearer token; 10s timeout; no redirects; size cap)
    → filter: status == running  AND  "oci" in tags
    → per app: slug(name), dedupe by vmid
    → target = guest ip : catalog port (scheme honored, web_path kept)
    → TCP reachability probe — publish only live targets
    → expose.allow / expose.deny applied
    → render Traefik file-provider YAML (routers + services + TLS)
    → atomic write of ONLY conf.d/proxmenux-generated.yaml (with .bak)
    → embedded dashboard serves the sync state (read-only HTML + JSON)
Traefik file provider hot-reloads — no restart, no static config changes
```

## Why not a Traefik plugin (architecture decision)

The original is a yaegi plugin (`experimental.plugins`). This project is a
**companion generator + Traefik file provider** instead:

| | plugin (original) | this project |
|---|---|---|
| interpreter | yaegi inside the proxy data plane | none |
| install | `experimental.plugins`, GitHub download at Traefik runtime | one static binary + systemd unit |
| config | 6 keys in static `traefik.yaml` incl. cluster token | domain-centric `config.yaml` (0600) |
| data source | PVE API `:8006`, 4 cluster privileges | ProxMenux monitor API `:8008`, read-only |
| per-app work | edit Notes in PVE UI, hand-write rules/ports | none — ProxMenux install implies route |
| Traefik changes | static config + restart | none (file provider already watches `conf.d`) |

Provider semantics are preserved — poll → dynamic config; only the transport
changes (file vs in-process channel). Same pattern already proven on this
host by ProxDnx.

## What changed vs the original

| Original | This project |
|---|---|
| PVE API token: `VM.Audit, VM.GuestAgent.Audit, Sys.Audit, Datastore.Audit` | read-only ProxMenux monitor token (optional) |
| Per-guest `traefik.enable=true` Notes labeling | automatic for OCI-managed apps |
| Hand-written ports/rules per guest | catalog-known `ports[]` + `web_path` |
| IPs via QEMU guest agent (needs privileges) | monitor-reported guest `ip` |
| Blast radius of a compromised provider: guests + cluster audit | read-only app inventory of one LAN |

## Security model

1. **Least privilege** — read-only monitor API token, dedicated. The provider
   also works with auth disabled (not recommended).
2. **No PVE API token** — no root, no guest agent, no cluster audit.
   Compromise = read-only app names/IPs/ports of one LAN.
3. **Secrets** — never in YAML, logs, or CLI args; `token_file` (0600) read at
   poll time; installer prompts interactively. systemd hardening:
   `ProtectSystem=strict`, limited `ReadWritePaths`, `NoNewPrivileges`,
   `PrivateTmp`, `ProtectHome`.
4. **Bounded egress** — fixed `api_url`, redirects not followed, response size
   cap, poll-interval floor 10s, per-request timeout.
5. **Exposure control** — only catalog-known ports of running OCI apps;
   per-app `deny` (opt-out) / optional `allow` (opt-in); TCP reachability
   probe before publish (drops dead or drifted watches instead of exposing
   them).
6. **TLS modes** — `internal` (default: local CA, LAN-only, zero external
   deps) · `per-app` (ACME TLS-ALPN per hostname, needs public 443 straight
   to Traefik) · `wildcard` (DNS-01 `*.<domain>`, creds in separate 0600 env
   file).
7. **Dashboard** — default off; optional `traefik.<domain>` router with CIDR
   allowlist, the secure replacement for `api.insecure`.
8. **Generated-file safety** — `# GENERATED` header, one fixed output path,
   atomic swap (tmp + fsync + rename), previous copy kept as `.bak`, no other
   file under `conf.d` ever touched.
9. **Supply chain** — stdlib-only Go, pinned deps, `gosec` + `golangci-lint` +
   `go vet` + tests in CI, goreleaser static binaries.
10. **Tests are a first-class deliverable** — table-driven unit tests with
    fixtures captured from real API shapes; `--dry-run` integration mode.
11. **Provider dashboard is read-only by construction** — serves only the
    last sync state: no mutation endpoints, no secrets rendered, no config
    writes. Direct access is a plain LAN bind; the recommended path routes it
    through Traefik (`dashboard.via_traefik`) with a CIDR allowlist, so it is
    never reachable from an unprotected entrypoint.

## Configuration

[`config.example.yaml`](config.example.yaml) documents every key with safe
defaults. Required inputs: `domain.base` (+ `tls.email` unless
`tls.mode: internal`). Installs as `/etc/traefik-proxmenux/config.yaml`
(0600); the API token lives in its own 0600 file.

Key choices (**bold** = default):

| Key | Values |
|---|---|
| `proxmenux.source` | **oci** · watches · all |
| `tls.mode` | **internal** · per-app · wildcard |
| `domain.slug_source` | **watch** ("AdGuard Home" → `adguard-home`) · hostname |
| `routing.probe` | **true** |
| `expose.dashboard.enabled` | **false** |
| `dashboard.enabled` | **true** (read-only provider UI) |

## Dashboard (ProxDnx-style)

The provider embeds a **read-only status UI** showing every URL it generated,
per container — it already holds all of that after each sync.

Per container: app name + vmid, `https://<slug>.<domain>` link, target
`ip:port`, TLS mode, probe result — and when a route is skipped, the reason
(probe failed, denied, stopped, collision). Header: last/next sync, exposed /
skipped counts, source filter, poll interval. Same data as JSON at
`GET /api/routes` for scripts or other tools.

| Access path | URL | Security |
|---|---|---|
| Traefik-routed (recommended) | `https://provider.<domain>` (`via_traefik: true`) | same generated TLS + CIDR allowlist |
| Direct | `http://<traefik-ct>:8081` (`listen`) | LAN bind only |

Read-only by construction: no mutation endpoints, no secrets rendered. Ships
in M1 alongside the core — the UI is just the sync state the provider keeps
anyway.

## Milestones

| M | Scope | Status |
|---|---|---|
| M0 | Scaffold: PLAN.md, config.example.yaml, README, MIT (derived-from) | ✅ done |
| M1 | Core Go: config loader, monitor client, transform, generate, `--once --dry-run`, embedded read-only dashboard (HTML + `/api/routes`) + unit tests | planned |
| M2 | Packaging: hardened systemd unit, `install.sh` / `uninstall.sh` | planned |
| M3 | Live on pve-host the-traefik-host + hardening: monitor auth + token, `api.insecure` fix | planned |
| M4 | CI, docs, goreleaser `v0.1.0` | planned |
| M5 | (optional, later) ProxMenux OCI catalog entry: one-click traefik+provider stack | planned |

## Decisions (2026-11-15)

1. **TLS for the pve-host test = `internal` first.** Public exposure later via
   `wildcard` (DNS-01) — the public edge is NPM+ behind Cloudflare, so
   per-app TLS-ALPN can't pass through it.
2. **Enable monitor API auth + create a dedicated token in M3.** (Currently
   configured-but-disabled; the API is open on `0.0.0.0:8008`.)
3. **Fix `api.insecure: true` on the-traefik-host in M3**, replaced by the
   CIDR-allowlisted dashboard router.

## Testing on pve-host (the-traefik-host = traefik, 192.0.2.50, vmbr0)

- **Phase 1 — dry run (no side effects):** build on pve-host (`go build`, Go
  1.24.4 on the host), `pct push` the binary into the-traefik-host, `--once --dry-run`,
  review slugs/targets against live app_watches.
- **Phase 2 — live (`tls.mode: internal`):** install the unit, verify the
  generated file, Traefik dashboard routers, provider dashboard lists every
  route with probe status, `curl --resolve` checks; route count == dry-run
  count.
- **Phase 3 — hardening:** enable monitor auth + token (re-test with
  `token_file`), disable `api.insecure`, publish the dashboard router.
- **Rollback:** disable unit + `rm conf.d/proxmenux-generated.yaml` → conf.d
  back to its original empty state.

Full engineering detail (threat analysis, risks, pitfalls): [PLAN.md](PLAN.md).

## License

MIT — derived from [NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider) (MIT).
