# Delegate-lane disposal: dispose operation + nudge + automatic residue collection

**Status:** draft spec, rev 6 (four adversarial review rounds, 4×2 competing
reviewers; history in §Review log)
**Problem owner:** Jesse
**Date:** 2026-07-13

## Problem

Isolation delegate worktree lanes (`dlg_*`) are only disposed in the parent
session's close path (`disposeDelegateLanesAtClose`, native worktree tools
spec §9 step 4). Merged-but-committed lanes are additionally only collected
by the `prune` operation of the `manage_worktree` tool — and a lane locked
with the live session's own `serf:dlg:` marker is skipped even by that.

Observed failures (2026-07-13):

1. One long-lived resumed session (`01KX4DMT…`, alive since 2026-07-09)
   accumulated **21 locked delegate lanes**, all fully merged into `main`.
   Nothing could ever collect them.
2. **Two independent sessions** that *actively tried* to clean up (`remove`
   on a clean, merged worktree) were **refused and gave up**, reporting "the
   runtime still marks completed delegate sessions as live". Root cause:
   `liveWorkUnder`'s subagent branch counts every retained child rooted
   under the path with no running/driving check, and labels it
   `"(subagent, running)"` (`session_tools_worktree.go:647-667`) — a
   completed, idle, retained delegate is reported to the model as a running
   subagent. The models correctly declined to bypass a safety guard; the
   guard's label was false, and no legitimate move could unblock them.
3. Closed sessions' KEPT lanes sit until someone happens to run prune.

So: no disposal primitive a live session can use, no prompt to use one, no
model-independent collection — and the one guard models do hit lies to them.

## Goal, phased

- **Phase 1 (build scope):** a synchronous `dispose` primitive (P1), a
  completion-time nudge toward it (P2), and automatic collection of
  closed-session residue (P3). P1+P2 make prompt cleanup possible and
  likely; P3 guarantees eventual, model-independent collection of a
  closed-session lane **once its branch merges and a top-level session later
  opens in that repo** (changed-never-merged and dirty lanes are preserved
  by design — work preservation outranks tidiness).
- **Phase 2 (specified, deferred):** the rev-3 background mid-life sweep as
  a backstop iff phase 1 measurably leaks in live long-running sessions
  (preserved at commit `75c7b086`).

## Non-goals / accepted limitations

- Automatic collection of managed (user/session) worktrees — `prune`
  territory, unchanged.
- Reclaiming lanes locked by foreign sessions; cross-process liveness
  detection is out.
- Live-session pileup is bounded, not eliminated, in phase 1; close-time
  disposal and P3 collect the remainder.
- Changed-never-merged lanes of dead sessions persist until deliberately
  forced out; **dirty** lanes of dead sessions likewise persist for manual
  handling (`force_dirty` / switch-in) — P3 never touches dirt.
- Changing the lock decision core (`worktree.Decide`) — no new events needed:
  `EvDelegateRevive` already expresses the resume re-lock decision (verified
  round 4).

## Phase 1 design

### D0. Disposal predicates (reused, not reinvented)

A lane is **collectible** iff its tree is clean (`worktree.CleanTree`) and
either existing predicate holds:

- `worktree.Unchanged(run, lane, sc.BaseSHA)` — tip == recorded base, or
- `disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)` disposable — the
  shared two-arm merged test (`worktree.Merged`: ancestry **or**
  cherry/patch-equivalence, with remote-tracking-tip resolution).

Branch deletion of a collectible lane is safe under either arm.

### P1. `dispose` operation on `manage_worktree` (new)

Synchronous, in-turn, on the owning session.

**Availability.** Two strips exist today (`session_init.go:770-780`): leaf
delegates (`delegationAllowance <= 0`) lose all root-only tools — unchanged;
worktree-isolated children lose `manage_worktree` unconditionally.
The second stays for the repo-wide ops but is loosened for `dispose`, which
is ownership-safe by construction (steps 1, 5): **isolated coordinators with
a delegation allowance get a dispose-only surface.** Mechanics (the registry
cannot express per-op gating — it removes whole tools): the gate is
**in-handler** on `spawn.isolation` (durable in the restore descriptor), and
the coordinator is registered with a **dispose-only tool definition variant**
whose schema/description lists only the `dispose` op — a schema advertising
five ops that refuse is a known model-confusion generator. The `delegate`
tool's isolation description ("The delegate cannot use manage_worktree
itself", `definitions.go:136`) is updated to match. This shrinks the
rev-3 coordinator gap (C7): grandchild lanes become collectible whenever the
coordinator's model follows the nudge. Non-isolated coordinators already
have the full tool and simply gain the op.

