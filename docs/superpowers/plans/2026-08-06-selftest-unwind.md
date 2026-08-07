# Selftest Unwind Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hand-rolled shell selftest wave with a small tested Go runner, delete the disk-reclaim reclamation subsystem in favor of enforced per-suite cleanup, and remove every wall-clock dependency and loaded-box tolerance from the selftest suites.

**Architecture:** A new `cmd/serf-selftest` Go tool owns the parallel wave: process groups, TERM forwarding with a KILL grace, per-suite TMPDIR isolation, and a leftover-files check that FAILS any suite that does not clean up after itself. The 55-line Makefile shell recipe and its 1,100-line shell selftest (`make-selftest-selftest.sh`) are deleted; the runner's contract lives in ordinary Go tests that synchronize on FIFOs and process exits, never on sleeps. `disk-reclaim.sh` shrinks to a ~60-line `disk-preflight.sh` (free-space floor + bounded GOCACHE probe) because the check came from real disk-full incidents (kata 98x9, 6jxs); the worktree-classification/reclaim machinery and its 700-line selftest are deleted.

**Tech Stack:** Go (root module, stdlib only: os/exec, syscall, os/signal), bash for the remaining suite scripts.

## Global Constraints

- TDD for the Go runner and the new preflight selftest: failing test first, then code.
- No test may use `sleep` as synchronization. Timeouts may exist only as failure ceilings that never bind on success, and any timeout a test deliberately trips must be injected at its smallest supported value.
- Every suite must confine writes to `$TMPDIR` and leave it empty on success — the runner enforces this.
- Suite scripts are invoked as `scripts/<name>-selftest.sh` from the repo root; the runner preserves that contract so `run-module-tests.sh` line 397 (`${MAKE:-make} selftest`) keeps working unchanged.
- `make selftest` must stay quiet on success (PASS lines only) and replay a failing suite's whole log.
- Match surrounding style; no whitespace-only changes.
- Commit after every green step. Branch: `selftest-unwind`.

---

### Task 1: `cmd/serf-selftest` runner — happy path, failure replay, leak check

**Files:**
- Create: `cmd/serf-selftest/main.go`
- Create: `cmd/serf-selftest/wave.go`
- Test: `cmd/serf-selftest/wave_test.go`

**Interfaces:**
- Produces: `runWave(cfg waveConfig) int` where `waveConfig{ScriptsDir string, Suites []string, KillGrace time.Duration, Out io.Writer, Signals <-chan os.Signal}`; returns the process exit code (0 ok, 1 suite failure, 128+sig on interrupt). `main.go` is a thin flag-parsing wrapper (`-scripts-dir`, `-kill-grace`).

- [ ] **Step 1: Write failing tests** in `wave_test.go` using fixture scripts written into `t.TempDir()`:

```go
func writeSuite(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name+"-selftest.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAllSuitesPassPrintsPassAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "alpha", "echo alpha-ran\nexit 0\n")
	writeSuite(t, dir, "beta", "exit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"alpha", "beta"}, KillGrace: time.Second, Out: &out})
	if code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	for _, want := range []string{"PASS  alpha", "PASS  beta"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "alpha-ran") {
		t.Fatalf("passing suite's log must not be replayed:\n%s", out.String())
	}
}

func TestFailingSuiteReplaysItsLogOnce(t *testing.T) // beta exits 3 with "beta-broke" on stdout: exit 1, "FAIL  beta", log replayed exactly once, alpha still PASS
func TestSuiteLeavingTempFilesFails(t *testing.T)    // suite exits 0 but writes "$TMPDIR/leak"; expect exit 1 and "leaked" + "leak" in output
func TestSuiteGetsPrivateTmpdir(t *testing.T)        // suite writes "$TMPDIR" to a result file the test reads back; two suites must see different, existing dirs
```

- [ ] **Step 2: Run to verify failure**: `go test ./cmd/serf-selftest/` → FAIL (undefined runWave).
- [ ] **Step 3: Implement `wave.go`.** One goroutine per suite. Per suite: `os.MkdirTemp` run dir already created by runWave; suite tmp at `<run>/<name>/tmp`, log at `<run>/<name>.log`; `exec.Command(filepath.Join(cfg.ScriptsDir, name+"-selftest.sh"))` with `Env` overriding `TMPDIR`, `SysProcAttr{Setpgid: true}`, stdout+stderr to the log file. After Wait: if exit 0, leak-check the tmp dir (`os.ReadDir`; non-empty → synthesized failure listing the entries). Results collected over a channel; ordered report in `cfg.Suites` order: `PASS  %-26s %ds` / `FAIL  %-26s` + log replay. Remove the run dir before returning.
- [ ] **Step 4:** `go test ./cmd/serf-selftest/` → PASS. `gofmt -l cmd/serf-selftest` clean.
- [ ] **Step 5: Commit** `feat(selftest): add Go wave runner with enforced suite cleanup`.

