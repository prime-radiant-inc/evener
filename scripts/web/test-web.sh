#!/usr/bin/env bash
# test-web.sh — the frontend's single gate entry point: typecheck, unit
# tests, then lint (mirrors the Go test+lint split, but the frontend
# toolchain doesn't need separate targets per check).
#
# The three checks are independent readers of the same sources, so they run
# concurrently and wall time is the slowest one (vitest) instead of the sum.
# Each writes its own log; a failure replays exactly the failing check's
# output. Every check runs with its own HOME/TMPDIR/XDG roots under a
# scratch-lib minted directory; a failed or interrupted run keeps the log
# directory and prints its path.
set -u

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$script_dir/../lib/scratch-lib.sh"

cd "$script_dir/../../cmd/evener-hub/frontend" || exit 1

dir=""
pids=""; started=""; fail=0; complete=0

stop_checks() {
	for pid in $pids; do kill -TERM "$pid" 2>/dev/null || :; done
	for pid in $pids; do wait "$pid" 2>/dev/null || :; done
	pids=""
}

finish() {
	finish_status=$?
	[ "$complete" -eq 1 ] || stop_checks
	if [ "$complete" -eq 1 ] && [ "$fail" -eq 0 ] && [ "$finish_status" -eq 0 ]; then
		scratch_rm || finish_status=1
	else
		[ -z "$dir" ] || printf 'full logs: %s\n' "$dir" >&2
	fi
	trap - 0; exit "$finish_status"
}

interrupted() { stop_checks; exit "$1"; }

# The trap is armed before any scratch exists: a crash between mint and arming
# would leak the directory (the trap-before-mkdir ordering the audit enforces).
trap finish EXIT
trap 'interrupted 129' 1; trap 'interrupted 130' 2; trap 'interrupted 143' 15

scratch_dir dir evener-test-web

for c in typecheck test lint; do
	check_dir="$dir/$c"
	if ! mkdir -p "$check_dir/home" "$check_dir/tmp" "$check_dir/xdg-config" "$check_dir/xdg-cache" "$check_dir/xdg-state"; then fail=1; break; fi
	HOME="$check_dir/home" TMPDIR="$check_dir/tmp" XDG_CONFIG_HOME="$check_dir/xdg-config" XDG_CACHE_HOME="$check_dir/xdg-cache" XDG_STATE_HOME="$check_dir/xdg-state" NODE_DISABLE_COMPILE_CACHE=1 npm run "$c" >"$dir/$c.log" 2>&1 &
	pids="$pids $!"; started="$started $c"
done

set -- $pids
for c in $started; do
	pid=$1; shift
	if wait "$pid"; then check_status=0; else check_status=$?; fi
	pids="$*"
	printf '%s\n' "$check_status" >"$dir/$c.status"
done

for c in typecheck test lint; do
	if [ "$(cat "$dir/$c.status" 2>/dev/null || echo 1)" = 0 ]; then
		printf 'PASS  web-%s\n' "$c"
	else
		printf 'FAIL  web-%s\n' "$c"; [ ! -f "$dir/$c.log" ] || cat "$dir/$c.log"; fail=1
	fi
done

complete=1
exit "$fail"
