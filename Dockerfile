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
FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      openssl \
      python3 \
      xorriso \
      libvirt-clients \
      qemu-utils \
      iproute2 \
      nftables \
      tar \
      zstd \
      xz-utils \
      systemd \
      dbus \
    && rm -rf /var/lib/apt/lists/*

COPY backend/webkvm /usr/local/bin/webkvm
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/webkvm /usr/local/bin/docker-entrypoint.sh

ENV DATA_DIR=/opt/webkvm \
    BIND_ADDR=0.0.0.0 \
    PORT=8080

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
