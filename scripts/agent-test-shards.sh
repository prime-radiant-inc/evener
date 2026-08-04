#!/usr/bin/env bash
# agent-test-shards.sh — run the agent package's tests as cost-balanced shards.
#
# WHY: the agent package is one test binary of ~2750 tests. Run as a single
# invocation its wall time is ~26-32s, and raising -parallel does not help: the
# suite's real work is only ~13s of user CPU, so past about 6 concurrent tests the
# extra slots buy nothing and double the kernel time in scheduler churn.
#
# What does help is more OS processes, each modestly parallel. Measured on an idle
# 10-core box:
#
#   1 invocation, -parallel 32                 ~32s
#   2 shards split by topic (git / rest)       ~26s
#   3 shards x -parallel 3, cost-balanced      ~22s
#   4 shards x -parallel 3, cost-balanced      ~21s   <- default
#   6 shards x -parallel 3, cost-balanced      ~22s
#
# BALANCE is what matters, not shard count. The first version of this script split
# topically (real-git tests vs the rest) and left 25s of the 26s in one shard;
# weighting by measured cost is what took it to 21s. Past ~4 shards the returns
# flatten, because each shard's own floor (~7-9s) starts to dominate.
#
# HOW THE WEIGHTS ARE DERIVED: a CACHED survey, refreshed only when the set of
# tests changes.
#
# Surveying on every run is self-defeating — the survey is itself a full suite pass
# (~28s), so it costs more than the sharding saves. But a checked-in profile would
# silently rot: a new slow test would keep landing in whichever shard its name
# sorted into, and the balance would decay invisibly.
#
# So the survey is cached under the build cache, keyed by the sorted list of test
# names. Add, rename, or remove a test and the key changes and the survey re-runs
# once; otherwise every run reuses it and pays nothing. Costs are stable across
# runs in a way that name-order is not, and being ~23% high (vs. the isolated cost
# scripts/test-cost.sh measures) is well inside what bin-packing needs.
#
# USAGE:
#   scripts/agent-test-shards.sh [go-test-flags...]
#     scripts/agent-test-shards.sh -short -count=1
#
#   AGENT_SHARD_COUNT      number of shards (default 4)
#   AGENT_SHARD_PARALLEL   -parallel within each shard (default 3)
#   AGENT_SHARD_SKIP       regex of tests to skip in every shard
#   AGENT_SHARD_NO_SURVEY  1 = ignore the cache and weight every test equally
#                          (loses balance; for debugging the harness itself)
#   AGENT_SHARD_RESURVEY   1 = force the survey to re-run even on a cache hit
#
# Every test lands in exactly one shard, and the script proves that before running
# anything: a filter bug that dropped tests would otherwise present as a faster,
# still-green suite.
#
# OUTPUT: one PASS/FAIL line per shard with wall time. Non-zero exit if any shard
# fails or the partition check finds a discrepancy.
set -uo pipefail

cd "$(dirname "$0")/../agent" || { echo "agent-test-shards: no agent dir" >&2; exit 2; }

flags="$*"
shards=${AGENT_SHARD_COUNT:-4}
par=${AGENT_SHARD_PARALLEL:-3}
skip=${AGENT_SHARD_SKIP:-}
noSurvey=${AGENT_SHARD_NO_SURVEY:-0}
logdir="$(mktemp -d -t agent-test-shards.XXXXXX)"
logdir="$(cd "$logdir" && pwd -P)"

# The reclaimer cannot use the shard directory mtime as a liveness signal:
# shard output is appended below it, and appending an existing log does not
# update the directory. Keep an explicit heartbeat in the directory while
# this run owns it so a long-running shard remains protected.
heartbeat="$logdir/.heartbeat"
heartbeat_pid=""
normal_completion=0
cleanup_done=0
pids=()
names=()
: >"$heartbeat"
heartbeat_loop() {
	local parent_pid="$1" sleep_pid=""
	stop_heartbeat() {
		[ -n "$sleep_pid" ] && kill -TERM "$sleep_pid" 2>/dev/null || :
		exit 143
	}
	trap stop_heartbeat HUP INT TERM
	while kill -0 "$parent_pid" 2>/dev/null && touch "$heartbeat" 2>/dev/null; do
		sleep 5 &
		sleep_pid="$!"
		wait "$sleep_pid" 2>/dev/null || :
		sleep_pid=""
	done
	rm -f "$heartbeat" 2>/dev/null || :
}
heartbeat_loop "$$" &
heartbeat_pid="$!"

