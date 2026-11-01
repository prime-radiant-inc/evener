# Tech-debt audit: agent-delegates

Slice: `agent/delegate_*.go`, `agent/subagent*.go`, `agent/subagents.go` (non-test). Delegate and subagent runtime, tree controller, delivery, attention, reclaim, stop. Worktree `.worktrees/debt-audit` at origin/main. Read-only audit; no tests were run.

## Overview and inventory

| File | Lines | funcs |
|---|---|---|
| delegate_runtime.go | 3568 | 100 |
| subagents.go | 2536 | 78 |
| delegate_tree_controller.go | 1256 | 49 |
| delegate_delivery.go | 1251 | 48 |
| delegate_tree_attention.go | 1062 | 47 |
| delegate_tree_start.go | 1059 | 35 |
| delegate_tree_stop.go | 1034 | 35 |
| delegate_tree_reclaim.go | 1014 | 29 |
| delegate_tree_restore.go | 900 | 22 |
| delegate_tree_intents.go | 825 | 23 |
| delegate_tree_finish.go | 635 | 42 |
| delegate_tree_watch.go | 490 | 15 |
| delegate_tree_steer.go | 459 | 18 |
| subagent_manager.go | 426 | 21 |
| other 10 small files | ~1600 | |
| **Total (non-test)** | **18,170** | ~590 |

Tests in the same filename families: 134 files, 61,718 lines (3.4x the production code). 12 files and 4,013 lines are `*covtest*` coverage files. About 2,641 of the 18,170 production lines are comment lines (14.5%).

Churn since 2026-08-25: delegate_runtime.go 87 commits, subagents.go 62, delegate_tree_controller.go 31. These are the hot spots.

Largest functions (measured start-of-func to next func): `prepareSubagentRunFromSelection` ~500 lines, `subagent.run` ~320, `delegateRuntime.send` ~305, `delegateRuntime.create` ~237, `restoreIdle` ~224, `stopSubtreeLocked` ~170, `Reconcile` ~150, `describe` ~148, `commitStart` ~143.

Shape of the problem. The design is coherent: a lock-protected controller holds durable journal state plus process-local claims, with Begin/Complete/Abort pairs. Most debt is (a) a leftover production code path that only tests exercise, (b) the same bookkeeping snippet copy-pasted dozens of times across the claim pairs, (c) three or four giant orchestration functions that restate each other, and (d) test-only seams living in production types. Nothing here argues for a rewrite. The fixes are deletions and extracting small helpers.

Product-doc check: S12 owns this slice (docs/product/subsystems.md row S12). None of the findings below change a recovery or preservation contract, except agent-delegates-17 which is a product decision for Jesse.

---

## Findings

### agent-delegates-1 (high): the "legacy in-process spawn" API is dead in production and kept alive by 22 test files

Locations:
- agent/subagents.go:727-757 `spawnAgent`
- agent/subagents.go:759-783 `prepareSubagentRun`, `prepareSubagentRunWithModelSelection`
- agent/subagents.go:1357-1373 `trackAndLaunchPreparedSubagent`
- agent/subagents.go:1439-1494 `sendInput`, `startOrSteerSubagentRun`
- agent/subagents.go:1732-1752 `cancelAgent`
- agent/tree_counter.go:202-206 `releasePreparedTreeSlot`
- agent/delegate_user_stop.go:80-84 comment: "cancelAgent's own cancel (unwired in production today)"
- agent/subagents.go:96 and :1705, agent/subagent_manager.go:363-376 `resultConsumed`

Evidence: `git grep` for `spawnAgent`, `.sendInput(`, `cancelAgent`, `startOrSteerSubagentRun`, `trackAndLaunchPreparedSubagent` outside `_test.go` returns only definitions, comments and each other. The only production entry to a child run is `delegateRuntime.create/send`. The code even says so: subagents.go:742 "The legacy in-process spawn mints no delegate job" and delegate_user_stop.go:82 "unwired in production today". 22 test files still call `spawnAgent`/`prepareSubagentRun` (for example plugin_agents_integration_test.go has 11 call sites, sandbox_delegate_create_test.go 5).

A second-order effect: `resultConsumed` is only set true by `cancelAgent` (subagents.go:1748). `reserveSlot` (subagent_manager.go:363,378) treats a terminal child as reclaimable only when `closed || consumed`. In production nothing sets `consumed`, so the retained-terminal cap can only be relieved by `closed`. I did not trace every path that sets `closed`, so I cannot say whether this strands slots today, but the `consumed` half of the predicate is unreachable by real use. It should be verified before deleting.

Impact: ~200 lines of production code plus config plumbing (`SessionConfig.spawn` comments at session_config.go:288, 800, 832, 918 still describe "set by spawnAgent") that new contributors read as live. Tests of this path prove nothing about the real delegate path.

Fix plan:
1. For each of the 22 test files, switch the setup to the real `delegateRuntime.create` path (the `newDelegateControllerTestHarness`/scripted-provider helpers already used by delegate_*_test.go). Where a test only needs a child `Session`, call `prepareStableDelegateRun`.
2. Delete `spawnAgent`, `sendInput`, `startOrSteerSubagentRun`, `cancelAgent`, `prepareSubagentRun`, `prepareSubagentRunWithModelSelection`, `trackAndLaunchPreparedSubagent`, `releasePreparedTreeSlot`, the `subagentAfterPrepare` hook, and `resultConsumed` (after verifying the retention predicate, replace `closed || consumed` with `closed`).
3. Fix the comments in session_config.go and delegate_user_stop.go.
4. Update docs/product/subsystems.md only if it names these (it does not appear to).

