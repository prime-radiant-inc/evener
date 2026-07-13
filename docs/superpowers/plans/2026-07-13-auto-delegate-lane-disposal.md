# Automatic delegate-lane disposal (no model in the loop)

**Status:** draft spec, rev 2 (rev 1 adversarially reviewed by two competing
reviewers; all confirmed findings addressed below, see §Review log)
**Problem owner:** Jesse
**Date:** 2026-07-13

## Problem

Isolation delegate worktree lanes (`dlg_*`) are only disposed in the parent
session's close path (`disposeDelegateLanesAtClose`, native worktree tools
spec §9 step 4). Merged-but-committed lanes are additionally only collected
by the `prune` operation of the `manage_worktree` tool, which the model must
invoke.

Observed failure mode (2026-07-13, this repo): one long-lived resumed session
(`01KX4DMT…`, alive since 2026-07-09) accumulated **21 locked delegate lanes**
whose branches were all fully merged into `main`. Nothing would ever collect
them: the session never closes, and no session runs the prune operation
unprompted.

Two lifecycle gaps:

1. **No mid-life disposal.** A delegate that reaches a terminal state keeps its
   lane (locked) until the parent session closes — for long-lived resumed
   sessions, effectively forever.
2. **No automatic prune.** Close-time disposal KEEPs changed lanes "for
   prune", but the prune sweep is model-invoked only. Merged kept lanes from
   closed sessions sit indefinitely.

## Goal

Delegate lanes are collected by serf itself, on time-and-event triggers, with
**zero dependence on the model calling a tool**. Close-time disposal semantics
are unchanged; this adds triggers and the minimum machinery to make mid-life
disposal safe.

## Non-goals

- Automatic collection of **managed (user/session) worktrees**. Only delegate
  lanes (`dlg_*`, delegate-provenance sidecars) are auto-collected. Managed
  worktrees a user deliberately kept — including a closed session's own
  worktree, unlocked at close precisely so resume can re-enter it — remain the
  province of the explicit `manage_worktree` `prune` operation. (rev-1 T3
  swept those too; review finding B5 killed that.)
- Reclaiming lanes locked by *other, live-or-dead* sessions (foreign
  `serf:dlg:` / `serf:<session>` locks). Cross-process liveness detection is a
  separate problem; those remain for manual prune. A resumed session reclaims
  its *own* crash residue via the sweeps below, since the session id persists
  across resume.
- Changing the lock decision core (`worktree.Decide`) or the
  non-force-remove safety ladder.

## Design

### D0. Disposal predicates (reused, not reinvented)

A lane is **collectible** iff its tree is clean (`worktree.CleanTree`) and
either of the *existing* predicates holds:

- `worktree.Unchanged(run, lane, sc.BaseSHA)` — tip == recorded base, no
  merge_target needed (covers detached-HEAD-created lanes and rebased/deleted
  targets), or
- `disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)` reports disposable —
  the shared two-arm merged test (`worktree.Merged`: ancestry **or**
  cherry/patch-equivalence, with remote-tracking-tip resolution), so squash-
  and rebase-merged lanes are collected too.

rev-1 hand-rolled a bare `merge-base --is-ancestor` test; review findings
A3/B2/B3 showed it both under-collects (squash merges, empty merge_target)
and contradicted the reuse non-goal. rev 2 reuses the predicates verbatim.

Branch deletion of a collectible lane is safe under either arm: unchanged →
tip == base, no work exists; merged → the work is reachable from the merge
target. The close path's "unchanged lane: tip == base, no work lost" comment
is generalized accordingly where the mechanics are factored out.

### D1. Mid-life lane sweep (owning session, in-process)

Add `Session.sweepOwnDelegateLanes()`: enumerate this session's isolation
lanes (as `ownedIsolationLanes` does), and dispose a lane iff **all** of:

- **(a) terminal, latest record:** judged against the delegate's **latest**
  job record / folded delegate state — not an arbitrary record for the
  delegate id (a resumed delegate has several; rev-1 was ambiguous, findings
  A7/B8). The latest job has `Status.IsTerminal()` and there is no
  running/queued follow-up job for the delegate.
- **(b) idle ≥ TTL:** at least `laneIdleTTL` (**30 minutes**) since the
  **latest** job's terminal timestamp (`EndedAt` / jobstore event `TS`).
- **(c) collectible** per D0.
- **(d) no live work:** `liveWorkUnder(lanePath)` reports nothing — no
  running jobs or subagent activity rooted under the lane (this session's
  view; lanes are locked with our own marker, so other sessions' work under
  them is not an expected state — see (e)).
