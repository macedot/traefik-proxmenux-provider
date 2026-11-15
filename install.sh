#!/usr/bin/env bash
# traefik-proxmenux-provider installer — plug-n-play for ProxMenux Traefik CTs.
#
# Only real question: your domain. Everything else is auto-detected or
# defaulted (monitor API is probed on the LAN; TLS defaults to a local CA;
# dashboard is ON). Run as root inside the Traefik LXC.
#
# Usage:
#   ./install.sh                          # interactive
#   ./install.sh --domain home.example.com [--email you@example.com]
#                [--api-url http://HOST:8008] [--tls-mode internal|per-app|wildcard]
#                [--token-file /path/to/token] [--dashboard-hostname provider]
#                [--no-dashboard] [--binary ./traefik-proxmenux-provider]
#                [--non-interactive]
set -euo pipefail

CONFIG_DIR=/etc/traefik-proxmenux
CONFIG_FILE=$CONFIG_DIR/config.yaml
BIN_PATH=/usr/local/bin/traefik-proxmenux-provider
UNIT_FILE=/etc/systemd/system/traefik-proxmenux-provider.service

DOMAIN="" EMAIL="" API_URL="" TLS_MODE="internal" TOKEN_FILE=""
DASH_HOSTNAME="provider" NO_DASHBOARD=false BINARY="" NONINTERACTIVE=false

log()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m ==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

ask() { # ask PROMPT DEFAULT -> reply on stdout (default if empty)
  local prompt="$1" def="$2" reply=""
  if $NONINTERACTIVE; then printf '%s\n' "$def"; return; fi
  read -r -p "$prompt [$def]: " reply </dev/tty || reply=""
  printf '%s\n' "${reply:-$def}"
}

ask_secret() { # reads a secret from the tty; empty when skipped
  if $NONINTERACTIVE; then printf ''; return; fi
  read -r -s -p "$1 (blank to skip): " reply </dev/tty || reply=""
  printf '\n' >&2
  printf '%s' "$reply"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="$2"; shift 2 ;;
    --email) EMAIL="$2"; shift 2 ;;
    --api-url) API_URL="$2"; shift 2 ;;
    --tls-mode) TLS_MODE="$2"; shift 2 ;;
    --token-file) TOKEN_FILE="$2"; shift 2 ;;
    --dashboard-hostname) DASH_HOSTNAME="$2"; shift 2 ;;
    --no-dashboard) NO_DASHBOARD=true; shift ;;
    --binary) BINARY="$2"; shift 2 ;;
    --non-interactive) NONINTERACTIVE=true; shift ;;
    *) die "unknown flag: $1" ;;
  esac
done

[[ $EUID -eq 0 ]] || die "run as root (this installs a systemd service)"
command -v systemctl >/dev/null || die "systemd required"

# --- locate the binary -------------------------------------------------------
if [[ -z $BINARY ]]; then
  SELF_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
  for c in "$SELF_DIR/traefik-proxmenux-provider" "$SELF_DIR/../traefik-proxmenux-provider" ./traefik-proxmenux-provider; do
    [[ -x $c ]] && BINARY=$c && break
  done
fi
[[ -n $BINARY && -x $BINARY ]] || die "provider binary not found; pass --binary /path"
"$BINARY" --version >/dev/null || die "binary check failed"

