# Delegate-lane disposal: dispose operation + nudge + automatic residue collection

**Status:** draft spec, rev 9.1 (seven adversarial review rounds, 7×2
competing reviewers; history in §Review log)
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
  - **P0:** close-time disposal collects **ancestry-merged** lanes, not just
    unchanged ones — the owner disposes its own residue at exit, with the
    durable mark, no model action (collects the flagship 21 at that
    session's close, assuming ancestry/single-commit merges; see
    limitations).
  - **P1:** a synchronous `dispose` primitive so a live session (model) can
    collect a lane the moment its work merges — including rebase/squash
    merges, under model judgment.
  - **P2:** a completion-time nudge toward P1.
  - **P3:** automatic collection of **cleanly-closed** sessions' residue by
    later sessions.
- **Phase 2 (specified, deferred):** the rev-3 background mid-life sweep as
  a backstop iff phase 1 measurably leaks (preserved at commit `75c7b086`).

## Non-goals / accepted limitations

- Automatic collection of managed (user/session) worktrees — `prune`
  territory, unchanged.
- Reclaiming lanes locked by live foreign sessions.
- **Crashed-and-abandoned sessions' lanes are collectable by nothing in
  this spec** (rev-8 finding N1): lanes are created locked, a crash leaves
  the git-registry lock in place, and every collector — P3 (unlocked-only),
  prune (`ActSkip` on any lock), P0 (needs a close) — skips them; only a
  resume (re-lock + eventual P0/dispose) or manual `git worktree unlock`
  reclaims them. Accepted for phase 1; a staleness-based lock reclaim is a
  phase-2+ candidate. The Goal wording says "cleanly-closed" deliberately.
- **Automatic collectors never use the cherry arm** (rev-8 finding M2):
  patch-equivalence proves the *changes* landed, not that the *commits* are
  reachable — auto-`branch -D` of a rebase/squash-merged lane would make
  the original commits gc-able with nobody in the loop. P0 and P3 therefore
  collect only clean + (`Unchanged` OR **ancestry**-merged) lanes;
  cherry-equivalent and multi-commit-squash lanes are collected only via
  model-judged `dispose` (P2 makes that likely). This also keeps P0 cheap
  (no `git cherry` patch-id walks at close — rev-8 finding M1).
  **Designated mitigation if squash-heavy workflows pile up** (decided
  rev 9.1, not built — no such workflow exists in current practice): the
  worktree and the branch ref are separable — an automatic collector may
  remove a cherry-merged lane's **worktree** (disk, registry entry, lock —
  the actual pileup) while **keeping the branch ref**, which preserves
  every commit. Prune's vocabulary already has this worktree-removed/
  branch-kept state (`WorktreeRemoved`). If the gap becomes real, build
  this; never auto-`branch -D` on cherry evidence.
- **Behavior change, acknowledged:** ancestry-merged lanes no longer survive
  the parent's close for post-close inspection (`docs/worktrees.md:136-138`
  promises changed lanes are "inspectable/mergeable/switch-able" after
  close). For the ancestry arm every commit is reachable from the target —
  only the checkout and branch ref disappear — but the workflow change is
  real and the doc rewrite must present it as such, not as wording. No
  opt-out in v1 (YAGNI; revisit on complaint).
- Live-session pileup between merges and close is bounded by P1/P2; P3
  cadence can lag a merge by hours. Both feed phase 2's trigger.
- Dirty and unmerged lanes of dead sessions persist for manual handling.
- Changing the lock decision core — `EvDelegateRevive` suffices (verified).

## Phase 1 design

### D0. Disposal predicates — two tiers (reused, not reinvented)

A lane is clean iff `worktree.CleanTree` holds. Then:

