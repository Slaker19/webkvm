# Changelog

All notable changes to this project are documented in this file,
following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/).

Spanish version: [CHANGELOG.es.md](CHANGELOG.es.md).

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

[0.0.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.0.1
