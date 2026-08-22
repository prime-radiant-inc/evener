#!/usr/bin/env bash
# test-web-browser.sh — the real browser-only frontend guards. They stay out
# of test-web because jsdom cannot evaluate the CSS cascade or browser
# geometry. Every guard runs so one missing browser or failing case does not
# hide the remaining guards' verdicts; the exit status is the first nonzero
# one.
set -u

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
. "$script_dir/../lib/scratch-lib.sh"

cd "$script_dir/../../cmd/evener-hub/frontend" || exit 1

dir=""
guard_pid=""; status=0; complete=0

stop_guard() {
	[ -z "$guard_pid" ] || { kill -TERM "$guard_pid" 2>/dev/null || :; wait "$guard_pid" 2>/dev/null || :; guard_pid=""; }
}

finish_browser() {
	finish_status=$?; stop_guard
	if [ "$complete" -eq 1 ] && [ "$status" -eq 0 ] && [ "$finish_status" -eq 0 ]; then
		scratch_rm || { finish_status=1; [ -z "$dir" ] || printf 'full logs: %s\n' "$dir" >&2; }
	else
		[ -z "$dir" ] || printf 'full logs: %s\n' "$dir" >&2
	fi
	trap - 0; exit "$finish_status"
}

interrupted_browser() { stop_guard; exit "$1"; }

# The trap is armed before any scratch exists: a crash between mint and arming
# would leak the directory (the trap-before-mkdir ordering the audit enforces).
trap finish_browser EXIT
trap 'interrupted_browser 129' 1; trap 'interrupted_browser 130' 2; trap 'interrupted_browser 143' 15

scratch_dir dir evener-test-web-browser

for guard in layoutguard overflowguard shellguard spawnguard; do
	guard_dir="$dir/$guard"
	mkdir -p "$guard_dir/home" "$guard_dir/tmp" "$guard_dir/xdg-config" "$guard_dir/xdg-cache" "$guard_dir/xdg-state" || exit 1
	HOME="$guard_dir/home" TMPDIR="$guard_dir/tmp" XDG_CONFIG_HOME="$guard_dir/xdg-config" XDG_CACHE_HOME="$guard_dir/xdg-cache" XDG_STATE_HOME="$guard_dir/xdg-state" NODE_DISABLE_COMPILE_CACHE=1 node "scripts/$guard/run.mjs" >"$dir/$guard.log" 2>&1 &
	guard_pid=$!
	if wait "$guard_pid"; then
		guard_pid=""
		printf 'PASS  web-%s\n' "$guard"
	else
		guard_status=$?
		guard_pid=""
		printf 'FAIL  web-%s (exit %s)\n' "$guard" "$guard_status" >&2
		cat "$dir/$guard.log"
		[ "$status" -ne 0 ] || status="$guard_status"
	fi
done

complete=1
exit "$status"
