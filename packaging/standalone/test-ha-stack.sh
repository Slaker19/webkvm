#!/usr/bin/env bash
# Hermetic test for scripts/install-ha-stack.sh --install. Every external
# command (apt-get, dpkg-query, apt-cache, systemctl, id, modprobe, ...) is a
# stub found first on PATH, so nothing touches the host: no package manager,
# no systemd, no kernel module. Does not require root.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/scripts/install-ha-stack.sh"

sh -n "${SCRIPT}"
if command -v shellcheck >/dev/null 2>&1; then
	shellcheck "${SCRIPT}"
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/webkvm-ha-test.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT
BIN="${WORK}/bin"
STATE="${WORK}/state"
LOG="${WORK}/calls.log"
mkdir -p "${BIN}"

write_stub() { # name, body
	printf '#!/bin/sh\n%s\n' "$2" >"${BIN}/$1"
	chmod 0755 "${BIN}/$1"
}

# Stubs record every call in $LOG and read/write fake state in $STATE.
write_stub id 'echo 0'
write_stub uname 'case "$1" in -r) echo 6.8.0-test ;; *) echo Linux ;; esac'
write_stub modprobe 'echo "modprobe $*" >>"$STUB_LOG"'
write_stub dpkg-query 'for last; do :; done
[ -e "$STUB_STATE/installed.$last" ] && printf "install ok installed" || exit 1'
write_stub apt-cache 'for last; do :; done
printf "%s:\n  Installed: (none)\n  Candidate: 1.0\n" "$last"'
write_stub apt-get '
echo "apt-get $*" >>"$STUB_LOG"
case "$1" in
update) exit 0 ;;
install)
	[ -e "$STUB_STATE/postinst_enable_corosync" ] && touch "$STUB_STATE/enabled.corosync.service"
	[ -e "$STUB_STATE/postinst_start_corosync" ] && touch "$STUB_STATE/active.corosync.service"
	[ -e "$STUB_STATE/postinst_enable_sbd" ] && touch "$STUB_STATE/enabled.sbd.service"
	for f in "$STUB_STATE"/postinst_enableunit.*; do
		[ -e "$f" ] || continue
		touch "$STUB_STATE/enabled.${f##*/postinst_enableunit.}"
	done
	for f in "$STUB_STATE"/postinst_startunit.*; do
		[ -e "$f" ] || continue
		touch "$STUB_STATE/active.${f##*/postinst_startunit.}"
	done
	[ -e "$STUB_STATE/fail_install" ] && { echo "E: Unable to locate package" >&2; exit 100; }
	for a in "$@"; do
		case "$a" in -*|install) ;; *) touch "$STUB_STATE/installed.$a" ;; esac
	done
	exit 0 ;;
esac'
# rpm/dnf/pacman stubs share the same fake state (installed.<pkg>).
write_stub rpm 'for last; do :; done
[ -e "$STUB_STATE/installed.$last" ] || exit 1'
write_stub dnf '
echo "dnf $*" >>"$STUB_LOG"
[ "$1" != -q ] || shift
case "$1" in
repolist) echo "repo id repo name"; cat "$STUB_STATE/repos" 2>/dev/null; exit 0 ;;
repoquery) for last; do :; done; [ -e "$STUB_STATE/nocandidate.$last" ] || echo "$last-1.0-1"; exit 0 ;;
install)
	[ -e "$STUB_STATE/postinst_enable_corosync" ] && touch "$STUB_STATE/enabled.corosync.service"
	[ -e "$STUB_STATE/postinst_start_corosync" ] && touch "$STUB_STATE/active.corosync.service"
	[ -e "$STUB_STATE/fail_install" ] && { echo "Error: Unable to find a match" >&2; exit 1; }
	for a in "$@"; do
		case "$a" in -*|install) ;; *) touch "$STUB_STATE/installed.$a" ;; esac
	done
	exit 0 ;;
