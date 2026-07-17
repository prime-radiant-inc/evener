# Aggregate Quiet Lint Implementation Plan

**Status:** Implemented and integrated. Project 4 was canceled by Jesse on
2026-07-17 and is not a prerequisite for the final program gate.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `make lint` run every existing non-fuzz Go module lint check with bounded concurrency, print only compact status when fully healthy while preserving the existing degraded missing-gitleaks warning, and replay every failed check's complete log followed by one deterministic summary.

**Architecture:** Add one Bash runner modeled on `scripts/run-module-tests.sh`: it receives the canonical module list through `MODULES`, executes `golangci-lint run ./...` in fixed-size waves controlled by `LINT_PARALLEL`, records statuses separately from private logs, and replays failures in input order. Keep lint-family ownership in the Makefile; a reusable Make recipe wrapper captures routine output from the non-module checks, discards it only after success, preserves the existing successful-but-degraded missing-gitleaks warning, and replays complete output unchanged on failure.

**Tech Stack:** GNU Make, macOS-compatible Bash (no associative arrays or `wait -n`), POSIX command-line utilities, `golangci-lint`, deterministic shell self-tests with fake executables and temporary module trees.

## Global Constraints

This spec does not:

- alter lint rules, tool versions, or module coverage;
- change `make test`, race, fuzz, or live-test gates;
- add a new build system;
- cache lint results;
- hide failure output;
- print per-module success lines;
- modify production Serf behavior.

- Treat every requirement outside `docs/superpowers/specs/2026-07-15-aggregate-quiet-lint-design.md` as a defect. Stop and ask Jesse instead of expanding the implementation scope.

## Prerequisite and Program Gate

This is Project 6 of 6 and its implementation is already integrated. The final
real `make lint` run remains a program-wide gate over all active projects.
Project 4 was canceled and is not part of that gate. Do not use Project 6 to
alter failures owned by another project.

---

## File Structure

- Create `scripts/run-module-lint.sh`: the only module scheduler; owns module defaults, the `LINT_PARALLEL` bound, child lifecycle, per-module status/log files, deterministic replay, summaries, and temporary-file cleanup.
- Create `scripts/run-module-lint-selftest.sh`: offline behavioral tests that run the executable runner and a copied real Makefile against fake `golangci-lint`, `go`, `git`, and secret-scan commands. It must inspect observed invocations and process state, never rendered shell or Make command text.
- Modify `Makefile:284-329`: wrap the five non-module lint families so routine successful chatter is discarded, the existing successful-but-degraded missing-gitleaks warning remains visible, and failed output is replayed; replace the serial `lint-golangci` loop with the new runner while passing `GO_MODULES` unchanged. Do not modify `GO_MODULES`, any test/race/fuzz target, command flags, tool versions, or the `lint` dependency list.

### Task 1: Aggregate module runner and executable contract tests

**Files:**
- Create: `scripts/run-module-lint-selftest.sh`
- Create: `scripts/run-module-lint.sh`

**Interfaces:**
- Consumes: `MODULES` as a whitespace-separated module list, defaulting exactly to `. agent llm auth envvars invariant identifier`; `LINT_PARALLEL` as a positive integer, defaulting to `4`; `golangci-lint` resolved from `PATH`.
- Produces: executable `scripts/run-module-lint.sh`; on success, exactly `lint: checking N modules` and `PASS lint (N modules, Ns)`; on failure, complete failed logs in `MODULES` order, an optional retained-log path before the summary, final `FAIL lint (F/N modules: ...)`, and a nonzero status.
- Process contract: at most `LINT_PARALLEL` direct child linters run simultaneously; `INT`, `TERM`, or `HUP` terminates and waits for active children before removing the temporary directory and returning nonzero.

- [ ] **Step 1: Capture the pre-implementation base commit in a private Git ref**

Capture the exact commit before either implementation commit. A private ref survives separate shells and makes the final audit independent of how many scoped correction commits are needed.

```bash
base_ref=refs/serf-plan-bases/aggregate-quiet-lint
if git show-ref --verify --quiet "$base_ref"; then
  echo "ref already exists; inspect it before resuming: $base_ref" >&2
  exit 1
fi
git update-ref "$base_ref" HEAD
git rev-parse "$base_ref"
```

Expected: a fresh run creates the ref and `git rev-parse` prints the current pre-implementation `HEAD`. A resumed run with an existing ref stops instead of silently moving the audit base. Do not move or delete this ref until Task 3's final audit is complete.

- [ ] **Step 2: Create the runner self-test harness and happy/multi-failure scenarios**

Create executable `scripts/run-module-lint-selftest.sh` with these helpers and scenarios. The fake linter derives its module from `PWD`, records every invocation, emits distinct stdout/stderr markers, and fails only modules named by `FAKE_FAIL_MODULES`; the assertions inspect those behavioral artifacts rather than source strings.