**Steps:**

1. **Validate ownership + record quiescence.** Ownership: the record is in
   **this** session's jobstore with `ParentSessionID == s.id` (forwarded
   copies in ancestor stores preserve the original creator and are refused).
   Record quiescence — latest job terminal, no running/queued follow-up —
   is taken **under `jm.mu`** with the durable snapshot and running-map read
   in the same hold (the `outstandingDelegateCount` recipe,
   `session_jobtree_drain.go:33-41`); `s.mu` guards none of that state and
   is used only for the in-memory session fields below. (rev-5.1 finding G6.)
2. **Validate delivery quiescence:** refuse if any armed watch routes
   `send_to` this delegate or any pending (fired-but-undelivered) watch-send
   targets it — `cfg.pending` / durable `EventWatchSendPending`
   (`ResolvedSendTo` / `ReceiverDelegateID` are enumerable) in this session's
   job manager **and all retained children's**. Refusal names the delivery.
3. **Validate subtree quiescence — two checks, both required:**
   - the drain-style check (`treeHasOutstandingWork`): no outstanding
     delegate jobs, undelivered attention, or driving in the child's subtree;
   - **`liveWorkUnder(lanePath)` for shell jobs:** the drain check
     *deliberately excludes background shells* (correct for drain, fatal for
     disposal — a grandchild's shell writing inside the lane must refuse
     disposal). The shell-handle branch of `liveWorkUnder` exists for exactly
     this (`jobstore/record.go:180-186`); its **subagent branch is ignored
     here** (the retained child itself is what we evict) — and, companion
     fix, that branch's label becomes honest: `"(subagent, retained — idle)"`
     when the child is quiescent, so `remove` refusals stop telling models a
     finished delegate is running (field failures #2).
   (rev-5.1 findings G2/H1.)
4. **Stop-gate the child** (under `sub.mu`): re-verify not running/driving
   and set the stop-gate so a wake-edge drive cannot launch afterwards
   (`SetNotifyFunc` fires from child-side goroutines at arbitrary moments,
   `subagents.go:725`).
5. **Refuse foreign lanes:** lock state must classify as this session's own
   `serf:dlg:` marker (or unlocked crash residue) per
   `worktree.ClassifyReason` / `worktree.Decide`.
6. **Evaluate the lane**, reporting state either way:
   - collectible per D0 → proceed;
   - unmerged commits → refuse; `force` overrides (merge gate);
   - dirty tree → refuse; `force_dirty` overrides (dirty gate).
   Flags orthogonal, matching `remove` exactly.
7. **Evict the retained child** if present: close it, remove from the
   subagent table, `DisposeSandboxScratch` for an owned env.
8. **Unlock → `git worktree remove` (non-force unless dirty-forced) → append
   `EventDelegateDisposed` → `git branch -D` → delete sidecar.** Factored
   from `disposeOneDelegateLane`; **the late-dirty downgrade is
   caller-dependent** (rev-5.1 finding G1): a *live* `dispose` that loses
   the clean-check race **re-locks with its own marker** and reports KEPT
   (preserving "unlocked ⇒ no live owner"); the *close path* unlocks-and-
   keeps (dead owner; see P3).

Post-disposal `delegate_send` gets the disposed refusal — made
**unconditional** (today checked only on the not-retained path,
`job_delegate.go:682,841`) — with copy generalized (current text hardcodes
"disposed at session close", `job_delegate.go:768`).

**Sandboxed sessions:** repo-root git ops build their control env exactly as
delegate revival does (`useDelegateWorktreeControlPolicy`); verified
workable from a lane-rooted coordinator env. Unsatisfiable → clear error.

### P2. Completion nudge

**Unconditional wording, conditional surface** (rev-5.1 finding G3 killed
render-time D0 gating: a changed lane is merged by the parent *after* the
completion report renders, so a merged-only nudge would never fire for the
flagship failure class — the nudge must anticipate the merge, and `dispose`
itself safely refuses premature calls):

The terminal lane report of a finished isolated delegate (inline tool result
AND background `job_finished` notification) gains one sentence, rendered iff
the receiving session **has the `dispose` op AND owns the delegate**
(`ParentSessionID == s.id` — reports can surface in ancestor sessions via
forwarded descriptors, where the nudge would point at a guaranteed refusal;
rev-5.1 finding H4):

`When you're done with this delegate's work (e.g., after merging it),
dispose its worktree and branch: manage_worktree op=dispose id=<dlg_…>.`

No render-time git evaluation (also removes that cost/context concern); no
nudge for lanes already gone. Wording notes (compact_context lesson):
conditional phrasing, exact call, no bare "clean up". Copy validated with
the multi-provider live ergonomics-eval harness before landing: (a) disposal
happens after the model merges a delegate's work, (b) no disposal of
delegates the scenario resumes later (premature `dispose` without `force`
refuses — assert the model doesn't force), (c) zero scolds/confusion.

### P3. Automatic residue collection (no model in the loop)

One pass per top-level session (`isSubagentSession()`), local exec envs
only, one-shot `s.clock` timer `laneSweepDelay` (10m) after open, cancelled
if close begins first. Scope: **delegate lanes only** (sidecar `DelegateID`
provenance) that are **unlocked** and collectible per D0, in **this repo's
worktree root** (the guarantee is per-repo: a repo no top-level session ever
opens in again is never swept — accepted, stated in §Goal).

**Implementation: parameterized reuse of `worktreePruneSweep1`** (verified
structurally sound round 4 — delegate lanes live under the same
`<worktreeRoot>/<projectID>/` enumeration), with: delegate-only filter,
`Unchanged` OR'd into the predicate, **skip-and-continue** error posture, no
disposed-mark attempt (foreign per-session store; the WorkingDir-stat crash
net covers revival — vaguer but accurate refusal, accepted), never re-lock
on failure, grace check below.

**Lock-model changes making "unlocked ⇒ no live owner" real:**

- **Close-time KEEP stamps the sidecar** with a `kept_at` timestamp (both
  the changed-lane KEEP and the late-dirty downgrade). P3's grace skips
  lanes whose `kept_at` **or** sidecar/directory mtime is within `laneGrace`
  (30m). Without the stamp, grace keyed on write-mtime protects nothing
  idle >30m — including the close-in-progress window it exists for
  (rev-5.1 finding H3a).
- **Session resume re-locks its own undisposed lanes** (enumerate own
  descriptors; re-take the `serf:dlg:` marker via the existing
  `EvDelegateRevive` decision — Unlocked→lock, OwnDelegate→adopt; no new
  lock-core event). Placement: this **cannot** ride `resumeWorktreeReentry`
  (which runs before `initSessionState`; the jobstore it needs doesn't exist
  yet) — it is a **new post-init resume step** in `session_init.go`, budgeted
  as such (rev-5.1 finding H3c). Failure handling: a failed re-lock emits a
  warning and is **retried once at the P3 timer**; still-failed → warning
  naming the exposed lane (rev-5.1 finding H3b).
- **Close-path late-dirty downgrade unlocks-and-keeps** (dead owner; the old
  re-lock made the lane invisible to every collector forever). Honest
  consequence (rev-5.1 finding H2): that lane is **dirty**, so P3 and prune
  still skip it — unlocking's benefit is manual collection (`force_dirty`,
  switch-in, adoption), not automatic collection. Test 19 asserts exactly
  that.

Close-time disposal is otherwise unchanged.

## Phase 2 (deferred backstop): mid-life background sweep

Rev-3 design preserved at commit `75c7b086`; P1's factored mechanics,
stop-gate, guards, and quiescence checks are the code it would reuse. Build
trigger: live sessions still accumulating collectible lanes past an agreed
threshold despite P2.

## Constants

| name | value | why |
|---|---|---|
| `laneSweepDelay` | 10m | one-shot P3 delay past open-time revival races; re-lock retry point |
| `laneGrace` | 30m | grace on `kept_at`/mtime covering close and hand-off windows |

## Testing (TDD, red → green per case)

P1 unit (real-git fixtures via the existing worktree test harness):

1. Terminal + merged (ancestry) → disposed: lane removed,
   `EventDelegateDisposed`, branch + sidecar deleted.
2. Squash-merged (cherry arm) → disposed.
3. Unchanged lane, empty/deleted merge_target → disposed (Unchanged arm).
4. Clean + unmerged commits → refused with ahead report; `force` → disposed;
   `force_dirty` alone → still refused (orthogonality).
5. Dirty tree → refused; `force_dirty` → disposed; `force` alone → refused.
6. Running or driving delegate → refused.
7. Wake-edge race: notification arms concurrently with dispose → stop-gate
   or drive wins, never both.
8. Live grandchild **background shell** rooted in the lane → refused (the
   `liveWorkUnder` shell branch; the drain check alone must not pass it).
9. Live grandchild delegate / undelivered attention → refused (drain check).
10. Armed watch `send_to=` the delegate → refused, watch named.
11. Pending watch-send targeting the delegate (incl. in a retained child's
    manager; incl. restored after budget-clear) → refused.
12. Retained quiescent child → stop-gated, evicted, removed; subsequent
    `delegate_send` disposed-refusal **on the retained path**; copy doesn't
    claim "at session close".
13. Live dispose loses the clean-check race → lane **re-locked with own
    marker**, KEPT reported.
14. Foreign / session lock marker → refused. Unknown id → invalid_request.
15. Availability: leaf delegate has no `manage_worktree`; non-isolated
    coordinator has full tool incl. `dispose`; isolated coordinator with
    allowance gets the dispose-only variant (schema lists only `dispose`;
    other ops refused in-handler); its dispose works on an own grandchild
    lane, refuses a sibling's (ownership); `delegate` tool description
    updated.
16. `remove` blocked only by a retained terminal delegate → refusal labels
    it `retained — idle` and suggests `dispose`.
17. Sandboxed session: control-env dance; unsatisfiable → clear error.

P2: nudge present on both surfaces iff session has the op AND
`ParentSessionID == s.id`; absent in ancestor sessions receiving forwarded
reports, in leaf delegates, and for non-isolated (no-lane) delegates; copy
pinned; no git run at render. Multi-provider live eval gates landing,
including the merge-then-dispose flow and the premature-dispose refusal.

P3 unit (fake clock):

18. Unlocked merged delegate lane KEPT by a prior session's close (kept_at
    aged out) → collected at the timer; nothing before the delay; timer
    cancelled by early close.