esac
exit 0'
write_stub pacman '
echo "pacman $*" >>"$STUB_LOG"
for last; do :; done
case "$1" in
-Q) [ -e "$STUB_STATE/installed.$last" ] || exit 1 ;;
-Si) [ -e "$STUB_STATE/official.$last" ] || exit 1
	printf "Repository      : %s\nName            : %s\n" "$(cat "$STUB_STATE/official.$last")" "$last" ;;
-S)
	[ -e "$STUB_STATE/postinst_enable_corosync" ] && touch "$STUB_STATE/enabled.corosync.service"
	[ -e "$STUB_STATE/postinst_start_corosync" ] && touch "$STUB_STATE/active.corosync.service"
	[ -e "$STUB_STATE/fail_install" ] && { echo "error: target not found" >&2; exit 1; }
	for a in "$@"; do
		case "$a" in -*) ;; *) touch "$STUB_STATE/installed.$a" ;; esac
	done ;;
*) exit 1 ;;
esac
exit 0'
for banned in yay paru makepkg git; do
	write_stub "$banned" 'echo "'"$banned"' $*" >>"$STUB_LOG"; exit 1'
done
write_stub systemctl '
echo "systemctl $*" >>"$STUB_LOG"
verb=$1
quiet=0
if [ "$2" = --quiet ]; then quiet=1; shift; fi
unit=$2
[ "$quiet" = 0 ] || exec >/dev/null 2>&1
case "$verb" in
is-enabled)
	if [ -e "$STUB_STATE/unknown.$unit" ]; then echo "Failed to connect to bus" >&2; exit 1; fi
	if [ -e "$STUB_STATE/enabled.$unit" ]; then echo enabled; exit 0; fi
	# systemd >= 256 answers a not-yet-installed unit with the bare word.
	if [ -e "$STUB_STATE/notfound.$unit" ]; then echo not-found; exit 4; fi
	echo disabled; exit 1 ;;
is-active)
	if [ -e "$STUB_STATE/unknown.$unit" ]; then echo "Failed to connect to bus" >&2; exit 1; fi
	if [ -e "$STUB_STATE/active.$unit" ]; then echo active; exit 0; fi
	if [ -e "$STUB_STATE/failed.$unit" ]; then echo failed; exit 3; fi
	echo inactive; exit 3 ;;
disable)
	shift
	for u; do [ -e "$STUB_STATE/sticky.$u" ] || rm -f "$STUB_STATE/enabled.$u"; done
	exit 0 ;;
stop)
	shift
	for u; do [ -e "$STUB_STATE/sticky.$u" ] || rm -f "$STUB_STATE/active.$u"; done
	exit 0 ;;
esac
exit 0'

fail() { echo "FAIL: $*" >&2; echo "--- calls:" >&2; cat "${LOG}" >&2 || true; exit 1; }
called() { grep -Eq -- "$1" "${LOG}"; }

# run_install <tag> [flags...]: fresh state each time; the test sets the
# fixture by creating files in $STATE before calling it.
run_install() {
	OUT="${WORK}/out.$1"
	shift
	set +e
	PATH="${BIN}:${PATH}" STUB_LOG="${LOG}" STUB_STATE="${STATE}" \
		OS_RELEASE_FILE="${WORK}/os-release" PACMAN_SYNC_DIR="${STATE}/sync" \
		sh "${SCRIPT}" --install --yes "$@" >"${OUT}" 2>&1 </dev/null
	RC=$?
	set -e
}

reset() {
	rm -rf "${STATE}" "${LOG}"
	mkdir -p "${STATE}"
	: >"${LOG}"
	printf 'ID=ubuntu\nVERSION_ID="24.04"\n' >"${WORK}/os-release"
}

