# RUNBOOK: Security Model and Auditing

[**English**](SECURITY-MODEL.md) • [**Español**](SECURITY-MODEL.es.md)

Purpose: Document the security architecture of WebKVM, where operational activities are recorded, and how administrators audit the system. Complements [SECURITY.md](../../SECURITY.md).

---

## 1. Authentication and Authorization

- **Sessions**: Signed JWT (`JWT_SECRET`). Roles: `admin`, `operator`, `viewer`. Protected by `RequireRole`/`RequireAtLeast` middleware in `internal/api`.
- **Administrators**: Exempt from resource quotas and storage pool ACLs by design.
- **API Tokens**: Long-lived Bearer tokens for automation; revocable instantly.
- **Persistent Token Blacklist** (`revoked.json`, 0600 permissions): Revocations persist across daemon restarts. Uses atomic writes (tmp -> fsync -> rename) and fails open on corruption (renaming to `.corrupt-<ts>`).
- **Login Rate Limiting**: Enforced by IP; trusted CIDRs configurable via `WEBKVM_TRUSTED_RATELIMIT_CIDRS`.

## 2. Password Policy

- Minimum 12 characters; at least 3 of 4 character classes; checks against an embedded list of ~1,100 common passwords; rejects trivial character substitutions (e.g. `p@ss`).
- Bcrypt hash storage (`password_hash`).
- Initial credential files `admin-password.*` (0600) are removed transactionally only after hashes are saved to disk.

## 3. Virtual Machine and Container Controls

- **Resource Quotas**: vCPU/RAM applied to **running** VMs and containers; disk is checked **globally** across all pools. Instantiating a template charges the quota to the **instantiating user**.
- **Storage Pool ACLs** (`AllowedPools`): Users receive 403 Forbidden on unassigned pools; administrators are exempt.
- **Storage Pools**: Creation and deletion restricted to `admin`.
- **Appliance Deployments**:
  - Target network is pre-validated against available bridges and networks before creating background jobs.
  - Cloud-Init payloads are validated prior to queueing.
  - Failed initializations mark the job as `error` and audit `appliance.deploy_cloudinit_failed` without exposing incomplete credentials.
  - Automatic cleanup (`removePoolImage`) purges orphan volumes on failures.
- **XML Sanitization**: Names and parameters validated with strict regex patterns; OVA descriptors XML-escaped.

## 4. Audit Trail

Append-only JSONL log stored at `/opt/webkvm/audit.log` (0600). Each entry includes:
`time, user, role, action, resource, ip, detail, error`.

| Action | Example |
|---|---|
| Login / Logout | `auth.login`, `auth.logout` |
| Users Management | `user.create`, `user.update`, `user.delete` |
| VM / Container Lifecycle | `vm.create`, `vm.start`, `vm.stop`, `vm.delete` |
| System Settings | `system.update`, `settings.change` |
| Appliances | `vm.appliance_deploy`, `appliance.deploy_cleanup`, `appliance.deploy_cloudinit_failed` |
| Storage & ISOs | `iso.download`, `storage.pool.create` |

## 5. Audit Log Rotation and Durability

- Rotates at 10 MB with **up to 7 backup files** (`audit.log.1` … `.7`), with total size capped at ~80 MB.
- **fsync** after each flush and before close ensures zero log loss during unexpected system crashes.
- `Logger.Close()` executed during graceful daemon shutdown.
- API queries inspect active and rotated logs, returning newest-first paginated records.

### Useful Audit Queries

```bash
AUDIT=/opt/webkvm/audit.log

# Last 20 audit events
tail -20 "$AUDIT" | jq -r '.time + " " + .user + " " + .action'

# Failed login attempts
grep '"action":"auth.login"' "$AUDIT" | jq -r 'select(.error) | .time + " " + .ip'

# User management changes
grep -E '"action":"user\.' "$AUDIT" | jq -r '.time + " " + .user + " " + .action + " " + (.resource // "")'

# Lifecycle events for a specific instance
grep '"resource":"<vm-id-or-name>"' "$AUDIT" | jq -r '.time + " " + .action'

# Appliance deployments and results
grep -E '"action":"(vm.appliance_deploy|appliance.deploy_cleanup|appliance.deploy_cloudinit_failed)"' \
  "$AUDIT" | jq -r '.time + " " + .action + " " + (.detail.error // .error // "")'
```

> Without `jq` installed, `grep '"action":"..."' "$AUDIT"` is still readable —
> one JSON line per event.

---

## 6. Background Jobs

- Long-running operations (image downloads, backups, imports) execute through an asynchronous worker pool.
- Completed or failed jobs older than 24 hours are automatically purged every 5 minutes; `queued`/`running` jobs are never purged.
- Contention is handled with a `sync.RWMutex`; `UpdatedAt` (unix seconds) is refreshed on every create and update.

## 7. Monthly Audit Checklist

- [ ] Review failed logins and rate-limit trips.
- [ ] Review role and user account changes.
- [ ] Verify rotation: `ls -la /opt/webkvm/audit.log*`.
- [ ] Confirm no `error` jobs have accumulated in the ISO/task UI.
- [ ] Confirm recent datadir backups exist.
- [ ] Review failed appliance deployments and their cleanup.

## 8. Responding to a Security Incident

1. **Revoke access**: change the admin password, revoke API tokens, rotate
   `JWT_SECRET` (rotating it invalidates every active session token).
2. **Export the audit log** before deleting anything.
3. **Isolate the host** (firewall/network) if you suspect host compromise.
4. See [DISASTER-RECOVERY.md](DISASTER-RECOVERY.md) to restore.

## 9. Fail-Closed Destructive Guards & Input Hardening

- **Host-disk guards fail closed**: `WipeHostDisk` / `InitHostDiskDirectory` refuse with `503` when the `lsblk` safety probe cannot run or be parsed (a failed probe must never read as "nothing is mounted"). A failed `wipefs` step aborts the format path instead of continuing onto `sgdisk`/`mkfs`.
- **Mount-point sanitization**: persistent mounts are restricted to `/mnt/` and `/srv/` (no `..`, no control/newline characters, never a bare root), since the value is written verbatim into a systemd `.mount` unit.
- **Terminal tickets are endpoint-scoped**: `?ticket=` is accepted only on `/api/host/terminal`, `/api/events` and `/api/vms/{id}/serial`; anywhere else it returns `403`. The host PTY still requires `/bin/login` — there is no root-shell fallback — and re-checks the ticket's admin role.
- **Domain XML injection surface**: `SetBootDevice` accepts only `disk`/`cdrom`/`network` (same set as `UpdateDomain`); `ImportDomain` validates the archive's `<name>` against the volume-name pattern before it reaches NVRAM path rewriting.
- **Firewall applies are serialized** with a per-apply temp file; `flushTable` only ignores "table does not exist" and surfaces permission/binary errors.
- **Disk attach policy**: one image may be attached to at most one VM (`409`), and a non-empty image requires explicit `force: true` (`409` otherwise) — enforced in `CreateDisk`, not just in the UI.
- **Pool ACL coverage**: ISO upload/download, volume and ISO listings, and the fleet snapshot list are all scoped to `AllowedPools` / VM ACLs; `requirePermission` denies when the user store is unavailable instead of allowing.