```bash
#!/usr/bin/env bash
# run-module-lint-selftest.sh — offline behavioral tests for module linting.
set -uo pipefail

runner="$(cd "$(dirname "$0")" && pwd)/run-module-lint.sh"
makefile="$(cd "$(dirname "$0")/.." && pwd)/Makefile"
work="$(mktemp -d -t serf-module-lint-selftest.XXXXXX)"
trap 'rm -rf "$work"' EXIT

checks=0
fails=0
ok() { checks=$((checks + 1)); printf 'ok   - %s\n' "$1"; }
bad() { checks=$((checks + 1)); fails=$((fails + 1)); printf 'FAIL - %s\n' "$1"; }
assert_eq() {
  if [ "$1" = "$2" ]; then ok "$3"; else bad "$3 (want '$2', got '$1')"; fi
}
assert_has() {
  if grep -qF -- "$2" "$1"; then ok "$3"; else bad "$3 (missing '$2')"; sed 's/^/    | /' "$1"; fi
}
assert_not_has() {
  if grep -qF -- "$2" "$1"; then bad "$3 (unexpected '$2')"; sed 's/^/    | /' "$1"; else ok "$3"; fi
}

new_case() {
  case_dir="$(mktemp -d "$work/case.XXXXXX")"
  repo="$case_dir/repo"
  state="$case_dir/state"
  bin="$case_dir/bin"
  mkdir -p "$repo" "$state" "$bin"
  for module in agent llm auth envvars invariant identifier one two three four five; do
    mkdir -p "$repo/$module"
  done
  cat >"$bin/golangci-lint" <<'FAKE_LINT'
#!/usr/bin/env bash
set -u
module="$(basename "$PWD")"
[ "$PWD" = "$FAKE_REPO" ] && module=.
printf '%s\t%s\n' "$module" "$*" >>"$FAKE_STATE/calls"
printf 'stdout:%s\n' "$module"
printf 'stderr:%s\n' "$module" >&2
case " ${FAKE_FAIL_MODULES:-} " in
  *" $module "*) exit 7 ;;
esac
exit 0
FAKE_LINT
  chmod +x "$bin/golangci-lint"
}

run_lint() {
  modules="$1"
  output="$2"
  shift 2
  (
    cd "$repo" || exit 1
    env TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
      MODULES="$modules" "$@" "$runner"
  ) >"$output" 2>&1
}

new_case
out="$case_dir/success.out"
if run_lint ". agent llm" "$out"; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "all-success exits zero"
assert_eq "$(wc -l <"$out" | tr -d ' ')" "2" "all-success has exactly two output lines"
assert_eq "$(sed -n '1p' "$out")" "lint: checking 3 modules" "all-success prints one start line"
case "$(sed -n '2p' "$out")" in
  "PASS lint (3 modules, "*"s)") ok "all-success prints one final PASS line" ;;
  *) bad "all-success PASS line has the wrong shape" ;;
esac
assert_not_has "$out" "stdout:" "successful stdout chatter is absent"
assert_not_has "$out" "stderr:" "successful stderr chatter is absent"
assert_eq "$(cut -f1 "$state/calls" | sort | tr '\n' ' ' | sed 's/ $//')" ". agent llm" "all requested modules ran"
assert_eq "$(cut -f2 "$state/calls" | sort -u)" "run ./..." "every module receives the unchanged golangci-lint argv"
assert_eq "$(find "$case_dir" -maxdepth 1 -type d -name 'serf-module-lint.*' | wc -l | tr -d ' ')" "0" "successful temporary logs are removed"

new_case
out="$case_dir/failure.out"
if FAKE_FAIL_MODULES="identifier llm" run_lint "identifier agent llm auth" "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "mixed failures exit nonzero"; else bad "mixed failures unexpectedly exit zero"; fi
assert_eq "$(cut -f1 "$state/calls" | sort | tr '\n' ' ' | sed 's/ $//')" "agent auth identifier llm" "every module runs after early failures"
assert_eq "$(cut -f2 "$state/calls" | sort -u)" "run ./..." "failed and passing modules use the same unchanged argv"
assert_has "$out" "stdout:identifier" "first failed module stdout is complete"
assert_has "$out" "stderr:identifier" "first failed module stderr is complete"
assert_has "$out" "stdout:llm" "second failed module stdout is complete"
assert_has "$out" "stderr:llm" "second failed module stderr is complete"
assert_not_has "$out" "stdout:agent" "passing module stdout stays hidden"
assert_not_has "$out" "stderr:auth" "passing module stderr stays hidden"
identifier_line="$(grep -nF -- '----- identifier -----' "$out" | cut -d: -f1)"
llm_line="$(grep -nF -- '----- llm -----' "$out" | cut -d: -f1)"
if [ "$identifier_line" -lt "$llm_line" ]; then ok "failure logs follow MODULES order"; else bad "failure logs are out of MODULES order"; fi
assert_eq "$(tail -n 1 "$out")" "FAIL lint (2/4 modules: identifier llm)" "one final summary names every failed module in order"
logdir="$(sed -n 's/^full logs: //p' "$out")"
assert_eq "$(find "$logdir" -type f -name '*.log' | wc -l | tr -d ' ')" "2" "only failed module logs are retained"
```

