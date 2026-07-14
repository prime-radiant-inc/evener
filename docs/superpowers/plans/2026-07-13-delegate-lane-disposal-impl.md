# Delegate-Lane Disposal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Delegate isolation worktree lanes get collected without depending on
the model running a tool: at owner close (P0), via a new model-invoked
`dispose` op (P1) with a completion nudge (P2), and by delayed sweeps over
cleanly-closed sessions' residue (P3).

**Architecture:** Widen close-time disposal to ancestry-merged lanes; add a
synchronous `dispose` operation on `manage_worktree` with a
quiescence/gate/evict/remove ladder factored from the close path; nudge via a
new `DisposalHint` result field; run a parameterized reuse of the prune sweeps
at open+10m and close. All safety decisions are specified in the DESIGN SPEC —
`docs/superpowers/plans/2026-07-13-auto-delegate-lane-disposal.md` (rev 10.3,
same directory as this plan). **Every task below names the spec section that
governs it; the spec text wins over this plan on any conflict.**

**Tech Stack:** Go; real-git test fixtures via the existing worktree test
harness (`agent/session_tools_worktree_*_test.go` patterns,
`agent/internal/worktree/*_test.go`); fake clock `agent/internal/clock`.

## Global Constraints

- TDD, red → green per step; commit after each green (small commits).
- Never hold `s.mu` across a git subprocess (`session_tools_worktree.go:505`).
- Documented lock order: `responseSideEffectsMu` before `s.mu`
  (`session.go:144`); `sub.mu` before `s.mu` (`subagents.go:898`).
- Automatic disposal predicates: **local branch refs only** (`refs/heads/*`),
  ancestry arm only — never `git cherry`, never `refs/remotes/*` evidence
  (spec §D0, §No-fetch invariant). This is the safety core.
- No `git fetch` anywhere in disposal paths.
- Non-force `git worktree remove` always (except explicit `force_dirty`).
- Constants `laneSweepDelay`=10m, `laneGrace`=30m, `laneClosePassBudget`=30s,
  `laneTailWarnThreshold`=50 — package vars (test-overridable, pattern:
  `agent/jobs.go:121 defaultCloseGrace`).
- Match surrounding code style; wire new events/warnings through the existing
  `s.emit(events.EventWarning, …)` shape.
- Run `go test ./agent/... ./agent/internal/...` before every commit; `make
  lint` before finishing a task (lint stops at first failing module).

---

## Task 0: Local-refs-only ancestry predicate (D0-auto evidence boundary)

Spec: §D0 "two tiers" + "Remote-tracking evidence is not auto-trustworthy" +
Implementation order item 0. THE safety fix — lands first.

Files: `agent/internal/worktree/predicates.go`, `predicates_test.go` (or the
existing test file for `Merged`).

- [ ] Read `worktree.Merged`, `checkMerged`, `disposableReason`'s callee chain
      (`predicates.go:96-258`) to map how target refs resolve (local branch +
      remote-tracking) and where the ancestry vs cherry arms split.
- [ ] Write failing tests for a new exported predicate `MergedAncestryLocal`
      (name it to match package conventions if a better fit exists):
      (a) lane merged into a local branch target → true;
      (b) lane whose merge_target resolves ONLY to `refs/remotes/*` → false
          even when ancestry against the tracking ref holds (simulated
          force-push fixture: stale tracking ref contains the lane tip,
          recreated upstream branch does not — assert false, and assert no
          `git cherry` was invoked (runner call log));
      (c) unmerged lane → false; (d) empty/deleted target → false.