# 1. Fresh node whose postinst enables+starts corosync and enables sbd: the
#    units that were off are disabled and stopped again, exit 0.
reset
touch "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync" "${STATE}/postinst_enable_sbd"
run_install fresh
[ "${RC}" -eq 0 ] || fail "fresh install exit ${RC}: $(cat "${OUT}")"
called 'systemctl disable corosync.service' || fail "new corosync was not disabled"
called 'systemctl stop corosync.service' || fail "new corosync was not stopped"
called 'systemctl disable sbd.service' || fail "new sbd was not disabled"
[ ! -e "${STATE}/enabled.corosync.service" ] || fail "corosync still enabled"
[ ! -e "${STATE}/active.corosync.service" ] || fail "corosync still active"
called 'systemctl (disable|stop) .*--now' && fail "disable --now must not be used by the installer"
called 'systemctl (enable|start)' && fail "installer enabled or started a unit"
called 'modprobe' && fail "modprobe ran without --load-drbd"
called 'mask' && fail "a command mentions mask"
# The snapshot precedes the first apt-get call.
first_query=$(grep -n 'systemctl is-enabled' "${LOG}" | head -1 | cut -d: -f1)
first_apt=$(grep -n 'apt-get' "${LOG}" | head -1 | cut -d: -f1)
[ "${first_query}" -lt "${first_apt}" ] || fail "unit snapshot must precede apt-get"

# 1b. Real systemd (Fedora 44, v259) prints "not-found" for a unit that does not
#     exist yet: that is a definite state, so the new unit is restored.
reset
touch "${STATE}/notfound.corosync.service" "${STATE}/notfound.sbd.service"
touch "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync" "${STATE}/postinst_enable_sbd"
run_install notfound
[ "${RC}" -eq 0 ] || fail "not-found install exit ${RC}: $(cat "${OUT}")"
grep -q 'cannot read the state' "${OUT}" && fail "not-found was treated as unknown"
called 'systemctl disable corosync.service' || fail "not-found corosync was not restored"
called 'systemctl stop corosync.service' || fail "not-found corosync was not stopped"
called 'systemctl disable sbd.service' || fail "not-found sbd was not restored"

# 1c. A leftover "failed" state (an earlier purge) is not running: the unit a
#     postinst enables afterwards is still restored.
reset
touch "${STATE}/failed.sbd.service" "${STATE}/postinst_enable_sbd"
run_install failedleft
[ "${RC}" -eq 0 ] || fail "failed-leftover install exit ${RC}: $(cat "${OUT}")"
called 'systemctl disable sbd.service' || fail "sbd with a failed leftover was not restored"

# 2. Pre-enabled and active corosync (existing cluster node) is never touched.
reset
touch "${STATE}/enabled.corosync.service" "${STATE}/active.corosync.service"
touch "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync"
run_install precluster
[ "${RC}" -eq 0 ] || fail "existing-cluster install exit ${RC}: $(cat "${OUT}")"
called 'systemctl (disable|stop) corosync' && fail "pre-existing corosync was touched"
[ -e "${STATE}/enabled.corosync.service" ] && [ -e "${STATE}/active.corosync.service" ] || fail "corosync state changed"
called 'systemctl disable pacemaker.service' || fail "new pacemaker was not restored"

# 3. apt failure: non-zero exit AND the cleanup ran.
reset
touch "${STATE}/fail_install" "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync"
run_install aptfail
[ "${RC}" -eq 11 ] || fail "apt failure must exit 11, got ${RC}"
called 'systemctl disable corosync.service' || fail "cleanup did not run after the apt failure"
called 'systemctl stop corosync.service' || fail "cleanup did not stop corosync after the apt failure"
[ ! -e "${STATE}/active.corosync.service" ] || fail "corosync left running after the apt failure"

# 4. Unknown state: the unit is never touched, even if it ends up enabled,
#    and the run reports it with a non-zero exit.
reset
touch "${STATE}/unknown.corosync.service"
run_install unknown
called 'systemctl (disable|stop) corosync' && fail "unknown-state unit was touched"
[ "${RC}" -eq 0 ] || fail "unknown + still unreadable should not fail: ${RC}"
reset
touch "${STATE}/unknown.corosync.service" "${STATE}/postinst_enable_corosync"
write_stub systemctl_after 'exit 0' # placeholder keeps BIN tidy for readers
rm -f "${BIN}/systemctl_after"
# make the unit readable (and enabled) after the install by clearing the
# "unknown" marker in the apt-get stub's postinst
sed -i 's|\[ -e "$STUB_STATE/postinst_enable_corosync" \] \&\& touch|[ -e "$STUB_STATE/postinst_enable_corosync" ] \&\& rm -f "$STUB_STATE/unknown.corosync.service" \&\& touch|' "${BIN}/apt-get"
run_install unknown2
[ "${RC}" -eq 12 ] || fail "unknown unit that became enabled must exit 12, got ${RC}: $(cat "${OUT}")"
called 'systemctl (disable|stop) corosync' && fail "unknown-state unit was touched"
grep -q 'systemctl disable --now' "${OUT}" || fail "exact manual command missing"

