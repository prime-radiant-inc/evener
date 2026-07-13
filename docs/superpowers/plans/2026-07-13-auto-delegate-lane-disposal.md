# Delegate-lane disposal: dispose operation + nudge + automatic residue collection

**Status:** draft spec, rev 5 (three adversarial review rounds, 3×2 competing
reviewers; history in §Review log)
**Problem owner:** Jesse
**Date:** 2026-07-13

## Problem

Isolation delegate worktree lanes (`dlg_*`) are only disposed in the parent
session's close path (`disposeDelegateLanesAtClose`, native worktree tools
spec §9 step 4). Merged-but-committed lanes are additionally only collected
by the `prune` operation of the `manage_worktree` tool — and a lane locked
with the live session's own `serf:dlg:` marker is skipped even by that.

Three observed failures (2026-07-13):

1. One long-lived resumed session (`01KX4DMT…`, alive since 2026-07-09)
   accumulated **21 locked delegate lanes**, all fully merged into `main`.
   Nothing could ever collect them.
2. A model that *actively tried* to clean up (another session's log: `remove`
   on a clean, merged worktree) was **refused**, because `liveWorkUnder`
   counts a retained *terminal* delegate child as a live handle
   (`session_tools_worktree.go:647-667` — no running/driving check). Cleanup
   isn't just un-nudged: the existing path is blocked in the common case.
3. Closed sessions' KEPT lanes sit until someone happens to run prune.

So: no disposal primitive a live session can use, no prompt to use one, no
model-independent collection.

## Goal, phased

- **Phase 1 (build scope):** a synchronous `dispose` primitive (P1), a
  completion-time nudge toward it (P2), and automatic collection of
  closed-session residue (P3). P1+P2 make prompt cleanup possible and likely;
  P3 guarantees eventual, model-independent collection of every
  closed-session lane **once its branch merges** (a changed-but-never-merged
  lane is preserved forever by design — work preservation outranks tidiness).
- **Phase 2 (specified, deferred):** the rev-3 background mid-life sweep as a
  backstop iff phase 1 measurably leaks in live long-running sessions
  (preserved at commit `75c7b086`; build trigger: observed pileup despite P2).

The split is deliberate: model-invoked disposal runs in-turn and needs only a
narrow stop-gate (P1 step 4) instead of rev 3's full async machinery
(reservation protocol covering every revival path, ticker quiescence,
close-join ordering).

## Non-goals / accepted limitations

- Automatic collection of **managed (user/session) worktrees** — `prune`
  territory, unchanged.
- Reclaiming lanes locked by foreign sessions; cross-process liveness
  detection is out.
- **Live-session pileup is bounded, not eliminated, in phase 1.** A model
  that ignores the nudge accumulates lanes until close; close-time disposal
  (unchanged lanes) and P3 (merged lanes) then collect them.
- Changed-and-never-merged lanes of dead sessions persist until a human or
  model deliberately forces them out. Correct: they hold unlanded work.
- Changing the lock decision core (`worktree.Decide`) or the non-force-remove
  safety ladder.

## Phase 1 design

### D0. Disposal predicates (shared by P1, P2 gating, P3)

A lane is **collectible** iff its tree is clean (`worktree.CleanTree`) and
either existing predicate holds:

- `worktree.Unchanged(run, lane, sc.BaseSHA)` — tip == recorded base, or
- `disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)` disposable — the
  shared two-arm merged test (`worktree.Merged`: ancestry **or**
  cherry/patch-equivalence, with remote-tracking-tip resolution).

Branch deletion of a collectible lane is safe under either arm: unchanged →
no work exists; merged → reachable from the merge target.

### P1. `dispose` operation on `manage_worktree` (new)

`manage_worktree` gains a `dispose` op taking a delegate id (`dlg_*`).
Synchronous, in-turn, on the owning session (the tool is root-only —
`rootOnlyWorktreeTools` — so only top-level sessions can invoke it; see P2
for the nudge implication). Steps:

1. **Validate ownership + record quiescence** (under `s.mu`): this session
   owns the delegate (its jobstore holds the record with
   `ParentSessionID == s.id`); latest job record terminal; no running/queued
   follow-up job.
2. **Validate delivery quiescence:** refuse if any **armed watch** routes
   `send_to` this delegate **or any pending (fired-but-undelivered)
   watch-send** targets it — scanning `cfg.pending` / durable
   `EventWatchSendPending` state in this session's job manager **and all
   retained children's job managers** (pending sends survive watch clearing
   and session resume; they are guaranteed deliveries — destroying one is the
   rev-3 lesson). Refusal names the watch/pending delivery.