- **D0-auto** (P0, P3): clean AND (`worktree.Unchanged(run, lane, sc.BaseSHA)`
  OR ancestry-merged — `worktree.Merged`'s ancestry arm only).
- **D0-model** (P1 `dispose` without `force`): clean AND (`Unchanged` OR the
  full two-arm `disposableReason` — ancestry or single-commit
  cherry/patch-equivalence). The model's invocation is the judgment that
  patch-equivalent-but-unreachable commits may be discarded.

**No-fetch invariant** (rev-8 finding M5): `worktree.Merged` resolves only
local refs (`rev-parse`, `for-each-ref`; `predicates.go:96-130`) — no
network at close or in sweeps, ever. Stale tracking refs can only produce
false *unmerged* (lane kept — safe).

### P0. Close-time disposal collects ancestry-merged lanes

`disposeOneDelegateLane`'s predicate widens from `worktree.Unchanged` to
**D0-auto**. Ancestry-merged lanes' commits are reachable from the target,
so removal + `branch -D` loses nothing. Consequences:

- The owner appends `EventDelegateDisposed` in its **own** store — later
  resumes get the clear disposed refusal.
- The KEEP set shrinks to unmerged / cherry-only-merged / dirty /
  state-unverifiable lanes; the close-time KEEP warning copy is updated
  accordingly (today it says "with unmerged work",
  `session_worktree_close.go:92` — rev-8 finding M4).
- **Every KEEP path touches the sidecar before unlocking** (changed,
  late-dirty downgrade, state-unverifiable); the touch is what P3's grace
  keys on. The rev-8 claim "own merged lanes never reach KEEP" holds only
  up to the unverifiable path (rev-8 finding N4): a transiently-unverifiable
  merged lane is KEPT — if its touch also fails and it is out-of-grace, the
  session's own close pass may collect it; that is safe (D0-auto re-eval +
  stat net) and, since the record is in the collector's **own still-open
  store**, the close pass **does append the disposed mark when it owns the
  record** — the "no mark (foreign store)" rule is scoped to genuinely
  foreign lanes.
- **P0 shares the close-time budget**: `laneClosePassBudget` (30s) covers
  P0 disposal **and** the P3 close pass together (rev-8 finding M1 — rev 8
  budgeted only the latter). Lanes not reached are KEPT exactly as today
  (touch + unlock), with one warning. P0's git ops run before
  `env.Cleanup()` kills residual lane processes, so a stale `index.lock`
  can stall an op — the budget is the mitigation; each op is best-effort
  skip-on-error as today.

### P1. `dispose` operation on `manage_worktree` (new)

Synchronous, in-turn, on the owning session. Target: a delegate id; lane
path from the descriptor's `WorkingDir`, sidecar from `metaDirForLane`.
**Git control env from the sidecar's `OriginalRoot`** (lanes live outside
any repo; with the lane dir gone there is nothing to walk up from —
verified populated on every delegate sidecar since the codec's
introduction, and for nested coordinators it resolves to the *main* repo
root, never the parent's own disposable lane).

**Availability.** Leaf delegates: no tool (unchanged). Worktree-isolated
coordinators with a delegation allowance: **dispose-only surface** —
in-handler gate on `spawn.isolation` + a dispose-only tool-definition
variant; `delegate` tool isolation copy (`definitions.go:136`) updated.
Non-isolated coordinators: full tool.

**Steps:**

1. **Ownership + record quiescence.** Record in this session's jobstore,
   `ParentSessionID == s.id`. Latest job terminal, no running/queued
   follow-up — under `jm.mu`, snapshot + running-map in one hold.
   Already-Disposed record → **idempotent remnant cleanup** (branch if its
   tip judges D0-model-collectible, sidecar), report already-disposed.
   **Closing gate:** if the session is closing (`s.closing`), refuse —
   this is the WG admission gate, see the close protocol below.
2. **Delivery quiescence:** refuse on armed watches routing `send_to` this
   delegate or pending watch-sends targeting it (self + retained children).
3. **Subtree quiescence:** drain-style check (`treeHasOutstandingWork`) AND
   live shells tree-wide via the recursive `liveWorkHandles` walk across
   retained descendants. Companion fix: `liveWorkUnder`'s subagent label
   becomes `"(subagent, retained — idle)"` when quiescent.