- [ ] Implement: ancestry arm only, evidence restricted to `refs/heads/*`
      (mode parameter on the existing resolution or a post-filter — follow
      the existing code's shape).
- [ ] Green; commit.

## Task 1: Factor disposal mechanics; caller-dependent downgrade

Spec: §P1 step 8, §P0 (KEEP paths), rev-8 G1/rev-9 log.

Files: `agent/session_worktree_close.go`, `session_worktree_close_test.go`.

- [ ] Extract `disposeOneDelegateLane`'s unlock→remove→mark→branch-D→sidecar
      sequence into a reusable helper taking an options struct with a
      `downgrade` policy: `downgradeUnlockKeep` (close path: dead owner) vs
      `downgradeRelockKeep` (live dispose: re-lock own marker via
      `worktree.FormatDelegateMarker`). Pure refactor first: close path keeps
      TODAY's behavior (re-lock) in this task; the policy flip to
      unlock-keep happens in Task 2 with its own red→green.
- [ ] Existing close-path tests stay green (behavior unchanged this task).
- [ ] Add tests pinning both downgrade policies on the helper directly
      (fixture: make `git worktree remove` refuse via a dirty write between
      check and remove — the existing dirty-race test pattern).
- [ ] Green; commit.

## Task 2: P0 — close-time disposal collects ancestry-merged lanes

Spec: §P0 entire section. Tests 1, 2, 2a, 3, 5 in spec §Testing.

Files: `agent/session_worktree_close.go`, tests.

- [ ] Failing test: merged-with-commits lane (ancestry, local target) at close
      → removed, `EventDelegateDisposed` in own store, branch+sidecar gone;
      unchanged lane still collected.
- [ ] Failing test: cherry-only-merged lane at close → KEPT (runner log
      asserts no `git cherry`); remote-tracking-only target → KEPT.
- [ ] Failing test: unmerged / dirty / state-unverifiable → KEPT with
      **sidecar touch (UpdateSidecar rewrite) BEFORE unlock on all three
      paths**, then unlocked; late-dirty downgrade now unlock-and-keep
      (policy flip from Task 1) and ALSO touches.
- [ ] Failing test: KEEP warning copy = "not collected automatically
      (unmerged or squash-merged), dirty, or unverifiable" (replaces "with
      unmerged work", `session_worktree_close.go:92`).
- [ ] Failing test: resume after P0 disposal → disposed refusal (existing
      `assessDelegateResumability` path).
- [ ] Implement: predicate = `worktree.Unchanged` OR Task 0's predicate.
- [ ] Green; commit.

## Task 3: Close budget + budget-exempt tail

Spec: §P0 last bullet; §Constants; test 4.

Files: `agent/session_worktree_close.go`, `agent/session_lifecycle.go` (ctx
plumbing), tests.

- [ ] Add the four package-var constants (all four; others used later).
- [ ] Failing test (fake clock or stub runner with delays): close with many
      lanes exhausts `laneClosePassBudget` → remaining lanes get touch+unlock
      ONLY (no predicate evaluation, no remove), never left locked; one
      aggregated warning; a second aggregated warning when the tail exceeds
      `laneTailWarnThreshold`.
- [ ] Implement: deadline ctx minted by the close path, threaded to
      `disposeDelegateLanesAtClose`; ctx-aware git runner already exists
      (`newWorktreeGitRunner(ctx, …)`) — verify and use.
- [ ] Green; commit.

## Task 4: Dispose WG + close protocol (set-flag → cancel → join → drain)

Spec: §P1 "Dispose-turn vs own-close protocol"; Implementation order 1-3;
test 15.

Files: `agent/session.go` (WG field), `agent/session_lifecycle.go`, tests.

- [ ] Read `close()` (`session_lifecycle.go:80-180`) and the `sendersWG`
      idiom (`session.go:147`).
- [ ] Failing test: a goroutine holding the WG (simulated in-flight dispose)
      + concurrent `Close()` → close sets `closing`, cancels turn ctx, joins,
      THEN drains; an admission attempt after `closing` refuses; no deadlock
      (test with `-timeout`).
- [ ] Implement: `disposeWG` on Session; admission helper
      `beginDispose() bool` doing check-AND-Add under one `s.mu` hold;
      restructure close per spec steps 1-4 — first hold (`responseSideEffectsMu`
      then `s.mu`) sets `closing` and releases BOTH; cancel; join; reacquire
      pair; `drainForClose` onward unchanged.
- [ ] All existing lifecycle/close tests green; commit.

## Task 5: Tree-wide live-shell walk + honest labels + remove-refusal hint

Spec: §P1 step 3; problem §2 companion fixes; tests 11 (shell part), 18.

Files: `agent/subagents.go` (walk), `agent/session_tools_worktree.go`
(labels + refusal copy), tests.

- [ ] Failing test: retained child session with a running background shell in
      ITS job manager, cwd under a path → new Session-level
      `liveShellsUnderTree(path)` reports it (recursion releases each
      `sub.mu` before descending — `treeHasOutstandingWork` pattern,
      `session_jobtree_drain.go:104-127`); parent-only scan misses it (red
      first).
- [ ] Failing test: `liveWorkUnder` labels a retained terminal (not running,
      not driving) child `"(subagent, retained — idle)"`; running child keeps
      `"(subagent, running)"`.
- [ ] Failing test: `remove` refused ONLY because of retained terminal
      delegate children → refusal names them and suggests
      `manage_worktree op=dispose id=<dlg_…>`.
- [ ] Implement; green; commit.

## Task 6: `sub.disposeGated` + drive refusal + watchSendBusy refusal

Spec: §P1 step 4; tests 9, 10.

Files: `agent/subagents.go` (flag + drive check), `agent/job_delegate.go`
(retained-send busy refusal), tests.

- [ ] Failing test: with `sub.disposeGated` set (under `sub.mu`),
      `driveSubagentNotificationTurn` refuses to launch (notify fired
      concurrently); clearing the flag restores drives.
- [ ] Failing test: `delegate_send` to a gated retained delegate — plain call
      → refusal text says busy/retry; **watch-originated** call
      (`FromWatch:true`) → result classified `watchSendBusy` (pattern:
      `job_delegate.go:1532-1546`), frame retried at next boundary, NOT
      `dropWatchSend` (assert pending survives).
- [ ] Implement; green; commit.

## Task 7: `dispose` op — schema, dispatch, validation ladder (steps 1-6)

Spec: §P1 intro + steps 1-6 (incl. `OriginalRoot` control env, idempotent
already-Disposed, half-removed arm, closing gate via Task 4's
`beginDispose`); tests 6, 7, 8, 9, 13(refusal parts), 14, 16.

Files: `agent/internal/tool/definitions.go` (op enum + arg docs),
`agent/session_tools_worktree.go` (dispatch + validation), tests.

- [ ] Add `dispose` to the op enum + arg docs (`force`, `force_dirty`
      orthogonal — copy `remove`'s wording).
- [ ] Failing tests, one per validation rung (each red→green→commit):
      unknown id → invalid_request; forwarded (non-owned) record → refused;
      non-terminal/running job → refused; armed watch `send_to` → refused
      naming it; pending watch-send (own + retained-child manager) → refused;
      subtree: drain-check hit → refused; live grandchild shell (Task 5
      helper) → refused; foreign lock marker → refused; D0-model evaluate:
      ancestry AND cherry merged → proceeds; clean+unmerged → refused,
      `force` overrides, `force_dirty` alone does NOT; dirty → refused,
      `force_dirty` overrides, `force` alone does NOT; half-removed (lane dir
      gone, record+branch+sidecar remain) → judged via control env from
      sidecar `OriginalRoot`, merged → cleanup, unmerged → refused;
      already-Disposed → idempotent remnant cleanup, "already disposed"
      report; session closing → refused (Task 4 gate).
- [ ] Steps 1 record checks under `jm.mu` one-hold (recipe:
      `session_jobtree_drain.go:33-41`); in-memory session fields under
      `s.mu`; no mutex across git.
- [ ] Green; commit. (Execution steps 7-8 are Task 8 — this task's evaluate
      arm may return a stub "would dispose" error to keep tests honest,
      replaced in Task 8.)

## Task 8: `dispose` execution — gate, evict, cascade budget, remove ladder

Spec: §P1 steps 4 (gate set), 7, 8; nested-coordinator cascade; Implementation
order 4 (budget ownership sub-tasks a-c); tests 10(gate-clearing), 12, 12a,
13.

Files: `agent/session_tools_worktree.go`, `agent/session_lifecycle.go`
(cascade ctx honor), tests.

- [ ] Failing test: retained quiescent child → gate set, evicted
      (`close(false)` + owned-env full `Cleanup()`, removed from table),
      lane removed, `EventDelegateDisposed`, branch+sidecar gone;
      post-dispose `delegate_send` → disposed refusal.
- [ ] Failing test: every refusal exit after the gate (steps 5, 6) clears
      `disposeGated` (parameterize over exits); step-8 late-dirty KEEP after
      eviction does NOT attempt reversal — lane re-locked own marker,
      resumable via restore path (no Disposed mark).
- [ ] Failing test (depth-2 cascade): coordinator child with its own merged +
      unmerged grandchild lanes → dispose evicts it, grandchild merged lanes
      collected by the CHILD's close-time disposal, unmerged KEPT; the whole
      cascade honors ONE deadline ctx (inject tiny budget, assert a
      descendant's disposal did not mint a fresh budget — spec Impl-order
      4c).
- [ ] Failing test: remove-refused with lane GONE (concurrent collector
      simulated by deleting between unlock and remove) → Disposed mark +
      remnant cleanup + reported disposed; re-lock failure → warning naming
      lane.
- [ ] Implement using Task 1's factored helper (`downgradeRelockKeep`).
- [ ] Green; commit.

## Task 9: `delegate_send` Disposed hardening + copy

Spec: §P1 post-disposal paragraph; test 12 (constructed retained+Disposed).

Files: `agent/job_delegate.go`, tests.

- [ ] Failing test: constructed retained child + `rec.Disposed` →
      `delegate_send` refuses on the retained path too (today only the
      restore path checks — `job_delegate.go:682,841`).
- [ ] Failing test: refusal copy no longer claims "at session close"
      (`job_delegate.go:768`) — generalized wording.
- [ ] Implement; green; commit.

## Task 10: Fold/doctor Disposed visibility

Spec: §P1 "Mid-life Disposed visibility"; test 20.

Files: `agent/internal/jobstore/fold.go`, `agent/internal/jobstore/record.go`
(`DelegateRecord`), doctor consumers (`agent/doctor/tree.go`), tests.

- [ ] Failing test: `FoldDelegates` over a store with a mid-life
      `EventDelegateDisposed` → `DelegateRecord.Disposed == true`,
      `Resumable == false` (or equivalent field semantics); doctor tree
      rendering shows non-resumable.
- [ ] Implement (additive; nothing un-disposes); green; commit.

## Task 11: Availability — dispose-only surface for isolated coordinators

Spec: §P1 "Availability"; test 17.

Files: `agent/internal/tool/definitions.go` (dispose-only variant +
`delegate` copy fix at `:136`), `agent/session_init.go` (wiring at
`:770-780`), `agent/session_tools_worktree.go` (in-handler op gate), tests.

- [ ] Failing tests: leaf delegate (allowance 0) → no `manage_worktree`;
      non-isolated coordinator → full tool incl. dispose; isolated
      coordinator with allowance → registry serves the dispose-only variant
      (schema lists ONLY `dispose`), other ops refused in-handler with the
      isolation rationale; its dispose on an owned grandchild lane works, on
      a sibling's → ownership refusal. Cover BOTH spawn and restore
      (`initSessionState` is the shared chokepoint — verify with a restore
      test).
- [ ] Update the `delegate` tool isolation description.
- [ ] Green; commit.

## Task 12: P2 nudge — `DisposalHint` + notification text

Spec: §P2 (wording verbatim; surface mechanics; ownership gate). P2 tests.

Files: `agent/session_tools_jobs.go` (exported `DisposalHint string`
`json:"disposal_hint,omitempty"` on `delegateWorktreeToolResult`, populated
in `delegateWorktreeToolResultFrom`), `agent/job_notify.go` (text append),
tests.

- [ ] Failing tests: finished isolated delegate → inline result carries
      `disposal_hint` with the EXACT spec sentence; notification block
      carries the sentence; ancestor session receiving a forwarded report →
      NO hint; session without the dispose op (leaf) → NO hint; JSON
      round-trip emits the field (exported-field regression).
- [ ] No git invocation at render (assert on runner log).
- [ ] Green; commit.

## Task 13: P3 — sweep extraction + open-timer/close passes

Spec: §P3 entire section; Implementation order 0 consumer requirement;
tests 21-28, 30.

Files: `agent/session_tools_worktree.go` (extract sweeps 1+2 with
error-policy/grace/DelegateID-filter parameters; model-facing `prune`
behavior UNCHANGED — abort policy preserved),
`agent/session_worktree_sweep.go` (new), `agent/session_lifecycle.go`
(open timer + close pass hookup, joins), tests.

- [ ] Refactor first (green throughout): parameterize sweeps; existing prune
      tests unchanged.
- [ ] Failing tests (fake clock + injected constants): unlocked
      ancestry-merged foreign lane past grace → collected at open+delay;
      nothing before; timer cancelled by early close; cherry-only or
      remote-tracking-only → NEVER collected; close pass collects foreign
      residue within the shared budget; close joins in-flight open pass;
      managed worktrees untouched; locked lanes skipped; own-store record →
      Disposed mark appended, foreign → no mark attempt; orphan
      branch+sidecar → sweep-2 arm reclaims; failure → lane left UNLOCKED
      (never re-lock), skip-and-continue; two concurrent passes → collected
      exactly once, losers skip on refusal/ENOENT; top-level sessions only,
      local envs only.
- [ ] Green; commit.

## Task 14: Resume re-lock (post-init) + retry

Spec: §P3 "Session resume re-locks"; test 30 (re-lock parts).

Files: `agent/session_init.go` (post-init step — AFTER jobstore exists;
`resumeWorktreeReentry` runs pre-init and cannot host this), tests.

- [ ] Failing tests: resumed session with undisposed lanes → re-locked via
      `EvDelegateRevive` decisions (Unlocked→lock, OwnDelegate→adopt);
      foreign P3 then skips them; revival adopts its own re-locked lane;
      re-lock failure → warning + one retry at the P3 open timer (top-level)
      or a dedicated one-shot `laneSweepDelay` timer (restored subagent
      coordinator); still-failed → warning naming the lane; Disposed lanes
      NOT re-locked.
- [ ] Green; commit.

## Task 15: Docs

Spec: §Files touched docs bullet (four `docs/worktrees.md` updates named) +
native worktree tools spec §9.

- [ ] Update `docs/worktrees.md`: close now collects ancestry-merged lanes
      (explicit behavior change, not wording); `:108-112` squash guidance →
      `dispose` + `force`; `:121-123` delegate/manage_worktree claim →
      dispose-only surface; `:136-138` "prune will offer" → P0/P3 semantics.
- [ ] Update the native worktree tools spec §9 (disposal no longer
      close-only; `dispose` op; step-5 revival defenses unchanged).
- [ ] Docs-only commit.

## Task 16: E2E scenarios + eval cards

Spec: §Testing E2E (a)(b)(c); §P2 eval gate (cards only — the LIVE 3×3 run is
a separate human-triggered step, not this task).

Files: e2e test layer (follow `e2e` build-tag patterns in the repo),
`tests/scenarios/worktree-dispose/` (cards).

- [ ] E2E (b): session A closes with ancestry-merged lane → P0 collects;
      resume → disposed refusal. E2E (c): A closes keeping unmerged lane,
      branch merges, session B opens → collected after injected delay+grace;
      resume → stat-net refusal. E2E (a): scripted dispose flow (tool-level,
      not live-model).
- [ ] Write eval cards (merge card, squash card, resume-later card) per spec
      §P2 gate wording; record the gate thresholds in the cards' README.
- [ ] Green; commit.
