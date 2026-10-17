# Changelog

All notable changes to this project are documented in this file,
following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/).

Spanish version: [CHANGELOG.es.md](CHANGELOG.es.md).

## [0.1.8] — 2026-10-17

### Added
- **Native Kernel sysfs Software RAID:**
  - Automated format, systemd persistent mount, and granular multi-folder/single-purpose pool provisioning (`CreateHostRAID`).
  - Native kernel sysfs discovery (`/sys/block/md*/md`) eliminating sub-process overhead.
  - Live array scrubbing/checking (`SyncAction`) and lifecycle control (`StopHostRAID`).
  - Non-destructive mounting of pre-existing RAID arrays without formatting (`mode: "mount"`).
- **Native Network & Pool Discovery:**
  - Unlocked discovery of native virtualization networks (`incusbr0`, `virbr0`, etc.) and default storage pools without artificial name filtering.
- **Helper Scripts Fast Ingestion:**
  - High-speed streaming catalog import via compressed tarball (625 apps in < 1 second).
  - Admin-only role lockdown and direct interactive TUI terminal invocation.

### Changed & Refactored
- **Frontend Architecture:** Decomposed `frontend/src/routes/Storage.svelte` (5,295 lines down to 542 lines) into 14 cohesive components in `src/lib/components/storage/`.
- **Backend Architecture:** Split monolithic `backend/internal/api/storage.go` (2,003 lines) into 6 domain-specific modules.
- **Safety & Resilience:** Universal `safeStorage` wrapper with in-memory fallback, memory leak cleanup in Status lifecycle, and `<firmware>` support in domain policy for UEFI backup restores.

## [0.1.7] — 2026-10-11

### Added

- **Hardware & VFIO Passthrough Manager:**
  - Dedicated Hardware & VFIO management panel in Settings (`HardwareVfioTab.svelte`) with live IOMMU preflight diagnostics (Intel VT-d / AMD-Vi, DMA translation mode, and VFIO kernel module availability).
  - Visual breakdown of all system PCI devices grouped by IOMMU group, with status badges for driver, in-use instances, system bridges, and host-critical resources (boot VGA, primary uplink, storage controllers).
  - One-click driver isolation and assignment: endpoints `POST /api/host/pci-devices/{address}/bind-vfio` to bind devices to `vfio-pci` via kernel `driver_override` and `POST /api/host/pci-devices/{address}/unbind-vfio` to restore original host drivers.
  - Strict security validation against host-critical devices, path traversal, and conflicting running virtual machines.
  - Automated driver rollback: if binding to `vfio-pci` fails, sysfs `driver_override` is cleared and default drivers are reprobed to prevent leaving host devices orphaned.
- **Host Operating System Updates Manager (APT, DNF, Pacman):**
  - Integrated host OS package update manager in Settings (`HostUpdatesTab.svelte`) supporting multi-distribution package managers (APT on Debian/Ubuntu, DNF on RHEL/Rocky Linux/Fedora, Pacman on Arch/CachyOS).
  - Endpoints `GET /api/host/updates` for querying upgradable packages with automatic security advisory detection, category tabs (All, Security, Regular, Held), and pending reboot detection (`/var/run/reboot-required`).
  - Repository index refresh via `POST /api/host/updates/refresh` guarded with preflight binary checks and concurrency mutual exclusion (`isRefreshingHost`).
  - Asynchronous system upgrade endpoint `POST /api/host/updates/upgrade` executing non-interactive system updates with progress parsing, error tail extraction, and integration into the global Task Center.
- **Multi-Distribution Compatibility & Packaging:**
  - Full native runtime and installation support across Ubuntu 24.04 LTS, Rocky Linux 9.8, and CachyOS / Arch Linux.
  - Dynamic distro-aware UEFI/OVMF firmware search paths (`/usr/share/OVMF`, `/usr/share/edk2/ovmf`, `/usr/share/edk2-ovmf/x64`).
  - Standalone installer (`packaging/standalone/install.sh`) enhanced with DNF package compatibility (`iptables-nft`, optional package fallback via `pkg_available`) and co-located binary discovery.
