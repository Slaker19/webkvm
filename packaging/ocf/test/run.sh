#!/bin/sh
# Harness for packaging/ocf/incus. Offline: fakes for incus/timeout/sleep/date
# and ocf-shellfuncs in a temp dir. Usage: sh packaging/ocf/test/run.sh [-v]
HERE=$(cd "$(dirname "$0")" && pwd)
AGENT="$HERE/../incus"
VERBOSE=0
[ "${1:-}" = "-v" ] && VERBOSE=1

T=$(mktemp -d "${TMPDIR:-/tmp}/ocf-incus-test.XXXXXX") || exit 1
trap 'rm -rf "$T"' EXIT INT TERM
mkdir -p "$T/bin" "$T/ocf/lib/heartbeat" "$T/nobin"
for f in incus timeout sleep date; do
	cp "$HERE/fake-$f" "$T/bin/$f"
	chmod 755 "$T/bin/$f"
done
cp "$HERE/fake-ocf-shellfuncs" "$T/ocf/lib/heartbeat/ocf-shellfuncs"
# PATH without incus (for the "not installed" case): fakes minus incus.
for f in timeout sleep date; do cp "$T/bin/$f" "$T/nobin/$f"; done

PASS=0
FAIL=0
N=0
RC=0

# scn key=value ...: fresh scenario state (defaults: RUNNING on m1, we are m1).
scn() {
	N=$((N + 1))
	FAKE_S="$T/s$N"
	export FAKE_S
	mkdir -p "$FAKE_S"
	echo m1 >"$FAKE_S/me"
	echo m1 >"$FAKE_S/member"
	echo RUNNING >"$FAKE_S/status"
	echo remote >"$FAKE_S/pool"
	echo linstor >"$FAKE_S/driver"
	echo false >"$FAKE_S/autostart"
	printf 'm1,ONLINE\nm2,ONLINE\nm3,ONLINE\n' >"$FAKE_S/members"
	echo 1000 >"$FAKE_S/clock"
	echo 0 >"$FAKE_S/sleeps"
	: >"$FAKE_S/calls"
	: >"$FAKE_S/log"
	for kv in "$@"; do
		printf '%s\n' "${kv#*=}" >"$FAKE_S/${kv%%=*}"
	done
}

# run ACTION [VAR=VALUE ...]: runs the agent, sets RC.
run() {
	_a=$1
	shift
	env -i PATH="${AGENT_PATH:-$T/bin:/usr/bin:/bin}" FAKE_S="$FAKE_S" \
		OCF_ROOT="${AGENT_OCF_ROOT:-$T/ocf}" \
		OCF_RESKEY_instance="${INSTANCE-web1}" \
		OCF_RESKEY_CRM_meta_timeout=300000 OCF_RESKEY_CRM_meta_interval=30000 \
		"$@" sh "$AGENT" "$_a" >"$FAKE_S/stdout" 2>"$FAKE_S/stderr"
	RC=$?
}

ok() {
	PASS=$((PASS + 1))
	echo "PASS  $1"
}
ko() {
	FAIL=$((FAIL + 1))
	echo "FAIL  $1  ($2)"
	if [ "$VERBOSE" = 1 ]; then
		sed 's/^/      | /' "$FAKE_S/log" "$FAKE_S/stderr" "$FAKE_S/calls"
	fi
}
st() { cat "$FAKE_S/$1"; }
ncalls() { grep -c -- "$1" "$FAKE_S/calls"; }

# check NAME EXPECTED_RC [extra shell condition ...]
check() {
	_n=$1
	_e=$2
	shift 2
	if [ "$RC" != "$_e" ]; then
		ko "$_n" "rc=$RC expected $_e"
		return
	fi
	for _c in "$@"; do
		if ! eval "$_c"; then
			ko "$_n" "condition failed: $_c"
			return
		fi
	done
	ok "$_n"
}

