# Delegate-lane disposal: dispose operation + nudge + automatic residue collection

**Status:** draft spec, rev 7 (five adversarial review rounds, 5×2 competing
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
2. **Two independent sessions** that *actively tried* to clean up (`remove`
   on a clean, merged worktree) were **refused and gave up**: `liveWorkUnder`'s
   subagent branch counts every retained child rooted under the path with no
   running/driving check and labels it `"(subagent, running)"`
   (`session_tools_worktree.go:647-667`) — a completed, idle, retained
   delegate is reported to the model as a running subagent. The models
   correctly declined to bypass a safety guard; the guard's label was false.
3. Closed sessions' KEPT lanes sit until someone happens to run prune.

## Goal, phased

- **Phase 1 (build scope):** a synchronous `dispose` primitive (P1), a
  completion-time nudge toward it (P2), and automatic collection of
  closed-session residue (P3). P1+P2 make prompt cleanup possible and
  likely; P3 guarantees eventual, model-independent collection of a
  closed-session lane **once its branch merges and a top-level session later
  opens or closes in that repo**.
- **Phase 2 (specified, deferred):** the rev-3 background mid-life sweep as
  a backstop iff phase 1 measurably leaks (preserved at commit `75c7b086`).

## Non-goals / accepted limitations

- Automatic collection of managed (user/session) worktrees — `prune`
  territory, unchanged.
- Reclaiming lanes locked by foreign sessions.
- Live-session pileup is bounded, not eliminated, in phase 1.
- **P3 cadence gap (accepted):** P3 fires per top-level session at open+10m
  **and once at close**. A lane whose branch merges while sessions are
  already past their open pass is collected at the next top-level close or
  open in that repo — in a long-lived-session workflow that can lag hours or
  days. Continuous collection is exactly phase 2's ticker; this gap is part
  of its build trigger. (rev-6 finding J3.)
- Changed-never-merged and dirty lanes of dead sessions persist for manual
  handling (`force`/`force_dirty`/switch-in) — P3 never touches dirt.
- Changing the lock decision core (`worktree.Decide`) — `EvDelegateRevive`
  already expresses the resume re-lock (verified round 4).

## Phase 1 design

### D0. Disposal predicates (reused, not reinvented)

A lane is **collectible** iff its tree is clean (`worktree.CleanTree`) and
either existing predicate holds:

- `worktree.Unchanged(run, lane, sc.BaseSHA)` — tip == recorded base, or
- `disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)` disposable — the
  shared two-arm merged test (`worktree.Merged`: ancestry **or**
  cherry/patch-equivalence, with remote-tracking-tip resolution).

### P1. `dispose` operation on `manage_worktree` (new)

Synchronous, in-turn, on the owning session. Target: a delegate id
(`dlg_*`); the lane path resolves from the descriptor's `WorkingDir`, the
sidecar from `metaDirForLane` of it.

**Availability.** Two strips exist today (`session_init.go:770-780`): leaf
delegates (`delegationAllowance <= 0`) lose all root-only tools — unchanged;
worktree-isolated children lose `manage_worktree` unconditionally. The
second stays for the repo-wide ops but is loosened for `dispose`
(ownership-safe by construction, steps 1/6): **isolated coordinators with a
delegation allowance get a dispose-only surface** — the gate is
**in-handler** on `spawn.isolation` (durable in the restore descriptor;
per-session registries make a **dispose-only tool-definition variant**
expressible, so the schema doesn't advertise ops that refuse). The
`delegate` tool's isolation copy ("The delegate cannot use manage_worktree
itself", `definitions.go:136`) is updated. Non-isolated coordinators already
have the full tool.

**Steps:**

1. **Validate ownership + record quiescence.** Ownership: record in **this**
   session's jobstore with `ParentSessionID == s.id` (forwarded copies
   refused). Record quiescence — latest job terminal, no running/queued
   follow-up — under **`jm.mu`** with snapshot and running-map read in one
   hold (the `outstandingDelegateCount` recipe).
2. **Validate delivery quiescence:** refuse on any armed watch routing
   `send_to` this delegate or any pending (fired-but-undelivered) watch-send
   targeting it — `cfg.pending` / durable `EventWatchSendPending` in this
   session's job manager **and all retained children's**. Refusal names it.
3. **Validate subtree quiescence — two checks, both required:**
   - drain-style (`treeHasOutstandingWork`): no outstanding delegate jobs,
     undelivered attention, or driving in the child's subtree;
   - **live shells under the lane, tree-wide:** the drain check excludes
     shells by design, and the parent's `liveWorkUnder` shell branch scans
     only the parent's own `jm.running` (`agent/jobs.go:275-292`) — a
     grandchild's shell lives in the **retained child's** job manager and is
     invisible to both (rev-6 finding I1). New small helper: recurse
     `liveWorkHandles()` across this session **and all retained descendant
     sessions**, refusing if any live shell's `WorkingDir`
     (`jobstore/record.go:218-225`) is under the lane. The subagent branch
     is not used here (the retained child is what step 7 evicts) — and,
     companion fix, that branch's label becomes honest:
     `"(subagent, retained — idle)"` when the child is quiescent, so
     `remove` refusals stop calling finished delegates "running" (field
     failures #2).
4. **Dispose-gate the child.** No existing primitive fits: `childStopGated`
   keys on a cancelled-by-parent record and cannot gate a *completed*
   delegate, and the durable stop-gate field feeds only watch-send
   suppression (rev-6 finding J1). New minimal machinery: a per-child
   in-memory `sub.disposeGated` flag set **under `sub.mu`** after
   re-verifying `!running && !driving`; `driveSubagentNotificationTurn`
   refuses to launch while it is set (same `sub.mu` hold that sets
   `sub.driving`), and `delegate_send`'s retained path refuses with a
   busy/retryable error. **Reversal is mandatory:** any refusal or failure
   after this step clears the flag before returning, so a kept lane's
   delegate is immediately resumable again. Eviction (step 7) makes the flag
   moot.
5. **Refuse foreign lanes:** lock state must classify as this session's own
   `serf:dlg:` marker (or unlocked crash residue) per
   `worktree.ClassifyReason` / `worktree.Decide`.
6. **Evaluate the lane**, reporting state either way:
   - collectible per D0 → proceed;
   - unmerged commits → refuse; `force` overrides (merge gate);
   - dirty tree → refuse; `force_dirty` overrides (dirty gate);
   - **lane directory missing/not-a-worktree but record+branch+sidecar
     remain** (half-removed residue, manual `mv`): skip the tree checks,
     judge the *branch tip* against base/merge_target directly; collectible
     → proceed to step 8's mark/branch-delete/sidecar-delete (no worktree
     remove); unmerged → refuse with the state named (rev-6 finding J7).
   Flags orthogonal, matching `remove` exactly.
7. **Evict the retained child** if present: close it, remove from the
   subagent table, `DisposeSandboxScratch` for an owned env.
8. **Unlock → `git worktree remove` (non-force unless dirty-forced) → append
   `EventDelegateDisposed` → `git branch -D` → delete sidecar.** Factored
   from `disposeOneDelegateLane`; the late-dirty downgrade is
   **caller-dependent** (rev-5.1 G1): close path unlocks-and-keeps (dead
   owner); a live `dispose` whose remove is refused **stats the lane
   first** (rev-6 finding I2): directory gone (a concurrent collector won
   the unlock window) → treat as collected — append the Disposed mark (our
   store), clean up branch/sidecar remnants, report disposed; directory
   present (late dirty write) → re-lock with own marker and report KEPT; a
   failed re-lock is a warning naming the exposed lane, not a silent KEPT.

Post-disposal `delegate_send` takes the not-retained restore path (eviction
precedes the mark) where the Disposed check already lives; the check is
**also added to the retained path** as cheap defense-in-depth — no phase-1
flow produces retained+Disposed, so its test constructs the state explicitly
(rev-6 finding I3) — and the refusal copy is generalized (today hardcodes
"disposed at session close", `job_delegate.go:768`).

**Sandboxed sessions:** control env exactly as delegate revival
(`useDelegateWorktreeControlPolicy`); unsatisfiable → clear error.

### P2. Completion nudge

Unconditional wording, conditional surface (round-4 G3: lanes merge *after*
the completion report renders, so a merged-gated nudge would never fire for
the flagship class; `dispose` refuses premature calls safely):

The terminal lane report of a finished isolated delegate (inline tool result
AND background `job_finished` notification) gains one sentence, rendered iff
the receiving session **has the `dispose` op AND owns the delegate**
(`ParentSessionID == s.id`; forwarded reports in ancestors get no nudge):

`When you're done with this delegate's work (e.g., after merging it),
dispose its worktree and branch: manage_worktree op=dispose id=<dlg_…>.`

No render-time git evaluation. Copy validated with the multi-provider live
ergonomics-eval harness before landing: (a) disposal after the model merges,
(b) no disposal of delegates the scenario resumes later (and no `force`
under the premature refusal), (c) zero scolds/confusion.

### P3. Automatic residue collection (no model in the loop)

Runs per top-level session (`isSubagentSession()`), local exec envs only:
**once at open+`laneSweepDelay` (10m)** — past open-time revival races —
**and once at close**, immediately after `disposeDelegateLanesAtClose`
(same quiesce domain; a long-lived session thereby collects at its exit
what merged during its life — rev-6 finding J3). The open timer is cancelled
if close begins first, and **close joins an in-flight open pass** before its
own disposal runs — cancellation alone doesn't cover a pass mid-git (rev-6
finding J2a).

