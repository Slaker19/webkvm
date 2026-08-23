# WebKVM — Installation guide & technical documentation

Single document covering **installation** of the WebKVM manager and the
**technical documentation** of the application and its code. For daily use see
[USAGE.md](USAGE.md).

---

## Part I — Installation

### 1. Requirements

| Requirement | Detail |
|-----------|---------|
| OS | Debian/Ubuntu (apt), Fedora/RHEL (dnf) or Arch (pacman) |
| Architecture | amd64 / x86_64 |
| KVM | `/dev/kvm` present (virtualization enabled in BIOS/UEFI, or nested on the hypervisor) |
| RAM | at least 2 GB |
| Disk | at least 5 GB free |
| Network | outbound internet (packages and tool downloads) |

### 2. One-command install

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-webkvm.sh | sudo bash
# it asks: IP only (HTTP) vs With SSL — self-signed cert works for IP + domain
# + optional domain (e.g. webkvm.example.com) + network mode (nat / bridge / both)
```

The script downloads the repository (tarball, no `git` required) to a working
directory under `/var/tmp` and runs the standalone installer. The full output is
persisted at **`/var/log/webkvm-install.log`**; if something fails the working
directory is kept so you can attach it to a bug report.

Or from a clone:

```bash
git clone https://github.com/Slaker19/webkvm
cd webkvm
sudo ./install.sh              # delegates to packaging/standalone/install.sh
sudo ./packaging/standalone/install.sh --dry-run   # verify without touching anything
```

What the installer does:

1. Preflight checks (KVM, RAM ≥ 2 GB, disk ≥ 5 GB, amd64) with actionable errors.
2. Installs libvirt, QEMU, OVMF, swtpm, xorriso and dnsmasq via apt/dnf/pacman.
   On Arch it also refreshes `archlinux-keyring` first (old ISOs ship stale keys).
3. Deploys the **prebuilt binary** (frontend embedded). The server **never
   compiles**: no Go/Node toolchain needed.
4. Installs `webkvm.service`, data under `/opt/webkvm`.
5. Asks about HTTPS and networking interactively, or uses defaults when piped
   (`curl … | sudo bash` → non-interactive: SSL yes, NAT networking).
6. Health-checks the service (`/api/health`) and prints the summary: URL,
   admin password, networks.

### 3. Install from a prebuilt binary

The binary embeds the frontend: it is the only artifact the server needs
(system libvirt/QEMU only). Build once on a dev machine or CI:

```bash
make binary   # backend/webkvm
make dist     # dist/webkvm-<version>.tar.gz (binary + installer + scripts + SHA256SUMS)
```

On the target server:

```bash
sudo WEBKVM_BINARY=backend/webkvm ./install.sh
# from a tarball:
tar xzf webkvm-<version>.tar.gz
sudo WEBKVM_BINARY=backend/webkvm bash packaging/standalone/install.sh
# served over HTTPS with mandatory checksum:
sudo WEBKVM_BINARY_URL=https://your-server/webkvm \
     WEBKVM_BINARY_SHA256=<sha256> \
     ./install.sh
