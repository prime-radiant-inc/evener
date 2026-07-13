# Delegate-lane disposal: dispose operation + nudge + automatic residue collection

**Status:** draft spec, rev 4 (restructured into phases after Jesse's
completion-nudge proposal; rev 1–3 adversarial-review history in §Review log)
**Problem owner:** Jesse
**Date:** 2026-07-13

## Problem

Isolation delegate worktree lanes (`dlg_*`) are only disposed in the parent
session's close path (`disposeDelegateLanesAtClose`, native worktree tools
spec §9 step 4). Merged-but-committed lanes are additionally only collected
by the `prune` operation of the `manage_worktree` tool — and a lane locked
with the live session's own `serf:dlg:` marker is skipped even by that, so a
live session has **no way at all** to dispose a finished delegate's lane, even
if the model wants to.

Observed failure mode (2026-07-13, this repo): one long-lived resumed session
(`01KX4DMT…`, alive since 2026-07-09) accumulated **21 locked delegate lanes**
whose branches were all fully merged into `main`. Nothing would ever collect
them: the session never closes, and the model — despite being shown each
lane's report on every delegate completion — never invoked prune (which would
have skipped the locked lanes anyway).

Three gaps:

1. **No disposal primitive for a live session.** Not even the model can clean
   up a finished delegate's lane mid-session.
2. **No prompt to do so.** The completion report shows the lane but suggests
   nothing.
3. **No collection of closed-session residue.** Close-time disposal KEEPs
   changed lanes "for prune", but prune is model-invoked only.

## Goal, phased