4. **Dispose-gate the child:** `sub.disposeGated` under `sub.mu` after
   re-verifying `!running && !driving`; `driveSubagentNotificationTurn`
   refuses while set. **The retained-path refusal for a watch-originated
   send must be constructed as `watchSendBusy`** — the default
   `sendMessageFailed` stamps `watchSendHardFailure`, which permanently
   `dropWatchSend`s the frame (rev-8 finding N2; the one existing busy
   construction site, `job_delegate.go:1532-1546`, is the pattern; this
   lands in `job_delegate.go`, not `job_watch.go`). Reversal mandatory on
   every later refusal/failure exit.
5. **Foreign locks refused** per `worktree.ClassifyReason`/`Decide`.
6. **Evaluate:** D0-model collectible → proceed; unmerged → refuse, `force`
   overrides; dirty → refuse, `force_dirty` overrides (orthogonal); lane
   dir missing but record+branch+sidecar remain → judge branch tip via the
   `OriginalRoot` env; collectible → step 8 mark/branch-delete/
   sidecar-delete; unmerged → refuse naming the state.
7. **Evict the retained child:** close, remove from table,
   `DisposeSandboxScratch`.
8. **Unlock → `git worktree remove` → `EventDelegateDisposed` →
   `git branch -D` → delete sidecar.** Remove refused → stat: gone →
   mark + clean remnants + report disposed; present (late dirty) → re-lock
   own marker, KEPT; re-lock failure warns.

**Dispose-turn vs own-close protocol** (rev-8 findings M3/N3 — rev 8's
"join at the pre-`drainForClose` point" was a deadlock (join under the
`s.mu` hold that `drainForClose` runs in, while dispose steps need `s.mu`)
or, joined earlier, a reopened race). The protocol is
**set-flag → join → drain**, restructuring the top of `close()`:

1. lock `s.mu`, set `closing = true`, unlock;
2. **join the in-flight-dispose WaitGroup** (no locks held) — new
   `wg.Add`s are impossible because dispose's step 1 refuses when
   `closing` is set, checked under the same `s.mu`;
3. lock and proceed with `drainForClose` and the rest of close as today.

The WG field lives on `Session` (`agent/session.go`). Dispose observes ctx
cancellation between git ops; each op completes or fails atomically and
the reversal clause covers failure exits.

