# Changelog

All notable changes to this project are documented in this file,
following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and [Semantic Versioning](https://semver.org/).

Spanish version: [CHANGELOG.es.md](CHANGELOG.es.md).

## [Unreleased] — v0.1.14 candidate

Stability release: the v0.1.13 features hardened for production — async
evacuations with persisted state, a durable task history with per-task logs,
cross-node schedule collision fixes, and a validated installer for the apt,
dnf and pacman families.

### Added

- **Async node evacuation**: evacuation runs as a background job (202 + job ID)
  with live per-VM progress in the Task Center, a Web Push notification on
  completion, cancel (no new migrations are started; the in-flight one is
  never killed) and resume (re-running evacuates the remaining VMs). The
  evacuation record persists to `<dataDir>/evacuations.json`, survives a
  server restart and closes as `interrupted` instead of deadlocking the
  single-flight guard.
- **Task history and logs**: terminal jobs are persisted to
  `<dataDir>/jobs-history.json` (capped, deduped by job ID, 7-day TTL) and
  survive a server restart. Every job accumulates a bounded progress log
  served as text by `GET /api/jobs/{id}/log`; `GET /api/jobs/history` and
  `GET /api/jobs/stats` (running / done 24h / failed 24h / evacuations,
  owner-filtered by RBAC) power a "Recently finished" section with
  per-task log download in the Task Drawer (en/es/ca).
- **HA validation gate**: `docs/RELEASING.md` §8 now requires a green
  live-validation slice (GUIDED-VALIDATION against the 3-node lab, results
  recorded with the environment cited) for any release touching HA code
  paths; runbook evidence labels were reconciled with the v0.1.12 lab
  results in both languages.

### Fixed

- **Cross-node schedule collision**: VM power/snapshot schedules are
  node-scoped (`nodeID/vm` composite keys with legacy-key migration) and the
  cron scheduler dispatches each action to the schedule's owning node —
  previously two nodes hosting a same-named VM shared one schedule, and one
  node's cron could power the other node's machine. Cover uploads through a
  remote dispatch are node-scoped for the same reason.
- **Installer `--https=no`**: a re-install over a preserved config kept the
  previous `server.tls_cert`/`tls_key`, so the backend served HTTPS while the
  health check probed HTTP and rolled back a good deployment. An explicit
  plain-HTTP install now clears persisted TLS keys, the health check derives
  its protocol from the persisted config, and a `--port` ignored by a
  preserved config is warned about instead of failing silently.
- **Stale version fallback**: an unflagged build no longer reports `0.1.12`;
  the source default is `dev` and release builds stamp the real version.
- **Empty job logs**: `submitJob`/`submitJobWithPool` success paths now write
  a terminal `completed` line so the log endpoint is not empty on success.

## [0.1.13] — 2026-11-16

Feature release: maintenance-safe node drain, browser push, and optional OIDC SSO.

### Added

- **Node drain and evacuation**: admins can mark a libvirt node as draining, block new placements, inspect a per-VM evacuation plan, and migrate migratable instances to a chosen node. Pacemaker-managed, Incus, GPU-passthrough and stale entries are skipped with a reason.
- **Browser push notifications**: VAPID-backed Web Push subscriptions per user, a service worker, a test notification and terminal job alerts. Opt-in from Account; no subscription means no notifications.
- **OIDC/SSO login**: environment-configured authorization-code flow with PKCE, signed state/nonce, ID-token signature/issuer/audience validation, role mapping, optional auto-provisioning and optional local-login lockdown with an `admin` break-glass account. Existing local accounts are never linked automatically.
- **i18n regression gate**: `scripts/check-hardcoded-es.mjs` plus CI; Spanish UI literals and backend fallbacks were cleaned up across the frontend and API.

### Fixed

- **CI flakes**: the federation parallelism test now synchronizes on both calls instead of a 700ms deadline; the cooldown flood test uses a 30s window so a slow runner cannot expire it mid-test. CI annotates failing tests and the log tail when no marker matches.
- **Remaining Spanish UI strings**: VM audio labels, deployment tasks, terminal shortcuts, RAID/disk initialization, shared-folder mounts, batch clone, app-store script comments and other literals now use `t()` in en/es/ca.
- **Backend fallbacks**: auth, quota, media-protection, host-update progress and VNC helper strings are English now; the SPA localizes by stable code.
- **CodeQL hygiene**: the i18n scanner uses line-based comment/code masking instead of regex HTML filtering, clearing the four tooling alerts; the command-runner and Incus trust-on-first-use findings are documented and dismissed as intentional.

### Changed

- **Default bind address**: fresh installs listen on `0.0.0.0` so the UI is reachable on the LAN without editing `config.json`. When TLS is off, the backend falls back to `127.0.0.1` unless `WEBKVM_ALLOW_INSECURE_HTTP=1` is set; behind a TLS reverse proxy, set `server.bind_addr` to `127.0.0.1`.
- **AUR**: `webkvm-bin` is documented as an unofficial third-party package. The in-repo `packaging/arch/PKGBUILD` is a local-build reference; WebKVM does not publish or endorse the AUR package.
- **Dependencies**: `github.com/SherClockHolmes/webpush-go v1.4.0`; `github.com/zitadel/oidc/v3` becomes a direct dependency.

## [0.1.12-fix02] — 2026-11-16

Packaging and maintenance follow-up. The application behaviour is unchanged.

### Fixed

- **`.deb` checksum verification**: the package is uploaded as `webkvm_0.1.12.fix02_amd64.deb` and `SHA256SUMS` matches the asset name. GitHub rewrites `~` to `.` on upload, which broke `sha256sum -c` for v0.1.12-fix01.
- **Native package ordering**: Debian builds `0.1.12.fix02` and RPM builds `0.1.12-1.fix02`, so they upgrade over `0.1.12`; the previous `~fix01` / `0.fix01` versions sorted below it.
- **i18n leftovers from issue #11**: VM audio-model labels and the app deployment task text use `t()`; wrong-password detection uses the stable `invalid_password` code instead of a Spanish string. The wider sweep remains tracked in issue #11.
- **Go dependencies**: `go.opentelemetry.io/otel*` updated to `v1.47.0` (Dependabot PR #13); no security advisory involved.

### Added

- **Release notes** for this version published from `docs/releases/0.1.12-fix02.md`.

## [0.1.12-fix01] — 2026-11-16

Security and packaging follow-up. Application behaviour is unchanged.

### Fixed

- **Go 1.26.11**: rebuilds with the fix for `GO-2026-6859` (HTTP/1 server crash via a `MaxBytesReader` header race). CI now pins 1.26.11; the v0.1.12 release binary was already built with it.
- **`golang.org/x/crypto`**: updated to `v0.58.1-0.20261009181718-2f1f834330b2`, clearing `GO-2026-5932` (unmaintained openpgp). The affected package is not called by WebKVM.
- **Updater**: `isNewer` understands the `-fixNN` suffix, so `0.1.12-fix01` is offered over `0.1.12`. Older 0.1.12 binaries need one manual `sudo webkvm-update`.
- **CI/CD**: release jobs pin Go 1.26.11; Docker builder downloads Go 1.26.11.

### Added

- **Code scanning**: GitHub default setup covers Go and JavaScript/TypeScript; an advanced CodeQL workflow was removed because it conflicts with default setup.
- **Release notes** published from `docs/releases/<version>.md`.
- **Tests** for the `-fixNN` updater comparison.

## [0.1.12] — 2026-11-15

### Added

- **Selective host package upgrades**: `POST /api/host/updates/upgrade` accepts an optional `{packages:[...]}` body; the UI gains per-package checkboxes and an "Upgrade selected (N)" action; the CLI gains `updates upgrade [pkg...]`. Package names are validated against a safe shape to block flag injection.
- **Host update notifications**: upgrade success/failure now flows through the notification channels (Telegram, Discord, Slack, webhook, etc.) via `notifyUpgrade`.
- **Scheduled host updates**: new `hostsched` package persists a cron-based schedule (`enabled`, `upgrade_cron`, `reboot_after`) to `host-schedule.json`, exposed via `GET/PUT /api/host/updates/schedule` and a "Update Schedule" card in the host-updates tab.
- **1-click host reboot**: `POST /api/host/reboot` (admin) behind the "reboot required" banner, plus `settings reboot` in the CLI.
- **Multi-node cluster (F3)**: the dashboard now operates across nodes, not just probes them. `ListVMs` federates domains from every enabled remote node (each tagged with its `node`), `POST /api/vms` accepts a target `node` to deploy a VM onto a remote hypervisor (admin, KVM-only), and lifecycle operations route to their owning node. The Nodes form tests libvirt and reports the domain count and KVM availability before saving. Easy mode builds a `qemu+ssh` URI; Advanced accepts a raw URI. The form offers both a downloadable dependency script and a confirmed SSH installer for apt/dnf/pacman nodes. Arch full-system upgrades require explicit opt-in. `POST /api/vms/{id}/migrate` supports live migration with optional disk copying, or offline definition migration when `shared_storage=true` (the disks must already be available at identical paths). The API rejects disabled nodes and returns 501 for remote VM operations that are not routed yet. Read-only VM detail (domain, disks, interfaces, snapshots, logs) routes to its remote node, and the Nodes page shows CPU and RAM capacity for placement.

### Fixed

- **Host upgrade "Read-only file system"**: the assisted host upgrade ran the package manager (`apt-get`/`dnf`/`pacman`) inside the WebKVM service's hardened mount namespace, where `ProtectSystem=full` mounts `/etc` read-only, so `dpkg`/`rpm` failed writing config files. The package manager now runs via `systemd-run` (a transient unit forked from PID 1's namespace), escaping the hardening; Docker and non-systemd hosts fall back to a direct child, which is correct there. The repository refresh path got the same treatment.
- **Machine-readable error codes for host upgrades**: the upgrade job now surfaces a stable `error_code` (`host_upgrade_readonly_filesystem`, `host_upgrade_timeout`, `host_upgrade_failed`) instead of leaving clients to parse prose, and `waitJob` propagates it to the thrown error.
- **External snapshot deletion with the VM off or after a revert**: libvirt's AppArmor profile marks the snapshot's backing file read-only (`deny ... w`), so the offline block-commit failed with `EACCES` and no audit record. The disk chain is now owned by the QEMU user, and new WebKVM domains carry a configurable AppArmor label; `WEBKVM_DISABLE_APPARMOR=0` keeps confinement. Deleting snapshots now reclaims the overlay and the VM boots afterwards on both Debian and Ubuntu. Existing domains are migrated on the next start, revert, or snapshot deletion, so an in-place update from the UI needs no manual XML edit.
- **Missing resources now return 404 instead of 500**: VM, storage pool, volume, ISO, snapshot, PCI/USB, shared-folder and template handlers map libvirt's not-found errors centrally (74 routes covered by the live API sweep).
- **Network interface PATCH accepts URL-encoded MAC addresses** (`52%3A54%3A...`), matching the DELETE handler.
- **Deleting a paused or suspended VM** now destroys the QEMU process before undefining it, instead of leaving a transient domain resident.
- **UI localisation**: removed hardcoded Spanish strings in AppStore, ImageHub, MultiView, VmList, CronPicker and the VM disk card; `en`, `es` and `ca` are symmetric again.

### Security

- **AppArmor workaround for external snapshots**: `<seclabel type='none' model='apparmor'/>` is added to WebKVM-defined domains on AppArmor hosts by default. QEMU remains unprivileged (`libvirt-qemu`) with DAC, seccomp and cgroups; SELinux is never disabled. See `SECURITY.md` for the rationale and `WEBKVM_DISABLE_APPARMOR=0`.

## [0.1.11] — 2026-11-06

### Added

- **3D acceleration (virgl) for virtio video**, reported by [@sfs-pra](https://github.com/sfs-pra) in [#9](https://github.com/Slaker19/webkvm/issues/9): new `accel_3d` option emits `<acceleration accel3d='yes'/>` plus a validated `<graphics type='egl-headless'>` renderer, so Wayland compositors (niri, sway) start inside a guest. Fixes the underlying bug where saving the console type wiped every `<graphics>` block, silently disabling 3D. Gated on host capabilities (render node + `egl-headless`), shown disabled with the reason otherwise.
- **Updater shows tag + commit**: `#/status` now surfaces the latest release tag alongside the newest commit on `main` (`latest_commit`/`build_commit`), and source-mode update compares the built commit against `main` instead of always assuming an update exists.
- **Backup → notifications**: backup success/failure now flows through the notification channels (Telegram, Discord, Slack, webhook, etc.) via `Runner.SetAlertFunc`.
- **Docker-based 1-click apps deploy as VMs**: Pi-hole, AdGuard Home, Vaultwarden, Uptime Kuma, Portainer, Beszel, Home Assistant, Jellyfin, and others now default to a KVM VM (their provisioning scripts require docker/systemd, unavailable in unprivileged LXC).
- **Playwright E2E smoke suite** in `e2e/`: login + 5 routes asserted free of console errors, plus status/version checks.

### Fixed

- **Storage/ISO fixes contributed by [@kuanghl](https://github.com/kuanghl) in [#10](https://github.com/Slaker19/webkvm/pull/10)**: ISO upload limit raised from 10 GiB to 50 GiB; pools created from the UI now autostart (they went inactive after a libvirtd restart, breaking every upload); `GetISOs` refreshes the pool before listing; re-uploading a ghost file no longer fails with a permanent 409 (registered volumes and referenced files still refuse); pools without a purpose tag infer it from the name; the ISO tab uses a single pool dropdown.
- **openSUSE Leap 16.1 image URL** pointed at the unversioned "latest" build (the pinned `Build2.16` had been removed upstream and returned 404).
- **`exec` → `CommandRunner`** in the appliance deploy path (`qemu-img` resize/info), completing the testability migration already applied to `mdraid`/`zvol`.

## [0.1.10] — 2026-11-01

### Added

- **Dual View Mode in VmList**: Added toggle between Cards Grid and Dense Enterprise Table view, stored in user preferences (`localStorage`).
- **1-Click IP Copy**: Instant clipboard copy with visual feedback and toast on both cards and table without navigating to instance details.
- **Clustering Readiness**: First-class Node badge (`vm.node`) on cards and table, plus cluster node filtering chips.
- **Unified Filter Chips**: Always-visible quick filter bar with live counts for instance types (All, VMs, Containers) and states (Running, Shutoff, Paused).
- **Flexible Layer 2 & Layer 3 Network Management**:
  - Pure Layer 2 virtual switch creation without required host IP or dnsmasq (`no CIDR`), ideal for virtual routers/firewalls (pfSense, OPNsense, VyOS) and isolated zero-trust workloads.
  - Universal 802.1Q VLAN-aware trunking (`vlan_filtering 1`) supported across all Linux bridges and compatible with both QEMU/KVM guests and Incus/LXC containers.
  - First-class creation mode selector in `Networks`: Layer 2 Pure Switch, Layer 3 Host-Only, Layer 3 NAT, and Physical Direct Bridge with smart pre-population.
  - Interactive Visual Network & Port Topology Map in `Networks.svelte` displaying host uplinks, bridge structures, and connected virtual machines / containers.
  - Quick search and filtering input in `Networks.svelte` by name, CIDR, mode, and interface.
- **Background Periodic SMART Health Watcher**:
  - Native daemon `smart.Watcher` performing periodic (24h) drives sweeps, alerting on disk failure, high temperature (≥60°C), or reallocated sectors (>50) via the `notify` subsystem.
- **Terminal Session Log Downloader**:
  - 1-click `.log` export in `TerminalPanel` to download session buffers directly from serial and host consoles.

### Fixed & Hardened

- **Storage Volume Thin Provisioning**: Switched from `STORAGE_VOL_CREATE_PREALLOC_METADATA` to standard sparse creation (`0`), preventing `qemu-img` from running `preallocation=falloc` and failing with `No space left on device` (ENOSPC).
- **Select Mode Toggle Bug**: Fixed issue where the Select Mode button in `VmList` auto-exited immediately due to an eager effect when `selectedKeys` was empty.
- **Security & Vulnerabilities**:
  - Upgraded Go dependency `golang.org/x/net` to `v0.62.0`, resolving GO-2026-6866 and GO-2026-6860.
  - Pinned `minimatch` to `^11.0.3` via npm overrides, eliminating 13 moderate vulnerabilities in frontend toolchain.

### Security & Hardening

- **Multi-Tenant Batch Clone ACL Enforcement**: Enforced `assertPoolAllowed`, `assertNetworkAllowed`, and `assertPoolPurpose` in `POST /api/vms/{id}/batch-clone` to prevent restricted operators from allocating VMs into unauthorized storage pools or network bridges.
- **Storage Upload OVA Safety**: Strictly clean up temporary files and abort with HTTP 400 on `.ova` archive extraction failures in `UploadDisk`, preventing unvalidated QCOW2 files from bypassing backing-file inspections.
- **TOTP 2FA Overwrite Protection**: Mandated current password verification when re-enabling or modifying TOTP settings on accounts where 2FA is already active.
- **Proxmox Vzdump Tar-Slip Mitigation**: Enforced `filepath.Clean` and traversal component rejection in `ImportFromProxmox`, preventing malicious backup archives from overwriting container configuration metadata.
- **Recursive System Disk Protection**: Implemented recursive block device tree inspection (`checkTree`) in `assertNotSystemDisk` to safeguard complex LVM and LUKS partitioned layouts from accidental disk wiping.
- **Privileged Account Metadata Protection**: Blocked non-admin operators from setting `ci_user` to privileged system accounts (`root`, `admin`, `system`) to prevent unauthorized guest password resets.

### Changed & Refactored

- **Modular Refactoring of `VmList` Dialogs**:
  - Extracted modals into dedicated components under `lib/components/vm-list/`: `VmImportModal`, `VmImportLxcModal`, `VmAssignGroupModal`, and `VmInstantiateModal`.

## [0.1.9] — 2026-10-25

### Added

- **1-Click Incus GPU Hardware Acceleration**:
  - Support for container GPU sharing (`type: gpu`, exposing `/dev/dri/renderD128`) without detaching from host kernel, both in container creation (`VmCreate`) and hot-toggle in hardware settings (`VmHardwareTab`).
- **Incus Container PCI Passthrough**:
  - Implemented `AttachPCIDevice` and `DetachPCIDevice` in `IncusBackend`, and enabled `VmPciCard` for container instances.
- **On-Demand Backup Retention Pruning**:
  - Added `POST /api/backup/targets/{id}/prune` endpoint and CLI command `webkvm-cli backup prune <target>`.
  - Added "Prune" action button in `Backup` page for targets with retention policies configured.
- **Unified VM Scheduler**:
  - Re-routed power and automatic snapshot scheduler through neutral `compute.Backend`, ensuring cron power operations and automated snapshot retention work identically for both KVM VMs and Incus containers.
- **802.1Q VLAN Tagging in VM Detail**:
  - Added optional VLAN Tag parameter to the "Add Interface" dialog in `VmDetail` and visual VLAN badge in `VmNetCard`.

### Changed & Refactored

- **Modular Refactoring of `api/vms.go`**:
  - Decomposed 2,216-line monolith into 5 clean domain modules (`vm_lifecycle.go`, `vm_snapshots.go`, `vm_devices.go`, `vm_clones.go`, `vm_export_import.go`), reducing `vms.go` to 494 lines while retaining 100% API compatibility.

## [0.1.8-fix01] — 2026-10-18

### Security & Hardening

- **Path Injection Guard**: Enforce strict `filepath.Clean` and `safeDiskPathRE` sanitization on disk and partition target devices prior to filesystem probes in `InitHostDiskDirectory`, resolving CodeQL security alerts #125 and #126.
- **Dependency Security**: Updated `source-map-js` to 1.2.2 and `postcss-selector-parser` to 7.1.6, addressing Dependabot vulnerability alerts.

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

### Fixed

- **Plain-HTTP login loop ([#5](https://github.com/Slaker19/webkvm/issues/5)):** session/CSRF cookies no longer carry the `Secure` flag when TLS is off, so browsers stop discarding the session on plain `http://` LAN deployments (dashboard flashed once and bounced back to the login page). An explicit `WEBKVM_COOKIE_SECURE` still wins when set.
- **`webkvm-cli users update`:** the subcommand advertised in the help text now exists — change password, role, email or active state from the terminal (`PUT /api/users/{username}`).
- **`webkvm-cli` local endpoint autodetection:** the CLI reads `{DATA_DIR}/config.json` (custom port, bind address, HTTP vs HTTPS) instead of guessing `https://127.0.0.1:8080`, retries once with the other scheme on an HTTPS/HTTP mismatch, and treats a machine-local bind address as local for zero-token admin access.
- **Installer no longer forces the `vmbr1` NAT bridge + dnsmasq in bridge-only mode:** isolated NAT stays opt-in (`--network=both` or `WEBKVM_NAT_BRIDGE=1`), matching the documented mode contract.

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

[0.1.11]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.11
[0.1.12]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.12
[0.1.13]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.13
[0.1.12-fix02]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.12-fix02
[0.1.12-fix01]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.12-fix01
[0.1.10]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.10
[0.1.4]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.4
[0.1.3]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.3
[0.1.2-fix2]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2-fix2
[0.1.2-fix1]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2-fix1
[0.1.2]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2
[0.1.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.1
[0.1.0]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.0
[0.0.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.0.1
