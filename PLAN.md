# traefik-proxmenux-provider — Project Plan

> Status: **planning** — no implementation yet (M1+ pending approval).

Convert [NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider)
into a ProxMenux-native Traefik auto-routing companion: **every app installed
through ProxMenux OCI Apps gets a working HTTPS route with zero per-app
configuration.** The only user inputs: a domain (+ ACME email when public TLS
is wanted). Simple and secure by construction.

---

## 1. What the original does (verified from source)

- Traefik **plugin provider** (yaegi, loaded via `experimental.plugins`) from
  `github.com/NX211/traefik-proxmox-provider`.
- Polls the Proxmox VE API (`:8006`) every `pollInterval` with a **cluster-wide
  API token** (`VM.Audit, VM.GuestAgent.Audit, Sys.Audit, Datastore.Audit`).
- For every running VM/CT: reads the **Notes** field, parses `traefik.key=value`
  lines as labels; requires `traefik.enable=true` per guest.
- Resolves guest IPs via QEMU guest agent / LXC interface endpoints.
- Builds a `genconf`-style dynamic config and pushes it to Traefik through the
  plugin config channel every poll.
- Manual per-guest work: edit Notes in the PVE UI; hand-write rule, port, TLS.

## 2. What changes (the conversion)

The data source moves from the PVE API to the **ProxMenux monitor API**
(`http://<pve-host>:8008`). Verified live on pve-host (2026-11-15):

- `GET /api/vms` → array of guests: `vmid, name, ip, type, status, tags,
  app_watches[]`
- each `app_watches[]`: `name` ("AdGuard Home"), `ports[] {port, scheme,
  web_path, category}`, `health_path`, `helper_slug`, `installed_via: oci_image`
- OCI-managed apps carry synthetic **read-only** watches maintained by
  ProxMenux from the OCI catalog — ports come from the catalog record, not
  guesswork. `tags` contains `oci` for these guests.

Consequences:

| Original | This project |
|---|---|
| PVE API token with 4 cluster privileges | read-only ProxMenux monitor token (optional) |
| Per-guest Notes labeling in PVE UI | nothing — ProxMenux install implies route |
| Hand-written ports/rules per guest | catalog-known `ports[]` + `web_path` |
| IPs via guest agent (needs privileges) | monitor-reported guest `ip` |
| 6 provider keys incl. cluster token | domain-centric `config.yaml` |

**No PVE token means the blast radius of a compromised provider is "read app
names/IPs/ports of one LAN" — it cannot touch guests, storage, or the cluster.**

## 3. Architecture decision

### Option A — keep the plugin/yaegi shape (faithful conversion)
- Drop-in under `providers.plugin`; no extra process.
- Cons: keeps `experimental.plugins` + the yaegi interpreter inside the proxy
  data plane; plugin config (incl. token) lives in static `traefik.yaml`;
  plugin code is downloaded from GitHub at Traefik runtime (supply chain);
  hardest to test and debug.

### Option B — companion generator + Traefik file provider (**recommended**)
- Small Go daemon (one systemd unit) inside the Traefik CT; polls the monitor
  API; atomically writes **one file**: `/etc/traefik/conf.d/proxmenux-generated.yaml`.
- Traefik's file provider in the ProxMenux OCI image already points at
  `conf.d` (verified on the-traefik-host) and hot-reloads it.
- Own `config.yaml` (chmod 0600), secrets via `token_file` (0600).
- No experimental plugins, no yaegi, no Traefik restart, **zero changes to
  Traefik static config**.
- Trivially testable: `--once --dry-run` prints the YAML it would write.
- Cons: one systemd unit; config latency = poll interval + file watch
  (seconds — acceptable for homelab app routing).

**Decision: Option B.** The provider nature (poll → dynamic config) is
preserved; only the transport into Traefik changes (file vs in-process
channel). Same pattern already proven on this host by ProxDnx.

## 4. Data flow

