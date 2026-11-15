#!/bin/sh
# WebKVM optional clustering/HA stack helper.
#
# The base WebKVM install does not include clustering. This helper checks,
# installs the Pacemaker/Corosync/pcs/SBD/fence-agents/resource-agents/
# drbd-utils packages from your distribution, and diagnoses failures. It
# never joins a cluster, formats disks, writes SBD/fencing config or starts
# Pacemaker: those steps are node-specific and stay manual.
#
# --install is fail-closed:
#   1. it records whether each module unit (corosync, pacemaker, pcsd, sbd and
#      the Debian 13 extras pcsd-ruby, logd, sbd_remote; a distro without a
#      unit reports it as not found) is enabled/active BEFORE touching packages;
#   2. it installs the packages (a failure is an error, never ignored);
#   3. on every exit path (success, apt failure, Ctrl-C, kill) it disables and
#      stops ONLY the units that were definitely disabled and inactive before.
#      Units that were enabled/active, or whose state could not be read, are
#      never touched;
#   4. it re-reads the unit states and exits non-zero, printing the exact
#      command, if a unit that was not running before still is.
# Families: apt (Debian/Ubuntu supported, derivatives experimental), dnf
# (Fedora and RHEL-like, experimental: the High Availability repository must be
# enabled by YOU, it is never enabled here) and pacman (Arch-like, partial: only
# packages in core/extra/multilib are installed with "pacman -S --needed"; the
# AUR remainder is printed as commands for a NON-root user and never built
# here; a bare "pacman -Sy" is never run). Equivalent: webkvm-cli ha deps.
#
# Exit codes: 0 ok | 1 --check found missing components | 2 usage/privileges
#   10 distro not installable here (unsupported, experimental not accepted,
#      HA repository not enabled, pacman sync database missing)
#   11 package manager failure (cleanup ran) | 12 units need operator action
#   13 done, but the operator still has to install the AUR remainder
#
#   ./install-ha-stack.sh --check     # default: report what is missing
#   sudo ./install-ha-stack.sh --install --yes
#   sudo ./install-ha-stack.sh --doctor
#   ./install-ha-stack.sh --json --check
set -eu

MODE=check
JSON=0
YES=0
ACCEPT_EXPERIMENTAL=0
LOAD_DRBD=0
WITH_INCUS=0
WITH_DRBD_DKMS=0

usage() {
	cat <<'EOF'
Usage: install-ha-stack.sh [--check|--install|--doctor] [--json] [options]

  --check    Report the state of Incus, DRBD, LINSTOR, Pacemaker, Corosync,
             SBD, drbd-reactor, kernel headers and DKMS. Read-only (default).
  --install  Install pacemaker corosync pcs sbd fence-agents resource-agents
             drbd-utils with apt or dnf; on Arch only the official-repo part.
             Units that were not enabled/active before
             are disabled and stopped again afterwards; pre-existing or
             unreadable units are never touched. Never forms a cluster.
  --doctor   Collect diagnostics for a failed stack (modules, services,
             quorum, ports, watchdog, recent kernel errors).
  --json     Machine-readable output for --check.

Install options:
  --yes, -y               Required when stdin is not a terminal (otherwise the
                          script asks for confirmation).
  --accept-experimental   Allow experimental/partial distributions (apt
                          derivatives, Fedora/RHEL-like, Arch-like).
  --with-incus            Also install the incus package (apt only). Incus is not
                          enabled/started unless it was already enabled.
  --with-drbd-dkms        Also install dkms, kernel headers and drbd-dkms (apt only).
  --load-drbd             Run "modprobe drbd" afterwards (never done otherwise).

LINSTOR is not in distribution repositories and is never installed here.
EOF
}

for arg in "$@"; do
	case "$arg" in
	--check) MODE=check ;;
	--install) MODE=install ;;
	--doctor) MODE=doctor ;;
	--json) JSON=1 ;;
	--yes | -y) YES=1 ;;
	--accept-experimental) ACCEPT_EXPERIMENTAL=1 ;;
	--load-drbd) LOAD_DRBD=1 ;;
	--with-incus) WITH_INCUS=1 ;;
	--with-drbd-dkms) WITH_DRBD_DKMS=1 ;;
	-h | --help) usage; exit 0 ;;
	*) echo "unknown argument: $arg" >&2; usage >&2; exit 2 ;;
	esac
