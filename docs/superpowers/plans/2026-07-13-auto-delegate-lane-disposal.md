# Automatic delegate-lane disposal (no model in the loop)

**Status:** draft spec, rev 3 (two adversarial review rounds, 2×2 competing
reviewers; all confirmed findings addressed — see §Review log)
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
are unchanged; this adds triggers plus the minimum machinery (a per-delegate
reservation, a quiescence gate, admission hardening) to make mid-life disposal
safe.

## Non-goals / accepted limitations

- Automatic collection of **managed (user/session) worktrees**. Only delegate
  lanes are auto-collected; managed worktrees remain the province of the
  explicit `manage_worktree` `prune` operation.
- Reclaiming lanes locked by *foreign* sessions (other live sessions, or dead
  sessions whose id this session doesn't carry). Cross-process liveness
  detection is out; those remain for manual prune. A resumed session reclaims
  its own crash residue via its own sweep (session id persists across resume).
- **Mid-tree coordinator gap (accepted):** a retained delegate that is itself
  a delegate *parent* owns its grandchild lanes (`ParentSessionID` semantics);
  the top session's sweep never sees them, and they're locked so T3 skips
  them. Those lanes are collected when the coordinator is evicted or closed
  (its close path runs `disposeDelegateLanesAtClose`), and any merged
  leftovers by T3 afterwards. Recursing the sweep into retained children's
  stores is explicitly deferred — if deep coordinator trees become a real
  workload, revisit. (rev-2 finding C7.)
- **Crash-residue TTL delay (accepted):** a `runtime_lost` delegate's
  `EndedAt` is re-minted at reconcile time, so after a crash+resume its lanes
  become collectible only `laneIdleTTL` after the resume, not immediately.
  Conservative by design. (rev-2 finding C10.)
- Changing the lock decision core (`worktree.Decide`) or the non-force-remove
  safety ladder.

## Design

### D0. Disposal predicates (reused, not reinvented)

A lane is **collectible** iff its tree is clean (`worktree.CleanTree`) and
either existing predicate holds:

- `worktree.Unchanged(run, lane, sc.BaseSHA)` — tip == recorded base (covers
  detached-HEAD-created lanes and rebased/deleted targets), or
- `disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)` reports disposable —
  the shared two-arm merged test (`worktree.Merged`: ancestry **or**
  cherry/patch-equivalence, with remote-tracking-tip resolution), so squash-
  and rebase-merged lanes are collected too.

Branch deletion of a collectible lane is safe under either arm: unchanged →
no work exists; merged → the work is reachable from the merge target. The
close path's "unchanged lane: tip == base, no work lost" rationale is
generalized accordingly where the mechanics are factored out.

### D1. Mid-life lane sweep (owning session, in-process)

Add `Session.sweepOwnDelegateLanes()`: enumerate this session's isolation
lanes (as `ownedIsolationLanes` does) and dispose a lane iff all conditions
hold. Conditions (a), (b), (d), (f) and the retained-child liveness checks
are evaluated together **under `s.mu`** as part of step 1 (reserve); git-based
conditions (c), (e) run after reservation, outside the mutex.

- **(a) quiescent, latest record:** the delegate's **latest** job record /
  folded state is `Status.IsTerminal()`, there is no running/queued follow-up
  job, **and** — because drive/steer turns mint no job record — if the child
  session is retained, its in-memory `sub.running` and `sub.driving` flags are
  both false. (rev-2 findings C1/D2: record-terminal ≠ idle.)
- **(b) idle ≥ TTL:** at least `laneIdleTTL` (**30 minutes**) since the
  delegate's **last activity**: the max of the latest job's terminal
  timestamp (`EndedAt` / event `TS`) and, for a retained child, an in-memory
  last-activity timestamp updated whenever a drive turn or delivery touches
  the child (new field, set by the drive/steer/watch-delivery paths).
- **(c) collectible** per D0.
- **(d) no live work, liveness-aware:** no *running* jobs rooted under the
  lane. NOTE: `liveWorkUnder` as it exists reports every retained child whose
  env is rooted at the path, running or not — using it verbatim would make the
  retained-child case unsweepable (rev-2 findings C2/D1). The sweep uses a
  running-aware variant: tracked-but-quiescent children rooted at the lane are
  exactly what step 2 evicts; running jobs/turns block.
- **(e) our lock:** lock state classifies as this session's own `serf:dlg:`
  marker (or unlocked crash residue) per `worktree.ClassifyReason` /
  `worktree.Decide`; foreign and session-switched-in locks are skipped.
- **(f) not watched, result consumed:** no armed watch routes `send_to` this
  delegate (a later watch firing must not be destroyed — rev-2 finding D4),
  and, for a retained child, the retention manager's unconsumed-result
  invariant is respected: an unconsumed terminal result blocks eviction, same
  as `reserveSlot` refuses (rev-2 finding D6).

**Disposal steps, in order:**

1. **Reserve** (under `s.mu`, no git calls under it): verify (a), (b), (d),
   (f); check the delegate id is not already reserved; add it to the
   in-memory `disposingDelegates` set. See D4 for the full reservation
   protocol — the set, not the mutex, is what excludes concurrent revival.
2. **Evict the retained child**, if any: close it (quiescent by (a)/(f)),
   remove it from the subagent table, `DisposeSandboxScratch` for an owned
   env — the same cleanup the parent close path performs before disposal.
3. **Unlock → `git worktree remove` (non-force)**. A refusal (late dirty
   write) re-locks the lane with our marker, clears the reservation, keeps.
4. **Append `EventDelegateDisposed`** (durable mark; store is open mid-life).
5. **`git branch -D`**, delete sidecar, clear the reservation.

Steps 3–5 are the existing unchanged-lane mechanics factored out of
`disposeOneDelegateLane`; the close path is re-expressed on the factored
helper with identical behavior.

**Sandboxed sessions:** the sweep's repo-root git operations build their
control env exactly as delegate revival does (the
`useDelegateWorktreeControlPolicy` dance); if a confined control env cannot
be built, the lane is silently skipped this round. (rev-2 finding C9.)

### D2. Triggers

All triggers: local execution environments only, **top-level sessions only**
(`isSubagentSession()` guards; delegate children run the same lifecycle code
but must not start tickers or sweeps).

- **T1 — periodic:** a per-(top-level)-session housekeeping goroutine,
  `s.clock.NewTicker` every **10 minutes** (fake-clock testable), calling
  `sweepOwnDelegateLanes()`. The **first tick doubles as the open/resume
  sweep** — there is deliberately no immediate-at-open sweep, so lazy
  delegate reconstruction, restored pending watch-sends, and other open-time
  revival machinery settle first (rev-2 finding D5; the 10-minute delay is
  the cheap, race-free ordering). **Quiesce ordering:** the close path
  signals and **joins** the goroutine at the *very top* of close — before
  `drainForClose` and the subagent-close loop, not merely before
  `disposeDelegateLanesAtClose` — so an in-flight sweep's eviction (step 2)
  never races the close path's own subagent teardown or scratch disposal
  (rev-2 finding D3), and no disposed mark can land after `closeStoreOnly`.
- **T3 — session open, cross-session lane residue:** one pass (also deferred
  to the first tick, same rationale) over **delegate lanes only** (sidecar
  records `DelegateID` provenance) that are **unlocked** and collectible per
  D0, i.e. lanes a prior session's close KEPT whose branches have since
  merged. Deviations from the D1 mechanics, all load-bearing:
  - **No disposed mark.** The lane's job record lives in the *owner*
    session's per-session jobstore, which this session cannot append to —
    step 4 is skipped and revival protection falls to the existing
    WorkingDir-stat crash net (`working directory no longer exists` — a
    vaguer but accurate refusal, accepted). (rev-2 findings C5/D7.)
  - **Never re-lock on failure.** The close-path dirty-refusal handler
    re-locks with the disposer's own marker; applied to a foreign unlocked
    lane that would wedge it forever (skipped by every future sweep and by
    manual prune). T3's failure path restores the prior **unlocked** state
    and skips. (rev-2 finding C6.)
  - **Grace window.** Lanes whose sidecar/directory mtime is within
    `laneIdleTTL` are skipped, so a just-KEPT or about-to-be-revived lane
    (owner resumed, revival re-lock not yet taken) isn't snatched the moment
    its branch merges. (rev-2 finding C8.)
  - **Skip-and-continue per lane** — a `git worktree remove` / `branch -D`
    refusal (e.g. branch checked out elsewhere) skips that lane; the sweep
    never aborts. Cross-process races resolve per-lane the same way. Results
    emitted as a single info/warning event, not injected into the transcript.

T1 handles the live-long-session case (the observed 21-lane pileup); T3
handles the closed-session "kept for prune" case.

### D3. Failure posture

Every step best-effort, never `--force`, fail-safe toward preservation. A
sweep error on one lane clears that lane's reservation, skips it, continues;
sweep-level errors are silent (T1) or a single warning event (T3). Sweeps run
off the turn path and never hold `s.mu` across a git subprocess; the only
cross-component coupling is the reservation protocol (D4).

### D4. Reservation protocol + admission hardening

The `disposingDelegates` set is a symmetric mutual-exclusion protocol, not a
one-way check (rev-2 finding C3):

- **Sweep side:** acquired in D1 step 1 under `s.mu`; released when disposal
  completes, aborts, or the lane is kept.
- **Revival side:** **every** path that revives or restores a delegate child —
  `delegate_send` admission (both the retained-child and restore paths),
  lazy reconstruction at/after session open, and pending watch-send delivery —
  **acquires the reservation at admission under `s.mu` and holds it until the
  revived child / new job record is visible** to the sweep's step-1 re-verify
  (i.e. tracked in the subagent table or a running job record exists). A
  point-in-time check is insufficient: `restoreTerminalDelegateChild` runs
  long, mints no record until late, and the child isn't tracked until
  `trackIfAbsent`.
- **Refusal semantics:** an acquisition attempt against a *reserved* delegate
  is **busy/retryable** — watch-send delivery in particular must classify it
  like `watchSendBusy` (retry at the next boundary), never a permanent
  `dropWatchSend` (rev-2 finding D4: a reservation that later aborts must not
  have destroyed a guaranteed watch delivery). Only `rec.Disposed` is the
  permanent refusal.

Two `delegate_send` admission fixes in `agent/job_delegate.go`:

1. **Check `rec.Disposed` on the retained-child path too.** Today
   `assessDelegateResumability` (the only `Disposed` check) runs only when
   the child is not retained; mid-life disposal makes the retained path
   reachable-after-disposal, so the disposed refusal must be unconditional.
2. **Acquire the reservation** per the protocol above; a reserved delegate
   gets a busy/retryable refusal whose text tells a model caller it can retry
   or start a new delegate.

## Constants

| name | value | why |
|---|---|---|
| `laneIdleTTL` | 30m | long enough for follow-up `delegate_send`; also T3's mtime grace |
| `laneSweepInterval` | 10m | bounded staleness ≤ TTL+interval; first tick doubles as open sweep |

## Testing (TDD, red → green per case)

Unit (fake clock + real-git fixtures via the existing worktree test harness):

1. Terminal + merged (ancestry) + idle ≥ TTL → removed, `EventDelegateDisposed`
   appended, branch and sidecar gone.
2. Same but **squash-merged** (cherry arm) → removed.
3. Unchanged lane with empty/deleted merge_target → removed (Unchanged arm).
4. Terminal + merged but idle < TTL → kept, still locked.
5. Resumed delegate: first job terminal 40m ago, latest job terminal 5m ago →
   kept (latest-record TTL).
6. Retained child with `sub.driving` (or `sub.running`) true and all job
   records terminal → kept; drive activity refreshes the idle basis (b).
7. Terminal + unmerged commits → kept forever by the sweep.
8. Non-terminal delegate / running follow-up job → skipped.
9. Retained quiescent child: sweep evicts and closes it (sandbox scratch
   disposed) before removal; a `delegate_send` during disposal gets the busy
   refusal; after, the disposed refusal — on the retained path specifically.
10. Retained child with an **unconsumed terminal result** → kept (D1(f)).
11. Armed watch with `send_to=` the delegate → kept; a watch firing against a
    *reserved* delegate is retried, not dropped; against a *disposed* one it
    surfaces the disposed refusal.
12. Reservation held by a revival path (mid-restore, no record yet, untracked
    child) → sweep skips; sweep holding it → revival gets busy.
13. Foreign / session lock marker → skipped.
14. Dirty-race on remove (own lane) → re-locked, reservation cleared, kept.
15. T1 quiesce: tick in flight when close starts → close joins the sweeper
    before `drainForClose`; no double child-close/scratch-dispose; no
    disposed mark after store close.
16. First tick after resume collects lanes merged while the session was down;
    no sweep runs at open before the first tick.
17. T3 collects an unlocked merged delegate lane KEPT by a prior session's
    close; skips a managed (non-delegate) worktree; skips a lane with mtime
    within grace; a remove/branch refusal leaves the lane **unlocked** and
    the sweep continues; no disposed mark is attempted; a later resume of the
    owner gets the WorkingDir-stat refusal.
18. Child (delegate) sessions start no ticker and run no sweeps.
19. Sweep disabled for non-local exec envs; sandboxed session without a
    buildable control env skips lanes silently.

E2E: long-lived session spawns an isolated delegate, its branch is merged
externally, fake clock advances past TTL+interval → lane, branch, sidecar
gone; the retained child was evicted; a subsequent `delegate_send` returns
the disposed-lane error.

## Files touched (est.)

- `agent/session_worktree_close.go` — factor disposal mechanics (~40 loc moved)
- `agent/session_worktree_sweep.go` (new) — sweep, reservation, ticker, T3 (~300 loc)
- `agent/session_lifecycle.go` — start/join ticker (top of close), guards (~30 loc)
- `agent/job_delegate.go` — D4 admission + reservation acquisition (~50 loc)
- `agent/job_watch.go` — busy-vs-drop classification for reserved delegates (~15 loc)
- `agent/subagent_manager.go` — quiescence/last-activity accessors (~20 loc)
- tests (~800 loc)

## Review log

**Rev 1 → rev 2** (reviewers A, B): reservation set replacing unimplementable
mutex serialization (A1/B4); retained-child eviction + unconditional Disposed
check (A2/B1); predicate reuse `Unchanged` OR `disposableReason` (A3/B2/B3);
T3 rescoped to delegate lanes, skip-and-continue, top-level only, managed
worktrees excluded (A4/A5/B5/B6/B7); ticker/close quiesce (A6); latest-record
TTL (A7/B8); naming + scope fixes (A8/A9/B9).

**Rev 2 → rev 3** (reviewers C, D):

- **C1/D2 (critical/major):** drive/steer turns mint no job record, so
  "record-terminal + EndedAt idle" could evict a child mid-turn → (a) gates
  on `sub.running`/`sub.driving`; (b) idle basis includes in-memory last
  activity.
- **C2/D1 (major/critical):** `liveWorkUnder` verbatim reports retained
  quiescent children, making the retained-child case unsweepable → (d) is a
  running-aware variant.
- **C3 (major):** reservation was a one-way check with no acquisition/release
  protocol on the revival side → D4 symmetric protocol, held until the
  revived child/record is sweep-visible.
- **C4/D4/D5 (major):** watch-send delivery and lazy reconstruction are
  revival paths outside `delegate_send`; reserved-refusal would permanently
  drop guaranteed watch sends; open-time sweeps race reconstruction →
  revival-side acquisition covers all paths; busy-vs-disposed refusal split;
  first-tick-only sweeps (no at-open sweep); condition (f) keeps watched
  delegates.
- **C5/D7 (major/minor):** T3 cannot append the disposed mark to a foreign
  per-session store → T3 skips the mark, stat crash net covers revival;
  documented.
- **C6 (major):** dirty-refusal re-lock would wedge foreign lanes under the
  sweeper's marker → T3 failure path restores unlocked, never re-locks.
- **D3 (major):** quiesce point moved to the top of close, before
  `drainForClose`.
- **D6 (major):** eviction respects the retention manager's
  unconsumed-result invariant → condition (f).
- **C7 (major):** mid-tree coordinator lanes uncollectable → accepted
  limitation, documented with the eventual-collection path.
- **C8/C9/C10 (minor):** T3 mtime grace window; sandbox control-env dance for
  sweep git ops; reconcile-minted `EndedAt` delay accepted and documented.