# ---- static checks -------------------------------------------------------
N=$((N + 1)); FAKE_S="$T/s$N"; mkdir -p "$FAKE_S"; : >"$FAKE_S/log"; : >"$FAKE_S/calls"; : >"$FAKE_S/stderr"
if sh -n "$AGENT"; then ok "syntax: sh -n"; else ko "syntax: sh -n" "syntax error"; fi
if command -v dash >/dev/null 2>&1; then
	if dash -n "$AGENT"; then ok "syntax: dash -n"; else ko "syntax: dash -n" "syntax error"; fi
else
	echo "SKIP  syntax: dash -n (dash not installed)"
fi
if command -v shellcheck >/dev/null 2>&1; then
	if shellcheck -s sh "$AGENT"; then ok "shellcheck -s sh"; else ko "shellcheck -s sh" "warnings"; fi
else
	echo "SKIP  shellcheck (not installed)"
fi

# ---- meta-data -----------------------------------------------------------
scn
run meta-data
check "meta-data: rc 0" 0
if command -v xmllint >/dev/null 2>&1; then
	if xmllint --noout "$FAKE_S/stdout" 2>/dev/null; then ok "meta-data: valid XML (xmllint)"; else ko "meta-data: valid XML (xmllint)" "invalid"; fi
fi
if command -v python3 >/dev/null 2>&1; then
	if python3 - "$FAKE_S/stdout" <<'PY'
import sys, xml.dom.minidom
d = xml.dom.minidom.parse(sys.argv[1])
r = d.documentElement
assert r.tagName == "resource-agent" and r.getAttribute("name") == "incus"
assert d.getElementsByTagName("longdesc") and d.getElementsByTagName("shortdesc")
p = {e.getAttribute("name"): e for e in d.getElementsByTagName("parameter")}
assert set(p) == {"instance", "project", "stop_timeout", "move_wait"}, p
assert p["instance"].getAttribute("required") == "1" and p["instance"].getAttribute("unique") == "1"
dfl = {n: e.getElementsByTagName("content")[0].getAttribute("default") for n, e in p.items()}
assert dfl == {"instance": "", "project": "default", "stop_timeout": "60", "move_wait": "90"}, dfl
a = {e.getAttribute("name"): (e.getAttribute("timeout"), e.getAttribute("interval")) for e in d.getElementsByTagName("action")}
assert a["start"] == ("300s", ""), a
assert a["stop"] == ("120s", ""), a
assert a["monitor"] == ("30s", "30s"), a
assert a["validate-all"] == ("20s", ""), a
assert "meta-data" in a and not any(n.startswith("migrate") for n in a), a
PY
	then ok "meta-data: params/actions (python3 minidom)"; else ko "meta-data: params/actions (python3 minidom)" "assertion"; fi
fi

# ---- monitor -------------------------------------------------------------
scn; run monitor
check "monitor: RUNNING here -> 0" 0
scn status=STOPPED; run monitor
check "monitor: STOPPED here -> 7" 7
scn member=m2; run monitor
check "monitor: RUNNING on another member -> 7" 7
scn member=m2 status=ERROR "members=m1,ONLINE
m2,OFFLINE"; run monitor
check "monitor: ERROR, its member offline -> 7" 7
scn status=FROZEN; run monitor
check "monitor: FROZEN here -> 1" 1
scn status=ERROR; run monitor
check "monitor: ERROR here -> 1" 1
scn exists=0; run monitor
check "monitor: instance missing -> 7" 7
scn daemon_down=1; run monitor
check "monitor: daemon down (non-probe) -> 1" 1
scn daemon_down=1; run monitor OCF_RESKEY_CRM_meta_interval=0
check "monitor: daemon down (probe) -> 5" 5
scn hang=info; run monitor
check "monitor: incus hangs (timeout 124) -> 1" 1
scn project_exists=0; run monitor OCF_RESKEY_project=nope
check "monitor: project missing -> 5" 5
scn; AGENT_PATH="$T/nobin:/usr/bin:/bin" run monitor
check "monitor: no incus binary -> 5" 5
scn; AGENT_OCF_ROOT="$T/absent" run monitor
check "monitor: without ocf-shellfuncs (fallback codes) -> 0" 0
scn status=STOPPED; AGENT_OCF_ROOT="$T/absent" run monitor
check "monitor: without ocf-shellfuncs, STOPPED -> 7" 7
scn; run monitor
check "every incus call goes through timeout" 0 \
	'[ "$(grep -c "^incus " "$FAKE_S/calls")" = "$(grep -c "^timeout [0-9]* incus " "$FAKE_S/calls")" ]'

