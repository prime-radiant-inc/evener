# Delegate-Turn Limits Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the concurrent delegate-turn cap configurable (default 50), make slot occupancy observable, move drive turns onto their own bounded/timeout'd budget, raise the retained-terminal cap to 2048 (configurable), and give `job_list` offset windowing with an honest `showing A-B of N` footer.

**Architecture:** One root-minted, tree-shared atomic counter per budget (spawn turns vs drive turns), configured via `SessionConfig` with the existing CLI/launchconfig/snapshot plumbing pattern used by `MaxSubagentDepth`. Slot reservations carry a holder kind so occupancy is diagnosable. Error becomes a typed, formatted error preserving `errors.Is(err, errTreeAtCapacity)`.

**Tech Stack:** Go 1.25, `primeradiant.com/serf` module, stdlib only (sync/atomic, context, fmt).

**Spec:** `docs/superpowers/specs/2026-07-19-delegate-turn-limits-design.md` (committed 2f2e41df).

## Global Constraints

- Tests deterministic, scripted provider only (docs/testing.md; no live calls, no credentials).
- Default concurrent-turn cap: **50** (`defaultMaxConcurrentDelegateTurns`).
- Drive budget: constant **8** (`defaultMaxConcurrentDriveTurns`), not a config knob.
- Drive timeout: **5 min** (`driveTurnTimeout` var); re-drive pacing: **1s** (`driveRedriveMinInterval` var).
- Retained-terminal default: **2048** (`defaultMaxRetainedTerminal`).
- Error prefix token `tree_at_capacity` preserved; `errors.Is(err, errTreeAtCapacity)` must keep working (tests rely on it).
- Naming: kebab CLI / snake config+toml / camel wire (docs/conventions/naming.md).
- Work happens in the `subagent-limit-study` worktree on branch `subagent-limit-study`.

---

## Phase 1 — Configurable caps + job_list windowing

### Task 1: SessionConfig knobs + defaults + snapshot round-trip

**Files:**
- Modify: `agent/session_config.go` (fields after `MaxSubagentDepth` ~line 52; `applyDefaults` ~line 447; `toSnapshot`/`configFromSnapshot` ~lines 470-540)
- Modify: `agent/schema/config_snapshot.go:16`
- Test: `agent/session_config_test.go`

**Interfaces:**
- Produces: `SessionConfig.MaxConcurrentDelegateTurns int` (json `max_concurrent_delegate_turns,omitempty`), `SessionConfig.MaxRetainedTerminal int` (json `max_retained_terminal,omitempty`). Both `<= 0` → defaults (50 / 2048) in `applyDefaults`. Constants `defaultMaxConcurrentDelegateTurns = 50`, `defaultMaxRetainedTerminalDelegates = 2048` in `session_config.go`.

- [ ] **Step 1: Failing test** — add to `agent/session_config_test.go`:

```go
func TestSessionConfigDelegateTurnKnobsDefaults(t *testing.T) {
	c := SessionConfig{}
	c.applyDefaults()
	if c.MaxConcurrentDelegateTurns != 50 {
		t.Fatalf("MaxConcurrentDelegateTurns = %d, want 50", c.MaxConcurrentDelegateTurns)
	}
	if c.MaxRetainedTerminal != 2048 {
		t.Fatalf("MaxRetainedTerminal = %d, want 2048", c.MaxRetainedTerminal)
	}
}

func TestSessionConfigDelegateTurnKnobsSnapshotRoundTrip(t *testing.T) {
	in := SessionConfig{MaxConcurrentDelegateTurns: 7, MaxRetainedTerminal: 99}
	snap := in.toSnapshot()
	out := configFromSnapshot(snap)
	if out.MaxConcurrentDelegateTurns != 7 || out.MaxRetainedTerminal != 99 {
		t.Fatalf("round trip = %+v, want 7/99", out)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `cd agent && go test -run 'TestSessionConfigDelegateTurnKnobs' -count=1 .` → undefined fields.

- [ ] **Step 3: Implement** — in `session_config.go` after `MaxSubagentDepth`:

```go
	// MaxConcurrentDelegateTurns bounds concurrently running delegate turns
	// across the whole session tree (tree-counter cap). Zero defaults to 50.
	MaxConcurrentDelegateTurns int `json:"max_concurrent_delegate_turns,omitempty"`

	// MaxRetainedTerminal bounds retained terminal child records per parent.
	// Zero defaults to 2048.
	MaxRetainedTerminal int `json:"max_retained_terminal,omitempty"`