done

[ "$MODE" = check ] || [ "$(id -u)" -eq 0 ] || {
	echo "run --install/--doctor as root" >&2
	exit 2
}

DISTRO=unknown
PM=unknown
ID=
ID_LIKE=
OS_RELEASE_FILE=${OS_RELEASE_FILE:-/etc/os-release}
if [ -r "$OS_RELEASE_FILE" ]; then
	# shellcheck disable=SC1090
	. "$OS_RELEASE_FILE"
	case " ${ID:-} ${ID_LIKE:-} " in
	*debian* | *ubuntu*) DISTRO=debian; PM=apt ;;
	*fedora* | *rhel* | *centos* | *almalinux* | *rocky*) DISTRO=rhel; PM=dnf ;;
	*arch*) DISTRO=arch; PM=pacman ;;
	esac
fi

# TIER drives --install and is the same as the Go profile
# (backend/internal/hainstall/distro.go): Ubuntu >= 22 and Debian >= 12 are
# supported (older releases unsupported); other apt derivatives and dnf need
# --accept-experimental; pacman is "partial" (the AUR part stays manual).
VERSION_MAJOR=${VERSION_ID:-}
VERSION_MAJOR=${VERSION_MAJOR%%.*}
case "$VERSION_MAJOR" in
'' | *[!0-9]*) VERSION_MAJOR=-1 ;;
esac
case "$PM:${ID:-}" in
apt:ubuntu) if [ "$VERSION_MAJOR" -ge 22 ]; then TIER=supported; else TIER=unsupported; fi ;;
apt:debian) if [ "$VERSION_MAJOR" -ge 12 ]; then TIER=supported; else TIER=unsupported; fi ;;
apt:* | dnf:*) TIER=experimental ;;
pacman:*) TIER=partial ;;
*) TIER=unsupported ;;
esac
SUPPORT=$TIER
FAMILY=$PM

have() { command -v "$1" >/dev/null 2>&1; }
unit_installed() { systemctl cat "$1.service" >/dev/null 2>&1; }
unit_active() { systemctl is-active --quiet "$1" 2>/dev/null; }

# --- unit state helpers (used by --install) -------------------------------
MOD_UNITS="corosync pacemaker pcsd sbd pcsd-ruby logd sbd_remote"
INSTALL_STARTED=0
RESTORED=0
PRE=

# unit_query VERB UNIT prints one definite systemd state, or "unknown" for a
# timeout, a missing systemctl, an unreadable bus or any unexpected answer.
unit_query() {
	_rc=0
	if have timeout; then
		_out=$(timeout 10 systemctl "$1" "$2" 2>&1) || _rc=$?
	else
		_out=$(systemctl "$1" "$2" 2>&1) || _rc=$?
	fi
	case "$_rc" in 124 | 126 | 127) echo unknown; return 0 ;; esac
	_state=$(printf '%s\n' "$_out" | sed -n '1p')
	case "$1:$_state" in
	is-enabled:*"No such file or directory"* | is-enabled:not-found) echo not-found ;;
	is-enabled:enabled | is-enabled:enabled-runtime | is-enabled:linked | is-enabled:linked-runtime | \
		is-enabled:masked | is-enabled:masked-runtime | is-enabled:static | is-enabled:indirect | \
		is-enabled:disabled | is-enabled:generated | is-enabled:transient | is-enabled:alias | is-enabled:bad)
		echo "$_state" ;;
	is-active:active | is-active:inactive | is-active:failed | is-active:activating | \
		is-active:deactivating | is-active:reloading | is-active:maintenance)
		echo "$_state" ;;
	*) echo unknown ;;
	esac
}

# restore_units disables (no --now) and stops ONLY the units recorded as
# definitely disabled+inactive before the install ("R"). Idempotent; a no-op
# before the install started.
restore_units() {
	[ "$INSTALL_STARTED" = 1 ] && [ "$RESTORED" = 0 ] || return 0
	RESTORED=1
	for u in $MOD_UNITS; do
		[ "$(cat "$PRE/$u" 2>/dev/null || true)" = R ] || continue
		[ "$(unit_query is-enabled "$u.service")" != not-found ] || continue
		echo "restoring $u.service to disabled/stopped"
		systemctl disable "$u.service" || echo "warning: systemctl disable $u.service failed" >&2
		case $(unit_query is-active "$u.service") in
		active | activating | reloading | unknown)
			systemctl stop "$u.service" || echo "warning: systemctl stop $u.service failed" >&2
			;;
		esac
	done
}


