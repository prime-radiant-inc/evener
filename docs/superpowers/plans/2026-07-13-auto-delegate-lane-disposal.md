# Delegate-lane disposal: dispose operation + nudge + automatic residue collection

**Status:** draft spec, rev 8 (six adversarial review rounds, 6×2 competing
reviewers; history in §Review log)
**Problem owner:** Jesse
**Date:** 2026-07-13

## Problem

Isolation delegate worktree lanes (`dlg_*`) are only disposed in the parent
session's close path — and only when **unchanged** (`worktree.Unchanged`);
merged-but-committed lanes are KEPT at close and collected only by the
model-invoked `prune` operation. A lane locked with the live session's own
`serf:dlg:` marker is skipped even by that.

Observed failures (2026-07-13):

1. One long-lived resumed session (`01KX4DMT…`, alive since 2026-07-09)
   accumulated **21 locked delegate lanes**, all fully merged into `main`.
2. **Two independent sessions** that *actively tried* to clean up (`remove`
   on a clean, merged worktree) were **refused and gave up**: `liveWorkUnder`'s
   subagent branch counts every retained child rooted under the path with no
   running/driving check and labels it `"(subagent, running)"`
   (`session_tools_worktree.go:647-667`). The models correctly declined to
   bypass a safety guard; the guard's label was false.
3. Closed sessions' KEPT lanes sit until someone happens to run prune.

## Goal, phased

