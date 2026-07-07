# Agent test-suite speedup — investigation & findings

Date: 2026-07-07
Status: **INVESTIGATED AND STOPPED.** Three approaches were spiked; none reached
the ~15s goal. The acute problem that motivated this (a flaky, timing-out
`-race` gate) was fixed separately and is green on main. See "Outcome" at the
bottom. This doc is kept as a findings record so the work is not re-tread.
Base: branch `perf/test-suite-speedup` off main `6dd4c69d`. Anchors verified 2026-07-07.

## Problem

The `agent` root package's `go test` wall-clock regressed from a reliable ~15s
to ~195s. Two contributors are already fixed on main (fuzz files leaked into the
unit gate; a per-session `git rev-parse` fork). The remaining cost was
mis-diagnosed as environment work; **a spike disproved that** — routing a
pure-logic test onto a fake environment saved ~8%. A CPU profile of a
representative pure-logic test showed it is **~97% blocked/waiting**, not
computing and not doing FS/subprocess work.

The wait is **real wall-clock time spent on the session's async timers**.
`SessionConfig.clock` defaults to `clock.Real()` (session_config.go:368,
jobs.go:394), and only 18 of 129 `newSession` call sites inject a fake clock —
so ~110 test sessions run on the real clock and pay real-time waits on every
`jm.clock` timer/ticker/backoff (delegate finalize, watch/quiet watchdogs,
shell finalize backoff, job-tree drain recheck, close grace, …). Confirmed:
dropping just one such interval (`drainRecheckInterval` 250ms→1ms) cut a
drain-heavy pilot file 65%. The aggregate barely moved because each family
waits on a *different* real-clock timer; the common root is the real clock.

## Goal

Make test sessions never wait real wall-clock time on the session's own timers,
without changing any test's logical assertions. Target: agent suite wall-clock
materially toward ~15s. Non-goal: changing behavior for tests that legitimately
assert on real elapsed time.

## Decisions (Jesse, 2026-07-07)

1. **Auto-advancing fake clock as the default test clock**, with an opt-out for
   tests that need the real clock.
2. **SDD with fan-out** for execution.
3. **Validation-first**: build the auto clock and prove the aggregate win on a
   pilot before the mass default flip; if it does not deliver, stop cheaply.
4. **Clean mocks where incidental** — never leave a test whose assertions only
   re-derive the fake's behavior. The auto clock changes only *speed*, never a
   logical outcome, so migrated tests still assert on real session/agent logic.

## Design

### Component 1 — the auto-advancing clock

Extend `agent/internal/agenttest.FakeClock` (already a full `clock.Clock` with
`Advance`, `BlockedCount()`, and the `BlockUntil(n)` quiescence handshake) with
an auto-driver:

```go
// AutoAdvance starts a background goroutine that advances virtual time to the
// next pending waiter once the system is quiescent (waiters are parked and the
// blocked count is stable across a short settle), firing timers deterministically
// with no wall-time wait. Returns a stop func; the session registers it with
// t.Cleanup via the testkit. Safe to call once per clock.
func (f *FakeClock) AutoAdvance() (stop func())
```

Quiescence heuristic: yield (`runtime.Gosched`) a few times to let session
goroutines run and park; when there is ≥1 waiter and `BlockedCount()` is stable
across the settle, `Advance` to the earliest waiter's deadline (firing it), then
repeat. This preserves ordering (earliest waiter first) and lets goroutines do
their non-clock work between advances. The driver stops at session Close.

### Component 2 — the testkit default + opt-out

`newSession` (testkit_test.go:64) defaults `SessionConfig.clock` to a fresh
auto-advancing `FakeClock`, registering its `stop` with `t.Cleanup`. Add:

```go
// withRealClock keeps the session on clock.Real() — for tests that assert on
// real elapsed wall-clock time or real-timeout behavior.
func withRealClock() sessionOpt
```

Tests that already inject a clock via `withConfig(SessionConfig{clock: ...})`
are unaffected (their explicit clock wins). Direct-`NewSession` test helpers
(subagents_test.go, etc.) get the same default where a migration reaches them.

### Component 3 — close the one clock leak

`session_jobtree_drain.go:157` uses a real `time.NewTicker(drainRecheckInterval)`
— the only session-runtime wait that bypasses the injectable clock. Change it to
`s.clock.NewTicker(drainRecheckInterval)` (production keeps `clock.Real()` and
the 250ms interval; behavior identical in prod). This is the only production
(non-test) change.

### The guardrail

The auto clock must change only speed. A migrated test asserts the same logical
outcome, just without waiting. Two mechanical safety nets:

1. **A flipped test that fails is reverted to `withRealClock()`** — a test that
   depends on real timing breaks loudly (a timing assertion), not silently.