TMP=$(mktemp -d "${TMPDIR:-/tmp}/webkvm-ha.XXXXXX")
on_exit() {
	_status=$?
	trap - EXIT INT TERM
	restore_units
	rm -rf "$TMP"
	exit "$_status"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
RESULTS="$TMP/results"
: >"$RESULTS"

add_result() { # id state detail
	printf '%s\t%s\t%s\n' "$1" "$2" "$3" >>"$RESULTS"
}

# --- probes ---------------------------------------------------------------

if have incus; then
	if incus info >/dev/null 2>&1; then
		add_result incus ok "$(incus version 2>/dev/null | head -1)"
	else
		add_result incus inactive "binary present, daemon not reachable"
	fi
else
	add_result incus missing "install incus"
fi

if lsmod 2>/dev/null | grep -q '^drbd'; then
	add_result drbd_module ok "loaded"
elif modinfo drbd >/dev/null 2>&1; then
	add_result drbd_module inactive "available, not loaded (modprobe drbd)"
else
	add_result drbd_module missing "no drbd module for $(uname -r)"
fi

if have drbdadm && drbdadm --version >/dev/null 2>&1; then
	add_result drbd_utils ok "drbdadm present"
else
	add_result drbd_utils missing "install drbd-utils"
fi

if unit_installed linstor-satellite; then
	if unit_active linstor-satellite; then
		add_result linstor_satellite ok "active"
	else
		add_result linstor_satellite inactive "installed, not running"
	fi
else
	add_result linstor_satellite missing "install LINSTOR from LINBIT's repository"
fi

if have pcs; then
	add_result pacemaker ok "pcs $(pcs --version 2>/dev/null | head -1)"
else
	add_result pacemaker missing "install pacemaker and pcs"
fi

# any_pkg_installed NAME...: true when one of the package names is installed
# (read-only query of the node's own package manager).
any_pkg_installed() {
	for _p in "$@"; do
		case "$PM" in
		apt) dpkg-query -W -f='${Status}' "$_p" 2>/dev/null | grep -q 'install ok installed' && return 0 ;;
		dnf) rpm -q "$_p" >/dev/null 2>&1 && return 0 ;;
		pacman) pacman -Q "$_p" >/dev/null 2>&1 && return 0 ;;
		esac
	done
	return 1
}

if any_pkg_installed fence-agents-base fence-agents-all fence-agents-common fence-agents; then
	add_result fence_agents ok "installed"
else
	add_result fence_agents missing "install fence-agents"
fi

if any_pkg_installed resource-agents-base resource-agents; then
	add_result resource_agents ok "installed"
else
	add_result resource_agents missing "install resource-agents"
fi

if unit_installed corosync; then
	add_result corosync ok "installed"
else
	add_result corosync missing "install corosync"
fi

if unit_installed sbd; then
	if unit_active sbd; then
		add_result sbd ok "active"
	else
		add_result sbd inactive "installed, inactive until the cluster starts"
	fi
else
	add_result sbd missing "install sbd"
fi

if [ -e /dev/watchdog ]; then
	add_result watchdog ok "/dev/watchdog present"
else
	add_result watchdog missing "no /dev/watchdog (needed for SBD watchdog fencing)"
fi

if unit_installed drbd-reactor; then
	add_result drbd_reactor ok "installed"
else
	add_result drbd_reactor missing "install drbd-reactor (LINSTOR controller HA)"
fi

if [ -d "/lib/modules/$(uname -r)/build" ]; then
	add_result kernel_headers ok "$(uname -r)"
else
	add_result kernel_headers missing "install linux-headers-$(uname -r)"
fi

if have dkms && dkms status 2>/dev/null | grep -qi drbd; then
	add_result dkms_drbd ok "$(dkms status 2>/dev/null | grep -i drbd | head -1)"
else
	add_result dkms_drbd inactive "no DRBD build registered in DKMS"
fi

# --- output ---------------------------------------------------------------

print_table() {
	printf '%-20s %-10s %s\n' COMPONENT STATE DETAIL
	while IFS='	' read -r id state detail; do
		printf '%-20s %-10s %s\n' "$id" "$state" "$detail"
	done <"$RESULTS"
}