Post-disposal `delegate_send` takes the restore path where the Disposed
check lives; the check is also added to the retained path as
defense-in-depth; refusal copy generalized (today hardcodes "at session
close"). **Mid-life Disposed visibility:** `FoldDelegates`/`DelegateRecord`
gain Disposed handling so doctor/listings stop showing disposed delegates
as resumable (verified additive-safe).

**Sandboxed sessions:** control env as delegate revival
(`useDelegateWorktreeControlPolicy`); unsatisfiable → clear error.

### P2. Completion nudge

Unconditional wording, conditional surface. Rendered on both surfaces iff
the receiving session **has the op AND owns the delegate**
(`ParentSessionID == s.id`):

`When you're done with this delegate's work (e.g., after merging it),
dispose its worktree and branch: manage_worktree op=dispose id=<dlg_…>.`

No render-time git. Honest reach: completion-time only — pre-existing
completed delegates' lanes are P0's job (or spontaneous model action), not
P2's. Note the nudge is now the **primary** path for rebase/squash-merge
workflows (D0-auto excludes them), which raises its importance — the eval
gate below includes a squash-merge scenario.

**Eval gate (falsifiable):** scenario cards under
`tests/scenarios/worktree-dispose/`, run live against 3 providers × 3 runs.
Pass: (a) ≥2/3 runs per provider dispose after the scenario's merge step —
including the squash-merge card (dispose refuses, model uses `force` after
verifying the squash landed, or reports the situation; either is a pass,
silent abandonment is a fail); (b) 0/9 runs dispose or `force` a delegate
the scenario later resumes; (c) 0/9 scolds/confusion. Copy iterates until
the gate passes; results recorded alongside the cards.

### P3. Automatic residue collection (no model in the loop)

Runs per top-level session (`isSubagentSession()`), local exec envs only:
once at open+`laneSweepDelay` (10m), once at close after P0 (foreign
residue; shares `laneClosePassBudget` with P0). Open timer cancelled if
close begins first; close joins an in-flight open pass (in the set-flag →
join → drain sequence, alongside the dispose WG).

Scope: delegate lanes only (sidecar `DelegateID`), unlocked, **D0-auto**
collectible, this repo's worktree root. Implementation parameterizes both
prune sweeps — sweep 1 (list-driven collection) and sweep 2's
orphan-sidecar reconciliation restricted to delegate sidecars (crash
between remove and `branch -D` otherwise strands branch+sidecar orphans
forever); this requires extracting the sweeps for reuse
(`session_tools_worktree.go` refactor, budgeted). Skip-and-continue; the
disposed mark is appended **iff the lane's record is in the collector's own
store** (own transiently-unverifiable KEEPs), otherwise skipped (foreign
store; stat net covers revival); never re-lock on failure.

**Grace = sidecar mtime** (`SidecarAge` pattern): skip lanes within
`laneGrace` (30m); every KEEP path touches first. Cross-collector
concurrency: every mutating step is a single git op exactly one side wins;
losers treat refusal/ENOENT as skip; worst interleaving is one side doing
the other's remnant cleanup (test exercises two concurrent passes).
**Constants are test-overridable** (`laneSweepDelay`/`laneGrace`/
`laneClosePassBudget` injectable) so the E2E tier doesn't wait wall-clock
40 minutes (rev-8 finding N5).

**Session resume re-locks its own undisposed lanes** via `EvDelegateRevive`
(post-init step; needs the jobstore). Failure: warning + one retry (P3 open
timer, or a dedicated one-shot for restored subagent coordinators);
still-failed → warning naming the lane.

**Close-path late-dirty downgrade unlocks-and-keeps**; dirty lanes stay
manual-only.

## Phase 2 (deferred backstop): mid-life background sweep

Rev-3 design preserved at `75c7b086`. Build trigger: live sessions still
accumulating collectible lanes past an agreed threshold despite P1/P2 —
including via the P3 cadence gap, squash-merge blindness (first response:
the branch-preserving worktree-only collection designated in §Non-goals,
not the sweep), and abandoned-session lock pileups (which may also motivate
staleness-based lock reclaim).

## Constants

| name | value | why |
|---|---|---|
| `laneSweepDelay` | 10m | P3 open-pass delay past revival races; re-lock retry point |
| `laneGrace` | 30m | sidecar-mtime grace covering close and hand-off windows |
| `laneClosePassBudget` | 30s | bounds P0 **and** the P3 close pass together; shutdown must not block on git work |

All three test-overridable.

## Testing (TDD, red → green per case)

P0 unit:

1. Close collects an **ancestry-merged** lane: removed, marked Disposed in
   own store, branch+sidecar gone. Unchanged lane → still collected.
2. **Cherry-only-merged (rebase/squash) lane at close → KEPT** (auto tier
   never uses the cherry arm), sidecar touched, unlocked.
3. Unmerged / dirty / state-unverifiable → KEPT, touch-before-unlock on all
   paths; KEEP warning copy names the actual mix (not "unmerged work").
4. Budget: many-lane close exhausts `laneClosePassBudget` → remaining lanes
   KEPT as today with one warning; close proceeds.
5. Resume after P0 disposal → clear disposed refusal.

P1 unit:

6. Live dispose: ancestry-merged AND cherry-merged → disposed (D0-model).
7. Unchanged, empty/deleted merge_target → disposed.
8. Clean+unmerged → refused; `force` → disposed; `force_dirty` alone →
   refused. Dirty → refused; `force_dirty` → disposed; `force` alone →
   refused.
9. Running/driving delegate → refused (pre-gate).
10. Dispose-gate: set under `sub.mu`; concurrent notification can't launch
    a drive; **watch-originated retained send during the gate →
    `watchSendBusy` classification, frame retried at the next boundary,
    NOT dropped** (constructed-result test); every post-gate refusal exit
    clears the gate.
11. Grandchild shell in a retained child's manager → refused (recursive
    walk). Grandchild delegate / attention → refused. Armed watch →
    refused, named. Pending watch-send (child-manager; post-budget-clear
    restore) → refused.