```

**One generic amd64 binary works on every supported distro**: it needs GLIBC
≥ 2.34 and `libvirt.so.0`, both present in Debian 13+, Ubuntu 24+,
Fedora 43/44 and Arch. The installer adds each distro's libvirt packages.

> Upgrades in place are supported: running the installer again keeps all data
> under `/opt/webkvm` and rolls back automatically if anything fails.

### 4. Installer variables

| Variable | What it does |
|----------|--------------|
| `WEBKVM_DATA_DIR` | Data directory (default `/opt/webkvm`). |
| `WEBKVM_PREFIX` | Install prefix (default `/usr/local`). |
| `WEBKVM_BIND_ADDR` | Listen address (default `0.0.0.0`). |
| `WEBKVM_PORT` | Port (default `8080`). |
| `WEBKVM_BINARY` | Path to a local binary. |
| `WEBKVM_BINARY_URL` | HTTPS URL for the binary (checksum mandatory). |
| `WEBKVM_BINARY_SHA256` | SHA-256 checksum of that binary. |
| `WEBKVM_HTTPS=yes\|no` | `yes` = native HTTPS with self-signed cert (SAN covers IP + hostname [+ domain]); `no` = plain HTTP. Interactive prompt default `yes`. |
| `WEBKVM_TLS_DOMAIN` | Certificate domain (e.g. `webkvm.example.com`). Empty = IP/hostname only. When set, the SAN includes it and Let's Encrypt is attempted with automatic fallback to self-signed. |
| `NETWORK_MODE` | `nat`, `bridge` or `both`. Interactive default `both`; piped installs default to **`nat`** so your LAN is never reconfigured silently. |
| `BRIDGE_DHCP`, `BRIDGE_STATIC_IP`, `BRIDGE_STATIC_GW`, `BRIDGE_STATIC_DNS` | br0 bridge settings (DHCP by default, or static). |
| `WEBKVM_NONINTERACTIVE=1` | Ask nothing; apply defaults. |
| `WEBKVM_ADMIN_PASSWORD` | Choose the initial admin password (otherwise a random one is generated and saved). |

### 5. HTTPS (no reverse proxy required)

The installer asks plainly:

```
How do you want to access WebKVM?
  1) IP only (plain HTTP)
  2) With SSL — self-signed certificate (works for IP and domain names, recommended)
Certificate domain (optional — e.g. webkvm.example.com; empty = IP/hostname only;
SAN includes LAN IP + hostname.local + localhost)
```

- **IP only (`WEBKVM_HTTPS=no`)** — plain HTTP on port 8080.
- **With SSL (default)** — the backend serves **HTTPS natively** with an RSA-2048
  self-signed certificate valid for 10 years.
  SAN = `DNS:webkvm, DNS:localhost, DNS:<hostname>.local, IP:127.0.0.1, IP:<LAN>
  [+ DNS:<domain>]`. Works over IP and domain without nginx/apache/caddy.
- **Public domain + Let's Encrypt** — when `WEBKVM_TLS_DOMAIN` resolves to the
  server and ports 80/443 are reachable, autocert issues and renews real
  certificates (cache in `DATA_DIR/tls`). If validation fails it falls back to
  the self-signed cert including that domain — the service never goes down.

Browsers warn about self-signed certificates. Download yours from
`https://IP:8080/api/system/cert` and trust it system-wide. Change later in
**Settings → Server → TLS certificate / TLS domain**, then
`systemctl restart webkvm`.

**Reverse proxy?** Only if you already operate one for WAF/rate-limit/central
auth. In that case install with `WEBKVM_HTTPS=no` and point your vhost at
`http://127.0.0.1:8080`.

### 6. VM networking

Chosen with `NETWORK_MODE`:

- **NAT (`nat`)** — VMs reach the internet through the host
  (`192.168.122.0/24`, `virbr0`). Requires `dnsmasq` (installed automatically).
- **Bridge macvlan `br0` (`bridge`)** — VMs get their own IP on the real LAN
  (DHCP by default, or static with `BRIDGE_STATIC_IP/CIDR` + gateway + DNS).
  The host's own address is never touched; the bridge is created with
  `ip link add br0 type macvlan`.
- **Both (`both`, interactive default)** — both networks available, pick per VM.

From the UI (**Networking**) you can create extra networks/bridges and per-VM
nftables firewall rules. The installer wires this up through
`scripts/setup-network.sh`.

### 7. Non-interactive install (servers / pipelines)

Every option is env-overridable; without a TTY defaults are used:

```bash
sudo WEBKVM_PORT=8080 \
     WEBKVM_HTTPS=yes \
     WEBKVM_TLS_DOMAIN=webkvm.example.com \
     NETWORK_MODE=nat \
     WEBKVM_NONINTERACTIVE=1 \
     ./packaging/standalone/install.sh
```

Full install output is persisted at **`/var/log/webkvm-install.log`**; on
failure the working directory under `/var/tmp` is kept and its path printed.

### 8. Upgrading