process_descendants() {
	local parent="$1" child
	for child in $(ps -axo pid=,ppid= 2>/dev/null | awk -v parent="$parent" '$2 == parent {print $1}'); do
		process_descendants "$child"
		printf '%s\n' "$child"
	done
}

stop_heartbeat_process() {
	[ -n "$heartbeat_pid" ] || return 0
	kill -TERM "$heartbeat_pid" 2>/dev/null || :
	wait "$heartbeat_pid" 2>/dev/null || :
	heartbeat_pid=""
}

stop_shard_processes() {
	local pid descendant
	local -a descendants=()
	for pid in ${pids[@]+"${pids[@]}"}; do
		[ -n "$pid" ] || continue
		for descendant in $(process_descendants "$pid"); do
			descendants+=("$descendant")
		done
	done
	for descendant in ${descendants[@]+"${descendants[@]}"} ${pids[@]+"${pids[@]}"}; do
		[ -n "$descendant" ] && kill -TERM "$descendant" 2>/dev/null || :
	done
	for pid in ${pids[@]+"${pids[@]}"}; do
		[ -n "$pid" ] && wait "$pid" 2>/dev/null || :
	done
	pids=()
}

cleanup() {
	local status="$?"
	trap - EXIT
	trap - HUP INT TERM
	[ "$cleanup_done" -eq 0 ] || exit "$status"
	cleanup_done=1
	stop_shard_processes
	stop_heartbeat_process
	rm -f "$heartbeat" 2>/dev/null || :
	if [ "$normal_completion" -eq 1 ] && [ "$status" -eq 0 ]; then
		rm -rf "$logdir"
	else
		printf 'full logs: %s\n' "$logdir" >&2
	fi
	exit "$status"
}

interrupted() {
	local status="$1" signal="$2"
	printf 'agent-test-shards.sh: interrupted by %s\n' "$signal" >&2
	exit "$status"
}

trap cleanup EXIT
trap 'interrupted 129 SIGHUP' HUP
trap 'interrupted 130 SIGINT' INT
trap 'interrupted 143 SIGTERM' TERM

build="$logdir/agent.test"
if ! go test -c -o "$build" . >"$logdir/build.log" 2>&1; then
	echo "agent-test-shards: build failed"; cat "$logdir/build.log"; exit 1
fi

# Survey, cached by the identity of the test set. On a hit this costs nothing; on
# a miss it pays one suite pass and every later run reuses it.
cacheDir="${AGENT_SHARD_CACHE_DIR:-$(go env GOCACHE)/serf-agent-shards}"
mkdir -p "$cacheDir" 2>/dev/null
testSetKey="$("$build" -test.list '.*' 2>/dev/null | sort | shasum -a 256 | cut -c1-16)"
cachedSurvey="$cacheDir/survey-$testSetKey.log"

if [ "$noSurvey" -eq 0 ]; then
	if [ "${AGENT_SHARD_RESURVEY:-0}" -eq 0 ] && [ -s "$cachedSurvey" ]; then
		cp "$cachedSurvey" "$logdir/survey.log"
	else
		printf 'agent-test-shards: surveying test costs (one-time for this test set)\n'
		surveyArgs=(-test.count=1 -test.parallel 6 -test.run '^(Test|Example)' -test.v)
		[ -n "$skip" ] && surveyArgs+=(-test.skip "$skip")
		case " $flags " in *" -short "*) surveyArgs+=(-test.short) ;; esac
		if ! "$build" "${surveyArgs[@]}" >"$logdir/survey.log" 2>&1; then
			echo "agent-test-shards: the survey pass failed — the suite is red" >&2
			grep -E "^(--- FAIL|panic:)" "$logdir/survey.log" | head -20 >&2
			echo "full log: $logdir/survey.log" >&2
			exit 1
		fi
		cp "$logdir/survey.log" "$cachedSurvey" 2>/dev/null
	fi
fi