12. Retained quiescent child → gated, evicted, removed; `delegate_send`
    disposed refusal (restore path); retained-path check vs constructed
    state; copy doesn't claim "at session close".
13. Remove-refused: present+dirty → re-locked, KEPT; gone → marked +
    remnants cleaned; re-lock failure warns.
14. Half-removed residue: control env via `OriginalRoot`; merged tip →
    cleanup; unmerged → refused. Already-Disposed → idempotent cleanup.
    Unknown id → invalid_request.
15. **Closing gate:** dispose admitted before `closing` completes fully
    (close joins); dispose attempted after `closing` → refused; no
    deadlock with the set-flag → join → drain sequence (regression test
    with a mid-dispose close).
16. Foreign / session lock marker → refused.
17. Availability: leaf/no tool; non-isolated coordinator/full;
    isolated coordinator/dispose-only variant; sibling-lane ownership
    refusal; `delegate` copy updated.
18. `remove` blocked by retained terminal delegate → `retained — idle`
    label + dispose suggestion.
19. Sandboxed: control-env dance; unsatisfiable → clear error.
20. Doctor/listing: mid-life Disposed shows non-resumable.

P2: nudge iff op available AND owner; copy pinned; no git at render. Live
eval per the gate incl. the squash-merge card.

P3 unit (fake clock + injected constants):

21. Unlocked ancestry-merged foreign lane past grace → collected at open
    pass; nothing before delay; timer cancelled by early close.
22. **Cherry-only-merged foreign lane → never collected by P3** (kept for
    model/manual action).
23. Close pass collects foreign residue within the shared budget; over
    budget → skip + one warning.
24. Close vs in-flight open pass → joined in the close protocol; no
    overlapping git ops.
25. Touch ordering: mid-close observer sees locked or within-grace (all
    KEEP paths); own-store mark appended when the collector owns the
    record.
