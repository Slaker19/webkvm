.PHONY: all install-deps build build-cli run dev stop clean install uninstall update status logs install-systemd install-systemd-force install-caddy install-all regen-cert rollback docker-build

# F12-02: recipes below use bash-only syntax (`read ... <<< "$$(...)"`,
# a here-string). make(1) runs recipes with /bin/sh regardless of the
# user's login shell, and on Debian/Ubuntu — precisely this project's
# primary install target — /bin/sh is dash, which rejects `<<<` with
# "Syntax error: redirection unexpected". That made `make install-systemd`
# (and `make update` / `make rollback`) abort with a non-zero exit and a
# generic Makefile error on every Debian/Ubuntu host, even though the
# new binary had already been installed and the service was healthy —
# a false failure report that could mislead an operator into an
# unnecessary `make rollback` on a perfectly good deploy. Force bash so
# these recipes run the shell they were actually written for.
SHELL := /usr/bin/env bash

# Version: prefer git tag/describe, fall back to "dev" for local builds.
# The tag's leading "v" is stripped so `make build` bakes exactly what
# release.yml publishes (`BARE="${VERSION#v}"`): otherwise a locally built
# binary reports v0.1.0 while the UI, package.json and the release assets all
# say 0.1.0. The strip is applied with `=`, not `?=`, so it also covers a
# version passed in explicitly (`make dist VERSION=v1.2.3`) — those used to
# slip through and produce mislabelled artifacts.
VERSION_RAW ?= $(shell git describe --tags --always 2>/dev/null || echo "dev")
override VERSION := $(patsubst v%,%,$(or $(VERSION),$(VERSION_RAW)))
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w \
  -X 'main.Version=$(VERSION)' \
  -X 'main.BuildTime=$(BUILD_TIME)'

PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
SYSTEMDDIR ?= /etc/systemd/system
LOGROTATEDIR ?= /etc/logrotate.d

# On bare-metal installs the data dir is also the install dir.
DATADIR ?= /opt/webkvm
LOGDIR ?= /var/log/webkvm

all: build

# ---- Dependencies ----

install-deps:
	@echo "Detecting distro..."
	@if ! command -v go >/dev/null 2>&1; then \
		echo "Installing Go 1.26..."; \
		curl -sL https://go.dev/dl/go1.26.7.linux-amd64.tar.gz | sudo tar -C /usr/local -xzf -; \
		echo 'export PATH=$$PATH:/usr/local/go/bin:$$HOME/go/bin' >> $$HOME/.bashrc; \
		export PATH=$$PATH:/usr/local/go/bin:$$HOME/go/bin; \
		echo "Go installed. Run: source ~/.bashrc"; \
	else \
		echo "Go already installed: $$(go version)"; \
	fi; \
	if command -v apt >/dev/null 2>&1; then \
		echo "Debian/Ubuntu detected"; \
		sudo apt update && sudo apt install -y \
			libvirt-daemon-system libvirt-dev qemu-system-x86 \
			swtpm ovmf virtinst bridge-utils \
			curl ca-certificates nodejs npm git \
			gcc libc6-dev make; \
	elif command -v pacman >/dev/null 2>&1; then \
		echo "Arch Linux detected"; \
		sudo pacman -S --needed --noconfirm \
			libvirt qemu-full swtpm edk2-ovmf dmidecode \
			curl nodejs npm git base-devel go; \
	else \
		echo "Unsupported distro. Install dependencies manually."; \
		exit 1; \
	fi; \
	echo "Enabling libvirtd..."; \
	sudo systemctl enable --now libvirtd; \
	echo "Done!"

# ---- Build ----

build-frontend:
	cd frontend && npm ci 2>/dev/null || npm install
	cd frontend && npm run build
	@echo "Frontend built -> frontend/dist/"