- [ ] **Step 3: Add setup, concurrency, and signal-cleanup scenarios to the same self-test**

Append these scenarios before the final self-test summary. They use a failing `mktemp`, a fake linter with an atomic directory lock plus start/release FIFOs, and an interruptible fake child. The `read -t 5` calls are bounded deadlock guards around explicit process notifications, not timing assertions or polling.

```bash
# Missing executable: run Bash's missing-command path once, retain that original
# diagnostic once, name every skipped module once, and launch no checks.
new_case
rm "$bin/golangci-lint"
out="$case_dir/missing.out"
(
  cd "$repo" || exit 1
  TMPDIR="$case_dir" PATH="/usr/bin:/bin" MODULES=". agent llm" "$runner"
) >"$out" 2>&1
rc=$?
if [ "$rc" -ne 0 ]; then ok "missing golangci-lint exits nonzero"; else bad "missing golangci-lint exits zero"; fi
assert_eq "$(grep -c 'command not found' "$out")" "1" "missing-command diagnostic appears once"
assert_eq "$(tail -n 1 "$out")" "FAIL lint (3 modules not checked: . agent llm)" "missing-command summary names every skipped module once"

# Temporary-log setup failure stops before any linter can launch.
new_case
cat >"$bin/mktemp" <<'FAKE_MKTEMP'
#!/usr/bin/env bash
printf 'mktemp: injected failure\n' >&2
exit 1
FAKE_MKTEMP
chmod +x "$bin/mktemp"
out="$case_dir/mktemp.out"
if run_lint ". agent" "$out"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "temporary-log creation failure exits nonzero"; else bad "temporary-log creation failure exits zero"; fi
assert_has "$out" "mktemp: injected failure" "temporary-log creation keeps the original diagnostic"
if [ ! -e "$state/calls" ]; then ok "temporary-log failure launches no checks"; else bad "temporary-log failure launched a check"; fi

# Concurrency: all fake linters hold an atomic active slot until the test sends
# release tokens. This makes an over-limit launch observable without sleeps.
new_case
mkfifo "$state/started" "$state/release"
exec 3<>"$state/started"
exec 4<>"$state/release"
cat >"$bin/golangci-lint" <<'FAKE_CONCURRENCY'
#!/usr/bin/env bash
set -u
while ! mkdir "$FAKE_STATE/lock" 2>/dev/null; do :; done
active="$(cat "$FAKE_STATE/active" 2>/dev/null || printf 0)"
active=$((active + 1))
printf '%s\n' "$active" >"$FAKE_STATE/active"
max="$(cat "$FAKE_STATE/max" 2>/dev/null || printf 0)"
[ "$active" -le "$max" ] || printf '%s\n' "$active" >"$FAKE_STATE/max"
rmdir "$FAKE_STATE/lock"
printf '%s\n' "$$" >"$FAKE_STATE/started"
IFS= read -r _ <"$FAKE_STATE/release"
while ! mkdir "$FAKE_STATE/lock" 2>/dev/null; do :; done
active="$(cat "$FAKE_STATE/active")"
printf '%s\n' "$((active - 1))" >"$FAKE_STATE/active"
rmdir "$FAKE_STATE/lock"
FAKE_CONCURRENCY
chmod +x "$bin/golangci-lint"
(
  cd "$repo" || exit 1
  TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
    MODULES="one two three four five" LINT_PARALLEL=2 "$runner"
) >"$case_dir/concurrency.out" 2>&1 &
lint_pid=$!
if IFS= read -r -t 5 _ <&3 && IFS= read -r -t 5 _ <&3; then ok "configured wave launches two checks"; else bad "configured wave did not launch two checks"; fi
for _ in one two three four five; do printf 'go\n' >&4; done
if wait "$lint_pid"; then rc=0; else rc=$?; fi
exec 3>&-
exec 4>&-
assert_eq "$rc" "0" "bounded-concurrency scenario exits zero"
assert_eq "$(cat "$state/max")" "2" "concurrency never exceeds LINT_PARALLEL=2"

# Interruption: the fake publishes its PID, then blocks. One FIFO gives the test
# a bounded acknowledgement of relayed TERM; another cancels a bounded runner
# watchdog. Either broken relay or broken runner exit is forced terminal before
# the corresponding wait can hang CI.
new_case
mkfifo "$state/started" "$state/release" "$state/stopped-ack" "$state/runner-watchdog-cancel"
exec 3<>"$state/started"
exec 4<>"$state/release"
exec 5<>"$state/stopped-ack"
exec 6<>"$state/runner-watchdog-cancel"
cat >"$bin/golangci-lint" <<'FAKE_INTERRUPT'
#!/usr/bin/env bash
set -u
trap 'printf stopped >"$FAKE_STATE/stopped"; printf "stopped\n" >"$FAKE_STATE/stopped-ack"; exit 143' HUP INT TERM
printf '%s\n' "$$" >"$FAKE_STATE/started"
IFS= read -r _ <"$FAKE_STATE/release"
FAKE_INTERRUPT
chmod +x "$bin/golangci-lint"
(
  cd "$repo" || exit 1
  TMPDIR="$case_dir" PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" \
    MODULES="one" LINT_PARALLEL=1 "$runner"
) >"$case_dir/interrupt.out" 2>&1 &
lint_pid=$!
if IFS= read -r -t 5 child_pid <&3; then ok "interrupt fake published its PID"; else bad "interrupt fake did not start"; child_pid=missing; fi
kill -TERM "$lint_pid"
if IFS= read -r -t 5 acknowledgement <&5 && [ "$acknowledgement" = stopped ]; then
  ok "runner relays termination to the active child"
else
  bad "runner did not relay termination within the bounded acknowledgement window"
  # Force every potentially blocked process to a terminal state before wait.
  printf 'release\n' >&4 || :
  [ "$child_pid" = missing ] || kill -TERM "$child_pid" 2>/dev/null || :
  if ! IFS= read -r -t 1 _ <&5; then
    [ "$child_pid" = missing ] || kill -KILL "$child_pid" 2>/dev/null || :
  fi
  kill -KILL "$lint_pid" 2>/dev/null || :
fi

# Bound runner termination independently of the child acknowledgement. Normal
# runner exit cancels this watchdog through fd 6. On timeout it first unblocks
# and force-kills any surviving child, then force-kills the runner, guaranteeing
# the final wait below can return and leaving the existing artifact assertions
# to detect any cleanup the runner itself skipped.
(
  if IFS= read -r -t 5 _ <"$state/runner-watchdog-cancel"; then
    exit 0
  fi
  printf 'timed out\n' >"$state/runner-timeout"
  printf 'release\n' >"$state/release" 2>/dev/null || :
  if [ "$child_pid" != missing ]; then
    kill -TERM "$child_pid" 2>/dev/null || :
    kill -KILL "$child_pid" 2>/dev/null || :
  fi
  kill -KILL "$lint_pid" 2>/dev/null || :
  exit 0
) &
watchdog_pid=$!
if wait "$lint_pid"; then rc=0; else rc=$?; fi
printf 'cancel\n' >&6 || :
wait "$watchdog_pid" || :
exec 3>&-
exec 4>&-
exec 5>&-
exec 6>&-
if [ -f "$state/runner-timeout" ]; then bad "interrupted runner exceeded its bounded exit window"; else ok "interrupted runner exits before the watchdog deadline"; fi
if [ "$rc" -ne 0 ]; then ok "interrupted runner exits nonzero"; else bad "interrupted runner exits zero"; fi
if [ -f "$state/stopped" ]; then ok "interrupted child handled termination"; else bad "interrupted child was not terminated"; fi
if [ "$child_pid" != missing ] && ! kill -0 "$child_pid" 2>/dev/null; then ok "interrupted child was waited"; else bad "interrupted child remains alive"; fi
assert_eq "$(find "$case_dir" -maxdepth 1 -type d -name 'serf-module-lint.*' | wc -l | tr -d ' ')" "0" "interruption removes temporary logs"

echo "----"
echo "run-module-lint-selftest: $checks checks, $fails failed"
[ "$fails" -eq 0 ]
```

