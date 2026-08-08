#!/usr/bin/env bash
# deploy-hub-selftest.sh — offline, deterministic tests for scripts/deploy-hub.sh's
# decision logic against fake `launchctl`, `make`, and `curl` binaries (kata mssy).
# No real launchd job, no real build, no real network: every external tool
# deploy-hub.sh shells out to is shadowed on PATH by a fake that records what
# it was called with and refuses anything it doesn't recognize, same pattern
# as scripts/tmux-send-selftest.sh.
#
# What each fake understands:
#   launchctl list             — prints $FAKE_STATE/list-output verbatim.
#   launchctl print <target>   — prints the fixture written by
#                                 write_print_fixture for that target and call
#                                 number (falls back to the call-1 fixture if
#                                 no call-2 fixture exists, so single-call
#                                 scenarios only need one fixture).
#   launchctl kickstart -k <t> — records the call; fails if
#                                 FAKE_LAUNCHCTL_KICKSTART_FAIL is set.
#   make build-hub              — records the call; fails if FAKE_MAKE_FAIL
#                                 is set. Never touches a real Makefile.
#   curl -fsS --max-time 3 <url> — prints $FAKE_STATE/curl-body if present,
#                                 else fails (simulates health never answering).
#
# The one thing this suite pins above everything else: scripts/deploy-hub.sh
# must NEVER call `make` or `launchctl kickstart` before preflight has
# confirmed the launchd job's binary matches this checkout, and must NEVER
# call `launchctl kickstart` if the build failed. Every early-exit scenario
# below asserts the fake call counters directly rather than trusting exit
# codes alone — an exit code proves the script stopped, not that it stopped
# BEFORE touching the old process.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/deploy-hub.sh"
. "$(dirname "$0")/selftest-lib.sh"

work="$(mktemp -d -t deploy-hub-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

calls() { cat "$state/$1" 2>/dev/null || echo 0; } # $1 = counter file name

# safe_key TARGET — the same "/" -> "_" mapping used by both the fixture
# writer below and the fake launchctl's print handler, so a fixture written
# for "gui/501/com.test.serf-hub" is found by a print call for that exact
# target and no other.
safe_key() { printf '%s' "$1" | tr '/' '_'; }

# write_print_fixture TARGET CALL_NUM PROGRAM PID ADDR — builds one
# `launchctl print` fixture in the real tool's own format (verified against
# an actual `launchctl submit` job during manual testing of deploy-hub.sh):
# a single-tab "program ="/"pid =" pair, and a two-tab-indented "arguments"
# block deploy-hub.sh's -addr parser reads.
write_print_fixture() {
	target="$1"
	n="$2"
	program="$3"
	pid="$4"
	addr="$5"
	safe="$(safe_key "$target")"
	f="$state/print-$safe-$n.txt"
	{
		printf '%s = {\n' "$target"
		printf '\tactive count = 0\n'
		printf '\tstate = running\n'
		printf '\n'
		printf '\tprogram = %s\n' "$program"
		printf '\tpid = %s\n' "$pid"
		printf '\targuments = {\n'
		printf '\t\t%s\n' "$program"
		printf '\t\t-addr\n'
		printf '\t\t%s\n' "$addr"
		printf '\t\t-serf\n'
		printf '\t\t%s/serf\n' "$(dirname "$program")"
		printf '\t}\n'
		printf '}\n'
	} >"$f"
}

# Each case gets its own fake-bin dir, state dir, and throwaway git repo, so
# no scenario's call counters, fixtures, or commit identity leak into
# another's. FAKE_MAKE_FAIL / FAKE_LAUNCHCTL_KICKSTART_FAIL are reset for the
# same reason tmux-send-selftest.sh resets its FAKE_TMUX_* knobs: a bare
# `VAR=val` assignment is not scoped to one call in bash, unlike `VAR=val cmd`.
new_case() {
	FAKE_MAKE_FAIL=""
	FAKE_LAUNCHCTL_KICKSTART_FAIL=""
	case_dir="$(mktemp -d "$work/case.XXXXXX")"
	bin="$case_dir/bin"
	state="$case_dir/state"
	mkdir -p "$bin" "$state" "$case_dir/repo"
	repo="$(cd "$case_dir/repo" && pwd -P)" # -P: resolve symlinks the same way `git rev-parse --show-toplevel` will
	(
		cd "$repo" &&
			git init -q &&
			git config user.email t@t &&
			git config user.name t &&
			git symbolic-ref HEAD refs/heads/main &&
			echo one >file &&
			git add -A &&
			git commit -qm init
	) >/dev/null || {
		echo "FAIL: could not set up throwaway repo" >&2
		exit 1
	}
	repo_sha="$(cd "$repo" && git rev-parse --short HEAD)"
	binary_path="$repo/serf-hub"

	cat >"$bin/launchctl" <<'FAKE_LAUNCHCTL'
#!/usr/bin/env bash
# Fake launchctl: understands only `list`, `print <target>`, and
# `kickstart -k <target>` — the exact three invocations deploy-hub.sh makes.
set -u
cmd="${1:-}"
case "$cmd" in
list)
	cat "$FAKE_STATE/list-output" 2>/dev/null
	exit 0
	;;