# Partition the surveyed tests into $shards balanced groups, one name-list each.
# Longest-processing-time-first greedy: optimal enough, and the weights are
# approximate to begin with.
if ! python3 - "$logdir" "$shards" "$noSurvey" "$build" > "$logdir/plan.txt" <<'PY'
import re, subprocess, sys

logdir, shards, noSurvey, build = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]

weights = {}
if not noSurvey:
    for line in open(f'{logdir}/survey.log', errors='replace'):
        m = re.match(r'--- (?:PASS|SKIP): (\S+) \(([0-9.]+)s\)', line.strip())
        if m and '/' not in m.group(1):
            weights[m.group(1)] = float(m.group(2))

if not weights:
    # No survey, or it measured nothing: weight every test equally. This still
    # partitions correctly, it is just unbalanced.
    out = subprocess.run([build, '-test.list', '.*'],
                         capture_output=True, text=True).stdout
    weights = {l.strip(): 1.0 for l in out.splitlines()
               if re.match(r'^(Test|Example)', l.strip())}

if not weights:
    print('agent-test-shards: found no tests to shard', file=sys.stderr)
    sys.exit(1)

bins = [[] for _ in range(shards)]
load = [0.0] * shards
for name, cost in sorted(weights.items(), key=lambda kv: -kv[1]):
    i = load.index(min(load))
    bins[i].append(name)
    load[i] += cost

# Partition proof: every surveyed test in exactly one shard, and nothing invented.
placed = [n for b in bins for n in b]
if len(placed) != len(set(placed)) or set(placed) != set(weights):
    print('agent-test-shards: partition is not a bijection over the test set',
          file=sys.stderr)
    sys.exit(1)
if any(not b for b in bins):
    print(f'agent-test-shards: asked for {shards} shards but only '
          f'{sum(1 for b in bins if b)} are non-empty; lower AGENT_SHARD_COUNT',
          file=sys.stderr)
    sys.exit(1)

for i, b in enumerate(bins):
    with open(f'{logdir}/shard{i}.names', 'w') as fh:
        fh.write('\n'.join(sorted(b)))
    print(f'{i} {len(b)} {load[i]:.1f}')
PY
then
	echo "agent-test-shards: partitioning failed" >&2
	exit 1
fi

shardCount="$(wc -l < "$logdir/plan.txt" | tr -d ' ')"
printf 'agent-test-shards: %s shards, -parallel %s each\n' "$shardCount" "$par"

# nameRegex <file> — anchored alternation over the names in file, so no name can
# prefix-match a different test.
nameRegex() {
	python3 - "$1" <<'PY'
import re, sys
print('^(' + '|'.join(re.escape(n) for n in open(sys.argv[1]).read().split()) + ')$')
PY
}

for i in $(seq 0 $((shardCount - 1))); do
	args=(-test.count=1 -test.parallel "$par" -test.run "$(nameRegex "$logdir/shard$i.names")")
	# Translate the caller's `go test` flags into the binary's -test.* spelling.
	for f in $flags; do
		case "$f" in
			-short|-race) args+=("-test.${f#-}") ;;
			-count=*) args+=("-test.count=${f#-count=}") ;;
			-v) args+=(-test.v) ;;
		esac
	done
	/usr/bin/time -p "$build" "${args[@]}" >"$logdir/shard$i.log" 2>&1 &
	pids+=("$!"); names+=("$i")
done

fail=0
for k in "${!pids[@]}"; do
	i="${names[$k]}"
	if wait "${pids[$k]}"; then
		printf 'PASS  agent:%-2s %8s (%s tests)\n' "$i" \
			"$(awk '/^real /{print $2"s"}' "$logdir/shard$i.log" | tail -1)" \
			"$(awk -v s="$i" '$1==s{print $2}' "$logdir/plan.txt")"
	else
		printf 'FAIL  agent:%-2s\n' "$i"; fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo
	echo "=== failing shard output ==="
	for i in $(seq 0 $((shardCount - 1))); do
		if grep -qE "^(FAIL|--- FAIL|panic:)" "$logdir/shard$i.log" 2>/dev/null; then
			echo "----- agent:$i -----"; cat "$logdir/shard$i.log"
		fi
	done
	echo
	echo "full logs: $logdir"
fi

if [ "$fail" -eq 0 ]; then
	normal_completion=1
fi
exit "$fail"