Run the installer again with the new code/binary. It:

1. Backs up the current binary and unit file (`*.previous`).
2. Installs the new ones.
3. Restarts the service and checks `/api/health`.
4. **Rolls everything back automatically if any step fails.**

Everything under `/opt/webkvm` (config, disks, ISOs, users) is preserved.

### 9. Uninstall

```bash
cd packaging/standalone
sudo ./uninstall.sh               # keeps data
sudo PURGE_DATA=1 ./uninstall.sh  # also removes data
```

### 10. Troubleshooting

| Symptom | Likely cause / fix |
|---------|---------------------|
| `/dev/kvm` missing | Enable virtualization in BIOS/UEFI or nested on the hypervisor |
| Installer rolls back | Health-check failed; check `journalctl -u webkvm -n 100` |
| Browser certificate warning | Expected with self-signed; trust the cert from `/api/system/cert` |
| Service not listening | `systemctl status webkvm` and journal |
| Domain gets no real cert | Not publicly reachable; self-signed fallback is normal |
| Port already in use | Fails fast before installing; free the port or set `WEBKVM_PORT` |

Install log lives at `/var/log/webkvm-install.log`; a failed run keeps its
working directory under `/var/tmp` (path is printed).

---

## Part II — Technical documentation

### 11. Architecture

WebKVM is a **monolithic two-part application**:

- **Go backend** (`backend/`): REST API + noVNC console + embedded Svelte SPA in
  the single `webkvm` binary.
- **Svelte 5 frontend** (`frontend/`): SPA compiled to static assets, embedded
  with `go:embed`.

The backend talks to **libvirt** (`qemu:///system`) to manage domains, networks
and storage. All application state (users, tokens, settings, backups, groups,
nodes) persists as **JSON files inside `DATA_DIR`** — no external database.

```
Browser ──HTTP/SSE + Bearer JWT──► Go backend (:8080)
                                    │
                                    ├─ REST API (chi v5)
                                    ├─ JWT auth + RBAC (admin/operator/viewer)
                                    ├─ SSE event hub (VM state, metrics)
                                    ├─ noVNC proxy (WebSocket → VNC), serial WebSocket
                                    └─ Backup runner (cron) + OVA import/export
                                    │  libvirt C API (libvirt-go)
                                    ▼
                        libvirtd / qemu:///system
                        VMs · Networks · Storage pools · Snapshots
                                    ▼
                            QEMU/KVM hypervisor
```

Three planes:

1. **Control plane (Go backend)** — orchestrates libvirt and exposes the API.
2. **Data plane (libvirt/QEMU)** — the real hypervisor; VMs live here.
3. **State plane (`DATA_DIR`)** — application configuration and metadata.

### 12. Repository layout

| Path | Contents |
|------|----------|
| `backend/` | Go backend (`cmd/server`, `cmd/cli`, `cmd/migrate-disk-names`, `internal/*`). |
| `frontend/` | Svelte 5 SPA (Vite, Tailwind v4). |
| `docs/` | This guide and the usage guide. |
| `packaging/standalone/` | Bare-metal installer with auto-rollback, uninstaller, static tests. |
| `scripts/` | One-liner wrapper, network setup, systemd unit, logrotate, backup helper. |
| `Makefile` | build / test / dist / clean. |
| `install.sh` | Unified entry point (delegates to the standalone installer). |

### 13. Backend packages (`backend/internal/`)