Size: ~220 lines deleted from production, test migration ~300-500 lines touched. Risk: medium, mostly in the test migration; behavior in production is unchanged. Do the `resultConsumed` retention change as its own commit with a test that fills the retained-terminal cap with unclosed terminal delegates.

---

### agent-delegates-2 (medium): dead machinery with `nolint` and zero production callers

Locations:
- agent/subagent_manager.go:157-176 `beginReconstructionSideEffects` (`//nolint:unused // retained for tagged restoration side-effect state-machine owners.`)
- agent/subagent_manager.go:28-29, 46, 178-184, 278 `activeRestoreSideEffects`, `restoreSideEffectsCond`, `restoreSideEffectsWait`, `waitForReconstructionSideEffects`, `closeOrRestoreResiduePending`
- agent/session_lifecycle.go:572 (the one caller of the wait)
- agent/delegate_tree_stop.go:575-619 `CloseResumability` (~45 lines)
- agent/delegate_tree_start.go:930-941 `inputPersistFailureBatch`

Evidence: `git grep` over all non-test Go files: `beginReconstructionSideEffects` has no caller, so `activeRestoreSideEffects` is never incremented in production; the wait loop in `waitForReconstructionSideEffects` can never block and `restoreSideEffectsWait` is a package-level hook variable for a test to intercept it. `CloseResumability` has 18 test references and zero non-test callers (the production resumability close goes through `appendResumabilityClosureLocked` inside `FailCommittedStart`). `inputPersistFailureBatch` is superseded by `startInputFailureBatch`.

Impact: a barrier that looks like it protects restore-versus-close races but does nothing; reviewers must reason about it. The `nolint:unused` comment refers to "tagged restoration owners" that do not exist in the tree.

Fix plan: delete the functions and fields above, the `restoreSideEffectsWait` var and the wait call in session_lifecycle.go:572; update the 4 test references to `beginReconstructionSideEffects` and the 18 to `CloseResumability` (delete tests that only exercise the dead function, keep any that pin behavior of the shared helpers). If the barrier is wanted in future, it comes back with a caller.

Size: ~110 lines production removed, ~300 test lines. Risk: low.

---

### agent-delegates-3 (low): production wrappers and seams that exist only for tests

Locations (production definition; all have zero non-test callers):
- agent/delegate_tree_finish.go:70-74 `BeginSettlement`; :76-84 `BeginFinalization` (only `BeginRunFinalization` at subagents.go:1908 is used; BeginSettlement is called from 7 test sites, BeginFinalization from 12)
- agent/delegate_runtime.go:133-135 `ReportActivity` (wraps `ReportActivityPhase(..., "")`)
- agent/delegate_tree_controller.go:457 `AuthorizeMutation`, :975 `completionSnapshot`, :1252 `capacityInUse`
- agent/delegate_tree_attention.go:208 `noteDelegateAttention`, :679 `nextIdleDelegateAttention`
- agent/subagents.go:399 `isRootOnlyJobPresenceTool`
- agent/delegate_artifacts.go:18 `delegateSessionDir`, :16 `delegateArtifactsSubdir`
- agent/subagents.go:2205-2208 the nil-session fallback in `(*subagent).stableDelegateFinish`, and the package-level `stableDelegateFinish` (called from tests only)
- agent/subagent_tally.go:54-63 `SubagentTallyForTest` plus the field `subagentTallyForTest` read on the hot path in `SubagentTally` (line 45)

Evidence: `git grep` outside `_test.go` shows definitions only. `SubagentTallyForTest` is an exported production method whose comment admits it exists so tests "outside package agent" can fake a tally.

Impact: small each, but they widen the API a reader must understand, and the settlement trio (`BeginSettlement` -> `BeginFinalization` -> `BeginRunFinalization`) looks like three live variants of one operation.

Fix plan: move each wrapper into a `_test.go` helper (or inline at the few call sites) and delete from production. For `SubagentTallyForTest`, have the daemon tests seed a real controller aggregate via an existing test harness or an `export_test.go`-style file; remove the field from `Session`. Keep `delegateRunTerminal` (genuinely shared).

Size: ~90 lines production deleted, ~60 added in test helpers. Risk: low.

---

### agent-delegates-4 (high): the "untrack from stop state and signal progress" snippet is copy-pasted ~14 times

Locations (each is `delete(c.X, token)` followed by `if c.stop != nil { if _, tracked := c.stop.X[token]; tracked { delete(c.stop.X, token); c.signalStopProgressLocked() } }`):
- agent/delegate_tree_work.go:75-82, 94-101
- agent/delegate_tree_steer.go:292-299 `releaseSteeringClaimLocked`, 302-310 `releaseModelClaimLocked`
- agent/delegate_delivery.go:321-326, 331-337, 369-375 (three copies inside `CompleteDelivery`)
- agent/delegate_tree_start.go:723, 832 (reservation release)
- agent/delegate_tree_finish.go:440 (`c.stop.active[lease]`)
- agent/delegate_tree_reclaim.go and others use the same `signalStopProgressLocked()`; `git grep -c signalStopProgressLocked` shows 20 call sites, `if _, tracked := c.stop.` shows 14.