- [ ] **Step 4: Make the self-test executable and run it to verify RED**

Run:

```bash
chmod +x scripts/run-module-lint-selftest.sh
scripts/run-module-lint-selftest.sh
```

Expected: FAIL because `scripts/run-module-lint.sh` does not exist; the harness must itself terminate and print a nonzero self-test summary rather than hanging.

- [ ] **Step 5: Implement the bounded module runner**

Create executable `scripts/run-module-lint.sh` with fixed-size indexed waves so it works with the repository's macOS Bash. Use `exec` in each child subshell so the recorded PID is the linter PID and signal cleanup cannot strand an intermediate shell.

```bash
#!/usr/bin/env bash
# run-module-lint.sh — lint all non-fuzz Go modules with bounded concurrency.
set -uo pipefail

MODULES=${MODULES:-". agent llm auth envvars invariant identifier"}
LINT_PARALLEL=${LINT_PARALLEL:-4}
case "$LINT_PARALLEL" in
  ''|*[!0-9]*|0)
    printf 'lint: LINT_PARALLEL must be a positive integer (got %s)\n' "$LINT_PARALLEL" >&2
    exit 2
    ;;
esac

# Whitespace splitting is the MODULES interface; Go module paths cannot contain
# whitespace. Indexed arrays retain caller order without Bash 4-only features.
# shellcheck disable=SC2206
modules=($MODULES)
module_count=${#modules[@]}
start=$SECONDS
printf 'lint: checking %d modules\n' "$module_count"

logdir=""
keep_failed_logs=0
active_pids=()

stop_children() {
  local pid
  [ "${#active_pids[@]}" -eq 0 ] && return 0
  for pid in "${active_pids[@]}"; do
    [ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null || :
  done
  for pid in "${active_pids[@]}"; do
    [ -n "$pid" ] && wait "$pid" 2>/dev/null || :
  done
  active_pids=()
}

cleanup() {
  stop_children
  if [ -n "$logdir" ] && [ "$keep_failed_logs" -eq 0 ]; then
    rm -rf "$logdir"
  fi
}

interrupted() {
  local status="$1"
  stop_children
  exit "$status"
}

trap cleanup EXIT
trap 'interrupted 129' HUP
trap 'interrupted 130' INT
trap 'interrupted 143' TERM

if ! logdir="$(mktemp -d -t serf-module-lint.XXXXXX)"; then
  printf 'lint: unable to create temporary log directory\n' >&2
  exit 1
fi

# Invoke the absent command once to retain Bash's original diagnostic without
# duplicating it once per module. This is the all-checks-impossible setup path.
if ! command -v golangci-lint >/dev/null 2>&1; then
  ( golangci-lint run ./... ) >"$logdir/setup.log" 2>&1
  status=$?
  cat "$logdir/setup.log"
  printf 'FAIL lint (%d modules not checked: %s)\n' "$module_count" "$MODULES"
  exit "$status"
fi

run_wave() {
  local first="$1" last="$2" i j pid status module log
  local -a indexes=()
  active_pids=()
  for ((i = first; i < last; i++)); do
    module="${modules[$i]}"
    log="$logdir/$i.log"
    (
      cd "$module" || exit 1
      exec golangci-lint run ./...
    ) >"$log" 2>&1 &
    active_pids+=("$!")
    indexes+=("$i")
  done
  for j in "${!active_pids[@]}"; do
    pid="${active_pids[$j]}"
    if wait "$pid"; then status=0; else status=$?; fi
    active_pids[$j]=""
    printf '%s\n' "$status" >"$logdir/${indexes[$j]}.status"
  done
  active_pids=()
}

for ((first = 0; first < module_count; first += LINT_PARALLEL)); do
  last=$((first + LINT_PARALLEL))
  [ "$last" -le "$module_count" ] || last=$module_count
  run_wave "$first" "$last"
done

failed_modules=()
for ((i = 0; i < module_count; i++)); do
  status="$(cat "$logdir/$i.status")"
  if [ "$status" -ne 0 ]; then
    failed_modules+=("${modules[$i]}")
  else
    rm -f "$logdir/$i.log"
  fi
  rm -f "$logdir/$i.status"
done

if [ "${#failed_modules[@]}" -eq 0 ]; then
  printf 'PASS lint (%d modules, %ds)\n' "$module_count" "$((SECONDS - start))"
  exit 0
fi

for ((i = 0; i < module_count; i++)); do
  [ -f "$logdir/$i.log" ] || continue
  printf '%s\n' "----- ${modules[$i]} -----"
  cat "$logdir/$i.log"
done
printf 'full logs: %s\n' "$logdir"
keep_failed_logs=1
printf 'FAIL lint (%d/%d modules: %s)\n' \
  "${#failed_modules[@]}" "$module_count" "${failed_modules[*]}"
exit 1
```