print_json() {
	printf '{"distribution":"%s","family":"%s","id":"%s","version":"%s","support":"%s","kernel":"%s","checks":[' \
		"$DISTRO" "$FAMILY" "$(printf '%s' "${ID:-unknown}" | tr -cd 'A-Za-z0-9._-')" \
		"$(printf '%s' "${VERSION_ID:-}" | tr -cd 'A-Za-z0-9._-')" "$SUPPORT" "$(uname -r)"
	first=1
	while IFS='	' read -r id state detail; do
		detail=$(printf '%s' "$detail" | tr '"\\' "'/" | tr '\n' ' ')
		[ "$first" = 1 ] || printf ','
		first=0
		printf '{"id":"%s","state":"%s","detail":"%s"}' "$id" "$state" "$detail"
	done <"$RESULTS"
	printf ']}\n'
}

# The HA stack is what --install provides. Everything else (incus, DRBD
# module, kernel headers, drbd-reactor, LINSTOR, watchdog, DKMS) is optional
# and must not turn --check into a failure.
CORE_IDS=" pacemaker corosync sbd fence_agents resource_agents drbd_utils "
missing=0
core_missing=0
core_list=
inactive=0
while IFS='	' read -r _id state _detail; do
	case "$state" in
	missing)
		missing=$((missing + 1))
		case "$CORE_IDS" in
		*" $_id "*) core_missing=$((core_missing + 1)); core_list="$core_list $_id" ;;
		esac
		;;
	inactive) inactive=$((inactive + 1)) ;;
	esac
done <"$RESULTS"

if [ "$JSON" = 1 ] && [ "$MODE" = check ]; then
	print_json
	[ "$core_missing" -eq 0 ] || exit 1
	exit 0
fi

case "$MODE" in
check)
	print_table
	printf '\n'
	if [ "$core_missing" -eq 0 ]; then
		echo "The HA stack is installed. LINSTOR cluster setup is still manual."
		[ "$missing" -eq 0 ] || echo "$missing optional component(s) are missing (incus, DRBD module, headers, drbd-reactor, LINSTOR); the HA stack does not need them."
		[ "$inactive" -eq 0 ] || echo "Some units are inactive; --doctor shows why."
		exit 0
	fi
	echo "$core_missing HA component(s) missing:$core_list. Run: sudo $0 --install"
	echo "LINSTOR is not in distribution repositories; add LINBIT's repo first."
	exit 1
	;;