# 5. A unit that stays active after the restore is reported with exit 12.
reset
touch "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync" "${STATE}/sticky.corosync.service"
run_install sticky
[ "${RC}" -eq 12 ] || fail "still-active unit must exit 12, got ${RC}"
grep -q 'sudo systemctl disable --now corosync.service' "${OUT}" || fail "exact manual command missing"

# 6. Experimental tiers never install without --accept-experimental.
reset
printf 'ID=fedora\nVERSION_ID=41\n' >"${WORK}/os-release"
run_install fedora-noaccept
[ "${RC}" -eq 10 ] || fail "fedora without the flag must exit 10, got ${RC}"
called 'dnf install|systemctl (disable|stop|enable|start)' && fail "fedora ran an installer without the flag"
reset
printf 'ID=linuxmint\nID_LIKE="ubuntu debian"\nVERSION_ID=22\n' >"${WORK}/os-release"
run_install mint
[ "${RC}" -eq 10 ] || fail "experimental derivative without the flag must exit 10, got ${RC}"

# 6a. Fedora (dnf): base set installed, new unit restored, pre-enabled untouched.
reset
printf 'ID=fedora\nVERSION_ID=41\n' >"${WORK}/os-release"
touch "${STATE}/enabled.pcsd.service" "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync"
run_install fedora --accept-experimental
[ "${RC}" -eq 0 ] || fail "fedora install exit ${RC}: $(cat "${OUT}")"
called 'dnf install -y --setopt=install_weak_deps=False pacemaker corosync pcs sbd fence-agents-all resource-agents drbd-utils' || fail "dnf base set not installed"
called 'apt-get|pacman' && fail "fedora used another package manager"
called 'systemctl disable corosync.service' || fail "dnf: new corosync was not restored"
called 'systemctl (disable|stop) pcsd\.service' && fail "dnf: pre-enabled pcsd was touched"
[ -e "${STATE}/enabled.pcsd.service" ] || fail "dnf: pcsd state changed"
first_query=$(grep -n 'systemctl is-enabled' "${LOG}" | head -1 | cut -d: -f1)
first_dnf=$(grep -n 'dnf install' "${LOG}" | head -1 | cut -d: -f1)
[ "${first_query}" -lt "${first_dnf}" ] || fail "unit snapshot must precede dnf install"

# 6b. dnf failure: exit 11 and the cleanup ran.
reset
printf 'ID=fedora\nVERSION_ID=41\n' >"${WORK}/os-release"
touch "${STATE}/fail_install" "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync"
run_install fedora-fail --accept-experimental
[ "${RC}" -eq 11 ] || fail "dnf failure must exit 11, got ${RC}"
called 'systemctl disable corosync.service' || fail "dnf: cleanup did not run after the failure"
[ ! -e "${STATE}/active.corosync.service" ] || fail "dnf: corosync left running after the failure"

# 6c. RHEL-like without the HighAvailability repo: exact command, nothing changed.
reset
printf 'ID="rocky"\nID_LIKE="rhel centos fedora"\nVERSION_ID="9.4"\n' >"${WORK}/os-release"
printf 'baseos Rocky BaseOS\nappstream Rocky AppStream\n' >"${STATE}/repos"
run_install rocky-norepo --accept-experimental
[ "${RC}" -eq 10 ] || fail "rocky without the HA repo must exit 10, got ${RC}"
grep -q 'sudo dnf config-manager --set-enabled highavailability' "${OUT}" || fail "exact repo command missing"
grep -q 'sudo dnf install -y dnf-plugins-core' "${OUT}" || fail "dnf-plugins-core hint missing"
called 'dnf install|config-manager|systemctl (disable|stop)' && fail "rocky without the repo changed something"
printf 'baseos Rocky BaseOS\nhighavailability Rocky HA\n' >"${STATE}/repos"
: >"${LOG}"
run_install rocky --accept-experimental
[ "${RC}" -eq 0 ] || fail "rocky with the HA repo exit ${RC}: $(cat "${OUT}")"
called 'dnf install -y' || fail "rocky did not install"
called 'config-manager' && fail "the installer must never enable a repository"