Scope: **delegate lanes only** (sidecar `DelegateID` provenance), **unlocked**
and collectible per D0, in this repo's worktree root. Implementation:
parameterized reuse of `worktreePruneSweep1` (structurally verified) with
delegate-only filter, `Unchanged` OR'd in, skip-and-continue, no
disposed-mark attempt (foreign store; WorkingDir-stat crash net covers
revival), never re-lock on failure, grace below.

**Lock/grace model:**

- **Grace = sidecar mtime** (`SidecarAge` pattern — the sidecar layer itself
  documents mtime-over-wall-clock for cross-machine skew,
  `sidecar.go:31-38,138-149`): skip lanes whose sidecar mtime is within
  `laneGrace` (30m). **Close-time KEEP touches the sidecar** (rewrite, no
  new field — the write *is* the signal; rev-6 finding J4 killed rev-6's
  `kept_at` wall-clock field), and the touch is pinned **before the
  unlock** in the close path, so no collector can observe
  unlocked+out-of-grace mid-close (rev-6 finding J2b). Both KEEP paths
  (changed-lane and late-dirty downgrade) touch.
- **Session resume re-locks its own undisposed lanes** via
  `EvDelegateRevive` (Unlocked→lock, OwnDelegate→adopt). Placement: a
  **post-init resume step** (needs the jobstore; `resumeWorktreeReentry`
  runs pre-init). Failure: warning + one retry — at the P3 open timer for
  top-level sessions, or a dedicated one-shot `laneSweepDelay` timer for
  restored subagent coordinators, which have no P3 (rev-6 finding I4);
  still-failed → warning naming the exposed lane.