Then run:

```bash
chmod +x scripts/run-module-lint.sh
bash -n scripts/run-module-lint.sh scripts/run-module-lint-selftest.sh
scripts/run-module-lint-selftest.sh
```

Expected: `bash -n` exits 0; the self-test ends with `run-module-lint-selftest: … checks, 0 failed`. The failure scenarios may retain failed logs only inside the self-test's top-level temporary directory, which its `EXIT` trap removes.

- [ ] **Step 6: Commit the independently green runner**

```bash
git status --short
git add scripts/run-module-lint.sh scripts/run-module-lint-selftest.sh
git commit -m "build: aggregate Go module lint failures" -m "Add a bounded module lint runner with deterministic failure replay, compact success output, setup diagnostics, and signal-safe child cleanup. Cover the executable behavior offline with temporary modules and fake linters, including multiple failures, configured concurrency, and interruption."
```

Expected: the commit contains only the two new executable scripts. Do not bypass any hook.

### Task 2: Quiet Make lint families and preserve canonical coverage

**Files:**
- Modify: `scripts/run-module-lint-selftest.sh`
- Modify: `Makefile:284-329`

**Interfaces:**
- Consumes: `GO_MODULES := . agent llm auth envvars invariant identifier` exactly as already defined at `Makefile:79`; existing commands `go run ./cmd/serf-namingcheck`, `go run ./cmd/serf-internalcheck`, `go run ./cmd/serf-docscheck`, `golangci-lint run ./...`, `go generate ./appwire/...`, `git diff --exit-code -- docs/appwire-protocol.md`, and `scripts/gitleaks-scan.sh repo` with unchanged arguments.
- Produces: `lint-golangci` invokes `MODULES="$(GO_MODULES)" scripts/run-module-lint.sh`; `lint` retains the exact dependency families `lint-naming lint-internal lint-docs lint-golangci lint-generated secret-scan`.
- Quiet-check contract: `run_quiet_lint` redirects a check's combined stdout/stderr to one private log, removes the log on success, and prints it in full before returning the original nonzero status on failure. Its optional second argument, `preserve-gitleaks-warning`, replays the existing `warning: gitleaks not installed; ...` line on a successful skipped scan, because that warning is policy-relevant rather than routine green chatter.