| Package | Responsibility |
|---------|----------------|
| `api/` | HTTP router (chi v5), handlers, middleware, embedded noVNC console. |
| `libvirt/` | libvirt integration: domains, storage, networks, OVA, snapshots, metrics, events, host bridges. |
| `auth/` | JWT, middleware, RBAC, token blacklist, login rate limiting, console tickets. |
| `user/` | User store (bcrypt) in `users.json`; initial admin seed. |
| `tokens/` | Persistent API tokens (`wvmb_…`, sha256-hashed) in `api-tokens.json`. |
| `configstore/` | Typed hot-reloadable settings in `config.json`. |
| `config/` | Boot config (env + `.env`), JWT secret resolution. |
| `backupstore/` | Backup v2: targets, schedules, jobs, tar producer, SFTP support, restore. |
| `audit/` | JSONL audit log (`audit.log`, 10 MB rotation). |
| `events/` | SSE fan-out hub (VM state, metrics). |
| `nodes/` | libvirt node registry (`nodes.json`). |
| `models/` | Shared types (VM, pools, networks, users, RBAC, quotas, metrics…). |
| `appliances/` | Community appliance catalog, defaults and provisioning scripts. |
| `cloudinit/` | NoCloud seed ISO generation (validated, YAML-escaped, xorriso). |
| `notify/` | Webhook/SMTP notifications. |
| `vmsched/` | Cron-based VM power scheduling. |
| `firewall/` | Per-VM nftables rules with atomic apply. |
| `frontend/` | Embedded compiled Svelte assets (`go:embed`). |
| `logging/` | Structured `log/slog` (JSON) with optional file tee. |

### 14. Environment variables

| Variable | Default | Notes |
|----------|---------|-------|
| `PORT` | `8080` | Backend HTTP/HTTPS port. |
| `BIND_ADDR` | `127.0.0.1` → `0.0.0.0` fresh installs | Persisted as `server.bind_addr` in `config.json`. |
| `LIBVIRT_URI` | `qemu:///system` | libvirt URI. |
| `DATA_DIR` | `/opt/webkvm` | Persistent data directory. |
| `JWT_SECRET` | auto-generated | Stored at `{DATA_DIR}/jwt.key` (0600); weak secrets rejected. |
| `WEBKVM_LOG_FILE` | `""` | Log tee (read by `GET /api/system/logs`). |
| `VNC_PROXY_HOST` | `127.0.0.1` | Target host for noVNC→VNC TCP connections. |
| `PUBLIC_HOST` | `""` | IP baked into `.rdp`/`.vv` files (auto-detected when empty). |
| `CORS_ORIGIN` | `*` | Allowed CORS origins (comma-separated). |
| `TLS_CERT` / `TLS_KEY` / `TLS_DOMAIN` | — | TLS settings (persisted as server.tls_*). |
| `WEBKVM_ADMIN_PASSWORD` | — | Initial admin password (random generated otherwise). |

`.env` in the working directory is loaded as fallback (godotenv); real
environment variables always win.

### 15. On-disk layout (`DATA_DIR`)

