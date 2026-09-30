# Changelog

All notable changes to this project are documented in this file,
following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/).

Spanish version: [CHANGELOG.es.md](CHANGELOG.es.md).

## [0.1.2-dev] — Unreleased

### Added

- **Live VM hardware & ballooning:**
  - Memory ballooning floor (`min_ram_mb`) support for dynamic memory reclaiming with guaranteed lower bounds.
  - Dedicated IOThreads (`iothreads`) support for VirtIO-SCSI storage controllers and event loop concurrency.
- **Cloud-Init & Direct Network Configuration:**
  - Direct Linux bridge network configuration with custom static IP/Subnet (CIDR), default gateway, and primary/secondary DNS server support.
  - Static IPv4/IPv6 CIDR, default gateway, and DNS configuration in Cloud-Init NoCloud seeds.
  - Visual selector in creation and edit wizards between DHCP and Static IP modes.
- **QEMU Guest Agent extensions:**
  - Filesystem TRIM (`fstrim`) API endpoint `POST /api/vms/{id}/guest/fstrim` and UI button in Guest tab.
  - Active guest sessions/users (`guest-get-users`) and guest timezone (`guest-get-timezone`) telemetry.

### Fixed

- **Netplan & NetworkManager Compatibility in Setup Scripts:**
  - Prioritized Netplan as the top-level declaration layer on modern Ubuntu/Debian systems, avoiding conflicting ephemeral network units.
  - Fixed NetworkManager bridge creation by referencing connection UUIDs to prevent word-splitting failures on connection names with spaces (`Wired connection 1`).
  - Added proper bridge slave profile generation (`nmcli con add type ethernet master ...`) in NetworkManager mode to ensure carrier persistence across reboots.
  - Filtered out IPv6 nameservers from IPv4 static DNS detection to prevent invalid NetworkManager keyfile generation and startup rejections.
  - Disabled `systemd-networkd` when NetworkManager is the active Netplan renderer, preventing dual-manager boot deadlocks in `*-wait-online.service`.
  - Added async interface polling loops to bridge setup steps to reliably await sysfs bridge creation.

## [0.1.1] — 2026-09-29

### Added

- **In-app self-update:**
  - Automated update support via `POST /api/system/update` and `/#/status`.
  - Automatic detection of update mode: **release** (downloads and installs verified GitHub release binaries) or **source** (pulls git checkout and rebuilds frontend/backend).
  - Checksum verification against published `SHA256SUMS` with fail-closed security before running root installers.
  - Transient systemd unit execution via `systemd-run` to isolate the updater from the main service cgroup during service stops and restarts.
  - Concurrency locking using `flock` on `/run/webkvm-update.lock`.
  - Staging and atomic binary replacement with automatic health check and rollback recovery.
  - Dynamic service port detection from `config.json` and systemd environment.
  - Status UI buttons, modals, and translations in English, Spanish, and Catalan.

## [0.1.0] — 2026-09-29

### Added

- **Incremental backup chains:**
  - Libvirt push-mode incremental backups with `CheckpointEntry` disk tracking.
  - Multi-disk checkpoint support with `DiskFiles` mapping to prevent cross-device rebase issues.
  - Target-staged restore to avoid `EXDEV` errors across mounted filesystems.
  - Chain-aware retention policy protecting incremental chains during pruning.
  - External storage pool support for `.qcow2` listings across SFTP, S3, and local directories.

### Fixed

- Sanitized storage pool and target destination paths to resolve security alerts.
- Multiline domain XML disk parsing and CD-ROM exclusion during backups.

---

## [0.0.1] — 2026-09-26

First public release of WebKVM: a self-hosted web panel (a single Go
binary with the Svelte frontend embedded) for managing a Linux
virtualisation host.

### Added

- **KVM/QEMU virtual machines via libvirt:** creation, cloning, hardware
  editing, in-browser VNC and serial console, snapshots, disks and
  storage migration between pools.
- **Incus/LXC containers** (optional module) managed from the same
  interface as VMs, including the Incus image catalogue. Containers pick
  their storage pool at creation time and can be moved between
  `container` pools afterwards — rootfs and snapshots relocated by
  Incus's native migration, with the instance stopped.
- **Storage:** local and network pools (NFS/SMB) with purposes
  (`disk`, `container`, `iso`, `backup`, `template`), physical disk
  inspection and a single media store for ISOs and images.
- **Networking:** bridges, NAT/isolated networks and unified
  Proxmox-style L2 networks, with a per-VM firewall.
- **Backups**, scheduled and on demand, with restore plus
  import/export in Proxmox vzdump format for containers.
- **Templates** for VMs and containers, with an admin-only
  "share with all users" toggle per template: a shared template is
  listable and instantiable by everyone regardless of owner, group or
  tag access, and every instantiation copies its full disk.
- **Users, roles and quotas:** RBAC with per-resource ACLs, resource
  quotas, 2FA/TOTP, API tokens and an audit log.
- **Cloud-init** for initial guest configuration (users, SSH keys,
  network, packages).
- **App store:** ready-to-deploy appliances and helper scripts, plus a
  catalogue of official cloud images.
- **PCI and USB passthrough** with an IOMMU/VFIO preflight check.
- **Interface in three languages:** English, Spanish and Catalan.
- **Installation:** multi-distro standalone installer (apt/dnf/pacman)
  with self-signed HTTPS and rollback, a Docker image, deb/rpm packages
  and a command-line client (`webkvm-cli`).

---

[0.1.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.1
[0.1.0]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.0
[0.0.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.0.1