```

In `applyDefaults`:

```go
	if c.MaxConcurrentDelegateTurns <= 0 {
		c.MaxConcurrentDelegateTurns = defaultMaxConcurrentDelegateTurns
	}
	if c.MaxRetainedTerminal <= 0 {
		c.MaxRetainedTerminal = defaultMaxRetainedTerminalDelegates
	}
```

Add both fields to `toSnapshot`, `configFromSnapshot`, and `agent/schema/config_snapshot.go` (snake-case json, mirroring `MaxSubagentDepth`).

- [ ] **Step 4: Run, verify pass** — same command; also `go test -run 'Snapshot' -count=1 .`

- [ ] **Step 5: Commit** — `git add agent/session_config.go agent/schema/config_snapshot.go agent/session_config_test.go && git commit -m "agent: configurable delegate-turn and retained-terminal knobs (defaults 50/2048)"`

---

### Task 2: treeCounter cap parameter + typed formatted error

**Files:**
- Modify: `agent/tree_counter.go` (full rewrite of cap + error)
- Modify: `agent/session_init.go:124,464` (mint sites)
- Modify: `agent/subagents.go:738` and `agent/job_delegate.go:1967` (failure sites)
- Test: `agent/tree_counter_test.go`, plus call-site fixes in `agent/agent_misc_program_fuzz_test.go:151`, `agent/session_init_seed100_exact_fuzz_test.go:248`

**Interfaces:**
- Consumes: `SessionConfig.MaxConcurrentDelegateTurns` (Task 1).
- Produces: `newTreeCounter(cap int64) *treeCounter` (cap `<= 0` → `defaultMaxConcurrentDelegateTurns`); `type treeCapacityError struct{ cap int64 }` with `Error() string` and `Is(error) bool` matching `errTreeAtCapacity`. `errTreeAtCapacity` becomes a bare sentinel. `treeCounter.cap` stays an unexported field readable in-package.

- [ ] **Step 1: Failing test** — new test in `agent/tree_counter_test.go`:

```go
func TestTreeCounterConfigurableCapAndErrorText(t *testing.T) {
	c := newTreeCounter(3)
	for i := range 3 {
		if !c.reserve() {
			t.Fatalf("reserve %d failed below cap 3", i+1)
		}
	}
	if c.reserve() {
		t.Fatal("reserve succeeded at cap 3")
	}
	err := &treeCapacityError{cap: 3}
	want := "tree_at_capacity: 3 delegate turn slots in use across this session tree. " +
		"Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry."
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, errTreeAtCapacity) {
		t.Fatal("treeCapacityError does not match errTreeAtCapacity via errors.Is")
	}
}
```

- [ ] **Step 2: Run, verify fail** — `cd agent && go test -run TestTreeCounterConfigurableCapAndErrorText -count=1 .`

- [ ] **Step 3: Implement** — in `tree_counter.go`:

```go
// defaultMaxConcurrentDelegateTurns is the default tree-wide cap on
// concurrently running delegate turns.
const defaultMaxConcurrentDelegateTurns = 50

// errTreeAtCapacity is the sentinel matched by errors.Is; the user-facing
// text is formatted by treeCapacityError with the live cap.
var errTreeAtCapacity = errors.New("tree_at_capacity")

// treeCapacityError is the formatted spawn/resume failure at tree capacity.
type treeCapacityError struct{ cap int64 }

func (e *treeCapacityError) Error() string {
	return fmt.Sprintf("tree_at_capacity: %d delegate turn slots in use across this session tree. "+
		"Wait for completions to free slots, job_stop work you no longer need, or narrow your fan-out and retry.", e.cap)
}

func (e *treeCapacityError) Is(target error) bool { return target == errTreeAtCapacity }