- **Comprehensive Security Audit Hardening:**
  - **Gate A (Host Path Confinement):** Host path confinement via `internal/fsutil.ConfinePath`, restrictive file permissions (0700/0600), isolated media directories, and single-stream exclusive disk uploads.
  - **Gate B (Domain XML Policy):** Strict domain XML schema validator and single `DomainDefineXML` enforcement funnel, loopback-only VNC listeners (`127.0.0.1`), and removal of unauthenticated external VNC ports.
  - **Gate C (Untrusted Image Security):** Deep image safety validation with `imgsafe`, qemu-img probe limits, and OVA tar extraction size and entry count limits.
  - **Auth & Session Security:** Session epoch increment on password change, TOTP token replay protection, WebAuthn SignCount tracking, and hardened Content Security Policy (connect-src 'self', script nonces).
  - **Network & SSRF Defense:** IP-pinned dialer for webhooks and alerts blocking 0.0.0.0/8, 198.18.0.0/15, 240.0.0.0/4, and NAT64 ranges. Safe right-to-left X-Forwarded-For parsing.
  - **Systemd Sandboxing:** Added `ProtectHostname`, `ProtectClock`, `RestrictRealtime`, and strict ReadWritePaths to systemd service units.
- **Defensive Storage Abstraction:**
  - Added unified `safeStorage` wrapper (`$lib/utils/storage.js`) featuring transparent `MemoryStorage` fallback for restrictive browser policies and SSR contexts.

### Changed

- **Backend Modularization (`internal/libvirt`):**
  - Decoupled monolithic `domain.go` by extracting domain subsystem responsibilities into dedicated modules:
    - `domain_pci.go`: PCI address hex formatting, preflight checks, VFIO binding, and sysfs rollback.
    - `domain_usb.go`: Host USB discovery, root hub filtering, and live attach/detach.
    - `domain_shares.go`: 9p filesystem shared folders management.
- **Frontend Bundle Optimization (Vite / Rollup):**
  - Configured intelligent granular manual chunking in `vite.config.js`.
  - Entry bundle `index.js` reduced by **70%** (from 682 KB to 200 KB / 61 KB gzipped).
  - Heavy modules isolated into on-demand cached chunks (`app-i18n`, `vendor-ui`, `vendor-lucide`, `vendor-xterm`).
  - Resolved all Rollup chunk size threshold warnings.

### Fixed

- **Async Task Tracking in Host Updates:**
  - Replaced floating unhandled promise loops in `HostUpdatesTab.svelte` with synchronized `api.waitJob` execution and 35-minute operational timeouts.
- **Cloud-Init Password Generation:**
  - Fixed empty password issuance in `cloudinit.go` by ensuring cryptographic entropy with fallback PRNG and SHA-512 crypt hashing.
- **Hardware Tab Runtime Safety:**
  - Guarded preflight state access with optional chaining (`preflight?.`) to eliminate `TypeError` crashes during initialization.
- **Serial Console WebSocket Teardown:**
  - Replaced fixed sleep delays with `r.Context().Done()` channel selection in `console_serial.go` to instantly terminate backend routines upon client disconnection.

## [0.1.6] — 2026-10-04

### Added

- **Unified Background Task & Real-Time Progress System:**
  - Full integration of all asynchronous and long-running operations with the global Task Center and Task Drawer (`TaskCenter.svelte` / `TaskDrawer.svelte`).
  - Added dedicated global jobs listing endpoint `GET /api/jobs` supporting role-based access control, owner isolation, and continuous polling.
  - Automatic progress reporting across VM cloning, batch cloning, snapshots, disk/storage migrations, VM/container provisioning, appliance deployment, host disk formatting/wiping, RAID array creation, and ZFS pool lifecycle actions.
- **Host Network Bonding & Atomic Netplan Persistence:**
  - Automated Layer 2 network bond creation (`balance-rr`, `active-backup`, `802.3ad`, etc.) with atomic Netplan file generation (`/etc/netplan/60-webkvm-bonds.yaml`), syntax verification (`netplan generate`), and safe application (`netplan apply`).
  - Dedicated endpoint `DELETE /api/host/bonds/{name}` to gracefully tear down bonds and release slave interfaces.
  - Resilient uplink protection allowing secondary slave interface isolation while safeguarding primary active uplinks against accidental disconnections.
