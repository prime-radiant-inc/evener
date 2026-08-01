# Test-Gate Runtime Reduction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run every existing post-merge test exactly once while reducing the idle-box gate median to 100 seconds or less.

**Architecture:** `scripts/run-module-tests.sh` remains the single scheduler. Two opt-in inputs change scheduling without changing coverage: `ROOT_FULL=1` removes exact `-short` only from the protected root wave, and `SELFTEST=1` starts `make selftest` after that wave and joins it alongside wave two. The Makefile enables only the self-test stream for ordinary `make test`; the optimized controller gate additionally enables full-root mode and drops the duplicate standalone root command after legacy equivalence is proven.

**Tech Stack:** Bash 3.2-compatible shell, GNU Make, Go test runner, fixture-driven shell self-tests.

## Global Constraints

- Never touch port 9180, `~/.serf/`, or `~/.local/state/serf/`.
- Preserve Jesse's uncommitted AskDock CSS/layoutguard files and `notes.txt`; never stage or edit them.
- Do not use `git stash`, `npm ci`, `git checkout <file>`, or directory-wide `git add`.
- Never widen a timeout or use a fixed sleep in place of an awaitable event.
- Keep wave one root-only; do not add Go modules or script self-tests to it.
- Keep all script self-tests, frontend checks, Go modules, and full-root coverage.
- A gate verdict is the command's bare exit status, never grep output or pipeline status.
- Use `apply_patch` for every repository edit and stage only named files.
- Commit each task separately with a substantive message.

## File map

- `scripts/run-module-tests.sh`: owns module flags, wave scheduling, concurrent auxiliary streams, aggregate verdicts, and failure-log replay.
- `scripts/run-module-tests-selftest.sh`: owns offline behavioral coverage of runner arguments, ordering, name collisions, verdicts, and failure evidence.
- `Makefile`: owns the public `make selftest` and `make test` entry points.
- `docs/testing.md`: owns the durable post-merge gate command contract.
- `docs/superpowers/specs/2026-08-01-test-gate-runtime-design.md`: records the approved design and measured outcome.
- `.superpowers/kata-fleet-ledger.md`: records controller evidence and the new handoff state.
- `/private/tmp/claude-501/-Users-jesse-prime-radiant-toil-suite-serf--claude-worktrees-webui-workspace-shell/a25329dd-a50c-4efe-bd9b-e36a57c5e538/scratchpad/gates.sh`: session-local controller helper updated only after equivalence and timing pass.

---

### Task 1: Full-root flag routing

**Files:**
- Modify: `scripts/run-module-tests-selftest.sh:20-48,171-180`
- Modify: `scripts/run-module-tests.sh:90-160`

**Interfaces:**
- Consumes: runner arguments in the existing `flags="$*"` representation.
- Produces: environment input `ROOT_FULL` (`0` by default; nonzero enables full root) and `module_test_flags <module>` returning the word-split flags for that module.

- [ ] **Step 1: Add word-level fixture assertions and the failing full-root case**

Add these helpers beside the existing assertions in `scripts/run-module-tests-selftest.sh`:

```bash
assert_has_word() {
	case " $1 " in
		*" $2 "*) ok "$3" ;;
		*) bad "$3 (missing word '$2' in '$1')" ;;
	esac
}

assert_not_has_word() {
	case " $1 " in
		*" $2 "*) bad "$3 (unexpected word '$2' in '$1')" ;;
		*) ok "$3" ;;
	esac
}

arguments_for() {
	awk -F '\t' -v stream="$1" '$1 == stream { print $2; exit }' "$state/calls"
}
```

Add this case immediately after the all-pass case:

```bash
new_case
out="$case_dir/root-full.out"
if run_tests ". agent" "$out" ROOT_FULL=1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "full-root run exits zero"
root_args="$(arguments_for .)"
agent_args="$(arguments_for agent)"
assert_not_has_word "$root_args" "-short" "full-root mode removes exact -short from root"
assert_has_word "$root_args" "-count=1" "full-root mode preserves root's other flags"
assert_has_word "$agent_args" "-short" "full-root mode keeps -short on non-root modules"
assert_has_word "$agent_args" "-count=1" "full-root mode preserves non-root flags"
```

