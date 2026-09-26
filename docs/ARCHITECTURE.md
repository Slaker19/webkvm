# WebKVM — Internal Architecture Deep Dive

A technical guide to the engineering principles, backend concurrency patterns, and frontend design behind WebKVM.

[**English**](ARCHITECTURE.md) • [**Español**](ARCHITECTURE.es.md)

---

## System Architecture Overview

```text
┌────────────────────────────────────────────────────────────────────────┐
│                   Web Browser / Client Applications                    │
│        Svelte 5 SPA • @xterm/xterm WebGL • noVNC RFB • SSE Client      │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │ HTTP/2 / TLS / WebSockets / SSE
┌───────────────────────────────────▼────────────────────────────────────┐
│                  WebKVM Backend (Single Go Binary)                     │
│                                                                        │
│   ┌─────────────────────┐ ┌───────────────────┐ ┌──────────────────┐   │
│   │   Chi HTTP Router   │ │  JWT & Token Auth │ │ Global Rate Lim. │   │
│   └──────────┬──────────┘ └─────────┬─────────┘ └────────┬─────────┘   │
│              │                      │                    │             │
│   ┌──────────▼──────────────────────▼────────────────────▼─────────┐   │
│   │               API Handlers & Service Layer                     │   │
│   │   VMs • Storage • Networks • Image Hub • Firewall • Settings   │   │
│   └───────────────────────┬──────────────────────┬─────────────────┘   │
│                           │                      │                     │
│   ┌───────────────────────▼────────┐  ┌──────────▼─────────────────┐   │
│   │       KVM / libvirt Driver     │  │    Incus / LXD Driver      │   │
│   │     (CGO + Unix RPC Socket)    │  │    (Native Go REST Client) │   │
│   └────────────────────────────────┘  └────────────────────────────┘   │
│                                                                        │
│   ┌────────────────────────────────────────────────────────────────┐   │
│   │              Embedded Svelte 5 Static Assets                   │   │
│   │                  //go:embed all:dist (SPA)                     │   │
│   └────────────────────────────────────────────────────────────────┘   │
└────────────────────────────────────────────────────────────────────────┘
```

---

## Core Subsystems

### 1. The Single Binary Paradigm (`go:embed`)
WebKVM eliminates the operational complexity of multi-tiered web apps:
- During compilation (`make build`), Vite builds the Svelte 5 single-page application into `frontend/dist/`.
- The Go compiler embeds all compiled assets, styles, fonts, and scripts directly into the executable using Go 1.16+ `embed.FS`.
- At runtime, `internal/frontend/embed.go` serves the single-page application directly from memory with optimal cache headers (`Cache-Control: public, max-age=31536000`).

### 2. Hybrid Compute Layer (`internal/compute`)
WebKVM defines a clean, abstracted `Instance` interface in `internal/compute/backend.go`:
- **`KVMBackend` (`internal/compute/kvm.go`)**: Interfaces with the Linux Kernel-based Virtual Machine via `libvirt.org/go/libvirt`. Handles hardware virtualization, QEMU domain XML generation, PCI device attachment, and VirtIO drivers.
- **`IncusBackend` (`internal/compute/incus/incus.go`)**: Directly communicates with the Incus/LXD daemon socket (`/var/lib/incus/unix.socket` or `/var/snap/lxd/common/lxd/unix.socket`) using the official Incus Go client SDK.
- **`CombinedBackend` (`internal/compute/combined.go`)**: Seamlessly routes incoming API calls to either KVM or Incus based on the instance identifier, presenting a single, unified compute interface to the user.

### 3. Concurrency & Goroutine Safety (`internal/safego`)
To guarantee high availability and crash resilience:
- All background goroutines (job sweepers, download streams, WebSocket proxies, libvirt event loops) are wrapped with `safego.Recover(name)`.
- If an unexpected runtime panic occurs in an isolated worker, the panic is safely caught, structured error telemetry is logged to journald, and the main server process remains uninterrupted.

### 4. Memory & Performance Profile
- **Zero Allocations on Idle**: When no active browser connections or background jobs are running, WebKVM's memory consumption stabilizes at **~12 MB to 18 MB of RSS memory**.
- **Garbage Collection Optimization**: WebSocket proxies stream data using fixed-size reused byte buffers (64 KB) to minimize Go runtime GC pressure during heavy I/O transfers.