print)
	target="${2:-}"
	safe="$(printf '%s' "$target" | tr '/' '_')"
	callfile="$FAKE_STATE/print-calls-$safe"
	n=$(($(cat "$callfile" 2>/dev/null || echo 0) + 1))
	printf '%s' "$n" >"$callfile"
	f="$FAKE_STATE/print-$safe-$n.txt"
	[ -f "$f" ] || f="$FAKE_STATE/print-$safe-1.txt"
	if [ ! -f "$f" ]; then
		echo "fake-launchctl: no print fixture for '$target' (call $n)" >&2
		exit 1
	fi
	cat "$f"
	exit 0
	;;
kickstart)
	if [ "${2:-}" != "-k" ]; then
		echo "fake-launchctl: kickstart expects '-k <target>', got '${2:-}'" >&2
		exit 1
	fi
	n=$(($(cat "$FAKE_STATE/kickstart-calls" 2>/dev/null || echo 0) + 1))
	printf '%s' "$n" >"$FAKE_STATE/kickstart-calls"
	printf '%s\n' "$@" >"$FAKE_STATE/kickstart-$n.argv"
	if [ -n "${FAKE_LAUNCHCTL_KICKSTART_FAIL:-}" ]; then
		echo "fake-launchctl: forced kickstart failure (FAKE_LAUNCHCTL_KICKSTART_FAIL)" >&2
		exit 1
	fi
	exit 0
	;;
*)
	echo "fake-launchctl: unsupported command: $cmd" >&2
	exit 127
	;;
esac
FAKE_LAUNCHCTL
	chmod +x "$bin/launchctl"

	cat >"$bin/make" <<'FAKE_MAKE'
#!/usr/bin/env bash
# Fake make: understands only `build-hub`. Never reads a real Makefile.
set -u
n=$(($(cat "$FAKE_STATE/make-calls" 2>/dev/null || echo 0) + 1))
printf '%s' "$n" >"$FAKE_STATE/make-calls"
printf '%s\n' "$@" >"$FAKE_STATE/make-$n.argv"
if [ "${1:-}" != "build-hub" ]; then
	echo "fake-make: unsupported target: ${1:-}" >&2
	exit 1
fi
if [ -n "${FAKE_MAKE_FAIL:-}" ]; then
	echo "fake-make: forced failure (FAKE_MAKE_FAIL)" >&2
	exit 1
fi
exit 0
FAKE_MAKE
	chmod +x "$bin/make"

	cat >"$bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
# Fake curl: understands only the exact `-fsS --max-time 3 <url>` shape
# deploy-hub.sh's health probe uses. Answers with $FAKE_STATE/curl-body if
# present, else fails (simulating a health endpoint that never comes up).
set -u
n=$(($(cat "$FAKE_STATE/curl-calls" 2>/dev/null || echo 0) + 1))
printf '%s' "$n" >"$FAKE_STATE/curl-calls"
printf '%s\n' "$@" >"$FAKE_STATE/curl-$n.argv"
if [ "$#" -ne 4 ] || [ "$1" != "-fsS" ] || [ "$2" != "--max-time" ] || [ "$3" != "3" ]; then
	echo "fake-curl: unexpected argv: $*" >&2
	exit 1
fi
case "$4" in
http://*/api/health) ;;
*)
	echo "fake-curl: unexpected URL: $4" >&2
	exit 1
	;;
esac
if [ -f "$FAKE_STATE/curl-body" ]; then
	cat "$FAKE_STATE/curl-body"
	exit 0
fi
exit 7
FAKE_CURL
	chmod +x "$bin/curl"
}

run_deploy_hub() (
	cd "$repo" || exit 99
	# bash 3.2's empty-array expansion under `set -u` needs the
	# "${args[@]+"${args[@]}"}" guard (see tmux-send-selftest.sh).
	env PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" \
		FAKE_MAKE_FAIL="${FAKE_MAKE_FAIL:-}" \
		FAKE_LAUNCHCTL_KICKSTART_FAIL="${FAKE_LAUNCHCTL_KICKSTART_FAIL:-}" \
		"$script" "${args[@]+"${args[@]}"}"
)

uid="$(id -u)"

# --- scenario 1: no launchd job matches *serf-hub* ---
new_case
args=()
out="$(run_deploy_hub 2>"$work/err1.txt")"
rc=$?
assert_eq "$rc" "1" "no-match: exits nonzero"
assert_has "$work/err1.txt" "no launchd job matching" "no-match: clear error naming the problem"
assert_eq "$(calls make-calls)" "0" "no-match: make is never invoked"
assert_eq "$(calls kickstart-calls)" "0" "no-match: kickstart is never invoked"

