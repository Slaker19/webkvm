# RUNBOOK: Upgrade and Rollback

[**English**](UPGRADE-ROLLBACK.md) • [**Español**](UPGRADE-ROLLBACK.es.md)

Purpose: Upgrade WebKVM safely and reliably, and roll back to the previous version if necessary, **without data loss or audit corruption**.

> Applies to native systemd installations. For containerized deployments, see [docs/DOCKER.md](../DOCKER.md).

---

## 1. Preparation

1. **Check current version:**
   ```bash
   webkvm version
   ```

2. **Review the changelog** (`CHANGELOG.md` in the release) for breaking changes or additional migration steps.

3. **Full backup of datadir** before making any changes:
   ```bash
   sudo scripts/webkvm-backup.sh
   # or manually:
   sudo systemctl stop webkvm
   sudo cp -a /opt/webkvm /opt/webkvm.bak.$(date +%F-%H%M)
   sudo systemctl start webkvm
   ```

4. **Download the release and verify checksums:**

   Official release assets for **v0.1.13** (tag `v0.1.13`):
   - `webkvm-0.1.13-linux_amd64` (standalone server binary)
   - `webkvm-cli-0.1.13-linux_amd64` (standalone CLI binary)
   - `webkvm-0.1.13.tar.gz` (tarball archive)
   - Debian `.deb` and RPM `.rpm` packages
   - `SHA256SUMS` (checksum verification file)

   ```bash
   VERSION=v0.1.13
   curl -fsSL -o /tmp/webkvm-$VERSION.tar.gz \
     https://github.com/Slaker19/webkvm/releases/download/$VERSION/webkvm-$VERSION.tar.gz
   curl -fsSL -o /tmp/SHA256SUMS \
     https://github.com/Slaker19/webkvm/releases/download/$VERSION/SHA256SUMS
   cd /tmp
   sha256sum -c SHA256SUMS --ignore-missing webkvm-$VERSION.tar.gz
   ```
   Must print `OK`. If it fails, **abort** immediately.

5. **Run the stack smoke test** (optional but recommended):
   ```bash
   sudo scripts/smoke.sh
   ```

---

## 2. Performing the Upgrade

### Method 1: In-App Web UI Update (Recommended for standalone native installs)

Administrators can trigger the upgrade directly from the dashboard:

1. Open the **Status** page (`/#/status`) and click **Update**.
2. **Environment requirement**: The backend service requires `WEBKVM_ALLOW_UPDATE=1` in its environment (configured by default in `webkvm.service` for native standalone installs).
3. **Execution & logging**: The update runs detached in the background so that stopping the service does not terminate the updater. Output is logged to `/var/log/webkvm/update.log`.
4. **Automatic mode selection**:
   - **`release` mode**: Used when `REPO_DIR` is not a git repository checkout. The updater queries the latest GitHub release, downloads `webkvm-<version>-linux_amd64`, verifies its checksum against `SHA256SUMS`, and installs it.
   - **`source` mode**: Used when `REPO_DIR` is a git repository checkout. It runs `git pull --ff-only` and rebuilds both frontend and backend (requires Go ≥ 1.26 and Node.js ≥ 20 on the host).
5. **Backup & auto-rollback**: The previous binary is backed up to `/usr/local/bin/webkvm.previous`. The updater restarts the service and polls `/api/health` for 30 seconds. If the health check fails, it automatically restores `webkvm.previous` and restarts the service.
6. **Deployment restrictions**:
   - **Docker**: Do NOT use the in-app update button inside Docker containers. Container filesystems should be updated by pulling the latest image (`docker compose pull && docker compose up -d`). See [docs/DOCKER.md](../DOCKER.md).
   - **HA Clusters**: In an HA cluster managed by Pacemaker, the button only updates the local node serving the panel and restarts its local service; do NOT use it as a cluster-wide updater. Cluster upgrades must be coordinated through Pacemaker. See [HA-CLUSTER.md](HA-CLUSTER.md).

### Method 2: Automated Deployment via Installer Script (with built-in rollback)

The tarball ships the **prebuilt binary** and the installer, so the
production server needs no Go or Node.js toolchain. The installer arms a
rollback trap: on any failure it restores the previous binary and unit.

