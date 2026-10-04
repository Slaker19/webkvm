# WebKVM — Container Image
#
# Multi-stage build:
# Stage 1: Build the Svelte SPA frontend assets
# Stage 2: Build the Go backend binary with CGO & libvirt on Ubuntu 24.04
# Stage 3: Minimal, secure runtime image based on Ubuntu 24.04

# ==============================================================================
# Stage 1: Build Frontend
# ==============================================================================
FROM node:22-bookworm-slim AS frontend-builder

WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci --prefer-offline --no-audit 2>/dev/null || npm install --no-audit

COPY frontend/ ./
RUN npm run build

# ==============================================================================
# Stage 2: Build Backend
# ==============================================================================
FROM ubuntu:24.04 AS backend-builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      build-essential \
      pkg-config \
      libvirt-dev \
    && rm -rf /var/lib/apt/lists/*

# Install Go matching host architecture (amd64 / arm64)
RUN ARCH=$(dpkg --print-architecture) && \
    curl -fsSL "https://go.dev/dl/go1.26.0.linux-${ARCH}.tar.gz" | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

WORKDIR /src/backend
# Cache Go dependencies layer
COPY backend/go.mod backend/go.sum ./
RUN go mod download

# Copy backend source
COPY backend/ ./

# Copy compiled frontend assets from Stage 1 into backend embedded FS
COPY --from=frontend-builder /src/frontend/dist ./internal/frontend/dist

ARG VERSION=0.1.6
RUN CGO_ENABLED=1 go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/webkvm ./cmd/server && \
    CGO_ENABLED=0 go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/webkvm-cli ./cmd/cli

# ==============================================================================
# Stage 3: Minimal Runtime Image
# ==============================================================================
FROM ubuntu:24.04

# Runtime dependencies: client-side tools required for libvirt, storage,
# networking and system operations.
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
      mdadm \
      zfsutils-linux \
    && rm -rf /var/lib/apt/lists/*

COPY --from=backend-builder /out/webkvm /usr/local/bin/webkvm
COPY --from=backend-builder /out/webkvm-cli /usr/local/bin/webkvm-cli
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/webkvm /usr/local/bin/webkvm-cli /usr/local/bin/docker-entrypoint.sh

ENV DATA_DIR=/opt/webkvm \
    BIND_ADDR=0.0.0.0 \
    PORT=8080

EXPOSE 8080

# HTTPS first (the entrypoint self-signs by default), plain HTTP fallback
# for installs with TLS disabled.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -kfsS --max-time 4 "https://127.0.0.1:${PORT:-8080}/api/health" || curl -fsS --max-time 4 "http://127.0.0.1:${PORT:-8080}/api/health" || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