- **Phase 1 (build scope):**
  - **P0:** close-time disposal collects **merged** lanes, not just
    unchanged ones — the owner disposes its own residue at exit, with the
    durable mark, no model action (this alone collects the flagship 21 at
    that session's close).
  - **P1:** a synchronous `dispose` primitive so a live session (model) can
    collect a lane the moment its work merges.
  - **P2:** a completion-time nudge toward P1.
  - **P3:** automatic collection of dead sessions' residue by later
    sessions.
- **Phase 2 (specified, deferred):** the rev-3 background mid-life sweep as
  a backstop iff phase 1 measurably leaks (preserved at commit `75c7b086`).

## Non-goals / accepted limitations

- Automatic collection of managed (user/session) worktrees — `prune`
  territory, unchanged.
- Reclaiming lanes locked by foreign sessions.
- **Multi-commit squash merges are not auto-detected** (rev-7 finding L2):
  `worktree.Merged`'s cherry arm is single-commit patch-equivalence, and
  `docs/worktrees.md:108-113` already documents such lanes reporting
  `unmerged`. They are collected only via model judgment (`dispose` +
  `force` after verifying the squash landed) — the nudge makes that path
  likely; P0/P3 never collect them. Accepted.
- Live-session pileup between merges and close is bounded by P1/P2, not
  eliminated; **P3 cadence** (open+10m and close passes) can lag a merge by
  hours in long-lived-session workflows. Both feed phase 2's build trigger.
- Changed-never-merged and dirty lanes of dead sessions persist for manual
  handling — nothing automatic touches dirt or unmerged work.
- Changing the lock decision core — `EvDelegateRevive` suffices (verified).

## Phase 1 design

### D0. Disposal predicates (reused, not reinvented)

A lane is **collectible** iff its tree is clean (`worktree.CleanTree`) and
either existing predicate holds:

- `worktree.Unchanged(run, lane, sc.BaseSHA)` — tip == recorded base, or
- `disposableReason(run, tip, sc.BaseSHA, sc.MergeTarget)` disposable — the
  shared two-arm merged test (`worktree.Merged`: ancestry or single-commit
  cherry/patch-equivalence, with remote-tracking-tip resolution).

### P0. Close-time disposal collects merged lanes

`disposeOneDelegateLane`'s predicate widens from `worktree.Unchanged` to
**collectible per D0** (children are already closed at that point; a merged
lane's work is reachable from the target, so removal+`branch -D` is safe —
the "unchanged lane: tip == base, no work lost" rationale generalizes).
Consequences:

- The owner appends `EventDelegateDisposed` in its **own** store — later
  resumes get the clear disposed refusal, not the stat-net fallback.
- The KEEP set shrinks to unmerged / dirty / state-unverifiable lanes.
- **Every KEEP path touches the sidecar before unlocking** — changed-lane,
  late-dirty downgrade, **and the state-unverifiable path**
  (`session_worktree_close.go:139-145`), which rev 7 missed (finding K4);
  the touch (an `UpdateSidecar` rewrite; mtime is the signal) is what P3's
  grace keys on.
- This resolves rev-7's self-nullifying pair (finding L1): rev 7 wanted a
  close-time P3 pass to collect the session's own just-merged lanes, but
  the KEEP touch put them inside the grace window. With P0, own merged
  lanes never reach KEEP at all; the close-time P3 pass exists only for
  *foreign* residue and keeps its grace semantics intact.

### P1. `dispose` operation on `manage_worktree` (new)

Synchronous, in-turn, on the owning session. Target: a delegate id
(`dlg_*`); the lane path resolves from the descriptor's `WorkingDir`, the
sidecar from `metaDirForLane` of it. **The git control env resolves from the
sidecar's `OriginalRoot`** — not by walking up from the lane path: lanes
live under the state dir, *outside any git repo*, so with the lane directory
gone there is nothing to walk up from (rev-7 finding K1; `OriginalRoot`
exists for exactly this, `sidecar.go:23`). The lane-path route stays as the
consistency cross-check when the directory exists.

**Availability.** Two strips exist today (`session_init.go:770-780`): leaf
delegates (`delegationAllowance <= 0`) lose all root-only tools — unchanged;
worktree-isolated children lose `manage_worktree` unconditionally. The
second is loosened for `dispose` (ownership-safe by construction):
**isolated coordinators with a delegation allowance get a dispose-only
surface** — in-handler gate on `spawn.isolation` plus a dispose-only
tool-definition variant (per-session registries make this expressible). The
`delegate` tool's isolation copy (`definitions.go:136`) is updated.

**Steps:**

1. **Validate ownership + record quiescence.** Record in **this** session's
   jobstore with `ParentSessionID == s.id` (forwarded copies refused).
   Latest job terminal, no running/queued follow-up — under **`jm.mu`**,
   snapshot and running-map in one hold. **An already-Disposed record is
   handled idempotently, not refused** (rev-7 finding L6): skip to remnant
   cleanup — delete the branch if its tip judges collectible, delete the
   sidecar, report already-disposed — so a crash between the mark and
   `branch -D` doesn't strand remnants forever.
2. **Validate delivery quiescence:** refuse on any armed watch routing
   `send_to` this delegate or any pending watch-send targeting it —
   `cfg.pending` / durable `EventWatchSendPending` in this session's job
   manager and all retained children's.
3. **Validate subtree quiescence — two checks:** drain-style
   (`treeHasOutstandingWork`), and live shells tree-wide via the recursive
   `liveWorkHandles` walk across this session and all retained descendants
   (grandchild shells live in the child's job manager,
   `jobstore/record.go:218-225`). Companion fix: `liveWorkUnder`'s subagent
   label becomes `"(subagent, retained — idle)"` when quiescent.
4. **Dispose-gate the child:** per-child in-memory `sub.disposeGated` under
   `sub.mu` after re-verifying `!running && !driving`;
   `driveSubagentNotificationTurn` refuses while set; `delegate_send`
   retained path gets busy/retryable. **Reversal mandatory** on every
   later refusal/failure exit. (No existing primitive fits — verified
   round 5.)
5. **Refuse foreign lanes:** lock state per `worktree.ClassifyReason` /
   `worktree.Decide`.
6. **Evaluate the lane:** collectible → proceed; unmerged → refuse, `force`
   overrides; dirty → refuse, `force_dirty` overrides (orthogonal, matching
   `remove`); **lane directory missing but record+branch+sidecar remain** →
   judge the branch tip via the `OriginalRoot` control env; collectible →
   step 8's mark/branch-delete/sidecar-delete; unmerged → refuse naming the
   state.
7. **Evict the retained child:** close, remove from subagent table,
   `DisposeSandboxScratch`.
8. **Unlock → `git worktree remove` (non-force unless dirty-forced) →
   `EventDelegateDisposed` → `git branch -D` → delete sidecar** (factored
   from `disposeOneDelegateLane`). Remove refused → stat: gone (concurrent
   collector) → mark + clean remnants + report disposed; present (late
   dirty) → re-lock own marker, KEPT; re-lock failure → warning naming the
   lane.

**Dispose-turn vs own-close race** (rev-7 finding K2 — new race P1
introduces; close cancels the turn ctx then runs its own disposal *before*
joining tool goroutines, `session_lifecycle.go:124-126,164,215-219`, and
both actors hold the same own-marker lock so the lock protocol gives no
exclusion): steps 5–8 run inside a session-level **in-flight-dispose
WaitGroup**; the close path **joins it first** — at the same pre-
`drainForClose` point where it joins the P3 open pass — so close's
disposal/`drainForClose` never overlaps a mid-dispose lane or yanks the
gated child under step 7. Dispose observes ctx cancellation only *between*
git ops (each op completes or fails atomically; the reversal clause covers
the failure exits).

Post-disposal `delegate_send` takes the restore path where the Disposed
check lives; the check is also added to the retained path as
defense-in-depth (test constructs the state); refusal copy generalized
(today hardcodes "at session close", `job_delegate.go:768`).

**Mid-life Disposed visibility** (rev-7 finding L4): `FoldDelegates` /
`DelegateRecord` gain Disposed handling so `Delegates()` consumers
(doctor tree, listings) stop showing a disposed delegate as resumable —
tolerable when disposal implied a dead session, wrong once P1 makes it
routine.

**Sandboxed sessions:** control env exactly as delegate revival
(`useDelegateWorktreeControlPolicy`); unsatisfiable → clear error.

### P2. Completion nudge

Unconditional wording, conditional surface (lanes merge *after* the report
renders; `dispose` refuses premature calls safely). Rendered on both
surfaces iff the receiving session **has the op AND owns the delegate**
(`ParentSessionID == s.id`):

`When you're done with this delegate's work (e.g., after merging it),
dispose its worktree and branch: manage_worktree op=dispose id=<dlg_…>.`

No render-time git. **Honest reach** (rev-7 finding L7): the nudge is a
completion-time surface — it never reaches lanes whose delegates already
completed (the existing 21 get collected by P0 at that session's close, or
by the model spontaneously; not by P2).

**Eval gate, made falsifiable** (rev-7 finding L5): scenario cards under
`tests/scenarios/worktree-dispose/` (the e2e-scenario-testing format used
for the F1–F4 ergonomics evals), run live against **3 providers ×
3 runs each** (claude / gpt-5.x / kimi tiers configured in the harness).
Pass: (a) ≥ 2/3 runs per provider dispose after the scenario's merge step;
(b) 0/9 runs dispose or `force` a delegate the scenario later resumes;
(c) 0/9 scolds/confusion (manual transcript check). Copy iterates until the
gate passes; results recorded alongside the cards.

### P3. Automatic residue collection (no model in the loop)

Runs per top-level session (`isSubagentSession()`), local exec envs only:
**once at open+`laneSweepDelay` (10m)** and **once at close** after the
session's own P0 disposal (foreign residue only — P0 already collected own
merged lanes). Open timer cancelled if close begins first; close **joins**
an in-flight open pass. **The close pass is time-boxed**
(`laneClosePassBudget`, 30s): lanes not reached are skipped with one
warning — unbounded foreign git work must not block daemon shutdown
(rev-7 finding K3); a pass killed mid-lane leaves at most one lane's
remnants, which the sweep-2 arm below reclaims later.

