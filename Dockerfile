# WebKVM — container image
#
# This image does NOT bundle libvirtd/QEMU: it is a thin client that talks
# to the libvirtd ALREADY running on the host (see docker-compose.yml). VMs
# are started by the host's libvirtd, never inside this container, so no
# /dev/kvm, libvirt-daemon-system or qemu-system-x86 is needed here — only
# the client-side tools the webkvm binary itself shells out to.
#
# The binary is prebuilt (same artifact the native installer uses) and only
# copied in here, never compiled inside the image — see backend/webkvm.
# Base pinned to an LTS for reproducible builds (rolling moves under us).
FROM ubuntu:24.04

# Every binary the backend shells out to must exist here (see the
# exec.Command inventory): ip, nft, iptables, qemu-img, virsh, xz,
# xorriso, tar, mountpoint/umount (util-linux), sysctl (procps),
# /bin/login (login — Host Terminal), curl (HEALTHCHECK below).
# gdisk/parted/e2fsprogs/xfsprogs/btrfs-progs/f2fs-tools: the Storage >
# Host Disks format/mount flow (host_disks.go) shells out to sgdisk,
# parted/partprobe and one mkfs.* per entry in the curated filesystem
# catalog — needed here too, since a physical disk passed through to
# this container (e.g. --device=/dev/sdb) is formatted from inside it,
# not on the host.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      openssl \
      python3 \
      xorriso \
      libvirt-clients \
      qemu-utils \
      iproute2 \
      nftables \
      iptables \
      procps \
      util-linux \
      login \
      tar \
      zstd \
      xz-utils \
      systemd \
      dbus \
      gdisk \
      parted \
      e2fsprogs \
      xfsprogs \
      btrfs-progs \
      f2fs-tools \
    && rm -rf /var/lib/apt/lists/*

COPY backend/webkvm /usr/local/bin/webkvm
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/webkvm /usr/local/bin/docker-entrypoint.sh

ENV DATA_DIR=/opt/webkvm \
    BIND_ADDR=0.0.0.0 \
    PORT=8080

EXPOSE 8080

# HTTPS first (the entrypoint self-signs by default), plain HTTP fallback
# for installs with TLS disabled.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -kfsS --max-time 4 "https://127.0.0.1:${PORT:-8080}/api/health" || curl -fsS --max-time 4 "http://127.0.0.1:${PORT:-8080}/api/health" || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