install)
	print_table
	case "$TIER" in
	supported) ;;
	experimental | partial)
		if [ "$ACCEPT_EXPERIMENTAL" != 1 ]; then
			echo "$DISTRO (${ID:-unknown}, $PM) is $TIER: re-run with --accept-experimental." >&2
			exit 10
		fi
		;;
	*)
		echo "Unsupported distribution. Install the components listed by --check by hand." >&2
		exit 10
		;;
	esac

	if [ "$PM" != apt ] && { [ "$WITH_INCUS" = 1 ] || [ "$WITH_DRBD_DKMS" = 1 ]; }; then
		echo "note: --with-incus/--with-drbd-dkms only apply to apt; install Incus and DRBD by hand on $PM." >&2
	fi

	if [ "$YES" != 1 ]; then
		if [ -t 0 ]; then
			printf 'Install the HA packages with %s now? [y/N] ' "$PM"
			read -r answer || answer=n
			case "$answer" in y | Y | yes) ;; *) echo "aborted"; exit 2 ;; esac
		else
			echo "refusing to install without a terminal: pass --yes" >&2
			exit 2
		fi
	fi

	# --- per-family pre-checks (nothing is changed yet) ---------------------
	PACMAN_SYNC_DIR=${PACMAN_SYNC_DIR:-/var/lib/pacman/sync}
	case "$PM" in
	dnf)
		if [ "${ID:-}" != fedora ]; then
			# RHEL-like: the HighAvailability repository is not enabled by default.
			major=${VERSION_ID%%.*}
			case "${ID:-}" in
			rhel) ha_repo="rhel-${major}-for-$(uname -m)-highavailability-rpms"
				ha_cmd="sudo subscription-manager repos --enable $ha_repo" ;;
			rocky) if [ "$major" = 8 ]; then ha_repo=ha; else ha_repo=highavailability; fi
				ha_cmd="sudo dnf config-manager --set-enabled $ha_repo" ;;
			ol) ha_repo="ol${major}_addons"; ha_cmd="sudo dnf config-manager --set-enabled $ha_repo" ;;
			*) ha_repo=highavailability; ha_cmd="sudo dnf config-manager --set-enabled $ha_repo" ;;
			esac
			if ! dnf -q repolist 2>/dev/null | awk -v want="$ha_repo" 'NR>1 { id=tolower($1); if (id ~ /highavailability/ || $1 == want) found=1 } END { exit found ? 0 : 1 }'; then
				echo "The High Availability repository is not enabled (expected: $ha_repo)." >&2
				echo "WebKVM never enables repositories. Enable it yourself, then re-run:" >&2
				[ "${ID:-}" = rhel ] || echo "  sudo dnf install -y dnf-plugins-core   # provides 'dnf config-manager'" >&2
				echo "  $ha_cmd" >&2
				echo "  # the id can differ on your release: dnf repolist --all" >&2
				exit 10
			fi
		fi
		;;
	pacman)
		if ! ls "$PACMAN_SYNC_DIR"/*.db >/dev/null 2>&1; then
			echo "The pacman sync databases are missing. Upgrade the whole system first (WebKVM never runs a bare -Sy):" >&2
			echo "  sudo pacman -Syu" >&2
			exit 10
		fi
		;;
	esac

	# --- resolve package names (nothing is changed yet) ---------------------
	pkg_installed() {
		case "$PM" in
		apt) dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q 'install ok installed' ;;
		dnf) rpm -q "$1" >/dev/null 2>&1 ;;
		pacman) pacman -Q "$1" >/dev/null 2>&1 ;;
		esac
	}
	pkg_candidate() {
		case "$PM" in
		apt) apt-cache policy "$1" 2>/dev/null | awk '/^ *Candidate:/ { if ($2 != "(none)" && $2 != "") ok = 1 } END { exit ok ? 0 : 1 }' ;;
		dnf)
			_cand=$(dnf -q repoquery --available "$1" 2>/dev/null) || _cand=$(dnf -q list --available "$1" 2>/dev/null | sed -n '2p')
			[ -n "$_cand" ] ;;
		pacman) pacman_repo "$1" >/dev/null ;;
		esac
	}
	# pacman_repo prints the OFFICIAL repo a package comes from; fails for the
	# AUR, third-party repos and unknown packages (those are the operator's).
	pacman_repo() {
		_repo=$(pacman -Si "$1" 2>/dev/null | sed -n 's/^Repository *: *//p' | head -1)
		case "$_repo" in core | extra | multilib) echo "$_repo" ;; *) return 1 ;; esac
	}
	resolve_pkg() { # logical -> real name on stdout; empty if unavailable
		case "$1" in
		fence-agents) [ "$PM" = apt ] && alts="fence-agents-base fence-agents" || alts="$1" ;;
		resource-agents) [ "$PM" = apt ] && alts="resource-agents-base resource-agents" || alts="$1" ;;
		*) alts="$1" ;;
		esac
		for alt in $alts; do
			if pkg_installed "$alt"; then
				echo "$alt"
				return 0
			fi
		done
		for alt in $alts; do
			if pkg_candidate "$alt"; then
				echo "$alt"
				return 0
			fi
		done
		return 0
	}
	case "$PM" in
	apt) logical="pacemaker corosync pcs sbd fence-agents resource-agents drbd-utils" ;;
	dnf) logical="pacemaker corosync pcs sbd fence-agents-all resource-agents drbd-utils" ;;
	pacman) logical="corosync resource-agents fence-agents sbd pacemaker pcs" ;;
	esac
	if [ "$PM" = apt ]; then
		[ "$WITH_INCUS" != 1 ] || logical="$logical incus"
		[ "$WITH_DRBD_DKMS" != 1 ] || logical="$logical dkms linux-headers-$(uname -r) drbd-dkms"
	fi
	MISSING=
	UNAVAILABLE=
	GUIDED=
	for pkg in $logical; do
		real=$(resolve_pkg "$pkg")
		if [ -z "$real" ]; then
			if [ "$PM" = pacman ]; then
				GUIDED="$GUIDED $pkg" # AUR / third party: the operator builds it
			elif [ "$PM" = dnf ] && [ "$pkg" = drbd-utils ]; then
				# Rocky/Alma/RHEL do not package it (Fedora does): only DRBD needs it.
				echo "note: drbd-utils is not in the enabled repositories (vendor/ELRepo/LINBIT); skipping it. Only DRBD needs it." >&2
			else
				UNAVAILABLE="$UNAVAILABLE $pkg"
			fi
		elif ! pkg_installed "$real"; then
			MISSING="$MISSING $real"
		fi
	done
	if [ -n "$UNAVAILABLE" ]; then
		echo "no installable candidate in the enabled repositories for:$UNAVAILABLE" >&2
		echo "Nothing was changed. Check your repositories (apt-get update / dnf makecache)." >&2
		exit 11
	fi

	# print_guided lists what the operator must install (Arch: AUR packages).
	print_guided() {
		echo
		echo "WebKVM does NOT install these (AUR / not in an official repository):$GUIDED"
		echo "Run them as a NORMAL user, never as root, and review every PKGBUILD first:"
		echo "  yay -S --needed$GUIDED"
		echo "  # or: paru -S --needed$GUIDED"
		echo "  # or by hand, in this order:"
		for g in $GUIDED; do
			echo "  git clone https://aur.archlinux.org/$g.git && cd $g && less PKGBUILD && makepkg -si && cd .."
		done
		echo "Then re-run: $0 --check (or 'Verify' in the WebKVM Nodes page)."
	}

	if [ -z "$MISSING" ]; then
		if [ -n "$GUIDED" ]; then
			print_guided
			exit 13
		fi
		echo "All HA packages are already installed; nothing to do and no unit was touched."
		echo "Re-check with: $0 --check"
		exit 0
	fi

	# --- (1) snapshot unit state BEFORE any package is installed -----------
	PRE="$TMP/pre"
	mkdir -p "$PRE"
	for u in $MOD_UNITS; do
		en=$(unit_query is-enabled "$u.service")
		ac=$(unit_query is-active "$u.service")
		if [ "$en" = unknown ] || [ "$ac" = unknown ]; then
			echo U >"$PRE/$u"
			echo "warning: cannot read the state of $u.service; it will not be touched" >&2
		elif { [ "$en" = disabled ] || [ "$en" = not-found ]; } && { [ "$ac" = inactive ] || [ "$ac" = failed ]; }; then
			echo R >"$PRE/$u"
		else
			echo P >"$PRE/$u"
		fi
		echo "  $u.service before: $en/$ac"
	done
	INCUS_WAS_ENABLED=0
	[ "$(unit_query is-enabled incus.service)" != enabled ] || INCUS_WAS_ENABLED=1

	# --- (2) install; any exit from here on triggers the restore ----------
	INSTALL_STARTED=1
	printf '\nInstalling distribution packages (%s):%s\n' "$PM" "$MISSING"
	APT_FAILED=0
	case "$PM" in
	apt)
		export DEBIAN_FRONTEND=noninteractive
		apt-get update || APT_FAILED=1
		if [ "$APT_FAILED" = 0 ]; then
			# shellcheck disable=SC2086
			apt-get install -y --no-install-recommends $MISSING || APT_FAILED=1
		fi
		;;
	dnf)
		# shellcheck disable=SC2086
		dnf install -y --setopt=install_weak_deps=False $MISSING || APT_FAILED=1
		;;
	pacman)
		# -S --needed only: never -Sy (partial upgrades), never -u.
		# shellcheck disable=SC2086
		pacman -S --needed --noconfirm $MISSING || APT_FAILED=1
		;;
	esac

	# --- (3) restore, (4) verify ------------------------------------------
	restore_units
	BAD=
	UNKNOWN_BAD=
	for u in $MOD_UNITS; do
		kind=$(cat "$PRE/$u")
		[ "$kind" != P ] || continue
		en=$(unit_query is-enabled "$u.service")
		ac=$(unit_query is-active "$u.service")
		hot=0
		case "$en" in enabled | enabled-runtime) hot=1 ;; esac
		case "$ac" in active | activating | reloading) hot=1 ;; esac
		if [ "$kind" = R ] && { [ "$en" = unknown ] || [ "$ac" = unknown ]; }; then
			hot=1
		fi
		[ "$hot" = 1 ] || continue
		if [ "$kind" = U ]; then
			UNKNOWN_BAD="$UNKNOWN_BAD $u.service"
		else
			BAD="$BAD $u.service"
		fi
	done

	RC=0
	if [ "$APT_FAILED" = 1 ]; then
		echo "error: package installation failed (units were restored where it was safe)." >&2
		echo "Fix the $PM error and re-run: sudo $0 --install --yes" >&2
		[ "$PM" != pacman ] || echo "If packages were not found, upgrade the system first: sudo pacman -Syu" >&2
		RC=11
	fi
	if [ -n "$BAD" ]; then
		echo "error: units that were not running before are still enabled/active:$BAD" >&2
		echo "Run: sudo systemctl disable --now$BAD" >&2
		RC=12
	fi
	if [ -n "$UNKNOWN_BAD" ]; then
		echo "error: units whose earlier state was unknown are now enabled/active:$UNKNOWN_BAD" >&2
		echo "Stop only if they are not part of a running cluster: sudo systemctl disable --now$UNKNOWN_BAD" >&2
		RC=12
	fi
	[ "$RC" -eq 0 ] || exit "$RC"
	if [ -n "$GUIDED" ]; then
		print_guided
		exit 13
	fi

	if [ "$WITH_INCUS" = 1 ] && have incus; then
		if [ "$INCUS_WAS_ENABLED" = 1 ]; then
			systemctl enable --now incus || echo "could not enable incus; run: sudo systemctl enable --now incus" >&2
		else
			echo "Incus is installed but not enabled. When you want it: sudo systemctl enable --now incus"
		fi
	fi
	if [ "$LOAD_DRBD" = 1 ]; then
		modprobe drbd || echo "modprobe drbd failed; --doctor explains the usual causes." >&2
	else
		echo "DRBD module not loaded (reboot, or: sudo modprobe drbd, or re-run with --load-drbd)."
	fi
	echo
	echo "Re-check with: $0 --check"
	echo "Not done on purpose: cluster join, SBD/fencing config, Pacemaker start."
	exit 0
	;;