```
ProxMenux monitor API (PVE host :8008)
  GET /api/vms   (Bearer token; 10s timeout; no cross-host redirects; size cap)
    → filter: status == running  AND  "oci" in tags   (source: oci, default)
    → per app: slug(name) + dedupe by vmid
    → target = guest ip : catalog port  (scheme honored; web_path kept)
    → reachability probe (TCP dial, 2s) — publish only live targets
    → expose.allow / expose.deny applied
    → render Traefik file-provider YAML:
        router  Host(<slug>.<domain>)  @ entrypoint, tls
        service http://ip:port  (+ optional health check from web_path)
    → atomic write (tmp + fsync + rename) of ONLY the output file; keep .bak
Traefik file provider hot-reloads (verify watch behavior on the-traefik-host in M3; if
watch defaults off, a one-line static-config addition is needed, user-approved)

Embedded dashboard: serves the last sync state — read-only HTML +
GET /api/routes (per-container URL, target, probe status, skip reason).
Access: direct LAN bind (dashboard.listen) or Traefik-routed with CIDR
allowlist (dashboard.via_traefik) — recommended.
```

## 5. Security model (requirements, not options)

1. **Least privilege.** Read-only monitor API token, dedicated. Provider works
   with auth disabled too (current pve-host state — flagged below), token is the
   recommended posture. Document how to enable monitor auth + create token.
2. **No PVE API token.** No root, no guest-agent, no cluster audit. Blast
   radius = read-only app inventory of one LAN (names, IPs, catalog ports).
3. **Secrets.** Never in YAML, logs, or CLI args. `token_file` (0600) read at
   poll time; installer prompts interactively. systemd hardening:
   `ProtectSystem=strict`, `ReadWritePaths=/etc/traefik/conf.d
   /etc/traefik-proxmenux`, `NoNewPrivileges`, `PrivateTmp`, `ProtectHome`.
4. **Bounded egress.** Single fixed `api_url`; no redirects followed; response
   size cap; poll interval floor 10s; per-request timeout.
5. **Exposure control.** Only catalog-known ports of OCI-managed, running
   guests. Per-app `deny` list (opt-out), optional `allow` (opt-in) mode.
   TCP reachability probe before publish — drops dead or drifted targets
   (mitigates the app_watches sidecar-drift class of bug observed on this
   host 2026-11-13).
6. **TLS modes.** `internal` (default: Traefik default CA, LAN-only, zero
   external deps) · `per-app` (ACME TLS-ALPN per hostname; needs public 443
   straight to Traefik) · `wildcard` (DNS-01 `*.<domain>`; DNS creds in a
   separate 0600 env file). Nothing exposed publicly unless configured.
7. **Dashboard.** Default OFF. Optional generated `traefik.<domain>` router
   with CIDR allowlist, replacing Traefik's `api.insecure` (currently `true`
   on the-traefik-host — flagged; hardening step in M3, user-approved).
8. **Generated-file safety.** Header `# GENERATED by traefik-proxmenux-provider`;
   single fixed output path; atomic swap; previous copy kept as `.bak`; the
   provider never touches any other file under `conf.d`.
9. **Supply chain.** Stdlib-only Go (only a YAML marshal dep), pinned deps,
   `gosec` + `golangci-lint` + `go vet` + tests in CI, goreleaser static
   binaries.
10. **Tests are a first-class deliverable** (project standard): table-driven
    unit tests for client / transform / generate with fixtures captured from
    the real API shapes; `--dry-run` integration mode.
11. **Provider dashboard is read-only by construction.** Embedded UI serves
    only the last sync state — no mutation endpoints, no secrets rendered,
    no config writes. Direct access is a plain LAN bind; the recommended
    path routes it through Traefik (`dashboard.via_traefik`) with a CIDR
    allowlist, so it is never reachable from an unprotected entrypoint.

## 6. Configuration

Shipped as `config.example.yaml` in the repo root. Only `domain.base` (+ 
`tls.email` unless `tls.mode: internal`) are required; everything else has
safe defaults. Install location: `/etc/traefik-proxmenux/config.yaml`, 0600.

## 7. Repo layout (target)

```
cmd/provider/main.go       # --config, --once, --dry-run, --version
internal/config/           # strict YAML load, validation, safe defaults
internal/proxmenux/        # monitor API client (bearer, timeouts, caps)
internal/transform/        # guests+app_watches → routes (slug, probe, allow/deny)
internal/generate/         # Traefik file-provider YAML rendering + atomic writer
internal/dashboard/        # read-only status UI (HTML + /api/routes)
internal/generate/testdata/  # fixtures from real API shapes
deploy/traefik-proxmenux-provider.service   # hardened unit
install.sh / uninstall.sh  # prompts → config.yaml + token 0600 + unit
config.example.yaml  PLAN.md  README.md  LICENSE  Makefile
.github/workflows/ci.yml   # build, vet, lint, gosec, test -race
```