build-backend: build-frontend
	cd backend && \
	  rm -rf internal/frontend/dist && \
	  mkdir -p internal/frontend/dist && \
	  cp -r ../frontend/dist/* internal/frontend/dist/
	cd backend && CGO_ENABLED=1 go build -buildvcs=false -ldflags "$(LDFLAGS)" -o webkvm ./cmd/server/
	@echo "Backend built -> backend/webkvm (v$(VERSION))"

# cli — build the standalone REST client (webkvm-cli). It is a pure Go
# client (no CGO, no libvirt headers), statically buildable.
build-cli:
	cd backend && CGO_ENABLED=0 go build -buildvcs=false -ldflags "-s -w" -o webkvm-cli ./cmd/cli/
	@echo "CLI built -> backend/webkvm-cli"

build: build-backend build-cli
	@echo ""
	@echo "Build complete (v$(VERSION))."
	@echo "Run 'make install' to install as a systemd service."

# ---- Standalone binary (no Node/Go needed on the target server) ----

# binary — build the self-contained single binary (frontend embedded).
# It is the only artifact the standalone installer needs from the build
# machine: the target server never has to install Go or Node.
binary: build
	@echo ""
	@echo "Binary ready: backend/webkvm (v$(VERSION), frontend embedded)"
	@echo ""
	@echo "Install it on a fresh server (no Go/Node required there):"
	@echo "  sudo WEBKVM_BINARY=$(PWD)/backend/webkvm ./install.sh"
	@echo "Or pack it for transfer:  make dist"

# dist — pack the binary + installer scripts into a single tarball, plus
# the RAW binaries update.sh downloads (it installs a binary, not a
# tarball — see packaging/standalone/update.sh). The binary is GENERAL
# (one build, amd64): Go+CGO only needs GLIBC >= 2.34 and libvirt.so.0,
# which the installer installs on every supported distro (Debian 12+,
# Ubuntu 22.04+, Fedora 36+, Arch, RHEL 9). SHA256SUMS uses bare asset
# filenames so update.sh's `grep "${BIN_NAME}"` actually matches.
dist: binary
	@rm -rf dist && mkdir -p dist
	@cp backend/webkvm dist/webkvm-$(VERSION)-linux_amd64
	@cp backend/webkvm-cli dist/webkvm-cli-$(VERSION)-linux_amd64
	@tar -czf dist/webkvm-$(VERSION).tar.gz \
		backend/webkvm \
		backend/webkvm-cli \
		install.sh \
		LICENSE CHANGELOG.md CHANGELOG.es.md \
		packaging/standalone/install.sh \
		packaging/standalone/uninstall.sh \
		packaging/standalone/update.sh \
		packaging/nfpm/webkvm.default \
		scripts/setup-network.sh scripts/setup-bridge.sh \
		scripts/webkvm.service \
		scripts/Caddyfile scripts/generate-self-signed.sh \
		scripts/install-caddy-systemd.sh scripts/install-webkvm.sh \
		scripts/smoke.sh \
		Makefile
	@cd dist && sha256sum webkvm-$(VERSION)-linux_amd64 webkvm-cli-$(VERSION)-linux_amd64 webkvm-$(VERSION).tar.gz > SHA256SUMS
	@echo ""
	@echo "Tarball:   dist/webkvm-$(VERSION).tar.gz"
	@echo "Binaries:  dist/webkvm-$(VERSION)-linux_amd64 dist/webkvm-cli-$(VERSION)-linux_amd64 (one build, amd64, all distros)"
	@echo "Checksum:  dist/SHA256SUMS (bare asset filenames)"
	@echo ""
	@echo "On the target server:"
	@echo "  tar xzf webkvm-$(VERSION).tar.gz"
	@echo "  sudo WEBKVM_BINARY=backend/webkvm bash packaging/standalone/install.sh"

# ---- Native packages (.deb/.rpm via nfpm) ----
# Version mapping (git tag stays v0.0.1):
#   deb: 0.0.1           (no prerelease suffix, no tilde needed)
#   rpm: 0.0.1 + release 1 (dash is illegal in rpm Version)
NFPM ?= nfpm
# Strip a leading "v" (git describe tags): package versions never carry it.
DEB_VER ?= $(shell echo "$(VERSION)" | sed 's/^v//; s/-beta$$/~beta/')
RPM_VER ?= $(shell echo "$(VERSION)" | sed 's/^v//; s/-.*//')
RPM_REL ?= $(shell if echo "$(VERSION)" | grep -q -- '-'; then echo "$(VERSION)" | sed 's/^[^-]*-/0./'; else echo 1; fi)

