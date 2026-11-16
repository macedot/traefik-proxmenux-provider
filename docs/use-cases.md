# Use cases

End-to-end walkthroughs of the scenarios the provider is built for. Each one
shows the complete config delta, what the provider does with it, and how to
verify. Baseline for all cases: the provider runs on the same machine as
Traefik (LXC recommended), the ProxMenux monitor API answers on the PVE host
at `:8008`, and the config lives at `/etc/traefik-proxmenux/config.yaml`.

---

## 1. Zero-config LAN setup (the default)

**Goal:** every app ProxMenux installs becomes `https://<app>.home.example.com`
on the LAN, with a locally-trusted-or-self-signed certificate. No DNS changes,
no ACME, no secrets.

```yaml
proxmenux:
  api_url: http://192.0.2.10:8008
domain:
  base: home.example.com
# tls.mode defaults to internal — Traefik's own CA signs everything
```

What happens every `poll_interval` (30s):

1. Pull the guest list from the monitor API.
2. Resolve the guest subnet (bridge gateway → `api_url` host → own /24).
3. Pick each guest's IP on that subnet; merge its watched ports.
4. Probe each candidate `ip:port`; publish only healthy targets.
5. Atomically rewrite `/etc/traefik/conf.d/proxmenux-generated.yaml`.

Verify:

```bash
curl -s http://127.0.0.1:8082/api/routes | jq '.routes[] | {hostname, ip, served, stale}'
curl -sk https://adguard-home.home.example.com/ -o /dev/null -w '%{http_code}\n'
```

Certificates show a browser warning (Traefik default CA) — expected for LAN
TLS. Move to case 7 for publicly-trusted certs.

---

## 2. Lock-down first: nothing published until you approve it

**Goal:** install the provider into an existing stack without instantly
exposing 20 apps. Review on the dashboard, enable one by one.

```yaml
routing:
  default_enabled: false
```

Every discovered app renders as a grayed card with an ⏸ disabled chip; its
router is **not written** to the Traefik config at all. Click the status dot
to enable an app — the next sync writes its router and the card turns 🟢.
The choice persists in `overrides.json` (explicit `false`/`true` per app
always beats the config default), so you can later flip
`default_enabled: true` without changing any per-app decision you already
made.

Toggling works per app, per **domain** (the dot on a domain header applies to
all its apps), and per **infrastructure entry**.

---

## 3. Multiple domains

**Goal:** some apps under `home.example.com`, others under a second zone, all
managed from one dashboard.

```yaml
domain:
  base: home.example.com        # default for every new app
  alternates:
    - alt.example.com           # pickable per app in the edit dialog
```

- The edit dialog (double-click a card, or Enter/F2) shows a domain
  `<select>`; the pick persists per app in `overrides.json`.
- Routes are grouped per domain in the dashboard, each group foldable.
- Each domain header has an on/off dot — a **global multiplier** over the
  per-app state: domain off = none of its apps are served, without touching
  any per-app toggle.
- Infrastructure entries (`<host>`, `menux`) live on `domain.base` and are
  editable the same way (stable identity `infra:<default-slug>` — renames
  never lose their toggle state).

Use `tls.mode: wildcard` (case 7) if the alternate zone is Cloudflare-proxied.

---

## 4. Multi-NIC guests, VPN sidecars and isolated bridges

**Goal:** guests that report several IPs — or only a VPN-side IP — must still
resolve to the address Traefik can actually reach.

```yaml
proxmenux:
  bridge: vmbr0          # the PVE host adapter whose network is "home"
  # lan_cidr: 192.0.2.0/24   # only if you must pin it explicitly
```

Resolution order for the "home" subnet: `lan_cidr` → **the bridge's network**
(from a guest on that bridge, the default gateway *is* the host's address on
the adapter) → the PVE host's /24 from `api_url` → the provider's own /24.

Then per guest:

- An IP inside that subnet wins (an arr-stack guest with `10.x` LAN +
  gluetun-side `10.77.x` gets the LAN IP).
- A guest whose **only** addresses live on other networks (a FlareSolverr
  clone glued to a gluetun bridge, an isolated `vmbr10` LXC) is **never
  published with an unreachable IP**: its cards render grayed with
  `⚠ stale — no ip available`. Nothing to point at — fix the guest's
  networking (add a veth on the host bridge) and the card flips green on the
  next sync.
- A guest reporting no IPv4 at all is skipped entirely (`no usable ip`).

Verify which subnet was resolved: it is logged at startup; with `log_level:
debug` each guest's pick is logged too.

---

## 5. Self-signed / untrusted backends (Vaultwarden case)

**Goal:** publish an app that speaks HTTPS with a self-signed certificate
(e.g. Vaultwarden, cert `CN=vaultwarden`, no IP SANs).

The probe performs a real TLS handshake with certificate verification **on**:

- Verification succeeds → published normally.
- The certificate is merely untrusted for this address (self-signed, unknown
  CA, IP without SAN) → the route **is** published, and its router carries a
  shared `serversTransport` that skips verification. The dashboard marks the
  card with an insecure indicator.
