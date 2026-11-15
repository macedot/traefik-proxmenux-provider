# HTTP API

The dashboard serves everything on one port (`dashboard.listen`, default
`:8082`). Read endpoints are GET/HEAD-only; mutation endpoints require
`dashboard.edit: true` and are validated + bounded (4 KiB bodies). No CORS
headers are emitted — cross-origin JS cannot call the JSON endpoints
(preflight fails); combined with JSON-only bodies this blocks form CSRF.

## GET /api/routes

The full snapshot, pretty-printed, `Cache-Control: no-store`:

```jsonc
{
  "version": "v0.1.0",
  "domain": "example.com",          // domain.base
  "tls_mode": "internal",
  "entrypoint": "websecure",
  "auth_mode": "open",                // open | token | unknown
  "generated_at": "2026-11-15T21:15:19Z",
  "next_sync_at": "2026-11-15T21:15:49Z",
  "sync_count": 123,
  "last_error": "",                   // omitted when empty
  "edit_enabled": true,               // dashboard.edit
  "domains": ["example.com", "example.org"],  // pickable, base first
  "routes": [
    {
      "key": "v100",                  // absent for apps (use vmid); infra = "infra:<slug>"
      "slug": "adguard-home",
      "hostname": "adguard-home.example.com",
      "domain": "example.com",      // the app's URL domain pick
      "app_name": "AdGuard Home",
      "kind": "app",                  // app | infra
      "vmid": 100,
      "guest_name": "adguard",
      "ip": "192.0.2.2",
      "scheme": "http",
      "port": 3000,
      "web_path": "/",
      "target": "http://192.0.2.2:3000",
      "health_path": "/health",
      "stale": false,                 // probe failed: shown, not served
      "disabled": false,              // toggled off: shown, not served
      "insecure": false,              // https backend with unverifiable cert
      "served": true                  // actually in the generated config
    }
  ],
  "skipped": [
    {"vmid": 112, "guest_name": "sabnzbd", "app_name": "SABnzbd",
     "reason": "guest not running"}
  ]
}
```

`routes` includes disabled and stale entries (flagged) — the UI shows them
grayed instead of pretending they vanished.

## POST /api/slug

`{"vmid": 100, "slug": "dns", "domain": "example.org"}`

- `slug` is slugified server-side; must contain at least one alphanumeric
- `domain` optional; must be in the configured domain list; empty resets
  the pick to the default
- Response: `{"ok": true, "vmid": 100, "slug": "dns", "domain": "example.org"}`
- Signals an immediate resync.

## DELETE /api/slug

`{"vmid": 100}` — clears the slug override **and** the domain pick
(back to defaults). Response: `{"ok": true, "vmid": 100, "cleared": true}`.

## POST /api/enabled

`{"vmid": 100, "disabled": true}` for apps, or
`{"key": "infra:menux", "disabled": false}` for infra entries.

- `key` wins over `vmid` when present; length ≤ 64; no path/whitespace chars
- stores an **explicit state** in `overrides.json`
  (`disabled` map, `true` = off / `false` = on); absent keys fall back to
  `routing.default_enabled`
- Response: `{"ok": true, "key": "100", "disabled": true}`
- Signals an immediate resync.

## GET /healthz

`{"status":"ok"}` — liveness only, no auth semantics, no data.

## Errors

Mutations answer `400` (bad input), `403` (`dashboard.edit: false`),
`405` (wrong method, with `Allow`), `500` (persistence failure) as
`{"error": "..."}`.

## overrides.json

The persistence layer for edits (`<config dir>/overrides.json`, 0600,
atomic temp+fsync+rename, nil-map repair on load):

```json
{
  "slug_overrides": {"100": "dns"},
  "domains":        {"100": "example.org"},
  "disabled":       {"100": true, "infra:menux": false}
}
```

Merged over `config.yaml` on every sync: dashboard edits always win; the
config file is never written to.
