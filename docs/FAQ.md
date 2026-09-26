# WebKVM — Frequently Asked Questions (FAQ)

Comprehensive answers to common architectural, technical, operational, and troubleshooting questions about WebKVM.

[**English**](FAQ.md) • [**Español**](FAQ.es.md)

---

## Table of Contents

1. [General & Architecture](#1-general--architecture)
2. [Virtual Machines & QEMU/KVM](#2-virtual-machines--qemukvm)
3. [Incus / LXC Containers](#3-incus--lxc-containers)
4. [Networking & Dynamic IPs](#4-networking--dynamic-ips)
5. [Storage Pools & Image Hub](#5-storage-pools--image-hub)
6. [WebGL Terminal & Consoles](#6-webgl-terminal--consoles)
7. [Security & Access Control](#7-security--access-control)
8. [Troubleshooting & Diagnostics](#8-troubleshooting--diagnostics)

---

## 1. General & Architecture

### What makes WebKVM different from Proxmox VE or Cockpit?
- **Proxmox VE** requires dedicating an entire machine or reformatting with its custom Debian distribution, running multiple heavy background daemons that consume 1–2 GB of RAM at idle.
- **Cockpit** is a basic server administrator dashboard that lacks native Incus/LXD container management, image hub downloads, automated cloud-init injection, safe firewall rollback, and high-performance WebGL terminal integration.
- **WebKVM** provides a modern cloud-like experience (instant COW clones, cloud-init templates, Incus containers, and WebGL terminal) packaged into a **single ~14 MB static Go binary** that uses **under 20 MB of RAM** and installs on top of your existing Linux distribution without modifying your OS.

### What are the minimum system requirements?
- **CPU**: 1 core (x86_64 with VT-x/AMD-V virtualization extensions enabled in BIOS/UEFI). ARM64 support is also supported.
- **RAM**: 2 GB RAM minimum (WebKVM itself uses ~15 MB; the remaining RAM is available for your VMs and containers).
- **Disk**: 5 GB free disk space for host binaries and base images.
- **OS**: Any standard Linux distribution (Debian 11+, Ubuntu 20.04+, Fedora 38+, Arch Linux, AlmaLinux 9+, Rocky Linux 9+).

### Does WebKVM work on Raspberry Pi or ARM64 boards?
Yes! When compiled for `linux/arm64`, WebKVM manages ARM64 KVM virtual machines (via UEFI AAVMF) and native ARM64 Incus/LXC containers with full hardware acceleration.

### Can WebKVM run inside another VM (Nested Virtualization)?
Yes. If your cloud provider or hypervisor supports nested virtualization (e.g. `kvm_intel.nested=1` or `kvm_amd.nested=1`), WebKVM will detect `/dev/kvm` and run hardware-accelerated guests seamlessly.

---

## 2. Virtual Machines & QEMU/KVM

### Should I choose QCOW2 or RAW disk format?
- **QCOW2 (Recommended)**: Thin-provisioned (only takes space on disk as the guest writes data), supports fast snapshots, instantaneous Copy-On-Write (COW) clones from base images, and live volume expansion.
- **RAW**: Direct block layout with marginally faster raw I/O performance, but consumes the entire disk allocation upfront and does not support backing files or instantaneous snapshots.

### How does Cloud-Init provisioning work?
WebKVM utilizes the standard `NoCloud` ISO/seed mechanism. When you create or instantiate a VM from a cloud template, WebKVM generates an ephemeral ISO containing `#cloud-config` directives (user accounts, authorized SSH keys, network interfaces, hostname, and post-installation bash scripts). The guest OS reads this disk on initial boot and self-configures in seconds.

### Are snapshots filesystem-consistent?
Yes. When the guest has `qemu-guest-agent` installed (automatically pre-configured by WebKVM cloud templates), WebKVM issues a filesystem freeze (`fsfreeze`) prior to taking live disk snapshots, ensuring zero database or filesystem corruption.

### Can I resize a VM disk while it is running?
Yes! For QCOW2 disks attached via VirtIO, WebKVM supports **live disk expansion** without guest downtime. Simply increase the disk size in the UI or CLI; the hypervisor expands the block allocation live, and modern Linux kernels automatically detect the new disk boundary.

---

## 3. Incus / LXC Containers

### What is the difference between Incus containers and Docker containers?
- **Docker / Podman**: Application containers designed to run a single microservice process (e.g., `nginx` or `redis`) with ephemeral root filesystems.
- **Incus / LXC**: Full **system containers** that run a complete Linux distribution with their own `systemd` init process, SSH server, package manager (`apt`/`apk`/`dnf`), syslog, and cron jobs. They feel and behave exactly like virtual machines, but launch in 200 milliseconds and consume only 30–50 MB of RAM.

### Can KVM VMs and Incus containers share the same network?
Yes. Both KVM virtual machines and Incus system containers attach to the same Linux bridges (`vmbr0` for LAN access and `vmbr1` for private NAT). VMs and containers can communicate directly with each other at full hardware line speed.

---

## 4. Networking & Dynamic IPs

### What happens if the host machine changes its IP address via DHCP?
WebKVM handles dynamic host IPs seamlessly:
1. **Server Sockets**: The Go backend binds to `0.0.0.0:8080`, continuously accepting traffic on any interface regardless of IP changes.
2. **Dynamic UI Endpoints**: The Svelte web frontend resolves all API and WebSocket endpoints dynamically from `window.location.host`.
3. **Guest Network Isolation**: VMs on the bridged network (`vmbr0`) maintain their own independent DHCP leases with your router, while VMs on `vmbr1` use WebKVM's internal NAT router (`100.0.0.1`).

### How do I use WebKVM on a laptop connected over Wi-Fi?
Standard Wi-Fi interfaces (`wlan0`) block multi-MAC layer-2 bridging at the hardware level. When using a Wi-Fi laptop:
- Select the **NAT Network (`vmbr1`)** when creating VMs.
- WebKVM routes the virtual machine traffic through the host's active Wi-Fi connection transparently.

### How do I connect to WebKVM using a local hostname instead of an IP?
You can access WebKVM using its mDNS hostname: `https://<hostname>.local:8080` (e.g., `https://webkvm.local:8080`). mDNS is supported natively across macOS, iOS, Android, Linux, and Windows.

---

## 5. Storage Pools & Image Hub

### What is "Pool Purpose" separation?
To prevent accidental corruption and security misconfigurations, WebKVM strictly classifies storage pools into:
- **`disk` pools**: Designated for VM root volumes, data disks, and container storage.
- **`iso` pools**: Designated for read-only installation media.
The backend validates pool purposes fail-closed, preventing disks from being uploaded into ISO pools or vice versa.

### How does the Image Hub accelerate deployments?
Instead of downloading ISOs and clicking through OS installers, the Image Hub lets you cache official pre-built cloud images (`ubuntu-24.04.qcow2`, `debian-12.qcow2`, `alpine-3.24.qcow2`). Creating a VM makes a thin COW overlay in milliseconds without duplicating the original base disk.

---

## 6. WebGL Terminal & Consoles

### Why does the Host Terminal prompt for system login?
For strict security and multi-user administration, WebKVM executes `/bin/login -p` over a secure PTY rather than opening an unauthenticated root shell. You must authenticate using valid Linux system user credentials, preventing unauthorized browser access.

### Why do TUI tools like `btop`, `htop`, `mc`, and `nano` work smoothly?
1. **TrueColor & UTF-8**: The PTY environment enforces `TERM=xterm-256color`, `COLORTERM=truecolor`, and `LANG=C.UTF-8`.
2. **Unicode 11 Engine**: xterm.js utilizes the official Unicode 11 addon to calculate precise cell widths for emojis, braille patterns, and box-drawing symbols.
3. **Binary WebSocket Pipeline**: Data frames flow as raw binary buffers without chunked UTF-8 validation errors.
4. **GPU WebGL Acceleration**: 60 FPS rendering prevents tearing and redraw lag during high-frequency telemetry updates.

### Will navigating between WebKVM tabs kill my terminal session?
No! `HostConsole` remains mounted and active in the background. You can navigate to *Virtual Machines*, *Storage*, or *Settings* and return to the terminal with your running commands, `btop`, or `nano` sessions intact.

---

## 7. Security & Access Control

### How does WebKVM protect WebSocket connections?
WebKVM uses **single-use ephemeral tickets** stored in memory with a 30-second time-to-live (TTL). When opening a terminal or console, the frontend requests a ticket over authenticated REST and passes it in the WebSocket upgrade. The ticket is immediately burned upon connection, ensuring no long-lived tokens exist in URLs or proxy logs.

### How does the SSRF safe dialer work?
When downloading images or ISOs from external URLs, WebKVM resolves DNS queries upfront and rejects any destination falling within loopback (`127.0.0.0/8`), private RFC 1918 ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), link-local, carrier-grade NAT, or IPv6 equivalents, re-validating every HTTP redirect hop.

### What is the Firewall "Safe-Apply" feature?
When modifying host firewall rules, WebKVM stages the rules with a 30-second confirmation window (configurable between 10 and 300 seconds via `firewall.confirm_window_secs`). If the administrator loses connectivity due to a bad rule and never confirms, the firewall automatically rolls back to the previous stable state, preventing accidental lockouts.

---

## 8. Troubleshooting & Diagnostics

### I forgot the initial admin password. How can I reset it?
If you have root access to the host machine:
```bash
# Generate a new admin password
webkvm-cli users update admin --password "NewSecurePassword123!"
```
Or remove `/opt/webkvm/users.json` and restart the service to re-generate initial credentials.

### What should I check if a VM fails to start?
1. Check KVM hardware acceleration: `ls -l /dev/kvm` (permissions should be `crw-rw---- root kvm`).
2. Verify available RAM: `free -h`.
3. Inspect backend logs: `journalctl -u webkvm -n 50 --no-pager`.