- [ ] **Step 2: Run the self-test and verify the intended RED**

Run:

```bash
scripts/run-module-tests-selftest.sh
```

Expected: exit 1 with exactly the new assertion reporting that root unexpectedly contains the word `-short`; the existing checks remain green.

- [ ] **Step 3: Implement module-specific flag selection**

In `scripts/run-module-tests.sh`, define the input with the other runner controls:

```bash
ROOT_FULL=${ROOT_FULL:-0}
```

After `flags="$*"`, add:

```bash
module_test_flags() {
	local m="$1" flag selected=""
	if [ "$m" != "." ] || [ "$ROOT_FULL" -eq 0 ]; then
		printf '%s' "$flags"
		return
	fi
	for flag in $flags; do
		[ "$flag" = "-short" ] && continue
		selected="$selected $flag"
	done
	printf '%s' "${selected# }"
}
```

At the start of `run_module`, capture `local test_flags` and use it for every Go-test path:

```bash
local m="$1" extra="$2" test_flags
test_flags="$(module_test_flags "$m")"
```

Replace all four test invocations' `$flags` arguments—including the agent shard and agent subpackage commands—with `$test_flags`. Do not change `extra`, package selection, fuzz exclusions, or wave membership.

- [ ] **Step 4: Run focused verification**

Run:

```bash
scripts/run-module-tests-selftest.sh
```

Expected: exit 0, all checks pass, and output contains no warning or failure.

Then run:

```bash
git diff --check -- scripts/run-module-tests.sh scripts/run-module-tests-selftest.sh
```

Expected: exit 0 with no output.

- [ ] **Step 5: Commit the full-root flag contract**

```bash
git add scripts/run-module-tests.sh scripts/run-module-tests-selftest.sh
git commit -m "test: let the root wave run the full suite" \
  -m "Add an opt-in ROOT_FULL runner mode that removes only exact -short from the protected root module while preserving every other module and flag. Pin the behavior through the fake-go argument seam so the test exercises the command actually invoked rather than matching rendered shell source."
```

---

### Task 2: Wave-two self-test stream

**Files:**
- Modify: `scripts/run-module-tests-selftest.sh:112-149,171-247`
- Modify: `scripts/run-module-tests.sh:40-88,184-240`

**Interfaces:**
- Consumes: `${MAKE:-make}`, `run_wave`, `logpath`, `fail`, and `failed_modules` from the runner.
- Produces: environment input `SELFTEST` (`0` by default; nonzero enables the stream), reserved stream name `selftest`, and `finish_stream <name> <pid>` for auxiliary-stream verdicts.

- [ ] **Step 1: Extend the fixture with behavior events**

In the fake `go`, after recording the call and before failure injection, add:

```bash
if [ "$module" = "agent" ] && [ "${FAKE_AGENT_AWAITS_SELFTEST:-0}" -ne 0 ]; then
	attempt=0
	while [ ! -f "$FAKE_STATE/selftest.started" ]; do
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 200 ]; then
			printf 'agent started without the selftest stream\n' >&2
			exit 3
		fi
		sleep 0.01
	done
fi
```

Immediately before the fake `go` exits successfully, add:

```bash
[ "$module" = "." ] && : >"$FAKE_STATE/root.finished"
```

Replace the fake `make` body with target-aware behavior:

```bash
#!/usr/bin/env bash
set -u
stream=web
[ "${1:-}" = "selftest" ] && stream=selftest
printf '%s\t%s\n' "$stream" "$*" >>"$FAKE_STATE/calls"
if [ "$stream" = "selftest" ]; then
	if [ "${FAKE_SELFTEST_REQUIRES_ROOT:-0}" -ne 0 ] && [ ! -f "$FAKE_STATE/root.finished" ]; then
		printf 'selftest started before root finished\n' >&2
		exit 4
	fi
	: >"$FAKE_STATE/selftest.started"
	printf 'selftest-stdout: tooling contracts passed\n'
	if [ "${FAKE_SELFTEST_FAIL:-0}" -ne 0 ]; then
		printf 'selftest-stderr: tooling contract failed\n' >&2
		exit 1
	fi
	exit 0
fi
printf 'web-stdout: 5308 tests passed\n'
if [ "${FAKE_WEB_FAIL:-0}" -ne 0 ]; then
	printf 'web-stderr: 1 test failed\n' >&2
	exit 1
fi
exit 0
```