Scope: delegate lanes only (sidecar `DelegateID` provenance), unlocked,
collectible per D0, this repo's worktree root. Implementation parameterizes
**both** prune sweeps (rev-7 finding L3): sweep 1 (worktree-list-driven
collection) **and sweep 2's orphan-sidecar reconciliation** restricted to
delegate sidecars — without it, a crash/skip between `worktree remove` and
`branch -D` leaves branch+sidecar orphans invisible to a list-driven sweep
forever, resurrecting the model-must-run-prune dependency. Skip-and-continue
per lane; no disposed-mark attempt (foreign store; stat net covers
revival); never re-lock on failure.

**Grace = sidecar mtime** (`SidecarAge` pattern): skip lanes whose sidecar
mtime is within `laneGrace` (30m); every close-KEEP path touches the
sidecar before unlocking (P0). **Cross-collector concurrency argument,
stated** (rev-7 finding L8): two passes (same daemon or two processes on a
shared state dir) may race on one lane; every mutating step is a single git
op that exactly one side wins (`worktree remove`, `branch -D`,
sidecar unlink), losers treat refusal/ENOENT as skip-and-continue, and
neither side marks foreign stores — so the worst interleaving is one side
doing the other's remnant cleanup. Test 28 exercises two concurrent passes.

**Session resume re-locks its own undisposed lanes** via `EvDelegateRevive`
(post-init step — needs the jobstore; `resumeWorktreeReentry` runs
pre-init). Failure: warning + one retry (P3 open timer, or a dedicated
one-shot for restored subagent coordinators); still-failed → warning naming
the lane.

