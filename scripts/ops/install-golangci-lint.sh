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
# perl is required, and there is no fallback. Each attempt is exec'd through
# perl's setpgrp(0, 0) so the whole `curl | sh` pipeline lands in a process group
# of its own and can be stopped as one; without that a cancelled install leaves
# curl and the upstream installer running, writing into the Go bin directory
# after this script has exited. setsid(1) would do the same and is not on macOS.
# perl is on macOS and on the CI image, the gate's runner already requires it for
# the same reason, and a perl-less path would have to go back to signalling one
# process and hoping. A missing perl fails here by name, before any attempt.
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
. "$script_dir/../lib/process-group-lib.sh"

# Base ten, and kept that way: the backoff below multiplies it, and bash reads a
# leading zero as octal.
attempts=${EVENER_GOLANGCI_INSTALL_ATTEMPTS:-3}
if [[ ! "$attempts" =~ ^[0-9]+$ ]] || [ "$((10#$attempts))" -lt 1 ]; then
	printf 'install-golangci-lint.sh: EVENER_GOLANGCI_INSTALL_ATTEMPTS must be a positive integer (got %q)\n' "$attempts" >&2
	exit 2
fi
attempts=$((10#$attempts))

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
attempt_record=""
# Set when a stop could not be shown to have worked: the scratch and the record
# in it then stay, whatever else runs afterwards.
attempt_unconfirmed=0
# Seconds to wait for the attempt's group to empty after each of SIGTERM and
# SIGKILL. Only a member that ignores or cannot take the signal reaches the end
# of either wait, so an ordinary stop costs milliseconds.
attempt_stop_grace=5
# stop_attempt — end the running attempt, prove nothing of it is left, and reap
# the one process this script spawned.
#
# The attempt is a whole pipeline of other people's programs — curl, and the
# shell running the upstream installer — so the process this script spawned is
# not the whole of it: it runs in a group of its own and the group is what gets
# stopped, which reaches every member however late it appeared. What the stop
# must not do is wait forever. The leader alone says nothing about the rest, and
# an unbounded `wait` on a process wedged in uninterruptible sleep hangs the
# signal handler that called it, so the group is probed under a bounded grace
# and an unstoppable one is named rather than waited on.
stop_attempt() {
	local stop_status=0 members target="$attempt_pid" recorded marker
	if [ -z "$target" ] && [ -n "$attempt_record" ] && [ -e "$attempt_record" ]; then
		# The spawn records itself, and this is why: between the fork and the
		# shell's own `attempt_pid=$!` there is an attempt running that this
		# variable does not know about, and a signal arriving there would find
		# nothing to stop. The record is created before the fork and filled in by
		# the child before it splits, so waiting on it is waiting for the pid the
		# shell has not been given yet.
		if recorded="$(pgroup_record_value "$attempt_record" "$attempt_stop_grace")"; then
			marker="${recorded##*:}"
			recorded="${recorded%:*}"
			target="${recorded#p*:}"
			# Only while the number still names this attempt: a pid read from
			# a file is a number the kernel may have handed on since.
			if ! pgroup_owned_by "$target" "$marker" && ! pid_owned_by "$target" "$marker"; then
				return 0
			fi
		else
			printf 'install-golangci-lint.sh: an install attempt was spawned but never named itself, so it cannot be shown to have stopped.\n' >&2
			return 1
		fi
	fi
	[ -n "$target" ] || return 0
	if pid_leads_pgroup "$target"; then
		stop_pgroup "$target" "$attempt_stop_grace" || stop_status=$?
	else
		# The number is not a group of this script's making: either the child
		# has not run its setpgrp yet, or it has gone and the kernel has given
		# the number to somebody else. `kill -- -PID` would name that somebody
		# in the second case, so the child itself is what gets stopped — and
		# then the group it may have formed in the meantime is asked about,
		# because the split can land between the question and the signal.
		stop_pid "$target" "$attempt_stop_grace" || stop_status=$?
		if [ "$stop_status" -eq 0 ]; then
			if ! members="$(pgroup_survivors "$target")"; then
				stop_status=2
			elif [ -n "$members" ]; then
				stop_pgroup "$target" "$attempt_stop_grace" || stop_status=$?
			fi
		fi
	fi
	if [ "$stop_status" -eq 2 ]; then
		# Nothing could be seen, so nothing here can say the pipeline stopped —
		# and leaving it at that leaves curl and the upstream installer running,
		# writing into the Go bin directory after this script has gone. Signal
		# blind and say so, which is what the gate does with the same answer.
		escalate_blind "$target" "$attempt_stop_grace"
		printf 'install-golangci-lint.sh: the install attempt could not be shown to have stopped: the process listing that answers whether group %s is empty would not run. It has been signalled blind, and not waited on.\n' \
			"$target" >&2
		attempt_pid=""
		return 1
	fi
	if [ "$stop_status" -ne 0 ]; then
		printf 'install-golangci-lint.sh: the install attempt would not stop; process group %s still holds %s. Not waiting on it.\n' \
			"$target" "$(pgroup_survivor_report "$target")" >&2
		attempt_pid=""
		return 1
	fi
	# The group is empty, so this reap cannot block: the leader is a zombie or
	# already collected, and bash keeps a reaped job's status either way. Only a
	# pid this shell was actually handed can be waited on.
	[ -n "$attempt_pid" ] && wait "$attempt_pid" 2>/dev/null || :
	attempt_pid=""
}
# finish_cleanup — stop the attempt, then remove the scratch if and only if the
# stop could be shown to have worked.
#
# A scratch deleted over a group that may still be running takes the record with
# it, and that record is the only name anyone has for what is holding the Go bin
# directory open. The same rule the gate follows with its own records: what
# cannot be shown to have stopped keeps its name, and is said out loud.
finish_cleanup() {
	if [ "$attempt_unconfirmed" -eq 0 ] && stop_attempt; then
		scratch_rm
		return 0
	fi
	# Said once and remembered: a signal trap and then the EXIT trap both run
	# this, and the second pass can only repeat a stop already given up on,
	# including its grace — and must not mistake "nothing left to try" for
	# "nothing was left running" and delete the scratch after all.
	if [ "$attempt_unconfirmed" -eq 0 ]; then
		attempt_unconfirmed=1
		attempt_record=""
		printf 'install-golangci-lint.sh: keeping %s: it holds the record of an install attempt that could not be shown to have stopped.\n' \
			"$attempt_scratch" >&2
	fi
	return 0
}
trap finish_cleanup EXIT
# A signal ends the script without running the EXIT trap, and a CI cancellation
# lands during the fetch more often than anywhere else, so the scratch would be
# left behind on the runner and the installer left running in it. Each of these
# stops the attempt, cleans up and exits with the conventional 128 plus the
# signal number; stop_attempt and scratch_rm are both safe to run twice, so the
# EXIT trap that follows changes nothing.
trap 'finish_cleanup; exit 129' HUP
trap 'finish_cleanup; exit 130' INT
trap 'finish_cleanup; exit 143' TERM
scratch_dir attempt_scratch evener-golangci-install
attempt_log="$attempt_scratch/attempt.stderr"
attempt_record="$attempt_scratch/attempt.pgid"

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
	# Created before the fork, filled in by the child before it splits: an empty
	# record means an attempt is spawning, which is what stop_attempt waits on.
	if ! : >"$attempt_record"; then
		printf 'install-golangci-lint.sh: could not create the process-group record %s; not spawning an install that nothing could stop.\n' \
			"$attempt_record" >&2
		exit 1
	fi
	perl -e "$PGROUP_SPAWN_PERL" \
		-- "$attempt_record" install-golangci-lint-attempt \
		bash -c 'set -o pipefail; curl -sSfL --connect-timeout 10 --max-time 60 "$1" | sh -s -- -b "$2" "$3"' \
		install-golangci-lint-attempt "$installer_url" "$bindir" "v$version" 2>"$attempt_log" &
	attempt_pid=$!
	attempt_status=0
	wait "$attempt_pid" || attempt_status=$?
	# Reaped, so both names for it go now, before the backoff below gives a
	# signal somewhere to land. A record left holding `pgid:N` for an attempt
	# that has been collected names whatever the kernel gives that number to
	# next, and the trap would aim a SIGTERM and a SIGKILL at it.
	attempt_pid=""
	rm -f "$attempt_record"
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
# The pin comes out of .tool-versions, so it is a string this script did not
# choose, and a `case` pattern would let a `*` or a `?` in it match versions it
# does not name. Quoting it inside [[ ]] compares the characters themselves.
if [[ "$reported" != *" version $version "* ]]; then
	printf 'install-golangci-lint.sh: %s/golangci-lint reports "%s", not the pinned v%s from %s\n' \
		"$bindir" "$installed" "$version" "$repo_root/.tool-versions" >&2
	exit 1
fi
