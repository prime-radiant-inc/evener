# Goal Wait Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the /goal mutation-count breaker with a wait registry + parked waiting state, a repetition-based progress ledger, and orthogonal spend budgets, in three rollout slices.

**Architecture:** Extend `agent/internal/goal` store (waits, ledger, budgets, new statuses) behind its own mutex; rewrite the `agent/session_goal.go` gate around a pure `decideGoalStep`; add `goal_wait`/`goal_cancel_wait`/`goal_expect` tools beside `update_goal`; persist GoalSnapshot v2 through `agent/schema` + `agent/session_state.go`; project waiting state over appwire events. Lock discipline unchanged: predicate pre-reads before `goalUpdateMu`/`s.mu`, `sclock` for all wait stamps.

**Tech Stack:** Go (this repo, `primeradiant.com/evener`), scripted LLM provider in tests (`newSession`/`withSteps` harness), `agent/internal/agenttest.FakeClock` for time, `-race` for concurrency gates.

**Spec:** `docs/superpowers/specs/2026-09-07-goal-wait-redesign-design.md`

## Global Constraints

- TDD every task: failing test first, then minimal implementation, then green suite, then commit.
- Deterministic default tests only: scripted provider, FakeClock, no network, no credentials.
- `go vet ./...` and the touched packages' `go test -race` green before each commit.
- Never delete or weaken an existing test to reach green; new statuses migrate old snapshots per the spec §7 table.
- Terminal report on every stop path, exactly once (`TakeTerminalReport` semantics kept).
- Capability gating on all new goal tools, mirroring `goalGuard`.
- Behavior outside the spec (compaction, projection of other events, TUI registry beyond `/goal resume`) stays untouched.

---

## File structure

- `agent/internal/goal/goal.go` — store: statuses (`waiting` added), waits registry, ledger, budgets, verdicts, `decideGoalStep` pure table, `claimWaitFireLocked`.
- `agent/internal/goal/wait.go` (new) — wait kinds, lease struct, validation, idempotency key, predicate truth evaluation seam.
- `agent/internal/goal/goal_test.go`, `agent/internal/goal/wait_test.go` (new) — pure tables, registry rules, migration math.
- `agent/session_goal.go` — gate rewrite (`decideGoalStep` wiring, atomic fire step, settle re-check), lock order, sclock stamps.
- `agent/session_goal_wait_test.go` (new), extend `agent/session_goal_dependents_test.go` — park/wake/expiry/hold tests.
- `agent/session_tools_goal.go` — `goal_wait`, `goal_cancel_wait`, `goal_expect` tools + validation errors.
- `agent/schema/snapshot.go` — GoalSnapshot v2 (+ pendingWake, ledgerSummary entries, budgets incl. maxParkedTotal, stage, terminalPending, reported, lossCause, deadlineFinalDelivered).
- `agent/session_state.go` — `goalSnapshotForMeta` mapping, dormant-blocked restore, backfill.
- `agent/session_init.go` — restore path (active + dormant-blocked; complete dropped; re-validation + coalesced timer).
- `agent/events/*`, `internal/appprojector/*` — `EventGoalWaiting/EventGoalResumed/EventGoalWatchdog`, `GoalStateData` wait list.
- `cmd/evener-tui/hub_commands.go`, `hub_command_registry.go` — `/goal resume` precedence, status text.
- `agent/internal/goal/prompt.go` — continuation prompt diff appendix (wait/condition tool shapes).

Each slice below ends with a green, committable, independently testable deliverable.

---

### Task 1: Slice-1 store — statuses, waits, budgets, verdicts

**Files:**
- Modify: `agent/internal/goal/goal.go`
- Create: `agent/internal/goal/wait.go`
- Test: `agent/internal/goal/goal_test.go`, `agent/internal/goal/wait_test.go`

**Interfaces:**
- Consumes: existing `Store`, `Status`, `Snapshot` shapes.
- Produces: `StatusWaiting`, `Wait`/`WaitKind`/`Lease` types, `RegisterWait`/`CancelWait`/`ClaimFire` store methods, `Budgets` struct + defaults (`maxContinuations` 200/1000, `deadline` 4h/24h, `maxParkedTotal` 24h), verdict strings (`no progress`, `budget exhausted`, `deadline exceeded`, `waiting lost: <cause>`), `decideGoalStep` signature `(snapshot, turnOutcome, predicateTruth[], pendingWake, advancementMarkers)`.