26. Late-dirty downgraded lane: unlocked, P3 skips, `force_dirty` collects.
27. Managed worktree unlocked+merged → untouched. Locked lanes (incl. a
    crashed session's) → skipped. Child sessions / non-local envs → no P3.
28. Orphan branch+sidecar → reclaimed by the sweep-2 arm.
29. Two concurrent passes → each lane collected exactly once; losers skip.
30. Owner-resume re-lock; foreign P3 skips; revival adopts; retry at the
    appropriate timer.

E2E (constants injected, no wall-clock waits): (a) nudge flow — delegate
completes, model merges, disposes; (b) session A closes with an
ancestry-merged lane → P0 collects at A's close; (c) A closes keeping an
unmerged lane, branch merges later, session B opens → collected after
delay+grace; resume refusals: disposed-mark for (b), stat-net for (c).

## Files touched (est.)

- `agent/internal/tool/definitions.go` — `dispose` op; dispose-only variant;
  `delegate` copy (~35 loc)
- `agent/session_tools_worktree.go` — dispose op + validation; in-handler
  gate; label fix; **sweep extraction for reuse** (~230 loc)
- `agent/session_worktree_close.go` — P0 predicate (D0-auto); KEEP copy;
  touch-before-unlock; budget hookup (~75 loc)
- `agent/session_worktree_sweep.go` (new) — P3 over extracted sweeps 1+2;
  open timer + close pass; own-store mark rule (~150 loc)
- `agent/session.go` — in-flight-dispose WG field (~5 loc)
- `agent/jobs.go` — recursive tree-wide `liveWorkHandles` (~25 loc)
- `agent/subagents.go` / `agent/job_watch.go` — `sub.disposeGated`;
  drive-launch check (~20 loc)
- `agent/job_delegate.go` — retained-path Disposed check; refusal copy;
  idempotent already-Disposed; **`watchSendBusy` construction for the gate
  refusal** (~45 loc)
- `agent/session_init.go` — post-init resume re-lock + coordinator retry;
  dispose-only wiring (~55 loc)
- `agent/session_lifecycle.go` — **set-flag → join → drain restructure**;
  P3 timer/close-pass (~35 loc)
- `agent/internal/jobstore/fold.go` (+ doctor consumers) — Disposed in
  projections (~25 loc)
- `agent/session_tools_jobs.go` / `agent/job_notify.go` — nudge surfaces
  (~30 loc)
- `agent/internal/worktree/predicates.go` — ancestry-only entry point for
  D0-auto (~15 loc)
- `tests/scenarios/worktree-dispose/` (new) — eval cards incl. squash card
- `docs/worktrees.md` — close-collects-merged as an explicit behavior
  change; squash guidance → `dispose force`; dispose-only surface; "prune
  will offer" → P0/P3 semantics; native spec §9 (~doc)
- tests (~1100 loc)

## Review log

**Revs 1–8**: rounds 1–6 (reviewers A–L) plus Jesse's two restructures
(phased design rev 4; dispose-only surface rev 5.1) — details in this
file's git history (`main` branch, `1be4dce9..5f68e415`).

**Rev 8 → rev 9** (M, N):

- **M2 (major):** P0's "merged ⇒ reachable" rationale false for the cherry
  arm — auto-collection would gc rebase-merged lanes' original commits with
  nobody in the loop; plus an unacknowledged regression of the documented
  post-close-inspection contract → **D0 split into auto (ancestry-only) and
  model tiers**; behavior change documented honestly; cherry collection is
  model-judged `dispose` only.
- **M1 (major):** P0's cherry-arm `git cherry` walks were unbounded inside
  close — mooted by ancestry-only D0-auto; residual P0 cost brought under
  the shared `laneClosePassBudget`; index.lock hazard noted.
- **M3/N3 (major):** rev-8's WG join point was a deadlock (inside the
  `s.mu`/`drainForClose` hold) or a reopened race (before it, with no
  admission gate) → **set-flag → join → drain** close protocol with a
  `closing`-gated `wg.Add` under `s.mu`; WG field budgeted on `Session`.
- **N1 (major):** crashed-and-abandoned sessions' locked lanes are
  collectable by nothing (prune `ActSkip`s any lock) → accepted limitation,
  stated; Goal wording narrowed to cleanly-closed sessions; phase-2+
  staleness reclaim noted.
- **N2 (major):** the gate's "busy/retryable" refusal, implemented via the
  path's universal `sendMessageFailed` idiom, stamps `watchSendHardFailure`
  → permanent `dropWatchSend` → bespoke `watchSendBusy` construction
  specified, in `job_delegate.go`, with the existing single busy site as
  pattern; test 10.
- **N4 (minor):** "own merged lanes never reach KEEP" false for the
  unverifiable path; close pass isn't structurally foreign-only → claim
  corrected; own-store disposed mark scoped rule.
- **N5 (minor):** E2E (c) unrunnable against wall-clock mtime grace →
  constants made test-injectable.
- **N6 (minor):** files-touched gaps (Session WG field; sweep extraction
  refactor) → added.
- **M4 (minor):** KEEP warning copy ("unmerged work") now wrong → updated.
- **M5 (minor):** no-fetch property stated as a load-bearing invariant.

Foundations positively verified across rounds 4–7: sweep reuse; pending-
send enumerability; `EvDelegateRevive`; forwarded-descriptor ownership;
lane-rooted coordinator control env; per-session registry variants; sidecar
machinery; `OriginalRoot` on every delegate sidecar, resolving to the main
repo root even for nested coordinators; sweep-2 orphan coverage of the
mark→branch-D crash tail; fold-Disposed additive safety; no-fetch merged
checks; adopted-branch protection unaffected by P0 orphans.