- [ ] **Step 1: Add behavioral Makefile integration tests and verify RED**

Insert this scenario in `scripts/run-module-lint-selftest.sh` immediately before its final `echo "----"`. It copies the real Makefile and runner into a temporary repository, runs the real `lint` target against fake external commands, and asserts observed tool invocations. It also makes the docs check fail once to prove stdout/stderr replay. It does not inspect `make -n`, Make's database, or rendered recipe strings.

```bash
# Makefile integration: copy the real build entry point, fake only external
# commands, and prove both quiet success and unchanged lint-family coverage.
case_dir="$(mktemp -d "$work/make-case.XXXXXX")"
repo="$case_dir/repo"
state="$case_dir/state"
bin="$case_dir/bin"
mkdir -p "$repo/scripts" "$state" "$bin"
cp "$makefile" "$repo/Makefile"
cp "$runner" "$repo/scripts/run-module-lint.sh"
chmod +x "$repo/scripts/run-module-lint.sh"
for module in agent llm auth envvars invariant identifier; do mkdir -p "$repo/$module"; done

cat >"$bin/go" <<'FAKE_GO'
#!/usr/bin/env bash
printf 'go %s\n' "$*" >>"$FAKE_STATE/families"
printf 'go-success-chatter\n'
if [ "${FAKE_FAIL_DOCS:-0}" = 1 ] && [ "$*" = "run ./cmd/serf-docscheck" ]; then
  printf 'docs stdout diagnostic\n'
  printf 'docs stderr diagnostic\n' >&2
  exit 9
fi
FAKE_GO
cat >"$bin/git" <<'FAKE_GIT'
#!/usr/bin/env bash
printf 'git %s\n' "$*" >>"$FAKE_STATE/families"
printf 'git-success-chatter\n'
FAKE_GIT
cat >"$bin/golangci-lint" <<'FAKE_MAKE_LINT'
#!/usr/bin/env bash
module="$(basename "$PWD")"
[ "$PWD" = "$FAKE_REPO" ] && module=.
printf '%s\t%s\n' "$module" "$*" >>"$FAKE_STATE/modules"
printf 'golangci-success-chatter\n'
FAKE_MAKE_LINT
cat >"$repo/scripts/gitleaks-scan.sh" <<'FAKE_SECRET_SCAN'
#!/usr/bin/env bash
printf 'secret-scan %s\n' "$*" >>"$FAKE_STATE/families"
if [ "${FAKE_GITLEAKS_MISSING:-0}" = 1 ]; then
  printf 'warning: gitleaks not installed; skipping repo secret scan (install: https://github.com/gitleaks/gitleaks)\n' >&2
  exit 0
fi
printf 'secret-success-chatter\n'
FAKE_SECRET_SCAN
chmod +x "$bin/go" "$bin/git" "$bin/golangci-lint" "$repo/scripts/gitleaks-scan.sh"

out="$case_dir/make-lint.out"
if (
  cd "$repo" || exit 1
  PATH="$bin:/usr/bin:/bin" FAKE_REPO="$repo" FAKE_STATE="$state" make lint
) >"$out" 2>&1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "real make lint wiring exits zero with healthy fakes"
assert_eq "$(wc -l <"$out" | tr -d ' ')" "2" "real make lint keeps the two-line success contract"
assert_eq "$(sed -n '1p' "$out")" "lint: checking 7 modules" "Makefile passes the canonical module count"
case "$(sed -n '2p' "$out")" in
  "PASS lint (7 modules, "*"s)") ok "real make lint prints the final PASS line" ;;
  *) bad "real make lint PASS line has the wrong shape" ;;
esac
assert_not_has "$out" "success-chatter" "all successful lint-family chatter is hidden"
assert_eq "$(cut -f1 "$state/modules" | sort | tr '\n' ' ' | sed 's/ $//')" ". agent auth envvars identifier invariant llm" "Makefile passes every canonical non-fuzz module"
assert_eq "$(cut -f2 "$state/modules" | sort -u)" "run ./..." "Makefile wiring preserves golangci-lint run ./..."
assert_has "$state/families" "go run ./cmd/serf-namingcheck" "make lint retains naming checks"
assert_has "$state/families" "go run ./cmd/serf-internalcheck" "make lint retains internal dependency checks"
assert_has "$state/families" "go run ./cmd/serf-docscheck" "make lint retains docs checks"
assert_has "$state/families" "go generate ./appwire/..." "make lint retains generation"
assert_has "$state/families" "git diff --exit-code -- docs/appwire-protocol.md" "make lint retains generated-file verification"
assert_has "$state/families" "secret-scan repo" "make lint retains the secret scan"

# The missing-gitleaks path is successful but degraded. Its existing warning is
# part of the secret-scan policy and must survive routine-success suppression.
out="$case_dir/make-secret-degraded.out"
if (
  cd "$repo" || exit 1
  PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" FAKE_GITLEAKS_MISSING=1 make secret-scan
) >"$out" 2>&1; then rc=0; else rc=$?; fi
assert_eq "$rc" "0" "missing-gitleaks secret scan remains successful"
assert_has "$out" "warning: gitleaks not installed; skipping repo secret scan" "missing-gitleaks warning remains visible"
assert_not_has "$out" "secret-success-chatter" "routine secret-scan success chatter remains hidden"

: >"$state/families"
out="$case_dir/make-docs-failure.out"
if (
  cd "$repo" || exit 1
  PATH="$bin:/usr/bin:/bin" FAKE_STATE="$state" FAKE_FAIL_DOCS=1 make lint-docs
) >"$out" 2>&1; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ]; then ok "failed non-module lint family returns nonzero"; else bad "failed non-module lint family returns zero"; fi
assert_has "$out" "docs stdout diagnostic" "failed non-module stdout is replayed"
assert_has "$out" "docs stderr diagnostic" "failed non-module stderr is replayed"
```

