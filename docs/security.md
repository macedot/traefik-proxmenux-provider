# Security model

Design rule: **the provider must be safe to run on a shared LAN and safe to
forget about.** Everything below is implemented, not aspirational.

## Identity and privilege

- Talks **only** to the ProxMenux monitor API (read-only, `GET`).
- **No PVE token, no guest agent, no cluster privileges.** The worst case
  after full provider compromise is a read-only inventory of app names,
  IPs, and catalog ports of one LAN.
- The monitor client: fixed `api_url`, redirects rejected at the transport
  level, response size capped (`max_response_bytes`), per-request timeout,
  poll-interval floor (10s).

## If-and-only-if authentication

`Detect()` classifies the monitor at startup:

| Monitor state | Provider behavior |
|---|---|
| auth-free | works; **never** sends credentials, even if a token is configured (warned in logs) |
| requires auth + valid `token_file` | sends `Authorization: Bearer` on every request |
| requires auth, no/invalid token | **refuses to publish** (`ErrAuthRequired`); existing generated file stays as-is |

The token is read from `token_file` (0600) **on every request** — rotation
takes effect without a restart. The token never appears in YAML, logs,
flags, or the dashboard.

## Dashboard

The UI is the only writable surface and it is deliberately tiny:

- two mutation endpoints (`/api/slug`, `/api/enabled`), both requiring
  `dashboard.edit: true`, both persisting only to `overrides.json`
- bounded bodies (4 KiB), strict JSON, key allowlists (slug re-slugified,
  domain ∈ configured list, key charset/length capped)
- **fail-closed exposure**: the Traefik-routed dashboard is generated only
  with an explicit `dashboard.allow_cidrs`; without one the generator
  errors out instead of publishing an open edit API
- CSRF: JSON-only bodies + no CORS headers (form CSRF produces invalid
  JSON; `fetch` preflight fails) — cross-origin calls are not possible
- no secrets in the snapshot; no config writes; no shell-outs

## Input handling (zero-trust toward the monitor API)

The monitor is part of the trusted compute base, but its data is still
treated as hostile:

- schemes: allowlist `http`/`https` (default `http`) — a crafted scheme can
  never reach Traefik config or become an executable `href` in the UI
- `web_path` / `health_path`: must be `/`-prefixed, whitespace- and
  control-character-free, else cleared
- guest IPs: `net.ParseIP`-validated, loopback/CGNAT/docker/link-local
  excluded — only parseable addresses reach service URLs
- YAML is **marshalled** from typed structs (never string-built) — monitor
  strings cannot inject YAML keys
- slugs: strict slugify; hostname labels validated; collisions resolved
  per hostname, remaining collisions skipped with a visible reason

## Generated-file safety

- one fixed output path, `# GENERATED` header
- atomic: temp file (same dir) → chmod → write → fsync → rename
- previous copy kept as `.bak`; unchanged content → no write at all
- `output.mode` configurable; unit `ReadWritePaths` limits writes to
  `conf.d` + `/etc/traefik-proxmenux`

## Process hardening (systemd unit)

`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
`ProtectKernelTunables/Modules/Logs`, `ProtectClock`, `ProtectHostname`,
`ProtectControlGroups`, `CapabilityBoundingSet=` (empty),
`AmbientCapabilities=` (empty), `RestrictAddressFamilies=AF_INET AF_INET6
AF_UNIX AF_NETLINK`.

## Supply chain

- stdlib-only Go + one YAML dependency, pinned
- CI: race tests, `go vet`, `gosec` (pinned action version),
  least-privilege job `permissions`

## Known limitations (documented, accepted)

- The dashboard on a LAN bind trusts the LAN: with `dashboard.edit: true`
  any LAN device can rename/toggle. Set `dashboard.edit: false` or front it
  with an authenticating proxy if the LAN is not trusted.
- `pmp-insecure` trades backend-cert verification for reachability on
  self-signed appliance targets; the flag is visible per route
  (`"insecure": true`) and the probe only downgrades after a *verification*
  failure — never silently.
- No per-request authentication on mutations (by design for
  plug-and-play); the fail-closed Traefik rule + `dashboard.edit` are the
  controls.
