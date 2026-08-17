#!/usr/bin/env bash
# e2e-webui-turn-controls-selftest.sh — offline, deterministic tests for the
# two dangerous edges of scripts/e2e-webui-turn-controls.sh: --stop, which
# signals processes and deletes a directory the caller names, and the
# isolation its header promises while the stack comes up.
#
# Nothing here builds serf or starts a real hub. A fixture `go` on PATH copies
# throwaway shell "binaries" into the run directory, so what is under test is
# the script's own ordering, guards, and the environment it hands its
# children — not the daemons. The only processes this suite signals are
# sleepers it spawned itself; a sleeper stands in for the real pid a mistyped
# --stop would otherwise reach.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/e2e-webui-turn-controls.sh"
. "$(dirname "$0")/selftest-lib.sh"

work="$(mktemp -d -t e2e-webui-turn-controls-selftest.XXXXXX)"
work="$(cd "$work" && pwd -P)"

# Run directories the script made under its own mktemp; reaped on the way out
# so a failing assertion cannot leave a sleeper or a directory behind.
run_dirs=()

cleanup() {
	for dir in ${run_dirs[@]+"${run_dirs[@]}"}; do
		for pidfile in "$dir"/*.pid; do
			[ -f "$pidfile" ] || continue
			kill "$(cat "$pidfile")" 2>/dev/null
		done
		rm -rf "$dir"
	done
	for pidfile in "$work"/sleeper-*.pid; do
		[ -f "$pidfile" ] || continue
		kill "$(cat "$pidfile")" 2>/dev/null
	done
	rm -rf "$work"
}
trap cleanup EXIT

# spawn_sleeper — start a throwaway process and put its pid file in
# $sleeper_pidfile. The background job lives in a subshell that exits
# immediately, so this shell never has it in its job table and never prints a
# "Terminated" line when the script under test (or cleanup) signals it. Its
# stdout goes to /dev/null: a sleeper holding a caller's pipe open would hang
# any command substitution around it for the sleeper's whole lifetime.
sleeper_seq=0
sleeper_pidfile=""
spawn_sleeper() {
	sleeper_seq=$((sleeper_seq + 1))
	sleeper_pidfile="$work/sleeper-$sleeper_seq.pid"
	(sleep 300 >/dev/null 2>&1 & printf '%s' "$!" >"$sleeper_pidfile")
}

alive() { kill -0 "$(cat "$1")" 2>/dev/null; }

# --- the fixture toolchain --------------------------------------------------
# `go build -o <out> <pkg>` copies a throwaway shell binary named after <out>.
# Everything else is refused loudly: a new build in the script must be taught
# here rather than silently producing nothing.
fakebin="$work/bin"
fixtures="$work/fixtures"
mkdir -p "$fakebin" "$fixtures"

cat >"$fakebin/go" <<'FAKE_GO'
#!/usr/bin/env bash
set -u
[ "${1:-}" = "build" ] || { echo "fake go: only 'go build' is understood, got: $*" >&2; exit 64; }
out=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out="${2:-}"; shift 2 ;;
	*) shift ;;
	esac
done
[ -n "$out" ] || { echo "fake go: no -o output path" >&2; exit 64; }
name="$(basename "$out")"
cp "$FAKE_FIXTURES/$name" "$out" || { echo "fake go: no fixture binary named $name" >&2; exit 65; }
chmod +x "$out"
FAKE_GO

cat >"$fakebin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
# The hub health probe. The fake hub is not a server, so answer for it.
exit 0
FAKE_CURL

# The two long-lived children. Each records the isolation-relevant part of the
# environment it was started with — that is the contract under test — then logs
# the "listening on" line the script polls for and parks until it is signalled.
#
# Only the named variables are recorded, never the whole environment: a failing
# assertion prints the file it read, and the operator's real environment is
# full of credentials that have no business in a test log.
cat >"$fixtures/report-env" <<'REPORT_ENV'
#!/usr/bin/env bash
# report-env OUTFILE — one "NAME=value" line per variable serf reads to find
# its configuration, with "<unset>" where the script cleared one.
for name in SERF_PROVIDERS_CONFIG SERF_STATE_DIR SERF_RUN_DIR SERF_HUB_TOKEN \
	SERF_HUB_ADDR SERF_HUB_SPAWNED XDG_STATE_HOME XDG_CONFIG_HOME \
	XDG_CACHE_HOME HOME; do
	if [ -n "${!name+set}" ]; then
		printf '%s=%s\n' "$name" "${!name}"
	else
		printf '%s=<unset>\n' "$name"
	fi
done >"$1"
REPORT_ENV

cat >"$fixtures/fakellm" <<'FAKE_FAKELLM'
#!/usr/bin/env bash
"$FAKE_FIXTURES/report-env" "$FAKE_STATE/fakellm.env"
echo "fakellm listening on 127.0.0.1:14001 (base_url http://127.0.0.1:14001/v1)" >&2
while :; do sleep 1; done
FAKE_FAKELLM

cat >"$fixtures/serf-hub" <<'FAKE_HUB'
#!/usr/bin/env bash
"$FAKE_FIXTURES/report-env" "$FAKE_STATE/hub.env"
cp "$HOME/.serf/providers.toml" "$FAKE_STATE/hub-providers.toml" 2>/dev/null
if [ "${FAKE_HUB_MODE:-}" = "die" ]; then
	echo "fake hub: refusing to start" >&2
	exit 1
fi
printf 'fake-auth-token\n' >"$HOME/.serf/auth-token"
echo "serf-hub listening on 127.0.0.1:14002" >&2
echo "daemon session=s-fixture pid=999999" >&2
while :; do sleep 1; done
FAKE_HUB

# The daemon binary the hub is pointed at; never executed by this fixture.
printf '#!/usr/bin/env bash\nexit 0\n' >"$fixtures/serf"
chmod +x "$fakebin"/* "$fixtures"/*

state="$work/state"
mkdir -p "$state"

# run_script ARGS... — run the script under test with the fixture toolchain
# first on PATH and with every redirect-serf-elsewhere variable exported. That
# is the operator the script's header promises isolation to: each of these has
# to be gone from the children's environment. Output lands in $out.
out="$work/out.txt"
leaked_providers="$work/real-providers.toml"
printf 'schema = 1\ndefault = "expensive-real-provider"\n' >"$leaked_providers"
run_script() {
	env PATH="$fakebin:$PATH" \
		FAKE_STATE="$state" FAKE_FIXTURES="$fixtures" \
		SERF_PROVIDERS_CONFIG="$leaked_providers" \
		SERF_STATE_DIR="$work/real-state" \
		SERF_RUN_DIR="$work/real-run" \
		SERF_HUB_TOKEN="not-the-fixture-token" \
		SERF_HUB_ADDR="http://127.0.0.1:1" \
		SERF_HUB_SPAWNED=1 \
		XDG_STATE_HOME="$work/real-xdg-state" \
		XDG_CONFIG_HOME="$work/real-xdg-config" \
		XDG_CACHE_HOME="$work/real-xdg-cache" \
		bash "$script" "$@" >"$out" 2>&1
}

# --- scenario 1: --help documents --stop, the script's only teardown --------
run_script --help
rc=$?
assert_eq "$rc" "0" "--help exits 0"
assert_has "$out" "--stop RUN_DIR" "--help documents --stop, the only way to reap a run"
assert_has "$out" "--skip-web" "--help still documents the flags before --stop"
assert_has "$out" "--background-job" "--help documents --background-job"
assert_not_has "$out" "set -euo pipefail" "usage stops at the end of the header comment"
assert_not_has "$out" "#!/usr/bin/env bash" "usage omits the shebang line"

# --- scenario 2: --stop validates the marker BEFORE it signals anything -----
# A mistyped path that happens to hold a hub.pid. The pid is a process this
# suite owns; if the script signals before checking the marker, it dies.
unmarked="$work/not-ours-pidfile"
mkdir -p "$unmarked"
spawn_sleeper
sleeper="$sleeper_pidfile"
cat "$sleeper" >"$unmarked/hub.pid"
run_script --stop "$unmarked"
rc=$?
assert_eq "$rc" "2" "--stop on an unmarked directory exits 2"
if alive "$sleeper"; then
	ok "--stop leaves a process alone when the directory has no marker"
else
	bad "--stop signalled a process out of an unmarked directory"
fi
assert_has "$out" "not one of this script's run directories" "the refusal names the marker"
[ -d "$unmarked" ] && ok "--stop does not delete an unmarked directory" || bad "--stop deleted an unmarked directory"
kill "$(cat "$sleeper")" 2>/dev/null

# --- scenario 3: the same guard covers the daemon pids in hub.log -----------
unmarked_log="$work/not-ours-hublog"
mkdir -p "$unmarked_log"
spawn_sleeper
sleeper="$sleeper_pidfile"
printf 'daemon session=abc pid=%s\n' "$(cat "$sleeper")" >"$unmarked_log/hub.log"
run_script --stop "$unmarked_log"
rc=$?
assert_eq "$rc" "2" "--stop on an unmarked directory holding a hub.log exits 2"
if alive "$sleeper"; then
	ok "--stop leaves a hub.log's daemon pids alone when the directory has no marker"
else
	bad "--stop signalled a daemon pid out of an unmarked directory"
fi
kill "$(cat "$sleeper")" 2>/dev/null

# --- scenario 4: a started stack is isolated, and --stop reaps it -----------
rm -f "$state"/*.env "$state/hub-providers.toml"
run_script --skip-web
rc=$?
assert_eq "$rc" "0" "a start with the fixture toolchain exits 0"
run="$(grep -oE 'run directory .*' "$out" | head -1 | sed 's/^run directory //')"
if [ -n "$run" ] && [ -d "$run" ]; then
	run_dirs+=("$run")
	ok "the start prints its run directory"
else
	bad "the start did not print a usable run directory"
fi
assert_has "$state/hub.env" "SERF_PROVIDERS_CONFIG=<unset>" "the hub does not inherit SERF_PROVIDERS_CONFIG"
assert_has "$state/fakellm.env" "SERF_PROVIDERS_CONFIG=<unset>" "fakellm does not inherit SERF_PROVIDERS_CONFIG"
for isolated in SERF_STATE_DIR SERF_RUN_DIR SERF_HUB_TOKEN SERF_HUB_ADDR SERF_HUB_SPAWNED XDG_STATE_HOME XDG_CONFIG_HOME XDG_CACHE_HOME; do
	assert_has "$state/hub.env" "$isolated=<unset>" "the hub does not inherit $isolated"
done
assert_has "$state/hub.env" "HOME=$run/home" "the hub runs under the throwaway HOME"
assert_has "$state/hub-providers.toml" "127.0.0.1:14001" "the hub's providers.toml points at fakellm"
assert_not_has "$state/hub-providers.toml" "expensive-real-provider" "the throwaway providers.toml is not the operator's"
assert_has "$out" "Ready." "the start reports Ready"

hub_pid="$(cat "$run/hub.pid" 2>/dev/null)"
fakellm_pid="$(cat "$run/fakellm.pid" 2>/dev/null)"
if kill -0 "$hub_pid" 2>/dev/null && kill -0 "$fakellm_pid" 2>/dev/null; then
	ok "both children are left running after a successful start"
else
	bad "a child was not left running after a successful start"
fi

run_script --stop "$run"
rc=$?
assert_eq "$rc" "0" "--stop on a marked run directory exits 0"
[ ! -e "$run" ] && ok "--stop removes the marked run directory" || bad "--stop kept the marked run directory"
# The pid files went with the directory, so check the values read before it.
sleep 0.3
if kill -0 "$hub_pid" 2>/dev/null; then bad "--stop left the hub running"; else ok "--stop stopped the hub"; fi
if kill -0 "$fakellm_pid" 2>/dev/null; then bad "--stop left fakellm running"; else ok "--stop stopped fakellm"; fi

# --- scenario 5: a failed startup does not orphan fakellm -------------------
rm -f "$state"/*.env
FAKE_HUB_MODE=die run_script --skip-web
rc=$?
if [ "$rc" -ne 0 ]; then ok "a hub that never listens fails the start"; else bad "a hub that never listens still exited 0"; fi
failed_run="$(grep -oE 'run directory .*' "$out" | head -1 | sed 's/^run directory //')"
if [ -n "$failed_run" ] && [ -d "$failed_run" ]; then
	run_dirs+=("$failed_run")
else
	bad "the failed start did not print a usable run directory"
fi
sleep 0.2
if alive "$failed_run/fakellm.pid"; then
	bad "a failed startup orphaned fakellm"
else
	ok "a failed startup reaps fakellm"
fi

selftest_summary
