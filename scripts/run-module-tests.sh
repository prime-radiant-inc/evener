#!/usr/bin/env bash
# run-module-tests.sh — run the Go module test suites with wave scheduling.
#
# The repo is a go.work workspace of independent modules. `go test ./...` does
# not span modules, so the suites must be invoked per module. Running them
# sequentially serializes four independent suites; running ALL of them at once
# oversubscribes the CPU and starves the latency-sensitive tmux/TUI end-to-end
# tests in the root module (they render in real time and time out when the
# CPU-heavy agent suite hogs the cores).
#
# The compromise is two waves:
#   WAVE1 — the root module ALONE. Its tmux/TUI tests are so timing-sensitive
#           that even the small llm/auth suites running alongside can starve a
#           session enough to fail; it gets the machine to itself.
#   WAVE2 — the agent module (CPU-heavy, extra in-package parallelism via
#           AGENT_PARALLEL) plus the small llm/auth modules in its shadow.
# Waves run one after another; modules WITHIN a wave run concurrently. This keeps
# the tmux suite from fighting any other suite for cores, so the run is both fast
# and flake-free.
#
# Usage:
#   scripts/run-module-tests.sh <go-test-flags...>
#     scripts/run-module-tests.sh -count=1
#     scripts/run-module-tests.sh -short -count=1
#     scripts/run-module-tests.sh -race -short -count=1
#
# Output: one PASS/FAIL line per module (with wall time) as each finishes; a
# failing module's full output is printed at the end. Exits non-zero on any
# failure. Override WAVE1/WAVE2 to change the schedule, AGENT_PARALLEL to tune
# the agent wave's -parallel.
set -uo pipefail

WAVE1=${WAVE1:-"."}
# fuzz is the serf-agnostic toolkit module (promoter/schemagen) and invariant is
# the zero-dependency assertion module: both are CPU-only unit tests with no
# tmux/TUI timing sensitivity, so they ride WAVE2 alongside the other library
# modules. Listed here (not just in the Makefile's GO_MODULES, which this script
# does not read) so their tests are actually gated.
WAVE2=${WAVE2:-"agent llm auth fuzz invariant"}
# Extra -parallel for the agent wave. Defaults to 32 (helps overlap the few
# remaining timer waits on multi-core dev machines). An explicit empty value
# (note: -, not :-) means "don't pass -parallel" so go test uses GOMAXPROCS —
# the -race gate sets this to avoid oversubscribing few-core CI, which starves
# real per-test work past its timeouts.
AGENT_PARALLEL=${AGENT_PARALLEL-32}
flags="$*"
logdir="$(mktemp -d -t serf-module-tests.XXXXXX)"
fail=0

logpath() { printf '%s/%s.log' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }

# run_wave <extra-flags> <module...> — run the modules concurrently, wait, and
# report each one's result; records failures in the global $fail.
run_wave() {
	local extra="$1"; shift
	local -a names=() pids=()
	local m log
	for m in "$@"; do
		log="$(logpath "$m")"
		# Word-split flags intentionally so callers can pass multiple flags.
		# shellcheck disable=SC2086
		( cd "$m" && /usr/bin/time -p go test $flags $extra ./... ) >"$log" 2>&1 &
		pids+=("$!"); names+=("$m")
	done
	local i
	for i in "${!pids[@]}"; do
		m="${names[$i]}"; log="$(logpath "$m")"
		if wait "${pids[$i]}"; then
			printf 'PASS  %-8s %s\n' "$m" "$(awk '/^real /{print $2"s"}' "$log" | tail -1)"
		else
			printf 'FAIL  %-8s\n' "$m"; fail=1
		fi
	done
}

agentExtra=""
[ -n "$AGENT_PARALLEL" ] && agentExtra="-parallel $AGENT_PARALLEL"
run_wave "" $WAVE1
run_wave "$agentExtra" $WAVE2

if [ "$fail" -ne 0 ]; then
	echo
	echo "=== failing module output ==="
	for m in $WAVE1 $WAVE2; do
		log="$(logpath "$m")"
		[ -f "$log" ] || continue
		if grep -qE "^(FAIL|--- FAIL|panic:)" "$log"; then
			echo "----- $m -----"; cat "$log"
		fi
	done
	echo
	echo "full logs: $logdir"
fi

exit "$fail"