- **Close-path late-dirty downgrade unlocks-and-keeps** (dead owner). That
  lane is dirty, so P3/prune skip it; unlocking's benefit is manual
  collection (`force_dirty`, switch-in, adoption) — asserted, not
  oversold.

Close-time disposal is otherwise unchanged.

## Phase 2 (deferred backstop): mid-life background sweep

Rev-3 design preserved at `75c7b086`; P1's factored mechanics, dispose-gate,
guards, and quiescence checks are the code it would reuse. Build trigger:
live sessions still accumulating collectible lanes past an agreed threshold
despite P2 — including via the accepted P3 cadence gap.

## Constants

| name | value | why |
|---|---|---|
| `laneSweepDelay` | 10m | P3 open-pass delay past revival races; re-lock retry point |
| `laneGrace` | 30m | sidecar-mtime grace covering close and hand-off windows |

## Testing (TDD, red → green per case)

P1 unit (real-git fixtures via the existing worktree test harness):

1. Terminal + merged (ancestry) → disposed: lane removed,
   `EventDelegateDisposed`, branch + sidecar deleted.
2. Squash-merged (cherry arm) → disposed.
3. Unchanged lane, empty/deleted merge_target → disposed (Unchanged arm).
4. Clean + unmerged commits → refused with ahead report; `force` → disposed;
   `force_dirty` alone → still refused (orthogonality).
5. Dirty tree → refused; `force_dirty` → disposed; `force` alone → refused.
6. Running or driving delegate → refused (before the gate is ever set).
7. Dispose-gate: gate set under `sub.mu` after re-verify; a concurrently
   arming notification cannot launch a drive while gated; `delegate_send`
   retained path gets busy/retryable; **a refusal at step 5/6 clears the
   gate** and the delegate drives/resumes normally after.
