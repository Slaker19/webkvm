# WebKVM — Usage guide

How to use WebKVM once installed. For installation and technical
documentation see [INSTALLATION.md](INSTALLATION.md).

[**English**](USAGE.md) • [**Español**](USAGE.es.md)

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
5. Advanced options: recommended disk performance (`cache=none, io=native`),
   space-reclaiming discard/TRIM, a CPU model dropdown with common presets
   (or "Custom" for a manual model), and an explicit CPU topology
   (sockets/cores/threads) instead of a flat vCPU count. A virtio hardware
   RNG is always attached (no toggle needed).
6. Create it — the VM boots and you can open its console.

### USB device passthrough (admin only)

From the VM's **Interfaces** tab, admins can attach a host USB device
directly to a running or stopped VM (and detach it again), listing what's
plugged into the host.

### PCI / GPU passthrough (admin only)

From the VM's **Hardware** tab, admins can bind a host PCI device (GPU,
NIC, NVMe controller…) to a stopped VM. WebKVM runs a **preflight check**
first — IOMMU enabled, the device's IOMMU group isolated, and `vfio-pci`
available — and refuses to attach when the group would drag unrelated
devices along with it.

### Templates and cloud-init

- **Create a template**: with the VM powered off, use "Make template". You can
  then spawn new VMs from it.
- **cloud-init (NoCloud)**: when instantiating a template you can provision a
  user, password, SSH key and hostname automatically.
- **Clone**: duplicates an existing VM (disks included) in one click.

#### Sharing a template with every user (admin only)

By default a template is visible only to its owner and to whoever the
normal ACL, group and tag rules already grant access to. A template's
detail page carries a **"Share with all users"** toggle, which only
admins can flip.

Once shared:

- **Every user can list and instantiate it**, regardless of owner, group
  or tag access. The toggle *bypasses* the usual scoping — it is not an
  additional ACL entry, so review the template's contents before
  enabling it.
- **Each instantiation makes a full copy of the template's disk.** Ten
  users instantiating a 40 GiB template consume 400 GiB. The UI shows
  this warning next to the toggle for exactly that reason.
- **Cloning a shared template does not carry the flag over.** The clone
  comes back private, and an admin must share it deliberately.

Turning the toggle off again hides the template from other users; it
does not touch instances already created from it.

### Lifecycle and snapshots