# --- scenario 2: multiple matching jobs, no --label given ---
new_case
printf '1001\t0\tcom.test.serf-hub-a\n1002\t0\tcom.test.serf-hub-b\n' >"$state/list-output"
args=()
out="$(run_deploy_hub 2>"$work/err2.txt")"
rc=$?
assert_eq "$rc" "2" "multi-match: exits nonzero"
assert_has "$work/err2.txt" "multiple serf-hub-like launchd jobs found" "multi-match: clear error asking for --label"
assert_has "$work/err2.txt" "com.test.serf-hub-a" "multi-match: lists the first candidate"
assert_has "$work/err2.txt" "com.test.serf-hub-b" "multi-match: lists the second candidate"
assert_eq "$(calls make-calls)" "0" "multi-match: make is never invoked"
assert_eq "$(calls kickstart-calls)" "0" "multi-match: kickstart is never invoked"

# --- scenario 3: --label given, but the job's program is a DIFFERENT checkout's binary ---
# This is the "don't restart someone else's hub" safety property — the most
# important one to pin, per the kata's acceptance criteria.
new_case
label="com.test.serf-hub"
target="gui/$uid/$label"
write_print_fixture "$target" 1 "/some/other/checkout/serf-hub" 4242 "127.0.0.1:19999"
args=(--label "$label")
out="$(run_deploy_hub 2>"$work/err3.txt")"
rc=$?
assert_eq "$rc" "1" "program-mismatch: exits nonzero"
assert_has "$work/err3.txt" "not this worktree's" "program-mismatch: clear error naming the mismatch"
assert_has "$work/err3.txt" "/some/other/checkout/serf-hub" "program-mismatch: error names the job's actual program"
assert_eq "$(calls make-calls)" "0" "program-mismatch: make is never invoked"
assert_eq "$(calls kickstart-calls)" "0" "program-mismatch: kickstart is never invoked — refuses before touching anything"

# --- scenario 4: build fails — old process must be left running untouched ---
new_case
label="com.test.serf-hub"
target="gui/$uid/$label"
write_print_fixture "$target" 1 "$binary_path" 5150 "127.0.0.1:19999"
FAKE_MAKE_FAIL=1
args=(--label "$label")
out="$(run_deploy_hub 2>"$work/err4.txt")"
rc=$?
assert_eq "$rc" "1" "build-fails: exits nonzero"
assert_has "$work/err4.txt" "build failed; old hub" "build-fails: clear error naming the build failure"
assert_has "$work/err4.txt" "5150" "build-fails: error names the old pid that is still running"
assert_eq "$(calls make-calls)" "1" "build-fails: make WAS invoked (and failed)"
assert_eq "$(calls kickstart-calls)" "0" "build-fails: kickstart is NEVER invoked when the build fails — old hub left running untouched"

# --- scenario 5: full happy path — build, restart, health check all succeed ---
new_case
label="com.test.serf-hub"
target="gui/$uid/$label"
write_print_fixture "$target" 1 "$binary_path" 6161 "127.0.0.1:19999" # pre-kickstart: old pid
write_print_fixture "$target" 2 "$binary_path" 7272 "127.0.0.1:19999" # post-kickstart: new pid
printf '{"version":"%s","started_at":"2026-01-01T00:00:00Z","hub_addr":"127.0.0.1:19999","capabilities":{}}' "$repo_sha" >"$state/curl-body"
args=(--label "$label")
out="$(run_deploy_hub 2>"$work/err5.txt")"
rc=$?
assert_eq "$rc" "0" "happy-path: exits 0"
assert_eq "$(cat "$work/err5.txt")" "" "happy-path: nothing on stderr"
assert_eq "$(calls make-calls)" "1" "happy-path: make is invoked exactly once"
assert_has "$state/make-1.argv" "build-hub" "happy-path: make is invoked with the build-hub target"
assert_eq "$(calls kickstart-calls)" "1" "happy-path: kickstart is invoked exactly once"
assert_has "$state/kickstart-1.argv" "-k" "happy-path: kickstart is called with -k (targeted, not a blanket restart)"
assert_has "$state/kickstart-1.argv" "$target" "happy-path: kickstart targets exactly the discovered job"
assert_has "$state/curl-1.argv" "http://127.0.0.1:19999/api/health" "happy-path: health probe hits the addr parsed from the job's -addr argument"
assert_has <(printf '%s' "$out") "6161" "happy-path: reports the old pid"
assert_has <(printf '%s' "$out") "7272" "happy-path: reports the new (different) pid"
assert_has <(printf '%s' "$out") "$repo_sha" "happy-path: reports the built commit identity"
assert_has <(printf '%s' "$out") "frontend dist rebuilt: unknown" "happy-path: reports asset freshness as unknown when dist/ doesn't exist, instead of failing"
assert_has <(printf '%s' "$out") "deploy-hub: OK" "happy-path: prints an overall OK summary line"

selftest_summary