# 6c2. Rocky/Alma/RHEL do not package drbd-utils: skipped, never a blocker, and
#      dnf is not asked to install it.
reset
printf 'ID="rocky"\nID_LIKE="rhel centos fedora"\nVERSION_ID="10.0"\n' >"${WORK}/os-release"
printf 'baseos Rocky BaseOS\nhighavailability Rocky HA\n' >"${STATE}/repos"
touch "${STATE}/nocandidate.drbd-utils"
run_install rocky-nodrbd --accept-experimental
[ "${RC}" -eq 0 ] || fail "rocky without drbd-utils exit ${RC}: $(cat "${OUT}")"
called 'dnf install -y .*pacemaker' || fail "rocky without drbd-utils did not install the stack"
called 'dnf install -y .*drbd-utils' && fail "dnf was asked for the missing drbd-utils"

# 6d. Arch, everything in the AUR: nothing installed, guided commands, exit 13.
reset
printf 'ID=arch\n' >"${WORK}/os-release"
mkdir -p "${STATE}/sync" && touch "${STATE}/sync/core.db"
run_install arch-aur --accept-experimental
[ "${RC}" -eq 13 ] || fail "arch with only AUR packages must exit 13, got ${RC}: $(cat "${OUT}")"
grep -q 'yay -S --needed corosync resource-agents fence-agents sbd pacemaker pcs' "${OUT}" || fail "AUR remainder not reported"
grep -q 'NORMAL user, never as root' "${OUT}" || fail "non-root warning missing"
called '^pacman -S ' && fail "arch installed although nothing is official"
called '^(yay|paru|makepkg|git) ' && fail "the installer ran an AUR helper/makepkg"
called 'systemctl (disable|stop)' && fail "arch touched units without installing"

# 6e. Arch with an official part: only that part, --needed, never -Sy; new
#     unit restored, pre-enabled untouched, AUR remainder reported (exit 13).
reset
printf 'ID=cachyos\nID_LIKE=arch\n' >"${WORK}/os-release"
mkdir -p "${STATE}/sync" && touch "${STATE}/sync/core.db"
echo extra >"${STATE}/official.corosync"
echo extra >"${STATE}/official.sbd"
echo chaotic-aur >"${STATE}/official.pcs"
touch "${STATE}/enabled.pacemaker.service" "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync"
run_install arch-mixed --accept-experimental
[ "${RC}" -eq 13 ] || fail "arch mixed must exit 13, got ${RC}: $(cat "${OUT}")"
called '^pacman -S --needed --noconfirm corosync sbd$' || fail "official part not installed with -S --needed"
called '^pacman -Sy|^pacman -Su|^pacman -S.*u ' && fail "pacman -Sy/-Syu must never run"
called 'systemctl disable corosync.service' || fail "pacman: new corosync was not restored"
called 'systemctl (disable|stop) pacemaker' && fail "pacman: pre-enabled pacemaker was touched"
grep -q 'yay -S --needed resource-agents fence-agents pacemaker pcs' "${OUT}" || fail "AUR remainder (incl. third-party repo pcs) not reported"
[ ! -e "${STATE}/installed.pcs" ] || fail "a third-party repo package was installed"

