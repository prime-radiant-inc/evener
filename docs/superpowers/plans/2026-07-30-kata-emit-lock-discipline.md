# Kata: the emit-under-a-lock rule has no enforcement, and its failure mode got much worse

## The problem

An event emitter must not hold any lock that the authoritative event consumer
takes. Nothing checks this. It is maintained by hand, at every call site, by
whoever last touched the file.

Until recently, breaking the rule cost a dropped event — invisible, and healed
by the next read. `agent/session_events.go`'s `sendEvent` now blocks when an
authoritative consumer is attached, so breaking the rule costs a **deadlocked
daemon**. The same mistake, with the same appearance in review, now has a
categorically different consequence.

The blocking send holds `eventsMu.RLock`, and `Close()` needs `eventsMu.Lock`.
So a wedge does not merely stall emission: the session can no longer be torn
down, and `Close()`'s `sendersWG.Wait()` never returns. The comment at
`agent/session_events.go` documents this deliberately, under "SCOPE OF THE
WAIT, because it is wider than it looks."

## Evidence, all verified in this tree

**The rule is real and already had to be applied by hand three times.** The
design spec (`docs/superpowers/specs/2026-07-29-appwire-authoritative-rejoin-design.md`,
"What remains, precisely") records it as an open residual: three call sites were
found and fixed, all the same callee reached from three different locked
callers, and there is no compiler or vet check for the next one.

**One class of violation fails loudly, and it is not the dangerous one.**
`Session.emit` calls `s.activeCausalProvenance()` (`agent/session_provenance.go:32-36`),
which takes `s.mu`. Go mutexes are not reentrant, so `emit` under `s.mu`
self-deadlocks immediately and locally — you find it the first time you run it.

**The dangerous class bypasses that.** `emitWithProvenance` takes the
provenance as a parameter and never touches `s.mu`. The jobManager is wired to
it directly — `jm.emit = s.emitWithProvenance` at `agent/session_init.go:196`
and `:574` — so a jobManager emit gets no self-deadlock guard at all. It will
sit and block.

**The consumer provably takes jobManager locks.** `server.BridgeEvent` reaches
`DetailedStatus` → `jobManager.list` → `jobstore.Store.Load()`, which opens and
reads `jobs.jsonl` under `jobstore.Store.mu`; `agent/jobs.go:753-772` calls
`jm.store.Load()` before `jm.mu.Lock()`. So the producer and the consumer
demonstrably contend for the same locks.

**Today the discipline holds, and you can see how narrowly.** All four callers
of the two `jm.emit` sites unlock on the line *immediately before* the emit:

| Emit | Enclosing function | Unlock |
|---|---|---|
| `agent/jobs.go:741` | `createShell` | `:740` |
| `agent/jobs.go:1523` | `emitFinishedJob` | `:1515` |
| `agent/job_shell.go:532` | `commitDelayedShell` | `:531` |
| `agent/job_delegate.go:2125` | `attachDelegateJobWithRestoreAndDelegate` | `:2124` |

There are roughly three dozen `jm.mu.Lock()` sites in `agent/jobs.go` alone. A
refactor that hoists an emit three lines, or wraps a caller in a new critical
section, deadlocks the daemon — and no test, vet check, or lint rule notices.

## What is not known

Whether any *other* emitter, anywhere, holds a lock the consumer takes. Nobody
has audited this. The four sites above are the ones a reviewer happened to
follow; they are not the result of a sweep.

Also unknown: whether an audit is even the right unit of work, or whether the
class should be closed structurally so it cannot recur. Both a completed audit
that stays true for one week and a mechanism that makes the rule enforceable
are useful answers. Work out which is worth its cost here and say why.

## Corrections to what earlier reports said

Two claims in the review record are wrong. They are recorded here so nobody
re-derives them.

- A reviewer wrote that "`BridgeEvent`'s comment admits this." It does not.
  `BridgeEvent`'s comment discusses `acceptsSessionEvent` being a cheap
  early-out that is not the guard. The admission is in the spec residual.
- The same report scoped this as "~70 sites, unaudited." That number does not
  correspond to anything I could reproduce. Establish the real surface yourself
  as part of the work, and state how you counted.

There is also a standing recommendation, **not** to be implemented as written:
"wrap `Session.mu` in an owner-tracking mutex under `serffuzz` and assert at
`sendEvent`'s blocking branch." A reviewer showed it buys less than it claims —
emit-under-`Session.mu` is already an immediate self-deadlock by construction,
so that half needs no enforcement, and the genuinely unguarded half is the
*non-Session* locks, which wrapping `Session.mu` does not cover. If you reach
for enforcement, this specific shape is not it.

## Constraints

- The blocking send stays. It is a decided design: with an authoritative
  consumer attached a drop is permanent projection corruption, and with nothing
  attached — every subagent and delegate, by design — the drop is the only
  behaviour that is not a wedge. Do not reintroduce dropping on the
  authoritative path, and do not add an unbounded buffer; that trades a
  correctness bug for a memory bug.
- No compatibility path, feature flag, or fallback.
- Read `AGENTS.md` and `docs/testing.md` before changing code or tests.
- Tests: no sleeps, no wall-clock races, no network, no credentials. Output must
  be pristine. Score any mutation on exit code AND `=== RUN` AND a named
  `--- FAIL:` line — on this codebase a syntax error, a hang, and a panic all
  read exactly like a caught mutation.
- `make lint` and untagged builds do not see `//go:build serffuzz` files. If you
  put anything behind that tag, sweep `go vet -tags serffuzz` per module across
  all eight in `go.work` — a root-level `./...` misses siblings.

## Push back

This kata was written from a verified read, but katas on this repo have been
wrong in their specifics often enough that the convention says to expect it. If
the premise does not hold — if the discipline is enforced somewhere I did not
find, if the consumer does not actually reach those locks on the path that
matters, if the four sites are not representative — say so and stop. Recording
that correction is worth more than the fix.
