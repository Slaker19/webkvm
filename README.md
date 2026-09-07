# WebKVM

A **native** hybrid virtual machine manager (**QEMU/KVM** VMs **and
**LXC containers**) with a web UI. Go backend + embedded Svelte 5 frontend
in a single binary (~14 MB). No mandatory reverse proxy: the backend serves
**HTTPS directly** with a self-signed certificate. Docker is supported as an
alternative to the native install (see [docs/DOCKER.md](docs/DOCKER.md)) for
those who prefer it.

[![CI](https://github.com/Slaker19/webkvm/actions/workflows/ci.yml/badge.svg)](https://github.com/Slaker19/webkvm/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.25-blue)
![Svelte](https://img.shields.io/badge/Svelte_5-orange)
![License](https://img.shields.io/badge/license-AGPLv3-blue)

## Quick install (native, one line)

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-webkvm.sh -o /tmp/webkvm-install.sh && sudo bash /tmp/webkvm-install.sh
```

The installer asks:

1. **How do you want to access WebKVM?** — `IP only (HTTP)` vs `With SSL — self-signed certificate (works for IP and domain names, recommended)`
2. If you choose SSL, an **optional domain** — e.g. `webkvm.example.com` (empty = certificate for IP/hostname only; SAN includes LAN IP + 127.0.0.1 + webkvm + localhost + hostname.local [+domain])
3. **VM networking** — `nat` (internet through the host, 192.168.122.0/24), `bridge` (macvlan br0 on your real LAN) or `both`

**Supported families (detected by package manager):** Debian/Ubuntu & derivatives (`apt`), Fedora/RHEL & derivatives (`dnf`/`yum`), Arch & derivatives (`pacman`). Preflight checks `/dev/kvm`, RAM ≥ 2 GB, disk ≥ 5 GB, `amd64`.

The binary is **prebuilt** and embeds the frontend — the server **never compiles**
(no Go/Node toolchain required). Build once with `make dist`, deploy with
`WEBKVM_BINARY` / `WEBKVM_BINARY_URL`.

```bash
# non-interactive (pipelines)
sudo WEBKVM_HTTPS=yes WEBKVM_TLS_DOMAIN=webkvm.example.com NETWORK_MODE=nat WEBKVM_NONINTERACTIVE=1 bash install-webkvm.sh

# dry-run (touches nothing)
sudo ./packaging/standalone/install.sh --dry-run
```

When done: `Web UI: https://IP:8080` (+ `https://your-domain:8080` if you set a
domain), certificate at `/opt/webkvm/certs/webkvm.crt`, admin password printed
and stored at `/opt/webkvm/admin-password.initial`. Change it on first login.

**No reverse proxy needed:** you don't need nginx/apache/caddy for HTTPS — the
backend terminates TLS natively. Only put a proxy in front if you already run
one (WAF/rate-limit/central auth); then install with `WEBKVM_HTTPS=no` and
`proxy_pass http://127.0.0.1:8080`.

## Quick install (Docker, one line)

Prefer Docker? This installs libvirt/QEMU + Docker if missing, then runs the
published image against the host's own libvirtd — see
[docs/DOCKER.md](docs/DOCKER.md) for host packages, the manual `docker run`
equivalent, and what every mount/privilege is for.

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-docker.sh -o /tmp/webkvm-docker-install.sh && sudo bash /tmp/webkvm-docker-install.sh
```

## Update

```bash
# from GitHub release (recommended)
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/packaging/standalone/update.sh -o /tmp/webkvm-update.sh && sudo bash /tmp/webkvm-update.sh

# from local repo clone
sudo /opt/webkvm-repo/packaging/standalone/update.sh --source
```

The updater downloads the latest release, stops the service, backs up the old
binary, installs the new one, runs a health check and restarts — with automatic
rollback if anything fails.

## Containers (LXC) — opt-in module

WebKVM manages containers natively through **Incus** (the community fork of
LXD, v2.2.0). The Incus Go client keeps the LXD REST API, so **existing LXD
daemons keep working unchanged**. Containers appear alongside VMs in the same
unified list with a `KVM`/`Incus` badge, are created from the same form
(friendly image picker + cloud-init credentials), support root-disk resize,
network interfaces and live CPU/RAM metrics.

**Host dependency.** The Incus daemon must be installed and running **on the
host** — WebKVM does not install it for you unless you pass
`WEBKVM_INSTALL_INCUS=1` to the installer. Native packages (no snap):

```bash
# Debian / Ubuntu / Arch / Fedora / RedHat family (native package)
sudo apt install incus    # or: sudo pacman -S incus / sudo dnf install incus
sudo systemctl enable --now incus
# first-run setup
sudo incus admin init     # legacy LXD: sudo lxd init
```

**Activation.** The container module is an opt-in (disabled by default). Enable
it with the `WEBKVM_INCUS_ENABLED=1` environment variable — in the systemd unit
(`Environment=WEBKVM_INCUS_ENABLED=1` in a drop-in under
`/etc/systemd/system/webkvm.service.d/`) or in the project `.env`:

```bash
WEBKVM_INCUS_ENABLED=1    # enable the container module
INCUS_SOCKET=             # optional override; auto-detected otherwise
```

The socket is auto-detected, probing the **Incus paths first**
(`/var/lib/incus/unix.socket`, `/run/incus/*`) and falling back to the legacy
LXD paths (snap `/var/snap/lxd/common/lxd/unix.socket`, apt
`/var/lib/lxd/unix.socket`); override with `INCUS_SOCKET` if your daemon
listens elsewhere.

**Permissions.** The user that runs the WebKVM process must be able to read the
daemon socket. The native service runs as `root` (no extra step). If you run
the binary as a non-root user (direct execution, hardened service or Docker),
add that user to the **`incus-admin`** group (Incus) or **`lxd`** group
(legacy LXD):

```bash
sudo usermod -aG incus-admin <user> && sudo systemctl restart webkvm
```

**Verify.** `incus_connected` appears in the backend log at startup and
containers show up in the web UI. The installer can do all of the above for you:

```bash
sudo WEBKVM_INSTALL_INCUS=1 bash install-webkvm.sh
```

> **Installer note (v2.2.0):** `WEBKVM_INSTALL_INCUS=1` installs the **native
> `incus` package** on every package manager (apt/pacman/dnf), falling back to
> the legacy native `lxd` package when `incus` is unavailable. Snap is **never**
> installed or forced. If no package is available it prints a warning asking you
> to install Incus or LXD manually and continues with a KVM-only install.

## Documentation

- **[docs/INSTALLATION.md](docs/INSTALLATION.md)** — step-by-step install, HTTPS,
  networking, variables, upgrade/uninstall plus technical docs (architecture,
  API, storage).
- **[docs/USAGE.md](docs/USAGE.md)** — daily use: VMs, templates, cloud-init,
  storage, networking, firewall, backups, users/roles.
- **[docs/DOCKER.md](docs/DOCKER.md)** — running WebKVM in a container against
  your host's existing libvirtd.
- **[docs/runbooks/RUNBOOKS.md](docs/runbooks/RUNBOOKS.md)** — runbooks de
  operaciones (V12-OPS-04): upgrade/rollback, disaster recovery, modelo de
  seguridad y auditoría, tareas day-2, smoke test.

## Build from source

```bash
make build          # Go backend + Svelte frontend → backend/webkvm
make dist           # dist/webkvm-<version>.tar.gz (binary + installer + SHA256SUMS)
```

## CLI for sysadmins

```bash
TOKEN=$(curl -ks -X POST https://127.0.0.1:8080/api/auth/login -H "Content-Type: application/json" -d '{"username":"admin","password":"YOUR_PASSWORD"}' | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
webkvm-cli --insecure --server https://127.0.0.1:8080 --token $TOKEN status
webkvm-cli --insecure --server https://127.0.0.1:8080 --token $TOKEN vms list
webkvm-cli --insecure --server https://127.0.0.1:8080 --token $TOKEN storage pools
```

## Security

- Secrets stored `0600` (JWT, CIFS secrets), login rate limiting,
  RBAC `admin > operator > viewer`, JSONL audit log, systemd hardening.
  See [SECURITY.md](SECURITY.md).
- Self-signed cert downloadable from `https://IP:8080/api/system/cert`; with a
  public `TLS_DOMAIN` Let's Encrypt is attempted (falls back to self-signed).

## License

**AGPLv3** — see [LICENSE](LICENSE).