Run:

```bash
scripts/run-module-lint-selftest.sh
```

Expected: FAIL in the Makefile integration scenario because current `make lint` echoes or prints successful naming/docs/generation chatter and current `lint-golangci` is the serial loop rather than `scripts/run-module-lint.sh`.

- [ ] **Step 2: Add one reusable quiet-check Make wrapper**

Add this definition immediately before `lint-naming`. It creates one log per non-module check, removes it on every exit, discards routine output only on success, and returns the failing command's exact status after replaying the complete combined output. The optional `preserve-gitleaks-warning` mode prints only the existing missing-tool warning from a successful secret-scan log.

```make
# Successful lint families are quiet; failures replay their complete output.
define run_quiet_lint
	@set -u; log="$$(mktemp -t serf-lint-check.XXXXXX)" || exit 1; \
	trap 'rm -f "$$log"' EXIT HUP INT TERM; \
	if ( $(1) ) >"$$log" 2>&1; then \
		if [ "$(2)" = preserve-gitleaks-warning ]; then \
			grep -F 'warning: gitleaks not installed; skipping repo secret scan' "$$log" >&2 || :; \
		fi; \
	else \
		status=$$?; cat "$$log"; exit $$status; \
	fi
endef
```

- [ ] **Step 3: Route the existing lint families through the wrapper without changing commands**

Modify only the lint recipes in `Makefile:284-329` as shown here and in the separate existing `secret-scan` recipe below. `generate` remains an unchanged, directly invokable target; `lint-generated` invokes the same generate command inside its captured check so `make lint` can suppress successful generation chatter without changing standalone `make generate` behavior.

```make
lint-naming:
	$(call run_quiet_lint,go run ./cmd/serf-namingcheck)

# lint-internal fails if any exported symbol in the agent/llm/providercfg
# libraries names a serf-internal type — keeping them externally importable.
lint-internal:
	$(call run_quiet_lint,go run ./cmd/serf-internalcheck)

# lint-docs fails if any exported package-level declaration in the published
# library packages (llm, agent, agent/events, auth/openai) lacks a doc comment.
lint-docs:
	$(call run_quiet_lint,go run ./cmd/serf-docscheck)

build-namingcheck:
	go build -o serf-namingcheck ./cmd/serf-namingcheck/

# golangci-lint across every module (./... is per-module under go.work).
lint-golangci:
	@MODULES="$(GO_MODULES)" scripts/run-module-lint.sh

# generate runs all `go generate` directives. Currently the AppWire protocol
# reference (docs/appwire-protocol.md) from the catalog in appwire/protocol.go.
generate:
	go generate ./appwire/...

# lint-generated fails if a committed generated file is stale — i.e. the
# AppWire catalog changed without regenerating the protocol doc.
lint-generated:
	$(call run_quiet_lint,go generate ./appwire/... && { git diff --exit-code -- docs/appwire-protocol.md || { echo "docs/appwire-protocol.md is stale; run 'make generate' and commit."; exit 1; }; })

lint: lint-naming lint-internal lint-docs lint-golangci lint-generated secret-scan
```

At the existing `secret-scan` target under its existing comment, change only its recipe to:

```make
secret-scan:
	$(call run_quiet_lint,scripts/gitleaks-scan.sh repo,preserve-gitleaks-warning)
```

Keep the existing comments above `lint-naming` and `secret-scan` in place. Do not duplicate or move the `secret-scan` target, change `fuzz-corpus-scan`, reorder/add/remove `lint` prerequisites, or touch `.PHONY`: this plan creates or renames no Make targets, so it must preserve the current declarations exactly even though `generate` and `lint-generated` are not currently listed there.

