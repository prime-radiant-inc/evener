#!/usr/bin/env bash
# fuzz-drive-selftest.sh — offline, deterministic test of scripts/fuzz-drive.sh.
#
# fuzz-drive makes live provider calls, so it cannot be exercised directly in a
# gate. This runs the real driver against a THROWAWAY git repo with the serf,
# harvester, and gh binaries replaced by controllable stubs and backoff sleeps
# made instant, and asserts the driver's contract: it drives every task x
# provider in isolated working dirs with recording on, retries transient
# failures, skips a misconfigured provider, harvests once, stages seeds on a
# branch (inspect-first) and only opens a PR with --pr, and honors --runs /
# --no-harvest.
set -uo pipefail

script="$(cd "$(dirname "$0")" && pwd)/fuzz-drive.sh"
checks=0 fails=0
ok() { checks=$((checks + 1)); printf '  ok: %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL: %s\n' "$1"; }

# new_repo builds a throwaway git repo with fuzz-drive.sh, a 2-task corpus, and
# the stub binaries; echoes its path. Each scenario gets a fresh one.
new_repo() {
	local r; r="$(mktemp -d "${TMPDIR:-/tmp}/fuzz-drive-test.XXXXXX")"
	mkdir -p "$r/scripts" "$r/fuzz/drive-tasks" "$r/bin"
	cp "$script" "$r/scripts/fuzz-drive.sh"
	printf 'normal task one\n' >"$r/fuzz/drive-tasks/01-a.txt"
	printf 'normal task two\n' >"$r/fuzz/drive-tasks/02-b.txt"
	( cd "$r" && git init -q && git config user.email t@t && git config user.name t \
		&& git add -A && git commit -qm init )

	# serf stub: records env + cwd per invocation; emits a recording line; fails
	# transiently once for a TRANSIENT task; fails non-transiently for badprovider.
	cat >"$r/bin/serf" <<STUB
#!/usr/bin/env bash
model=""; task=""
while [ \$# -gt 0 ]; do case "\$1" in --model) model="\$2"; shift 2;; --max-rounds|--reasoning-effort) shift 2;; --verbose) shift;; *) task="\$1"; shift;; esac; done
echo "RECORD=\${SERF_FUZZ_RECORD-} STATE=\${SERF_STATE_DIR-} CWD=\$PWD MODEL=\$model" >>"$r/serf-invocations.log"
if printf '%s' "\$model" | grep -q badprovider; then echo "unknown instance: badprovider" >&2; exit 1; fi
if printf '%s' "\$task" | grep -q TRANSIENT; then
  n=\$(cat "$r/transient.n" 2>/dev/null || echo 0); echo \$((n+1)) >"$r/transient.n"
  if [ "\$n" -eq 0 ]; then echo "Error: 429 rate limit exceeded" >&2; exit 1; fi
fi
printf 'data: {"x":1}\n\n' >>"\${SERF_STATE_DIR}/api-raw.jsonl"
exit 0
STUB
	# harvest stub: records its args; writes a fake new seed so git sees a change.
	cat >"$r/bin/harvest" <<STUB
#!/usr/bin/env bash
echo "HARVEST \$*" >>"$r/harvest-invocations.log"
out="$r"; while [ \$# -gt 0 ]; do case "\$1" in --out-root) out="\$2"; shift 2;; *) shift;; esac; done
mkdir -p "\$out/llm/testdata/fuzz/FuzzParseSSE"
printf 'go test fuzz v1\n[]byte("data:x")\n' >"\$out/llm/testdata/fuzz/FuzzParseSSE/seed_$RANDOM"
exit 0
STUB
	# gh stub: records a pr-create invocation.
	cat >"$r/bin/gh" <<STUB
#!/usr/bin/env bash
[ "\$1" = "pr" ] && echo "PR \$*" >>"$r/gh-invocations.log"
echo "https://example/pr/1"; exit 0
STUB
	chmod +x "$r/bin/serf" "$r/bin/harvest" "$r/bin/gh"
	echo "$r"
}

run_drive() { # repo, extra args...
	local r="$1"; shift
	SERF_FUZZ_SERF_BIN="$r/bin/serf" \
	SERF_FUZZ_HARVEST_BIN="$r/bin/harvest" \
	SERF_FUZZ_GH="$r/bin/gh" \
	SERF_FUZZ_DRIVE_SLEEP=true \
		bash "$r/scripts/fuzz-drive.sh" "$@" >"$r/drive.out" 2>"$r/drive.err"
}

echo "== scenario A: happy path (1 provider x 2 tasks, inspect-first) =="
r="$(new_repo)"
run_drive "$r" --providers "openai/gpt-5.4-mini"; rc=$?
[ "$rc" -eq 0 ] && ok "exit 0" || bad "exit $rc (see $r/drive.err)"
n=$(grep -c RECORD= "$r/serf-invocations.log" 2>/dev/null)
[ "$n" -eq 2 ] && ok "drove 2 runs" || bad "ran $n times, want 2"
grep -q 'RECORD=1 STATE=' "$r/serf-invocations.log" && ok "recording on (SERF_FUZZ_RECORD=1 + state dir)" || bad "recorder env not set"
distinct=$(awk '{print $3}' "$r/serf-invocations.log" | sort -u | wc -l)
[ "$distinct" -eq 2 ] && ok "isolated cwd per run" || bad "cwd not isolated ($distinct distinct)"
[ -f "$r/harvest-invocations.log" ] && ok "harvest invoked" || bad "harvest not invoked"
grep -q -- "--state-dir" "$r/harvest-invocations.log" && grep -q -- "--out-root $r" "$r/harvest-invocations.log" && ok "harvest got --state-dir + --out-root" || bad "harvest args wrong"
( cd "$r" && git rev-parse --verify -q "fuzz/drive-corpus-$(git rev-parse --short main)" >/dev/null ) && ok "seeds staged on a branch" || bad "no seed branch created"
[ ! -f "$r/gh-invocations.log" ] && ok "no PR by default (inspect-first)" || bad "opened a PR without --pr"

echo "== scenario B: --pr opens a PR (with an origin to push to) =="
r="$(new_repo)"
( cd "$r" && git init -q --bare "$r/origin.git" && git remote add origin "$r/origin.git" )
run_drive "$r" --providers "openai/gpt-5.4-mini" --pr
[ -f "$r/gh-invocations.log" ] && grep -q 'PR pr create' "$r/gh-invocations.log" && ok "--pr ran gh pr create" || bad "--pr did not open a PR"

echo "== scenario C: transient failure retries then succeeds =="
r="$(new_repo)"
printf 'do something TRANSIENT here\n' >"$r/fuzz/drive-tasks/03-flaky.txt"
run_drive "$r" --providers "openai/gpt-5.4-mini" --retries 3
calls=$(grep -c 'CWD=' "$r/serf-invocations.log")
[ "$calls" -ge 4 ] && ok "retried the transient task (>=4 serf calls for 3 tasks)" || bad "no retry (only $calls calls)"
grep -qi 'transient failure' "$r/drive.err" && ok "logged a backoff retry" || bad "no backoff logged"

echo "== scenario D: non-transient first failure skips the provider =="
r="$(new_repo)"
run_drive "$r" --providers "badprovider/x openai/gpt-5.4-mini"
bad_runs=$(grep -c 'badprovider' "$r/serf-invocations.log" 2>/dev/null)
[ "$bad_runs" -eq 1 ] && ok "badprovider skipped after 1 failed run" || bad "badprovider ran $bad_runs times, want 1"
grep -q 'skipping the rest of this provider' "$r/drive.err" && ok "logged the provider skip" || bad "no skip logged"
[ -f "$r/harvest-invocations.log" ] && ok "still harvested the good provider's traffic" || bad "did not harvest after a partial run"

echo "== scenario E: --runs caps total runs =="
r="$(new_repo)"
run_drive "$r" --providers "openai/gpt-5.4-mini" --runs 1
n=$(grep -c RECORD= "$r/serf-invocations.log")
[ "$n" -eq 1 ] && ok "--runs 1 ran exactly one task" || bad "ran $n, want 1"

echo "== scenario F: --no-harvest drives but does not harvest =="
r="$(new_repo)"
run_drive "$r" --providers "openai/gpt-5.4-mini" --no-harvest
[ ! -f "$r/harvest-invocations.log" ] && ok "--no-harvest skipped harvest" || bad "harvested despite --no-harvest"

echo
echo "fuzz-drive-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
