# Architecture

## Components

```
cmd/provider/          the daemon: flags, sync loop, wiring
internal/config/       strict YAML config (unknown keys rejected) + validation
internal/proxmenux/    read-only monitor-API client (auth detection, caps)
internal/discover/     LAN sweep to find the monitor API (TCP :8008 + health check)
internal/transform/    guests + watches → routes (slug, collisions, probe, toggles)
internal/generate/     routes → Traefik file-provider YAML, atomic writer
internal/overrides/    dashboard edits persisted to overrides.json (atomic, 0600)
internal/state/        one snapshot, atomic.Pointer, shared with the dashboard
internal/dashboard/    embedded status UI + gated mutation endpoints
```

One static Go binary, stdlib-only plus a YAML marshal dependency.

## The sync cycle

`main.go` runs `sync(ctx)` on a loop:

1. **Auth detection** (once): `client.Detect()` probes `GET /api/vms`
   unauthenticated.
   - `200` → monitor is auth-free (`AuthOpen`). A configured token is *never*
     sent.
   - `401/403` → monitor requires auth. With a valid `token_file`
     (`AuthToken`) the client enables the Bearer header; without one the
     provider **refuses to publish** (`ErrAuthRequired`) — if-and-only-if
     auth.
2. **Poll** `GET /api/vms` (10s timeout default, redirects rejected,
   response capped at `max_response_bytes`, default 5 MiB).
3. **Optional enrichment**: for guests with docker/native discovery
   (`source: watches|all`), `GET /api/vms/<id>/apps/{docker,native}-discovery`
   adds candidate ports.
4. **Transform** → `[]Route` (see [route-pipeline.md](route-pipeline.md)),
   including probing.
5. **Infra entries** (PVE UI on `:8006`, ProxMenux UI on `:8008` under
   `<host>.menux` / `menux` / `proxmenux`) are built from the monitor's
   hostname, probed, and toggled like apps.
6. **Render** the whole config into one `dynConfig` tree and `yaml.Marshal`
   it (marshal — never string concatenation, so monitor-supplied strings
   cannot inject YAML).
7. **Atomic write**: unchanged content → no write at all. Otherwise temp
   file in the same directory → `chmod` → write → `fsync` → `rename`,
   previous file kept as `.bak`. Nothing else under `conf.d` is touched.
8. **Snapshot** (`state.Holder`, `atomic.Pointer`) — the dashboard reads
   this; contains no secrets.

Writes happen only when content changes, so Traefik's watcher is not
needlessly triggered. Traefik hot-reloads `conf.d` (the ProxMenux OCI image
watches it); no static-config changes, no restarts.

Dashboard edits (`POST /api/slug`, `/api/enabled`) persist to
`overrides.json` and signal the resync channel, which triggers an immediate
extra sync so changes land within one poll instead of waiting.

## Why a companion generator, not a Traefik plugin

The upstream NX211 provider is a yaegi plugin loaded via
`experimental.plugins`. This project deliberately is not:

| | plugin (original) | this project |
|---|---|---|
| interpreter | yaegi inside the proxy data plane | none |
| install | code downloaded from GitHub at Traefik runtime | one static binary + systemd unit |
| config | 6 keys in static `traefik.yaml` incl. cluster token | domain-centric `config.yaml` (0600) |
| data source | PVE API `:8006`, 4 cluster privileges | ProxMenux monitor API `:8008`, read-only |
| per-app work | Notes labels in PVE UI, hand-written ports | none — ProxMenux install implies route |
| Traefik changes | static config + restart | none |

Provider semantics are preserved (poll → dynamic config); only the
transport changes (file vs in-process channel). The blast radius of a
compromised provider shrinks from "cluster + guests" to "read-only app
inventory of one LAN".

## Single point of truth

Config precedence per app:

1. `overrides.json` (dashboard edits: slug, domain, explicit on/off) — wins
2. `config.yaml` (`domain.slug_overrides`, `routing.default_enabled`)
3. Derived defaults (slug from watch name, `domain.base`, probe result)
