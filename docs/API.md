# WebKVM — REST & WebSocket API Specification

A comprehensive technical reference for the WebKVM REST API and WebSocket interfaces.

[**English**](API.md) • [**Español**](API.es.md)

---

## Authentication & Security Model

WebKVM supports three distinct authentication methods:

1. **Session Cookies (Browser Web UI)**:
   - Authenticated via `POST /api/auth/login`.
   - Returns HttpOnly `session` and CSRF double-submit cookies.
2. **API Bearer Tokens (Automations & CLI)**:
   - Header: `Authorization: Bearer wvmb_<token>`
   - Generated via UI or `webkvm-cli tokens create <name>`.
3. **Single-Use WebSocket Tickets (Consoles & Terminals)**:
   - WebSocket endpoints (`/api/vms/{id}/vnc`, `/api/vms/{id}/serial`, `/api/host/terminal`) accept short-lived, single-use tickets (`?ticket=<token>`).
   - Tickets have a 30-second TTL in RAM and are burned upon initial upgrade, preventing credential leakage in URLs.
   - `?ticket=` is scoped to terminal/SSE endpoints only (`/api/host/terminal`, `/api/events`, `/api/vms/{id}/serial`): presenting a valid console ticket on any other `/api/*` route returns `403`, so a console ticket can never stand in for a full session.

---

## Endpoints Reference

### 1. Authentication & Session

#### `POST /api/auth/login`
Authenticates a user and establishes a session.
- **Request Body**:
  ```json
  {
    "username": "admin",
    "password": "your-password"
  }
  ```
- **Response `200 OK`**:
  ```json
  {
    "token": "eyJhbGciOiJIUzI1NiIs...",
    "csrf": "6f2e...",
    "user": "admin",
    "role": "admin",
    "must_change_password": false
  }
  ```

#### `GET /api/auth/me`
Returns the currently authenticated user identity and role.

---

### 2. Virtual Machines & Containers

#### `GET /api/vms`
Lists all KVM virtual machines and Incus containers.
- **Response `200 OK`**:
  ```json
  [
    {
      "id": "3036beb7-4116-4d73-8db8-a71e94033526",
      "name": "ubuntu-srv",
      "type": "vm",
      "state": "running",
      "vcpus": 2,
      "ram_mb": 2048,
      "disk_gb": 20,
      "ip": "192.168.1.150",
      "autostart": true
    }
  ]
  ```

#### `POST /api/vms`
Creates a new KVM VM or Incus container.
- **Request Body**:
  ```json
  {
    "name": "debian-prod",
    "type": "vm",
    "vcpus": 2,
    "ram_mb": 2048,
    "disk_gb": 20,
    "image": "debian-12",
    "storage_pool": "webkvm-disks",
    "network": "vmbr0",
    "cloud_init": {
      "user": "admin",
      "password": "SecurePassword123!",
      "ssh_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5...",
      "hostname": "debian-prod"
    }
  }
  ```

#### Lifecycle Actions
- `POST /api/vms/{id}/start` — Power on instance
- `POST /api/vms/{id}/shutdown` — Send ACPI shutdown signal
- `POST /api/vms/{id}/forceoff` — Immediate power cut (destroy)
- `POST /api/vms/{id}/reboot` — Send reboot command
- `POST /api/vms/{id}/suspend` — Pause VM or freeze container
- `POST /api/vms/{id}/resume` — Resume execution
- `DELETE /api/vms/{id}` — Undefine instance and remove disks

#### VM Disks
- `POST /api/vms/{id}/disks` — Attach an existing image (`source`) or create a new volume (`pool`, `size_gb`, `format`). Attaching an image that already contains data is refused with `409` unless the request sets `"force": true`; attaching a volume already attached to another VM is refused with `409`.
- `GET /api/vms/{id}/disks/{dev}/probe` — Read-only inspection of an attached disk image (format, virtual/allocated size, `has_data`). Authenticated viewers may call it.

#### Storage Migration
- `POST /api/vms/{id}/move-storage` — Move an instance's storage to another pool. Returns `202 Accepted` + a `job_id`; poll `GET /api/storage/jobs/{id}` for byte-level progress.
  - **Request Body**: `{"pool": "webkvm-incus-2"}`
  - The destination pool's purpose must match the instance: `container` for an Incus container, `disk` (or `template`) for a KVM VM. A mismatch is refused with `400` (`pool "X" cannot hold a container`).
  - **KVM VMs**: each qcow2/raw disk is copied to the destination pool and the domain XML is repointed.
  - **Incus containers**: the rootfs *and its snapshots* are relocated with Incus's native instance migration. The container must be **stopped** (`409` otherwise), and the destination must differ from the current pool.
  - Moving between hypervisors (a libvirt pool ⇄ an Incus pool) is refused by design — export and re-import instead.

