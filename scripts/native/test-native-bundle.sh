#!/usr/bin/env bash
# test-native-bundle.sh — bundle the native iPhone app with Metro and fail on
# any specifier Metro cannot resolve.
#
# Nothing else in the gate runs Metro. Vitest resolves through Vite and `tsc`
# resolves through TypeScript's own resolver, so both read the tsconfig paths
# and neither reads metro.config.js. A resolver regression there — a stale
# alias, a dropped watchFolders entry, a nodeModulesPaths entry that only works
# from one directory — is invisible to every other native check and surfaces
# first on a device. Bundling the real entry point is the only thing that reads
# the real resolver.
#
# `expo export` is the bundling entry point this toolchain supports without a
# device, a simulator, or a running packager: it drives Metro over index.ts and
# exits non-zero naming the specifier and the import stack when resolution
# fails. It runs with --clear so the verdict never comes from a warm Metro
# cache, and on iOS only, which is the platform the app ships on.
#
# Usage:
#   scripts/native/test-native-bundle.sh [--verbose]
#
# Output: one PASS line with the wall time. A failure replays the bundler's
# whole log and keeps the log directory; --verbose replays it on success too.
set -uo pipefail

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
. "$script_dir/../lib/scratch-lib.sh"

verbose=0
for arg in "$@"; do
	case "$arg" in
	--verbose) verbose=1 ;;
	*)
		printf 'test-native-bundle.sh: unknown argument %q (usage: test-native-bundle.sh [--verbose])\n' "$arg" >&2
		exit 2
		;;
	esac
done

# A Metro run against a missing dependency tree fails with pages of resolution
# errors that all say the same thing. Name the real cause instead.
app_dir="$script_dir/../../mobile-native"
if [ ! -d "$app_dir/node_modules" ]; then
	printf 'test-native-bundle.sh: mobile-native/node_modules is missing; run `npm ci --prefix mobile-native` first\n' >&2
	exit 1
fi
cd "$app_dir" || exit 1

# A hung bundler must not hold a CI job open forever. This is a tripwire far
# above the real cost (~15s cold on a developer Mac), never the mechanism that
# decides the verdict. Must be a positive integer in seconds.
BUNDLE_TIMEOUT=${EVENER_NATIVE_BUNDLE_TIMEOUT:-600}
if [[ ! "$BUNDLE_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
	printf 'test-native-bundle.sh: EVENER_NATIVE_BUNDLE_TIMEOUT must be a positive integer in seconds (got %q)\n' "$BUNDLE_TIMEOUT" >&2
	exit 2
fi

dir=""
complete=0

finish() {
	finish_status=$?
	if [ "$complete" -eq 1 ]; then
		scratch_rm || finish_status=1
	else
		[ -z "$dir" ] || printf 'full log: %s\n' "$dir/bundle.log" >&2
	fi
	trap - 0
	exit "$finish_status"
}

# Armed before any scratch exists, so a failure between mint and arming cannot
# leak the directory.
trap finish EXIT
trap 'exit 129' 1
trap 'exit 130' 2
trap 'exit 143' 15

scratch_dir dir evener-native-bundle
mkdir -p "$dir/home" "$dir/tmp" || exit 1

# Job control puts the bundler in its own process group, so the bound below
# stops Metro's whole worker pool by signalling that group. Nothing here reads
# a global process list or guesses at a pid it did not start.
set -m
(
	HOME="$dir/home" TMPDIR="$dir/tmp" NODE_DISABLE_COMPILE_CACHE=1 \
		npx expo export --platform ios --output-dir "$dir/export" --clear
) >"$dir/bundle.log" 2>&1 &
bundle_pid=$!
set +m

started_at=$SECONDS
while kill -0 "$bundle_pid" 2>/dev/null; do
	if [ $((SECONDS - started_at)) -ge "$BUNDLE_TIMEOUT" ]; then
		kill -TERM -"$bundle_pid" 2>/dev/null || :
		wait "$bundle_pid" 2>/dev/null || :
		printf 'FAIL  native-bundle (timed out after %ss)\n' "$BUNDLE_TIMEOUT" >&2
		cat "$dir/bundle.log" >&2
		exit 1
	fi
	sleep 0.1
done
if wait "$bundle_pid"; then bundle_status=0; else bundle_status=$?; fi
elapsed=$((SECONDS - started_at))

if [ "$bundle_status" -ne 0 ]; then
	printf 'FAIL  native-bundle (%ss)\n' "$elapsed" >&2
	cat "$dir/bundle.log" >&2
	exit "$bundle_status"
fi

[ "$verbose" -eq 0 ] || cat "$dir/bundle.log"
# The module count is the one number worth carrying in a passing gate's line:
# a bundle that silently shrank resolved something differently than it used to.
modules="$(sed -n 's/.*(\([0-9][0-9]*\) modules).*/\1/p' "$dir/bundle.log" | tail -1)"
if [ -n "$modules" ]; then
	printf 'PASS  native-bundle (%ss, %s modules)\n' "$elapsed" "$modules"
else
	printf 'PASS  native-bundle (%ss)\n' "$elapsed"
fi
complete=1