**Close-path late-dirty downgrade unlocks-and-keeps**; that lane is dirty,
so P3/prune skip it — unlocking's benefit is manual collection.

## Phase 2 (deferred backstop): mid-life background sweep

Rev-3 design preserved at `75c7b086`; P1's factored mechanics, gate, and
guards are the code it would reuse. Build trigger: live sessions still
accumulating collectible lanes past an agreed threshold despite P1/P2 —
including via the accepted P3 cadence gap and squash-merge blindness.

## Constants

| name | value | why |
|---|---|---|
| `laneSweepDelay` | 10m | P3 open-pass delay past revival races; re-lock retry point |
| `laneGrace` | 30m | sidecar-mtime grace covering close and hand-off windows |
| `laneClosePassBudget` | 30s | close must not block shutdown on foreign git work |

## Testing (TDD, red → green per case)

P0 unit:

1. Close-time disposal collects a **merged-with-commits** lane (ancestry and
   cherry arms): removed, marked Disposed in own store, branch+sidecar gone.
2. Unchanged lane at close → still collected (regression).
3. Unmerged / dirty / state-unverifiable lanes at close → KEPT, **sidecar
   touched before unlock on all three paths**, then unlocked.
4. Resume after P0 disposal → clear disposed refusal (own-store mark).

P1 unit (real-git fixtures):

5. Live dispose: merged (both arms) → disposed end-to-end.
6. Unchanged, empty/deleted merge_target → disposed (Unchanged arm).
7. Clean+unmerged → refused; `force` → disposed; `force_dirty` alone →
   refused. Dirty → refused; `force_dirty` → disposed; `force` alone →
   refused.
8. Running/driving delegate → refused (pre-gate).
9. Dispose-gate: set under `sub.mu`; concurrent notification can't launch a
   drive; retained `delegate_send` busy/retryable; **every post-gate refusal
   exit (steps 5, 6, 7, 8) clears the gate** — parameterized over exits.
10. Grandchild shell in a retained child's job manager, rooted in the lane →
    refused (recursive walk; parent-only scan fails red first).
11. Grandchild delegate / undelivered attention → refused. Armed watch →
    refused, named. Pending watch-send (incl. child-manager, incl. restored
    post-budget-clear) → refused.
12. Retained quiescent child → gated, evicted, removed; `delegate_send`
    disposed refusal (restore path); retained-path check vs constructed
    state; copy doesn't claim "at session close".
13. Remove-refused: present+dirty → re-locked own marker, KEPT; gone →
    marked + remnants cleaned + reported disposed; re-lock failure warns.
