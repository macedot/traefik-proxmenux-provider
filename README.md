# traefik-proxmenux-provider

**One HTTPS route per ProxMenux app, with zero per-app configuration.**

A companion service for the [ProxMenux](https://github.com/MacRaisin/ProxMenux)
Traefik OCI app: it watches the ProxMenux monitor API and automatically
generates Traefik routes for every OCI-managed app — install an app in
ProxMenux, get `https://<app>.<your-domain>` seconds later. Remove the app,
the route disappears.

Converted from [NX211/traefik-proxmox-provider](https://github.com/NX211/traefik-proxmox-provider)
(MIT): same poll-provider idea, but the data source is ProxMenux instead of
raw Proxmox VE API — so there is **no PVE API token, no cluster privileges,
and no per-guest label editing**. Your only inputs: a domain (+ ACME email
for public TLS).

- 📐 Design & security model: [PLAN.md](PLAN.md)
- ⚙️ Configuration: [config.example.yaml](config.example.yaml)

## Status

**Planning** — architecture decided (companion generator + Traefik file
provider, no experimental plugins), implementation not started. See
[PLAN.md](PLAN.md) milestones.

## Security posture (short version)

- Reads only the ProxMenux monitor API; recommended to run with a dedicated
  read-only API token. No Proxmox API token, no root on guests.
- Secrets live in their own 0600 files, never in config or logs.
- Publishes only catalog-known ports of running OCI apps that answer a TCP
  probe — drifted or dead watches are skipped, not exposed.
- Deny/allow lists per app; TLS defaults to a local CA (LAN-only); hardened
  systemd unit; stdlib-only Go with gosec/lint/tests in CI.

## License

MIT — derived from NX211/traefik-proxmox-provider (MIT).
