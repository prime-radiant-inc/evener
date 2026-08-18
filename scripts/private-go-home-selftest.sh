#!/usr/bin/env bash
# private-go-home-selftest.sh — behavior checks for disposable Go process homes.
set -uo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
. "$script_dir/selftest-lib.sh"
. "$script_dir/private-go-home.sh"

scratch_dir work private-go-home-selftest
trap 'scratch_rm' EXIT

ambient_home="$work/ambient-home"
ambient_config="$work/ambient-config"
ambient_cache="$work/ambient-cache"
ambient_goenv="$ambient_config/go/env"
mkdir -p "$ambient_home" "$(dirname "$ambient_goenv")" "$ambient_cache"
printf 'EVENER_MARKER=ambient\n' >"$ambient_goenv"

owner="$work/default-owner"
(
	unset GOPATH GOCACHE
	HOME="$ambient_home"
	XDG_CONFIG_HOME="$ambient_config"
	XDG_CACHE_HOME="$ambient_cache"
	GOENV="$ambient_goenv"
	export HOME XDG_CONFIG_HOME XDG_CACHE_HOME GOENV
	evener_prepare_private_go_home "$owner"
	printf '%s\n' "$HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" "$XDG_STATE_HOME" "$GOENV" "$GOPATH" "$GOCACHE" >"$work/default.env"
	printf 'EVENER_MARKER=private\n' >>"$GOENV"
)
assert_eq "$(sed -n '1p' "$work/default.env")" "$owner/home" "default HOME belongs to the caller's lifecycle root"
assert_eq "$(sed -n '2p' "$work/default.env")" "$owner/xdg-config" "default XDG config belongs to the lifecycle root"
assert_eq "$(sed -n '3p' "$work/default.env")" "$owner/xdg-cache" "default XDG cache belongs to the lifecycle root"
assert_eq "$(sed -n '4p' "$work/default.env")" "$owner/xdg-state" "default XDG state belongs to the lifecycle root"
assert_eq "$(sed -n '5p' "$work/default.env")" "$owner/go-env" "ambient GOENV is copied into the lifecycle root"
assert_eq "$(sed -n '6p' "$work/default.env")" "$ambient_home/go" "default GOPATH keeps the ambient reusable module cache"
case "$(uname -s)" in
	Darwin) want_gocache="$ambient_home/Library/Caches/go-build" ;;
	*) want_gocache="$ambient_cache/go-build" ;;
esac
assert_eq "$(sed -n '7p' "$work/default.env")" "$want_gocache" "default GOCACHE keeps the ambient reusable build cache"
assert_eq "$(cat "$ambient_goenv")" "EVENER_MARKER=ambient" "writes through copied GOENV cannot mutate ambient Go settings"
assert_has "$owner/go-env" "EVENER_MARKER=private" "the owned GOENV copy remains writable to the child"

configured_goenv="$work/configured-go-env"
printf 'GOPATH=/obsolete\nGOPATH=\nGOPATH=/configured/gopath\nGOCACHE=/configured/gocache\n' >"$configured_goenv"
owner="$work/configured-owner"
(
	unset GOPATH GOCACHE
	HOME="$ambient_home" GOENV="$configured_goenv"
	export HOME GOENV
	evener_prepare_private_go_home "$owner"
	printf '%s\n' "${GOPATH-unset}" "${GOCACHE-unset}" >"$work/configured.env"
)
assert_eq "$(sed -n '1p' "$work/configured.env")" "unset" "copied GOENV retains its last nonempty GOPATH without an environment override"
assert_eq "$(sed -n '2p' "$work/configured.env")" "unset" "copied GOENV retains its last nonempty GOCACHE without an environment override"
assert_has "$owner/go-env" "GOPATH=/configured/gopath" "the configured GOPATH reaches the owned GOENV copy"
assert_has "$owner/go-env" "GOCACHE=/configured/gocache" "the configured GOCACHE reaches the owned GOENV copy"