Evidence: the same 6-line block with only the map name changed; `CompleteDelivery` (delegate_delivery.go:306-392) repeats it three times in one function. `AbortShellWork` and `ReportShellFinished` (delegate_tree_work.go:67-103) are character-for-character the same teardown.

Impact: every new claim type needs the block, and forgetting `signalStopProgressLocked()` stalls a stop drain forever (the drain waits on this signal in `waitForDelegateStopProgress`). This is the most likely place for a future stuck-stop bug, and the product contract (S12 stop must return to useful operation) depends on it.

Fix plan: add one helper in delegate_tree_stop.go, for example `untrackStopLocked[K comparable](tracked map[K]V, key K)` that does the check-delete-signal, generic over the stop map. Replace the 14 blocks. Behavior-preserving; the existing stop tests (delegate_tree_stop_test.go, delegate_stop_attention_scope_test.go) are the guard. Add one table test that each claim type's Abort and Complete wakes a waiting stop drain (a real behavior test, not a mock).

Size: ~-80 lines, +15. Risk: low.

---

### agent-delegates-5 (low): `CommitShellWork` has two identical branches

Location: agent/delegate_tree_work.go:40-65

Evidence: the `if c.closing || c.stopCoversLocked(...) { ... return true, nil }` branch and the fall-through both execute the same five statements (`work.jobID = ...; work.cancel = ...; work.committed = true; if c.stopCoversLocked { c.stop.work[token] = ... }; c.evidenceVersion++`). Only the return value differs. The inner `if c.stopCoversLocked(...)` in the first branch is redundant with the outer condition's own check.

Fix plan: assign once, then `return c.closing || covered, nil`. Pin with a test of both return values (closing; stop-covered; neither). Size -12 lines. Risk: low.

---

### agent-delegates-6 (medium): `CompleteSteerPersistence` copies the steer-admission block

Location: agent/delegate_tree_steer.go:116-179

Evidence: lines ~136-157 (the `stopFenced` recovery branch) and ~167-177 (the normal branch) both do: append `delegateSteeringAdmission`, `activityAt` update, the "Mirror ReportActivityPhase" quiet-cadence rearm with an identical comment, `productiveActivityAt` update, `evidenceVersion++`, and `capturedPlanLocked`. The only difference is `carriesAcrossGeneration: true`.

Impact: the comment says "Mirror ReportActivityPhase", so this is also a third hand-copy of activity-clock logic that lives in `ReportActivityPhase`. A change to the activity rules has to be made in three places.

Fix plan: extract `noteSteerAdmittedLocked(live, entry, carries bool)` and use it in both branches and have `ReportActivityPhase` share the clock-advance part. Guard with the existing steer tests plus one test that a steer during a covering stop still carries into the successor generation. Size -25 lines. Risk: low.

---

### agent-delegates-7 (medium): twelve hand-rolled parent-chain walks with inconsistent cycle protection

Locations:
- agent/delegate_tree_controller.go:507-527 `delegateIsAncestorLocked` (visited set)
- agent/delegate_tree_controller.go:1032-1044 `ancestorFenceLocked` (no cycle guard)
- agent/delegate_tree_attention.go:51-69 `delegateAttentionProjectionEligible` (visited set)
- agent/delegate_tree_attention.go:743-753 `ancestorChainRestorableLocked` (no guard; dereferences `c.durable[ancestorID].Descriptor` directly in the loop post-statement)
- agent/delegate_tree_reclaim.go:132-145 (candidate ancestor), :408-420 `reclamationCoversLocked`
- agent/delegate_tree_stop.go:462-473 `delegateDepthLocked`
- agent/delegate_tree_restore.go:564-577 `nearestReachableAttentionAncestorLocked`
- agent/delegate_lane_handoff.go:111-135 recursion `laneHandoffCoversLocked` <-> `laneHandoffCoveredByAncestorLocked`
- agent/delegate_tree_controller.go:808 and delegate_tree_watch.go:65 (single-hop reads)

Evidence: `grep 'Descriptor.ParentDelegateID$'` shows 12 assignment sites in the slice. Two walks protect against cycles, the others do not. `ancestorChainRestorableLocked`'s loop post-statement `c.durable[ancestorID].Descriptor.ParentDelegateID` would nil-deref if an ancestor row vanished mid-walk (the loop body's `idleRestorableLocked` check returns false first for a missing row, so it is safe today, but only by ordering).

Impact: the "is this delegate covered/fenced by an ancestor" family is the core of stop/handoff/restore semantics, and each variant re-derives termination. A durable-state cycle (the fold is supposed to prevent one) would hang a controller-lock-holding loop in the unguarded variants.

Fix plan: add `func (c *delegateTreeController) ancestorsLocked(id string) iter.Seq[string]` (or a `walkAncestorsLocked(id, func(id string, a *Aggregate) bool)`) in delegate_tree_controller.go next to `subtreeMembersLocked`, with a bounded visited set and nil-row stop. A pure `delegatestore.State` variant (for `delegateAttentionProjectionEligible` and `nearestReachableAttentionAncestorLocked`, which take a `State` not a controller) belongs in `agent/internal/delegatestore`. Rewrite each caller as a loop over the iterator. Guard with a table test over a small synthetic tree plus a cycle fixture, run against every caller.