### Task 2: runner signal handling — TERM forwarding, KILL grace, descendant reaping

**Files:**
- Modify: `cmd/serf-selftest/wave.go`
- Test: `cmd/serf-selftest/wave_test.go`

**Interfaces:**
- Consumes: `runWave` from Task 1; `cfg.Signals` channel and `cfg.KillGrace` become live.

- [ ] **Step 1: Write failing tests.** All synchronization is FIFOs and process exits — no sleeps:

```go
// TestInterruptTermsProcessGroupAndExits143:
// fixture suite: mkfifo via unix.Mkfifo in the test's dir; suite script:
//   trap 'echo suite-termed >"$FX/events"; exit 143' TERM
//   echo ready >"$FX/ready"
//   read _ <"$FX/hold"   # blocks forever; only TERM releases the suite
// test: start runWave in a goroutine with a signals channel; read "$FX/ready";
// send syscall.SIGTERM on the channel; require exit code 143 and "suite-termed"
// readable from "$FX/events". The runner returning proves the child was reaped.

// TestForkedDescendantIsReaped:
// suite forks a child of its own:
//   ( trap 'echo grandchild-termed >"$FX/events"; exit 0' TERM; echo up >"$FX/ready2"; read _ <"$FX/hold" ) &
//   echo ready >"$FX/ready"; wait
// after both ready markers, SIGTERM → expect "grandchild-termed" in events:
// proves the TERM went to the process group, not the single pid.

// TestTermIgnoringSuiteIsKilledAfterGrace:
// suite: trap '' TERM; echo ready >"$FX/ready"; read _ <"$FX/hold"
// cfg.KillGrace = 50 * time.Millisecond
// send SIGTERM after ready; runWave must return (KILL escalation) with 143.
// No assertion measures elapsed time — the only clock is the injected grace.
```

- [ ] **Step 2:** `go test ./cmd/serf-selftest/ -run 'Interrupt|Descendant|TermIgnoring'` → FAIL.
- [ ] **Step 3: Implement.** Each suite goroutine owns its child: a watcher goroutine selects on `shutdown` (closed by the signal listener) vs `done`. On shutdown: per-suite mutex, if not done `syscall.Kill(-pgid, SIGTERM)`; then select done vs `time.After(cfg.KillGrace)` → `syscall.Kill(-pgid, SIGKILL)` under the same mutex/done check. The Wait side sets done under the mutex the moment Wait returns, so no signal ever targets a reaped (reusable) pgid. Signal listener records the first signal; final exit code is `128+sig` when interrupted. Interrupted suites report as FAIL without log replay flood — replay only suites that failed on their own before the interrupt.
- [ ] **Step 4:** full `go test ./cmd/serf-selftest/` → PASS. `go vet ./cmd/serf-selftest/` clean.
- [ ] **Step 5: Commit** `feat(selftest): signal-safe wave shutdown with KILL grace`.

### Task 3: wire the Makefile to the runner; delete the shell recipe and make-selftest-selftest.sh

**Files:**
- Modify: `Makefile:179-299` (SELFTEST_SCRIPTS comment, recipe, and the 58-line signal-semantics comment block)
- Delete: `scripts/make-selftest-selftest.sh`
- Modify: `docs/testing.md` (references to the shell wave / make-selftest)

**Interfaces:**
- Consumes: `go run ./cmd/serf-selftest` CLI from Tasks 1-2.
- Produces: `make selftest` behavior identical from the outside (quiet PASS lines, failing log replay, nonzero exit, interrupt-safe).

- [ ] **Step 1:** Replace the recipe:

```make
# selftest hangs off `make test` because a script selftest is a test. It stays
# off `make lint` because run-module-lint-selftest.sh drives a fixture `make
# lint` of its own. The wave runner (cmd/serf-selftest) owns parallel spawn,
# signal forwarding, per-suite TMPDIR isolation, and the leftover-files check
# that fails any suite that does not clean up after itself; its contract is
# pinned by cmd/serf-selftest/wave_test.go, which runs in the ordinary Go test
# wave rather than here.
selftest:
	@go run ./cmd/serf-selftest $(SELFTEST_SCRIPTS)