3. **Validate subtree quiescence:** if the child session is retained, refuse
   unless the child's whole subtree is quiescent — `sub.running` and
   `sub.driving` false, no live grandchild jobs/delegates, no undelivered
   attention (the same liveness `DrainJobTree` waits on; reuse its check, not
   a new one). "Latest record terminal" alone says nothing about the child's
   own children.
4. **Stop-gate the child** (under `sub.mu`): re-verify not running/driving
   and set the child's stop-gate/closing flag so a **wake-edge drive cannot
   launch afterwards** — `SetNotifyFunc` fires from child-side goroutines at
   arbitrary moments (`subagents.go:725`), so a check without a gate is
   check-then-act. This one flag is the entirety of P1's concurrency
   machinery.
5. **Refuse foreign lanes:** lock state must classify as this session's own
   `serf:dlg:` marker (or unlocked crash residue) per
   `worktree.ClassifyReason` / `worktree.Decide`.
6. **Evaluate the lane** and report state either way:
   - collectible per D0 → proceed;
   - **unmerged commits → refuse; `force` overrides** (merge/provenance gate);
   - **dirty tree → refuse; `force_dirty` overrides** (dirty gate only).
   The two flags stay orthogonal, exactly matching the existing `remove` op's
   convention (`force` never implies `force_dirty` and vice versa).