Size: -80 lines, +40. Risk: medium (lock-held code, subtle predicates); do it one caller per commit.

---

### agent-delegates-8 (medium): `delegateTreeController` is a 55-field state machine with a hand-written claim protocol repeated per claim type

Locations: agent/delegate_tree_controller.go:44-116 (struct), and every `Begin*/Complete*/Abort*` pair: steer (delegate_tree_steer.go:82-190), model request (:193-290), start input (delegate_tree_start.go:431-497), settlement (delegate_tree_finish.go), shell work (delegate_tree_work.go), delivery (delegate_delivery.go:187-392), quiet attention (delegate_runtime.go:570-682), watch enqueue/delivery (delegate_tree_watch.go:80-200), runtime reclamation (delegate_tree_reclaim.go:69-243), start reservations (delegate_tree_start.go:608-740).

Evidence: 16 token-keyed claim maps on the struct (`reservations`, `inputClaims`, `steeringClaims`, `modelClaims`, `settlementClaims`, `work`, `deliveries`, `deliveryClaims`, `quietClaims`, `watchEnqueues`, `watchDeliveries`, `reclamations`, ...) each mirrored in `delegateStopState` (stop.steeringClaims, stop.modelClaims, stop.work, stop.deliveries, stop.starts, stop.watchEnqueues ...). `c.nextToken++` appears at 18 sites; `evidenceVersion++` at 76 production sites; `defer c.retirementChanged()` at 17 sites. Every Complete* starts with `claim == nil || c.X[claim.token] != claim -> errDelegateStaleLease`.

Impact: real maintenance drag, and it is where bugs like the one in agent-delegates-4 hide. But it is also load-bearing, heavily tested (the delegate test families are 61k lines), and the user-facing recovery contracts depend on its exact semantics. A rewrite would be a mistake.

Fix plan (incremental, after 4 and 7 land): introduce a small generic `claimSet[T]` in a new delegate_claims.go with `open(item) token`, `take(token, item) bool` (the nil/stale check), and `release(token)` that also performs the stop-state untrack and signal from finding 4. Migrate one claim type per PR starting with shell work and steering (smallest). Do not touch the reducer in delegate_tree_intents.go. Stop when the maps are encapsulated; do not try to unify the Begin/Complete semantics themselves, because each has distinct admission rules.

Size: -200 to -300 lines over 5-6 small PRs. Risk: medium-high if rushed; low per step.

---

### agent-delegates-9 (medium): `delegateRuntime.send` and its failure helpers restate each other

Locations: agent/delegate_runtime.go:1610-1913 (`send`, 305 lines), :1999-2096.

Evidence:
- The wait block (`waitCtx := ctx; if maxWaitMS > 0 { ... WithTimeout ...; observe := testOnly.delegateInlineWaitReady ... }; resolution := waitForDelegateInline(...); if resolution.fallback ... { result.TimedOut = true ... }`) appears twice: lines 1888-1905 and 2025-2043.
- The "overlay durable status/reason onto the populated result" block (`durableStatus := result.Status; durableReason := ...; populateStableDelegateSendResult(...); if durableReason != "" ...; if durableStatus != jobstore.StatusRunning ...; result.Action = "completed"; result.RunningInBackground = false`) appears twice: lines 2036-2044 and 2084-2093.
- Wrapper pairs that differ only by a trailing `nil` argument: `failStableSendStart` -> `failStableSendStartAfterDispatch` (1999-2005) and `stableSendFailureOutcome` -> `stableSendFailureOutcomeAfterDispatch` (2008-2012). Seven call sites in `send` pass a closure `afterDispatch`; each closure is just `finishRestore(x, err)` or `releaseFinishedGeneration`.
- Eleven exit points in `send` each rebuild the same 8-argument call.
- `send` also owns a ~25-line comment-and-code claim/guard dance (committedChildID claim, guarded drive guard, redrive in defer).