## 8. Milestones

| M | Scope | Acceptance |
|---|---|---|
| M0 | Scaffold repo: PLAN.md, config.example.yaml, README stub, MIT LICENSE (derived-from note), push to GitHub | repo exists, docs reviewable |
| M1 | Core Go: config loader, monitor client, transform, generate, `--once --dry-run`, embedded read-only dashboard (HTML + `/api/routes`) | `go test ./...` green; dry-run against live monitor API produces sane YAML; dashboard renders the dry-run state |
| M2 | Packaging: hardened systemd unit, `install.sh` (prompts: domain, email, TLS mode; token optional), `uninstall.sh` | clean install → unit active → generated file appears |
| M3 | Live on pve-host the-traefik-host (see §9) | routes verified via dashboard + curl; rollback tested |
| M4 | CI, README (quickstart + security model), goreleaser `v0.1.0` | CI green, release published |
| M5 | (optional, later) ProxMenux OCI catalog entry: one-click traefik+provider stack | separate effort, uses proxmenux-oci-catalog skill |

## 9. Test plan on pve-host (the-traefik-host = traefik, 192.0.2.50 on vmbr0)

Phase 1 — dry run (no side effects):
```bash
# build locally, copy in
GOOS=linux GOARCH=amd64 go build -o tp-provider ./cmd/provider
scp tp-provider pve-host:/tmp/ && ssh pve-host 'pct push 300 /tmp/tp-provider /usr/local/bin/'
pct exec 300 -- tp-provider --config /tmp/config-test.yaml --once --dry-run
# review printed YAML: expected slugs from live app_watches, no surprises
```
Phase 2 — live (`tls.mode: internal` first):
- write real config to `/etc/traefik-proxmenux/config.yaml` (0600), install unit
- verify: generated file exists in conf.d; Traefik dashboard shows the
  generated routers; provider dashboard (`dashboard.listen` / `via_traefik`
  hostname) lists every route with probe status;
  `curl -sk --resolve adguard.<domain>:443:127.0.0.1 https://adguard.<domain>/`
  returns the app; routes count == dry-run count
Phase 3 — hardening (each user-approved, not silent):
- enable monitor API auth + dedicated token → re-test with `token_file`
- disable `api.insecure`, publish dashboard router w/ CIDR allowlist
Rollback: `systemctl disable --now` unit + `rm conf.d/proxmenux-generated.yaml`
→ conf.d back to its original empty state.

## 10. Risks & mitigations

| Risk | Mitigation |
|---|---|
| Monitor API auth is **disabled** (open on `0.0.0.0:8008` LAN-wide, verified) | Provider supports/encourages tokens; hardening checklist in M3; flagged to user, never silently enabled |
| CF-proxied public zone can't do per-app TLS-ALPN unless 443 forwards raw to Traefik (on pve-host the public edge is NPM+) | Default `internal` TLS; public exposure via `wildcard` (DNS-01) or non-proxied DNS record |
| app_watches sidecar drift (name/port ↔ actual app mismatch) | OCI-only default source + TCP probe before publish + deny list |
| File provider may not watch by default | Verify on the-traefik-host in M3; if off, one user-approved static-config line (`watch: true`) |
| Name collisions between apps | Slug dedupe by vmid; optional `include_vmid_suffix` |
| Traefik dashboard `api.insecure: true` on the-traefik-host | M3 hardening step, user-approved |

## 11. Decisions (2026-11-15, user-approved)

1. TLS mode for the pve-host test: **`internal` first** ✓ — public exposure later
   via `wildcard` (DNS-01); public edge is NPM+ behind CF, per-app TLS-ALPN
   can't pass through it.
2. Enable monitor API auth + dedicated token as part of M3: **yes** ✓
   (currently configured-but-disabled, open on `0.0.0.0:8008`).
3. Fix `api.insecure: true` on the-traefik-host during M3: **yes** ✓ — replaced by the
   CIDR-allowlisted dashboard router.

Repository is **private** during initial development (set 2026-11-15).