7. **Evict the retained child** if present: close it, remove from the
   subagent table, `DisposeSandboxScratch` for an owned env. (The
   unconsumed-result gate rev 4 carried is dropped: delegates set
   `resultConsumed` at durable finish (`job_delegate.go:2222-2226`), so the
   state is unreachable for dispose's targets; the failed-durable-append
   window is already covered by step 1's record checks.)
8. **Unlock → `git worktree remove`** (non-force unless the dirty gate was
   forced) → **append `EventDelegateDisposed`** → **`git branch -D`** →
   delete sidecar. Factored from `disposeOneDelegateLane` so close-time
   disposal and P1 share one implementation.

Post-disposal `delegate_send` gets the disposed refusal — made
**unconditional** (today `assessDelegateResumability`, the only
`rec.Disposed` check, runs only on the not-retained path;
`job_delegate.go:682,841`) — and its copy generalized: the current text
hardcodes "disposed at session close" (`job_delegate.go:768`), false after P1.

**`remove` refusal legibility (companion fix):** when `remove` is blocked
solely by a retained *terminal* delegate child (observed in the field), the
refusal names the delegate and suggests `dispose`. `liveWorkUnder`'s
semantics are not changed (that would ripple through prune's safety posture).

**Sandboxed sessions:** repo-root git ops build their control env exactly as
delegate revival does (`useDelegateWorktreeControlPolicy`); unsatisfiable →
clear error.

### P2. Completion nudge

The terminal lane report a finished isolated delegate already carries
(`delegateWorktreeReport` — inline tool result AND background `job_finished`
notification) gains one conditional sentence, rendered **only in sessions
that have `manage_worktree`** (root-only — a mid-tree session must not be
nudged toward a tool it lacks):

- lane collectible per D0 (evaluated at render time, best-effort — the
  report path already runs git via a control env; on error, omit):
  `This delegate's worktree is unchanged-or-merged; if you're done with it,
  dispose it: manage_worktree op=dispose id=<dlg_…>.`
- lane has unmerged work or session lacks the tool: no nudge.

Wording notes (compact_context lesson): conditional phrasing, exact tool
call, no bare imperative "clean up". Copy validated with the existing
multi-provider live ergonomics-eval harness (as F1–F4 were) before landing:
(a) disposal happens after a merged delegate completes, (b) no disposal of
delegates the scenario resumes later, (c) zero scolds/confusion.

### P3. Automatic residue collection (no model in the loop)

One pass per top-level session (`isSubagentSession()` guard), local exec envs
only, one-shot `s.clock` timer `laneSweepDelay` (10m) after open so open-time
revival machinery (lazy reconstruction, restored pending watch-sends)
settles; the timer is cancelled if close begins first. Scope: **delegate
lanes only** (sidecar `DelegateID` provenance) that are **unlocked** and
collectible per D0.

**Implementation is a parameterized reuse of `worktreePruneSweep1`** — the
sweep already does the identical chain (unlocked gate, `liveWorkUnder`,
sidecar, `CleanTree`, `disposableReason`, remove → `branch -D` → sidecar
delete). P3 = sweep1 with: delegate-lanes-only filter, `Unchanged` OR'd into
the predicate, skip-and-continue error posture (sweep1's abort-on-first-error
is wrong for an automated pass), no disposed-mark attempt, never-re-lock on
failure, and the grace check. Not a second implementation. (rev-4 finding E7.)

- **No disposed mark:** the job record lives in the owner's per-session
  jobstore; revival protection falls to the WorkingDir-stat crash net
  (vaguer but accurate refusal — accepted).
- **Never re-lock on failure:** restore the prior unlocked state and skip.
- **Grace:** skip lanes whose sidecar/directory mtime is within `laneGrace`
  (30m).
- **Unlocked ⇒ owner-closed is made an invariant, not an assumption**
  (rev-4 findings E6/F5: a *resumed* owner's un-revived lanes previously sat
  unlocked indefinitely, exposed to P3 the moment they merged):
  **session resume re-locks the session's own undisposed delegate lanes** —
  enumerate own descriptors (as `ownedIsolationLanes` does), re-take the
  `serf:dlg:` marker on lanes that still exist, best-effort, alongside the
  existing managed-worktree resume re-lock. Symmetrically, the **close-path
  late-dirty downgrade no longer re-locks**: it unlocks-and-keeps, matching
  the changed-lane KEEP path — the old re-lock left a dead session's lane
  locked forever, invisible to P3 and prune alike (rev-4 finding E8). After
  these two changes, "unlocked" really does mean "no live owner", and the
  30m grace only has to cover the close-in-progress window it was designed
  for.
- Skip-and-continue per lane; cross-process races resolve per-lane (git
  refuses the loser). One info/warning event, not injected into the
  transcript. Sandbox control-env as in P1; unsatisfiable → skip silently.

Close-time disposal is otherwise unchanged. Between close-time disposal and
P3: every closed-session lane is collected once unchanged (at close) or once
its branch merges (next P3 pass); changed-never-merged lanes persist by
design.

## Phase 2 (deferred backstop): mid-life background sweep

The rev-3 design (10m per-session ticker, 30m activity-based TTL, symmetric
per-delegate reservation protocol across all revival paths, quiescence-aware
eviction, join-before-drainForClose) is preserved at commit `75c7b086` and
not built now. P1's factored mechanics, stop-gate, watch/pending guard, and
subtree-quiescence check are the code phase 2 would reuse; the delta is the
async reservation/ticker machinery. Build trigger: live sessions still
accumulating collectible lanes past an agreed threshold despite P2.

## Constants

| name | value | why |
|---|---|---|
| `laneSweepDelay` | 10m | one-shot P3 delay past open-time revival races |
| `laneGrace` | 30m | mtime grace covering the close-in-progress window |

## Testing (TDD, red → green per case)

P1 unit (real-git fixtures via the existing worktree test harness):

1. Terminal + merged (ancestry) delegate → disposed: lane removed,
   `EventDelegateDisposed`, branch + sidecar deleted.
2. Squash-merged (cherry arm) → disposed.
3. Unchanged lane, empty/deleted merge_target → disposed (Unchanged arm).
4. Clean + unmerged commits → refused with ahead report; `force` → disposed;
   `force_dirty` alone → still refused (orthogonality).
5. Dirty tree → refused; `force_dirty` → disposed; `force` alone → still
   refused on dirt.
6. Running or driving delegate → refused.
7. Wake-edge race: child notification arms concurrently with dispose → the
   stop-gate wins or the drive wins, never both (drive after gate refuses;
   dispose after drive-start sees `sub.driving` and refuses).
8. Retained child with live grandchild job / undelivered attention → refused
   (subtree quiescence).
9. Armed watch `send_to=` the delegate → refused, watch named.
10. Pending (fired, undelivered) watch-send targeting the delegate —
    including one held in a retained child's job manager and one restored
    across resume after its watch was budget-cleared → refused.
11. Retained quiescent child → stop-gated, evicted (scratch disposed),
    removed; subsequent `delegate_send` gets the disposed refusal **on the
    retained path**; refusal copy does not claim "at session close".
12. Foreign / session lock marker → refused. Non-delegate/unknown id →
    invalid_request.
13. `remove` blocked only by a retained terminal delegate → refusal names the
    delegate and suggests `dispose`.
14. Sandboxed session: control-env dance; unsatisfiable → clear error.

P2: nudge present iff collectible (both arms) AND session has the tool;
absent for unmerged lanes and in subagent sessions; copy pinned; render-time
git error → nudge omitted, report intact. Multi-provider live eval gates
landing.

P3 unit (fake clock):

15. Unlocked merged delegate lane KEPT by a prior session's close → collected
    at the timer; nothing before the delay; timer cancelled by early close.
16. Managed (non-delegate) worktree, unlocked+merged → untouched.
17. Lane mtime within grace → skipped.
18. Owner-resume re-lock: resumed session re-locks its undisposed lanes; P3
    in another session then skips them; revival still works (re-lock uses the
    owner's own marker).
19. Close-path late-dirty downgrade leaves the lane **unlocked**; after the
    owner closes and the branch merges, P3 collects it.
20. remove/branch refusal → lane left unlocked, sweep continues; no
    disposed-mark attempt; owner's later resume of that delegate gets the
    WorkingDir-stat refusal.
21. Locked lanes (any marker) → skipped. Child sessions / non-local envs →
    no P3.

E2E: (a) live nudge→dispose flow on a merged delegate (P2→P1); (b) session A
closes keeping a changed lane, branch merges, session B opens → after the
delay the lane/branch/sidecar are gone; resuming A's delegate reports the
working-directory-missing refusal.

## Files touched (est.)

- `agent/internal/tool/definitions.go` — `dispose` in the op enum + arg docs (~15 loc)
- `agent/session_tools_worktree.go` — dispose op + validation + remove-refusal legibility (~160 loc)
- `agent/session_worktree_close.go` — factor mechanics; downgrade no-relock (~45 loc)
- `agent/session_worktree_sweep.go` (new) — P3 pass parameterizing sweep1 (~90 loc)
- `agent/session_worktree_resume.go` — own-lane re-lock at resume (~30 loc)
- `agent/session_lifecycle.go` — P3 timer start/cancel (~15 loc)
- `agent/job_delegate.go` — unconditional Disposed check; refusal copy; report D0 eval for P2 (~40 loc)
- `agent/session_tools_jobs.go` — inline lane-report nudge surface (~10 loc)
- `agent/job_notify.go` — notification nudge surface (~15 loc)
- `docs/worktrees.md` + native worktree tools spec §9 — disposal is no longer
  close-only; `dispose` op documented (~doc)
- tests (~700 loc) + provider ergonomics eval scenarios

## Review log

**Rev 1 → rev 2** (reviewers A, B): reservation set replacing unimplementable
mutex serialization (A1/B4); retained-child eviction + unconditional Disposed
check (A2/B1); predicate reuse `Unchanged` OR `disposableReason` (A3/B2/B3);
residue pass rescoped to delegate lanes, skip-and-continue, top-level only
(A4/A5/B5/B6/B7); ticker/close quiesce (A6); latest-record TTL (A7/B8);
naming + scope (A8/A9/B9).

**Rev 2 → rev 3** (reviewers C, D): drive/steer turns mint no job record →
quiescence gates on `sub.running`/`sub.driving` (C1/D2); `liveWorkUnder`
verbatim made retained-child lanes unsweepable (C2/D1); symmetric reservation
protocol (C3, C4/D4/D5); foreign-store mark impossible → stat net (C5/D7);
never re-lock foreign lanes (C6); quiesce before `drainForClose` (D3);
unconsumed-result invariant (D6); coordinator gap accepted (C7); grace,
sandbox env, EndedAt-at-reconcile accepted (C8/C9/C10).

**Rev 3 → rev 4** (Jesse): restructured into phases around a synchronous
model-invoked `dispose` + completion nudge; rev-3 sweep deferred to phase 2.

**Rev 4 → rev 5** (reviewers E, F; conflicting claims adjudicated against
code):

- **E1 (major, confirmed over F's contrary clearance):** wake-edge drives
  launch from child-side notify goroutines at arbitrary moments
  (`subagents.go:725`) → P1 step 4 stop-gate; the "no concurrency machinery"
  claim narrowed honestly.
- **E2/F1 (major):** armed-watch guard missed pending fired-but-undelivered
  sends (incl. budget-cleared and child-jobManager pendings) → step 2 covers
  `cfg.pending` across self + retained children.
- **E3 (major):** child subtree liveness (grandchildren, undelivered
  attention) ignored → step 3 subtree quiescence via the DrainJobTree
  liveness check.
- **E4/F3 (major):** dispose inverted the force/force_dirty orthogonality →
  step 6 matches `remove` exactly; tests 4/5 pin orthogonality.
- **E5 (major):** nudge could render in sessions lacking the root-only tool →
  P2 gated on tool availability.
- **E6/F5 (major):** "unlocked ⇒ closed owner" was an assumption a resumed
  owner violates indefinitely → resume-time own-lane re-lock makes it an
  invariant; grace now covers only the close window.
- **E8 (minor):** close-path late-dirty re-lock made dead sessions' lanes
  permanently uncollectable → downgrade now unlocks-and-keeps.
- **E7 (minor):** P3 respecified as a parameterized reuse of
  `worktreePruneSweep1`.
- **F2 (major):** "everything a closed session leaves" overclaimed →
  qualified with "once its branch merges"; changed-never-merged persistence
  documented as intended.
- **F4 (major):** files-touched gaps (tool schema enum, inline result
  surface, report D0 eval) → added.
- **F6 (minor):** stale "disposed at session close" refusal copy + doc drift
  → scheduled.
- **F7 (minor):** unconsumed-result gate unreachable for delegates → dropped
  with rationale.
- **Field evidence (Jesse):** `remove` blocked by retained terminal delegate →
  problem statement §2 + the remove-refusal legibility companion fix.