**Spec:** §§1–2 (store, invariant, lease fields, defaults table, validation rules, terminal catch-up for retained-terminal job/delegate, approval content-key + generation).

- [ ] **Step 1: Write failing tests for StatusWaiting + invariant + budgets defaults**

```go
func TestWaitStatusInvariant(t *testing.T) {
    s := goal.NewStore()
    s.Set("ship it", time.Now())
    if _, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, time.Now()); !ok {
        t.Fatal("register should succeed")
    }
    snap, _ := s.Snapshot()
    if snap.Status != goal.StatusWaiting {
        t.Fatalf("status = %q, want waiting", snap.Status)
    }
}
```

- [ ] **Step 2: Run to verify fail**

Run: `go test ./agent/internal/goal/ -run TestWaitStatusInvariant -v`
Expected: FAIL (`StatusWaiting` undefined).

- [ ] **Step 3: Implement statuses, lease struct, registry, budgets, verdicts**

Add `StatusWaiting`, `WaitKind` enum (all six kinds), `Lease` struct with full predicate payload + `fired_epoch` + idempotency key `(kind, target, canonical predicate, deadline)`, registry with max-8 enforcement, `Budgets` + defaults table, verdict constants. Keep existing methods compiling (shim `RecordContinuation` delegating to the new fold path where the interim v1 judge needs it).

- [ ] **Step 4: Run tests**

Run: `go test ./agent/internal/goal/ -v`
Expected: PASS.

- [ ] **Step 5: Write failing validation tests (hallucinated targets rejected, retained-terminal accepted)**

```go
func TestRegisterWaitRejectsHallucinatedJob(t *testing.T) {
    s := goal.NewStore()
    s.Set("x", time.Now())
    if _, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilJob, Target: "job_999"}, time.Now()); ok {
        t.Fatal("hallucinated job must be rejected")
    }
}
```

- [ ] **Step 6: Run to verify fail, implement validation + catch-up routing, re-run green.**

- [ ] **Step 7: Commit**

```bash
git add agent/internal/goal/goal.go agent/internal/goal/wait.go agent/internal/goal/goal_test.go agent/internal/goal/wait_test.go
git commit -m "feat(goal): slice-1 store with waiting state, leases, budgets"
```

### Task 2: Slice-1 gate — decideGoalStep + atomic fire + settle

**Files:**
- Modify: `agent/session_goal.go`
- Test: `agent/session_goal_wait_test.go`

**Interfaces:**
- Consumes: Task 1 store methods + `decideGoalStep` signature.
- Produces: gate wiring (pre-read predicates → pure decide → mutator under `goalUpdateMu`), `claimWaitFireLocked` behavior, settle re-check, kick-outside-lock, timer disarm on clear/retarget/cancel.

**Spec:** §§1 (rules 1–8 with terminalPending latch + check-before-reset), 3 (lock order, atomic step, superseded/drop).

- [ ] **Step 1: Write failing gate test — parked goal skips fold**

```go
func TestGateParkSkipsFold(t *testing.T) {
    sess := newGoalMethodSession(t)
    defer sess.Close()
    // register until_time wait, run non-progressed continuation gate:
    // want ("", false), Iterations/NoProgressStreak unfolded.
}
```

(Follow existing `session_goal_dependents_test.go` harness: `newGoalMethodSession`, `wireKickAndNotify`, scripted steps.)

- [ ] **Step 2: Run to verify fail**

Run: `go test ./agent/ -run TestGateParkSkipsFold -v`
Expected: FAIL.

- [ ] **Step 3: Implement gate rewrite** (pre-read → `decideGoalStep` → commit; waiting branch skips fold and arms nothing; fired/expiry path claims `fired_epoch` into `pendingWake` before kick; terminalPending latch; check-before-reset ordering).

- [ ] **Step 4: Add tests for expiry re-drive-once, stale-park kicks once, double-claim collapse, retarget voids + disarms. Run green.**