19. Late-dirty downgraded lane: left **unlocked** at close; P3 **skips** it
    (dirty) even after its branch merges; manual `force_dirty` collects it.
20. Managed (non-delegate) worktree, unlocked+merged → untouched.
21. `kept_at`/mtime within grace → skipped (including a lane idle >30m whose
    close just happened — the stamp, not write-mtime, governs).
22. Owner-resume re-lock (post-init step): resumed session re-locks
    undisposed lanes; another session's P3 skips them; revival adopts its
    own re-locked lane (`EvDelegateRevive` OwnDelegate→adopt). Failed
    re-lock → warning, retried at the P3 timer.
23. remove/branch refusal in P3 → lane left unlocked, sweep continues; no
    disposed-mark attempt; owner's later resume of that delegate gets the
    WorkingDir-stat refusal.
24. Locked lanes → skipped. Child sessions / non-local envs → no P3.

E2E: (a) live nudge flow — delegate completes, model merges, disposes
(P2→P1); (b) session A closes keeping a changed lane (kept_at stamped),
branch merges, session B opens → after delay+grace the lane/branch/sidecar
are gone; resuming A's delegate reports the working-directory-missing
refusal.

## Files touched (est.)

- `agent/internal/tool/definitions.go` — `dispose` in op enum; dispose-only
  definition variant; `delegate` isolation copy fix (~35 loc)