nfpm:
	command -v $(NFPM) >/dev/null 2>&1 || go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.43.3

deb: binary nfpm
	VERSION_DEB="$(DEB_VER)" $(NFPM) package --config packaging/nfpm.deb.yaml --packager deb --target dist/
	@echo "deb: dist/ (install: sudo apt install ./webkvm_*_amd64.deb)"

rpm: binary nfpm
	VERSION_RPM="$(RPM_VER)" VERSION_RPM_RELEASE="$(RPM_REL)" $(NFPM) package --config packaging/nfpm.rpm.yaml --packager rpm --target dist/
	@echo "rpm: dist/ (install: sudo dnf install ./webkvm-*.rpm)"

# ---- Docker ----

# docker-build — build the prebuilt binary (same artifact the native
# installer uses, never compiled inside the image) then the container
# image. See docker-compose.yml / docs/DOCKER.md for how to run it.
docker-build: build-backend
	docker build -t webkvm:$(VERSION) -t webkvm:latest .
	@echo ""
	@echo "Image built: webkvm:$(VERSION) (also tagged webkvm:latest)"
	@echo "Run it with: docker compose up -d"

# ---- Install / Uninstall (systemd) ----

install: build
	@echo "Installing webkvm..."
	@echo "  binary  -> $(BINDIR)/webkvm"
	@echo "  data    -> $(DATADIR)"
	@echo "  logs    -> $(LOGDIR)"
	@echo "  service -> $(SYSTEMDDIR)/webkvm.service"
	sudo install -d $(BINDIR)
	sudo install -d $(DATADIR)
	sudo install -d $(LOGDIR)
	# Put the user in the libvirt group so manual 'virsh' / 'virt-manager'
	# works after they re-login. The service itself runs as root.
	@if ! id -nG "$$USER" 2>/dev/null | tr ' ' '\n' | grep -qx libvirt; then \
		echo "  adding $$USER to 'libvirt' group (re-login required for manual virsh)"; \
		sudo usermod -aG libvirt "$$USER" || true; \
	fi
	sudo install -m 0755 backend/webkvm $(BINDIR)/webkvm
	sudo install -m 0644 scripts/webkvm.service $(SYSTEMDDIR)/webkvm.service
	sudo install -m 0644 scripts/webkvm.logrotate $(LOGROTATEDIR)/webkvm 2>/dev/null || true
	sudo systemctl daemon-reload
	sudo systemctl enable --now webkvm
	@echo ""
	@echo "  webkvm installed and running"
	@read -r proto port <<< "$$(sudo bash scripts/detect-webkvm-endpoint.sh $(DATADIR))"; \
		echo "  Open $$proto://localhost:$$port in your browser"
	@echo "  Initial admin password: $(DATADIR)/admin-password.initial (if generated)"
	@echo "  Service: systemctl status webkvm"