2. **Logical-outcome preservation** — the auto clock fires each timer at its
   virtual deadline in order, so "X happens after the delegate-finalize timeout"
   still happens (at virtual time), only instantly. Tests asserting on *real*
   elapsed duration are the ones that opt out.

### Data flow / error handling

Only `session_jobtree_drain.go` changes in production, and only to read the
already-present `s.clock`. All other changes are test-only. Session construction
is unchanged except the default `clock` value.

## Migration process (fan-out)

1. **Task 1 (foundational, one subagent):** build `AutoAdvance` + unit tests;
   fix the drain leak. Prove a single drain-heavy test (`session_workmillis`)
   goes fast and stays green on the auto clock.
2. **Task 2 (validation gate, one subagent):** wire the default + `withRealClock`;
   run the full agent suite; record the aggregate wall-clock delta and the list
   of tests that failed on the auto clock. **If the aggregate does not drop
   materially, stop and report** — the assumption was wrong.
3. **Tasks 3..N (fan-out):** partition the failing tests by family into disjoint
   file sets; one subagent per set adds `withRealClock()` to the timing-dependent
   tests (or fixes a genuine bug the auto clock exposed), verifies its set green
   under `-race`, commits. Batches are file-disjoint so they fan out without
   conflict; the controller re-runs the full suite after merging.

## Testing

- `AutoAdvance` unit tests: a timer/ticker/sleep fires without real wall-time;
  ordering across multiple waiters is preserved; stop() ends the driver.
- Each batch passes under `-race -short` (the CI gate mode) and plain `go test`.
- Full agent suite passes after Task 2 and after each fan-out batch.
- Wall-clock measured before/after Task 2 and per batch.

## Success criteria

- Agent suite wall-clock materially toward ~15s; `-race` gate stays green.
- No test's logical coverage weakened (timing tests keep the real clock).
- Stop point: remaining wall-clock is dominated by tests on `withRealClock` that
  genuinely need real time, or by non-clock cost (the honest floor).

## Out of scope

- A fake/in-memory exec-env (disproved as the cost).
- Forcing `t.Parallel()` additions.
- Changing production timer intervals.
- Any production change beyond the one drain-ticker clock routing.

## Estimate

Rough, loc: `AutoAdvance` + tests ~80-140; testkit default + opt-out ~15; drain
leak 1 line. The bulk is the fan-out triage of tests that need `withRealClock`
(1-line each), sized by Task 2's failure list. Validation-first and stoppable at
Task 2 if the lever doesn't deliver.

---

## Outcome (2026-07-07) — investigated, stopped

The agent suite is ~195s. Root cause is diffuse, not a single lever: ~2000
session-constructing tests at ~0.42s each, a legitimate ~4x growth in test
count since the <15s days, and per-test cost that is dominated by **real
wall-clock waiting on the session's async timers** (a pure-logic test profiled
~97% blocked, not computing and not doing FS/subprocess work).

Three approaches were spiked before committing to a rollout:

1. **Fake exec-env** — disproved. Routing a pure-logic test onto a no-op
   environment saved ~8%; the environment is not the cost.
2. **Shorten one real-time interval** (`drainRecheckInterval` 250ms→1ms) — cut a
   drain-heavy pilot file 65% but moved the full suite only 195s→188s (noise):
   each family waits on a *different* real-clock timer; the common root is the
   real clock, but no single interval dominates the aggregate.
3. **Auto-advancing fake clock as the test default** — the most promising, and
   the one that failed at scale. A single pilot file dropped 76%, but the full
   suite stayed at ~195s with 58 failures and CPU rose 252s→508s. Root cause is
   fundamental: an auto-advancing clock **spins firing periodic tickers**
   (drain, watchdogs) forever, converting wait-time into CPU spin rather than
   freeing wall-clock. (This is why clockwork-style libraries do not
   auto-advance.)

**What shipped from this investigation:** only the drain-ticker clock routing
(`session_jobtree_drain.go`: `time.NewTicker` → `s.clock.NewTicker`), which is a
harmless correctness/consistency improvement — production keeps `clock.Real()`
and the 250ms interval, so behavior is identical, and the drain fallback is now
injectable like every other session timer.

**Suggested future direction (unspiked):** a *fast virtual clock* — virtual
time = real-elapsed × N (e.g. 1000×) — so a 250ms timer fires after ~250µs of
real time with `Now()` kept consistent. It sidesteps the auto-advance
quiescence/spin problem (it never has to decide *when* to advance; time simply
runs fast) and preserves real-clock semantics. It would need its own spike to
confirm it delivers at aggregate scale and does not break timing-sensitive
tests.

**Recommendation:** the acute `-race`-gate flakiness/timeouts and the mcp/fuzz
regressions are already fixed on green main; `<15s` is a separate, scoped
investigation, best resumed from the fast-virtual-clock idea rather than the
auto-advance approach.