// newTreeCounter returns a treeCounter with the given cap; cap <= 0 selects
// the default (50).
func newTreeCounter(cap int64) *treeCounter {
	if cap <= 0 {
		cap = defaultMaxConcurrentDelegateTurns
	}
	return &treeCounter{cap: cap}
}
```

Add `"fmt"` import; drop the old pinned-text var. Update mint sites in `session_init.go` (both): `tc = newTreeCounter(int64(cfg.MaxConcurrentDelegateTurns))`. Failure sites: `subagents.go` → `return nil, &treeCapacityError{cap: s.treeCounter.cap}`; `job_delegate.go` → `return nil, &treeCapacityError{cap: s.treeCounter.cap}`. Fix test call sites: `newTreeCounter()` → `newTreeCounter(0)` (default) in `agent_misc_program_fuzz_test.go`, `session_init_seed100_exact_fuzz_test.go`, and `tree_counter_test.go`; in `TestCounter17thFails` keep cap-16 semantics with `sess.treeCounter = newTreeCounter(16)` after session construction and update `wantErr` to the new text with "16 delegate turn slots in use".

- [ ] **Step 4: Run, verify pass** — `cd agent && go test -run 'TreeCounter|Counter17th|TestCounter' -count=1 .` then `go test -run 'Delegate|Subagent' -count=1 .`

- [ ] **Step 5: Commit** — `git commit -am "agent: tree counter takes configured cap; typed tree_at_capacity error"`

---

### Task 3: Retained-terminal cap 2048 + manager plumbing

**Files:**
- Modify: `agent/subagent_manager.go:39,47-56`
- Modify: `agent/session_init.go:163,507`
- Test: `agent/subagent_manager_test.go`; fix `newSubagentManager(nil)` call sites (e.g. `agent/job_delegate_test.go` lean fixture)

**Interfaces:**
- Consumes: `SessionConfig.MaxRetainedTerminal` (Task 1).
- Produces: `newSubagentManager(emit func(events.EventKind, events.EventData), cap int) *subagentManager` (cap `<= 0` → `defaultMaxRetainedTerminal`); `defaultMaxRetainedTerminal = 2048`.

- [ ] **Step 1: Failing test** — in `agent/subagent_manager_test.go`:

```go
func TestNewSubagentManagerDefaultCap2048(t *testing.T) {
	m := newSubagentManager(nil, 0)
	if m.maxRetainedTerminal != 2048 {
		t.Fatalf("default cap = %d, want 2048", m.maxRetainedTerminal)
	}
	m = newSubagentManager(nil, 7)
	if m.maxRetainedTerminal != 7 {
		t.Fatalf("explicit cap = %d, want 7", m.maxRetainedTerminal)
	}
}
```

- [ ] **Step 2: Run, verify fail** — `cd agent && go test -run TestNewSubagentManagerDefaultCap2048 -count=1 .`

- [ ] **Step 3: Implement** — `defaultMaxRetainedTerminal = 2048`; signature change; `if cap <= 0 { cap = defaultMaxRetainedTerminal }` in the constructor; call sites pass `cfg.MaxRetainedTerminal` (`session_init.go`) / `0` (test fixtures). Existing tests that set `sess.subagents.maxRetainedTerminal = 2` directly are untouched.

- [ ] **Step 4: Run, verify pass** — `cd agent && go test -run 'SubagentManager|ReserveSlot' -count=1 .` and `go build ./...`

- [ ] **Step 5: Commit** — `git commit -am "agent: retained-terminal cap defaults to 2048, configurable per session"`

---

### Task 4: CLI flags + launch surfaces

**Files:**
- Modify: `cmd/serf/main.go:184,234` (flag registration + runCfg field), `cmd/serf/run.go:43,236` (struct field + apply), `cmd/serf/serve.go:209,376` (flag + apply)
- Modify: `cmd/serf-hub/internal/launchconfig/types.go:27`, `schema.go:91`, `merge.go:123`, `args.go:39`, `wire.go:17,60`
- Modify: `appwire/types.go:1154`
- Modify: `cmd/serf-tui/internal/launchconfig/launch_settings_panel.go:256,387`, `launch_schema.go:120`
- Modify: `docs/conventions/naming.md` (add rows)
- Test: `cmd/serf/run_flags_fuzz_test.go` pattern; hub `merge`/`args` tests; `appwire/wiretypes_fuzz_test.go:35`

**Interfaces:**
- Consumes: Task 1 fields.
- Produces: CLI `--max-concurrent-delegates`, `--max-retained-terminal` (both `-1` = default); toml `max_concurrent_delegate_turns`, `max_retained_terminal`; wire `maxConcurrentDelegateTurns`, `maxRetainedTerminal` (`*int`).

- [ ] **Step 1: Failing test** — extend a run-flag test in `cmd/serf` (mirror the `maxSubagentDepth` case): parse `--max-concurrent-delegates 12 --max-retained-terminal 300`, assert both land on `baseSessionCfg`. Exact placement: the test file that covers `cfg.maxSubagentDepth` (`run_coverage_fuzz_test.go:182` region / run flag tests).

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement** — replicate the `maxSubagentDepth` pattern at every site listed:
  - `main.go`: `flags.maxConcurrentDelegates = fs.Int("max-concurrent-delegates", -1, "max concurrently running delegate turns per session tree (default: 50)")` and `flags.maxRetainedTerminal = fs.Int("max-retained-terminal", -1, "max retained terminal delegate records per session (default: 2048)")`; add to the runCfg literal and the `runCfg` struct (`run.go:43` region).
  - `run.go`: `if cfg.maxConcurrentDelegates >= 0 { baseSessionCfg.MaxConcurrentDelegateTurns = cfg.maxConcurrentDelegates }` (and retained equivalent).
  - `serve.go`: same two flags + `sessionCfg` assignment.
  - Hub launchconfig: two `*int` toml fields in `types.go`, two `LaunchControlInteger` schema entries in `schema.go` (Group `LaunchGroupLimits`, labels "Max concurrent delegates" / "Max retained terminal delegates"), two merge blocks in `merge.go`, two `add(...)` lines in `args.go`, two `copyIntPtr` lines in each `wire.go` function.
  - `appwire/types.go`: `MaxConcurrentDelegateTurns *int \`json:"maxConcurrentDelegateTurns,omitempty"\`` and `MaxRetainedTerminal *int \`json:"maxRetainedTerminal,omitempty"\`` beside line 1154; add both to `wiretypes_fuzz_test.go`'s schema map.
  - TUI: panel row + setter case mirroring `max_subagent_depth` in both files.
  - `docs/conventions/naming.md`: rows for both knobs.

