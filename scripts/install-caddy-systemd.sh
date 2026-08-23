#!/usr/bin/env bash
# Install Caddy (HTTPS termination) on a bare-metal / systemd host.
# Idempotent — safe to re-run.
#
# What it does:
#   1. apt install caddy (official Ubuntu/Debian package)
#   2. Drop scripts/Caddyfile in /etc/caddy/Caddyfile
#   3. Validate the Caddyfile with `caddy validate` (so a broken
#      Caddyfile doesn't take down the proxy on reload)
#   4. Enable + start caddy.service
#
# Usage:
#   /usr/local/bin/install-caddy-systemd.sh [path/to/Caddyfile]
# If no path is given, the script uses $SCRIPT_DIR/Caddyfile.

set -euo pipefail

log() { printf '[install-caddy] %s\n' "$*"; }
fail() { printf '[install-caddy] ERROR: %s\n' "$*" >&2; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CADDYFILE_SRC="${1:-}"
if [ -z "$CADDYFILE_SRC" ]; then
	if [ -f "$SCRIPT_DIR/Caddyfile" ]; then
		CADDYFILE_SRC="$SCRIPT_DIR/Caddyfile"
	fi
fi
CADDYFILE_DST="/etc/caddy/Caddyfile"

[ "$(id -u)" -eq 0 ] || fail "must run as root (use sudo)"

command -v apt >/dev/null 2>&1 || fail "apt not found — this script is for Ubuntu/Debian only"
[ -f "$CADDYFILE_SRC" ] || fail "source Caddyfile not found (tried \$1, \$SCRIPT_DIR, dev tree); copy it from the webkvm repo first"

if ! command -v caddy >/dev/null 2>&1; then
	log "installing caddy via apt..."
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq
	apt-get install -y -qq caddy
else
	log "caddy already installed: $(caddy version | head -1)"
fi

# Generate the self-signed cert. Idempotent: skips if already
# present, reuses the same cert across re-installs of webkvm.
log "ensuring self-signed cert exists at /etc/caddy/certs/..."
HOST_LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
HOST_LAN_IP="$HOST_LAN_IP" "$SCRIPT_DIR/generate-self-signed.sh"

# Back up any existing Caddyfile so a re-install doesn't blow it
# away (operator may have customized it).
if [ -f "$CADDYFILE_DST" ] && [ ! -f "$CADDYFILE_DST.bak.pre-webkvm" ]; then
	cp -a "$CADDYFILE_DST" "$CADDYFILE_DST.bak.pre-webkvm"
	log "backed up existing Caddyfile to $CADDYFILE_DST.bak.pre-webkvm"
fi

install -m 0644 "$CADDYFILE_SRC" "$CADDYFILE_DST"
log "wrote $CADDYFILE_DST"

# Validate before reloading — caddy refuses to reload with a broken
# config (it keeps the old one running) but failing fast is better
# DX.
log "validating Caddyfile..."
if ! caddy validate --config "$CADDYFILE_DST" 2>&1 | tee /tmp/caddy-validate.log; then
	fail "caddy validate failed — see /tmp/caddy-validate.log"
fi

# Enable + (re)start. `systemctl enable` is a no-op if already
# enabled; `reload-or-restart` picks the right action based on
# whether caddy was running.
if ! systemctl is-enabled --quiet caddy 2>/dev/null; then
	log "enabling caddy.service..."
	systemctl enable caddy
fi

log "reloading/restarting caddy.service..."
systemctl reload-or-restart caddy
sleep 1

if ! systemctl is-active --quiet caddy; then
	fail "caddy.service is not active — check 'journalctl -u caddy -n 30'"
fi

log "✅ caddy installed and serving on :80/:443"
log "   test:  curl -kfsS https://127.0.0.1/api/health"
log "   stop:  systemctl stop caddy  (backend on :8080 stays up)"