The bounded loop is only a tripwire around the awaited `selftest.started` event; the event, not elapsed time, is the synchronization mechanism.

- [ ] **Step 2: Add three failing self-test stream cases**

Add the ordering/overlap case:

```bash
new_case
out="$case_dir/selftest-overlap.out"
if FAKE_SELFTEST_REQUIRES_ROOT=1 FAKE_AGENT_AWAITS_SELFTEST=1 \
	run_tests ". agent" "$out" SELFTEST=1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "selftest waits for root and overlaps wave two"
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" ". agent selftest web" "selftest reports once after wave two"
assert_one_verdict_per_name "$out" "selftest overlap reports no name twice"
assert_eq "$(started_streams)" ". agent selftest web" "selftest overlap starts every requested stream"
```

Add the failure-evidence case:

```bash
new_case
out="$case_dir/selftest-failure.out"
if FAKE_SELFTEST_FAIL=1 run_tests "agent" "$out" SELFTEST=1; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "a failing selftest stream exits nonzero"; else bad "a failing selftest stream unexpectedly exits zero"; fi
assert_has "$out" "FAIL  selftest" "the selftest stream reports its own verdict"
assert_one_verdict_per_name "$out" "selftest failure reports no name twice"
assert_dump_nonempty "$out" "a failing selftest stream dumps output"
assert_has "$out" "----- selftest -----" "the selftest log is dumped under its own name"
assert_has "$out" "selftest-stderr: tooling contract failed" "the selftest diagnostic reaches the reader"
assert_not_has "$out" "----- agent -----" "the passing module is not dumped with selftest"
```

Add the wave-one failure coverage case:

```bash
new_case
out="$case_dir/root-failure-still-covers-later-streams.out"
if FAKE_TEST_FAIL="." run_tests ". agent" "$out" SELFTEST=1; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "root failure keeps the aggregate red"; else bad "root failure unexpectedly exits zero"; fi
assert_eq "$(verdicts "$out" | tr '\n' ' ' | sed 's/ *$//')" ". agent selftest web" "root failure still runs every later stream"
assert_eq "$(started_streams)" ". agent selftest web" "root failure does not skip later coverage"
assert_has "$out" "----- . -----" "the root failure is dumped"
assert_not_has "$out" "----- agent -----" "the later passing module is not dumped"
assert_not_has "$out" "----- selftest -----" "the later passing selftest is not dumped"
```

Add the collision case beside the existing web collisions:

```bash
new_case
out="$case_dir/selftest-name-conflict.out"
if run_tests "selftest" "$out" SELFTEST=1; then rc=0; else rc=$?; fi
assert_eq "$rc" "2" "a module colliding with the selftest stream is refused"
assert_eq "$(verdicts "$out" | wc -l | tr -d ' ')" "0" "the refused selftest collision reports no verdict"
assert_has "$out" "make selftest" "the refusal names the selftest entry point"
assert_has "$out" "SELFTEST=0" "the refusal names the explicit opt-out"
assert_eq "$(started_streams)" "" "the refused selftest collision starts no stream"
```

- [ ] **Step 3: Run the self-test and verify the intended RED**

Run:

```bash
scripts/run-module-tests-selftest.sh
```

Expected: exit 1 because no `selftest` stream exists yet. The overlap case reports the missing event/aggregate failure, the failure case cannot find `FAIL selftest`, and the collision case gets the wrong exit/result. Existing contracts remain green.

- [ ] **Step 4: Implement the self-test auxiliary stream**

Near `WEB`, define:

```bash
SELFTEST=${SELFTEST:-0}
```

