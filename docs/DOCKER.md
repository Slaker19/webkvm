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

## Quick start

```bash
# 1) Build the prebuilt binary + the image (must be built on/for the target
#    host's architecture — the binary is not compiled inside the image).
make docker-build

# 2) Edit docker-compose.yml if your paths differ from the defaults, then:
docker compose up -d
```

Open `https://<host-ip>:8080` — a self-signed certificate and initial admin
password are generated on first boot, exactly like a fresh native install.

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
pulling/rebuilding the image and recreating the container:

```bash
git pull
make docker-build
docker compose up -d
```

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
