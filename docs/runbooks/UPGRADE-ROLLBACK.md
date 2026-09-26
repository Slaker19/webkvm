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
   ```bash
   VERSION=v0.0.1
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

### Automated Deployment (with built-in rollback)

The tarball ships the **prebuilt binary** and the installer, so the
production server needs no Go or Node.js toolchain. The installer arms a
rollback trap: on any failure it restores the previous binary and unit.

```bash
cd /tmp && tar -xzf webkvm-$VERSION.tar.gz
sudo WEBKVM_BINARY=backend/webkvm bash packaging/standalone/install.sh --yes
```

### Manual Upgrade

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
is always left behind at `webkvm.previous` by `make install-systemd` (and by
the manual step above).

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

> **Important**: rollback does NOT restore the datadir. If the problem
> affects data, restore the full backup:
> ```bash
> sudo systemctl stop webkvm
> sudo rm -rf /opt/webkvm
> sudo cp -a /opt/webkvm.bak.<timestamp> /opt/webkvm
> sudo systemctl start webkvm
> ```

---

## 5. Uninstalling

```bash
make uninstall
# Stops and removes the service and the binary. The datadir is kept
# deliberately (delete it by hand if you want it gone).
```

## Exit Criteria

- [ ] `webkvm version` reports the target version.
- [ ] `systemctl is-active webkvm` = `active`.
- [ ] `/api/health` answers `ok`.
- [ ] `audit.log` is intact and still growing.
- [ ] On failure, the rollback completed and the service is operational.