- [ ] **Step 4: Run, verify pass** — `go build ./... && go test ./cmd/serf/ ./cmd/serf-hub/internal/launchconfig/ ./cmd/serf-tui/internal/launchconfig/ ./appwire/ -count=1`

- [ ] **Step 5: Commit** — `git commit -am "cmd,appwire: --max-concurrent-delegates / --max-retained-terminal across CLI, hub, TUI, wire"`

---

### Task 5: job_list offset windowing + honest footer

**Files:**
- Modify: `agent/jobs.go:428` (`listFilter` struct), `agent/jobs.go:750-789` (`listWithError`)
- Modify: `agent/session_tools_jobs.go:32,1219-1230` (`jobListResult`), `:1877-1900` (arg parse), `:1041` (footer), `agent/jobs_nested.go:65` (`walkDescendantJobs`)
- Modify: `agent/internal/tool/definitions.go` (job_list `limit` param region — add `offset`)
- Test: `agent/session_tools_jobs_list_test.go`

**Interfaces:**
- Produces: `listFilter.Offset int`; `listWithError(filter) ([]*jobstore.JobRecord, int, error)` (jobs page, total, err); `jobListResult.Offset, Total int` (json `offset`, `total`); footer `showing A-B of N jobs.` when `Offset > 0 || Total > len(page)`, else `N job(s).`; delegate footer `%d delegate(s).` when delegates present.

- [ ] **Step 1: Failing test** — seed 120 jobs via the existing job-list fixture pattern in `session_tools_jobs_list_test.go` (see how it creates delegates/shell jobs), then:

```go
// offset window
res := callJobList(t, s, map[string]any{"limit": 50, "offset": 50})
if !strings.Contains(res, "showing 51-100 of 120 jobs.") {
	t.Fatalf("footer missing window: %q", lastLine(res))
}
// default full page keeps old shape
res = callJobList(t, s, nil) // with <50 jobs fixture
if !strings.Contains(res, " job(s).") || strings.Contains(res, "showing") {
	t.Fatalf("full-page footer changed shape: %q", lastLine(res))
}
// negative offset rejected
if _, err := jobListFilterFromArgs(map[string]any{"offset": -1}); err == nil {
	t.Fatal("negative offset accepted")
}
```