8. Live grandchild **background shell in a retained child's job manager**,
   rooted in the lane → refused (the recursive handle walk; the parent-only
   scan must fail this test red first).
9. Live grandchild delegate / undelivered attention → refused (drain check).
10. Armed watch `send_to=` the delegate → refused, watch named.
11. Pending watch-send targeting the delegate (incl. in a retained child's
    manager; incl. restored after budget-clear) → refused.
12. Retained quiescent child → gated, evicted, removed; subsequent
    `delegate_send` gets the disposed refusal (restore path); retained-path
    Disposed check verified against a constructed retained+Disposed state;
    copy doesn't claim "at session close".
13. Live dispose remove-refusal: lane present+dirty → re-locked own marker,
    KEPT; lane **gone** (concurrent collector) → Disposed mark appended,
    branch/sidecar remnants cleaned, reported disposed; re-lock failure →
    warning naming the lane.
14. Half-removed residue (dir gone, record+branch+sidecar remain): merged
    branch → mark+branch-delete+sidecar-delete; unmerged → refused naming
    the state. Unknown id → invalid_request.
15. Foreign / session lock marker → refused.
16. Availability: leaf delegate has no `manage_worktree`; non-isolated
    coordinator full tool; isolated coordinator with allowance gets the
    dispose-only variant (schema lists only `dispose`); ownership refusal on
    a sibling's lane; `delegate` tool description updated.
17. `remove` blocked only by a retained terminal delegate → refusal labels
    it `retained — idle` and suggests `dispose`.
18. Sandboxed session: control-env dance; unsatisfiable → clear error.

P2: nudge on both surfaces iff op available AND `ParentSessionID == s.id`;
absent in ancestors/leaves/non-isolated delegates; copy pinned; no git at
render. Multi-provider live eval gates landing.

P3 unit (fake clock):

19. Unlocked merged delegate lane KEPT by a prior session's close (sidecar
    mtime aged past grace) → collected at the open pass; nothing before the
    delay; open timer cancelled by early close.
20. **Close pass:** lane merges while a long-lived session is open → its
    close pass collects it (after own disposal, before store close).
21. Close vs in-flight open pass → close joins the pass first; no
    overlapping git ops on the same lanes.
22. Close-KEEP sidecar touch ordering: touch lands before unlock; a
    collector observing mid-close always sees either locked or
    within-grace.
23. Late-dirty downgraded lane: left unlocked; P3 skips (dirty) even after
    its branch merges; manual `force_dirty` collects.
24. Managed (non-delegate) worktree, unlocked+merged → untouched.
25. Owner-resume re-lock (post-init): re-locks undisposed lanes; another
    session's P3 skips them; revival adopts own re-locked lane; failed
    re-lock retried at the appropriate timer (top-level: P3 open timer;
    restored coordinator: dedicated one-shot).
26. remove/branch refusal in P3 → lane left unlocked, sweep continues; no
    disposed-mark attempt; owner's later resume gets the WorkingDir-stat
    refusal.
27. Locked lanes → skipped. Child sessions / non-local envs → no P3.

E2E: (a) live nudge flow — delegate completes, model merges, disposes;
(b) session A closes keeping a changed lane (sidecar touched), branch
merges, session B opens → after delay+grace the lane/branch/sidecar are
gone; resuming A's delegate reports the working-directory-missing refusal.

## Files touched (est.)

- `agent/internal/tool/definitions.go` — `dispose` op; dispose-only variant;
  `delegate` isolation copy (~35 loc)
- `agent/session_tools_worktree.go` — dispose op + validation; in-handler op
  gate; honest subagent label + dispose suggestion in `remove` refusals
  (~190 loc)
- `agent/session_worktree_close.go` — factor mechanics; caller-dependent
  downgrade; sidecar touch before unlock on KEEP paths (~55 loc)
- `agent/session_worktree_sweep.go` (new) — P3 pass parameterizing sweep1;
  open-timer + close-pass entry points (~110 loc)
- `agent/jobs.go` — recursive tree-wide `liveWorkHandles` helper (~25 loc)
- `agent/subagents.go` / `agent/job_watch.go` — `sub.disposeGated` flag;
  drive-launch and retained-send checks (~30 loc)