```bash
cd /tmp && tar -xzf webkvm-$VERSION.tar.gz
sudo WEBKVM_BINARY=backend/webkvm bash packaging/standalone/install.sh --yes
```

### Method 3: Manual Upgrade

```bash
sudo systemctl stop webkvm
sudo cp -a /usr/local/bin/webkvm /usr/local/bin/webkvm.previous
sudo install -m 0755 /tmp/webkvm /usr/local/bin/webkvm
sudo systemctl daemon-reload
sudo systemctl start webkvm
```

---

## 3. Post-Upgrade Validation

1. **Service Status:**
   ```bash
   systemctl is-active webkvm        # -> active
   systemctl status webkvm --no-pager
   ```

2. **Health Check:**
   ```bash
   # 8080/http is the default; if this install uses a different port
   # or HTTPS (server.port / server.tls_cert in config.json), find it
   # with:
   #   sudo bash scripts/detect-webkvm-endpoint.sh /opt/webkvm
   curl -sk https://localhost:8080/api/health
   ```

3. **Version Check:**
   ```bash
   webkvm version
   ```

4. **UI Validation:** Access `https://<host>:8080` in a browser and verify login and VM lists.

5. **Audit Integrity:**
   ```bash
   wc -l /opt/webkvm/audit.log
   tail -3 /opt/webkvm/audit.log
   ```

---

## 4. Rollback Procedure

If the upgrade encounters issues or fails health checks, the previous binary
is always left behind at `/usr/local/bin/webkvm.previous` by the in-app updater,
`make install-systemd`, and the manual upgrade procedure.

```bash
# With the makefile:
sudo make rollback
# Moves webkvm.previous -> webkvm, restarts, health-checks for 20s.

# Manually:
sudo systemctl stop webkvm
sudo test -f /usr/local/bin/webkvm.previous || { echo "no previous binary"; exit 1; }
sudo mv /usr/local/bin/webkvm.previous /usr/local/bin/webkvm
sudo systemctl start webkvm
curl -sk https://localhost:8080/api/health   # adjust port/proto if not the default; see note above
```

### Rollback in HA Clusters (Pacemaker)

In a high-availability cluster, do not roll back nodes independently while Pacemaker is managing `webkvm.service` / `webkvm-app`.
1. Disable the resource before replacing the binary:
   ```bash
   sudo pcs resource disable webkvm-app --wait
   ```
2. Restore the previous binary on each node:
   ```bash
   sudo mv /usr/local/bin/webkvm.previous /usr/local/bin/webkvm
   ```
3. Re-enable the resource:
   ```bash
   sudo pcs resource enable webkvm-app --wait
   sudo pcs status
   ```
See [HA-CLUSTER.md](HA-CLUSTER.md) for full cluster maintenance details.

> **Important**: rollback does NOT restore the datadir. If the problem
> affects data, restore the full backup:
> ```bash
> sudo systemctl stop webkvm
> sudo rm -rf /opt/webkvm
> sudo cp -a /opt/webkvm.bak.<timestamp> /opt/webkvm
> sudo systemctl start webkvm
> ```

---

## 5. AppArmor Migration After Upgrade

On AppArmor hosts, WebKVM assigns `<seclabel type='none' model='apparmor'/>` to defined domains so that external snapshots and revert operations are not blocked by libvirt's read-only snapshot-backing deny rule.

- **Lazy Migration**: Existing virtual machines are migrated automatically on their next start, revert, or snapshot deletion. **No manual XML edits are needed.**
- **Retention**: To retain full AppArmor confinement, set `WEBKVM_DISABLE_APPARMOR=0` in the service environment (note that offline external snapshot deletions may fail until upstream libvirt addresses the issue).
- **SELinux**: SELinux hosts are unaffected (the seclabel is not added).
- See [SECURITY.md](../../SECURITY.md) for detailed security implications.

---

## 6. Uninstalling

```bash
make uninstall
# Stops and removes the service and the binary. The datadir is kept
# deliberately (delete it by hand if you want it gone).
```

## Exit Criteria

- [ ] `webkvm version` reports the target version (e.g. `0.1.13`).
- [ ] `systemctl is-active webkvm` = `active`.
- [ ] `/api/health` answers `ok`.
- [ ] `audit.log` is intact and still growing.
- [ ] On failure, the rollback completed and the service is operational.