doctor)
	echo "Host: $(uname -a)"
	echo "Distribution: $DISTRO ($PM)"
	[ -r /sys/firmware/efi ] && echo "Firmware: UEFI"
	have mokutil && mokutil --sb-state 2>/dev/null || true
	echo
	print_table
	echo
	echo "--- services"
	for unit in incus pacemaker corosync sbd linstor-satellite drbd-reactor; do
		printf '%-16s %s\n' "$unit" "$(systemctl is-active "$unit" 2>/dev/null || true)"
	done
	echo
	echo "--- DRBD"
	have drbdsetup && drbdsetup status 2>&1 | head -40 || echo "drbdsetup not installed"
	have dkms && dkms status 2>/dev/null | grep -i drbd || true
	echo
	echo "--- Pacemaker"
	have pcs && timeout 10 pcs status 2>&1 | head -25 || echo "pcs not installed"
	have corosync-quorumtool && corosync-quorumtool -s 2>&1 | head -8 || true
	echo
	echo "--- LINSTOR"
	have linstor && timeout 10 linstor node list 2>&1 | head -10 || echo "linstor CLI not installed"
	echo
	echo "--- ports (5405 corosync, 2224 pcsd, 3370 LINSTOR, 8443 Incus)"
	have ss && ss -lntup 2>/dev/null | grep -E ':(5405|2224|3370|8443)\b' || true
	echo
	echo "--- watchdog"
	ls -l /dev/watchdog* 2>/dev/null || echo "no /dev/watchdog"
	echo
	echo "--- recent kernel messages"
	dmesg 2>/dev/null | grep -iE 'drbd|watchdog|edac|mce' | tail -10 || true
	cat <<'EOF'

Common fixes:
  DRBD missing after a kernel update   -> dkms autoinstall && reboot
  Secure Boot refuses DRBD             -> enroll the DKMS MOK key (mokutil --import)
  No quorum                            -> corosync-cfgtool -s; pcs quorum status
  LINSTOR satellite offline            -> controller VIP reachable? port 3370; linstor node list
  SBD inactive                         -> normal until the cluster starts; check /dev/watchdog
  Incus pool unavailable on a member   -> incus cluster list; check the member's storage
EOF
	exit 0
	;;
esac