- `agent/session_tools_worktree.go` — dispose op + validation; in-handler op
  gate; remove-refusal label + dispose suggestion (~180 loc)
- `agent/session_worktree_close.go` — factor mechanics; caller-dependent
  downgrade; kept_at stamping (~55 loc)
- `agent/session_worktree_sweep.go` (new) — P3 parameterizing sweep1 (~90 loc)
- `agent/session_init.go` — post-init resume re-lock step; dispose-only
  surface wiring (~45 loc)
- `agent/session_lifecycle.go` — P3 timer start/cancel (~15 loc)
- `agent/internal/worktree/sidecar.go` — `kept_at` field (~10 loc)
- `agent/job_delegate.go` — unconditional Disposed check; refusal copy (~25 loc)
- `agent/session_tools_jobs.go` / `agent/job_notify.go` — nudge surfaces with
  ownership gate (~30 loc)
- `docs/worktrees.md` + native worktree tools spec §9 — disposal no longer
  close-only; `dispose` documented (~doc)
- tests (~800 loc) + provider ergonomics eval scenarios

## Review log

**Rev 1 → rev 2** (A, B): reservation set vs unimplementable mutex; retained
-child eviction + unconditional Disposed check; predicate reuse; residue pass
rescoped (delegate lanes, skip-and-continue, top-level); ticker/close
quiesce; latest-record TTL; naming/scope.