cleared_goenv="$work/cleared-go-env"
printf 'GOPATH=/obsolete\nGOPATH=\nGOCACHE=/obsolete\nGOCACHE=\n' >"$cleared_goenv"
owner="$work/cleared-owner"
(
	unset GOPATH GOCACHE
	HOME="$ambient_home" XDG_CACHE_HOME="$ambient_cache" GOENV="$cleared_goenv"
	export HOME XDG_CACHE_HOME GOENV
	evener_prepare_private_go_home "$owner"
	printf '%s\n' "$GOPATH" "$GOCACHE" >"$work/cleared.env"
)
assert_eq "$(sed -n '1p' "$work/cleared.env")" "$ambient_home/go" "a final empty GOENV GOPATH restores the ambient default"
assert_eq "$(sed -n '2p' "$work/cleared.env")" "$want_gocache" "a final empty GOENV GOCACHE restores the ambient default"

owner="$work/explicit-owner"
(
	HOME="$ambient_home" GOENV="$ambient_goenv" GOPATH=/explicit/gopath GOCACHE=/explicit/gocache
	export HOME GOENV GOPATH GOCACHE
	evener_prepare_private_go_home "$owner"
	printf '%s\n' "$GOPATH" "$GOCACHE" >"$work/explicit.env"
)
assert_eq "$(sed -n '1p' "$work/explicit.env")" "/explicit/gopath" "explicit GOPATH is preserved"
assert_eq "$(sed -n '2p' "$work/explicit.env")" "/explicit/gocache" "explicit GOCACHE is preserved"

owner="$work/off-owner"
(
	unset GOPATH GOCACHE
	HOME="$ambient_home" GOENV=off
	export HOME GOENV
	evener_prepare_private_go_home "$owner"
	printf '%s\n' "$GOENV" >"$work/off.env"
)
assert_eq "$(cat "$work/off.env")" "off" "GOENV=off remains literal off"
if [ ! -e "$owner/go-env" ]; then
	ok "GOENV=off creates no substitute settings file"
else
	bad "GOENV=off unexpectedly created $owner/go-env"
fi

linux_bin="$work/linux-bin"
linux_config="$work/linux-config"
linux_cache="$work/linux-cache"
mkdir -p "$linux_bin" "$linux_config/go" "$linux_cache"
printf '#!/bin/sh\nprintf "Linux\\n"\n' >"$linux_bin/uname"
chmod +x "$linux_bin/uname"
printf 'EVENER_LINUX_XDG=preserved\n' >"$linux_config/go/env"
owner="$work/linux-owner"
(
	unset HOME GOENV GOPATH GOCACHE
	PATH="$linux_bin:$PATH" XDG_CONFIG_HOME="$linux_config" XDG_CACHE_HOME="$linux_cache"
	export PATH XDG_CONFIG_HOME XDG_CACHE_HOME
	evener_prepare_private_go_home "$owner"
	printf '%s\n' "$HOME" "$GOENV" "${GOCACHE-unset}" >"$work/linux.env"
)
assert_eq "$(sed -n '1p' "$work/linux.env")" "$owner/home" "Linux without ambient HOME still receives an owned HOME"
assert_eq "$(sed -n '2p' "$work/linux.env")" "$owner/go-env" "Linux without HOME copies GOENV from XDG config"
assert_eq "$(sed -n '3p' "$work/linux.env")" "$linux_cache/go-build" "Linux without HOME preserves the XDG Go build cache"
assert_has "$owner/go-env" "EVENER_LINUX_XDG=preserved" "Linux XDG Go settings reach the owned copy"

blocker="$work/not-a-directory"
printf 'blocker\n' >"$blocker"
owner="$blocker/owner"
(
	HOME="$ambient_home"
	export HOME
	if evener_prepare_private_go_home "$owner" 2>"$work/setup-failure.err"; then
		printf 'success\n' >"$work/setup-failure.status"
	else
		printf 'failure\n' >"$work/setup-failure.status"
	fi
	printf '%s\n' "$HOME" >"$work/setup-failure.home"
)
assert_eq "$(cat "$work/setup-failure.status")" "failure" "an unusable lifecycle root fails containment setup"
assert_eq "$(cat "$work/setup-failure.home")" "$ambient_home" "failed containment setup does not export a nonexistent HOME"

selftest_summary
