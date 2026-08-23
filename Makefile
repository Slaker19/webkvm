.PHONY: all install-deps build build-cli run dev stop clean install uninstall update status logs install-systemd install-systemd-force install-caddy install-all regen-cert rollback

# Version: prefer git tag/describe, fall back to "dev" for local builds.
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo "dev")
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
		echo "Installing Go 1.25..."; \
		curl -sL https://go.dev/dl/go1.25.0.linux-amd64.tar.gz | sudo tar -C /usr/local -xzf -; \
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

# dist — pack the binary + installer scripts into a single tarball.
# The binary is GENERAL (one build, amd64): Go+CGO only needs GLIBC >= 2.34
# and libvirt.so.0, which the installer installs on every supported distro
# (Debian 12+, Ubuntu 22.04+, Fedora 36+, Arch, RHEL 9). No per-distro
# packages required. A SHA256SUMS file is emitted for WEBKVM_BINARY_SHA256.
dist: binary
	@rm -rf dist && mkdir -p dist
	@tar -czf dist/webkvm-$(VERSION).tar.gz \
		backend/webkvm \
		backend/webkvm-cli \
		install.sh \
		packaging/standalone/install.sh \
		packaging/standalone/uninstall.sh \
		scripts/setup-network.sh scripts/setup-bridge.sh \
		scripts/Caddyfile scripts/generate-self-signed.sh \
		scripts/install-caddy-systemd.sh scripts/install-webkvm.sh \
		Makefile
	@sha256sum backend/webkvm backend/webkvm-cli > dist/SHA256SUMS
	@echo ""
	@echo "Tarball:   dist/webkvm-$(VERSION).tar.gz"
	@echo "Binary:    dist/webkvm-$(VERSION).tar.gz (one build, amd64, all distros)"
	@echo "Checksum:  dist/SHA256SUMS   ->  WEBKVM_BINARY_SHA256=$$(awk '{print $$1}' dist/SHA256SUMS)"
	@echo ""
	@echo "On the target server:"
	@echo "  tar xzf webkvm-$(VERSION).tar.gz"
	@echo "  sudo WEBKVM_BINARY=backend/webkvm bash packaging/standalone/install.sh"

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
	@echo "  ✅ webkvm installed and running"
	@echo "  Open http://localhost:8080 in your browser"
	@echo "  Initial admin password: $$(DATADIR)/admin-password.initial (if generated)"
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
	@for i in $$(seq 1 20); do \
		if curl -fsS -m 2 http://127.0.0.1:8080/api/health >/dev/null 2>&1; then \
			echo "  health check passed after $${i}s"; \
			echo ""; \
			echo "  ✅ webkvm updated and running"; \
			echo "  To rollback: make rollback"; \
			echo "  To add HTTPS: make install-caddy"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "  ❌ health check did not pass within 20s — auto-rolling back"; \
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
	@echo "  ✅ webkvm updated (no health check performed)"

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
	@echo "  ✅ webkvm fully installed (backend on :8080, https on :443)"
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
	@for i in $$(seq 1 20); do \
		if curl -fsS -m 2 http://127.0.0.1:8080/api/health >/dev/null 2>&1; then \
			echo "  ✅ rolled back; health check passed after $${i}s"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "  ❌ rolled back but health check still failing — check 'systemctl status webkvm'"; \
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