install-systemd: build-backend
	@echo "Installing webkvm backend (with health-check + auto-rollback)..."
	sudo install -d $(BINDIR)
	# Move the currently-running binary aside BEFORE we install the
	# new one. If anything goes wrong (service fails to start,
	# health check fails), `make rollback` puts this back.
	if [ -f $(BINDIR)/webkvm ] && [ ! -f $(BINDIR)/webkvm.previous ]; then \
		sudo cp -a $(BINDIR)/webkvm $(BINDIR)/webkvm.previous; \
		echo "  saved previous binary to $(BINDIR)/webkvm.previous"; \
	fi
	sudo install -m 0755 backend/webkvm $(BINDIR)/webkvm
	sudo install -d $(SYSTEMDDIR)
	sudo install -m 0644 scripts/webkvm.service $(SYSTEMDDIR)/webkvm.service
	sudo install -m 0644 scripts/webkvm.logrotate $(LOGROTATEDIR)/webkvm 2>/dev/null || true
	sudo systemctl daemon-reload
	sudo systemctl restart webkvm
	@echo ""
	@echo "  waiting for backend to come up..."
	@read -r proto port <<< "$$(sudo bash scripts/detect-webkvm-endpoint.sh $(DATADIR))"; \
	for i in $$(seq 1 20); do \
		if curl -kfsS -m 2 "$${proto}://127.0.0.1:$${port}/api/health" >/dev/null 2>&1; then \
			echo "  health check passed after $${i}s ($${proto}://127.0.0.1:$${port})"; \
			echo ""; \
			echo "  webkvm updated and running"; \
			echo "  To rollback: make rollback"; \
			echo "  To add HTTPS: make install-caddy"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "  ERROR: health check did not pass within 20s ($${proto}://127.0.0.1:$${port}) — auto-rolling back"; \
	sudo make --no-print-directory rollback; \
	exit 1

# install-systemd-force: skip the health check. Use only when you
# know the new binary is fine and the loopback port is unreachable
# (e.g. the binary binds to a different port for some reason).
install-systemd-force: build-backend
	@echo "Installing webkvm backend (FORCE — no health check)..."
	sudo install -d $(BINDIR)
	if [ -f $(BINDIR)/webkvm ] && [ ! -f $(BINDIR)/webkvm.previous ]; then \
		sudo cp -a $(BINDIR)/webkvm $(BINDIR)/webkvm.previous; \
	fi
	sudo install -m 0755 backend/webkvm $(BINDIR)/webkvm
	sudo install -d $(SYSTEMDDIR)
	sudo install -m 0644 scripts/webkvm.service $(SYSTEMDDIR)/webkvm.service
	sudo install -m 0644 scripts/webkvm.logrotate $(LOGROTATEDIR)/webkvm 2>/dev/null || true
	sudo systemctl daemon-reload
	sudo systemctl restart webkvm
	@echo "  webkvm updated (no health check performed)"

# Install Caddy for HTTPS termination. Idempotent: re-running
# overwrites /etc/caddy/Caddyfile (backed up to .bak.pre-webkvm on
# the first run) and reloads the service. Use SKIP_INSTALL=1 to
# only refresh the Caddyfile (e.g. after a custom-cert change).
install-caddy:
	@if [ "$$SKIP_INSTALL" = "1" ]; then \
		echo "Refreshing /etc/caddy/Caddyfile only (SKIP_INSTALL=1)..."; \
		sudo install -m 0644 scripts/Caddyfile /etc/caddy/Caddyfile; \
		sudo caddy validate --config /etc/caddy/Caddyfile; \
		sudo systemctl reload-or-restart caddy; \
	else \
		sudo scripts/install-caddy-systemd.sh; \
	fi

# Regenerate the self-signed cert (10-year, LAN IP + DNS:hostname SAN).
# Use when the host's IP changes or the cert expires.
regen-cert:
	sudo FORCE=1 scripts/generate-self-signed.sh
	sudo systemctl reload caddy

# One-shot: backend + caddy. Use on a fresh host.
install-all: install-systemd install-caddy
	@echo ""
	@echo "  webkvm fully installed (backend on :8080, https on :443)"
	@echo "  open https://$$(hostname -I | awk '{print $$1}')"

# Roll back to the previous binary (saved by install-systemd).
rollback:
	@if [ ! -f $(BINDIR)/webkvm.previous ]; then \
		echo "ERROR: $(BINDIR)/webkvm.previous not found" >&2; \
		echo "  Nothing to roll back to." >&2; \
		exit 1; \
	fi
	@echo "Rolling back webkvm backend..."
	sudo mv $(BINDIR)/webkvm.previous $(BINDIR)/webkvm
	sudo systemctl restart webkvm
	@read -r proto port <<< "$$(sudo bash scripts/detect-webkvm-endpoint.sh $(DATADIR))"; \
	for i in $$(seq 1 20); do \
		if curl -kfsS -m 2 "$${proto}://127.0.0.1:$${port}/api/health" >/dev/null 2>&1; then \
			echo "  rolled back; health check passed after $${i}s"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "  ERROR: rolled back but health check still failing ($${proto}://127.0.0.1:$${port}) — check 'systemctl status webkvm'"; \
	exit 1

