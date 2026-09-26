# RUNBOOK: Day-2 Operations and Maintenance

[**English**](OPS-DAILY.md) • [**Español**](OPS-DAILY.es.md)

Purpose: Routine administrative tasks on an active WebKVM installation — monitoring, log inspection, audit verification, background jobs, backups, and housekeeping.

---

## 1. Monitoring and Health Checks

### API Health Check

```bash
curl -sk https://localhost:8080/api/health
# Expected response: {"status":"ok","version":"...","build_time":"..."}
```

Can be scheduled via cron:
```bash
*/1 * * * * root curl -fsS -m 5 http://127.0.0.1:8080/api/health || logger -t webkvm "WebKVM health check failed"
```

### Service and Resource Inspection

```bash
systemctl status webkvm --no-pager
journalctl -u webkvm -f
ss -ltnp | grep -E '8080|443'
```

> Behind a reverse proxy, or with `BIND_ADDR=127.0.0.1`, query the endpoint
> on loopback instead.

### UI Dashboard

- **Status**: version, backend state, available updates.
- **Settings**: bind address, port, logging, backups, certificates.
- **Alerts** (when the notifier is enabled): VM downtime, low disk space,
  backup failures.

---

## 2. Application Logs

- Structured logging (JSON format by default): `journalctl -u webkvm -f` or `/var/log/webkvm/` when `WEBKVM_LOG_FILE` is set.
- Configurable verbosity and shape: `WEBKVM_LOG_LEVEL` (`info`, `debug`, `warn`) and `WEBKVM_LOG_FORMAT` (`json`, `console`).

---

## 3. Auditing and Compliance

The audit log is the primary source of truth for forensic and compliance analysis:

```bash
ls -la /opt/webkvm/audit.log*
# Automatic rotation: audit.log, audit.log.1 ... .7 (10 MB each, max ~80 MB total)
tail -f /opt/webkvm/audit.log
```

See [SECURITY-MODEL.md](SECURITY-MODEL.md) for sample queries.

---

## 4. Background Jobs and Deployments

- Jobs run in memory; terminal jobs older than 24 hours are automatically purged every 5 minutes.
- Active (`running`) or pending (`queued`) jobs are never automatically terminated.
- If a download or deployment is stuck:
  1. Inspect the volume state: `virsh vol-list <pool>`.
  2. Check the task status in the UI Task Center.
  3. Clean up any orphan volume if needed and retry.
- Automatic cleanup (`removePoolImage`) already covers ordinary failures;
  a manually stuck job is an exceptional case.

---

## 5. Backups

The datadir is the single critical state directory.

```bash
# Manual snapshot:
sudo scripts/webkvm-backup.sh

# Configured in UI: Backup -> Targets -> Schedule + Retention
```

Suggested retention policy: 7 daily + 4 weekly + 12 monthly snapshots.

---

## 6. Housekeeping and Log Pruning

- Audit logs rotate automatically (7 × 10 MB) without requiring manual cron tasks.
- Systemd journal: `journalctl --vacuum-size=200M`.
- Storage pools: Inspect unreferenced storage volumes with `virsh vol-list <pool>` against active domains.

---

## 7. Periodic Smoke Testing

The `scripts/smoke.sh` script validates the end-to-end virtualization stack
(pool -> volume -> VM -> running) and **tears itself down** even on Ctrl+C or
failure. Worth running after host or libvirt upgrades:

```bash
sudo scripts/smoke.sh
```

Options: `SMOKE_TIMEOUT=180`, `WEBKVM_SMOKE_KEEP=1` (leaves the resources in
place for debugging), `WEBKVM_URL=https://localhost:8080` (adds a health check).

---

## 8. Secret Rotation

- **Admin password**: change it from the UI (Users -> edit) — the
  `admin-password.*` files are removed transactionally once the new password
  is saved.
- **API tokens**: re-issue and revoke any token no longer in use (UI or API).
- **`JWT_SECRET`**: rotate only on suspected compromise; it invalidates every
  active session.

---

## 9. Upgrading

See [UPGRADE-ROLLBACK.md](UPGRADE-ROLLBACK.md): back up first, verify the
checksum, then reinstall with the tarball's binary
(`sudo WEBKVM_BINARY=backend/webkvm bash packaging/standalone/install.sh --yes`,
which runs a health check and rolls back automatically), and validate.

---

## 10. Cutting a Release (maintainers only)

```bash
git tag v0.0.1 && git push origin v0.0.1
# GitHub Actions builds and publishes dist/webkvm-<tag>.tar.gz + SHA256SUMS
# Local alternative:
make release                      # local artifacts only
make release RELEASE_PUBLISH=1    # also publishes with gh
```

Check the published release and its checksum before announcing it.