**Rev 2 → rev 3** (C, D): drive turns mint no record → sub flags; verbatim
`liveWorkUnder` made lanes unsweepable; symmetric reservation protocol;
foreign-store mark impossible; never re-lock foreign lanes; quiesce before
`drainForClose`; unconsumed-result invariant; coordinator gap accepted.

**Rev 3 → rev 4** (Jesse): restructured into phases around synchronous
`dispose` + nudge; rev-3 sweep deferred to phase 2.

**Rev 4 → rev 5** (E, F): wake-edge stop-gate (E1, adjudicated over F);
pending watch-sends (E2/F1); subtree quiescence (E3); force/force_dirty
orthogonality (E4/F3); nudge availability gate (E5); resume re-lock + close
downgrade no-relock (E6/F5/E8); sweep1 reuse (E7); guarantee qualified (F2);
files-touched gaps (F4); stale copy (F6); unconsumed gate dropped (F7);
field evidence #1 → problem §2.

**Rev 5 → rev 5.1** (Jesse): dispose-only surface for isolated coordinators;
availability policy corrected (two strips, not root-only).

**Rev 5.1 → rev 6** (G, H + field evidence):

- **G1 (major):** shared step-8 mechanics + no-relock would leave a LIVE
  dispose's raced lane unlocked → downgrade is caller-dependent.
- **G2/H1 (major):** drain liveness excludes background shells → step 3 adds
  the `liveWorkUnder` shell branch; test 8.
- **G3 (major):** merged-gated nudge can't fire before the parent merges →
  P2 redesigned: unconditional wording, no render-time D0.
- **H2 (major):** late-dirty downgraded lane is dirty, so "P3 collects it"
  was false → honest semantics + test 19 rewritten (manual collection).
- **H3 (major):** resume re-lock couldn't ride `resumeWorktreeReentry`
  (pre-jobstore), had no failure story, and mtime-grace protected nothing
  idle >30m → post-init step, retry-at-timer, `kept_at` sidecar stamp.
- **H4 (minor):** nudge gate needs ownership (forwarded descriptors) →
  availability AND `ParentSessionID == s.id`.
- **G4 (minor):** registry can't express per-op gating → in-handler gate +
  dispose-only definition variant.
- **G5 (minor):** `delegate` tool copy "cannot use manage_worktree" → fixed.
- **G6 (minor):** record quiescence under `jm.mu` (same-hold snapshot), not
  `s.mu`.
- **G7 (minor):** P3 guarantee qualified per-repo; resume window covered by
  re-lock + grace stamp.
- **Field evidence #2 (Jesse, two sessions):** `remove` refusals label
  retained idle delegates as "running" → honest label + dispose suggestion
  (problem §2, test 16).

Verified-clean this round (for the record): sweep1 reuse structurally sound;
pending-send guard enumerable; `EvDelegateRevive` covers resume re-lock with
no lock-core change; forwarded-descriptor ownership filtering correct;
lane-rooted coordinator control env workable.