14. Half-removed residue (dir gone): control env via sidecar
    `OriginalRoot` (no lane path to walk); merged tip → mark+branch-D+
    sidecar-delete; unmerged → refused. **Already-Disposed record →
    idempotent remnant cleanup, reported already-disposed.** Unknown id →
    invalid_request.
15. Dispose-turn vs own-close: close joins the in-flight-dispose WG before
    `drainForClose`; no double remove, no false "re-locked" note, gated
    child not yanked mid-step-7.
16. Foreign / session lock marker → refused.
17. Availability: leaf → no tool; non-isolated coordinator → full tool;
    isolated coordinator → dispose-only variant (schema lists only
    `dispose`); sibling-lane ownership refusal; `delegate` copy updated.
18. `remove` blocked only by a retained terminal delegate → `retained —
    idle` label + dispose suggestion.
19. Sandboxed: control-env dance; unsatisfiable → clear error.
20. Doctor/listing: mid-life Disposed delegate shows non-resumable
    (fold/DelegateRecord).

P2: nudge iff op available AND owner; absent in ancestors/leaves/
non-isolated; copy pinned; no git at render. Live eval per the falsifiable
gate (3×3, thresholds above).

P3 unit (fake clock):

21. Unlocked merged foreign lane (sidecar mtime past grace) → collected at
    open pass; nothing before delay; timer cancelled by early close.
22. Close pass collects foreign residue; **time-box**: a slow lane exhausts
    the budget → remaining lanes skipped with one warning, close proceeds.
23. Close vs in-flight open pass → join first; no overlapping git ops.
24. KEEP sidecar touch ordering: collector observing mid-close sees locked
    or within-grace — including the state-unverifiable KEEP path.
25. Late-dirty downgraded lane: unlocked, P3 skips (dirty), manual
    `force_dirty` collects.
26. Managed (non-delegate) worktree unlocked+merged → untouched. Locked
    lanes → skipped. Child sessions / non-local envs → no P3.
27. Orphan branch+sidecar (crash between remove and branch-D; or
    budget-killed close pass) → reclaimed by the sweep-2 arm.
28. Two concurrent passes (two sessions) on one repo → every lane collected
    exactly once; losers skip on refusal/ENOENT; no error escalation.
29. Owner-resume re-lock: re-locks undisposed lanes; foreign P3 skips them;
    revival adopts; failed re-lock retried at the appropriate timer.
30. P3 remove/branch refusal → lane left unlocked, sweep continues; owner's
    later resume gets the stat-net refusal.

E2E: (a) live nudge flow — delegate completes, model merges, disposes;
(b) session A closes with a merged lane → **P0 collects it at A's close**
(the flagship path); (c) A closes keeping an unmerged lane, branch merges
later, session B opens → collected after delay+grace; resuming A's delegate
gets the appropriate refusal (disposed-mark for (b), stat-net for (c)).

## Files touched (est.)

- `agent/internal/tool/definitions.go` — `dispose` op; dispose-only variant;
  `delegate` isolation copy (~35 loc)
- `agent/session_tools_worktree.go` — dispose op + validation; in-handler
  gate; honest subagent label + dispose suggestion (~190 loc)
- `agent/session_worktree_close.go` — P0 predicate widening; factor
  mechanics; caller-dependent downgrade; touch-before-unlock on all KEEP
  paths (~70 loc)
- `agent/session_worktree_sweep.go` (new) — P3 parameterizing sweeps 1+2;
  open-timer + time-boxed close pass (~140 loc)
- `agent/jobs.go` — recursive tree-wide `liveWorkHandles` (~25 loc)
- `agent/subagents.go` / `agent/job_watch.go` — `sub.disposeGated`;
  drive-launch and retained-send checks (~30 loc)
- `agent/session_init.go` — post-init resume re-lock + coordinator retry;
  dispose-only surface wiring (~55 loc)
- `agent/session_lifecycle.go` — P3 timer/close-pass; joins (dispose WG +
  open pass) before `drainForClose` (~30 loc)
