# WebKVM CLI (`webkvm-cli`) — Reference Manual

A comprehensive guide to the `webkvm-cli` command-line utility for scripting, automation, and SSH administration.

[**English**](CLI.md) • [**Español**](CLI.es.md)

---

## Overview

`webkvm-cli` is a standalone CLI client that communicates directly with the WebKVM REST API.

### Key Highlights
- **Zero-Token Local Execution**: When executed locally on the host as root, it reads `/opt/webkvm/jwt.key` and signs a temporary admin token on the fly. No manual login or token flags required.
- **Remote Authentication**: Supports `webkvm-cli login` to securely cache session tokens in `~/.config/webkvm/token`.
- **Scripting Friendly**: The global `--json` flag outputs formatted JSON for seamless integration with `jq`, Python, or bash scripts.
- **Clean Column Output**: Tabular text output formatted for standard terminal width.

---

## Global Options

```text
webkvm-cli [OPTIONS] <COMMAND> [ARGS...]

Options:
  --server URL    WebKVM server URL (default: https://127.0.0.1:8080 or $WEBKVM_SERVER)
  --token TOKEN   API token or JWT (or $WEBKVM_TOKEN). Auto-detected on localhost.
  --insecure      Skip TLS verification for self-signed certificates (default: true)
  --secure        Enforce strict TLS certificate verification
  --json          Output raw JSON for scripting
  -h, --help      Display help information
```

---

## Authentication

### Local Execution (On the Host)
If running directly on the host server where WebKVM is installed, you can execute any command without passing credentials:

```bash
sudo webkvm-cli vms list
sudo webkvm-cli storage pools
```

### Remote Execution
To manage a remote WebKVM instance from your workstation:

```bash
# 1. Log in once (credentials are prompted interactively)
webkvm-cli --server https://192.168.1.10:8080 login

# 2. Subsequent commands use the cached token in ~/.config/webkvm/token
webkvm-cli --server https://192.168.1.10:8080 vms list

# 3. Log out and clear the cached session
webkvm-cli logout
```

---

## Server Health & Control

| Command | What it does |
|---------|--------------|
| `webkvm-cli status` | Health check (`GET /api/health`) — prints `status`, `data_dir` and whether libvirt is reachable. |
| `webkvm-cli info` | Host summary (`GET /api/host`): CPU, memory, hypervisor and capabilities. |
| `webkvm-cli restart` | Ask the backend to restart itself (`POST /api/system/restart`). Admin only. |

---

## Backups (`backup`)

| Command | What it does |
|---------|--------------|
| `webkvm-cli backup targets` | List configured backup targets. |
| `webkvm-cli backup run` | Trigger a backup now. |
| `webkvm-cli backup jobs` | List backup jobs with status, start time and size. |
| `webkvm-cli backup schedules` | List scheduled backups. |

---

## Virtual Machines & Containers (`vms`)

### List Instances
```bash
# Formatted table
webkvm-cli vms list

# JSON output
webkvm-cli --json vms list | jq .
```

### Create Instance
Create full KVM virtual machines or lightweight Incus system containers:

```bash
# Create a Debian 12 KVM VM with Cloud-Init
webkvm-cli vms create \
  --name web-server \
  --ram 2048 \
  --vcpus 2 \
  --disk 20 \
  --image debian-12 \
  --user admin \
  --password "SecretPassword123!" \
  --ssh-key "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5..."

# Create a lightweight Alpine Linux Incus Container
webkvm-cli vms create \
  --name alpine-ct \
  --type container \
  --ram 512 \
  --vcpus 1 \
  --disk 10 \
  --image images:alpine/3.21

# Create a custom VM with ISO
webkvm-cli vms create \
  --name custom-bsd \
  --ram 4096 \
  --vcpus 4 \
  --disk 50 \
  --iso FreeBSD-14.0-RELEASE-amd64-disc1.iso \
  --pool webkvm-disks
```

### Lifecycle Actions
```bash
webkvm-cli vms start <id>
webkvm-cli vms stop <id>        # Graceful ACPI shutdown
webkvm-cli vms forceoff <id>    # Instant power cut (destroy)
webkvm-cli vms reboot <id>
webkvm-cli vms suspend <id>
webkvm-cli vms resume <id>
webkvm-cli vms delete <id>
webkvm-cli vms clone <id>
webkvm-cli vms autostart <id> <on|off>
```

