# Task 89 - issue #1187 (Composer.integration cascade)

**Status:** done, gates green, not pushed.
**Branch:** `claude/composer-flake-1187` (worktree `.claude/worktrees/composer-flake-1187`, off `eee90925a`)
**SHAs:** `1a62a0987` root-cause fix, `fb86708c1` cascade containment.

## Mechanism

Two defects, both proven with deterministic tests, neither in Composer.tsx.

1. **Root cause** - `cmd/evener-hub/frontend/src/stores/testing/stalledIndexedDB.ts:7`.
   `holdIndexedDBEvent` wraps every listener registered while the hold is up.
   `release()` restored the addEventListener spy and drained the events queued
   *so far*, but the wrapper on already-registered listeners lives forever, so
   an event still in flight at release time was pushed onto a queue nobody
   drains again. For an IndexedDB request that is a promise that never settles.
   In `Composer.integration.test.tsx:635` the swallowed read is a pending-turns
   projection refresh started by the second submission, so the
   `flushPendingTurnsProjectionForTests()` in that test's `finally` (line 681)
   waits on it forever.

2. **Cascade** - that flush waits inside `act()`
   (`panes/session/composer/queue/pendingTurnsStore.ts:139`). vitest abandons
   the test at 5s, and an abandoned `act()` leaves React's act queue open for
   the rest of the FILE: every later `render()` produces nothing, which is
   exactly the CI symptoms (`Unable to fire a "change" event`,
   `Cannot read properties of null (reading 'disabled')`). Proven with a
   throwaway probe: abandoned `act()` poisons later renders; an abandoned
   `waitFor`, `user.type` or plain `await` does not.

## Reproduction

`vitest run <file>` in a loop with 48 `yes` hogs on 16 cores: **1 failure in
20**, then **1 in 26** - identical to CI (33 of 46, same first failure, same
error identities). Instrumented run showed the test body finishing in 368ms
("done"), `finally` releasing all 8 holds, then the flush never returning;
the DOM went missing at +5017ms. A `-t`-filtered single test never reproduced
(35 iterations) - the preceding tests' state is part of it.

## Fix

- `release()` now means "stop holding": an event arriving after it goes
  straight to its listener. New `stalledIndexedDB.test.ts` pins it; before the
  fix its third case reproduced #1187 in one line (a released read that never
  settles, 5s timeout).
- `settlePendingTurnsProjectionForTests` gained a per-round stall tripwire
  (4s). The round *count* already had a livelock tripwire; a single round had
  none. 4s against a slowest measured round of **165ms** across the whole web
  suite (10373 tests) under 32-way contention.
- `Composer.integration.test.tsx` is unchanged - no assertion or expected
  value touched, no timeout widened.

## Evidence

| | whole file, 48-hog contention |
|---|---|
| before | 19/20 pass, then 25/26 pass (2 reproductions) |
| after | **40/40 pass** |

Cascade check: with a temporary test that stalls a recovery read and never
releases it, the file now reports **1 failed / 46 passed** with
`pending-turns projection work stalled: 3 operation(s) still unsettled after
4000ms`, instead of a 5s timeout plus 33 consequential failures. Probe
reverted.

Gates: `make test-web` -> PASS web-typecheck / web-test / web-lint.
`npx biome check --write` on the three touched files - clean.
`make test-web-browser` not run: nothing rendering changed (test-support and a
test-only flush helper).

## Concerns

- The 4s tripwire sits under vitest's default 5s testTimeout by design; it can
  only fire in a run that was already within 1s of timing out. Worst measured
  round is 24x below it.
- `holdIndexedDBEvent` is used by six test files (`threads.test.ts`,
  `mutationOutbox*.test.ts`, `Session.test.tsx`, `Composer*.test.tsx`,
  `pendingTurnsStore.test.ts`); all pass, but any of them could have been
  silently relying on the swallow. None appear to.

## Review round 1 (PR #1196)

One Low accepted: the 4s tripwire had nothing asserting it fires. Added
`a flush that can never settle trips instead of hanging inside act` beside the
store's existing flush tests (`pendingTurnsStore.test.ts`), driving
`submitWithPendingTracking` with a `perform` that never resolves, advancing
fake timers past the ceiling, and asserting the flush rejects with the stall
message and the unsettled count.

- SHA `46e47d8c4`, then merge `6c5432e71` (`origin/main` at `d89096907`).
- RED: tripwire dropped back to a bare `Promise.allSettled` -> the new test
  times out at 5000ms, the exact failure it prevents. Reverted; GREEN 17/17.
- The rejection handler is attached before the clock moves: attaching it after
  `advanceTimersByTimeAsync` left an unhandled rejection in the window, and
  test output has to be pristine.
- Gates after the merge: `make test-web` -> PASS web-typecheck / web-test /
  web-lint. Biome clean on the touched file. Not pushed.