# ---- start ---------------------------------------------------------------
scn; run start
check "start: RUNNING here -> 0 (idempotent, no start/move)" 0 \
	'[ "$(ncalls "^incus start")" = 0 ]' '[ "$(ncalls "^incus move")" = 0 ]'
scn status=STOPPED; run start
check "start: STOPPED here -> start -> 0" 0 '[ "$(st status)" = RUNNING ]' '[ "$(ncalls "^incus start web1 --project default")" = 1 ]'
scn member=m2; run start
check "start: RUNNING on another ONLINE member -> 1, never moved" 1 \
	'[ "$(ncalls "^incus move")" = 0 ]' '[ "$(st member)" = m2 ]' '[ "$(st status)" = RUNNING ]'
scn member=m2 "members=garbage"; run start
check "start: RUNNING elsewhere, member state unknown -> 1, never moved" 1 '[ "$(ncalls "^incus move")" = 0 ]'
scn member=m2 status=ERROR move_fail_n=3 "members=m1,ONLINE
m2,OFFLINE"; run start
check "start: member offline -> move (3 retries) -> start -> 0" 0 \
	'[ "$(ncalls "^incus move web1 --target m1 --project default")" = 4 ]' \
	'[ "$(st member)" = m1 ]' '[ "$(st status)" = RUNNING ]'
scn member=m2 status=STOPPED "members=m1,ONLINE
m2,EVACUATED"; run start
check "start: STOPPED on EVACUATED member -> move -> start -> 0" 0 '[ "$(st member)" = m1 ]' '[ "$(st status)" = RUNNING ]'
scn member=m2 status=RUNNING "members=m1,ONLINE
m2,EVACUATED"; run start
check "start: RUNNING on EVACUATED member -> 1 running elsewhere, no move" 1 \
	'[ "$(ncalls "^incus move")" = 0 ]' '[ "$(st member)" = m2 ]' 'grep -q "running elsewhere" "$FAKE_S/log"'
scn project_exists=0; run start OCF_RESKEY_project=nope
check "start: project missing (list empty, project show fails) -> 5" 5 '[ "$(ncalls "^incus project show nope")" = 1 ]'
scn project_exists=0; run stop OCF_RESKEY_project=nope
check "stop: project missing -> 0" 0
scn exists=0 hang=project; run monitor
check "monitor: empty list and project show hangs -> 1 (not 5/7)" 1
scn member=m2 status=STOPPED; run start
check "start: STOPPED on another member -> move -> start -> 0" 0 '[ "$(st member)" = m1 ]' '[ "$(st status)" = RUNNING ]'
scn member=m2 status=ERROR move_fail_n=9999 "members=m1,ONLINE
m2,OFFLINE"; run start
check "start: move never succeeds -> 1 after move_wait (90s)" 1 \
	'[ "$(st member)" = m2 ]' '[ "$(st clock)" -ge 1090 ]' '[ "$(st clock)" -le 1100 ]'
