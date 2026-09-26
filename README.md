# WebKVM

<div align="center">

**Lightweight, Single-Binary Native Virtual Machine & Container Manager for Linux**

[![CI Status](https://github.com/Slaker19/webkvm/actions/workflows/ci.yml/badge.svg)](https://github.com/Slaker19/webkvm/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?style=flat&logo=go)](https://golang.org)
[![Svelte Version](https://img.shields.io/badge/Svelte-5_Runes-FF3E00?style=flat&logo=svelte)](https://svelte.dev)
[![TailwindCSS](https://img.shields.io/badge/TailwindCSS-v4-38B2AC?style=flat&logo=tailwind-css)](https://tailwindcss.com)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPLv3-blue.svg)](LICENSE)
[![Zero Dependencies](https://img.shields.io/badge/Runtime_Dependencies-0-success.svg)](#architecture)

[**English**](README.md) • [**Español**](README.es.md) • [**Documentation**](docs/USAGE.md) • [**FAQ**](docs/FAQ.md) • [**CLI Guide**](docs/CLI.md) • [**API Reference**](docs/API.md)

</div>

---

## What is WebKVM?

**WebKVM** turns any existing Linux server (Ubuntu, Debian, Fedora, Arch, AlmaLinux, Rocky) into a fast, private cloud virtualization platform **in 1 minute without reformatting or hijacking the operating system**.

Built in **Go** with CGO bindings to `libvirt` and an embedded **Svelte 5** single-page web app, WebKVM compiles down to a **single ~14 MB static executable**. It consumes **around 15 MB of RAM** at idle and provides unified management for full **QEMU/KVM Virtual Machines** and lightweight **Incus/LXC System Containers**.

```text
┌─────────────────────────────────────────────────────────────────────────┐
│                              WebKVM Web UI                              │
│                 Svelte 5 • Tailwind v4 • WebGL Terminal                 │
└────────────────────────────────────┬────────────────────────────────────┘
                                     │ HTTPS / WSS / REST
┌────────────────────────────────────▼────────────────────────────────────┐
│                    WebKVM Server (Single Go Binary)                    │
│   Auth / JWT • WebSocket Proxy • Image Hub • Firewall • Storage Pools   │
└──────────────────┬──────────────────────────────────┬───────────────────┘
                   │ CGO / Unix Socket                │ REST / Unix Socket
┌──────────────────▼──────────────┐ ┌─────────────────▼───────────────────┐
│          QEMU / KVM             │ │            Incus / LXD              │
│    Hardware Virtualization      │ │       LXC System Containers         │
│  (Ubuntu, Windows, FreeBSD...)  │ │     (Alpine, Debian, Ubuntu...)     │
└─────────────────────────────────┘ └─────────────────────────────────────┘
```

---

## Why WebKVM? (The Sweet Spot)

| Feature / Metric | WebKVM | Proxmox VE | Cockpit (Machines) | virt-manager |
|---|---|---|---|---|
| **Installation Footprint** | **Single binary (~14 MB)** | Entire ISO / OS takeover | System daemon & modules | Desktop GTK application |
| **Idle RAM Usage** | **~15 MB** | 1.2 GB – 2.5 GB | ~80 MB | N/A (Client only) |
| **Host System Preservation** | **100% untouched** (Standard distro) | Requires custom Debian fork | Untouched | Untouched |
| **KVM Virtual Machines** | Yes — full support | Yes — full support | Yes — basic support | Yes — full support |
| **Incus / LXC Containers** | **Yes — native unified view** | Partial — custom LXC scripts | No native Incus | No |
| **Cloud-Init & Image Hub** | **Yes — 1-click 5-second deploy** | Partial — manual template setup | No — manual | No — manual |
| **Embedded Web Terminal** | **WebGL + Unicode 11 + TUI keys** | Partial — basic noVNC / xterm | Partial — basic terminal | No — desktop terminal only |
| **Live Transfer Speed & ETA** | **Yes — real-time MB/s & progress** | Partial — basic progress | No | No |
| **Zero-Token Local CLI** | **Yes — `webkvm-cli` auto-auth** | Partial — `pvesh` / `qm` | No — `cockpit-bridge` | Partial — `virsh` only |
| **Multi-Console Live Grid** | **Yes — Multi-View interactive grid** | No — 1 VM at a time | No — 1 VM at a time | Partial — multiple windows |

---

## Quickstart (1-Minute Installation)

### Native Install (Recommended)

Run the unattended installer on your server as root:

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-webkvm.sh | sudo bash
```

The installer auto-detects your package manager (`apt`, `dnf`, `pacman`), installs required virtualization packages (`qemu-kvm`, `libvirt-daemon-system`), configures dual bridge/NAT networking, generates TLS certificates, and registers the `webkvm.service` systemd unit.

**Access the Web UI**:
- URL: `https://<YOUR-SERVER-IP>:8080` (or `https://localhost:8080` if running locally)
- Initial credentials are displayed at the end of the installation and stored at `/opt/webkvm/admin-password.initial`.

### Docker Install

If you prefer running WebKVM in a container against your host's libvirt socket:

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-docker.sh -o /tmp/webkvm-docker-install.sh && sudo bash /tmp/webkvm-docker-install.sh
```
*(See [docs/DOCKER.md](docs/DOCKER.md) for custom volume mounts and security privileges).*

---

## Key Capabilities

### 1. Unified Hybrid Compute (KVM VMs + Incus Containers)
- Manage hardware-virtualized VMs (Windows, Linux, BSD) and sub-second LXC system containers under a single unified dashboard.
- Real-time CPU, RAM, Disk, and Network telemetry via Server-Sent Events (SSE).
- Live hot-plug disk resizing without guest downtime.

### 2. Image Hub & Instant Cloud-Init Deployments
- Pre-curated official cloud images (`.qcow2`) for Ubuntu, Debian, Alpine, Fedora, Rocky, and AlmaLinux.
- Deploy instances in **under 5 seconds** using Copy-On-Write (COW) backing files.
- Inject SSH public keys, usernames, passwords, and custom `#cloud-config` scripts automatically on boot.

### 3. High-Performance WebGL Host Terminal
- Powered by modern `@xterm/xterm` with **GPU WebGL acceleration** and **Unicode 11** for flawless rendering of `btop`, `htop`, `mc`, `nano`, and `yazi`.
- Dedicated TUI toolbar with function keys (`F1`–`F12`), `Esc`, `Tab`, and `Ctrl+C`.
- **Background Persistence**: Navigating between sections in WebKVM preserves active shell sessions without disconnecting.
- Secure system PAM authentication (no insecure automatic root logins).

### 4. Live Download Speed & Metrics
- Real-time download rate smoothing (e.g. `52.4 MB/s · 120 MB / 1.8 GB · ETA 25s (45%)`).
- Globally tracked in the bottom dockable **Task Drawer** (`Ctrl+J` / `⌘J`).

### 5. Multi-View Matrix
- Monitor and interact with multiple running VM graphical consoles simultaneously in a customizable live grid.

### 6. Built-in Security Architecture
- **Ephemeral Single-Use Tickets**: WebSockets authenticate using 30-second single-use RAM tickets. No long-lived JWTs are ever leaked in query strings.
- **Anti-SSRF Safe Dialer**: Remote image downloads validate and block private/loopback/link-local IPv4 and IPv6 address ranges with strict redirect re-validation.
- **Firewall Safe-Apply**: Host firewall changes include an automated 30-second rollback timer to prevent accidental lockouts.

---

## Zero-Token Command Line Interface (`webkvm-cli`)

WebKVM includes a full-featured CLI tool (`webkvm-cli`) designed for automation, scripting, and SSH administration.

When run on the host as root, **it requires zero tokens or login steps** (auto-authenticates via local key):

```bash
# List all virtual machines and containers
webkvm-cli vms list

# Deploy a new Debian 12 VM in 5 seconds
webkvm-cli vms create --name debian-prod --ram 2048 --vcpus 2 --disk 20 --image debian-12

# Create a lightweight Alpine Incus container
webkvm-cli vms create --name alpine-ct --ram 512 --vcpus 1 --type container --image images:alpine/3.21

# Download an official cloud image
webkvm-cli images pull-cloud ubuntu-24.04

# Scripting with jq (--json flag)
webkvm-cli --json vms list | jq '.[] | select(.state=="running") | .name'
```
*(See [docs/CLI.md](docs/CLI.md) for full syntax and examples).*

---

## In-Depth Documentation

| Guide | Description |
|---|---|
| [**User Guide (USAGE.md)**](docs/USAGE.md) | Comprehensive walkthrough of VM creation, storage pools, networks, cloud-init, and backups. |
| [**Installation Guide (INSTALLATION.md)**](docs/INSTALLATION.md) | Step-by-step setup, custom ports, dual bridge/NAT networks, Let's Encrypt TLS, and systemd units. |
| [**Frequently Asked Questions (FAQ.md)**](docs/FAQ.md) | Over 30 detailed answers covering architecture, Wi-Fi laptops, storage formats, and troubleshooting. |
| [**Comparison Matrix (COMPARISON.md)**](docs/COMPARISON.md) | Technical comparison between WebKVM, Proxmox VE, Cockpit, and virt-manager. |
| [**CLI Reference (CLI.md)**](docs/CLI.md) | Full command documentation for `webkvm-cli`. |
| [**REST & WebSocket API (API.md)**](docs/API.md) | Complete OpenAPI-style reference with JSON request/response payloads. |
| [**Architecture Deep Dive (ARCHITECTURE.md)**](docs/ARCHITECTURE.md) | Internal architecture: CGO libvirt bindings, Incus driver, and embedded Svelte 5 engine. |
| [**Docker Deployment (DOCKER.md)**](docs/DOCKER.md) | Running the same binary as a container: host prerequisites, bind mounts, and the self-update exception. |
| [**Operational Runbooks**](docs/runbooks/RUNBOOKS.md) | Daily operations, upgrade/rollback, disaster recovery, and the security model. |
| [**Security Policy (SECURITY.md)**](SECURITY.md) | Threat model, SSRF protection, RBAC roles, and vulnerability disclosure. |

---

## Building From Source

Prerequisites: Go ≥ 1.26, Node.js ≥ 20, `libvirt-dev`, `gcc`.

```bash
# Clone the repository
git clone https://github.com/Slaker19/webkvm.git
cd webkvm

# Build complete single binary (compiles Svelte frontend and embeds into backend/webkvm)
make build

# Run unit tests
go test ./...
cd frontend && npm test
```

---

## License

WebKVM is open-source software licensed under the [GNU Affero General Public License v3.0 (AGPLv3)](LICENSE).
