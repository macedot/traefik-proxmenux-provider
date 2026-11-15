# Documentation

How **traefik-proxmenux-provider** works, in detail.

| Doc | Contents |
|-----|----------|
| [architecture.md](architecture.md) | Components, the sync cycle, data flow, why it is not a Traefik plugin |
| [route-pipeline.md](route-pipeline.md) | From monitor API to Traefik YAML: filtering, slugs, collisions, probing, toggles, domains |
| [configuration.md](configuration.md) | Every config key: defaults, validation, behavior |
| [dashboard.md](dashboard.md) | The status UI: rendering, edit flows, keyboard, in-place patching, fold/group |
| [api.md](api.md) | HTTP API (`/api/routes`, `/api/slug`, `/api/enabled`, `/healthz`) and `overrides.json` |
| [security.md](security.md) | Threat model, if-and-only-if auth, secrets handling, hardening |
| [operations.md](operations.md) | CLI flags, install/uninstall, deployment, CI, troubleshooting |

## The 30-second version

```
ProxMenux monitor API (PVE host :8008, GET /api/vms)
        │  every poll-interval (default 30s)
        ▼
filter running LXC guests with app_watches (source: oci|watches|all)
        ▼
slug(name) + per-vmid overrides (rename, domain pick, on/off)
        ▼
reachability probe (TCP for http, verified TLS for https;
self-signed https backends are flagged insecure, not dropped)
        ▼
render ONE YAML file: routers + services (+ shared insecure transport)
        ▼
atomic write (temp + fsync + rename, .bak kept)
        ▼
Traefik file provider hot-reloads it — routes live, no restart
```

A dashboard on `:8082` (LAN) or `https://provider.<domain>` (allowlist
required — fail-closed) shows every route and can rename/pick domains and
toggle apps on/off; edits persist to `overrides.json` next to the config.

Removed or stopped apps simply stop being polled, so their routes
disappear on the next sync.
