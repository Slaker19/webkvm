# Running WebKVM in Docker

WebKVM is a **native** app first (see [INSTALLATION.md](INSTALLATION.md)); the
container image is an *alternative* packaging of the exact same binary, for
people who prefer managing it with Docker/Compose. It is **not** a different,
lighter product: the goal is 1:1 feature parity with the native install, with
one unavoidable exception (self-update, see below).

## How it works

The container does **not** bundle libvirtd or QEMU. It connects to the
libvirtd **already running on the host**, the same way the native binary
does — VMs are started by the host's libvirtd, not inside the container. That
means:

- **Prerequisite:** the host must already have libvirt/QEMU installed and
  running. If it doesn't yet, run `install.sh` once (or just
  `make install-deps`) to set that up — Docker mode does not install
  libvirt/QEMU for you.
- The container only needs the *client-side* tools the webkvm binary shells
  out to (`qemu-img`, `virsh`, `ip`, `nft`, `xorriso`, `openssl`, `tar`, …),
  never `/dev/kvm` or a QEMU/libvirt server.
- `--network host --privileged` is required for full functionality (VM
  bridging to the real LAN, per-VM nftables rules) — the same trust level the
  native install already has, since its systemd unit also runs as root on the
  host. There is no "reduced" mode.

## Quick start (automated)

One line, from a fresh server — installs libvirt/QEMU and Docker if
missing, then deploys the published image:

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-docker.sh -o /tmp/webkvm-docker-install.sh && sudo bash /tmp/webkvm-docker-install.sh
```

Or, from a checkout of this repo:

```bash
sudo ./docker-install.sh              # pulls the published image
sudo ./docker-install.sh --source     # builds the image locally instead
sudo ./docker-install.sh --dry-run    # preview only, no changes
```

This verifies/installs the same libvirt/QEMU/KVM stack `install.sh` sets up
for a native install (see **Host packages** below — the container itself
brings everything else), verifies/installs Docker Engine + the compose
plugin, then runs `docker compose up -d` with the full privileges/mounts
from `docker-compose.yml`. Prerequisites are idempotent — safe to re-run.

## Quick start (manual, using the published image)

```bash
# 1) Edit docker-compose.yml if your paths differ from the defaults, then:
docker compose up -d
```

`docker-compose.yml` points at `slaker1908/webkvm:latest` by default, so
`docker compose up -d` just pulls it — no local build needed.

Prefer to build from source instead (e.g. to test a local change)?

```bash
make docker-build   # builds the binary + the image locally, tagged webkvm:latest
docker compose build
docker compose up -d
```

Open `https://<host-ip>:8080` — a self-signed certificate and initial admin
password are generated on first boot, exactly like a fresh native install.
(If `DATA_DIR` already holds an existing native install, the printed
"admin password" file may be a stale leftover from *that* install's first
boot, not the current password — log in with your existing credentials.)

## Host packages (prerequisites — install these BEFORE Docker)

Docker mode does not install libvirt/QEMU for you — the container is a
client of the libvirtd already running on the host, so this stack must
already be installed and running there, exactly as `install.sh` sets up
for a native install. `docker-install.sh` installs these for you; if you'd
rather do it by hand:

| Distro family | Command |
|---|---|
| Debian/Ubuntu (`apt`) | `sudo apt-get install libvirt-daemon-system libvirt-clients libvirt-daemon-driver-qemu qemu-system-x86 qemu-utils ovmf swtpm swtpm-tools virtinst bridge-utils dnsmasq-base` |
| Fedora/RHEL (`dnf`) | `sudo dnf install libvirt-daemon-kvm libvirt-client qemu-kvm qemu-img edk2-ovmf swtpm-tools virt-install bridge-utils dnsmasq` |
| Arch (`pacman`) | `sudo pacman -S libvirt qemu-full qemu-img swtpm edk2-ovmf virt-install dnsmasq` |

Then enable the daemon and Docker itself:

```bash
sudo systemctl enable --now libvirtd   # or virtqemud on newer libvirt (9.7+)
sudo systemctl enable --now docker
```

Docker Engine + the compose plugin, if not already installed:

