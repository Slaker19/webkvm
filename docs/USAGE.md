# WebKVM — Usage guide

How to use WebKVM once installed. For installation and technical
documentation see [INSTALLATION.md](INSTALLATION.md).

## 1. First login

1. Open the URL printed by the installer (default `https://IP:8080`, or
   `http://IP:8080` if you chose plain HTTP).
2. Log in as `admin` with the password stored in
   `/opt/webkvm/admin-password.initial` (also shown at the end of the install).
3. The system will ask you to **change the password** on first login.

With a self-signed certificate the browser warns the first time. To remove the
warning download the certificate from `https://IP:PORT/api/system/cert` and
trust it in your OS.

## 2. Interface

The UI is a single-page app with a sidebar:

| Tab | What it does |
|-----|--------------|
| **VMs** | VM list/grid, creation, power actions, console. |
| **Storage** | Disk pools, volumes, ISO library. |
| **Networking** | NAT/bridge networks, host bridges, firewall. |
| **Backup** | Targets, schedules, jobs, archives and restore. |
| **Users** | Accounts, roles, groups, API tokens. |
| **Status** | Backend, libvirt, host status and logs. |
| **Settings** | Server configuration (port, TLS, CORS, etc.). |

## 3. Virtual machines

### Creating a VM

1. Go to **VMs → New VM**.
2. Pick the operating system (OS presets set firmware UEFI/BIOS, TPM/secure
   boot, disk bus, RAM/vCPU).
3. Attach an ISO from the **Storage** library (or upload one).
4. Choose the network (NAT for isolated internet access, or bridge for a real
   LAN IP).
5. Create it — the VM boots and you can open its console.

### Templates and cloud-init

- **Create a template**: with the VM powered off, use "Make template". You can
  then spawn new VMs from it.
- **cloud-init (NoCloud)**: when instantiating a template you can provision a
  user, password, SSH key and hostname automatically.
- **Clone**: duplicates an existing VM (disks included) in one click.

### Lifecycle and snapshots

- Power actions: start, shutdown, reboot, suspend, resume, force off.
- **Snapshots**: create from the VM detail page (disk-only or with memory),
  view the tree with history, revert or delete.
- **Autostart**: mark VMs to boot automatically when the host starts.

### Console and graphics

- **In-browser VNC console**: from the VM detail page (embedded noVNC,
  no plugins).
- **Serial console**: embedded terminal (80×24), survives guest reboots.
- **SPICE/RDP**: download `.vv` (SPICE) or `.rdp` files for external clients.
  The IP baked in comes from `PUBLIC_HOST` or the first non-loopback address.
- The serial/host consoles authenticate via short-lived single-use tickets —
  no long-lived credentials ever travel in URLs.

## 4. Storage

- **Pools**: the backend manages its own pools under `/opt/webkvm/pools`:
  - `webkvm-disks` — VM disks.
  - `ISOS` — ISO images.
- Additional pools of type `dir` (local) or `netfs` (NFS/SMB/CIFS) can be
  created; `iso`-purpose pools are read-only for volume operations.
- **Volumes**: create, resize and delete disks inside a pool.
- **ISO library**: upload, download (with progress) and delete ISOs from the web.

### Authenticated CIFS (SMB) network pools

To mount an SMB3 share with credentials (e.g. for backups):

1. **Storage → New Pool**, type `netfs`, format `cifs`, filling `source_host`,
   `source_dir`, `source_username` and `source_password`.
2. The password is stored as a **libvirt secret** (never returned by the API;
   only its UUID is kept on disk).
3. Rotate credentials by updating the pool (it must be stopped).
4. If libvirtd is reinstalled, recover the pool sending `cifs-needs-reauth:
   true` plus the current credentials.

## 5. Networking and firewall

### Virtual networks

- **NAT**: VMs reach the internet through the host (`192.168.122.0/24`).
- **Bridge (macvlan `br0`)**: VMs get their own LAN IP (DHCP or static).
- Extra networks with custom DHCP ranges (start/end, gateway, DNS) can be
  created, and autostart toggled per network.

### Per-VM firewall

From the VM detail page:

- **Inbound rules** (nftables) to expose specific ports.
- **Port forwarding** from the host to the VM.

Rules apply atomically with safeguards that prevent locking yourself out of SSH.

## 6. Backups

1. Create a backup **target** (local folder or NFS/SMB/SFTP mount).
2. Create a **schedule** (cron expression) or run one manually.
3. The runner produces one archive per VM (`vm-<name>.tar.zst`) plus a config
   archive (`config.tar.zst`), optional SHA-256 verification and automatic
   retention (keep-last / keep-days).
4. **Restore**: full restore (with size limits and protected-path checks) or
   "restore as VM" (re-imports the backup as a new VM).
5. Secrets (webhooks, SMTP, SFTP, CIFS) are never included in config backups.

Progress shows live in the notification center and under **Backup → Jobs**.

## 7. Alerts and notifications

Configure **webhook (HTTPS)** or **email (SMTP)** notifications for events like:

- A VM going down unexpectedly.
- Low disk space.
- Backup success/failure.

## 8. Quotas and scheduling

- **Per-user quotas**: limits on VM count, vCPUs, RAM and disk — enforced on
  create, clone, import, restore and resize. Admins are exempt.
- **VM scheduling**: cron-based auto power on/off (e.g. shut down test VMs at
  night).

## 9. Users, roles and API

### Roles (RBAC)

| Role | Capabilities |
|------|--------------|
| **admin** | Everything: users, settings, nodes, backups, destructive actions. |
| **operator** | Create/edit/delete VMs, power actions, disks, networks, pools, backups. |
| **viewer** | Read-only. |

- Create users from the **Users** tab and assign role/group.
- Users change their own password from **Account**.
- The initial password must be changed on first login.

### API tokens

From **Account → API tokens** create long-lived tokens (`wvmb_…`) for
scripting. Use them with header `Authorization: Bearer <token>`. Only the
SHA-256 hash is stored. Tokens expire (30 days by default) and are revocable.

### Audit log

Every sensitive action is recorded in `/opt/webkvm/audit.log` (JSONL) with
user, role, action, resource and source IP.

## 10. Community appliances

The **Community apps** dialog deploys ready-to-run VMs from official cloud
images (Ubuntu, Debian, Rocky, CentOS Stream, Fedora, Arch, Alpine, openSUSE)
plus turnkey appliances (Home Assistant OS, OpenWrt, OPNsense) and one-click
apps installed on first boot over Ubuntu 24.04 (WordPress, Nextcloud, Odoo,
Moodle).

Pick username/password (required for cloud-init images), target network and VM
name; deployment runs as a background job with live progress.

## 11. Logs and troubleshooting

```bash
systemctl status webkvm           # service status
journalctl -u webkvm -f           # live logs
cat /opt/webkvm/logs/backend.log  # when WEBKVM_LOG_FILE is enabled
```

| Symptom | Fix |
|---------|-----|
| Service won't start | `systemctl status webkvm` + `journalctl -u webkvm -n 100` |
| `/dev/kvm` missing | Enable virtualization in BIOS or nested virt |
| Browser certificate warning | Download cert from `/api/system/cert` and trust it |
| Can't connect to libvirt | Check `systemctl status libvirtd`; backend runs as root |
| Can't create a VM from ISO | Upload the ISO in **Storage** first, then attach it |
| Serial console says VM is off | Start the VM; the console reconnects automatically on next attempt |
