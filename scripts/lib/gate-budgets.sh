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

# Whether gate_source_helper has successfully sourced the helper. gate_budget
# consults this instead of `command -v` alone: an ambient executable (or an
# unrelated function) named load_aware_workers must never stand in for the
# helper this library was asked to source, and with no helper sourced the budget
# must fall back to the fixed default rather than run whatever PATH offers.
_gate_budgets_helper_sourced=0

# gate_source_helper PATH — source the load-aware helper when it is readable, so
# the budgets size themselves from spare capacity. An unreadable helper is not
# fatal: gate_budget then answers with its fixed default, which is how the gate
# behaved before the helper existed. Failures inside the helper itself are the
# helper's problem, not this call's.
gate_source_helper() {
	if [ -r "$1" ]; then
		. "$1" && _gate_budgets_helper_sourced=1
	fi
}

# gate_budget CAP DEFAULT — the load-aware worker count for CAP, or DEFAULT
# when the helper is absent or its answer is not a positive integer. A zero from
# the helper is treated as unusable too: `-p 0` and `--maxWorkers=0` are invalid
# and a zero would silently drop the parallelism the caller asked for. Only the
# function gate_source_helper sourced is consulted; see _gate_budgets_helper_sourced.
gate_budget() {
	_gb_cap=$1
	_gb_default=$2
	_gb_value=
	if [ "${_gate_budgets_helper_sourced:-0}" -eq 1 ] && command -v load_aware_workers >/dev/null 2>&1; then
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
	# Total shard processes the runner may keep alive at once, independent of
	# AGENT_SHARD_COUNT (which partitions the tests) and of each shard's
	# AGENT_SHARD_PARALLEL: the runner otherwise starts every shard at once, so
	# a one-CPU cgroup still gets one process per shard. gate_budget 8 8 is
	# min(cores, 8) on an idle machine, so a host with at least 8 CPUs keeps
	# starting every shard at once (unchanged) while a smaller or busier one
	# starts fewer.
	export AGENT_SHARD_CONCURRENCY=${AGENT_SHARD_CONCURRENCY-$(gate_budget 8 8)}
	# The root-module packages sharded beside the root go test (evener dev
	# hub-shards / cli-shards): cmd/evener-hub's ~2100 and cmd/evener's ~340
	# mostly-serial tests. Their per-shard width, survey width and concurrency
	# are budgeted exactly like the agent's, so a loaded or one-CPU host shrinks
	# every runner. Each runner's concurrency is its own budget, not a share of
	# one: the two overlap only while the CLI's shards run (about 12s at the
	# head of the root wave), and the gate already lets its streams contend
	# rather than coordinating one CPU budget across them.
	gate_init_shard_budgets HUB 8
	gate_init_shard_budgets CLI 6
}

# gate_init_shard_budgets PREFIX COUNT — export PREFIX_SHARD_COUNT (default
# COUNT) and PREFIX_SHARD_PARALLEL / _SURVEY_PARALLEL / _CONCURRENCY from the
# same budgets as the agent's shards; an explicit value in the environment wins.
gate_init_shard_budgets() {
	eval "export ${1}_SHARD_COUNT=\${${1}_SHARD_COUNT-$2}"
	eval "export ${1}_SHARD_PARALLEL=\${${1}_SHARD_PARALLEL-$(gate_budget 3 3)}"
	eval "export ${1}_SHARD_SURVEY_PARALLEL=\${${1}_SHARD_SURVEY_PARALLEL-$(gate_budget 6 6)}"
	eval "export ${1}_SHARD_CONCURRENCY=\${${1}_SHARD_CONCURRENCY-$(gate_budget 8 8)}"
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
# average rises, and four again when the helper is unavailable. Never fewer
# than two: the suite runs on vitest's vmThreads pool, and with one worker
# vitest batches every file into a single shared VM context, so module
# singletons, jsdom windows and prototype stubs leak from file to file. The
# caller expands this unquoted, so it must stay free of glob characters.
vitest_run_args() {
	_vra_workers=$(gate_budget 4 4)
	[ "$_vra_workers" -ge 2 ] || _vra_workers=2
	printf '%s' "--maxWorkers=$_vra_workers"
}
