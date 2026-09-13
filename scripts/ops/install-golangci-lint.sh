#!/usr/bin/env bash
# install-golangci-lint.sh — install the .tool-versions-pinned golangci-lint
# into $(go env GOPATH)/bin, retrying a download that failed in transit.
#
# The upstream installer has no retry of its own. Its http_download_curl
# returns 1 for any non-200 and the installer's `set -e` aborts there, and its
# only options are -b (bindir), -d (debug), -h/-? (usage), -x (xtrace) and the
# release tag — nothing to ask it for another attempt. So one 500 from the
# release CDN failed the whole lint-golangci job, and with it the aggregate
# `static` job (GitHub run 34644540860). The retry has to wrap the invocation.
#
# The retry is bounded and deliberately does not classify the failure. The
# installer collapses every cause to exit 1, so telling a 5xx from a bad pin
# would mean matching upstream's log wording — a coupling that breaks silently
# the next time it is reworded. A genuinely broken pin therefore costs the
# attempts below plus their backoff before it fails, and it still fails with
# the installer's own diagnostics on stderr.
#
# The pinned version is the one in .tool-versions and is never chosen here.
# There is no Go-toolchain fallback: docs/developing-evener/linting.md makes
# `make tools` the install path and a missing golangci-lint a hard gate
# failure, and this script does not invent a second source for it.
#
# Usage:
#   scripts/ops/install-golangci-lint.sh
#     EVENER_GOLANGCI_INSTALL_ATTEMPTS=1 scripts/ops/install-golangci-lint.sh
#
# EVENER_GOLANGCI_INSTALL_ATTEMPTS is the total number of attempts (default 3,
# a positive integer). Backoff between them is 5s, then 10s, and so on.
#
# The fetch of install.sh is bounded at 10s to connect and 60s in total, because
# a connection that opens and then stalls is not a failed attempt to curl and
# would sit there forever instead of reaching the retry. Three attempts plus
# their backoff therefore cannot exceed about 195s. What that does NOT bound is
# the release download the upstream installer does with its own curl: this
# script cannot pass options into it, so a stall there is still unbounded.
set -euo pipefail

installer_url='https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh'

script_dir="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
repo_root="$(CDPATH='' cd -- "$script_dir/../.." && pwd)"
. "$script_dir/../lib/scratch-lib.sh"

attempts=${EVENER_GOLANGCI_INSTALL_ATTEMPTS:-3}
if [[ ! "$attempts" =~ ^[1-9][0-9]*$ ]]; then
	printf 'install-golangci-lint.sh: EVENER_GOLANGCI_INSTALL_ATTEMPTS must be a positive integer (got %q)\n' "$attempts" >&2
	exit 2
fi

version="$(awk '$1=="golangci-lint" {print $2}' "$repo_root/.tool-versions")"
if [ -z "$version" ]; then
	printf 'install-golangci-lint.sh: no golangci-lint row in %s\n' "$repo_root/.tool-versions" >&2
	exit 1
fi

bindir="$(go env GOPATH)/bin"

# Each attempt's stderr is captured so a retry notice can name the last thing
# that went wrong, then replayed so the installer's own diagnostics still reach
# the operator. The EXIT trap is armed before the scratch is minted, so a crash
# in between leaks nothing.
# Declared before the mint so the name is assigned in one place; scratch_dir
# fills it with printf -v and exits on any failure.
attempt_scratch=""
attempt_pid=""
# stop_attempt — end the running attempt and reap it.
#
# The attempt is a whole pipeline of other people's programs, so stopping the
# one process this script spawned is not enough: it runs in a process group of
# its own and the group is what gets signalled, which reaches the curl and the
# shell running the installer however late either appeared. The pid is signalled
# too, for the instant before the group exists. Reaping matters as much as
# signalling: without it the script can exit while the installer is still
# writing into the Go bin directory.
stop_attempt() {
	[ -n "$attempt_pid" ] || return 0
	kill -TERM "$attempt_pid" 2>/dev/null || :
	kill -TERM -- -"$attempt_pid" 2>/dev/null || :
	local waited=0
	while [ "$waited" -lt 50 ] && kill -0 "$attempt_pid" 2>/dev/null; do
		sleep 0.1
		waited=$((waited + 1))
	done
	kill -KILL "$attempt_pid" 2>/dev/null || :
	kill -KILL -- -"$attempt_pid" 2>/dev/null || :
	wait "$attempt_pid" 2>/dev/null || :
	attempt_pid=""
}
trap 'stop_attempt; scratch_rm' EXIT
# A signal ends the script without running the EXIT trap, and a CI cancellation
# lands during the fetch more often than anywhere else, so the scratch would be
# left behind on the runner and the installer left running in it. Each of these
# stops the attempt, cleans up and exits with the conventional 128 plus the
# signal number; stop_attempt and scratch_rm are both safe to run twice, so the
# EXIT trap that follows changes nothing.
trap 'stop_attempt; scratch_rm; exit 129' HUP
trap 'stop_attempt; scratch_rm; exit 130' INT
trap 'stop_attempt; scratch_rm; exit 143' TERM
scratch_dir attempt_scratch evener-golangci-install
attempt_log="$attempt_scratch/attempt.stderr"

