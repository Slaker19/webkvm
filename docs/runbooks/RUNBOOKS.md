# RUNBOOKS — WebKVM

[**English**](RUNBOOKS.md) • [**Español**](RUNBOOKS.es.md)

Operational runbooks for system administrators. The goal is to ensure that every change, incident, or recovery scenario is resolved following tested procedures, copy-paste commands, and explicit *exit criteria*.

## Index

| Runbook | When to Use |
|---|---|
| [INSTALLATION.md](../INSTALLATION.md) | Initial installation (binary, systemd, Docker, Caddy) |
| [UPGRADE-ROLLBACK.md](UPGRADE-ROLLBACK.md) | Safely upgrading or rolling back versions |
| [DISASTER-RECOVERY.md](DISASTER-RECOVERY.md) | Host loss, corrupted datadir, libvirt recovery |
| [SECURITY-MODEL.md](SECURITY-MODEL.md) | System security architecture and auditing |
| [OPS-DAILY.md](OPS-DAILY.md) | Day-2 operations: health, logs, audit trails, jobs, backups |

## Golden Rules (Apply to all runbooks)

1. **Never** edit `/opt/webkvm/users.json` or files in the datadir while the service is running without creating a backup first.
2. Before any operation that modifies the datadir, use `scripts/webkvm-backup.sh` or the UI (Backup -> Create backup).
3. When updating binaries, preserve the previous binary (`webkvm.previous`) for fast rollbacks.
4. The binary is precompiled and self-contained: **no** Go or Node.js toolchains are needed on production servers.
5. Always verify the downloaded artifact checksum against `SHA256SUMS` before installation.
6. Shell scripts in this repository run with `-euo pipefail`; if an error occurs, inspect the output rather than suppressing it.

## Key Files and Paths

| Item | Path |
|---|---|
| Binary | `/usr/local/bin/webkvm` (or `/opt/webkvm/webkvm`) |
| Previous Binary (Rollback) | `/usr/local/bin/webkvm.previous` |
| Datadir | `/opt/webkvm` |
| Users Store | `/opt/webkvm/users.json` (0600) |
| Initial Admin Credentials | `/opt/webkvm/admin-password.initial` (0600) |
| Revoked Tokens Blacklist | `/opt/webkvm/revoked.json` (0600) |
| API Tokens | `/opt/webkvm/api-tokens.json` (0600) |
| Audit Trail | `/opt/webkvm/audit.log` (+ `.1` … `.7` rotated, 10 MB each) |
| Application Log | `/var/log/webkvm/` (when `WEBKVM_LOG_FILE` is configured) |
| systemd Service | `/etc/systemd/system/webkvm.service` |
| Caddy Reverse Proxy | `/etc/caddy/Caddyfile`, service `caddy` |
| Health Check Endpoint | `curl -sk https://localhost:8080/api/health` |

> The binary's path depends on the installation method. See
> [INSTALLATION.md](../INSTALLATION.md) for the exact flow of your deployment.

## Quick Command Reference

```bash
# Service status and logs
systemctl status webkvm
journalctl -u webkvm -f
make status / make logs          # within the repo

# Health check
curl -sk https://localhost:8080/api/health
curl -s  http://127.0.0.1:8080/api/health

# Installed version
webkvm version

# Recent audit trail entries
grep '"action"' /opt/webkvm/audit.log | tail

# Virtualization stack smoke test
sudo scripts/smoke.sh
```

Next: [UPGRADE-ROLLBACK.md](UPGRADE-ROLLBACK.md).