| Path | Contents |
|------|----------|
| `pools/webkvm-disks` | VM disk pool (libvirt pool `webkvm-disks`). |
| `pools/ISOS` | ISO library pool. |
| `pool-purposes.json` | Purpose per pool (`disk`/`iso`). |
| `users.json` | Users with bcrypt hashes (0600). |
| `jwt.key` | JWT secret (0600). |
| `api-tokens.json` | sha256 hashes of API tokens. |
| `config.json` | Persisted settings (typed schema). |
| `backup/{targets,schedules,jobs}.json` | Backup v2 registry. |
| `nodes.json` | libvirt nodes. |
| `groups.json` | VM groups/tags. |
| `audit.log` | JSONL audit log. |
| `cifs-secrets.json` | CIFS secret UUIDs for netfs pools (0600). |
| `covers/` | VM cover images. |
| `logs/backend.log` | Structured log (when `WEBKVM_LOG_FILE` enabled). |
| `admin-password.initial` | Initial admin password (first boot only). |
| `certs/` | TLS certificates (self-signed). |
| `appliances.json` | Appliance catalog (seeded from defaults on first boot). |
| `tls/` | autocert cache (Let's Encrypt). |

### 16. Authentication & security

- **JWT HS256** (TTL 24 h by default, hot-reloadable). The server **refuses to
  boot** with placeholder/weak secrets; generates a random one and persists it
  in `jwt.key`.
- **Login rate limiting**: 5 failures / 15 min → 15-minute lockout (429 +
  `Retry-After`); loopback always trusted; `WEBKVM_TRUSTED_RATELIMIT_CIDRS`
  adds trusted CIDRs; `WEBKVM_TRUST_PROXY` controls `X-Forwarded-For`.
- **RBAC**: fixed hierarchy `admin > operator > viewer`.
- **API tokens**: `wvmb_` prefix + 32 random bytes; only the sha256 hash is
  stored; expiring and revocable.
- **`must_change_password`** blocks every endpoint except auth/password/health
  until the initial password is rotated.
- **SSRF blocklist** on ISO downloads (loopback, link-local, private ranges,
  CGNAT).
- **Path traversal**: sanitization on ISO upload/rename; validated paths on
  backups (`backupstore/path_safety.go`, symlink-aware deny list).
- **Firewall**: dedicated nftables table, atomic validation (`nft -c`), rules
  applied with argument-separated exec (no shell).
- **systemd hardening**: `NoNewPrivileges`, `ProtectSystem=full`,
  `ProtectHome=read-only`, `PrivateTmp`, restricted `CapabilityBoundingSet`,
  scoped `ReadWritePaths`.
- **Code scanning**: CodeQL with zero open alerts; golangci-lint + eslint +
  prettier enforced in CI.

### 17. REST API (summary)

Base URL: `http(s)://<host>:8080/api`. Auth: `Authorization: Bearer <JWT or
API token>`.

| Area | Main endpoints |
|------|----------------|
| Auth | `POST /auth/login`, `POST /auth/logout`, `GET /auth/me`, `PUT /users/me/password` |
| Users/groups | `GET/POST/PUT/DELETE /users/{username}`, `GET/POST /groups`, `GET /accounts` |
| VMs | `GET/POST /vms`, `GET/PUT/DELETE /vms/{id}`, power actions, clone/import/import-OVA/export |
| Console | serial WebSocket (+ single-use tickets), noVNC, `.rdp`/`.vv` download, clipboard |
| Storage | pools/volumes/ISOs CRUD, upload/download ISO |
| Networks | bridges/networks CRUD, VLAN-aware toggle |
| Snapshots/backups | snapshot CRUD + revert; backup targets/runs/jobs/schedules |
| Appliances | catalog CRUD, deploy (background job), provisioning scripts |
| System | `GET /health`, `/status`, `/metrics`, `/logs`, `GET /events` (SSE), `/system/cert`, `/system/version` |

Frontend uses a single API client (`frontend/src/lib/stores/auth.svelte.js`);
routes are registered in `backend/internal/api/router.go`.

### 18. CIFS-authenticated netfs pools

`netfs` pools with format `cifs` support authentication via **libvirt secrets**:
username + password are sent together (400 if partial), the password is stored
only as a libvirt secret UUID in `{DATA_DIR}/cifs-secrets.json` (0600) — never
returned by the API. Rotate credentials with `PUT /api/storage/pools/{name}`
(pool must be stopped). After a libvirtd reinstall (secrets wiped) recover the
pool by sending `cifs-needs-reauth: true` plus current credentials.

### 19. Build from source

```bash
make build          # npm ci + frontend build → go:embed → go build -o backend/webkvm
make test           # go test ./... && go vet ./... ; eslint + prettier on frontend
make dist           # dist/webkvm-<version>.tar.gz (binary + installer + SHA256SUMS)
```

Version is stamped via `git describe --tags --always` or `WEBKVM_VERSION`
(ldflags), fallback `dev`.

### 20. Do-not-break invariants

> Changing these breaks compatibility with existing installs and VMs.

| Invariant | Value |
|-----------|-------|
| Domain metadata XML namespace | `https://webvm.local/ns` |
| Disk pool name | `webkvm-disks` |
| ISO pool name | `ISOS` |
| Go module | `webkvm` |
| systemd service | `webkvm.service` |
| Backup filename pattern | `webkvm-<host>-<ts>` |

### 21. Security & license

- Vulnerability reports: [SECURITY.md](../SECURITY.md).
- License **AGPLv3**: [LICENSE](../LICENSE).
MDEOF
echo "INSTALLATION.md EN: $(wc -l < docs/INSTALLATION.md) líneas"