# pipefail is what makes the fetch of install.sh part of the attempt: without
# it a failed curl hands `sh` an empty script, which exits 0 and reports a
# successful install of nothing. It is set inside the attempt's own shell, since
# that is where the pipeline now runs. The timeouts are what make a stalled
# fetch a failed attempt rather than a hang: curl has none by default, so a
# connection that opens and then goes quiet never returns and the retry never
# happens.
#
# Each attempt is exec'd through perl so it lands in its own process group and
# can be stopped as one. Named here rather than discovered at the spawn, where
# it would fail as an exec error inside an attempt log.
if ! command -v perl >/dev/null 2>&1; then
	printf 'install-golangci-lint.sh: perl is not on PATH. Each install attempt runs through perl setpgrp(0, 0) so the whole curl | sh pipeline can be stopped as a process group; setsid(1) would serve as well but is not on macOS.\n' >&2
	exit 2
fi
attempt=1
while :; do
	: >"$attempt_log"
	# The attempt runs in the background, in a process group of its own, and is
	# waited on. Backgrounded because a trapped signal is held until the current
	# foreground command returns, and a stalled fetch would hold a cancellation
	# for as long as curl's own bound; `wait` takes the signal at once. In its own
	# group because that is the only handle on the pipeline's other processes.
	# perl's setpgrp(0, 0) does the split between fork and exec — setsid(1) would
	# too and is not on macOS, and `set -m` would turn job control on for the
	# whole script.
	perl -e 'setpgrp(0, 0); exec @ARGV or die "exec: $!\n"' \
		-- bash -c 'set -o pipefail; curl -sSfL --connect-timeout 10 --max-time 60 "$1" | sh -s -- -b "$2" "$3"' \
		install-golangci-lint-attempt "$installer_url" "$bindir" "v$version" 2>"$attempt_log" &
	attempt_pid=$!
	attempt_status=0
	wait "$attempt_pid" || attempt_status=$?
	attempt_pid=""
	if [ "$attempt_status" -eq 0 ]; then
		cat "$attempt_log" >&2
		break
	fi
	cat "$attempt_log" >&2
	if [ "$attempt" -ge "$attempts" ]; then
		printf 'install-golangci-lint.sh: golangci-lint v%s did not install in %s attempt(s); the installer diagnostics are above.\n' \
			"$version" "$attempts" >&2
		exit 1
	fi
	delay=$((attempt * 5))
	# The attempt's last non-empty line is the closest thing to a cause this
	# script can name without parsing the installer's wording, which it
	# deliberately does not do. It also tells the operator when waiting is
	# pointless: a cause that is not the network — a pin with no release, a
	# bindir that cannot be written — fails identically on every attempt, so the
	# same line three times over means the backoff is buying nothing.
	last_error="$(awk 'NF { line = $0 } END { if (line != "") print line }' "$attempt_log")"
	printf 'install-golangci-lint.sh: install attempt %s of %s failed (%s); retrying in %ss.\n' \
		"$attempt" "$attempts" "${last_error:-no diagnostic output}" "$delay" >&2
	sleep "$delay"
	attempt=$((attempt + 1))
done

# Prove the pin actually landed rather than trusting the exit status: an
# installer that wrote a different release, or a bindir already holding an
# older binary, would otherwise reach the lint gate as a version mismatch
# nobody attributed to this step.
if ! installed="$("$bindir/golangci-lint" version 2>&1)"; then
	printf 'install-golangci-lint.sh: %s/golangci-lint did not run after installation: %s\n' \
		"$bindir" "$installed" >&2
	exit 1
fi
# Match the version as a whole word wherever it sits. The tool prints one line,
#
#   golangci-lint has version 2.13.1 built with go1.27.0 from 6d2288e0 on ...
#
# so the token after "version" is the bare release, with no leading `v`. That is
# the same spelling .tool-versions pins, and deliberately not the `v$version`
# tag the installer is invoked with, so do not add a `v` here. Squeezing the
# reported text to single-space-separated words and wrapping the result in
# spaces means the token is surrounded by spaces even when it ends a line or
# ends the output, which a bare *" version X "* pattern would reject.
reported=" $(printf '%s' "$installed" | tr -s '[:space:]' ' ') "
case "$reported" in
*" version $version "*) ;;
*)
	printf 'install-golangci-lint.sh: %s/golangci-lint reports "%s", not the pinned v%s from %s\n' \
		"$bindir" "$installed" "$version" "$repo_root/.tool-versions" >&2
	exit 1
	;;
esac