- **Native Ntfy & Gotify Alert Notification Channels:**
  - Native integration with Ntfy (`ntfy.sh` or self-hosted) with customizable server URLs, topic routing, optional Bearer authorization tokens, and priority/emoji tag mapping based on alert severity (`info`, `warning`, `critical`).
  - Native integration with Gotify self-hosted push notification server with application token authentication and numeric priority levels.
  - Interactive UI configuration cards in Settings > Notifications with live status indicator badges and masked credential storage.
  - Zero-leak HTTP transport with automatic connection teardown (`DisableKeepAlives: true`) ensuring no persistent idle loops remain after sending alerts.
- **Advanced Perimeter Defense & Security Jail (SSH, Whitelists & Custom Jails):**
  - Host SSH brute-force protection jail: live monitoring of systemd journal / auth logs to isolate SSH attackers directly at kernel level (`nftables`).
  - Whitelist CIDRs/IPs management: configure subnets and IP addresses exempt from bans with automatic unbanning upon addition and persistence in `jail.json`.
  - Manual IP banning: easily ban any malicious IP with custom duration, reason, and target origin.
  - Custom Jails: create and manage customized jail profiles with distinct failure thresholds, detection windows, and ban durations.
  - Multi-tab management interface in Settings > Security Jail with real-time statistics and i18n support (ES, EN, CA).
- **Official Grafana Dashboard (`webkvm-overview.json`):**
  - Production-ready dark overview dashboard pre-wired with host telemetry, instance metrics, and storage pools.
  - Real-time stat panels, host CPU/RAM/Disk gauges, and historical time series for compute instances (vCPU usage %, memory allocation, disk I/O read/write throughput, network transfer rates).
  - Storage pool allocation vs capacity breakdown with gradient thresholds.
  - Dedicated endpoint `GET /api/metrics/grafana-dashboard` and one-click download button in Settings > Metrics.
- **Prometheus / Alertmanager Alerting Rules (`webkvm-alerts.yml`):**
  - Standard Alertmanager alerting rule definitions covering daemon reachability (`WebKVMDown`), host CPU/RAM/disk saturation, storage pool thresholds (>85%), and sustained VM CPU spikes.
  - Dedicated endpoint `GET /api/metrics/alert-rules` and one-click download/copy buttons in Settings > Metrics.
- **Full Internationalization (i18n):**
  - Complete multilingual translations across English, Spanish (`es`), and Catalan (`ca`) for Ntfy, Gotify, Grafana dashboard, and Alertmanager rules.

## [0.1.5] — 2026-10-03

### Added

- **Native Prometheus / OpenMetrics Exporter (`/metrics` & `/api/metrics/prometheus`):**
  - Standard Prometheus text format scraping endpoints exposing complete operational telemetry.
  - Process and build metadata (`webkvm_info`, `webkvm_up`).
  - System host metrics (CPU usage %, RAM total/used/free bytes, disk capacity and utilization, network rx/tx bandwidth, and system uptime).
  - VM state aggregates and per-instance live telemetry (`webkvm_vms_total`, `webkvm_vms_state`, `webkvm_vm_status`, `webkvm_vm_vcpus`, `webkvm_vm_cpu_usage_percent`, `webkvm_vm_memory_allocated_bytes`, `webkvm_vm_memory_used_bytes`, `webkvm_vm_disk_size_bytes`, `webkvm_vm_uptime_seconds`, and live disk I/O and network transfer rates).
  - Storage pool metrics (capacity, allocation, availability, and active status per pool).
- **Flexible Exporter Authentication & Security:**
  - Support for standard `Authorization: Bearer <token>` and `?token=` query parameters (accepting API tokens and session credentials).
  - Hot-reloadable setting `metrics.allow_unauthenticated` enabling seamless scraping on trusted private LANs.
  - Hot-reloadable setting `metrics.prometheus_enabled` allowing operators to toggle the scraping endpoint on or off.
