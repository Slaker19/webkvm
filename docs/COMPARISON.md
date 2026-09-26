# WebKVM vs. Alternative Virtualization Platforms

A comprehensive, objective technical comparison between **WebKVM**, **Proxmox VE**, **Cockpit (Machines)**, **virt-manager**, and **Portainer**.

[**English**](COMPARISON.md) • [**Español**](COMPARISON.es.md)

---

## Feature Comparison Matrix

| Architectural Category | WebKVM | Proxmox VE | Cockpit (Machines) | virt-manager | Portainer |
|---|---|---|---|---|---|
| **Architecture & Binary** | Single Go binary (~14 MB) + Embedded Svelte 5 | Multi-daemon Perl/C stack (PVE proxy, daemons) | Python / systemd D-Bus modules | Desktop GTK client | Go backend + Docker daemon |
| **Idle Memory Footprint** | **~15 MB RAM** | 1.2 GB – 2.5 GB RAM | ~80 MB RAM | N/A (Desktop client) | ~40 MB RAM |
| **Host System Preservation** | **100% untouched** (Standard OS) | Requires custom Debian ISO | Untouched | Untouched | Docker installed |
| **KVM Virtual Machines** | Yes — full support | Yes — full support | Yes — basic support | Yes — full support | No (containers only) |
| **LXC / Incus Containers** | **Yes — native Incus/LXD view** | Partial — custom PVE LXC scripts | No | No | No (app containers only) |
| **Cloud-Init & 1-Click Images** | **Yes — built-in Image Hub, 5s deploy** | Partial — manual template cloning | No — manual | No — manual | Partial — app templates |
| **Web Terminal Quality** | **WebGL + Unicode 11 + TUI keys** | Partial — basic xterm.js | Partial — basic terminal | No — desktop terminal only | Partial — basic exec shell |
| **Live Transfer Rate & ETA** | **Yes — real-time MB/s & ETA** | Partial — percentage only | No | No | Partial — basic layer pull |
| **Zero-Token Local CLI** | **Yes — auto-authenticating CLI** | Partial — `pvesh` / `qm` / `pct` | No | Partial — `virsh` only | No |
| **Web-Based Multi-View Grid** | **Yes — interactive multi-console** | No — 1 console at a time | No — 1 console at a time | Partial — multiple desktop windows | No |
| **Firewall Safe-Apply Rollback** | **Yes — 30s auto-rollback timer** | No — manual PVE firewall | No — firewalld module | No | No |
| **Single-Use WebSocket Tickets** | **Yes — 30s ephemeral in RAM** | Partial — session tickets / cookies | Partial — cookie auth | No — local socket | Partial — JWT in headers |

---

## Deep Dive by Platform

### 1. WebKVM vs. Proxmox VE

**When to choose Proxmox VE:**
- You are building an enterprise datacenter cluster with 3+ dedicated hypervisors requiring Corosync quorum, live VM migration over 10GbE, and Ceph distributed storage pools.
- You want to dedicate the entire physical machine exclusively to virtualization and do not plan to run other services directly on the host OS.

**When to choose WebKVM:**
- You want to turn an existing Linux server (Ubuntu, Debian, Fedora, Arch, AlmaLinux) into a fast, private virtualization platform without wiping the disk or replacing the OS.
- You care about minimal resource consumption (saving 1–2 GB of RAM for actual workloads on mini-PCs, homelabs, or cloud VPS instances).
- You want a unified, modern interface for both full KVM VMs and lightweight Incus system containers.
- You prefer single-binary simplicity with zero background clutter and instant updates.

---

### 2. WebKVM vs. Cockpit (Machines Module)

**When to choose Cockpit:**
- You only need basic server monitoring (inspecting systemd logs, storage mounts, and user accounts) and only run one or two simple VMs occasionally.

**When to choose WebKVM:**
- You need a dedicated, professional virtualization workflow:
  - **Incus/LXC integration**: Cockpit only supports KVM via libvirt-dbus and has no native system container support.
  - **Image Hub & Cloud-Init**: Cockpit requires downloading manual ISOs and stepping through OS installers; WebKVM deploys pre-configured VMs in 5 seconds.
  - **Storage & Networking**: WebKVM manages dedicated storage pools (with purpose enforcement), dual bridge/NAT routing, and advanced host firewall rules with safe rollback.
  - **Terminal Performance**: WebKVM features a GPU-accelerated WebGL terminal with Unicode 11 and TUI function keys (`F1`–`F12`), ensuring tools like `btop`, `htop`, and `mc` render without distortion.

---

### 3. WebKVM vs. virt-manager

**When to choose virt-manager:**
- You are sitting directly in front of a Linux desktop with an active X11/Wayland graphical session and prefer a native GTK desktop window.

**When to choose WebKVM:**
- You want 100% web-based access from any device (laptop, tablet, phone, Chromebook, or remote workstation) without configuring SSH X11 forwarding or VNC client software.
- You manage remote servers, cloud instances, or headless homelabs.
- You need team access with role-based permissions (Admin, Operator, Viewer) and comprehensive audit logging.

---

### 4. WebKVM vs. Portainer / Docker

**When to choose Portainer:**
- You only run Docker microservices (packaged as application containers with ephemeral state, e.g., Nextcloud, Plex, Traefik).

**When to choose WebKVM:**
- You need **real virtual machines** (running Windows, FreeBSD, custom kernels, or nested hypervisors) or **Incus system containers** (full Linux OS with systemd init, dedicated IP addresses, package managers, and persistent disk volumes).

---

## Summary

WebKVM occupies the **ideal sweet spot** in modern infrastructure:
1. It avoids the heavy, proprietary lock-in of enterprise hypervisor distributions.
2. It vastly exceeds the capabilities and visual responsiveness of generic server dashboards.
3. It gives administrators and homelab enthusiasts an aesthetic, cloud-native experience while keeping full control over the underlying Linux machine.