# 6f. pacman failure: exit 11, cleanup ran, -Syu suggested (never executed).
reset
printf 'ID=arch\n' >"${WORK}/os-release"
mkdir -p "${STATE}/sync" && touch "${STATE}/sync/core.db"
echo extra >"${STATE}/official.corosync"
touch "${STATE}/fail_install" "${STATE}/postinst_enable_corosync" "${STATE}/postinst_start_corosync"
run_install arch-fail --accept-experimental
[ "${RC}" -eq 11 ] || fail "pacman failure must exit 11, got ${RC}"
called 'systemctl disable corosync.service' || fail "pacman: cleanup did not run after the failure"
grep -q 'sudo pacman -Syu' "${OUT}" || fail "manual -Syu hint missing"
called '^pacman -Sy' && fail "pacman -Sy ran"

# 6g. Arch without sync databases: guided -Syu, nothing changed.
reset
printf 'ID=arch\n' >"${WORK}/os-release"
run_install arch-nodb --accept-experimental
[ "${RC}" -eq 10 ] || fail "arch without sync db must exit 10, got ${RC}"
grep -q 'sudo pacman -Syu' "${OUT}" || fail "guided -Syu missing"
called '^pacman -S' && fail "pacman ran without a sync database"

# 7. --load-drbd is the only way modprobe runs.
reset
run_install drbd --load-drbd
[ "${RC}" -eq 0 ] || fail "--load-drbd install exit ${RC}"
called 'modprobe drbd' || fail "--load-drbd did not modprobe"

# 8. Incus is never enabled unless it was already enabled.
reset
write_stub incus 'echo 1.0'
run_install incus --with-incus
called 'systemctl enable' && fail "incus enabled although it was not enabled before"
grep -q 'sudo systemctl enable --now incus' "${OUT}" || fail "manual incus step not printed"

# 9. Re-run with everything installed touches nothing.
reset
for p in pacemaker corosync pcs sbd fence-agents-base resource-agents-base drbd-utils; do touch "${STATE}/installed.$p"; done
run_install rerun
[ "${RC}" -eq 0 ] || fail "idempotent re-run exit ${RC}"
called 'apt-get|systemctl (disable|stop|enable|start)' && fail "re-run touched apt or units"

# --check / --json stay valid and read-only.
reset
JSON_OUT="$(PATH="${BIN}:${PATH}" STUB_LOG="${LOG}" STUB_STATE="${STATE}" OS_RELEASE_FILE="${WORK}/os-release" sh "${SCRIPT}" --json --check 2>"${WORK}/json.err" || true)"
printf '%s' "${JSON_OUT}" | python3 -I -c 'import json,sys; d=json.load(sys.stdin); assert d["checks"]' || fail "--json --check is not valid JSON: $(cat "${WORK}/json.err")"

# 7. Debian 13 ships three more enabled units (found on a real trixie, 8.32):
#    pcsd-ruby, logd and sbd_remote are snapshotted and restored too.
reset
printf 'ID=debian\nVERSION_ID="13"\n' >"${WORK}/os-release"
touch "${STATE}/postinst_enableunit.pcsd-ruby.service" "${STATE}/postinst_enableunit.logd.service" \
	"${STATE}/postinst_enableunit.sbd_remote.service" "${STATE}/postinst_startunit.logd.service"
run_install debian13units
[ "${RC}" -eq 0 ] || fail "debian 13 install exit ${RC}: $(cat "${OUT}")"
for u in pcsd-ruby logd sbd_remote; do
	called "systemctl is-enabled ${u}.service" || fail "${u} was not snapshotted"
	called "systemctl disable ${u}.service" || fail "${u} was not restored"
	[ ! -e "${STATE}/enabled.${u}.service" ] || fail "${u} still enabled"
done
called 'systemctl stop logd.service' || fail "running logd was not stopped"
[ ! -e "${STATE}/active.logd.service" ] || fail "logd still active"
# A new unit that was already on before is protected.
reset
printf 'ID=debian\nVERSION_ID="13"\n' >"${WORK}/os-release"
touch "${STATE}/enabled.pcsd-ruby.service" "${STATE}/postinst_enableunit.pcsd-ruby.service" "${STATE}/postinst_enableunit.logd.service"
run_install debian13protect
[ "${RC}" -eq 0 ] || fail "debian 13 protect exit ${RC}"
called 'systemctl (disable|stop) pcsd-ruby' && fail "pre-enabled pcsd-ruby was touched"
called 'systemctl disable logd.service' || fail "new logd was not restored"
# Distros without those units (not-found) are handled: nothing to disable.
reset
touch "${STATE}/notfound.pcsd-ruby.service" "${STATE}/notfound.logd.service" "${STATE}/notfound.sbd_remote.service"
run_install nounits
[ "${RC}" -eq 0 ] || fail "not-found extra units exit ${RC}: $(cat "${OUT}")"
grep -q 'cannot read the state' "${OUT}" && fail "not-found extra unit was treated as unknown"
called 'systemctl (disable|stop) (pcsd-ruby|logd|sbd_remote)' && fail "a missing unit was disabled"

