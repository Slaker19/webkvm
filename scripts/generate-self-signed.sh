#!/usr/bin/env bash
# Generate a long-lived self-signed TLS cert for Caddy.
# Idempotent: if a cert already exists at the target path, it
# exits 0 unless FORCE=1.
#
# Output:
#   $CERT_DIR/webkvm.crt  (chmod 0644, chown caddy:caddy)
#   $CERT_DIR/webkvm.key  (chmod 0600, chown caddy:caddy)
#
# The cert is valid 10 years and has SAN entries for:
#   DNS:webkvm
#   DNS:localhost
#   DNS:$HOSTNAME.local  (mDNS — works on any LAN without /etc/hosts)
#   IP:127.0.0.1
#   IP:$HOST_LAN_IP  (the host's primary non-loopback IPv4)
#
# Why DNS+IP SANs? Browsers since 2017 ignore CN and verify SAN
# strictly. A cert with only CN=webkvm will throw a name-mismatch
# error when the user visits https://192.168.1.130. Including
# both DNS:webkvm (for users who set /etc/hosts) and IP:... (for
# users who type the IP) covers both access paths.

set -euo pipefail

CERT_DIR="${CERT_DIR:-/etc/caddy/certs}"
DAYS="${DAYS:-3650}"
HOST_LAN_IP="${HOST_LAN_IP:-$(hostname -I 2>/dev/null | awk '{print $1}')}"

log() { printf '[gen-cert] %s\n' "$*"; }
fail() { printf '[gen-cert] ERROR: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "must run as root (use sudo)"
command -v openssl >/dev/null || fail "openssl not found"

# Reuse existing cert unless explicitly forced.
if [ -f "$CERT_DIR/webkvm.crt" ] && [ -f "$CERT_DIR/webkvm.key" ] && [ "${FORCE:-0}" != "1" ]; then
	log "cert already exists at $CERT_DIR (set FORCE=1 to regenerate)"
	exit 0
fi

mkdir -p "$CERT_DIR"
chmod 0755 "$CERT_DIR"

HOSTNAME="$(hostname 2>/dev/null || echo 'webkvm')"
SAN="DNS:webkvm,DNS:localhost,DNS:${HOSTNAME}.local,IP:127.0.0.1"
if [ -n "$HOST_LAN_IP" ]; then
	SAN="$SAN,IP:$HOST_LAN_IP"
fi
log "generating self-signed cert (SAN=$SAN, valid $DAYS days)..."

openssl req -x509 -newkey rsa:2048 -nodes -days "$DAYS" \
	-keyout "$CERT_DIR/webkvm.key" \
	-out "$CERT_DIR/webkvm.crt" \
	-subj "/C=AR/ST=BA/L=BA/O=webkvm/CN=webkvm" \
	-addext "subjectAltName=$SAN" \
	2>/dev/null
# -addext is supported since OpenSSL 1.1.1 (Ubuntu 20.04+, Debian 11+).
# If your distro is older, the script will fail loudly with an
# "unknown option" error — fall back to a config-file approach.

chmod 0644 "$CERT_DIR/webkvm.crt"
chmod 0600 "$CERT_DIR/webkvm.key"

# Best-effort chown to the caddy user (no-op if user doesn't exist,
# e.g. inside the caddy container which runs as root).
if id caddy >/dev/null 2>&1; then
	chown -R caddy:caddy "$CERT_DIR"
fi

log "wrote $CERT_DIR/webkvm.crt and $CERT_DIR/webkvm.key"
log "valid until: $(openssl x509 -in $CERT_DIR/webkvm.crt -noout -enddate 2>&1 | cut -d= -f2)"
