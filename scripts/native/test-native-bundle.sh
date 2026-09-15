#!/usr/bin/env bash
# test-native-bundle.sh — bundle the native iPhone app with Metro, so a
# resolver regression fails here instead of on a device.
#
# The app has three resolvers and only one of them ships. Vitest resolves
# through Vite, `tsc` through TypeScript's own resolver, and the app on a
# device through mobile-native/metro.config.js — its watchFolders, its
# nodeModulesPaths, and the resolveRequest hook that redirects specifiers
# coming from the shared mobile/ and frontend sources. Nothing else in the gate
# runs Metro, so a regression in that third resolver used to ship green.
#
# `expo export` drives Metro over the real entry point with no device,
# simulator or packager, and exits non-zero naming the specifier it could not
# resolve. --clear so the verdict never comes from a warm Metro cache, and iOS
# only because that is the platform the app ships on.
set -uo pipefail

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
. "$script_dir/../lib/scratch-lib.sh"

# A hung bundler must not hold a CI job open forever. GNU coreutils' timeout is
# on the ubuntu runner; on a Mac it is gtimeout if Homebrew coreutils is
# installed, and otherwise absent, where the run is unbounded and the CI step's
# own timeout-minutes is the backstop that matters.
bound=()
if command -v timeout >/dev/null 2>&1; then
	bound=(timeout 900)
elif command -v gtimeout >/dev/null 2>&1; then
	bound=(gtimeout 900)
fi

dir=""
# Armed before the scratch exists, so a failure between mint and arming cannot
# leak the directory.
trap scratch_rm EXIT
scratch_dir dir evener-native-bundle
mkdir -p "$dir/home" "$dir/tmp" "$dir/xdg-config" "$dir/xdg-cache" "$dir/xdg-state" || exit 1

cd "$script_dir/../../mobile-native" || exit 1
HOME="$dir/home" TMPDIR="$dir/tmp" XDG_CONFIG_HOME="$dir/xdg-config" XDG_CACHE_HOME="$dir/xdg-cache" XDG_STATE_HOME="$dir/xdg-state" NODE_DISABLE_COMPILE_CACHE=1 \
	${bound[@]+"${bound[@]}"} \
	npx expo export --platform ios --output-dir "$dir/export" --clear || exit 1

# The bundle is the thing this gate is about, so its absence is a failure even
# when the exporter was happy.
if ! compgen -G "$dir/export/_expo/static/js/ios/*" >/dev/null; then
	printf 'test-native-bundle.sh: export wrote no iOS bundle under %s\n' "$dir/export" >&2
	exit 1
fi
