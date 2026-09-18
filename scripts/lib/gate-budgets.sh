#!/bin/sh
# gate-budgets.sh — the load-aware parallelism budgets each gate stream uses,
# and the effective arguments those budgets become.
#
# Sourced, never executed; POSIX sh because both a bash gate script
# (scripts/gate/run-module-tests.sh) and a dash `sh -c` npm script
# (cmd/evener-hub/frontend/package.json) source it. Sourcing only defines
# functions and runs nothing.
#
# Source this first, then call gate_source_helper with the helper's path. Every
# budget falls back to its historical fixed default when the helper is absent or
# answers with something unusable, so a checkout without the helper behaves as
# it did before the helper existed.
#
# This wiring lives here rather than inline in run-module-tests.sh so it can be
# exercised directly: source the library, replace the helper's machine probes
# with fixture values, and read the arguments it builds (gatebudgets_test.go).
# Asserting rendered script text instead passes on a comment and pins
# formatting rather than behavior.

# gate_source_helper PATH — source the load-aware helper when it is readable, so
# the budgets size themselves from spare capacity. An unreadable helper is not
# fatal: gate_budget then answers with its fixed default, which is how the gate
# behaved before the helper existed. Failures inside the helper itself are the
# helper's problem, not this call's.
gate_source_helper() {
	if [ -r "$1" ]; then
		. "$1"
	fi
}

# gate_budget CAP DEFAULT — the load-aware worker count for CAP, or DEFAULT
# when the helper is absent or its answer is not a positive integer. A zero from
# the helper is treated as unusable too: `-p 0` and `--maxWorkers=0` are invalid
# and a zero would silently drop the parallelism the caller asked for.
gate_budget() {
	_gb_cap=$1
	_gb_default=$2
	_gb_value=
	if command -v load_aware_workers >/dev/null 2>&1; then
		_gb_value="$(load_aware_workers "$_gb_cap" 2>/dev/null)" || _gb_value=
	fi
	case "$_gb_value" in
	''|*[!0-9]*) _gb_value=$_gb_default ;;
	esac
	if [ "$_gb_value" -eq 0 ]; then
		_gb_value=$_gb_default
	fi
	printf '%s' "$_gb_value"
}

# gate_init_budgets — set the Go gate's parallelism budgets from the
# environment (an explicit override always wins) or from gate_budget. The
# AGENT_SHARD_* values are exported because the shard runner reads them from its
# own child environment; the -p/-parallel values stay shell variables because
# run_module word-splits them into the go test argv directly.
gate_init_budgets() {
	ROOT_P=${ROOT_P-$(gate_budget 6 6)}
	AGENT_PARALLEL=${AGENT_PARALLEL-$(gate_budget 6 6)}
	AGENT_P=${AGENT_P-$(gate_budget 4 4)}
	export AGENT_SHARD_PARALLEL=${AGENT_SHARD_PARALLEL-$(gate_budget 3 3)}
	export AGENT_SHARD_SURVEY_PARALLEL=${AGENT_SHARD_SURVEY_PARALLEL-$(gate_budget 6 6)}
}

# gate_module_flags MODULE — the -p/-parallel flags run-module-tests.sh hands
# `go test` for MODULE, or nothing for a module with no explicit budget (go's
# own default -p is cgroup-aware; overriding it with the host's core count would
# oversubscribe a CPU-limited container).
gate_module_flags() {
	_gf_flags=
	case "$1" in
	.)
		[ -n "${ROOT_P-}" ] && _gf_flags="-p $ROOT_P"
		;;
	agent)
		[ -n "${AGENT_P-}" ] && _gf_flags="-p $AGENT_P"
		[ -n "${AGENT_PARALLEL-}" ] && _gf_flags="${_gf_flags:+$_gf_flags }-parallel $AGENT_PARALLEL"
		;;
	esac
	printf '%s' "$_gf_flags"
}

# vitest_run_args — the flags the frontend gate hands `vitest run`: the worker
# count sized to spare capacity, four on an idle machine, fewer as the load
# average rises, and four again when the helper is unavailable. The caller
# expands this unquoted, so it must stay free of glob characters.
vitest_run_args() {
	printf '%s' "--maxWorkers=$(gate_budget 4 4)"
}