# 8. --json reports the real family and a version-aware tier (as the Go profile).
json_for() { # os-release body -> JSON on stdout
	reset
	printf '%b' "$1" >"${WORK}/os-release"
	PATH="${BIN}:${PATH}" STUB_LOG="${LOG}" STUB_STATE="${STATE}" OS_RELEASE_FILE="${WORK}/os-release" sh "${SCRIPT}" --json --check 2>/dev/null || true
}
json_field() { python3 -I -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$1"; }
expect_json() { # os-release body, family, support, id
	_j="$(json_for "$1")"
	[ "$(printf '%s' "${_j}" | json_field family)" = "$2" ] || fail "family for $4: $(printf '%s' "${_j}" | json_field family), want $2"
	[ "$(printf '%s' "${_j}" | json_field support)" = "$3" ] || fail "support for $4: $(printf '%s' "${_j}" | json_field support), want $3"
	[ "$(printf '%s' "${_j}" | json_field id)" = "$4" ] || fail "id for $4"
}
expect_json 'ID=debian\nVERSION_ID="13"\n' apt supported debian
expect_json 'ID=debian\nVERSION_ID="12"\n' apt supported debian
expect_json 'ID=debian\nVERSION_ID="11"\n' apt unsupported debian
expect_json 'ID=ubuntu\nVERSION_ID="24.04"\n' apt supported ubuntu
expect_json 'ID=ubuntu\nVERSION_ID="20.04"\n' apt unsupported ubuntu
expect_json 'ID=linuxmint\nID_LIKE="ubuntu debian"\nVERSION_ID=22\n' apt experimental linuxmint
expect_json 'ID=fedora\nVERSION_ID=41\n' dnf experimental fedora
expect_json 'ID=arch\n' pacman partial arch
expect_json 'ID=plan9\n' unknown unsupported plan9
# Debian 11 is refused by --install exactly like the Go profile (exit 10).
reset
printf 'ID=debian\nVERSION_ID="11"\n' >"${WORK}/os-release"
run_install debian11 --accept-experimental
[ "${RC}" -eq 10 ] || fail "debian 11 install must exit 10, got ${RC}"
called 'apt-get' && fail "apt-get ran on an unsupported Debian"

# 9. --check exit code: 0 when the HA stack is installed, even if the
#    optional components (incus, headers, drbd-reactor, linstor) are not.
run_check() { # tag
	set +e
	PATH="${BIN}:${PATH}" STUB_LOG="${LOG}" STUB_STATE="${STATE}" OS_RELEASE_FILE="${WORK}/os-release" \
		sh "${SCRIPT}" --check >"${WORK}/check.$1" 2>&1 </dev/null
	RC=$?
	set -e
}
write_stub pcs 'echo 0.12.0'
write_stub drbdadm 'exit 0'
reset
for p in fence-agents-base resource-agents-base; do touch "${STATE}/installed.${p}"; done
run_check stackok
[ "${RC}" -eq 0 ] || fail "--check with the HA stack installed exited ${RC}: $(cat "${WORK}/check.stackok")"
reset
touch "${STATE}/installed.fence-agents-base"
run_check stackmissing
[ "${RC}" -eq 1 ] || fail "--check with resource-agents missing must exit 1, got ${RC}"
grep -q 'resource_agents' "${WORK}/check.stackmissing" || fail "--check does not list the missing core component"
rm -f "${BIN}/pcs" "${BIN}/drbdadm"

echo "test-ha-stack: ok"
