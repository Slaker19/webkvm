#!/usr/bin/env bash
# WebKVM compatibility shim for community-scripts installers.
#
# An upstream install/<app>-install.sh is ordinary Debian/Alpine shell,
# but it is not self-contained: it opens by sourcing a framework and then
# calls ~15 helpers it never defines. Run one directly and it dies on the
# first line.
#
# The framework splits in two halves, and only one of them is portable:
#
#   core.func, tools.func   distro-level helpers — logging, apt retries,
#                           fetch_and_deploy_gh_release, setup_nodejs,
#                           setup_postgresql, arch_resolve and so on.
#                           Plain shell, no Proxmox assumptions.
#   install.func            container-level helpers — they assume they
#                           are running inside an LXC created moments ago
#                           by `pct`, and reach for RETRY_NUM, PASSWORD,
#                           container-getty overrides and an /usr/bin/update
#                           wrapper that re-runs the Proxmox launcher.
#
# So this shim sources the first half verbatim from upstream and replaces
# only the second. That is the whole design: 215 functions come from
# upstream (their code, their maintenance, their bug fixes), and WebKVM
# owns just the seven container-lifecycle stubs below — the ones whose
# job WebKVM has already done itself by the time this runs.
#
# Reimplementing the first half was the alternative. It is ~1300 lines of
# delicate apt/keyring/version logic; a private copy would silently rot
# against upstream and break installs in ways that are hard to diagnose.
#
# Invariants this file must preserve:
#   - Never `exit` from a helper on a non-fatal condition: the installer
#     is one long script and an early exit strands a half-built app.
#   - Every stub must be safe to call more than once.
#   - No network calls beyond the two upstream sources.

set -o pipefail

# ---------------------------------------------------------------------
# Environment the upstream halves expect to already exist.
# ---------------------------------------------------------------------

# VERBOSE drives $STD: upstream prefixes noisy commands with "$STD" so a
# quiet run can route them to /dev/null. WebKVM captures the whole log to
# /var/log/webkvm-provision.log and shows it in the UI, so the useful
# default is verbose — a silent failure is the expensive kind.
VERBOSE="${VERBOSE:-yes}"
STD=""
export STD VERBOSE

# Upstream's retry loops read these; unset they become `sleep` with no
# argument and an infinite wait.
RETRY_NUM="${RETRY_NUM:-10}"
RETRY_EVERY="${RETRY_EVERY:-3}"
export RETRY_NUM RETRY_EVERY

# Telemetry is opt-in upstream and stays off here. It is only ever read
# by install.func, which this shim does not source, but setting it
# explicitly means the answer does not change if that ever shifts.
DIAGNOSTICS="no"
export DIAGNOSTICS

# tz is read when configuring the container clock.
tz="${tz:-Etc/UTC}"
export tz

# APPLICATION/APP label log lines. WebKVM exports APP before sourcing
# this file; the fallback is a plain word on purpose. Using a
# "{{PLACEHOLDER}}" as the default here does not work: bash closes
# ${APP:-...} at the *first* unescaped brace, so the default expanded to
# a truncated token with stray braces trailing after it, which then
# leaked into the banner and the provenance file.
APP="${APP:-app}"
APPLICATION="${APPLICATION:-$APP}"
export APP APPLICATION

# CACHER controls the optional apt proxy. Empty means "direct", which is
# what a WebKVM container wants.
CACHER="${CACHER:-no}"
CACHER_IP="${CACHER_IP:-}"
export CACHER CACHER_IP

export DEBIAN_FRONTEND=noninteractive

# ---------------------------------------------------------------------
# Upstream halves.
# ---------------------------------------------------------------------
#
# Sourced, not piped: a pipeline runs the source in a subshell and every
# function it defines vanishes when that subshell exits, leaving the
# installer to fail on the first helper call with a bare "command not
# found".