# --- detect the ProxMenux monitor API ---------------------------------------
if [[ -z $API_URL ]]; then
  log "probing the LAN for the ProxMenux monitor API (port 8008)…"
  candidates=$("$BINARY" --discover-api || true)
  if [[ -z $candidates ]]; then
    warn "no monitor API found; enter it manually"
    API_URL=$(ask "ProxMenux monitor API URL" "http://$(ip route | awk '/default/ {print $3; exit}'):8008")
  else
    mapfile -t cands <<<"$candidates"
    if [[ ${#cands[@]} -eq 1 ]]; then
      API_URL=${cands[0]}
      log "found monitor API: $API_URL"
    else
      echo "Multiple candidates:"; for i in "${!cands[@]}"; do echo "  $((i+1))) ${cands[$i]}"; done
      n=$(ask "Which one?" "1")
      API_URL=${cands[$((n-1))]}
    fi
  fi
fi
log "monitor API: $API_URL"

# --- domain (the one required answer) ---------------------------------------
if [[ -z $DOMAIN ]]; then
  DOMAIN=$(ask "Base domain for app URLs (e.g. home.example.com)" "")
  [[ -n $DOMAIN ]] || die "a domain is required"
fi
log "app URLs will be https://<app>.$DOMAIN"

# --- TLS mode (default: internal = local CA, zero setup) ---------------------
if [[ $TLS_MODE != internal && $TLS_MODE != per-app && $TLS_MODE != wildcard ]]; then
  die "--tls-mode must be internal|per-app|wildcard"
fi
if ! $NONINTERACTIVE && [[ -z $EMAIL ]] && [[ $TLS_MODE == internal ]]; then
  better=$(ask "TLS mode (internal = local CA, plug-n-play; per-app/wildcard = public ACME)" "internal")
  case "$better" in internal|per-app|wildcard) TLS_MODE=$better ;; esac
fi
if [[ $TLS_MODE != internal && -z $EMAIL ]]; then
  EMAIL=$(ask "ACME email for $TLS_MODE certificates" "")
  [[ -n $EMAIL ]] || die "email is required for $TLS_MODE mode"
fi

# --- dashboard (default: ON) -------------------------------------------------
if $NO_DASHBOARD; then DASH_ENABLED=false; else DASH_ENABLED=true; fi
if $DASH_ENABLED; then
  log "dashboard: enabled on :8081 (http://<this-ct>:$DASH_HOSTNAME path configurable)"
fi

# --- optional API token ------------------------------------------------------
mkdir -p "$CONFIG_DIR" && chmod 700 "$CONFIG_DIR"
TOKEN_REF=""
if [[ -n $TOKEN_FILE ]]; then
  [[ -r $TOKEN_FILE ]] || die "cannot read --token-file $TOKEN_FILE"
  install -m 600 "$TOKEN_FILE" "$CONFIG_DIR/token"
  TOKEN_REF='  token_file: /etc/traefik-proxmenux/token'
else
  secret=$(ask_secret "ProxMenux API token (needed only if monitor auth is enabled)")
  if [[ -n $secret ]]; then
    umask 077; printf '%s' "$secret" > "$CONFIG_DIR/token"; umask 022
    TOKEN_REF='  token_file: /etc/traefik-proxmenux/token'
    log "token stored in $CONFIG_DIR/token (0600)"
  fi
fi

# --- write config (only what's non-default) ----------------------------------
tls_block=""
case $TLS_MODE in
  per-app)  tls_block=$'tls:\n  mode: per-app\n  email: "'"$EMAIL"'"' ;;
  wildcard) tls_block=$'tls:\n  mode: wildcard\n  email: "'"$EMAIL"'"\n  dns_provider: cloudflare\n  dns_credentials_file: '"$CONFIG_DIR"'/dns.env' ;;
esac

cat > "$CONFIG_FILE" <<EOF
# Written by install.sh — secrets live in separate 0600 files.
proxmenux:
  api_url: $API_URL
$TOKEN_REF
domain:
  base: $DOMAIN
${tls_block}dashboard:
  enabled: $DASH_ENABLED
  listen: ":8081"
  via_traefik: true
  hostname: $DASH_HOSTNAME
EOF
chmod 600 "$CONFIG_FILE"
"$BINARY" --check --config "$CONFIG_FILE" || die "generated config failed validation"

# --- install + start ---------------------------------------------------------
install -m 755 "$BINARY" "$BIN_PATH"
cat > "$UNIT_FILE" <<'UNIT'
[Unit]
Description=traefik-proxmenux-provider (ProxMenux apps -> Traefik routes)
Documentation=https://github.com/macedot/traefik-proxmenux-provider
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/traefik-proxmenux-provider --config /etc/traefik-proxmenux/config.yaml
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/etc/traefik/conf.d /etc/traefik-proxmenux
ProtectHome=true
PrivateTmp=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
MemoryDenyWriteExecute=true
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now traefik-proxmenux-provider.service

# --- verify ------------------------------------------------------------------
sleep 3
systemctl is-active --quiet traefik-proxmenux-provider.service || {
  journalctl -u traefik-proxmenux-provider -n 20 --no-pager; die "service failed to start"; }
curl -fsS http://127.0.0.1:8081/healthz >/dev/null || warn "dashboard not answering on :8081 yet"
[[ -f /etc/traefik/conf.d/proxmenux-generated.yaml ]] || warn "generated file not present yet"

log "installed. routes so far:"
curl -fsS http://127.0.0.1:8081/api/routes 2>/dev/null | head -40 || true
log "next: make *.${DOMAIN} resolve to this CT (local DNS override, or wildcard DNS-01 later)"
log "dashboard: http://$(hostname -I | awk '{print $1}'):8081  (also ${DASH_HOSTNAME}.${DOMAIN} via Traefik)"