| Distro family | Command |
|---|---|
| Debian/Ubuntu | `sudo apt-get install docker.io docker-compose-v2` |
| Fedora/RHEL | `sudo dnf install moby-engine docker-compose-plugin` (or add Docker's own repo: https://docs.docker.com/engine/install/fedora/) |
| Arch | `sudo pacman -S docker docker-compose` |

Notice what's **not** in these lists: `xorriso`, `openssl`, `python3`,
`nftables`, `qemu-utils`'s CLI tools for the *container's own* use, etc. —
those live inside the image (see the Dockerfile), not on the host. The
container talks to the host's nftables too, via `--network host` sharing
the host's network namespace directly — no separate host-side nftables
package is needed for that either.

## Manual `docker run` (no compose)

Equivalent to `docker-compose.yml`, for anyone who prefers a single command
over Compose (fill in a real `DATA_DIR` if it isn't `/opt/webkvm`):

```bash
docker run -d \
  --name webkvm \
  --restart unless-stopped \
  --network host \
  --privileged \
  -e DATA_DIR=/opt/webkvm \
  -e LIBVIRT_URI=qemu:///system \
  -e BIND_ADDR=0.0.0.0 \
  -e PORT=8080 \
  -v /opt/webkvm:/opt/webkvm \
  -v /var/run/libvirt:/var/run/libvirt \
  -v /run/systemd:/run/systemd \
  -v /var/log/journal:/var/log/journal:ro \
  -v /etc/passwd:/etc/passwd:ro \
  -v /etc/shadow:/etc/shadow:ro \
  -v /etc/group:/etc/group:ro \
  -v /etc/pam.d:/etc/pam.d:ro \
  slaker1908/webkvm:latest
```

See **Bind mounts — what and why** below for the reasoning behind each
`-v`/`-e`. Manage it afterward with `docker logs -f webkvm`, `docker
restart webkvm`, `docker rm -f webkvm`.

## Bind mounts — what and why

| Host path | Container path | Why |
|---|---|---|
| `/opt/webkvm` | `/opt/webkvm` | `DATA_DIR`: users, settings, certs, disk/ISO pools. Mounted at the **same absolute path** on both sides — libvirt stores a pool's location as a directory path, so if the container saw it at a different path than the host's libvirtd does, the pool would point at an empty/wrong directory. |
| `/var/run/libvirt` | `/var/run/libvirt` | The host's libvirt socket, so `LIBVIRT_URI=qemu:///system` reaches the host's libvirtd — identical to how the native binary talks to it. |
| `/run/systemd` | `/run/systemd` | Lets the container's `systemctl` CLI act on the host's real systemd (used for `journalctl`/service restart). |
| `/var/log/journal` (ro) | `/var/log/journal` | So `journalctl` can read the host's actual service logs. |
| `/etc/passwd`, `/etc/shadow`, `/etc/group`, `/etc/pam.d` (ro) | same paths | The **Host Terminal** feature authenticates real host user accounts via PAM. Only the *data* files are shared — the container uses its **own** `/bin/login` binary (same Debian base image), not the host's, to avoid a glibc/library-version mismatch between the two filesystems. PAM's `pam_unix` module only reads these data files, so authentication still checks real host passwords. |

Sharing the host's auth files and systemd socket, plus `--privileged`, gives
the container **root-equivalent trust over the host** — this is not a new
regression, it's the same trust the native install already has (its systemd
unit runs `User=root`). Do not expose this container to untrusted operators
without understanding that.

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `DATA_DIR` | `/opt/webkvm` | Where state/certs/pools live. Keep in sync with the volume mount above. |
| `LIBVIRT_URI` | `qemu:///system` | libvirt connection URI (talks to the host over the mounted socket). |
| `BIND_ADDR` | `0.0.0.0` | Listen address inside the container (with `network_mode: host` this is the host's own interface). |
| `PORT` | `8080` | Listen port. |

## The one deliberate exception: self-update

The native install's auto-update button (`git pull` + rebuild) doesn't make
sense for a container — you don't rebuild a running container, you replace
it. **Do not use the in-app update flow in Docker mode.** Instead, update by
pulling the new image and recreating the container:

```bash
docker compose pull
docker compose up -d
```

(If you build from source instead of using the published image, `git pull &&
make docker-build && docker compose up -d` is the equivalent.)

Everything else — Host Terminal, logs, service restart, VM networking,
firewall — is designed to behave exactly like the native install.

## Notes

- Run **either** the native install **or** the container on a given host, not
  both at once — they'd fight over the same port and the same disk/ISO pools.
- **Never point a second webkvm instance (Docker or native) at the same
  libvirtd with a different `DATA_DIR`, even "just to test".** libvirt
  storage pools are identified **by name** on the connection, not scoped per
  `DATA_DIR`: if the second instance already has pools with the same names
  (e.g. `webkvm-disks`, `ISOS`), it will silently **redefine their target
  path** to its own `DATA_DIR/pools/...`, breaking the first instance's view
  of its own storage (the underlying files aren't touched, but the pool
  object that points at them is). If you need to test Docker mode, do it
  against a libvirtd that has no pools yet, or rename the test pools first.
- The image is built for the same target architecture as the binary
  (`amd64`); build it on/for the host you intend to run it on.
