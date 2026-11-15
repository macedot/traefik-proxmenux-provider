#!/usr/bin/env bash
# traefik-proxmenux-provider uninstaller: removes service, binary, config,
# and the generated Traefik file. Never touches other files in conf.d.
set -euo pipefail

UNIT_FILE=/etc/systemd/system/traefik-proxmenux-provider.service
BIN_PATH=/usr/local/bin/traefik-proxmenux-provider
CONFIG_DIR=/etc/traefik-proxmenux
GENERATED=/etc/traefik/conf.d/proxmenux-generated.yaml

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }

[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }

if systemctl list-unit-files traefik-proxmenux-provider.service >/dev/null 2>&1; then
  systemctl disable --now traefik-proxmenux-provider.service 2>/dev/null || true
  rm -f "$UNIT_FILE"
  systemctl daemon-reload
  log "service removed"
fi

rm -f "$BIN_PATH"
rm -f "$GENERATED" "$GENERATED.bak"
log "binary and generated routes file removed"

if [[ -d $CONFIG_DIR ]]; then
  read -r -p "remove config + token in $CONFIG_DIR? [y/N]: " a </dev/tty || a=n
  if [[ ${a,,} == y ]]; then rm -rf "$CONFIG_DIR"; log "config removed"; fi
fi
log "uninstalled."