- The target is dead (refused/timeout/unreachable) → stale, never published;
  the chip states the real reason (`port closed`, `timed out`, `unreachable`,
  `no ip available`).

There is nothing to configure — the behavior is automatic, per target, on
every sync. This is deliberately not a per-app `insecure_skip_verify` flag:
the provider never hard-codes knowledge about specific apps.

---

## 6. Curated exposure of a busy stack

**Goal:** out of 25 discovered apps, publish a known-good subset with clean
URLs.

```yaml
domain:
  slug_overrides:
    "AdGuard Home": dns          # dns.home.example.com instead of adguard-home…
  include_vmid_suffix: false
expose:
  allow:                         # empty = everything; listing slugs = allowlist
    - dns
    - plex
    - qbittorrent
  deny:                          # wins over allow — belt and suspenders
    - flaresolverr
routing:
  middlewares:
    - secure-headers@file        # applied to every generated router
```

`allow`/`deny` match the final slug (after `slug_source` and
`slug_overrides`). Everything not allow-listed still shows in the dashboard
as skipped (with the reason) so you never lose visibility.

---

## 7. Public exposure with real certificates (Cloudflare DNS-01)

**Goal:** the same routes reachable from the internet through
Cloudflare-proxied DNS, with valid Let's Encrypt certificates and a WAF in
front.

```yaml
tls:
  mode: wildcard
  email: you@example.com
  cert_resolver: letsencrypt
  dns_provider: cloudflare
  dns_credentials_file: /etc/traefik-proxmenux/dns.env   # 0600
routing:
  middlewares:
    - crowdsec@file              # your LAPI-bounced middleware
```

- One `*.home.example.com` certificate via DNS-01 — no inbound 443 needed on
  the origin, ideal behind Cloudflare proxy.
- DNS credentials live **only** in the 0600 env file; the provider never
  holds them — it just references the path in the generated Traefik
  `certificatesResolvers` block.
- `per-app` mode instead issues individual ACME certs per hostname and needs
  public TCP 443 straight to Traefik.
- Make sure any catch-all router in your static config has a **lower
  priority** than the generated ones, or it will teapot your subdomains
  (a `noop@internal` catch-all matching `HostRegexp` at priority > 30
  returns 418 for every app).

---

## 8. Publishing the provider dashboard itself

**Goal:** reach the UI as `https://provider.home.example.com` instead of the
raw `:8082` port.

```yaml
dashboard:
  enabled: true
  listen: ":8082"          # or "127.0.0.1:8082" to keep the raw port local-only
  via_traefik: true
  hostname: provider
  allow_cidrs: ["192.0.2.0/24"]   # REQUIRED — see expose.dashboard too
```

- **Fail-closed:** a dashboard route without `allow_cidrs` is never generated.
  The UI carries edit endpoints, so it must never be world-readable. This is
  enforced by the generator, not by convention.
- The same gate exists for the `expose.dashboard` variant; both write CIDR
  allowlist middlewares into the generated router.
- `edit: false` turns the UI read-only (no renames, no toggles) — useful when
  you publish it broadly but edit only from the raw port.

---

## 9. Monitor with authentication enabled

**Goal:** ProxMenux monitor auth turned on; the provider must authenticate
without ever leaking the token.

```yaml
proxmenux:
  api_url: http://192.0.2.10:8008
  token_file: /etc/traefik-proxmenux/token   # chmod 0600, token only
```

The if-and-only-if contract, enforced by `client.Detect()`:

- monitor open + no token configured → works, stays open-mode
- monitor open + token configured → the token is **never sent**
- monitor requires auth + token configured → sent as `Authorization: Bearer`
- monitor requires auth + no token → the provider **refuses to publish**
  anything and reports `auth_mode: required` on the dashboard/API

The token never appears in YAML, logs, CLI args, or the generated Traefik
config.

---

## 10. Day-2 operations

**Rollback:** stop the unit, delete
`/etc/traefik/conf.d/proxmenux-generated.yaml` — the provider touches no
other file under `conf.d`. `output.backup: true` keeps a `.bak` of the
previous generation for diffing (`diff conf.d/*.bak conf.d/proxmenux-generated.yaml`).

**Faster convergence:** `provider.poll_interval: 10s` (floor) during initial
curation; back to 30s afterwards. Edits made in the UI take effect on the
next sync; the card shows `⟳ syncing` until the new hostname is actually
republished.

**Health checks:** `routing.health_check: true` adds Traefik health checks on
routers whose watches expose `health_path` — Traefik then pulls dead backends
from rotation between provider syncs, instead of waiting up to 30s.

**Debugging a missing app:** the dashboard shows skipped entries with the
reason (`guest not running`, `no discoverable ports`, `denied by
expose.deny`, `hostname reserved`, `slug collision`, `no usable ip`). Raw
data: `GET /api/routes` (JSON, same source the UI renders).

---

*Related docs: [configuration reference](configuration.md) ·
[route pipeline](route-pipeline.md) · [dashboard](dashboard.md) ·
[HTTP API](api.md) · [security model](security.md) ·
[operations](operations.md).*