Impact: this is the most-edited function in the slice and a 300-line function is where the issue-numbered fixes (#940, #932, #3502, #3723, #184) accrete. Each comment paragraph records a past race.

Fix plan:
1. Extract `waitForInlineResult(ctx, waiter, maxWaitMS, result) (sendMessageResult, delegateInlineResolution, ok bool)` and use it in both places.
2. Extract `overlayDeliveredPacket(result *sendMessageResult, packet)`; use it in both.
3. Collapse the wrapper pairs: make `afterDispatch` always required (pass a no-op) or put it on a small `sendFailure` struct built once at the top of `send` (`started`, `waiter`, `maxWaitMS`, `ctx`) with a method `fail(cause, plans, after func())`. That removes the 8-argument repetition.
4. Split `send` into `steerIfIdle`, `claimStartWindow` (returns a release func), and `launchAndWait`.
All behavior-preserving; the delegate send tests (delegate_resource_runtime_test.go and delegate_send_*_test.go) are the guard.

Size: -90 lines. Risk: medium (race-sensitive ordering). Keep each extraction a separate commit.

---

### agent-delegates-10 (medium): the "hand the child to a run" sequence is repeated three times and has drifted

Locations:
- agent/delegate_runtime.go:875-903 `launchAcceptedDelegateAttention`
- agent/delegate_runtime.go:1859-1878 (inline in `send`)
- agent/delegate_runtime.go:2318-2324 (inline in `create`)
- dead twin: agent/subagents.go:1483-1486

Evidence: the first two do `fatalRunGated=false; resetSubagentForRunLocked; sub.driving=false; launchSubagentRun(...); startDelegateQuietWatchdog(...)` in that order. `create` calls `startDelegateQuietWatchdog` before `launchSubagentRun` (lines 2322 then 2324), the opposite order. Also `create` ends with `s.delegateController.emitDelegateUpdates(plans)` (line ~2315) while `send` uses `s.executeDelegateMutationPlans(plans)` (line 1851), which additionally runs attention cleanup and delivery plans from `plans.attention` / `plans.deliveries` (delegate_delivery.go:936-977). `CompleteStartInput` returns `plans.deliveries = replayDeliveriesForOwnerLocked(...)` (delegate_tree_start.go:493). So `create` drops any replay deliveries that `CompleteStartInput` planned for the new delegate, whereas `send` executes them.

I think that is benign for a brand-new delegate (it owns nothing yet), but I did not prove it, and the two paths should not differ by accident.

Impact: drift risk in the most race-prone handoff in the slice; a fix made to one copy (the comments cite #932/#940/#3723) silently misses the others.

Fix plan: one `func (s *Session) startRunForCommittedDelegate(sub, started, input, runOpts) ` that owns the reset, drive-flag handoff, launch, watchdog arm (pick one order, with a test that pins it), and plan execution via `executeDelegateMutationPlans`. Add a regression test that `create` executes planned deliveries for the new delegate if the plan contains any.

Size: -60 lines. Risk: medium; first write the pinning test.

---

### agent-delegates-11 (high): two independent code paths build a child Session from a descriptor

Locations:
- agent/subagents.go:848-1357 `prepareSubagentRunFromSelection` (~500 lines, mutates `subCfg.spawn.*` field by field; sets ~35 `spawn` fields at lines 876-1005)
- agent/subagents.go:812-846 `subagentConfigFromFrozenDescriptor` (copies ~25 parent fields)
- agent/delegate_runtime.go:2706-2928 `restoreIdle` (builds a `RestoreSessionConfig` literal with a ~40-field `spawnConfig` at ~2793-2830)

Evidence: both paths decide the same facts for a delegate Session: parent controller, retirement controller, root session id, owning delegate id, parent tool-call/item ids, steer and system-notification callbacks, job-event forwarding, depth, delegation allowance, shared task store, frozen role prompt and skills, tool-name ceiling, denied tool names, isolation, result schema, watch grant, job activity clock. In the fresh path `deniedToolNames` is derived through the tool policy; in restore it is `s.unavailableMemoryToolNames()`. Restore also repeats the profile/result-schema handling that `prepareStableDelegateRun` (subagents.go:785-817) does on the fresh path (JSON-decode `descriptor.ResultSchema`, `WithCommunicateOutputSchema`) and the environment-ownership dance (`shared := sharedRestoreEnvironment` vs `prepareSubagentEnvironmentFor`).

Impact: a new per-delegate setting must be added in both places in the right form. This is the classic "restored child differs from fresh child" bug generator, and the product promise is that a delegate restores equivalent to how it ran. A comment at subagents.go:812-818 already documents one such drift class (process-scoped fields).

Fix plan: define `func (s *Session) delegateChildSpawnConfig(descriptor delegatestore.Descriptor, lease delegateLease, sharedStore *task.TaskStore, ...) spawnConfig` as the single constructor of the delegate-specific `spawnConfig`, called from both the fresh path and `restoreIdle`. Add a differential test: fresh-prepare then freeze descriptor, restore from that descriptor, and compare the resulting `spawnConfig` (excluding live pointers) field by field. That test is the main deliverable because it keeps the two honest forever. Then split `prepareSubagentRunFromSelection` into: resolve tools/policy, build environment, build config, build session (4 functions of ~100 lines).

Size: -120 lines, +70 (test). Risk: medium-high; the differential test comes first and may find real existing drift (to be triaged as bugs, TDD).

---

### agent-delegates-12 (low): redundant nil defaults and nil-receiver guards the constructor already makes impossible

Locations:
- Defaults applied in `openDelegateTreeController` (agent/delegate_tree_controller.go:317-319 `now`, :332-334 `attentionOpen`) and then re-applied: delegate_delivery.go:518-520, delegate_shell_repair.go:50-55, delegate_tree_restore.go:531-533 and :762-764, delegate_tree_watch.go:438, 471-477, delegate_delivery.go:120-128 (`coldDelegateDeliveryReceiver.appendDelegateNotificationDurably`).
- Nil-receiver guards: `if c == nil` immediately after `func (c *delegateTreeController)` in 48 methods (for example delegate_lane_handoff.go:37, 100, 111, 124, 141, 158; delegate_tree_controller.go:465, 485, 551, 645...); 41 `delegateController == nil` checks outside tests.

Evidence: the controller value in `c.attentionOpen` / `c.now` can never be nil after construction, yet six call sites fall back to `transcript.OpenWriterForSession` and `time.Now().UTC()` again. Note `c.attentionOpen` is what the shell-repair path then passes into `snapshot.attentionOpen`, so the snapshot copies are non-nil too.

Impact: noise, and it hides where the real invariant lives ("a Session without a delegate tree has `delegateController == nil`; a controller is never nil once constructed"). Several nil-receiver guards are probably there because `Session.delegateController` can legitimately be nil on sessions that do not own or inherit a tree; those should be handled once at the Session boundary.

Fix plan: delete the six duplicate fallbacks (the receiver struct built at delegate_delivery.go:918 takes values from the controller). For the nil-receiver guards, add a single `func (s *Session) controller() *delegateTreeController` that callers use when a tree may be absent, and remove the receiver guards from methods only invoked through a non-nil controller. Do this by running the tests and removing guards in batches by file. Keep guards on the few methods genuinely invoked on possibly-nil receivers.

Size: -90 lines. Risk: low-medium (a removed guard could expose a latent nil deref; rely on the race-lane CI and the `-race` delegate tests).

---

### agent-delegates-13 (medium): delegate_runtime.go is a 3,568-line grab bag, including a process-global ticker registry

Locations: agent/delegate_runtime.go: controller methods (ReportActivity/Intent/Phase 133-245, BeginQuietAttention/CompleteQuietAttention 570-682), quiet-watch hub 299-552, attention drive 736-1230, owed-bootstrap restore 937-1230, send 1610-1913, create 2099-2334, restoreIdle 2706-2928, bootstrap 3229-3565.

Evidence: the file holds methods on `delegateTreeController`, on `Session`, and on `delegateRuntime`, spanning at least eight concerns. The quiet-watch hub (about 250 lines) multiplexes a ticker across leases per Session through a process-wide `delegateQuietWatchHubs` map keyed by `*Session` (line ~345-352) and a package-global `delegateQuietWatchTickDone atomic.Pointer` test hook (line 355) with `setQuietWatchTickDone`. The comment at 310-318 narrates history ("Previously each lease armed its own NewTicker...") and says the registry lives in this file "so Session's struct stays untouched". 540 of its lines are comments.

Impact: 87 commits in 2 months touch this one file, so merges collide, and review can't scope a change. The global registry keyed by `*Session` is exactly the kind of hidden coupling that makes tests order-dependent.

Fix plan:
1. Pure moves, no logic changes: `delegate_quiet_watch.go` (hub plus tick), `delegate_attention_drive.go` (driveStableDelegateAttention, owed bootstrap), `delegate_send.go`, `delegate_create.go`, `delegate_restore_runtime.go`, `delegate_bootstrap.go`. Move `ReportActivity*` and `*QuietAttention` controller methods into delegate_tree_*.go.
2. Then replace the global registry with a field on `Session` (or on the controller, which already exists per tree) holding the hub; delete `delegateQuietWatchTickDone` global in favor of the existing `cfg.testOnly` pattern, or better, make the tick injectable.
3. Remove the history narration.
Step 1 is mechanical and should land first, alone, with the `git diff --name-only`/function-set check from the history-rewrite gate.

Size: step 1 is ~0 net lines; step 2 about -60. Risk: step 1 low; step 2 medium.

---

### agent-delegates-14 (low): about 35 test-only hooks live in production types

Locations: `s.cfg.testOnly.*` is read at 38 sites in the slice (for example `delegateSendStartClaimed`, `delegateSendStartCommitted`, `delegateSendChildResolved`, `delegateAttentionBeforeDriveClaim`, `delegateAttentionBeforeGuardRelease`, `subagentBeforeSettlement`, `subagentBeforeGenerationReleased`, `idleTeardownMemberStarted`, `delegateInlineWaitReady`, ...); plus `testBeforeReconstructionWait` (subagent_manager.go:30), `delegateRestoreBeforeSideEffects` (Session field read at delegate_runtime.go:1823, 1016), `subagentTallyForTest` (finding 3), `delegateQuietWatchTickDone` (finding 13).

Evidence: 33 distinct names counted via `grep -o 'testOnly\.[a-zA-Z]*'`. Most are interleaving hooks inserted at one line of a production path so a test can park a goroutine there.

Impact: these are the price of testing real races without mocks, and the repo's testing guidance accepts scripted-provider tests below the LLM boundary, so I would not remove them wholesale. But hooks in the middle of `send`, `driveStableDelegateAttention` and `run` encode the current line-by-line order of the code as a test contract; the 12 covtest files that cite source line numbers show the same coupling.

Fix plan: no deletion. (a) Fold the three non-`testOnly` variants into `testOnly` so there is one mechanism. (b) When agent-delegates-9/10/11 extract helpers, move each hook with its step rather than leaving it behind. (c) Don't add new hooks without Jesse's approval.

Size: ~-20 lines. Risk: low.

---

### agent-delegates-15 (low): "resolve profile ref, apply cross-provider communicate overrides, resolve switch target" is copied five times

Locations:
- agent/subagent_model_selection.go:70-84 and :112-123 (the explicit-model block, identical in both branches of `selectSubagentModel`)
- agent/subagent_model_selection.go:137-142 (`resolvePluginAgentModel`)
- agent/session.go:1668-1672
- agent/job_delegate.go:140-144

Evidence: `resolveProfileForRef(base, ref)` followed by `if crossProvider { x = x.WithCommunicateOverridesFrom(base) }` appears at those five sites; in `selectSubagentModel` the next lines are also identical (`resolveModelSwitchTarget(s.client, resolved, s.id, *s.providerAdapterTimeout(), "", nil)` with the same `fmt.Errorf("model override %q: %w", ...)`).

Fix plan: add `func (s *Session) resolveProfileInheritingOverrides(base *provider.Profile, ref string) (*provider.Profile, error)` and `func (s *Session) resolveExplicitSubagentModel(base, explicit)`; the plugin-model-unavailable branch and the plain branch then share the call. Tests: existing subagent model selection tests (plugin agent fallback, explicit override). Size -35 lines. Risk: low.

---

### agent-delegates-16 (medium): three parallel terminal-failure paths outside the "single authority" reducer

Locations:
- agent/delegate_tree_intents.go:3-30 header: "single authority for every decision on the generation finish path"
- agent/delegate_tree_start.go:449-497 `CompleteStartInput` (failure branch), :499-553 `FailCommittedStart`, :555-590 `FailCommittedRestart`, :930-951 `inputPersistFailureBatch`/`startInputFailureBatch`/`committedStartFailureBatch`
- agent/delegate_tree_restore.go:768-826 (`runtime_lost` terminal built with `terminalFinishBatch`)

Evidence: `FailCommittedStart` and `FailCommittedRestart` are ~90% the same function (exact-lease check, stopping branch via `finishStoppedStartLocked`, running/recovery/ready guard, `committedStartFailureBatch`, append with `recoveryRequired` latch on failure, `generationFinishedPlansLocked`); the only difference is the extra `ResumabilityClosed` event and the claimed-for-close return. `startInputFailureBatch` and `committedStartFailureBatch` have identical bodies apart from their default reason strings. The `mu.Lock(); var cancel context.CancelFunc; defer func(){ mu.Unlock(); if cancel != nil { cancel() } }()` preamble appears in 8 functions (`grep 'var cancel context.CancelFunc'`: start.go 4, runtime.go 2, intents.go 1, restore.go 1).

Impact: the header promises one place for finish decisions, but start failures, restart failures, and runtime-lost finishes each decide their own latch, plans and release. The reducer's own comment (lines 27-30) admits the finishers "still drive their own entry paths".

Fix plan: (1) parameterize `FailCommittedRestart` as the base and make `FailCommittedStart` call it with an optional closure event; (2) merge the two batch builders into `startFailureBatch(lease, failure, defaultReason, defaultMessage)`; (3) extract `c.withDeferredCancel(func() (..., context.CancelFunc))` for the 8 lock/cancel preambles; (4) correct the header comment to say what is and is not routed through the reducer. Deliberately not folding them into the reducer now; do that only if a bug shows the split costs something.

Size: -90 lines. Risk: medium (finish-path latches); existing finish/start tests guard.

---

### agent-delegates-17 (low, needs Jesse): legacy delegate state refuses startup instead of recovering

Locations: agent/delegate_legacy_state.go:11-25 (called from agent/delegate_runtime.go:3236); docs/job-control.md:1432-1433, 1645-1647; docs/subagent-management/11-delegate-resource-model.md:128-131, 334; agent/delegate_legacy_state_covtest_test.go.

Evidence: `rejectLegacyDelegateState` returns `legacy_delegate_state: root session %q contains legacy delegate lifecycle state; use a fresh state root` and `legacy_delegate_watch_state ...`, and bootstrap aborts. The docs call this an intentional flag-day cutover ("no migration or alias").

Impact: AGENTS.md says not to treat fail-closed as a default design goal and to prefer automatic recovery and continued use of healthy parts of the product. Today an old root session whose jobs.jsonl has `delegate_created` or a `delegate` job-started row cannot be opened at all; the remedy is "use a fresh state root", which does not preserve the user's session. I have not checked how many such roots exist or whether the cutover happened long enough ago that none remain.

This is a consequential product choice with more than one reasonable answer (keep the refusal and delete the check once old roots are gone; open the root and quarantine only the legacy delegate rows so healthy work continues; or translate). It is a decision for Jesse, and it also must not become new backward-compatibility code without his approval.

Fix plan: ask Jesse. If the answer is "the cutover is old enough", delete the 78-line file, its covtest, the call site, and the three doc passages (cheapest outcome). If the answer is "recover", design the quarantine in the friction punchlist flow first.

Size: -150 lines on the delete path. Risk: product risk, not technical.

---

### agent-delegates-18 (low): delegate_artifacts.go is mostly thin wrappers and hardening for a trusted directory

Location: agent/delegate_artifacts.go:1-186

Evidence: `delegateArtifactsDir` and `delegateSessionDir` only wrap `schema.SessionArtifactsDir`/`schema.SessionDir` to re-prefix an error. `delegateSessionDir` and `delegateArtifactsSubdir` have no production callers (finding 3). `ensureDelegateArtifactsDir` carries a comment "stateDir is trusted layout", then guards three levels of directories against planted symlinks with a custom `mkdirVerifiedDir`, and `removeDelegateArtifacts` (about 70 lines) re-verifies each level with `Lstat`. `git grep` finds no other `Lstat`-verified mkdir helper in `agent/`, so this is a one-off scheme for one directory.

Impact: 186 lines for "make a directory, remove a directory". The hardening protects against an attacker who can already write inside the user's state directory.

Fix plan: confirm with Jesse that the threat model does not need it; if so, `ensure` becomes `os.MkdirAll(dir, 0o700)`, `remove` becomes `os.RemoveAll(dir)` plus the best-effort empty-parent removal, and the wrappers go. If the symlink defense is intended, move `mkdirVerifiedDir` next to `schema.SessionArtifactsDir` so other callers can share it. The tests in delegate_artifacts_test.go (the symlink cases) tell which it is.

Size: -110 lines on the simplify path. Risk: low-medium (security judgement).

---

### agent-delegates-19 (medium): test-suite debt in this slice: coverage-chasing files, source line numbers in comments, and phrase assertions

Locations: 12 `*covtest*` files in this slice totaling 4,013 lines (for example agent/delegate_tree_work_covtest_test.go, agent/delegate_legacy_state_covtest_test.go, agent/delegate_tree_finish_covtest_test.go, delegate_runtime_covtest{2,3,4}_test.go). 99 comment lines in them cite production line numbers ("covers the closing=true path (lines 22-23)", "(lines 39-45)"). 30 assertions use `strings.Contains(err.Error(), "...")` in delegate tests, for example delegate_lane_branch_disposal_test.go:112, 303, 342; delegate_delivery_branches_test.go:840; delegate_runtime_branches_test.go:604, 625; delegate_resource_shell_test.go:180.

Evidence: delegate_tree_work_covtest_test.go has `TestBeginShellWork_StaleLease` that only checks `err == nil` ("expected error for stale lease"), a coverage line, not behavior. It sits beside a production function (`CommitShellWork`) whose two identical branches (finding 5) it did not notice. The 30 phrase assertions violate AGENTS.md: "no test may check that copy, error messages ... match a hardcoded phrase".

Impact: line-number comments rot on the next edit; phrase assertions make wording changes fail tests; coverage-only tests inflate the 61.7k-line suite (3.4x production size) without protecting behavior.

Fix plan: (1) For each covtest file, keep tests that assert an observable outcome (sentinel error via `errors.Is`, state transition) and fold them into the owning `delegate_tree_<x>_test.go`; delete tests that exist only to touch a branch and assert `err != nil`. (2) Replace the 30 phrase checks with sentinel errors (`errors.Is`) or typed fields; where the production error is a bare `fmt.Errorf`, introduce a sentinel only if a behavior depends on it (otherwise assert only that an error occurred and that no state changed). (3) Don't add more covtest files. Do this alongside the production change that touches each area (findings 4, 5, 9) rather than as a standalone sweep.

Size: -1,500 to -2,500 test lines, 30 assertions rewritten. Risk: low for the suite as long as each deletion is justified by a behavior test that remains.

---

### agent-delegates-20 (low): the reducer-era comments reference past issue numbers and spec codes instead of stating the rule

Locations: agent/delegate_user_stop.go:24 "(S6)", agent/subagent_tally.go:20, 32 "(S3)", agent/delegate_tree_controller.go:92 "(kata tpb0)", agent/delegate_runtime.go:1591 etc. `(#940)`, `(#932)`, `(#3723)`, `(#3502)`, `(#3906, #3951)`, and 28 `#NNNN` references in the slice.

Evidence: `grep -o '#[0-9]\{3,5\}'` over non-test files in the slice returns 28 hits; "S6" and "S3" are spec section codes with no link; "kata tpb0" refers to a task id no reader can resolve. Some are useful as pointers, but several paragraphs lead with the ticket instead of the invariant.

Impact: minor, but these are the comments a new contributor reads first. The repo's comment rule says history belongs in commit messages.

Fix plan: when touching a function, rewrite its comment to state the invariant and delete the ticket/section code. Do not do a standalone sweep (whitespace/comment-only churn conflicts with the 87-commit hot file). Size: opportunistic. Risk: none.

---

## Recommended fix sequence

1. **agent-delegates-2, -3, -1** (delete dead code first). Start with 2 and 3 (small, no test migration risk), then 1 (the large test migration). Deleting first shrinks every later refactor. Verify the `resultConsumed` retention predicate in its own commit.
2. **agent-delegates-5 and -4** (tiny helper that removes the stuck-stop failure mode). Land the `untrack + signal` helper with its behavior test before touching claim structure.
3. **agent-delegates-6, -15, -12** (self-contained cleanups, each reviewable alone).
4. **agent-delegates-13 step 1** (pure file split of delegate_runtime.go, nothing else in the PR), which makes the next items reviewable by file.
5. **agent-delegates-7** (ancestor iterator), one caller per commit.
6. **agent-delegates-11** differential test first (it may surface real restore/fresh drift: treat those as bugs, TDD), then the shared `spawnConfig` constructor.
7. **agent-delegates-9 and -10** (`send`/`create` extraction, unified run launch), guarded by the pinning tests from the earlier steps.
8. **agent-delegates-16** (start/restart failure merge).
9. **agent-delegates-8** (claim-set encapsulation), one claim type per PR, only after 4 and 7 have made the pattern visible.
10. **agent-delegates-13 step 2, -14, -19, -20** opportunistically alongside the above, as each area is touched. Update docs/product/subsystems.md S12 row only if file names it links change (the split in step 4 renames `delegate runtime`'s anchor file).
11. **agent-delegates-17 and -18** are decisions for Jesse; ask before any work. Neither blocks the rest.
