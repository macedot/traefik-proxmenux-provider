# Dashboard

Single embedded page (`//go:embed static/index.html`), vanilla JS, no
framework, no external requests. Served from
`http://<traefik-host>:8082` (LAN bind) or
`https://provider.<domain>` (only with `dashboard.allow_cidrs` — the
generator refuses the route otherwise; fail-closed).

## Layout

- **Header**: title; `<N> published services`; `updated <ISO timestamp>`
  (`yyyy-mm-dd HH:MM:SS +HH:MM`, viewer's timezone); monitor auth mode.
- **Infrastructure** — always the first section; foldable; count in header.
- **Domain sections** — one per domain actually in use, config order
  (base first), header shows the domain + app count, foldable with
  per-domain state persisted in `localStorage` (`pmp-fold-<domain>`).
- Cards: hostname link, app name, guest + clickable `ip:port` chip, status
  chips (`⚠ stale — port closed`, `⏸ disabled`), status dot button.

All sections share one grid style — no alignment drift between them.

## Status dot (toggle)

| Dot | Meaning | Click |
|-----|---------|-------|
| 🟢 | serving | stop |
| 🟠 | applying (or stale) | locked while in flight |
| 🔴 | stopped | start |

Clicking POSTs `/api/enabled`, immediately shows 🟠 (locked), then
**polls `GET /api/routes`** (every ~0.9s, ≤9s) until the route's `served`
matches the request. Only then is the card **patched in place** — the page
never reloads for a toggle: `views` maps each card key (`v<vmid>` or
`infra:<slug>`) to its element, and the fresh card element replaces it in
the same grid slot. Domain-section membership cannot change from a toggle,
so no reflow is needed.

## Edit flow (rename + domain)

Double-click a card (click-count detection, `ev.detail === 2`; anchors and
the edit row exempt) or focus + `Enter`/`F2`:

- name input (server-side slugify mirrored client-side to predict the
  hostname) + domain dropdown + `✓ save` / `↺ default` / `✕ discard`
- keyboard: `Enter` saves, `Esc` discards — on **both** the input and the
  select
- picking a domain applies immediately: the card grays and pulses
  (`.editing`: accent border + opacity keyframes), controls go half-opacity
  with a wait cursor, and it stays that way until the route is republished
  under the new hostname, then a full `load()` runs (the card may move to
  another domain section, so a re-group is expected here)
- `↺ default` resets slug + domain; the pulse persists while the editor is
  open from opening until the change is live

A 30s auto-refresh (`setInterval`) is paused while an edit is open
(`EDITING` guard) so the editor is never wiped mid-typing; background
refreshes rebuild the page, which is intentional — they are the
"manual sync" catch-all.

## Rendering rules

- every dynamic string passes through `esc()` (`textContent`-based) before
  touching `innerHTML`; slugs are server-side slugified; schemes are
  allowlisted to `http`/`https` at the transform layer, so link `href`s can
  never become executable URLs
- `Cache-Control: no-store` on page + API
- `rel="noopener noreferrer"` on all external links

## Endpoints consumed

- `GET /api/routes` — snapshot (below)
- `POST /api/slug`, `DELETE /api/slug` — gated by `dashboard.edit`
- `POST /api/enabled` — gated by `dashboard.edit`

See [api.md](api.md) for exact shapes.