(Use the file's existing helpers; if `callJobList` doesn't exist, invoke `jobListTool(s, args, 0)` and format via `formatJobList` as neighboring tests do.)

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement** —
  - `listFilter`: add `Offset int`.
  - `jobListFilterFromArgs`: parse `offset` via `shellIntArg`; error `"offset must be non-negative"` when `< 0`.
  - `listWithError`: compute `total := len(jobs)` after sort; slice `lo := min(filter.Offset, total)`, `hi := min(lo+filter.Limit, total)` (guard `filter.Limit > 0`); return `(jobs[lo:hi], total, nil)`. Update callers (only `session_tools_jobs.go:916`).
  - `walkDescendantJobs`: it takes `filter`; apply the same offset+limit window to its merged result before returning (check whether it currently applies `filter.Limit` itself — if limiting happens inside per-child `listWithError` calls, move windowing to the merged slice so the window is global).
  - `jobListResult`: add `Offset int \`json:"offset,omitempty"\``, `Total int \`json:"total"\``.
  - Footer in `formatJobList`:

```go
	if out.Offset > 0 || out.Total > len(out.Jobs) {
		fmt.Fprintf(&b, "\nshowing %d-%d of %d jobs.", out.Offset+1, out.Offset+len(out.Jobs), out.Total)
	} else {
		fmt.Fprintf(&b, "\n%d job(s).", out.Count)
	}
	if len(out.Delegates) > 0 {
		fmt.Fprintf(&b, " %d delegate(s).", len(out.Delegates))
	}
```

  - `definitions.go` job_list schema: add `"offset": {"type": "integer", "description": "Window start into the newest-first listing. Default 0."}`.

- [ ] **Step 4: Run, verify pass** — `cd agent && go test -run 'JobList' -count=1 .`

- [ ] **Step 5: Commit** — `git commit -am "agent: job_list offset windowing with 'showing A-B of N' footer"`

---

### Task 6: Phase-1 docs + gate

**Files:**
- Modify: `docs/job-control.md` (tree-cap section ~lines 1279-1296)

- [ ] **Step 1:** Update the spec text: cap 16 → configurable (default 50) via `--max-concurrent-delegates`; new error text; retained-terminal default 2048 via `--max-retained-terminal`; job_list windowing note.
- [ ] **Step 2:** `make test-short` (or `go test ./agent/ ./cmd/... -count=1`) green; `make vet`.
- [ ] **Step 3:** Commit — `git commit -am "docs: job-control tree-cap section reflects configurable caps"`

**Phase 1 review checkpoint.**

---

## Phase 2 — Occupancy observability

### Task 7: Reservation kinds + occupancy tuple

**Files:**
- Modify: `agent/tree_counter.go`, `agent/jobs.go:226-248` (`treeReservation`)
- Modify: `agent/subagents.go:734` (spawn reserve), `agent/job_delegate.go:1965` (resume reserve), `agent/subagents.go:975` (drive reserve)
- Test: `agent/tree_counter_test.go`

**Interfaces:**
- Produces: `type slotKind int` (`slotKindJob`, `slotKindDrive`); `(s *Session) reserveTreeSlot(kind slotKind) (*treeReservation, bool)`; `(c *treeCounter) occupancy() (total, jobs, drives, cap int64)`. `treeReservation` gains `kind slotKind`; `release` decrements per-kind. `reserveTreeSlot` keeps its current signature for the in-package test hook — instead: change signature to take kind and update all callers (preferred; tests included).

- [ ] **Step 1: Failing test**:

```go
func TestTreeCounterOccupancyByKind(t *testing.T) {
	c := newTreeCounter(4)
	r1, _ := reserveOn(c, slotKindJob)  // test helper or direct c.reserve(slotKindJob)
	r2, _ := reserveOn(c, slotKindDrive)
	total, jobs, drives, cap := c.occupancy()
	if total != 2 || jobs != 1 || drives != 1 || cap != 4 {
		t.Fatalf("occupancy = %d/%d/%d cap %d", total, jobs, drives, cap)
	}
	r1.release()
	r1.release() // idempotent
	_, jobs, _, _ = c.occupancy()
	if jobs != 0 {
		t.Fatalf("jobs = %d after release, want 0", jobs)
	}
	_ = r2
}
```