- `agent/session_init.go` — post-init resume re-lock (+ coordinator retry
  timer); dispose-only surface wiring (~55 loc)
- `agent/session_lifecycle.go` — P3 open-timer start/cancel; close-pass call
  + in-flight join (~25 loc)
- `agent/job_delegate.go` — retained-path Disposed check; refusal copy (~25 loc)
- `agent/session_tools_jobs.go` / `agent/job_notify.go` — nudge surfaces with
  ownership gate (~30 loc)
- `docs/worktrees.md` — beyond the two scheduled updates: `:121-123` (the
  delegate-can't-use-manage_worktree claim vs the dispose-only surface),
  `:136-138` (the "prune will *offer* to collect" promise vs P3's silent
  collection — deliberate behavior change, documented), `:108-112` (manual
  `remove` guidance → `dispose`); native worktree tools spec §9 (~doc)
- tests (~900 loc) + provider ergonomics eval scenarios

## Review log

**Rev 1 → rev 2** (A, B): reservation vs unimplementable mutex; retained-child
eviction + Disposed check; predicate reuse; residue pass rescoped; quiesce;
latest-record TTL.

**Rev 2 → rev 3** (C, D): drive turns mint no record; liveWorkUnder verbatim
unsweepable; symmetric reservation; foreign-store mark; never re-lock foreign;
quiesce before drainForClose; coordinator gap accepted.

**Rev 3 → rev 4** (Jesse): phased restructure around synchronous `dispose` +
nudge; sweep deferred to phase 2.

**Rev 4 → rev 5** (E, F): wake-edge stop-gate need (E1, adjudicated);
pending watch-sends; subtree quiescence; force orthogonality; nudge
availability; resume re-lock + downgrade no-relock; sweep1 reuse; guarantee
qualified; field evidence #1.

**Rev 5 → rev 5.1** (Jesse): dispose-only surface for isolated coordinators.

**Rev 5.1 → rev 6** (G, H + field #2): caller-dependent downgrade; shell-aware
quiescence; nudge redesign (no render-time D0); honest late-dirty semantics;
post-init re-lock + kept_at + grace rework; ownership nudge gate; in-handler
op gate; honest "retained — idle" label.

**Rev 6 → rev 7** (I, J):

- **J1 (critical):** no stop-gate primitive exists for a *terminal* delegate
  (`childStopGated` needs cancelled-by-parent; durable gate feeds only
  watch suppression) → new minimal `sub.disposeGated` flag under `sub.mu`
  with mandatory reversal on refusal; test 7.
- **I1 (major, + J5's citation catch):** parent-only `liveWorkUnder` cannot
  see grandchild shells in retained children's job managers → recursive
  tree-wide `liveWorkHandles` helper, budgeted; citations corrected.
- **J2 (major):** in-flight open pass vs close needed a join, not just
  cancellation; sidecar touch pinned before unlock.
- **J3 (major):** one-shot-at-open cadence left merge-after-open lanes
  uncollected for the life of long sessions → close pass added; residual
  gap documented as accepted + phase-2 trigger.
- **J4 (minor):** `kept_at` wall-clock field was the sidecar layer's own
  documented anti-pattern and redundant with the write's mtime → dropped;
  grace keys on sidecar mtime, KEEP touches the sidecar.
- **I2 (minor):** live dispose racing a concurrent collector → stat-after-
  refusal: gone ⇒ mark disposed + clean remnants; present ⇒ re-lock; re-lock
  failure warns.
- **I3 (minor):** retained-path Disposed check has no producing phase-1
  scenario → kept as defense-in-depth, test constructs the state.
- **I4 (minor):** restored subagent coordinators have no P3 timer for the
  re-lock retry → dedicated one-shot.
- **J6 (minor):** three additional `docs/worktrees.md` contradictions
  scheduled, incl. the "prune will offer" promise P3 deliberately changes.
- **J7 (minor):** half-removed-lane outcome defined (step 6 fourth arm,
  test 14).

Verified-clean in rounds 4–5 (foundations positively confirmed): sweep1
reuse; pending-send enumerability; `EvDelegateRevive` rows; forwarded-
descriptor ownership; lane-rooted coordinator control env; per-session
registries expressing the dispose-only variant; sidecar machinery in scope
at close; render sites seeing `ParentSessionID`; delegate sidecars carrying
`MergeTarget` (the flagship 21-lane class is collectible).