- [ ] **Step 4: Run the self-test and syntax checks to verify GREEN**

```bash
bash -n scripts/run-module-lint.sh scripts/run-module-lint-selftest.sh
scripts/run-module-lint-selftest.sh
```

Expected: syntax checks exit 0; the self-test reports zero failed checks. The healthy fake full `make lint` output is exactly two lines, its call logs contain all six existing lint families and all seven canonical modules with `run ./...`, the degraded missing-gitleaks scenario remains successful while printing its warning, and the injected docs failure retains both diagnostic streams.

- [ ] **Step 5: Commit the quiet Make wiring**

```bash
git status --short
git add Makefile scripts/run-module-lint-selftest.sh
git commit -m "build: keep successful lint runs quiet" -m "Route the canonical non-fuzz module list through the aggregate runner and capture routine output from the existing naming, internal, docs, generated-file, and secret-scan checks. Preserve every existing command, failure diagnostic, and the successful-but-degraded missing-gitleaks warning, with behavioral Makefile coverage using fake external commands."
```

Expected: this commit changes only `Makefile` and the self-test. Do not stage unrelated files and do not bypass hooks.

### Task 3: Real gate verification and scope audit

**Files:**
- Verify only: `Makefile`
- Verify only: `scripts/run-module-lint.sh`
- Verify only: `scripts/run-module-lint-selftest.sh`

**Interfaces:**
- Consumes: the actual repository tools and current working tree.
- Produces: fresh evidence that the offline contracts pass, the real lint gate still exercises its existing families, success output is compact, and the implementation diff stays inside the Scope Lock.

- [ ] **Step 1: Re-run deterministic executable tests from a clean command invocation**

```bash
bash -n scripts/run-module-lint.sh scripts/run-module-lint-selftest.sh
scripts/run-module-lint-selftest.sh
```

Expected: both commands exit 0 and the self-test's last line reports `0 failed`.

- [ ] **Step 2: Run the real lint gate**

```bash
make lint
```

Expected on a fully healthy checkout with gitleaks installed: exactly one `lint: checking 7 modules` line followed eventually by one `PASS lint (7 modules, Ns)` line, with no per-module success lines or successful tool chatter. If gitleaks is absent, the existing `warning: gitleaks not installed; skipping repo secret scan ...` line must remain visible even though the target exits successfully. If a real check fails, do not alter rules, flags, versions, module membership, generated output, or secret policy to force green; retain the complete failed output, distinguish a pre-existing lint failure from an implementation defect, and fix only defects in the scoped orchestration.

- [ ] **Step 3: Audit the diff against the Scope Lock**

```bash
base_ref=refs/serf-plan-bases/aggregate-quiet-lint
git rev-parse --verify "$base_ref"
git diff --check "$base_ref"..HEAD
git diff --name-only "$base_ref"..HEAD
git diff "$base_ref"..HEAD -- Makefile scripts/run-module-lint.sh scripts/run-module-lint-selftest.sh
git diff --check
git diff --cached --check
git diff --name-only
git diff --cached --name-only
git status --short
```

Expected: the base ref resolves; `git diff --check "$base_ref"..HEAD`, `git diff --check`, and `git diff --cached --check` exit 0. The committed base-to-HEAD range names only `Makefile`, `scripts/run-module-lint.sh`, and `scripts/run-module-lint-selftest.sh`; the staged and unstaged name lists are empty after the implementation commits. `GO_MODULES`, tool versions, lint flags, the `lint` family list, test/race/fuzz/live gates, cache behavior, generated artifacts, and production Serf files are unchanged. If pre-existing unrelated worktree files are visible, inspect and report them separately; they must not be staged or modified.

- [ ] **Step 4: Commit only if verification required a scoped correction**

If Steps 1-3 exposed and you corrected an orchestration or self-test defect, commit only those corrections:

```bash
git status --short
git add Makefile scripts/run-module-lint.sh scripts/run-module-lint-selftest.sh
git commit -m "test: harden aggregate lint orchestration" -m "Correct the scoped runner or behavioral self-test issue found by final syntax, fake-command, real-lint, and diff verification without changing lint policy or any non-lint gate."
```

Expected: no third commit when no correction was needed. Never create an empty commit and never stage unrelated files.

- [ ] **Step 5: Repeat the final evidence after any correction, then remove the private base ref**

If Step 4 created a correction commit, first repeat Steps 1-3 in full. Once the executable tests, real lint gate, committed range, staged diff, and unstaged diff all have fresh passing evidence, run:

```bash
base_ref=refs/serf-plan-bases/aggregate-quiet-lint
git diff --check "$base_ref"..HEAD
git diff --name-only "$base_ref"..HEAD
git diff --check
git diff --cached --check
git status --short
git update-ref -d "$base_ref"
```

Expected: the base-to-HEAD name list still contains only the Makefile and two scripts, both worktree diff checks are clean, and the private audit ref is deleted only after the final evidence has been read. Report local commit state, verification state, and push state separately; this plan does not authorize a push.