- **Prometheus Scraper UI & Integration Guide:**
  - Dedicated Prometheus integration card in Settings under the new Metrics section.
  - One-click copy for the full `/metrics` endpoint URL and complete ready-to-use `prometheus.yml` scrape configuration job snippet.
  - Complete internationalization across English, Spanish, and Catalan.

## [0.1.4] — 2026-10-03

### Added

- **Multi-Stage Container Build & Pre-Flight Diagnostics:**
  - Multi-stage `Dockerfile` with isolated Svelte frontend compilation (Node 22 slim) and CGO/libvirt backend build (Ubuntu 24.04 with Go 1.26), ensuring full binary and glibc compatibility regardless of host environment.
  - Layer caching optimization for fast rebuilds (~15 seconds).
  - Pre-flight diagnostic checks in `docker-entrypoint.sh` verifying libvirt socket connectivity, hardware virtualization acceleration (`/dev/kvm`), and ZFS kernel module availability (`/dev/zfs`).
  - Container health check integration (`HEALTHCHECK`) and host parity configuration (`pid: host`, systemd units, `/dev:rslave`).
- **Live Memory Ballooning:**
  - Dynamic memory adjustment on running virtual machines (`SetMemoryFlags` with `DOMAIN_MEM_LIVE`) without requiring VM shutdown or reboot.
  - User interface indicator in VM settings alerting when live memory ballooning is active on running instances.
- **Proactive Storage Degradation Alerts:**
  - Expanded `AlertEngine` monitoring physical drive S.M.A.R.T. health, uncorrectable pending sectors, and NVMe critical hardware warnings (`CriticalWarning`).
  - Automatic critical alerts for degraded, faulted, or unavailable ZFS storage pools (`zpool`).
  - Automatic detection of degraded software RAID arrays (`mdadm`) via `/proc/mdstat`.
  - Refined ZFS pool status indicators with color-coded severity (`ONLINE`, `DEGRADED`, `FAULTED`/`UNAVAIL`).
- **Comprehensive Internationalization (i18n):**
  - Full multilingual support (English, Español, Català) for newly added components: S.M.A.R.T. Telemetry modal (`SmartModal`), NetGuard Security Jail tab (`SecurityJailTab`), and Batch VM Cloning modal (`BatchCloneModal`).

### Fixed

- **ZFS ZVol Device Resolution in Containers:**
  - Added adaptive retry and synchronization loop with `udevadm settle` in `zvol.Resolve()` to reliably locate `/dev/zvol` and block device nodes created asynchronously in containerized environments.
- **VM Boot Device Compatibility:**
  - Accepted `"hd"` and `"disk"` as valid boot device identifiers, mapping seamlessly to the libvirt XML domain schema.
- **Static Analysis & Slice Allocation (CodeQL):**
  - Eliminated high-severity taint tracking alert in batch clone handler by dynamically sizing result slice with `var cloned []models.VM`.

## [0.1.3] — 2026-10-01

### Added

