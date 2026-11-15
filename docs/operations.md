# Operations

## CLI

```
traefik-proxmenux-provider [flags]
  --config        path to config.yaml      (default /etc/traefik-proxmenux/config.yaml)
  --once          run one sync and exit
  --dry-run       print generated YAML instead of writing (with --once)
  --check         validate config and exit
  --discover-api  sweep the local subnet for the monitor API (:8008 health endpoint) and exit
  --version       print version and exit
```

Typical bring-up:

```bash
./traefik-proxmenux-provider --discover-api            # find the monitor
./traefik-proxmenux-provider --check                   # validate config
./traefik-proxmenux-provider --once --dry-run          # preview slugs/targets
./traefik-proxmenux-provider                           # run
```

## Install / uninstall

`install.sh` (interactive or fully flag-driven with `--non-interactive`):

- writes `/etc/traefik-proxmenux/config.yaml` (0600)
- copies the token into `$CONFIG_DIR/token` (0600) only when the monitor
  requires auth (it probes first — if-and-only-if)
- installs the hardened systemd unit, enables + starts it
- flags: `--domain --email --api-url --tls-mode --token-file
  --dashboard-hostname --no-dashboard --binary`

`uninstall.sh` stops/disables the unit and removes the binary; the
generated route file is deleted so `conf.d` returns to its prior state.

## Build

```bash
make build VERSION=v0.1.0      # static binary, stdlib + yaml only
go test -race ./...            # unit + probe tests (self-signed httptest targets)
```

## Deployment pattern used by the author

dev tree → `rsync` to the Traefik machine → `make build` → stop unit →
replace binary → start unit (`pct push` for LXC). `--check` before every
start; `systemctl` restarts require the unit stopped first when replacing
the binary (`Text file busy`).

## CI

GitHub Actions on push/PR: `go vet`, `go test -race`, `gosec`
(pinned `securego/gosec@v2.30.0`), jobs run with
`permissions: contents: read`.

## Troubleshooting

| Symptom | Check |
|---------|-------|
| routes missing | `/api/routes` → `skipped[]` has the reason; `last_error` on the dashboard header |
| route served but app errors through Traefik | `insecure: true` in `/api/routes`? backend cert self-signed → should be on `pmp-insecure`; if it *is* on, check the app itself |
| dashboard says "monitor auth check failed" | monitor requires auth and no/invalid `token_file` |
| toggles don't change anything | `dashboard.edit: false` → 403; or the app's dot shows 🟠 until the resync lands |
| overrides "stuck" | overrides.json wins over config — clear the key in the file or via the dashboard's ↺ default |
| Traefik didn't pick up the file | file provider must watch `conf.d` (ProxMenux OCI image does); verify `ls -la conf.d/*.yaml` mtime |
| everything 418 on one domain | a catch-all router with a too-high priority is intercepting — priority must be 1 for fallbacks |

## Upgrade notes

- config format is strictly validated: new keys from a newer binary on an
  old config are fine (they have defaults); old keys removed by a newer
  binary fail `--check` loudly.
- overrides.json is forward/backward compatible: unknown keys in JSON are
  ignored, missing maps are created on load.