- `agent/job_delegate.go` — retained-path Disposed check; refusal copy;
  idempotent already-Disposed handling (~35 loc)
- `agent/internal/jobstore/fold.go` (+ doctor consumers) — Disposed in
  `FoldDelegates`/`DelegateRecord` (~25 loc)
- `agent/session_tools_jobs.go` / `agent/job_notify.go` — nudge surfaces
  (~30 loc)
- `tests/scenarios/worktree-dispose/` (new) — eval cards + recorded results
- `docs/worktrees.md` — §disposal (close now collects merged), `:108-112`
  (squash guidance → `dispose force`), `:121-123` (dispose-only surface),
  `:136-138` ("prune will offer" → P0/P3 semantics); native spec §9 (~doc)
- tests (~1000 loc)

## Review log

**Rev 1 → rev 2** (A, B) · **rev 2 → rev 3** (C, D) · **rev 3 → rev 4**
(Jesse: phased restructure) · **rev 4 → rev 5** (E, F) · **rev 5 → 5.1**
(Jesse: dispose-only surface) · **rev 5.1 → 6** (G, H + field #2) ·
**rev 6 → 7** (I, J) — details preserved in git history of this file.

**Rev 7 → rev 8** (K, L):

- **L1 (major):** rev-7's close pass and KEEP-touch nullified each other
  (own just-merged lanes always within grace at close) → **P0**: close-time
  disposal itself widened to D0-collectible; close pass narrowed to foreign
  residue. Also makes the flagship story honest: the 21 lanes collect at
  owner close with no model action (was: only via 21 dispose calls).
- **K1 (major):** the half-removed arm had no repo to run git in (lanes
  live outside any repo; nothing to walk up from) → control env via sidecar
  `OriginalRoot`.
- **K2 (major):** dispose-turn vs own-close was a new unguarded race (close
  cancels ctx, disposes, joins tool goroutines only later; both actors hold
  the same marker) → in-flight-dispose WG joined before `drainForClose`.
- **K3 (major):** close pass added unbounded synchronous foreign git work
  to shutdown → `laneClosePassBudget` (30s) time-box; budget-killed
  remnants covered by the sweep-2 arm.
- **L2 (major):** multi-commit squash merges permanently undetected —
  guarantee overstated → accepted limitation, documented; `dispose force`
  is the path; flagship-collectible claim qualified.
- **L3 (major):** remove→branch-D crash tail orphans branch+sidecar,
  invisible to a sweep-1-only P3 → P3 parameterizes sweep 2's
  orphan-sidecar reconciliation too.
- **L4 (minor):** mid-life Disposed invisible to `FoldDelegates`/doctor →
  fold + consumers scheduled.
- **L5 (minor):** eval gate referenced a harness that isn't a repo artifact
  and asserted an unfalsifiable negative → concrete scenario-card location,
  3×3 run matrix, numeric thresholds.
- **L6 (minor):** dispose on an already-Disposed record undefined though
  phase 1 produces it → idempotent remnant cleanup.
- **L7 (minor):** P2 has zero reach into pre-existing lanes → stated;
  P0 owns that class.
- **L8 (minor):** cross-collector concurrency argument unstated → stated
  (single-git-op atomicity + skip-on-refusal/ENOENT), test 28.
- **K4 (minor):** third KEEP path (state-unverifiable) skipped the touch →
  all KEEP paths touch; test 24.

Foundations positively verified across rounds 4–6: sweep1 reuse; pending-
send enumerability; `EvDelegateRevive` rows; forwarded-descriptor ownership;
lane-rooted coordinator control env; per-session registry variants; sidecar
machinery (`UpdateSidecar` truncating rewrite under held lock); render sites
seeing `ParentSessionID`; `MergeTarget` on delegate sidecars; `disposeGated`
reversal reachability incl. interruption (synchronous handler, in-memory
flag); recursive-walk lock ordering (no `jm.mu` nesting; `sub.mu → s.mu`
respected); mid-life Disposed events fold-safe (nothing un-disposes).