(Implementation shape: `treeCounter.reserve(kind slotKind) bool` CAS-increments `n`, then `nKind[kind].Add(1)`; `release` reverses both. Document that occupancy reads are approximate under concurrent reserve/release — diagnostic only.)

- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** kinds on counter + reservation; thread kind through the three reserve sites (spawn/resume → `slotKindJob`, drive → `slotKindDrive`).
- [ ] **Step 4: Run pass** — plus existing counter tests stay green (`TestCounterIdleFreesAndRestartRebuild`, `TestCounter17thFails`).
- [ ] **Step 5: Commit** — `git commit -am "agent: tree slots carry holder kind; occupancy() tuple"`

---

### Task 8: Occupancy surfaces — error clause + job_list + status

**Files:**
- Modify: `agent/tree_counter.go` (`treeCapacityError` gains `jobs, drives int64`; `Error()` appends clause)
- Modify: `agent/subagents.go`, `agent/job_delegate.go` failure sites → populate from `s.treeCounter.occupancy()`
- Modify: `agent/session_tools_jobs.go` (`jobListTool`/`formatJobList` footer)
- Modify: `agent/status.go` (detailed status; near `detailedStatusTerminalJobsLimit` usage)
- Test: `agent/tree_counter_test.go`, `agent/session_tools_jobs_list_test.go`

**Interfaces:**
- Consumes: Task 7 `occupancy()`.
- Produces: error text `tree_at_capacity: N delegate turn slots in use across this session tree (J delegate jobs, D drive turns). Wait for ...`; job_list footer line `delegate turn slots: X/Y in use (J jobs, D drive turns).` printed only when `X > 0`; status shows the same tuple when non-zero.

- [ ] **Step 1: Failing test** — saturate a cap-2 counter with one job slot + one drive slot; assert the formatted error contains `(1 delegate jobs, 1 drive turns)`; assert job_list on a session with held slots prints the `delegate turn slots:` line.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** — extend `treeCapacityError` and its two construction sites (read `occupancy()`); footer/status lines.
- [ ] **Step 4: Run pass** — `go test -run 'TreeCounter|JobList|Status' -count=1 ./agent`
- [ ] **Step 5: Commit** — `git commit -am "agent: surface slot occupancy in capacity error, job_list, status"`

**Phase 2 review checkpoint.**

---

## Phase 3 — Drive budget split + timeout + pacing

### Task 9: driveCounter — drives leave the spawn budget

**Files:**
- Modify: `agent/session_config.go` (`spawnConfig` gains `driveCounter *treeCounter` beside `treeCounter` ~line 414)
- Modify: `agent/session_init.go:121-130,461-470` (mint `defaultMaxConcurrentDriveTurns = 8` counter at root, inherit in children)
- Modify: `agent/subagents.go:961-1029` (`driveSubagentNotificationTurn` reserves from `s.driveCounter`)
- Test: `agent/tree_counter_test.go` / `agent/subagents_fuzz_test.go:515` region

**Interfaces:**
- Produces: `defaultMaxConcurrentDriveTurns = 8`; `Session.driveCounter *treeCounter` (nil-tolerant like `treeCounter`: nil = unbounded); drives reserve `slotKindDrive` slots from `driveCounter`, never from `treeCounter`.

- [ ] **Step 1: Failing test**:

```go
func TestDriveBudgetIndependentOfSpawnBudget(t *testing.T) {
	// fixture session with tree cap 1 and drive cap 1
	sess := newDelegateTestSession(t, c)
	sess.treeCounter = newTreeCounter(1)
	sess.driveCounter = newTreeCounter(1)
	// pin the drive budget: a spawn still reserves a tree slot
	dslot, ok := sess.driveCounter.reserve(slotKindDrive)
	if !ok { t.Fatal("setup: drive reserve failed") }
	defer dslot.release()
	if _, ok := sess.reserveTreeSlot(slotKindJob); !ok {
		t.Fatal("spawn slot blocked by saturated drive budget")
	}
	// pin the tree budget: driveSubagentNotificationTurn still reserves
	// (covered by driving a child with pending attention — reuse the
	// subagents_fuzz_test.go:515 drive fixture, asserting launch succeeds
	// while treeCounter is full)
}
```

(For the second half, adapt the existing drive fixture rather than hand-rolling; the fuzz test at `subagents_fuzz_test.go:515` already drives a child at capacity — change it to pin the *drive* counter and assert the not-launch path, and add the positive case pinning the *tree* counter and asserting launch.)

- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** — spawnConfig field, mint/inherit, drive reserve switch. `reserveTreeSlot` untouched.
- [ ] **Step 4: Run pass** — `go test -run 'Drive|Counter' -count=1 ./agent`
- [ ] **Step 5: Commit** — `git commit -am "agent: drive turns on separate budget (cap 8), off the spawn tree counter"`

---

### Task 10: Drive timeout + re-drive pacing

**Files:**
- Modify: `agent/subagents.go:980-1028`
- Test: `agent/subagents_test.go` (new)

**Interfaces:**
- Produces: `var driveTurnTimeout = 5 * time.Minute`; `var driveRedriveMinInterval = 1 * time.Second` (vars, test-shrinkable). `driveCtx` from `context.WithTimeout`. Re-drive waits the interval via `s.sclock().After(...)` before recursing.

- [ ] **Step 1: Failing test** — two tests:
  1. Shrink `driveTurnTimeout` to ~50ms (t.Cleanup restore), start a drive whose child blocks (fake adapter step that blocks on a channel), advance/wait past timeout, assert the drive slot is freed (`driveCounter.occupancy()` back to 0).
  2. Shrink `driveRedriveMinInterval` is NOT needed — instead assert no immediate re-drive: child with persistent pending attention; after first drive completes, within the interval `driving` is false and no new drive has launched; after the interval elapses (real 1s sleep is too slow — instead shrink the var to 20ms and assert a re-drive happens within 1s but not within 5ms).

- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement**:

```go
var driveTurnTimeout = 5 * time.Minute
var driveRedriveMinInterval = 1 * time.Second
```

In `driveSubagentNotificationTurn`: `driveCtx, driveCancel := context.WithTimeout(context.Background(), driveTurnTimeout)`. In the deferred re-check, before the recursive `driveSubagentNotificationTurn` call:

```go
			select {
			case <-s.sclock().After(driveRedriveMinInterval):
			case <-driveCtx.Done():
				return
			}
```

- [ ] **Step 4: Run pass.**
- [ ] **Step 5: Commit** — `git commit -am "agent: drive turns time out at 5m; re-drive paced at 1s"`

---

### Task 11: Origin-failure regression test + docs close-out

**Files:**
- Test: `agent/tree_counter_test.go` (new regression test)
- Modify: `docs/job-control.md` (drive budget paragraph)

- [ ] **Step 1: Failing test** — `TestRegressionIdleDelegatesNeverBlockSpawn`: scripted adapter always communicating "done"; sequentially spawn 50 foreground delegates to completion (pattern: the study's scratch experiment); saturate the drive counter directly to 8; assert `createDelegate` for #51 succeeds; assert `job_list` output contains `delegate turn slots:` when slots are held and the windowed footer shape.
- [ ] **Step 2: Run, verify it passes against the Phase-3 tree** (it is the acceptance proof; if any assertion fails, that phase has a gap — fix before continuing).
- [ ] **Step 3:** `docs/job-control.md`: drive budget (8), timeout, pacing; error-text occupancy clause; `--max-concurrent-delegates` in the rollout paragraph.
- [ ] **Step 4:** Full gate: `make test-short && make vet` (or `go test ./... -count=1` scoped to agent+cmd if the full suite is slow), plus `go build ./...`.
- [ ] **Step 5: Commit** — `git commit -am "test,docs: idle-delegate spawn regression; drive budget docs"`

**Phase 3 review checkpoint → merge readiness.**

---

## Self-review notes (plan author)

- Spec coverage: items 1→Tasks 1,2,4; item 2→Tasks 7,8; item 3→Tasks 9,10; item 4→Tasks 1,3,4; item 5→Task 5; item 6→tests in every task + Task 11. Docs→Tasks 6,11.
- `errors.Is` compatibility preserved via `treeCapacityError.Is`.
- Nil-counter tolerance: sessions without a minted counter keep the unbounded nil path (`reserveTreeSlot` returns nil reservation, true) — drive counter mirrors this.
- Golden snapshot test (`agent/snapshot_golden_test.go`) unaffected: new fields are `omitempty` and unset in the golden fixture.
- Risk called out in spec (default 16→50 behavior change) is documented in Task 6/11 doc edits.