```

Drop `make-selftest` from `SELFTEST_SCRIPTS`. Trim the SELFTEST_SCRIPTS comment (lines 179-186) of the run-module-lint assertion count and fuzz-wave provenance only if stale; keep the "each is the ONLY thing that pins its script's contract" sentence.

- [ ] **Step 2:** `git rm scripts/make-selftest-selftest.sh`.
- [ ] **Step 3:** Update `docs/testing.md` mentions of the wave mechanics/make-selftest to point at `cmd/serf-selftest`.
- [ ] **Step 4: Verify:** `make selftest` → all suites PASS (any suite that FAILs the new leak check gets fixed here: add the missing `trap 'rm -rf ...' EXIT` to that suite — each such fix is part of this task). Then Ctrl-C behavior: `make selftest & sleep 2; kill -INT $!` is NOT the verification (wall clock); instead run `go run ./cmd/serf-selftest tmux-read` and interrupt via the Go tests already covering it — the make-level check is just that an interrupted run leaves no `serf-selftest` temp dirs behind (`ls ${TMPDIR:-/tmp} | grep serf-selftest` empty).
- [ ] **Step 5: Commit** `feat(make): drive the selftest wave with cmd/serf-selftest`.

### Task 4: replace disk-reclaim with disk-preflight

**Files:**
- Create: `scripts/disk-preflight.sh` (~60 lines)
- Create: `scripts/disk-preflight-selftest.sh`
- Delete: `scripts/disk-reclaim.sh`, `scripts/disk-reclaim-selftest.sh`
- Modify: `Makefile:159-160` (cache-preflight), `Makefile` SELFTEST_SCRIPTS (drop `disk-reclaim`, add `disk-preflight`)
- Modify: `scripts/run-module-tests.sh:36-38`
- Modify: `scripts/merge-approval-gate-selftest.sh:116-234` (fake + four assertions rename to `disk-preflight`)
- Modify: `runtime_pair_build_test.go:241` (stub filename)
- Modify: `scenariopatternkill_audit_test.go:82-93` (delete the disk-reclaim-selftest entry and the stale prose reference at 123/155)
- Modify: `scripts/report-tmp-debris.sh:142`, `scripts/report-orphaned-worktrees.sh:18,36`, `scripts/setup-gocache.sh:16` (message/comment pointers)
- Modify: `docs/testing.md`, `docs/conventions/agent-fleets.md` (disk-reclaim mentions → report tools + preflight)

**Interfaces:**
- Produces: `scripts/disk-preflight.sh` — no flags. Silent exit 0 when free space ≥ floor and GOCACHE answers. Exit 1 with a named diagnosis otherwise. Env: `SERF_DISK_MIN_FREE_GB` (default 5), `SERF_GOCACHE_PROBE_TIMEOUT` (default 10, integer seconds), `SERF_GOCACHE_PROBE_CMD` (test seam, replaces the probe command — carried over verbatim from disk-reclaim.sh:163).

- [ ] **Step 1: Write `disk-preflight-selftest.sh` first** (failing): scenarios (a) healthy fixture → silent, exit 0; (b) `SERF_DISK_MIN_FREE_GB=999999` → exit 1, message names the floor, the free figure, and points at `scripts/report-tmp-debris.sh` and `scripts/report-orphaned-worktrees.sh`; (c) stalled probe: `SERF_GOCACHE_PROBE_CMD` set to a script that blocks reading a FIFO no one writes, `SERF_GOCACHE_PROBE_TIMEOUT=1` → exit 1, message says STALLED and names the GOCACHE path (the 1s here is the smallest supported value of a deliberately-tripped timeout, per Global Constraints); (d) `--help` prints the header. Harness: same `ok`/`assert_eq` pattern as `scripts/setup-gocache-selftest.sh`, `mktemp -d` + `trap rm -rf EXIT`.
- [ ] **Step 2: Implement `disk-preflight.sh`:** free-space floor via `df -Pk .` awk column 4; GOCACHE path via `go env GOCACHE`; bounded probe lifted from `disk-reclaim.sh` `gocache_probe()` (lines 150-176: backgrounded probe + 20/s status polls capped at the timeout — the poll is a failure ceiling, not sync) with `mkdir -p "$gocache"` as the default probe command; diagnosis text carried from disk-reclaim.sh:238-246 (kata r07s wording). `--help` via the same awk header idiom.
- [ ] **Step 3:** run the new selftest → PASS. Rewire every consumer listed above; `git rm` the two old scripts.
- [ ] **Step 4: Verify:** `scripts/merge-approval-gate-selftest.sh` PASS, `go test . -run 'TestNoCardOrScriptPatternKills|RuntimePair' -count=1` PASS, `make selftest` PASS, `grep -rn disk-reclaim scripts/ Makefile docs/ *.go` → no hits.
- [ ] **Step 5: Commit** `feat(scripts): slim disk-reclaim to a disk-preflight check`.

### Task 5: remove remaining wall-clock waits from suites

**Files:**
- Modify: `scripts/run-module-tests-selftest.sh:531-533` (`SERF_ROOT_PACKAGE_LIST_TIMEOUT=5` → `1`; the `exec sleep 20` ready-keeper and watchdog ceilings shrink to match: keeper 6, watchdogs 10→5)
- Modify: `scripts/agent-test-shards-selftest.sh` (replace `wait_for_file` 0.01s-poll loops with FIFO reads; replace `sleep 1000` blocking children with `read _ <fifo` blocks)

**Interfaces:**
- Consumes: nothing new. Produces: identical assertions, event-driven sync.

- [ ] **Step 1:** Make each edit; the fixture fake binaries gain a `mkfifo` + `read` in place of `sleep`; readiness markers switch from polled files to FIFO writes mirroring the pattern already in `run-module-lint-selftest.sh`.
- [ ] **Step 2: Verify:** run both suites standalone → PASS; time them (`run-module-tests` scenario drops ~4s).
- [ ] **Step 3: Commit** `test(selftest): event-driven waits, minimum injected timeouts`.

### Task 6: shared assertion harness

**Files:**
- Create: `scripts/selftest-lib.sh` (`ok`, `bad`, `assert_eq`, `assert_has`, `assert_not_has`, `selftest_summary`, checks/fails counters)
- Modify: all 20 remaining `scripts/*-selftest.sh` — delete the inline copies, `. "$(dirname "$0")/selftest-lib.sh"` at the top; suites keep any helper unique to them (e.g. merge-approval-gate's `assert_before`).

- [ ] **Step 1:** Extract the helper set from `scripts/setup-gocache-selftest.sh` (the smallest complete copy) into `scripts/selftest-lib.sh`, matching the counter/summary wording the suites already print (`<name>: N checks, M failed`).
- [ ] **Step 2:** Migrate suites in three batches (fuzz-*, report/tmux/small, the four big runners), running each migrated suite before moving on.
- [ ] **Step 3: Verify:** `make selftest` → all PASS; `grep -ln 'assert_eq()' scripts/*-selftest.sh` → empty.
- [ ] **Step 4: Commit per batch** `refactor(selftest): share the assertion harness`.

### Task 7: full-gate verification

- [ ] **Step 1:** `make selftest` — all suites PASS, wall time recorded (expect ≈25s idle → bounded by run-module-lint/run-module-tests, down from 44s).
- [ ] **Step 2:** `make test` — full gate green.
- [ ] **Step 3:** `make lint` — green (audit tests updated in Task 4 run here too).
- [ ] **Step 4: Commit** any stragglers; update this plan's checkboxes.

## Decisions locked (and one flagged)

- **Flagged for Jesse:** `disk-reclaim` is not deleted outright — its `--check` floor came from two real disk-full incidents that masqueraded as test flakes (script header, kata 98x9/6jxs/r07s), so the ~60-line preflight keeps that early warning while the worktree-classification/reclaim machinery (~95% of the code and its whole 700-line selftest) is deleted. The read-only `report-*` scripts stay: they are the human-facing "look before you delete" tools and cost the wave ~0s.
- Cleanup enforcement moves into the runner: a suite that leaves anything in its TMPDIR fails. That is the replacement for janitorial reclamation.
- `reclaim-test-debris.sh` (GOCACHE shard ager) survives this plan: it handles SIGKILL/power-loss debris that no in-test cleanup can, and its selftest costs 1s. Candidate for a later unwind if Jesse wants.
- tmux-read/tmux-send stay separate suites (separate subject scripts); duplication is resolved by the shared harness, not by merging suites.