- **(e) our lock:** lock state classifies as this session's own `serf:dlg:`
  marker (or unlocked crash residue) per `worktree.ClassifyReason` /
  `worktree.Decide`; foreign and session-switched-in locks are skipped,
  exactly as in `disposeOneDelegateLane`.

**Disposal steps, in order:**

1. **Reserve** (under `s.mu`, no git calls held under it): re-verify (a),
   check the delegate id is not already reserved, then add it to an in-memory
   `disposingDelegates` set. This set — not the mutex — is what excludes
   `delegate_send`: admission (both the retained-child and restore paths, see
   D4) refuses or waits-and-retries while the id is reserved. rev-1 claimed
   mutex-based mutual exclusion across the whole disposal, which is
   unimplementable under the "never hold s.mu across a subprocess" discipline
   (findings A1/B4); the reservation set is the concrete mechanism.
2. **Evict the retained child**: if the terminal delegate's child session is
   still retained in memory, close it (it is terminal and idle by (a)) and
   remove it from the subagent table, including `DisposeSandboxScratch` for an
   owned env — the same cleanup the parent close path performs before
   disposal. Mid-life disposal without this deletes the tree under a live
   child env (findings A2/B1). Only after eviction is the lane's directory
   fair game.
3. **Unlock → `git worktree remove` (non-force)**. A dirty-race refusal
   re-locks the lane, clears the reservation, and keeps it — existing
   behavior.
4. **Append `EventDelegateDisposed`** (durable mark; store is open mid-life).
5. **`git branch -D`**, delete sidecar, clear the reservation.

Steps 3–5 are the existing unchanged-lane mechanics factored out of
`disposeOneDelegateLane` for reuse; the close path is re-expressed on the
factored helper with identical behavior.

### D2. Triggers

All triggers: local execution environments only (same guard as the close
path), **top-level sessions only** — delegate/subagent child sessions run the
same lifecycle code but must not start tickers or open-time sweeps (finding
B6: every delegate spawn would otherwise fire repo-wide git sweeps and race
its siblings).

- **T1 — periodic:** a per-(top-level)-session housekeeping goroutine,
  `s.clock.NewTicker` every **10 minutes** (fake-clock testable), calling
  `sweepOwnDelegateLanes()`. **Quiesce ordering:** the close path signals the
  goroutine and **joins it** (waits for any in-flight sweep) *before*
  `disposeDelegateLanesAtClose` runs and before the jobstore closes — a
  mid-flight tick must not race close-time disposal on the same lane or
  append the disposed mark after `closeStoreOnly` (finding A6).
- **T2 — session open/resume:** run `sweepOwnDelegateLanes()` once shortly
  after open (off the turn path), so a resumed session promptly collects
  lanes that merged while it was down.
- **T3 — session open, cross-session lane residue:** one pass over
  **delegate lanes only** (entries whose sidecar records delegate provenance /
  `dlg_*` naming) that are **unlocked** and collectible per D0 — i.e. lanes a
  prior session's close KEPT for prune whose branches have since merged.
  Managed worktrees are explicitly out (see Non-goals). Error posture is
  **skip-and-continue per lane** — an automated sweep must not abort on the
  first `git worktree remove` / `branch -D` refusal the way
  `worktreePruneSweep1` does (findings A4/B7); a branch checked out elsewhere
  is a skip, not a wedge. Cross-process races (two serf processes opening
  simultaneously) resolve per-lane: git refuses the loser's remove/branch
  delete, which is a skip. Results emitted as a single info/warning event,
  not injected into the transcript.

T1+T2 handle the live-long-session case (the observed 21-lane pileup);
T3 handles the closed-session "kept for prune" case.

### D3. Failure posture

Every step best-effort, never `--force`, fail-safe toward preservation. A
sweep error on one lane clears that lane's reservation, skips it, and
continues; sweep-level errors are silent (T1) or a single warning event
(T2/T3). Sweeps run off the turn path and never hold `s.mu` across a git
subprocess; the only cross-component coupling is the `disposingDelegates`
reservation set.

### D4. `delegate_send` admission hardening

Two admission changes in `agent/job_delegate.go` (rev-1 omitted this file
from scope; findings A9/B1):

1. **Check `rec.Disposed` on the retained-child path too.** Today
   `assessDelegateResumability` (the only `Disposed` check) runs only when
   the child session is not retained; a retained terminal child resumes with
   no check. Mid-life disposal makes that path reachable-after-disposal, so
   the disposed refusal must be unconditional.