- **Phase 1 (this spec's build scope):** give the session a synchronous
  `dispose` primitive (P1), nudge the model toward it at delegate completion
  (P2), and collect closed-session residue automatically (P3). P1+P2 make
  prompt cleanup possible and likely; P3 guarantees eventual cleanup with
  zero model dependence for everything a closed session leaves behind.
- **Phase 2 (specified, deferred):** a background mid-life sweep in the
  owning session (the rev-3 design) as a backstop iff phase 1 measurably
  leaks in live long-running sessions. Not built until the leak is observed.

The phase-1/phase-2 split is deliberate: model-invoked disposal runs
synchronously in-turn, so it needs none of the async machinery (reservation
protocol, eviction-vs-drive races, ticker quiescence) that made rev 3 large.
The nudge cannot *bound* the leak — the observed session ignored richer
signals — which is why P3 is unconditional and phase 2 stays specified.

## Non-goals / accepted limitations

- Automatic collection of **managed (user/session) worktrees**. Only delegate
  lanes are auto-collected; managed worktrees remain the province of the
  explicit `manage_worktree` `prune` operation.
- Reclaiming lanes locked by *foreign* sessions. Cross-process liveness
  detection is out; manual prune territory.
- **Live-session pileup is bounded, not eliminated, in phase 1.** A model
  that ignores the nudge accumulates lanes until its session closes; they are
  then collected by close-time disposal (unchanged) or P3 (merged). If that
  window proves painful, phase 2 exists.
- Changing the lock decision core (`worktree.Decide`) or the non-force-remove
  safety ladder.

## Phase 1 design

### D0. Disposal predicates (shared by P1 result-reporting and P3)

A lane is **collectible** iff its tree is clean (`worktree.CleanTree`) and
either existing predicate holds:

- `worktree.Unchanged(run, lane, sc.BaseSHA)` — tip == recorded base, or
- `disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)` reports disposable —
  the shared two-arm merged test (`worktree.Merged`: ancestry **or**
  cherry/patch-equivalence, with remote-tracking-tip resolution), so squash-
  and rebase-merged lanes count.

Branch deletion of a collectible lane is safe under either arm: unchanged →
no work exists; merged → reachable from the merge target.

### P1. `dispose` operation on `manage_worktree` (new)

`manage_worktree` gains a `dispose` operation taking a delegate id (`dlg_*`).
Synchronous, in-turn, on the owning session. Steps:

1. **Validate ownership and quiescence** (under `s.mu`): the delegate's
   latest job record is terminal with no running/queued follow-up; if the
   child session is retained, `sub.running` and `sub.driving` are both false
   (drive/steer turns mint no job record — rev-3 lesson) and its terminal
   result has been consumed (the retention manager's invariant). A running or
   driving delegate → clear refusal naming the state.
2. **Refuse foreign lanes:** lock state must classify as this session's own
   `serf:dlg:` marker (or unlocked crash residue) per `worktree.ClassifyReason`
   / `worktree.Decide`.
3. **Evaluate the lane** and report it in the refusal/result either way:
   - collectible per D0 → proceed;
   - **changed/unmerged → refuse by default** ("N commits ahead, dirty=X;
     pass `force_dirty` to discard"), reusing the existing force/force_dirty
     convention so deliberate discard is possible but never accidental.
4. **Evict the retained child** if present: close it (quiescent by step 1),
   remove from the subagent table, `DisposeSandboxScratch` for an owned env.
5. **Unlock → `git worktree remove`** (non-force unless `force_dirty`) →
   **append `EventDelegateDisposed`** → **`git branch -D`** → delete sidecar.
   Steps factored from `disposeOneDelegateLane` so close-time disposal and P1
   share one implementation.

Post-disposal `delegate_send` gets the existing disposed refusal — **fixed to
be unconditional**: today `assessDelegateResumability` (the only
`rec.Disposed` check) runs only when the child is *not* retained
(`job_delegate.go:682,841`); since P1 evicts, the not-retained path is the one
taken afterwards, but the check is made unconditional anyway (cheap, closes
the class).

**Watch guard:** if an armed watch routes `send_to` this delegate, `dispose`
refuses and names the watch — disposing would destroy a guaranteed future
delivery (rev-3 lesson). The model can retarget or remove the watch first.

**Concurrency posture:** none needed beyond step 1's under-mutex validation.
The operation runs in the session's own turn; `delegate_send` and `dispose`
are both model-sequential within the turn, and background watch deliveries to
this delegate are excluded by the watch guard. (This is the machinery savings
over phase 2.)

**Sandboxed sessions:** repo-root git ops build their control env exactly as
delegate revival does (`useDelegateWorktreeControlPolicy`); if unsatisfiable,
`dispose` fails with a clear error.

### P2. Completion nudge

The terminal lane report a finished isolated delegate already carries
(`delegateWorktreeReport`: path/branch/ahead/dirty — inline tool result AND
background `job_finished` notification) gains one conditional sentence:

- lane collectible per D0 (evaluated at render time, best-effort; on git
  error, omit):
  `This delegate's worktree is unchanged-or-merged; if you're done with it,
  dispose it: manage_worktree op=dispose id=<dlg_…>.`
- lane has unmerged work: no nudge (disposal would be force-only; wrong thing
  to suggest).

Wording notes (compact_context lesson: bare imperative verbs mis-bind on some
providers): the nudge is conditional ("if you're done"), names the exact tool
call, and never uses a bare "clean up". Copy to be validated with the
existing multi-provider ergonomics-eval harness (kimi / gpt-5.x / claude)
before landing; the eval asserts (a) disposal happens after a merged
delegate completes, (b) no disposal of delegates the scenario resumes later,
(c) zero scolds/confusion.

### P3. Automatic residue collection (no model in the loop)

One pass per top-level session (`isSubagentSession()` guard), local exec envs
only, run **once, delayed** (a one-shot `s.clock` timer, `laneSweepDelay` =
10 minutes after open) so open-time revival machinery — lazy delegate
reconstruction, restored pending watch-sends — settles first. Scope:
**delegate lanes only** (sidecar records `DelegateID` provenance) that are
**unlocked** and collectible per D0 — i.e. lanes a prior session's close KEPT
whose branches have since merged, and unlocked crash residue. Mechanics
deviate from P1 where the lane is foreign, all load-bearing (rev-3 findings):

- **No disposed mark.** The job record lives in the owner session's
  per-session jobstore, which this session cannot append to; revival
  protection falls to the existing WorkingDir-stat crash net (vaguer but
  accurate refusal — accepted).
- **Never re-lock on failure.** The close-path dirty-refusal handler re-locks
  with the disposer's own marker; on a foreign lane that would wedge it
  forever (locked lanes are skipped by every sweep and by manual prune). P3's
  failure path restores the prior unlocked state and skips.
- **Grace window:** lanes whose sidecar/directory mtime is within
  `laneGrace` (30m) are skipped — a just-KEPT or about-to-be-revived lane
  (owner resumed; revival re-lock not yet taken) isn't snatched the moment
  its branch merges.
- **Skip-and-continue per lane;** a `worktree remove`/`branch -D` refusal
  (branch checked out elsewhere, cross-process race — git refuses the loser)
  skips that lane. Results emitted as one info/warning event, not injected
  into the transcript.
- Sandbox control-env handling as in P1; unsatisfiable → skip all, silently.

Close-time disposal (`disposeDelegateLanesAtClose`) is unchanged. Between it
and P3, every lane of a *closed* session is eventually collected with no
model involvement: unchanged lanes at close, merged lanes at the next
session's P3 pass.

## Phase 2 (deferred backstop): mid-life background sweep

The rev-3 design — 10-minute per-session ticker sweeping the session's own
locked lanes with a 30m activity-based TTL, symmetric per-delegate
reservation protocol covering all revival paths (delegate_send, lazy
reconstruction, watch-send delivery), quiescence-aware eviction, close-path
join-before-drainForClose — is preserved in git history at rev 3 of this file
(commit `75c7b086`) and is **not built now**. Trigger to build it: telemetry
or observed practice showing live long-running sessions still accumulating
collectible lanes past an agreed threshold despite P2. Much of P1 (factored
disposal mechanics, unconditional Disposed check, watch guard, quiescence
validation) is the same code phase 2 would reuse; the delta is the async
safety machinery.

## Constants

| name | value | why |
|---|---|---|
| `laneSweepDelay` | 10m | one-shot P3 delay past open-time revival races |
| `laneGrace` | 30m | mtime grace so P3 never snatches a fresh/kept lane |

## Testing (TDD, red → green per case)

P1 unit (real-git fixtures via the existing worktree test harness):

1. Terminal + merged (ancestry) delegate → `dispose` removes lane, appends
   `EventDelegateDisposed`, deletes branch + sidecar.
2. Squash-merged (cherry arm) → disposed.
3. Unchanged lane, empty/deleted merge_target → disposed (Unchanged arm).
4. Unmerged commits / dirty tree → refused with ahead/dirty report;
   `force_dirty` → disposed.
5. Running or driving delegate (`sub.running` / `sub.driving`) → refused.
6. Retained child with unconsumed terminal result → refused.
7. Retained quiescent child → evicted (scratch disposed) then removed;
   subsequent `delegate_send` gets the disposed refusal **on the retained
   path** (regression for the unconditional-check fix).
8. Armed watch `send_to=` the delegate → refused, watch named; after watch
   removal → disposed.
9. Foreign / session lock marker → refused.
10. Non-delegate id / unknown id → clear invalid_request.
11. Sandboxed session: control-env dance used; unsatisfiable → clear error.

P2: render-level unit tests — nudge present iff collectible (both arms),
absent for unmerged lanes, exact copy pinned; multi-provider live ergonomics
eval as described (gates landing, like the F1–F4 worktree ergonomics evals).

P3 unit (fake clock):

12. Unlocked merged lane KEPT by a prior session's close → collected at the
    one-shot timer; nothing runs before the delay.
13. Managed (non-delegate) worktree, unlocked+merged → untouched.
14. Lane mtime within grace → skipped.
15. remove/branch refusal → lane left **unlocked**, sweep continues; no
    disposed-mark attempt; owner's later resume gets the WorkingDir-stat
    refusal.
16. Locked lanes (any marker) → skipped.
17. Child sessions / non-local envs → no P3.

E2E: session A spawns an isolated delegate whose branch merges; model is
shown the nudge and disposes (P2→P1, live eval); separately session A closes
keeping a changed lane, the lane's branch is then merged, session B opens →
after the delay the lane, branch, and sidecar are gone (P3), and resuming A's
delegate reports the working-directory-missing refusal.

## Files touched (est.)

- `agent/session_tools_worktree.go` — `dispose` op wiring + validation (~120 loc)
- `agent/session_worktree_close.go` — factor disposal mechanics for reuse (~40 loc moved)
- `agent/session_worktree_sweep.go` (new) — P3 one-shot pass (~120 loc)
- `agent/session_lifecycle.go` — P3 timer start/cancel (~15 loc)
- `agent/job_delegate.go` — unconditional Disposed check (~10 loc)
- `agent/job_notify.go` / lane-report rendering — P2 nudge (~25 loc)
- tests (~600 loc) + provider ergonomics eval scenarios

## Review log

**Rev 1 → rev 2** (adversarial reviewers A, B): reservation set replacing
unimplementable mutex serialization (A1/B4); retained-child eviction +
unconditional Disposed check (A2/B1); predicate reuse `Unchanged` OR
`disposableReason` (A3/B2/B3); T3 rescoped to delegate lanes,
skip-and-continue, top-level only (A4/A5/B5/B6/B7); ticker/close quiesce
(A6); latest-record TTL (A7/B8); naming + scope (A8/A9/B9).

**Rev 2 → rev 3** (reviewers C, D): drive/steer turns mint no job record →
quiescence gates on `sub.running`/`sub.driving` (C1/D2); `liveWorkUnder`
verbatim made retained-child lanes unsweepable (C2/D1); reservation protocol
made symmetric across all revival paths (C3, C4/D4/D5); T3 cannot mark a
foreign store — skip mark, stat net covers (C5/D7); never re-lock foreign
lanes on failure (C6); quiesce before `drainForClose` (D3); unconsumed-result
invariant respected (D6); mid-tree coordinator gap accepted (C7); grace
window, sandbox control env, reconcile-minted EndedAt accepted (C8/C9/C10).

**Rev 3 → rev 4** (Jesse): restructured into phases around a synchronous
model-invoked `dispose` + completion nudge. Rationale: in-turn disposal
eliminates the async machinery (reservation protocol, eviction-vs-drive
races, ticker quiescence) that dominated rev 3; the nudge makes prompt
cleanup likely; P3 alone already guarantees model-independent collection of
everything a closed session leaves. Rev-3 sweep preserved as specified
phase-2 backstop with an explicit build trigger. All rev-2/rev-3 safety
findings carried into P1/P3 where applicable (quiescence gates, watch guard,
unconsumed-result invariant, foreign-store mark impossibility, no-relock,
grace window, sandbox control env).
