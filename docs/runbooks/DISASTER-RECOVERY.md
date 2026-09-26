# RUNBOOK: Disaster Recovery

[**English**](DISASTER-RECOVERY.md) • [**Español**](DISASTER-RECOVERY.es.md)

Purpose: Restore WebKVM following catastrophic failures — host hardware failure, corrupted datadir, libvirt corruption, or lost configuration files. The system is designed so that **the datadir is the single source of state**; all other components (binaries, systemd services, storage pools) are reproducible.

---

## 0. Architecture and Dependency Map

```
Operational WebKVM =
  Binary (/usr/local/bin/webkvm)
  + Datadir (/opt/webkvm)      <-- Critical state (requires backup)
  + libvirt (storage pools, volumes, QEMU/KVM domains)
  + (Optional) Caddy / SSL certificates / .env
```

- **Virtual machines and disks reside in storage pools**, not within the WebKVM datadir.
- The datadir contains: `users.json`, `api-tokens.json`, `revoked.json`, `audit.log*`, `backup/`, `nodes.json`, `certs/`, and configuration stores.

---

## 1. Host Boots but Service Fails to Start

```bash
systemctl status webkvm
journalctl -u webkvm -n 100 --no-pager

# Common causes:
# - libvirtd stopped       -> sudo systemctl start libvirtd
# - Port 8080 collision    -> ss -ltnp | grep 8080
# - Datadir missing/broken -> check /opt/webkvm
# - Missing binary         -> reinstall binary (see Section 2)
```

WebKVM includes automatic background reconnection to libvirt with exponential backoff (10s to 60s); restarting WebKVM is not required once libvirtd recovers.

---

## 2. Lost or Corrupted Binary

1. Download the required release from GitHub.
2. Verify checksum against `SHA256SUMS` (see UPGRADE-ROLLBACK).
3. Install:
   ```bash
   sudo install -m 0755 webkvm /usr/local/bin/webkvm
   sudo systemctl restart webkvm
   ```
4. Verify via `/api/health`.

> The datadir format does not change between minor versions; replacing the
> binary alone never loses users or audit history.

---

## 3. Corrupted or Lost Datadir

### 3a. Restoring from Automated UI Backups

If local backups were configured in `/opt/webkvm/backup/`:

```bash
sudo systemctl stop webkvm
# Move damaged datadir aside:
sudo mv /opt/webkvm /opt/webkvm.broken
sudo mkdir -p /opt/webkvm
# Extract backup archive into /opt/webkvm
sudo tar -xzf /path/to/backup.tar.gz -C /opt/webkvm
sudo chown -R root:root /opt/webkvm
sudo chmod 600 /opt/webkvm/*.json /opt/webkvm/revoked.json 2>/dev/null || true
sudo systemctl start webkvm
curl -sk https://localhost:8080/api/health
```

### 3b. Restoring from a Manual Backup (`webkvm-backup.sh` or a plain copy)

```bash
sudo systemctl stop webkvm
sudo rm -rf /opt/webkvm
sudo cp -a /opt/webkvm.bak.<date> /opt/webkvm
# Restore correct ownership and permissions:
sudo chown -R root:root /opt/webkvm
sudo chmod 600 /opt/webkvm/*.json /opt/webkvm/revoked.json 2>/dev/null || true
sudo chmod 700 /opt/webkvm/certs 2>/dev/null || true
sudo systemctl start webkvm
```

### 3c. Restoring without a Datadir Backup

If no datadir backup exists, the recoverable state is:

- **VMs are intact in libvirt** — still listed by `virsh list --all`, and
  WebKVM re-detects and manages them after a reinstall.
- **User accounts, API tokens and audit history are lost.** Reinstall,
  re-create the admin account, and re-issue tokens.

---

## 4. Partial Corruption of Individual State Files

| File | Symptom | Recovery |
|---|---|---|
| `users.json` | Every login fails | Restore from backup. Without one: reinstall and re-create the admin. |
| `revoked.json` | `Error loading revoked file` | **Fail-open**: the server renames the corrupt file to `revoked.json.corrupt-<ts>` and continues. Previously revoked tokens are no longer revoked — revoke them again, or rotate `JWT_SECRET` to invalidate every issued token at once. |
| `audit.log` | Empty list or parse errors | The logger skips malformed lines; restore from backup only if you need the history. |
| `api-tokens.json` | Valid tokens return 401 | Restore from backup, or re-create the tokens. |
| `certs/` | TLS handshake fails | `make regen-cert` for a new self-signed pair, or re-point at your own certificate. |

---

## 5. Lost libvirt Domains with Intact Disks

Disks normally survive inside the storage pool even when the domain
definition is gone. Re-create the domain with `virt-install` / `virsh
define`, pointing it at the existing volume:

```bash
virsh vol-list <pool>                    # locate the volume
# Re-create the VM reusing that disk; the guest's data is in the volume.
```

If the domain exists but panics on every boot and was provisioned with
cloud-init, note that the NoCloud seed is consumed on first boot only —
re-provisioning does not happen automatically.

---

## 6. Full Host Rebuild (bootstrap)

1. Install dependencies: `make install-deps` (libvirt, QEMU, etc.).
2. `make install-all` (backend + Caddy + HTTPS).
3. Restore the datadir (Section 3).
4. Check the pools: `virsh pool-list --all`; re-activate with `pool-start`.
5. Run `sudo scripts/smoke.sh` to validate the stack end to end.
6. Log in as admin and verify VMs, users, audit log and jobs.

---

## Incident Quick Checklist

```bash
# 1. Is the service answering?
systemctl is-active webkvm
curl -sk https://localhost:8080/api/health

# 2. Is libvirt healthy?
virsh -c qemu:///system list --all

# 3. Is the datadir intact?
ls -la /opt/webkvm | head

# 4. Is the audit log intact?
wc -l /opt/webkvm/audit.log

# 5. What is the most recent backup?
ls -lt /opt/webkvm/backup/ | head
```
