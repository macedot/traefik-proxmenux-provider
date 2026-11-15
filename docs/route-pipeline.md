# Route pipeline: monitor API → Traefik YAML

`transform.Transform(guests, extras, disabled, routeDomains, cfg)` is a pure
function: guests in, routes + skip-reasons out. It never talks to the
network (probing happens inside it, but is read-only).

## 1. Filtering

- Only guests with `status == running` produce routes (others are listed as
  skipped with reason `guest not running`).
- Only LXC guests (`type == "lxc"` when reported) — ProxDnx parity.
- `source: oci|watches|all` (config `proxmenux.source`) selects which
  watches count: OCI-catalog synthetic watches only, all app_watches, or
  all watches + discovery candidates.
- One route per (guest, watch-port); deduped by `guestIP:port` across the
  whole run — the first watch (by vmid, then order) wins.
- `expose.deny` (opt-out) and `expose.allow` (opt-in) match on the
  lowercased slug.

## 2. Slugs and hostnames

- `slug(name)` = lowercase, non-alphanumerics → `-`, trimmed dashes
  (`"AdGuard Home"` → `adguard-home`). `domain.slug_source: hostname` uses
  the guest hostname instead.
- Per-vmid slug overrides win: config `domain.slug_overrides`, then
  `overrides.json` (dashboard renames).
- **Collisions are per full hostname**: two apps named identically on
  *different* domains coexist; on the same domain the later vmid gets
  `<slug>-<vmid>`. A remaining collision is skipped, never silently merged.
- Reserved labels (`ReservedHostnames(cfg)`: infra + dashboard names) block
  slugs on the primary domain only — the same label on an alternate domain
  is fine.

## 3. Domains

- `routeDomains` (from `overrides.json` `domains`) picks the URL domain per
  vmid; absent → `domain.base` (default). Values are validated against
  `cfg.Domain.All()` = `[base, alternates…]` (deduped, base first).
- Reserved-label check applies only when the pick equals the base domain
  (that is where infra + dashboard live).

## 4. Target address

`pickIP(guest, cfg)` chooses the IPv4:

1. Candidates = `ip` + `ips[]` (deduped, IPv4 only, `net.ParseIP`-validated).
2. If `proxmenux.lan_cidr` (or the runtime-resolved provider subnet) is set,
   the first candidate inside that CIDR wins.
3. Otherwise the first candidate that is not loopback / not
   CGNAT / docker / link-local (`excludedIP`).

Invalid IP strings are dropped, so the rendered service URL
(`scheme://ip:port`) always contains a parseable address.

Scheme and path hygiene (defense against crafted monitor data):

- scheme: `""` → `http`; only `http`/`https` accepted, anything else drops
  the port. `portScheme()` additionally folds any non-https value to `http`.
- `web_path` / `health_path`: must start with `/` and contain no whitespace
  or control characters, else cleared.

## 5. Probing

`ProbeRoute` (default on, `routing.probe`, 1.2s timeout):

| Target | Probe | Result |
|--------|-------|--------|
| `http`  | TCP connect | open → served |
| `https` | TLS handshake, **verification on** | validates → served, `insecure=false` |
| `https` | cert fails validation for the dial address (self-signed, unknown CA, no IP SAN) | served with `insecure=true` → `pmp-insecure` transport |
| any | connect/handshake fails | `stale=true`, not served |

`pmp-insecure` is one shared `serversTransport` (`InsecureSkipVerify: true`)
emitted only when at least one route needs it. Verification failures are a
*trust* downgrade, not an availability failure — the app is up, its cert is
just not made for the IP (typical appliance case, e.g. Vaultwarden with
`CN=vaultwarden`).

## 6. On/off state (tri-state)

Effective disabled = explicit `overrides.json disabled[vmid]` if present,
otherwise `!routing.default_enabled`.

- disabled routes are **not dropped**: they stay in the snapshot
  (`disabled: true, served: false`) so the dashboard shows them grayed.
- infra entries go through `ApplyToggle` with keys `infra:<slug>`; the same
  tri-state applies. The dashboard's own route is never toggleable.

## 7. Rendering

`generate.Render` emits one `http:` block:

- router `pmp-<slug>`: `Host(\`<hostname>\`)` on `routing.entrypoint`,
  chained middlewares (`routing.middlewares`, plus `crowdsec@file`
  automatically when `conf.d/zz-crowdsec.yml` exists), TLS per `tls.mode`
  (`internal` → no certResolver; `wildcard`/`per-app` → resolver).
- service `pmp-<slug>`: one server `scheme://ip:port`; optional
  health-check (`path` from `web_path`, 30s interval) when
  `routing.health_check`; `insecure` routes reference `pmp-insecure`.
- infra entries render identically (kind `infra`), incl. `pmp-insecure`
  for the PVE UI (`https://<host>:8006`). They are editable: `ApplyInfraEdits`
  applies slug renames and domain picks keyed by the stable
  `infra:<default-slug>` identity, then rebuilds the hostname.
- the provider dashboard route is emitted **only** when
  `dashboard.via_traefik` AND `dashboard.allow_cidrs` are both set —
  fail-closed (see [security.md](security.md)).