Extend the preflight collision loop:

```bash
if [ "$SELFTEST" -ne 0 ] && [ "$m" = "selftest" ]; then
	echo "run-module-tests.sh: 'selftest' is the tooling stream's name, not a Go module." >&2
	echo "run-module-tests.sh: run 'make selftest' for tooling alone, or pass SELFTEST=0 to test a Go module named selftest." >&2
	exit 2
fi
```

Extract auxiliary-stream joining so web and selftest share one implementation:

```bash
finish_stream() {
	local name="$1" pid="$2" log
	log="$(logpath "$name")"
	if wait "$pid"; then
		printf 'PASS  %-8s %s\n' "$name" "$(awk '/^real /{print $2"s"}' "$log" | tail -1)"
	else
		printf 'FAIL  %-8s\n' "$name"
		fail=1
		failed_modules+=("$name")
	fi
}
```

Replace the bottom-level scheduling with:

```bash
run_wave $WAVE1

selftest_pid=""
if [ "$SELFTEST" -ne 0 ]; then
	/usr/bin/time -p "${MAKE:-make}" selftest >"$(logpath selftest)" 2>&1 &
	selftest_pid="$!"
fi

run_wave $WAVE2

[ -n "$selftest_pid" ] && finish_stream selftest "$selftest_pid"
[ -n "$web_pid" ] && finish_stream web "$web_pid"
```

Keep the frontend start before wave one. Delete only the duplicated inline web join block replaced by `finish_stream`.

- [ ] **Step 5: Run focused verification**

Run:

```bash
scripts/run-module-tests-selftest.sh
```

Expected: exit 0, all old and new checks pass, and output is pristine.

Then run:

```bash
git diff --check -- scripts/run-module-tests.sh scripts/run-module-tests-selftest.sh
```

Expected: exit 0 with no output.

- [ ] **Step 6: Commit the self-test stream**

```bash
git add scripts/run-module-tests.sh scripts/run-module-tests-selftest.sh
git commit -m "test: overlap tooling checks with wave two" \
  -m "Start the selftest stream only after the protected root wave, join it with the existing aggregate verdict machinery, and replay complete diagnostics on failure. Behavioral fixture events pin root isolation, wave-two overlap, reserved-name refusal, and failure evidence."
```

---

### Task 3: Wire `make test` to the scheduler

**Files:**
- Modify: `Makefile:152-185`

**Interfaces:**
- Consumes: `SELFTEST=1` from Task 2 and the existing standalone `selftest` target.
- Produces: `make test` with unchanged coverage but self-tests scheduled after wave one.

- [ ] **Step 1: Update the Makefile scheduling contract**

Revise the self-test comment to state that the runner starts this wave after protected wave one. Change:

```make
test: selftest
	@MODULES="$(GO_MODULES)" MAKE="$(MAKE)" $(MEMCAP) scripts/run-module-tests.sh -short -count=1
```

to:

```make
test:
	@MODULES="$(GO_MODULES)" SELFTEST=1 MAKE="$(MAKE)" $(MEMCAP) scripts/run-module-tests.sh -short -count=1
```

Do not alter `selftest`, `test-short`, `test-race`, or `test-web` coverage.

- [ ] **Step 2: Verify the standalone and integrated entry points**

Run each command directly and use its bare exit status:

```bash
scripts/run-module-tests-selftest.sh
make selftest
make test
```

Expected: all three exit 0. `make test` reports root first, then wave-two module verdicts, one `selftest` verdict, and one `web` verdict. No warning or failure output is permitted.

- [ ] **Step 3: Commit the Makefile integration**

```bash
git add Makefile
git commit -m "test: schedule selftests after the root wave" \
  -m "Route make test through the runner's SELFTEST stream instead of paying the tooling wave as a serial prerequisite. This preserves the standalone target and all coverage while keeping tooling work out of the latency-sensitive root window."
```

---

### Task 4: Prove equivalence, measure the optimized gate, and publish it

