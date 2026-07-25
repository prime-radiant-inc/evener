#!/usr/bin/env bash
# agent-test-shards.sh — run the agent package's tests as two concurrent shards.
#
# WHY: the agent package is one test binary of ~3550 tests, and ~117 of them
# drive real `git` subprocesses. Those two populations have opposite shapes: the
# git tests are syscall-bound (each spawns ~14 processes and spends most of its
# time waiting on them), the rest are CPU-bound. Run as one binary they share a
# single -parallel budget, so the CPU-bound bulk waits behind slots held by tests
# that are only blocked on fork/exec.
#
# Splitting them into two concurrently-running invocations of the SAME prebuilt
# binary lets the kernel overlap one population's waiting with the other's
# computing. Measured on an idle 10-core box:
#
#   one binary, -parallel 32          ~33 s
#   two shards,  -parallel 8 each     ~23 s
#
# Note the parallelism has to come DOWN, not up: at -parallel 24+ per shard the
# two shards oversubscribe each other and the win evaporates (~30 s).
#
# This deliberately does NOT move the tests into a separate package. They
# reference ~102 unexported agent identifiers, so a real package split would mean
# exporting all of them — a large production-API change to buy a scheduling
# effect that two -run filters already buy.
#
# USAGE:
#   scripts/agent-test-shards.sh [go-test-flags...]
#     scripts/agent-test-shards.sh -short -count=1
#
#   AGENT_SHARD_GIT_PARALLEL    -parallel for the git shard (default 8)
#   AGENT_SHARD_REST_PARALLEL   -parallel for the rest (default 32)
#   AGENT_SHARD_SKIP       regex of test names to skip in both shards
#
# The shard membership is DERIVED from the source, not hardcoded: any test whose
# body calls newWorktreeRepo* is a git test. A new git test therefore joins the
# right shard automatically, and the script verifies the two shards are
# exhaustive and disjoint before trusting them — a regex split that silently
# dropped tests would look like a passing, faster suite.
#
# OUTPUT: one PASS/FAIL line per shard with wall time. Exits non-zero if either
# shard fails or if the membership check finds a gap.
set -uo pipefail

cd "$(dirname "$0")/../agent" || { echo "agent-test-shards: no agent dir" >&2; exit 2; }

flags="$*"
# The two shards want OPPOSITE parallelism, which is the whole reason splitting
# them helps. The git shard is syscall-bound: a high -parallel just piles up
# concurrent fork/exec and it peaks around 8. The rest is CPU-bound and scales to
# ~32 on a 10-core box (its tests are short and mostly waiting on nothing).
# One shared budget cannot serve both, and the unsharded suite had to pick one.
gitPar=${AGENT_SHARD_GIT_PARALLEL:-8}
restPar=${AGENT_SHARD_REST_PARALLEL:-32}
skip=${AGENT_SHARD_SKIP:-}
logdir="$(mktemp -d -t agent-test-shards.XXXXXX)"

# gitTestNames prints every test whose body calls the real-git harness.
gitTestNames() {
	python3 - <<'PY'
import glob, re
names = []
for path in sorted(glob.glob('*_test.go')):
    src = open(path).read()
    if 'newWorktreeRepo' not in src:
        continue
    for m in re.finditer(r'^func (Test\w+)\(t \*testing\.T\) \{((?:.|\n)*?)(?=\nfunc |\Z)', src, re.M):
        if 'newWorktreeRepo' in m.group(2):
            names.append(m.group(1))
print('\n'.join(sorted(set(names))))
PY
}

names="$(gitTestNames)"
if [ -z "$names" ]; then
	echo "agent-test-shards: found no real-git tests; refusing to shard on an empty set" >&2
	exit 2
fi
# Anchor each name so a prefix cannot pull in an unrelated test.
gitRe="^($(printf '%s' "$names" | tr '\n' '|' | sed 's/|$//'))\$"

build="$logdir/agent.test"
if ! go test -c -o "$build" . >"$logdir/build.log" 2>&1; then
	echo "agent-test-shards: build failed"; cat "$logdir/build.log"; exit 1
fi

# Shard B is "everything that is not shard A", plus any caller-supplied skip, so
# the two are disjoint by construction.
shardBSkip="$gitRe"
[ -n "$skip" ] && shardBSkip="($gitRe)|($skip)"

# Membership guard: the shards must together select exactly the whole suite, so a
# filter bug cannot silently drop tests and look like a faster green run. Counted
# from the source rather than by running the binary — a dry run costs as much as
# the suite itself, and -test.list ignores -test.run so it cannot answer this.
membershipCheck() {
	python3 - "$1" <<'PY2'
import glob, re, sys
gitNames = set(sys.argv[1].split())
total = git = 0
for path in glob.glob('*_test.go'):
    src = open(path).read()
    for m in re.finditer(r'^func (Test\w+)\(t \*testing\.T\) \{', src, re.M):
        total += 1
        if m.group(1) in gitNames:
            git += 1
    total += len(re.findall(r'^func (Example\w*)\(\)', src, re.M))
missing = sorted(gitNames - {
    m.group(1)
    for path in glob.glob('*_test.go')
    for m in re.finditer(r'^func (Test\w+)\(t \*testing\.T\) \{', open(path).read(), re.M)
})
if missing:
    print('unlocatable git tests: ' + ' '.join(missing), file=sys.stderr)
    sys.exit(1)
print(f'{git} {total - git}')
PY2
}

counts="$(membershipCheck "$names")" || {
	echo "agent-test-shards: shard membership check failed" >&2
	exit 1
}
a="${counts%% *}"
b="${counts##* }"
if [ -z "$a" ] || [ -z "$b" ] || [ "$a" -eq 0 ] || [ "$b" -eq 0 ]; then
	printf 'agent-test-shards: refusing to shard on a degenerate split (A=%s B=%s)\n' "$a" "$b" >&2
	exit 1
fi

runShard() {
	local name="$1" runRe="$2" skipRe="$3"
	local shardPar="$4"
	local args=(-test.parallel "$shardPar" -test.run "$runRe")
	[ -n "$skipRe" ] && args+=(-test.skip "$skipRe")
	# Translate the caller's `go test` flags to the binary's -test.* spelling.
	local f
	for f in $flags; do
		case "$f" in
			-short|-race) args+=("-test.${f#-}") ;;
			-count=*) args+=("-test.count=${f#-count=}") ;;
			-v) args+=(-test.v) ;;
		esac
	done
	/usr/bin/time -p "$build" "${args[@]}" >"$logdir/$name.log" 2>&1
}

runShard git "$gitRe" "$skip" "$gitPar" &
gitPID=$!
runShard rest '^(Test|Example)' "$shardBSkip" "$restPar" &
restPID=$!

fail=0
for pair in "git:$gitPID" "rest:$restPID"; do
	name="${pair%%:*}"; pid="${pair##*:}"
	if wait "$pid"; then
		printf 'PASS  agent:%-5s %s (%s tests)\n' "$name" \
			"$(awk '/^real /{print $2"s"}' "$logdir/$name.log" | tail -1)" \
			"$([ "$name" = git ] && echo "$a" || echo "$b")"
	else
		printf 'FAIL  agent:%-5s\n' "$name"; fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo
	echo "=== failing shard output ==="
	for name in git rest; do
		if grep -qE "^(FAIL|--- FAIL|panic:)" "$logdir/$name.log" 2>/dev/null; then
			echo "----- agent:$name -----"; cat "$logdir/$name.log"
		fi
	done
	echo
	echo "full logs: $logdir"
fi

exit "$fail"