2. **Check the `disposingDelegates` reservation** (under `s.mu`) before
   admission proceeds on either path; a reserved delegate gets the same
   clear `target_not_resumable`-style refusal (or a bounded wait-then-retry —
   implementer's choice, but the refusal text must tell the model to start a
   new delegate).

## Constants

| name | value | why |
|---|---|---|
| `laneIdleTTL` | 30m | long enough for follow-up `delegate_send`, short enough to matter |
| `laneSweepInterval` | 10m | bounded staleness ≤ TTL+interval; negligible git cost |

## Testing (TDD, red → green per case)

Unit (fake clock + real-git fixtures via the existing worktree test harness):

1. Terminal + merged (ancestry) + idle ≥ TTL → removed, `EventDelegateDisposed`
   appended, branch and sidecar gone.
2. Same but **squash-merged** (cherry arm) → removed.
3. Unchanged lane with empty/deleted merge_target → removed (Unchanged arm).
4. Terminal + merged but idle < TTL → kept, still locked.
5. Resumed delegate: first job terminal 40m ago, latest job terminal 5m ago →
   kept (latest-record TTL).
6. Terminal + unmerged commits → kept forever by the sweep.
7. Non-terminal delegate / running follow-up job → skipped.
8. Retained child session: sweep evicts and closes it (sandbox scratch
   disposed) before removal; a `delegate_send` issued during disposal gets the
   reservation refusal; one issued after gets the disposed refusal — on the
   retained path specifically (regression for the D4.1 hole).
9. Reservation race: `delegate_send` admission wins the reservation → sweep
   skips the lane this round.
10. Foreign / session lock marker → skipped.
11. Dirty-race on remove → re-locked, reservation cleared, kept.
12. T1 quiesce: tick in flight when close starts → close waits; no disposed
    mark after store close; no double disposal.
13. T2 on resume collects lanes merged while the session was down.
14. T3 at open collects an unlocked merged delegate lane KEPT by a prior
    session's close; skips a managed (non-delegate) unchanged worktree; a
    branch checked out elsewhere is skipped and the sweep continues.
15. Child (delegate) sessions start no ticker and run no T2/T3.
16. Sweep disabled for non-local exec envs.

E2E: long-lived session spawns an isolated delegate, delegate's branch is
merged externally, fake clock advances past TTL+interval → lane, branch,
sidecar gone; the delegate's child session (if retained) was evicted; a
subsequent `delegate_send` returns the disposed-lane error.

## Files touched (est.)

- `agent/session_worktree_close.go` — factor disposal mechanics for reuse (~40 loc moved)
- `agent/session_worktree_sweep.go` (new) — sweep, reservation set, ticker, T3 lane pass (~250 loc)
- `agent/session_lifecycle.go` — start/join ticker, T2/T3 at open, child-session guard (~30 loc)
- `agent/job_delegate.go` — D4 admission hardening (~25 loc)
- tests (~600 loc)

## Review log (rev 1 → rev 2)

Two competing adversarial reviewers, findings verified against code:

- **A1/B4 (critical):** rev-1's "no window" serialization was unimplementable
  (mutex across subprocesses forbidden; Disposed mark lands after remove) →
  D1 step 1 reservation set + D4.2.
- **A2/B1 (critical):** retained terminal child sessions — disposal under a
  live child env, and `delegate_send`'s retained path never checks
  `Disposed` → D1 step 2 eviction + D4.1 + condition (d) `liveWorkUnder`.
- **A3/B2/B3 (major):** bare ancestor test under-collects (squash merges) and
  does not subsume Unchanged (empty merge_target etc.) → D0 reuses
  `Unchanged` OR `disposableReason`.
- **A4/A5/B5/B6/B7 (major):** auto-running `worktreePruneSweep1-3` from every
  session open: abort-on-first-error posture, cross-process/in-process races,
  silent deletion of deliberately-kept managed worktrees, delegate children
  triggering repo-wide sweeps → T3 rescoped to unlocked delegate lanes only,
  skip-and-continue, top-level sessions only; managed worktrees moved to
  Non-goals.
- **A6 (major):** ticker vs close race → T1 quiesce/join ordering.
- **A7/B8 (minor):** arbitrary-record terminal/TTL ambiguity → latest-record
  semantics in (a)/(b).
- **A8 (minor):** there is no `worktree_prune` tool; it's the `prune`
  operation of `manage_worktree` → prose fixed; non-local guard applies to
  all triggers.
- **A9 (minor):** `job_delegate.go` missing from scope → added.
- **B9 (minor):** E2E as written couldn't pass without D4.1 → test 8 +
  E2E updated to pin the retained-path refusal.
