#!/usr/bin/env bash
# scratch-selftest-lib.sh — shared harness for proving a long-running script
# cleans up its scratch directory, sourced by the coverage runners' selftests.
#
# It exists for a bug that hid behind a green assertion. Each of those suites
# asserted `ls -A "$tmphome"` was empty after a run and called that "leaves no
# scratch behind" — but macOS `mktemp` ignores TMPDIR unless you hand it an
# explicit path template, so the scratch never entered $tmphome in the first
# place. The assertion inspected a directory the script had no way of using, and
# passed for as long as the bug existed. 56 abandoned directories holding 1.5GB
# had accumulated in one developer's real temp directory by the time anyone
# looked, one per selftest run and one per failed measurement.
#
# So the load-bearing check here is assert_scratch_inside_tmpdir: it catches the
# run in the middle and requires the scratch to be somewhere a leak check can
# see it. The kill checks then mean something, because they are reading the
# directory the script actually uses.
#
# Sourced, never executed. The caller provides:
#   tmphome        — the private TMPDIR handed to the run
#   start_scratch_run — starts a run that BLOCKS with its scratch created, in the
#                    background, and sets run_pid. Blocking is the point: the
#                    scratch only exists while the run is in flight.
#   run            — runs the script to completion under $tmphome, exiting zero
#                    (used by the reclaim assertions below)
#   scratch_prefix — the runner's scratch basename prefix, e.g. "serf-testcov"

# await_scratch — returns 0 once $tmphome is non-empty, 1 after ~10s.
await_scratch() {
	waited=0
	until [ -n "$(ls -A "$tmphome" 2>/dev/null)" ]; do
		waited=$((waited + 1))
		[ "$waited" -gt 100 ] && return 1
		sleep 0.1
	done
	return 0
}

# await_no_scratch — returns 0 once $tmphome is empty, 1 after ~5s. A signalled
# run needs a moment: bash defers a trap until the foreground command returns,
# so cleanup happens once the child it was waiting on is gone.
await_no_scratch() {
	waited=0
	until [ -z "$(ls -A "$tmphome" 2>/dev/null)" ]; do
		waited=$((waited + 1))
		[ "$waited" -gt 50 ] && return 1
		sleep 0.1
	done
	return 0
}

# assert_scratch_inside_tmpdir — the falsifiability check for every "leaves
# nothing behind" assertion in these suites.
assert_scratch_inside_tmpdir() {
	if start_scratch_run && await_scratch; then
		ok "the scratch is created inside TMPDIR"
	else
		bad "the scratch went outside TMPDIR, where no leak check can see it"
	fi
	kill -KILL -"$run_pid" 2>/dev/null
	wait "$run_pid" 2>/dev/null
	rm -rf "${tmphome:?}"/* 2>/dev/null
}

# assert_killed_run_cleans_up SIGNAL — a coverage run is long enough that
# interrupting one is routine, and each abandoned scratch holds a full set of
# profiles. The signal goes to the process group, which is what a terminal's
# Ctrl-C and a supervisor's kill both send.
assert_killed_run_cleans_up() {
	signal="$1"
	if ! start_scratch_run || ! await_scratch; then
		bad "the run never created its scratch directory"
		return
	fi
	kill -"$signal" -"$run_pid" 2>/dev/null
	wait "$run_pid" 2>/dev/null
	await_no_scratch
	assert_eq "$(ls -A "$tmphome")" "" "a run killed by SIG$signal leaves no scratch behind"
	rm -rf "${tmphome:?}"/* 2>/dev/null
}

# assert_reclaims_abandoned_scratch — no janitor sweeps TMPDIR for these
# runners; reclaim-test-debris.sh deliberately does not know their prefixes. A
# scratch whose owner is gone — SIGKILLed, OOM-killed, or cut off with the
# machine — is therefore the next run's problem, and the next run takes it.
assert_reclaims_abandoned_scratch() {
	local dead abandoned
	# A pid that has already exited AND been reaped: the shell that printed its
	# own pid is gone by the time the substitution returns.
	dead="$(bash -c 'echo $$')"
	abandoned="$tmphome/$scratch_prefix.$dead"
	mkdir -p "$abandoned"
	printf 'mode: set\n' >"$abandoned/root.cov"
	run >/dev/null 2>&1
	if [ -e "$abandoned" ]; then
		bad "an abandoned scratch survived the next run, and nothing else sweeps it"
	else
		ok "the next run reclaims an abandoned scratch"
	fi
	rm -rf "${tmphome:?}"/* 2>/dev/null
}

# assert_keeps_concurrent_scratch — starting the same runner twice against one
# TMPDIR is a supported thing to do, so the reclaim above must be scoped to
# leftovers no live process owns. This suite's own pid stands in for the
# concurrent run: it is live, it is not the run being started, and using it
# needs no process to babysit.
assert_keeps_concurrent_scratch() {
	local concurrent="$tmphome/$scratch_prefix.$$"
	mkdir -p "$concurrent"
	printf 'mode: set\n' >"$concurrent/root.cov"
	run >/dev/null 2>&1
	if [ -d "$concurrent" ]; then
		ok "a live process's scratch survives another run's reclaim"
	else
		bad "a live process's scratch was reclaimed out from under it"
	fi
	rm -rf "${tmphome:?}"/* 2>/dev/null
}

# assert_failed_run_keeps_scratch_until_next_run CMD... — a failed run keeps its
# profiles and per-module logs on purpose: the failure line printed their path
# and they are the only record of why. CMD is the caller's way of making the run
# fail. Nothing sweeps what it leaves, so the NEXT run of the same script
# removes it — which is exactly when a failed run's diagnostics stop being the
# current answer.
assert_failed_run_keeps_scratch_until_next_run() {
	"$@" >/dev/null 2>&1
	if [ -n "$(ls -A "$tmphome" 2>/dev/null)" ]; then
		ok "a failed run keeps its scratch for diagnosis"
	else
		bad "a failed run discarded the profiles and logs its failure line named"
	fi
	run >/dev/null 2>&1
	assert_eq "$(ls -A "$tmphome")" "" "the next run reclaims the failed run's scratch"
}