### Snapshots
```bash
# List snapshots
webkvm-cli vms snapshots <id>

# Create snapshot
webkvm-cli vms snapshot <id> create "pre-upgrade-v1"

# Revert to snapshot
webkvm-cli vms snapshot <id> revert <snapshot-id>

# Delete snapshot
webkvm-cli vms snapshot <id> delete <snapshot-id>
```

---

## Image Hub (`images`)

Manage official cloud base disks and container images:

```bash
# List all cached and available images
webkvm-cli images list

# Download official cloud base image
webkvm-cli images pull-cloud ubuntu-24.04
webkvm-cli images pull-cloud alpine-3.24
webkvm-cli images pull-cloud debian-12

# Download Incus container image
webkvm-cli images pull-container images:alpine/3.21
webkvm-cli images pull-container images:centos/10-stream

# Delete cached images
webkvm-cli images delete-cloud alpine-3.24
webkvm-cli images delete-container <fingerprint>
```

---

## Storage Management (`storage`)

```bash
# List storage pools (KVM and Incus)
webkvm-cli storage pools

# Create new KVM VDI or ISO pool
webkvm-cli storage pool-create backup-pool --type dir --purpose disk --path /opt/webkvm/pools/backup-pool

# Create new Incus (LXC) container pool
webkvm-cli storage pool-create lxc-nvme --purpose container --type dir --path /var/lib/incus/storage-pools/lxc-nvme
webkvm-cli storage pool-create lxc-zfs --purpose container --type zfs

# Delete storage pool
webkvm-cli storage pool-delete backup-pool

# List disk volumes (KVM or Incus)
webkvm-cli storage volumes
webkvm-cli storage volumes webkvm-disks
webkvm-cli storage volumes default

# List ISO library
webkvm-cli storage isos

# Download ISO directly from URL into pool
webkvm-cli storage download-iso \
  --url "https://releases.ubuntu.com/24.04/ubuntu-24.04-live-server-amd64.iso" \
  --name "ubuntu-24.04-server.iso" \
  --pool webkvm-isos
```

### Host Physical Disks (`host disks`)

```bash
# List physical disks (size, fstype, mountpoints)
webkvm-cli host disks list

# Inspect a pool disk image before attaching it (format, size, has-data)
webkvm-cli host disks probe /opt/webkvm/pools/webkvm-disks/ubuntu-26.04.qcow2

# Deep inspection (OS/partitions, needs libguestfs on the host; polls a job)
webkvm-cli host disks probe /opt/webkvm/pools/webkvm-disks/ubuntu-26.04.qcow2 --deep
```

---

## Cloud-Init Studio & Recipes (`snippets`)

Manage declarative YAML provisioning recipes and snippets:

```bash
# List all Cloud-Init recipes (Official Presets + Custom)
webkvm-cli snippets list

# Display full YAML recipe and metadata
webkvm-cli snippets show preset-k3s
webkvm-cli snippets show preset-docker

# Create custom Cloud-Init snippet
webkvm-cli snippets create \
  --name "PostgreSQL Bootstrap" \
  --category devops \
  --type user-data \
  --desc "Installs PostgreSQL 16 and configures default database" \
  --file ./postgres-init.yaml

# Delete custom snippet
webkvm-cli snippets delete <snippet-id>
```

---

## Network Management (`networks`)

```bash
# List networks and bridges
webkvm-cli networks list

# Show detailed network configuration
webkvm-cli networks show vmbr0

# Inspect active DHCP leases
webkvm-cli networks leases vmbr1

# Start or stop network bridge
webkvm-cli networks start <name>
webkvm-cli networks stop <name>
```

---

## Server Settings (`settings`)

View and modify server settings in real-time:

```bash
# List all settings and their active values
webkvm-cli settings list

# Get specific setting
webkvm-cli settings get terminal.idle_timeout_min

# Update setting (applies hot-reloadable settings immediately)
webkvm-cli settings set terminal.idle_timeout_min 0
webkvm-cli settings set logging.level debug
```

---

## Audit Log (`audit`)

Inspect system security and administrative actions:

```bash
# View last 20 audit entries
webkvm-cli audit list

# View custom limit in JSON for SIEM integration
webkvm-cli --json audit list --limit 100 | jq .
```

---

## Users & API Tokens (`users`, `tokens`)

```bash
# List users
webkvm-cli users list

# Create operator account
webkvm-cli users create operator1 "SecurePass123!" --role operator --email user@example.com

# Delete account
webkvm-cli users delete operator1

# List and create API tokens
webkvm-cli tokens list
webkvm-cli tokens create "ci-cd-pipeline"
```