- **SPICE Console & Streaming Proxy:**
  - Full Web SPICE console client (`spice.mjs`) with dynamic auto-scaling viewport fit, aspect ratio preservation, and vdagent monitor resizing (`VD_AGENT_MONITORS_CONFIG`).
  - Scancode Set 1 hardware keyboard mapping for physical and virtual keyboards (ES ISO and US ANSI layouts) with `AltGr` synthetic Ctrl suppression and extended key coverage (`º`, `\`, `ç`, `ñ`, `< >`, accents).
  - Sub-pixel mouse coordinate scaling via `getBoundingClientRect()`.
  - High-performance streaming WebSocket proxy for SPICE graphics with ping/pong keepalives and atomic goroutine termination.
- **S.M.A.R.T. Health Telemetry:**
  - Concurrent physical disk inspection and S.M.A.R.T. metrics retrieval (`smartctl`), displaying disk health, temperature, power-on hours, bad sectors, and error logs in a dedicated dashboard modal.
- **NetGuard Jail & Brute-force Protection:**
  - Hardened IP blacklist isolation and rate-limiting jail preventing unauthorized access, with real-time UI inspection (`SecurityJailTab.svelte`).
- **Batch VM Cloning:**
  - Batch clone modal and API enabling parallel duplication of virtual machines with custom naming prefixes and sequential numbering.

### Fixed

- **Cloud-Init VM Deployment on Storage Pools & Graphics Compatibility:**
  - Resolved 400 Bad Request error (`purpose "disk", expected "container"`) and silent failure when deploying Cloud-Init VMs on libvirt pools such as `webkvm-disks` by explicitly passing `type: "vm"` in the wizard payload.
  - Added support for `"completed"` terminal job status in client-side job polling (`waitJob`), preventing infinite loading spinners upon instance provisioning.
  - Safe automatic degradation to VNC graphics (`resolveGraphicsType`) on hosts whose QEMU binary (such as QEMU 10+) lacks SPICE server support.
  - Fixed autogenerated deployment password length in App Store to strictly conform with Cloud-Init requirements (6 to 12 characters).
- **Language Selector in Compact & Hover Sidebar:**
  - Fixed dropdown menu flickering, jumping, and unexpected auto-closing when toggled in compact (rail) and auto-expand (hover) sidebar modes.
  - Sidebar maintains its expanded state while floating menus are open and prevents focus-loss collapses.
  - In compact rail mode, the language menu opens to the right (`side="right"`, `align="end"`) avoiding off-screen left clipping.
  - Removed conflicting anchor-width constraints from DropdownMenu primitives in Tailwind v4.

## [0.1.2-fix2] — 2026-09-30

### Fixed

- **Storage Pool Deduplication & Capacity Calculation:**
  - Added missing `DeviceID` (`st_dev`) resolution to Incus storage pools in `IncusBackend.ListStoragePools()` and `CreateStoragePool()`.
  - Prevented multi-accounting and duplicate summing of disk capacity and used allocation across shared host filesystems between KVM and Incus storage pools.

## [0.1.2-fix1] — 2026-09-30

### Security

- **Path Injection Hardening:**
  - Strengthened path traversal defenses in `zvol.Resolve` and `mdraid.ensureDeviceNode` using `filepath.Clean`, explicit prefix boundaries, and localized regex checks.
- **Dependency Vulnerability Fixes:**
  - Resolved Dependabot alerts (CVE-2026-102277 / GHSA-q2hr-2g5m-vwhr) by locking `brace-expansion` dependencies to patched versions (`1.1.21` and `5.0.12`) via npm overrides.

## [0.1.2] — 2026-09-30

### Added

- **ZFS Storage Pools & ZVols:**
  - Management of ZFS storage pools with support for stripe, mirror, raidz1, and raidz2 topologies.
  - Creation and lifecycle of raw block devices (ZVols) with thin provisioning (sparse) support for direct native VM attachment.
  - Dedicated REST API endpoints: `GET /api/host/zpools`, `POST /api/host/zpools`, `GET /api/host/zvols`, `POST /api/host/zvols`, and `DELETE /api/host/zvols/{name}`.
  - Dedicated ZFS management tab in the WebKVM Storage dashboard with pool and volume creation wizards.
- **Linux Software RAID (mdadm):**
  - Creation and management of host software RAID arrays supporting RAID levels 0, 1, 5, 6, and 10.
  - Automatic persistence to `/etc/mdadm/mdadm.conf` and safe disk/mount validation checks.
  - Dedicated REST API endpoint: `POST /api/host/raid`.
  - Interactive RAID array creation wizard in the Storage view.
- **Host Disk Filtering:**
  - Automatic exclusion of squashfs, iso9660, and snap loop devices from host physical disk listings.
  - Recognition and support for `/dev/md*` software RAID block devices.
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

[0.1.4]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.4
[0.1.3]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.3
[0.1.2-fix2]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2-fix2
[0.1.2-fix1]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2-fix1
[0.1.2]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2
[0.1.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.1
[0.1.0]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.0
[0.0.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.0.1