### 5. Disk Content Inspection (`internal/diskprobe` + `hostcaps` GuestFS)
Attaching an existing disk image to a VM used to be blind: nothing checked whether the image was empty, a previously installed guest OS, or another VM's data. `internal/diskprobe` answers one question before attach/format — *does this disk already contain data?* — at two levels:
- **Basic (always available)**: `qemu-img info` reports the real image format plus allocated bytes. A freshly created qcow2 allocates ~0; anything beyond a 1 MiB slack (or any backing file) sets `HasData`. Fast, dependency-free, read-only, 15 s timeout.
- **Deep (only when libguestfs is installed)**: `virt-inspector --no-applications` boots a minimal appliance to read partitions/filesystems and identify the installed OS (120 s timeout, served as a background job via `submitJob` because it is slow).
`hostcaps` probes for `virt-inspector` + `guestfish` (`LookPath`) and exposes `GuestFS`/`GuestFSBin` through `/api/host/capabilities`; absence is normal and only disables the deep path. `POST /api/vms/{id}/disks` refuses a non-empty image with `409` unless `force: true`, and refuses double-attach (one image, one VM) the same way — both enforced server-side, with the Add-Disk dialog rendering the warning client-side first.

---

## Frontend Architecture (Svelte 5 with Runes)

WebKVM was engineered specifically for **Svelte 5**:
- **Runes Reactivity**: State management utilizes Svelte 5 primitives (`$state`, `$derived`, `$effect`) for deterministic, granular updates without virtual DOM overhead.
- **Dynamic Module Splitting**: Every major route (`VmDetail`, `Storage`, `ImageHub`, `HostConsole`, `Settings`) is split into independent lazy-loaded chunks via dynamic `import()`.
- **GPU Terminal Acceleration**: The terminal component integrates `@xterm/xterm` with `@xterm/addon-webgl` and `@xterm/addon-unicode11`, streaming 60 FPS terminal output directly through the GPU.
- **Design system**: semantic color/motion tokens in `app.css` (3 themes, 6 accents), shadcn-based primitives plus WebKVM-owned components (`StatusBadge`, `Tip`, `EmptyState` variants, `TableSkeleton`/`CardGridSkeleton`/`VmDetailSkeleton`, `Sheet` drawer). Modals live in `Dialog`/`Sheet`; `window.confirm` is not used.
- **Component split**: self-contained dialogs/forms are extracted from the large routes with explicit props and parent-owned data (`DeleteVmDialog`, `ManageGroupsDialog`, `CloudInitPreviewDialog`, `AddScheduleDialog`, `CreateVolumeInlineForm`). Rule of thumb: a block is extracted only when its state is genuinely self-contained — forms whose dozen `bind:` fields are cross-validated against the rest of the page stay in the route.
- **i18n**: every user-visible string goes through `t()` (`en`/`es`/`ca` in `i18n.svelte.js`); `t()` returns the raw key for a missing entry, so `t(k) || 'fallback'` never fires — do not use that pattern.

### Proxmox LXC Compatibility (vzdump)
The `internal/vzdump` package handles bidirectional conversion between Proxmox vzdump LXC and Incus backup formats:

- **Import** (`POST /api/vms/import`): detects `vzdump-lxc-*.tar.zst` / `.tar.gz` by looking for `./etc/vzdump/pct.conf` or root-level filesystem entries. Converts to Incus-native format under `backup/container/` (`backup.yaml` with container config + volume struct, `index.yaml` with name/backend/pool/type, `rootfs/`). Handles both Proxmox layout (files at root) and Incus layout (`rootfs/` prefix), hard links (with correct `backup/container/rootfs/` prefix for `tar --strip-components=2`), and symlinks.
- **Export** (`GET /api/vms/{id}/export?format=proxmox`): reads Incus backup (strips `backup/container/` prefix), expands `rootfs.squashfs` via `unsquashfs` if present, generates `pct.conf` from `backup.yaml` metadata, and produces `vzdump-lxc-*.tar.zst` with `./etc/vzdump/pct.conf` + `./rootfs/`.
- Requires `squashfs-tools` (`unsquashfs`, `mksquashfs`) on the host (`.49`).
- **7 unit tests** in `vzdump_test.go` cover gzip/zstd detection, import, export, and filename generation.