#### Instance Metadata
- `PUT /api/vms/{id}/meta` — Update alias, notes, cover, groups, owner, and the `template` / `shared` flags.
  - `"shared": true` on a template makes it listable and instantiable by **every** user, bypassing owner/group/tag scoping. **Admin only.** Each instantiation copies the template's full disk. Clones never inherit the flag.

#### Hardware & Devices
- `GET /api/vms/{id}` — Full instance detail; `PATCH /api/vms/{id}` updates vCPU, RAM, CPU model/topology, firmware and disk tuning.
- `GET`/`POST /api/vms/{id}/boot` — Read or set the boot device (`disk`, `cdrom`, `network` only — any other value is rejected).
- `GET`/`POST`/`DELETE /api/vms/{id}/networks[/{mac}]` — List, attach, edit (`PATCH`) or detach network interfaces. `GET /api/vms/{id}/vlan-support` reports whether the backing bridge accepts VLAN tags.
- `POST /api/vms/{id}/usb` and `DELETE /api/vms/{id}/usb/{vendorId}/{productId}` — Attach/detach a host USB device. **Admin only.**
- `POST /api/vms/{id}/pci` — Pass one or more host PCI addresses through to a **stopped** VM: `{"addresses": ["0000:01:00.0", "0000:01:00.1"]}`. **Admin only.** Normally every address in one IOMMU group is passed at once; the boot/console GPU is refused unconditionally. Audited as `vm.pci_attach`.
- `DELETE /api/vms/{id}/pci/{address}` — Detach a passed-through device. The address is percent-encoded in the path (it contains `:` and `.`). Audited as `vm.pci_detach`.
- `POST`/`DELETE /api/vms/{id}/shared-folders[/{tag}]` — Manage virtiofs shared folders.
- `GET /api/vms/{id}/graphics`, `/spice`, `/rdp` — Graphics configuration and downloadable `.vv` / `.rdp` connection files.

#### Snapshots
- `GET`/`POST /api/vms/{id}/snapshots` — List or create a snapshot (`{"name": "...", "memory": false}`). Disk-only snapshots of a running VM are filesystem-consistent when the guest agent responds, and fall back to crash-consistent otherwise.
- `DELETE /api/vms/{id}/snapshots/{sid}` and `POST /api/vms/{id}/snapshots/{sid}/revert`.
- `GET /api/vms/snapshots` — Fleet-wide snapshot list, scoped to the caller's VM ACLs.

#### Guest Integration & Monitoring
- `GET /api/vms/{id}/guest-info` — Data reported by `qemu-guest-agent` (IPs, hostname, OS).
- `POST /api/vms/{id}/reset-password` — Reset a guest account password through the guest agent.
- `POST /api/vms/{id}/clipboard` — Push text into the guest clipboard.
- `GET /api/vms/{id}/metrics` and `/metrics/history` — Live and historical CPU/RAM/IO counters.
- `GET /api/vms/{id}/logs` — Recent libvirt/QEMU log lines for the instance.
- `GET`/`PUT /api/vms/{id}/alerts` — Per-instance alert thresholds.
- `GET`/`PUT /api/vms/{id}/schedule` — Cron-based auto power-on/off.

#### Cloning, Templates & Cloud-Init
- `POST /api/vms/{id}/clone` — Duplicate an instance including its disks.
- `POST /api/vms/{id}/make-template` and `POST /api/vms/{id}/unset-template`.
- `GET`/`POST /api/vms/{id}/cloudinit` — Read or replace the instance's NoCloud user-data.
- `POST`/`DELETE /api/vms/{id}/cover` — Set or clear the card image.

#### Incus Catalogue
- `GET /api/vms/incus-images` — Distribution images offered by the container wizard.
- `GET /api/vms/incus-profiles` — Available Incus profiles.

---

### 2.1. Templates

- `GET /api/templates` — List templates visible to the caller: those they own or are granted, plus every template flagged `shared`.
- `POST /api/templates/{id}/instantiate` — Create a new instance from a template, copying its disk. Accepts cloud-init fields (user, password, SSH key, hostname) for NoCloud provisioning.

---