**Files:**
- Modify after successful measurement: `docs/testing.md`
- Modify after successful measurement: `docs/superpowers/specs/2026-08-01-test-gate-runtime-design.md`
- Modify after successful measurement: `.superpowers/kata-fleet-ledger.md`
- Modify after successful measurement: `/private/tmp/claude-501/-Users-jesse-prime-radiant-toil-suite-serf--claude-worktrees-webui-workspace-shell/a25329dd-a50c-4efe-bd9b-e36a57c5e538/scratchpad/gates.sh`

**Interfaces:**
- Consumes: `ROOT_FULL=1 make test`, legacy four-gate stack, idle-box process check.
- Produces: evidence that every old test remains covered, three optimized green timing samples, durable three-command gate documentation, and updated controller helper.

- [ ] **Step 1: Confirm the box is idle and the tree contains only expected changes**

Run:

```bash
pgrep -fl 'go test|vitest|run-module-tests|make test|make lint|make build'
git status --short
```

Expected: `pgrep` exits 1 with no matching test process. Git status shows only this task's tracked changes/commits plus Jesse's known AskDock/layoutguard files and `notes.txt`.

- [ ] **Step 2: Run one legacy equivalence cycle**

Run serially from the controller worktree, capturing each command's exit before reading its log:

```bash
make lint
make build
make test
go test ./...
```

Expected: four bare exit codes of 0 and pristine output. If the root gate fails specifically on `TestServeVerboseSurvivesAnUnreadStderr`, rerun that test solo once; two solo failures halt the train and keep a5a9 open.

- [ ] **Step 3: Run three optimized idle-box cycles**

For each cycle, recheck that no competing test process exists, then time these commands serially:

```bash
/usr/bin/time -p make lint
/usr/bin/time -p make build
/usr/bin/time -p env ROOT_FULL=1 make test
```

Capture each command's bare exit before inspecting timing output. Expected: all nine commands exit 0 with pristine output. Sum the three component wall times per cycle and use the median cycle total. Success requires median total at or below 100.00 seconds.

- [ ] **Step 4: Stop honestly if the target is missed**

If the median is above 100 seconds, do not change coverage, wave one, timeouts, agent sharding, or Vitest pooling under this plan. Record the measurements in the design and ledger, identify the new critical-path stream from its captured log, and return to Jesse with a new design proposal. Do not publish the three-command gate yet.

- [ ] **Step 5: Publish the proven gate contract**

Only when Step 3 succeeds:

- Add the three-command post-merge stack to `docs/testing.md`, including that `ROOT_FULL=1` runs the full root suite in protected wave one and `make test` owns the self-test/frontend/module streams.
- Change the design status to implemented and append the three sample totals, median, coverage-equivalence cycle, and measured reduction from both 167.39 seconds and the 200-second directive baseline.
- Add a new top handoff entry to `.superpowers/kata-fleet-ledger.md` with commit IDs, bare gate exits, sample logs/times, the a5a9 open/unmerged scope correction, and the new helper contract.
- Edit the controller helper so its test phase is `ROOT_FULL=1 make test` and remove only the standalone `go test ./...` phase.

- [ ] **Step 6: Verify and commit durable documentation**

Run:

```bash
git diff --check -- docs/testing.md docs/superpowers/specs/2026-08-01-test-gate-runtime-design.md .superpowers/kata-fleet-ledger.md
```

Expected: exit 0 with no output.

Stage only the named repository files and commit:

```bash
git add docs/testing.md docs/superpowers/specs/2026-08-01-test-gate-runtime-design.md .superpowers/kata-fleet-ledger.md
git commit -m "docs(testing): publish the single-pass merge gate" \
  -m "Record the legacy equivalence cycle and three idle optimized samples, then make ROOT_FULL=1 make test the sole owner of full-root, module, tooling, and frontend test coverage. Preserve lint and build as separate gates and retire only the duplicate standalone root test."
```

- [ ] **Step 7: Run the published helper once**

Run the updated session helper with a fresh log path. Expected: exactly three bare exit codes—lint, build, and test—all 0, with pristine captured output. Confirm Jesse's live files are still unstaged and unchanged.