# Base tooling the upstream halves take for granted.
#
# The community-scripts framework was written against Proxmox's LXC
# templates, which ship gnupg, curl and friends. A stock Incus/cloud
# image is more minimal. The gap is not cosmetic: without gnupg,
# download_gpg_key cannot dearmor a repository key, so setup_nodejs,
# setup_php, setup_postgresql and every other setup_* that adds an APT
# repository fail — and they fail at key-import time, with a message
# that points at the key rather than at the missing tool.
_webkvm_bootstrap_prereqs() {
  local missing=()
  command -v curl >/dev/null 2>&1 || missing+=(curl)
  command -v gpg >/dev/null 2>&1 || missing+=(gnupg)
  command -v jq >/dev/null 2>&1 || missing+=(jq)
  command -v file >/dev/null 2>&1 || missing+=(file)
  command -v sudo >/dev/null 2>&1 || missing+=(sudo)
  [[ -e /usr/share/ca-certificates ]] || missing+=(ca-certificates)
  ((${#missing[@]} == 0)) && return 0

  if command -v apk >/dev/null 2>&1; then
    apk update >/dev/null 2>&1
    apk add --no-cache "${missing[@]}" >/dev/null 2>&1
  else
    apt-get update >/dev/null 2>&1
    apt-get install -y --no-install-recommends "${missing[@]}" >/dev/null 2>&1
  fi
}
_webkvm_bootstrap_prereqs

_webkvm_source_remote() {
  local name="$1" url="$2" dest="/usr/local/share/webkvm/${1}"
  mkdir -p /usr/local/share/webkvm
  if [[ ! -s "$dest" ]]; then
    if ! curl -fsSL --retry 3 --retry-delay 2 --max-time 120 -o "$dest" "$url"; then
      echo "webkvm: cannot download ${name} from ${url}" >&2
      return 1
    fi
  fi
  # shellcheck disable=SC1090
  source "$dest" || {
    echo "webkvm: cannot load ${name}" >&2
    return 1
  }
}

_WEBKVM_CORE_URL="${WEBKVM_CORE_URL:-https://raw.githubusercontent.com/community-scripts/core/main/core/core.func}"
_WEBKVM_TOOLS_URL="${WEBKVM_TOOLS_URL:-https://raw.githubusercontent.com/community-scripts/ProxmoxVE/main/misc/tools.func}"
_WEBKVM_ERRH_URL="${WEBKVM_ERRH_URL:-https://raw.githubusercontent.com/community-scripts/core/main/core/error_handler.func}"

_webkvm_source_remote "core.func" "$_WEBKVM_CORE_URL" || exit 90
_webkvm_source_remote "tools.func" "$_WEBKVM_TOOLS_URL" || exit 91
# error_handler.func carries catch_errors and the ERR/EXIT traps that
# abort a broken install. Loading it is not optional: without it a failed
# dependency step (a missing GPG key, an unreachable repository) merely
# prints a red line and the script sails on to "finish" with rc=0,
# handing the user a container whose service cannot even start.
_webkvm_source_remote "error_handler.func" "$_WEBKVM_ERRH_URL" || exit 94

# color() and the msg_* family come from core.func. Calling color() now
# populates the escape sequences the installer prints.
declare -F color >/dev/null 2>&1 && color

# ---------------------------------------------------------------------
# The seven container-lifecycle stubs.
#
# Each of these exists upstream to finish creating an LXC. WebKVM has
# already created the container, configured its network, set credentials
# and installed its own console access before this script ever runs, so
# the honest implementation is to verify rather than repeat.
# ---------------------------------------------------------------------

# catch_errors comes from error_handler.func and is deliberately NOT
# overridden here.
#
# An earlier version of this shim defined its own lenient variant that
# set -E without -e, so a failing command was reported and then ignored.
# The result was worse than no error handling at all: installing Actual
# Budget, the Node.js repository setup failed, npm was never installed,
# every later step failed too — and the script still exited 0 and
# reported success, leaving a container whose service restart-looped on
# a missing binary. A failed install must fail loudly.
declare -F catch_errors >/dev/null 2>&1 || {
  echo "webkvm: error_handler.func did not provide catch_errors" >&2
  exit 95
}

# verb_ip6: upstream optionally disables IPv6 inside the container.
# WebKVM manages container networking itself, so silently rewriting
# sysctl here would contradict the network the user configured.
verb_ip6() { :; }

# setting_up_container: upstream waits for DHCP to hand the fresh LXC an
# address. WebKVM's container is already up, but the wait is still worth
# keeping: cloud-init can start this script microseconds before the
# interface is ready, and every install that follows needs the network.
setting_up_container() {
  msg_info "Waiting for network"
  local i
  for ((i = RETRY_NUM; i > 0; i--)); do
    [[ -n "$(hostname -I 2>/dev/null)" ]] && break
    sleep "$RETRY_EVERY"
  done
  if [[ -z "$(hostname -I 2>/dev/null)" ]]; then
    msg_error "No network after $((RETRY_NUM * RETRY_EVERY))s"
    return 121
  fi
  # Debian/Ubuntu images refuse pip installs outside a venv unless this
  # marker is removed; several installers assume upstream cleared it.
  rm -rf /usr/lib/python3.*/EXTERNALLY-MANAGED 2>/dev/null || true
  msg_ok "Network ready: $(hostname -I | awk '{print $1}')"
}

# network_check: upstream pings public resolvers and aborts without
# connectivity. Kept, because failing here with a clear message is far
# better than failing twenty minutes later mid-compile.
network_check() {
  local ok=no
  if ping -c1 -W2 1.1.1.1 &>/dev/null || ping -c1 -W2 8.8.8.8 &>/dev/null || ping -c1 -W2 9.9.9.9 &>/dev/null; then
    ok=yes
  fi
  if [[ "$ok" != yes ]]; then
    msg_error "No internet connectivity; the installer needs to download packages"
    return 101
  fi
  msg_ok "Internet connected"
}

# update_os: upstream's version also wires up an apt caching proxy and
# reads container-creation variables WebKVM never sets. The part that
# matters to an installer is simply a current package index.
update_os() {
  msg_info "Updating OS packages"
  if command -v apk >/dev/null 2>&1; then
    apk update >/dev/null 2>&1 && apk upgrade >/dev/null 2>&1
  else
    apt-get update >/dev/null 2>&1
    apt-get -o Dpkg::Options::="--force-confold" -y upgrade >/dev/null 2>&1
  fi
  msg_ok "Updated OS packages"
}

# motd_ssh: upstream writes a login banner advertising the Proxmox
# helper-scripts project and its /usr/bin/update wrapper. That wrapper
# re-runs the *Proxmox* launcher, which would fail on an Incus container
# and confuse anyone who ran it, so WebKVM writes a banner that credits
# upstream without promising a broken command.
motd_ssh() {
  local f=/etc/profile.d/00_webkvm.sh
  {
    echo '[ -t 1 ] || return 0'
    echo 'echo ""'
    echo "echo \"${APPLICATION} — provisioned by WebKVM\""
    echo 'echo "Installer by community-scripts (MIT): https://github.com/community-scripts/ProxmoxVE"'
    echo 'if [ -r /etc/os-release ]; then . /etc/os-release; echo "OS: ${PRETTY_NAME:-$NAME}"; fi'
    echo 'echo "IP: $(hostname -I 2>/dev/null | awk "{print \$1}")"'
    echo 'echo ""'
  } >"$f"
  chmod 0644 "$f"
}

# customize: upstream enables root autologin on the container console
# when no password was set, and installs the /usr/bin/update wrapper.
# Both are decisions WebKVM has already made deliberately — forcing
# passwordless root here would quietly undo the credentials the user
# chose, so this only records provenance.
customize() {
  mkdir -p /usr/local/share/webkvm
  printf 'app=%s\nprovisioned_by=webkvm\ninstaller=community-scripts\n' \
    "${APP}" >/usr/local/share/webkvm/provenance
}

# cleanup_lxc exists in core.func, but only when that half loaded. A
# fallback keeps a missing definition from aborting a finished install
# on its very last line.
declare -F cleanup_lxc >/dev/null 2>&1 || cleanup_lxc() {
  if command -v apt-get >/dev/null 2>&1; then
    apt-get -y autoremove >/dev/null 2>&1
    apt-get -y autoclean >/dev/null 2>&1
  fi
}

# Upstream calls these from several helpers; they only mean something
# when a Proxmox host is listening for progress updates.
post_progress_to_api() { :; }
post_update_to_api() { :; }

# FUNCTIONS_FILE_PATH is how an installer re-sources the framework:
#   source /dev/stdin <<<"$FUNCTIONS_FILE_PATH"
# Everything is loaded already, so exporting an empty value turns that
# line into a harmless no-op instead of a syntax error.
FUNCTIONS_FILE_PATH=""
export FUNCTIONS_FILE_PATH
