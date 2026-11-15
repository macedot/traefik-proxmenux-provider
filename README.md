# traefik-proxmenux-provider

**One HTTPS route per ProxMenux app, with zero per-app configuration.**

A companion service for the [ProxMenux](https://github.com/MacRimi/ProxMenux)
Traefik OCI app: it watches the ProxMenux monitor API and automatically
generates Traefik routes for every OCI-managed app. Install an app in
ProxMenux → `https://<app>.<your-domain>` works seconds later. Remove the
app → the route disappears.

Converted from [NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider)
(MIT): same poll-provider idea, but the data source is ProxMenux instead of
the raw Proxmox VE API — **no PVE API token, no cluster privileges, no
per-guest label editing.** Your only inputs: a domain (+ ACME email for
public TLS).

> **Status: implemented** — M1 (core + dashboard), M2 (packaging) and most of
> M3 done; running live in a homelab behind Traefik. Go 1.24+, stdlib-only;
> no local checkout required to deploy — build a static binary and copy it in.

## Install (inside the Traefik CT)

```bash
./install.sh   # asks only for your domain; detects the monitor API; TLS = local CA
```

Dashboard is **on by default** at `http://<traefik-ct>:8082` (and
`https://provider.<domain>` via Traefik). If `zz-crowdsec.yml` exists in
`conf.d`, generated routers automatically chain `crowdsec@file`.

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
changes (file vs in-process channel). The same companion-generator pattern is
proven in ProxDnx.

## What changed vs the original

| Original | This project |
|---|---|
| PVE API token: `VM.Audit, VM.GuestAgent.Audit, Sys.Audit, Datastore.Audit` | read-only ProxMenux monitor token (optional) |
| Per-guest `traefik.enable=true` Notes labeling | automatic for OCI-managed apps |
| Hand-written ports/rules per guest | catalog-known `ports[]` + `web_path` |
| IPs via QEMU guest agent (needs privileges) | monitor-reported guest `ip` |
| Blast radius of a compromised provider: guests + cluster audit | read-only app inventory of one LAN |

## Security model

1. **Least privilege + if-and-only-if auth** — the provider probes the
   monitor at startup: it works without authentication **if and only if**
   ProxMenux itself is auth-free; the moment the monitor requires auth, it
   refuses to publish routes until a valid `token_file` is configured (and
   it never sends credentials to an auth-free monitor).
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
7. **Dashboard exposure** — the provider UI listens on a LAN bind by default;
   the optional Traefik-routed path (`expose.dashboard`, CIDR allowlist) is
   the secure replacement for `api.insecure`.
8. **Generated-file safety** — `# GENERATED` header, one fixed output path,
   atomic swap (tmp + fsync + rename), previous copy kept as `.bak`, no other
   file under `conf.d` ever touched.
9. **Supply chain** — stdlib-only Go, pinned deps, `gosec` + `golangci-lint` +
   `go vet` + tests in CI, goreleaser static binaries.
10. **Tests are a first-class deliverable** — table-driven unit tests with
    fixtures captured from real API shapes; `--dry-run` integration mode.
11. **Provider dashboard is minimal and gated** — serves sync state plus two
    validated mutation endpoints (rename/domain pick, per-app toggle) that
    require `dashboard.edit: true` and persist only to `overrides.json`;
    no secrets rendered, no config writes, no shell-outs. Direct access is a
    plain LAN bind; the recommended path routes it through Traefik
    (`dashboard.via_traefik`) with a CIDR allowlist, so it is never reachable
    from an unprotected entrypoint.

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
| `domain.alternates` | extra domains pickable per app in the dashboard |
| `routing.probe` | **true** |
| `routing.default_enabled` | **true** · false (opt-in: apps start disabled) |
| `expose.dashboard.enabled` | **false** |
| `dashboard.enabled` | **true** (provider status UI) |

## Dashboard

The provider embeds a status UI listing every app and infrastructure URL it
manages, grouped per domain (config order, foldable sections).

Per app: name + vmid, the published `https://<slug>.<domain>` link, a
clickable target `ip:port`, a stale probe flag — plus per-app controls:

- **on/off toggle** — disabled apps stay visible (grayed) but are removed
  from the generated config; `routing.default_enabled` decides what an
  untouched app does (`false` = opt-in: everything starts grayed until you
  enable it one by one)
- **rename + domain picker** — double-click a card (or Enter/F2) to rename
  the slug and pick any configured domain; persists to `overrides.json`,
  "Default" resets both. While an edit is in flight the card pulses until
  the new URL is actually published
- infrastructure entries (PVE UI, ProxMenux UI) get the same toggle

Header: last sync (ISO timestamp), exposed/skipped counts, auth mode.
Same data as JSON at `GET /api/routes` for scripts or other tools.

| Access path | URL | Security |
|---|---|---|
| Traefik-routed (recommended) | `https://provider.<domain>` (`via_traefik: true`) | same generated TLS + CIDR allowlist |
| Direct | `http://<traefik-ct>:8082` (`listen`) | LAN bind only |

Read-only by construction: no mutation endpoints, no secrets rendered. Ships
in M1 alongside the core — the UI is just the sync state the provider keeps
anyway.

## Milestones

| M | Scope | Status |
|---|---|---|
| M0 | Scaffold: PLAN.md, config.example.yaml, README, MIT (derived-from) | ✅ done |
| M1 | Core Go: config, monitor client, transform, generate, dry-run, embedded read-only dashboard | ✅ done |
| M2 | Packaging: hardened systemd unit, `install.sh` / `uninstall.sh` | ✅ done |
| M3 | Hardening: monitor auth + token, dashboard via Traefik (CIDR allowlist) instead of `api.insecure` | auth flow shipped · hardening steps pending |
| M4 | CI, docs, goreleaser `v0.1.0` | CI live · release pending |
| M5 | (optional, later) ProxMenux OCI catalog entry: one-click traefik+provider stack | planned |

## Decisions

1. **TLS `internal` first** — safe LAN default with zero external deps;
   public exposure later via `wildcard` (DNS-01) or a non-proxied DNS record.
2. **Monitor auth + dedicated token** — supported from day one with
   if-and-only-if semantics; the M3 checklist walks through enabling it.
3. **No insecure Traefik dashboard** — use the provider's own dashboard,
   routed through Traefik with a CIDR allowlist instead of `api.insecure`.

## Testing on a live instance

- **Phase 1 — dry run (no side effects):** build (`make build` or
  `go build`), copy the binary into the Traefik host/CT, run
  `--once --dry-run`, review slugs/targets against the live app_watches.
- **Phase 2 — live (`tls.mode: internal`):** install the unit, verify the
  generated file, Traefik routers, provider dashboard lists every route with
  probe status, `curl --resolve` checks; route count == dry-run count.
- **Phase 3 — hardening:** enable monitor auth + token (re-test with
  `token_file`), disable any insecure dashboard, publish the dashboard
  router with a CIDR allowlist.
- **Rollback:** stop the unit and remove
  `conf.d/proxmenux-generated.yaml` → `conf.d` back to its original state.

Full engineering detail (threat analysis, risks, pitfalls): [PLAN.md](PLAN.md).

## License

MIT — derived from [NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider) (MIT).