- Power actions: start, shutdown, reboot, suspend, resume, force off.
- **Snapshots**: create from the VM detail page (disk-only or with memory),
  view the tree with history, revert or delete. Disk-only snapshots of a
  running VM are filesystem-consistent when the guest agent is available
  (quiesced via cloud-init's pre-installed `qemu-guest-agent`), falling back
  to a crash-consistent snapshot otherwise.
- **Disk resize**: grow a disk while the VM is shut off (any change) or
  while it's running (grow-only, qcow2 disks only) — no downtime needed to
  add space to a live VM's disk.
- **Autostart**: mark VMs to boot automatically when the host starts.

### Console and graphics

- **In-browser VNC console**: from the VM detail page (embedded noVNC).
  Auto-reconnects with backoff if the connection drops, and its own sidebar
  panel exposes image quality/compression sliders, a dot-cursor toggle, and
  power actions (reboot/shutdown/force off) without leaving the console.
- **Serial console**: embedded terminal (80×24), survives guest reboots.
- **SPICE/RDP**: download `.vv` (SPICE) or `.rdp` files for external clients.
  The IP baked in comes from `PUBLIC_HOST` or the first non-loopback address.
- Every embedded console (serial, host terminal, VNC) authenticates via
  short-lived tickets — no long-lived session credentials ever travel in
  URLs. The VNC ticket is scoped to that one VM and console-related action
  and expires after an hour (reusable within that window, since the console
  can reconnect and its power buttons need it too), unlike the single-use,
  30-second tickets the serial/host terminals use.

## 3.1. Containers (LXC)

WebKVM manages **LXC containers natively** (installed by default by the
standalone installer; see
[INSTALLATION.md](INSTALLATION.md) → "Containers (LXC)"). Containers share the
same UI as VMs:

- **Unified list**: containers appear alongside VMs, each card showing a
  `KVM`/`Incus` badge and a provisioning chip; the `All · VMs · Containers`
  filter narrows the list.
- **Creation**: the same "Create" form with the instance-type selector set to
  **Container (Incus / LXC)** — pick a distribution from the image dropdown (or
  "Custom / Other…"), choose the network (same bridges as VMs), and set the
  **Incus / LXC credentials** (username optional; blank = the password is
  applied to `root`). No ISOs, no manual YAML.
- **Storage pool**: step 3 of the wizard ("Resources") offers a **storage
  pool** selector listing only pools whose purpose is `container`
  (defaulting to `webkvm-incus`). VM-disk, ISO, backup and template pools
  never appear there — and container pools never appear when you are
  creating a KVM VM, because libvirt and Incus manage separate worlds.
- **Detail**: containers support root-disk **resize**, **add/remove/change**
  network interfaces, serial console and live CPU/RAM **metrics**. KVM-only
  controls (chipset/UEFI/TPM, VNC, CD-ROM, guest-agent password reset, clone)
  are hidden.

#### Moving a container to another storage pool

Use **"Move storage"** in the sidebar of the container's detail page.
It relocates the rootfs — snapshots included — to another `container`
pool using Incus's native migration, reporting byte-level progress.

- The container **must be stopped**; a running instance is refused.
- The destination must be a *different* `container` pool.
- Moves **between hypervisors** (a KVM pool ⇄ an Incus pool) are refused
  by design. Export and re-import instead.

> The Storage page's **LXC** tab deliberately offers no move button. A
> container rootfs is a `container`-type volume owned by its instance,
> not a detachable `custom` volume, so it is moved from the instance —
> not from the volume list, where it does not appear.

## 4. Storage

- **Pools**: the backend manages its own pools under `/opt/webkvm/pools`:
  - `webkvm-disks` — VM disks.
  - `webkvm-isos` — ISO images.
  - `webkvm-incus` — container rootfs (when the container module is on).
- Additional pools of type `dir` (local) or `netfs` (NFS/SMB/CIFS) can be
  created; `iso`-purpose pools are read-only for volume operations.
- **Purposes**: every pool declares what it may hold — `disk` (libvirt
  qcow2/raw), `container` (Incus rootfs), `iso`, `backup` or `template`.
  A pool may combine several. Purposes are what every pool selector in
  the UI filters on, so a container can never land in a VM-disk pool and
  a qcow2 can never land in the ISO library.
- **Moving storage**: a VM disk moves from **Storage → Disks → Move**;
  a container rootfs moves from its own **instance detail page** (see
  "Containers (LXC)" above). Moving across hypervisors is not possible.
- **Volumes**: create, resize and delete disks inside a pool, or **upload an
  existing disk image** (`.qcow2`, `.img`, `.raw`, `.qed`) directly into a
  pool — the file streams straight to disk and libvirt auto-detects its
  format on refresh, no conversion needed.
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

### Host bridges

WebKVM uses **real OS-level Linux bridges** (Proxmox-style); libvirt virtual
networks (`default`, `virbr0`, …) are gone.

- **Shared L2 `vmbr0`/`br0`** (default, recommended): VMs and containers land
  on the real LAN and get their IP from the router (DHCP or static).
- **Isolated NAT `vmbr1`** (opt-in): a Linux bridge on a kernel `dummy`
  interface with `100.0.0.1/24`, `dnsmasq` DHCP and `MASQUERADE`.
- Extra **host bridges** with an optional IP + custom DHCP range
  (start/end, gateway, DNS) can be created from the **Networking** page, and
  autostart toggled per bridge.

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