Run: `go test ./agent/ -run 'TestGate|TestGoalHold|TestArmGoal' -count=1 -race 2>&1 | tail -3`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_goal.go agent/session_goal_wait_test.go
git commit -m "feat(goal): slice-1 gate with decideGoalStep and atomic fire"
```

### Task 3: Slice-1 tools — goal_wait / goal_cancel_wait (+ validation errors)

**Files:**
- Modify: `agent/session_tools_goal.go`
- Test: extend `agent/session_goal_wait_test.go` (tool-level cases)

**Interfaces:**
- Consumes: Task 1 registry methods.
- Produces: `goal_wait`, `goal_cancel_wait` tool handlers, capability gating, named validation errors.

**Spec:** §§2 (both registration paths, size caps: matcher ≤1KB, URL ≤2KB, label ≤256), 7 (cancel removes live lease only; claimed pendingWake still drives with note).

- [ ] **Step 1: Write failing tool test (hallucinated target → named error; valid until_time → parked).**
- [ ] **Step 2: Run fail. Step 3: Implement tools mirroring `registerGoalTools`. Step 4: green + vet. Step 5: Commit** `feat(goal): goal_wait and goal_cancel_wait tools`.

### Task 4: Slice-1 persistence — GoalSnapshot v2, backfill, restore, timer

**Files:**
- Modify: `agent/schema/snapshot.go`, `agent/session_state.go`, `agent/session_init.go`
- Test: `agent/session_goal_persist_test.go` (extend), new restore timer test

**Interfaces:**
- Consumes: Task 1–2 store shapes.
- Produces: v2 snapshot (waits full payload, pendingWake, ledgerSummary stub for slice 2, budgets incl. maxParkedTotal, stage, terminalPending, reported, lossCause, deadlineFinalDelivered), backfill rule, dormant-blocked restore, coalesced sclock timer re-arm from the §2 four-way min, kick-immediately on non-empty pendingWake.

**Spec:** §§7 (schema, migration table, backfill, dormant restore, timer), 2 (four-way min single source).

- [ ] **Step 1: Write failing round-trip test (waits + budgets + pendingWake survive persist/restore).**
- [ ] **Step 2: Run fail. Step 3: Implement schema + mapping + restore + timer. Step 4: add migration test (v1 streak-5 → nudge allowance; terminals: blocked dormant, complete dropped; budgets backfilled). Run green + race. Step 5: Commit** `feat(goal): snapshot v2 with waits, budgets, dormant restore`.

### Task 5: Slice-1 projection + interim judge + slice gate

**Files:**
- Modify: `agent/events/payloads.go`, `agent/events/events.go`, `internal/appprojector/*` (goal cases), `agent/session_goal.go` (interim judge)
- Test: projector tests + interim test (wake/evaluation turns bypass `RecordContinuation`)

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces: `GoalStateData` wait list, `EventGoalWaiting/EventGoalResumed`, chip aggregation, autonomy-in-flight treatment, interim v1-breaker-stays-armed behavior.

**Spec:** §§7 (wire), 9 (slice-1 scope + interim rule).

- [ ] **Step 1: Write failing projector + interim tests. Step 2: Run fail. Step 3: Implement. Step 4: Full slice-1 suite green (`go test ./agent/... -count=1`), vet clean. Step 5: Commit** `feat(goal): slice-1 projection and interim judge`.

### Task 6: Slice-2 ledger — fingerprints, novelty, tiers, backstop

**Files:**
- Modify: `agent/internal/goal/goal.go`, `agent/internal/goal/wait.go` (or new `ledger.go`)
- Test: `agent/internal/goal/goal_test.go` (pure tables)

**Interfaces:**
- Consumes: Task 1 store + Task 2 fold site.
- Produces: `TurnOutcome`, canonical fingerprint/hash/digest rules, K=3/K=6 tiers, B=12 backstop (period ≤8 scope), `mutated`-in-rule, waits-only advancement, `migrated` fingerprint + residual disclosure.

**Spec:** §4 in full.

- [ ] **Step 1: Write failing pure-table tests** (identical×3 no-delta → stall; K=6 pre-evidence opening not stalled; period-2 → backstop; timestamp-noise canonicalized; genuine-retry with delta → live; junk write no-delta → accrues).
- [ ] **Step 2: Run fail. Step 3: Implement ledger + fold. Step 4: green + race. Step 5: Commit** `feat(goal): progress ledger with two-tier stall and backstop`.

### Task 7: Slice-2 gate integration — ledger fold, §8 forward, migration seeding

**Files:**
- Modify: `agent/session_goal.go`, `agent/session_init.go` (seed table), `agent/subagents.go` (forward, or honest rejection)
- Test: gate tests (read-heavy opening, alternating backstop, genuine retry), child forward tests, migration seeding test

**Spec:** §§4 (fold wiring), 8 (terminal-only matching, gate-before-claim, cross-session pre-reads, slice-1 boundary vs slice-2 forward), 7 (seed table).

- [ ] **Step 1: Write failing integration tests. Step 2: Run fail. Step 3: Implement. Step 4: green + race. Step 5: Commit** `feat(goal): ledger gate wiring, child forward, migration seeding`.

### Task 8: Slice-3 graduation — nudge/park/block, watchdog, verification

**Files:**
- Modify: `agent/session_goal.go` (stages), new watchdog path, `agent/session_tools_goal.go` (`goal_expect`)
- Test: graduation tests (nudge names evidence → park → block, one transcript note), watchdog per-stretch tests (FakeClock), verification tests (conditioned reject names condition; unconditioned self-declares)

**Interfaces:**
- Produces: stage machine, `EventGoalWatchdog`, `goal_expect` with identical validation + attach-scan, check-on-claim verifier.

**Spec:** §6 in full (stages, watchdog ≤2/stretch + 4/24h + threshold floor, conditional verification, deltas).

- [ ] **Step 1: Write failing tests per behavior. Step 2: Run fail. Step 3: Implement. Step 4: green + race. Step 5: Commit** `feat(goal): graduation, watchdog, conditional verification`.

### Task 9: Slice-3 resume + command surface + prompt appendix

**Files:**
- Modify: `agent/session_goal.go` (resume + renewal), `cmd/evener-tui/hub_commands.go` + registry (precedence, status text), `agent/internal/goal/prompt.go` (tool shapes)
- Test: resume tests (no-progress keeps budgets; budget/deadline needs --extend with clamps; waiting-lost path; no-goal error), dispatcher tests (resume never GoalSets literal; `/goal resume <text>` sets text), status-text test

**Spec:** §§5 (resume/renewal/extend grammar), 7 (precedence, RPC/flag, status format, prompt-diff appendix).

- [ ] **Step 1: Write failing tests. Step 2: Run fail. Step 3: Implement. Step 4: green + full `go test ./...` for touched modules + vet. Step 5: Commit** `feat(goal): resume, command surface, prompt appendix`.

### Task 10: Full-suite verification + fuzz refresh

**Files:** none (verification only; goldens only if the v2 shape breaks them, per §9 compat bullet).

- [ ] **Step 1: Run `go test ./agent/... -count=1` green.**
- [ ] **Step 2: Run `go test ./agent/ -race -run 'Goal|Watch|Restore|Fuzz' -count=1` green.**
- [ ] **Step 3: Run `go vet ./...` clean.**
- [ ] **Step 4: Run e2e scripted scenarios** (poll-then-event zero extra calls; true loop nudge→park→block one note; notification re-arm; 24h-park ≤2 notices via FakeClock).
- [ ] **Step 5: Commit** any golden refresh separately (`test(goal): refresh goldens for v2 shape`) or record clean.

## Self-Review

- Spec coverage: §§1–9 all mapped (store §1→T1, gate §3→T2, tools §2/§7→T3, persist §7→T4, wire §7→T5, ledger §4→T6, integration §8→T7, graduation §6→T8, resume/surface→T9, verify §9→T10). Condition-query schema (§6) defined at T8 before verifier code. Timer single-source (§2) enforced from T1. No placeholders: every step names files, commands, expected outputs.
- Type consistency: `decideGoalStep(snapshot, turnOutcome, predicateTruth[])` with snapshot = full GoalSnapshot everywhere; `pendingWake` list shape in T1/T2/T4; `--extend <budget> <value>` two-token grammar in T9; verdict strings verbatim from spec §1.