uninstall:
	@echo "Removing webkvm..."
	-sudo systemctl disable --now webkvm 2>/dev/null
	-sudo rm -f $(SYSTEMDDIR)/webkvm.service
	-sudo rm -f $(LOGROTATEDIR)/webkvm
	-sudo rm -f $(BINDIR)/webkvm
	sudo systemctl daemon-reload
	@echo "Note: data in $(DATADIR) and logs in $(LOGDIR) were kept. Remove manually if desired."

# ---- Service management ----

status:
	@systemctl status webkvm --no-pager || true

logs:
	@journalctl -u webkvm -f --no-pager

# ---- Quick start (no install) ----

run:
	@if pgrep -f webkvm >/dev/null 2>&1; then \
		echo "webkvm is already running (pid: $$(pgrep -f webkvm | tr '\n' ' '))"; \
		echo "Run 'make stop' first if you want a fresh instance."; \
		exit 1; \
	fi
	@echo "Starting in background..."
	cd backend && nohup ./webkvm > backend.log 2>&1 &
	@echo "Backend PID: $$!  (logs: backend/backend.log)"
	@echo "Open http://localhost:8080"

stop:
	-pkill -f webkvm 2>/dev/null || true
	@echo "Stopped."

# ---- Development (hot reload, no embed) ----

dev-backend:
	cd backend && go run ./cmd/server/

dev-frontend:
	cd frontend && npm run dev

# ---- Clean ----

clean: stop
	rm -f backend/webkvm backend/webkvm-cli
	rm -rf frontend/dist backend/internal/frontend/dist
	@echo "Cleaned."

# ---- Release ----

# release — assemble the release artifacts (dist tarball + SHA256SUMS)
# from the current tree. With RELEASE_PUBLISH=1 it also creates the
# GitHub release via the gh CLI (tag must already exist). The CI
# workflow .github/workflows/release.yml performs the same build in a
# clean environment on every `v*` tag push, so `make release` is for
# local verification/pre-release staging.
release: dist
	@test "$(VERSION)" != "dev" || { \
		echo "ERROR: VERSION is 'dev' — tag the release first:" >&2; \
		echo "  git tag vX.Y.Z && git push origin vX.Y.Z" >&2; \
		exit 1; \
	}
	@echo ""
	@echo "Release artifacts ready for v$(VERSION):"
	@ls -l dist/
	@echo ""
	@if [ "$$RELEASE_PUBLISH" = "1" ]; then \
		command -v gh >/dev/null 2>&1 || { echo "ERROR: gh CLI not found" >&2; exit 1; }; \
		git tag -l "v$(VERSION)" | grep -qx "v$(VERSION)" || { \
			echo "ERROR: tag v$(VERSION) does not exist — create it first" >&2; exit 1; }; \
		REPO="$$(git remote get-url origin 2>/dev/null | sed 's#https://github.com/##; s#\.git$$##' || echo '')"; \
		[ -n "$$REPO" ] || { echo "ERROR: cannot determine origin repo" >&2; exit 1; }; \
		gh release create "v$(VERSION)" dist/* \
			--repo "$$REPO" \
			--title "WebKVM v$(VERSION)" \
			--notes "Checksums in SHA256SUMS. Full changelog: https://github.com/$$REPO/blob/main/CHANGELOG.md"; \
		echo "Published v$(VERSION) to GitHub ($$REPO)."; \
	else \
		echo "To publish (or re-run with RELEASE_PUBLISH=1):"; \
		echo "  gh release create v$(VERSION) dist/* --title \"WebKVM v$(VERSION)\""; \
	fi