scn member=m2 status=ERROR move_fail_n=9999 "members=m1,ONLINE
m2,OFFLINE"; run start OCF_RESKEY_move_wait=20
check "start: move_wait=20 honoured" 1 '[ "$(st clock)" -ge 1020 ]' '[ "$(st clock)" -le 1030 ]'
scn status=STOPPED start_err_n=1; run start
check "start: LINSTOR controller error on first start -> retry -> 0" 0 '[ "$(ncalls "^incus start")" = 2 ]' '[ "$(st status)" = RUNNING ]'
scn status=STOPPED start_err_n=9999; run start
check "start: storage error forever -> 1 at 0.9*timeout (270s)" 1 '[ "$(st clock)" -ge 1270 ]' '[ "$(st clock)" -le 1280 ]'
scn status=STOPPED start_hard_fail=1; run start
check "start: non-storage start error -> 1 without retry" 1 '[ "$(ncalls "^incus start")" = 1 ]'
scn daemon_down=1; run start
check "start: daemon down -> 1 at deadline" 1 '[ "$(st clock)" -ge 1270 ]'
scn exists=0; run start
check "start: instance missing -> 5" 5
scn status=FROZEN; run start
check "start: FROZEN here -> 1" 1

# ---- stop ----------------------------------------------------------------
scn; run stop
check "stop: graceful -> 0" 0 '[ "$(st status)" = STOPPED ]' \
	'[ "$(ncalls "^incus stop web1 --project default --timeout 60")" = 1 ]' '[ "$(ncalls "--force")" = 0 ]'
scn graceful_fail=1; run stop
check "stop: graceful fails -> --force -> 0 (cause logged)" 0 '[ "$(st status)" = STOPPED ]' \
	'[ "$(ncalls "^incus stop web1 --project default --force")" = 1 ]' 'grep -q "graceful stop failed" "$FAKE_S/log"'
scn hang=stop; run stop
check "stop: graceful hangs (124) -> --force -> 0" 0 '[ "$(st status)" = STOPPED ]' 'grep -q "rc=124" "$FAKE_S/log"'
scn; run stop OCF_RESKEY_stop_timeout=15
check "stop: stop_timeout param passed through" 0 '[ "$(ncalls "^timeout 35 incus stop web1 --project default --timeout 15")" = 1 ]'
scn member=m2; run stop
check "stop: running on another member -> 0, untouched" 0 '[ "$(st status)" = RUNNING ]' '[ "$(ncalls "^incus stop")" = 0 ]'
scn status=STOPPED; run stop
check "stop: already STOPPED -> 0" 0 '[ "$(ncalls "^incus stop")" = 0 ]'
scn exists=0; run stop
check "stop: instance missing -> 0" 0
scn daemon_down=1; run stop
check "stop: daemon down -> 1" 1

# ---- validate-all --------------------------------------------------------
scn; run validate-all
check "validate-all: ok (linstor) -> 0" 0
scn driver=ceph; run validate-all
check "validate-all: ok (ceph) -> 0" 0
for bad in "" "-rf" "a_b" "a b" "x;id" 'a$(id)' "bad-" "a/b"; do
	scn; INSTANCE="$bad" run validate-all
	check "validate-all: bad name '$bad' -> 6" 6 '[ "$(ncalls "^incus")" = 0 ]'
done
unset INSTANCE
scn; INSTANCE="-rf" run stop
check "stop: bad name -> 6, incus never called" 6 '[ "$(ncalls "^incus")" = 0 ]'
unset INSTANCE
scn driver=zfs; run validate-all
check "validate-all: local pool driver (zfs) -> 6" 6
scn driver=dir; run validate-all
check "validate-all: local pool driver (dir) -> 6" 6
scn autostart=true; run validate-all
check "validate-all: boot.autostart=true -> 6" 6
scn project_exists=0; run validate-all OCF_RESKEY_project=nope
check "validate-all: missing project -> 5" 5
scn exists=0; run validate-all
check "validate-all: missing instance -> 5" 5
scn; AGENT_PATH="$T/nobin:/usr/bin:/bin" run validate-all
check "validate-all: no incus binary -> 5" 5
scn; run validate-all OCF_RESKEY_stop_timeout=abc
check "validate-all: stop_timeout not integer -> 6" 6
scn; run validate-all OCF_RESKEY_move_wait=0
check "validate-all: move_wait=0 -> 6" 6
scn; run bogus
check "unknown action -> 3" 3

echo
echo "SUMMARY: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