### 3. Image Hub & Background Jobs

#### `GET /api/images/cloud-base`
Lists available and locally cached official cloud base images.

#### `POST /api/images/cloud-base/pull`
Initiates an async background download for an official cloud image.
- **Request Body**: `{"id": "ubuntu-24.04"}`
- **Response `202 Accepted`**:
  ```json
  {
    "id": "ubuntu-24.04",
    "job_id": "base_img_1789672293311288289",
    "status": "queued"
  }
  ```

#### `GET /api/storage/jobs/{id}`
Polls progress, speed, and status of an active download or background job.
- **Response `200 OK`**:
  ```json
  {
    "id": "base_img_1789672293311288289",
    "name": "Download Ubuntu 24.04",
    "progress": 48.47,
    "status": "downloading",
    "bytes_done": 89050176,
    "bytes_total": 183697408,
    "speed_bps": 50447203,
    "eta_seconds": 1,
    "updated_at": 1789672295
  }
  ```

---

### 4. Storage & Pools

- `GET /api/storage/pools` — List storage pools with capacity and purpose. A purpose is one or more of `disk` (libvirt VM images), `container` (Incus rootfs), `iso`, `backup`, `template`, comma-separated when a pool serves several.
- `POST /api/storage/pools` — Create a directory or NFS/CIFS storage pool.
- `GET /api/storage/volumes` — List volumes in all or specific pool (scoped to the caller's `AllowedPools`; restricted users see an empty list for other pools).
- `GET /api/storage/isos` — List all available ISO images (same pool scoping; ISO upload/download also enforce `AllowedPools`).
- `POST /api/storage/isos/download` — Download an ISO directly into an ISO pool.
- `POST /api/storage/probe-disk` — Inspect a pool disk image before attaching it: `{"path": "...", "deep": false}` returns `{format, virtual_size, allocated, has_data, backing_file}` via `qemu-img`. With `"deep": true` (requires `libguestfs` on the host, see `guestfs` in host capabilities) it returns `202` + a job; poll `GET /api/storage/jobs/{id}` for the OS/partition/filesystem report.

---

### 5. Consoles & WebSockets

- `POST /api/vms/{id}/console-ticket` — Generate 30s ticket for VM serial console.
- `GET /api/vms/{id}/serial?ticket={tk}` — WebSocket stream to guest serial port.
- `POST /api/vms/{id}/vnc-ticket` — Generate ticket for VM VNC graphics.
- `GET /api/vms/{id}/vnc?vt={ticket}` — WebSocket proxy to RFB/noVNC frame buffer.
- `POST /api/host/terminal-ticket` — Generate admin ticket for host shell.
- `GET /api/host/terminal?ticket={tk}&cols={N}&rows={N}` — WebGL-accelerated host PTY stream.

---

### 6. Real-Time Events (Server-Sent Events)

#### `GET /api/events`
Opens a persistent SSE stream broadcasting real-time system events:
- `vm.state_changed`: Instance power state transitions (`running`, `shutoff`, `paused`).
- `vm.created` / `vm.deleted`: Instance lifecycle notifications.
- `task.progress`: Live background task updates.

---

### 7. Host Capabilities & Physical Disks

- `GET /api/host/capabilities` — Reports what the local QEMU/libvirt accepts (video/graphics models, disk buses, CPU models/flags, `spice_supported`, `parsed`). Includes `guestfs` / `guestfs_bin`: whether `virt-inspector` + `guestfish` are installed, which unlocks deep disk inspection.
- `GET /api/host/disks` — List physical disks with `fstype`, mountpoints and partition children (from `lsblk`).
- `POST /api/host/capabilities/refresh` — Re-probe QEMU/libvirt instead of serving the cached report.
- `GET /api/host/disks` / `GET /api/host/disks/filesystems` / `GET /api/host/disks/orphan-mounts` — Physical disks, their filesystems, and mounts whose backing device is gone.
- `POST /api/host/disks/wipe` and `POST /api/host/disks/initialize-directory` (admin-only) — Destructive guards are **fail-closed**: if the `lsblk` safety probe cannot run or be parsed, the request is refused with `503` instead of proceeding. `mount_point` must live under `/mnt/` or `/srv/`; the wipe step aborts the whole operation when it fails.
- `GET /api/host/pci-devices` — Host PCI devices available for passthrough, grouped by IOMMU group. **Admin only.**
- `GET /api/host/pci-preflight` — IOMMU/VFIO readiness: whether IOMMU is on, how many groups exist, and whether they are cleanly assignable. The UI blocks a passthrough attach when this reports the device's group would drag unrelated devices along.
- `GET /api/host/usb-devices` — USB devices currently plugged into the host. **Admin only.**
- `GET /api/host/stats` and `GET /api/host/metrics` — Host CPU, memory, load and storage counters.

### 8. Networks

- `GET`/`POST /api/networks` — List or create a host bridge (optional IP, DHCP range, gateway, DNS, autostart).
- `PUT`/`DELETE /api/networks/{id}` — Update or remove a bridge.
- `POST /api/networks/{id}/start` and `/stop` — Bring a managed bridge up or down.
- `GET /api/networks/{id}/leases` and `DELETE /api/networks/{id}/leases/{mac}` — Inspect and release DHCP leases.
- `GET /api/host/interfaces` — Physical NICs and existing bridges on the host.

---

### 9. Firewall

Per-VM rules live under the instance; host-wide rules use a **Safe-Apply**
protocol that cannot lock you out.

- `GET`/`PUT /api/vms/{id}/firewall` — Per-VM ingress rules and host→VM port forwards.
- `GET /api/firewall/host` — Current confirmed host ruleset.
- `POST /api/firewall/host/preview` — Render the nftables ruleset a payload would produce, without applying it.
- `POST /api/firewall/host/apply` — Stage the new ruleset and start a confirmation timer (30s by default, configurable via `firewall.confirm_window_secs` between 10 and 300). Returns the previous ruleset and the confirm `deadline`. Only one apply may be pending at a time (`400` otherwise).
- `POST /api/firewall/host/confirm` — Keep the staged ruleset. **If this never arrives before the deadline, the previous confirmed ruleset is restored automatically** — the mechanism that protects a remote admin who just locked out their own SSH.
- `POST /api/firewall/host/rollback` — Revert the staged ruleset immediately; returns `{"status": "no_pending"}` when nothing is staged.
- `GET /api/firewall/host/export` and `POST /api/firewall/host/import` — Portable JSON ruleset (1 MB body cap).

---

### 10. Backups

- `GET`/`POST /api/backup/targets` — List or create a backup target (local directory, NFS/SMB mount, SFTP).
- `PUT`/`DELETE /api/backup/targets/{id}` — Update or remove a target; `DELETE /api/backup/targets/{id}/config` drops only its stored configuration archive.
- `POST /api/backup/targets/test` — Validate connectivity and writability before saving.
- `POST /api/backup/targets/{id}/run` — Start a backup run; returns a job id.
- `GET /api/backup/targets/{id}/files` and `DELETE …/files/{filename}` — Browse and prune the archives on a target.
- `GET /api/backup/targets/{id}/verify` — Re-check the SHA-256 of the stored archives.
- `POST /api/backup/targets/{id}/restore` — Full restore (enforces size limits and protected-path checks).
- `POST /api/backup/targets/{id}/restore-as-vm` — Re-import an archive as a brand-new VM.
- `DELETE /api/backup/targets/{id}/runs/{suffix}` — Delete one complete run.
- `GET`/`POST /api/backup/schedules`, `PUT`/`DELETE /api/backup/schedules/{id}` — Cron schedules with keep-last / keep-days retention.
- `GET /api/backup/jobs` and `GET /api/vms/{id}/backup/jobs` — Run history, globally or per instance.
- `POST /api/vms/{id}/backup` — Back up a single instance to a chosen target; `GET /api/vms/{id}/backup/targets` lists the eligible ones.

Secrets (webhooks, SMTP, SFTP, CIFS credentials) are never written into a
configuration backup.

---

### 11. Users, Groups, Quotas & Tokens

- `GET`/`POST /api/users` — List or create users (`role`: `admin`, `operator`, `viewer`). **Admin only.**
- `PUT`/`DELETE /api/users/{username}` — Update role, group, `allowed_pools` and quotas, or delete the account.
- `POST /api/users/{username}/revoke-sessions` — Invalidate every active session for that user.
- `GET /api/users/{username}/usage` and `GET /api/users/me/usage` — Current consumption against the quota (VM count, vCPU, RAM, disk).
- `PUT /api/users/me/password` — Change your own password; enforces the password policy and clears the `must_change_password` flag.
- `GET`/`POST`/`PUT`/`DELETE /api/groups[/{name}]` — Manage groups used for ACL scoping.
- `GET`/`POST /api/tokens` — List or issue API tokens (`wvmb_…`, returned once, stored as a SHA-256 hash, 30-day default expiry).
- `POST /api/tokens/{id}/revoke` and `DELETE /api/tokens/{id}` — Revoke immediately (persisted in `revoked.json`) or delete the record.

Quotas cover vCPU/RAM on **running** instances and disk **globally** across
pools. Instantiating a template charges the quota to the instantiating user.
Administrators are exempt from quotas and pool ACLs by design.

---

### 12. Cloud-Init Snippets

- `GET`/`POST /api/cloudinit/snippets` — List or create reusable user-data snippets.
- `GET`/`PUT`/`DELETE /api/cloudinit/snippets/{id}` — Manage one snippet.
- `POST /api/cloudinit/preview` — Render the final NoCloud user-data for a set of inputs without creating anything.

---

### 13. Appliances, Helper Scripts & Media

- `GET`/`POST /api/appliances` — Catalogue of community appliances; `PUT`/`DELETE /api/appliances/{id}` for custom entries.
- `POST /api/appliances/{id}/deploy` — Deploy an appliance. The target network is validated against the available bridges (`400` before any job is created) and the cloud-init payload is validated before queueing. A failed initialization marks the job `error`, writes no `AppInfo`, and audits `appliance.deploy_cloudinit_failed`.
- `GET /api/appliances/{id}/provision` — The provisioning script the appliance runs on first boot.
- `GET /api/helper-scripts`, `POST /api/helper-scripts/refresh`, `GET /api/helper-scripts/{slug}/provision` — Community helper-script catalogue.
- `GET`/`POST /api/media`, `POST /api/media/upload`, `DELETE /api/media/{id}`, `GET /api/media/{id}/raw`, `POST /api/media/apply-usage` — Card covers and branding assets.

---

### 14. System, Settings, Notifications & Audit

- `GET /api/system/status` — Backend, libvirt and host status, plus available updates.
- `GET /api/system/logs` — Recent service logs.
- `POST /api/system/update` — Trigger the in-app update (native installs only; see DOCKER.md). Requires the backend to run as root with `WEBKVM_ALLOW_UPDATE=1` in the service environment, otherwise `403`. Returns `202` with `{status, mode, updater, log}`; `mode` is `release` (install the verified GitHub release asset) or `source` (rebuild from the checkout), chosen automatically depending on whether `REPO_DIR` is a git checkout. `503` if the updater script or `systemd-run` is missing. Progress is appended to the returned `log` path.
- `POST /api/system/restart` and `POST /api/system/apply-restart` — Restart the service, optionally applying staged settings first.
- `POST /api/system/backup` and `GET /api/system/backups` — Datadir snapshots.
- `GET /api/system/cert` — Download the server's certificate so you can trust it locally (unauthenticated by design).
- `GET`/`PUT /api/settings` — Read or write server configuration; `GET /api/settings/schema` describes every field, `POST /api/settings/apply-live` applies what can change without a restart, `POST /api/settings/reset` restores defaults.
- `GET`/`PUT /api/notify/config`, `GET /api/notify/events`, `POST /api/notify/test` — Webhook/SMTP notification channels, the list of subscribable events, and a test delivery.
- `GET`/`POST`/`PUT`/`DELETE /api/nodes[/{id}]` — Remote node registry for multi-host views.
- `GET /api/audit` — Paginated, newest-first audit records across the active and rotated logs; `GET /api/audit/export` streams them for archival. **Admin only.**
- `GET /api/alerts/active`, `GET /api/tags`, `GET /api/jobs/{id}` — Active alerts, the global tag list, and generic job polling.

---

### VM Export (`GET /api/vms/{id}/export`)
- `?format=proxmox` — Produces a `vzdump-lxc-*.tar.zst` archive compatible with Proxmox `pct restore`. Only valid for `hypervisor=="incus"` containers. Generates `etc/vzdump/pct.conf` + `rootfs/` directory tree (zstd-compressed).

### VM Import (`POST /api/vms/import`)
- Auto-detects Proxmox `vzdump-lxc-*.tar.zst` / `.tar.gz` files (with `./etc/vzdump/pct.conf` or rootfs at root level) and converts them to Incus-native backups automatically via `internal/vzdump`. Handles both Proxmox layout (files at root) and Incus layout (`rootfs/` prefix), hard links, and symlinks. Output follows Incus `backup/container/` convention (`backup.yaml`, `index.yaml`, `rootfs/`).
- The **container name** field is mandatory when importing.
