# Tech-debt audit: agent-jobs

Slice: `agent/job*.go` and `agent/jobs*.go` (non-test). Audited at `a6c0b9c561` (origin/main). Read-only audit, no tests were run.

## Overview and inventory

| File | Lines | Notes |
| --- | ---: | --- |
| `agent/job_watch.go` | 6,564 | 291 funcs, 259 commits (11 in the last 30 days) |
| `agent/jobs.go` | 2,364 | 99 funcs, 218 commits; `jobManager` struct at 64-252 |
| `agent/jobs_activity.go` | 2,005 | session activity tree (`evener/thread/jobs/list`) projector |
| `agent/jobs_nested.go` | 714 | descendant walks, stop, forwarding |
| `agent/job_shell.go` | 767 | `runShell`, delayed-shell lifecycle |
| `agent/jobs_activity_past.go` | 540 | historical activity loader/cache |
| `agent/job_transcript_read.go` | 508 | local job lookup for `read_transcript` |
| `agent/job_notify.go` | 332 | notification block rendering |
| `agent/job_delegate.go` | 286 | delegate arg/result types and grants |
| `agent/jobs_panel.go` | 155 | job output page / job get |
| `agent/job_output_digest.go` | 143 | inline shell digest |
| **Total (non-test)** | **14,378** | 2,861 are comment lines (20%) |

Tests for this slice (files matching `*job*_test.go` / `*watch*_test.go` in `agent/`): 148 files, 56,690 lines, about 4x the production code. 35 of the files are named `*cov*`, `*covtest*` or `*seed100*`.

Longest functions: `configureWatchWithHooks` 217 lines, `runShell` 188, `projectStableActivityDelegate` 130, `armFinalizedJob` 119, `deliverStableWatchSend` 93, `trimActivityTreeToFit` 92, `persistPendingWatchSend` 91, `createShell` 84.

Zero production callers (grep over all non-test Go in the repo, definitions excluded): 20+ functions, listed in finding 7. Six carry `//nolint:unused` with "retained for tagged fuzz owners".

Product-model notes that shaped the fix plans (from `AGENTS.md`, `docs/product/subsystems.md` row S12, `docs/product/session-activity.md`, `docs/product/friction.md` R03):

- S12 owns jobs, delegates and watches. Watch restore behavior (R03: active registrations end as `runtime_lost`, pending sends survive) is an open product item. Every refactor below must be behavior-preserving and must not touch that decision.
- Activity reads are domain reads (`session_activity*.go` plus the older `jobs_activity.go` tree that `evener/thread/jobs/list` still serves). Nothing here changes scope or subscription lifetimes.
- Docs must stay evergreen. Several docs cite `agent/job_watch.go` function homes and line anchors (finding 17).

Overall health: the logic is careful and heavily commented about concurrency invariants, but the slice has the classic LLM-accretion shape. One 6.5k-line file holds eight concerns, the same teardown/fire sequences are open-coded 5 to 8 times, production carries a lot of test-only code, and the test suite is dominated by coverage-driven files that assert on error phrasing.

---

## Findings

### agent-jobs-1 (high): `job_watch.go` is a 6,564-line, eight-concern file, including ~460 lines of Session child-drive logic that belongs elsewhere

Locations:
- `agent/job_watch.go:1-6564` (whole file)
- Session drive/stop-gate logic: `job_watch.go:5709-6172` (`drainPendingWatchSends`, `driveChildrenWithUndeliveredAttention` 5753, `redriveLiveChild` 5809, `driveChildIfNotStopGated` 5827, `renderUnreachableChildPendings` 5850, `childStopGated` 5944, `claimChildCommittedSendStart` 5983, `settleDrivenChildForwardedPendings` 6027, `retryRestoredPendingWatchSends` 6114)
- Existing homes for that logic: `agent/session_jobtree_drain.go` (50 funcs), `agent/subagents.go:1546` (`driveSubagentNotificationTurn`), `agent/subagents.go:1767` (`childFatalRunGated`)

Evidence: `grep -c '^func ' job_watch.go` is 291. Sections by line range: config/validate/install 195-1500; registry clear/events 1639-2365; projections (list/inspect/status/history) 2367-3270; fire paths (events, output, attach scan, timers) 3279-4270; send-rail delivery state machine 4268-5705; Session-level drain and child driving 5709-6170; frame rendering 6239-6486; notification routing 6488-6564. 259 commits touch the file, so every concern's change history collides. The Session methods at 5709-6172 operate on `subagent`/`childSessionID`, not on watch config; `childDriveGated` and `childStopGated` are about stop/fatal gating of children, a concern `subagents.go` and `session_jobtree_drain.go` already own.

Impact: merge conflicts and review cost on the hottest file in the repo; readers cannot tell which of 291 functions are entry points. Docs (`docs/architecture.md:330,389,423`) point at `job_watch.go` for drain functions that are really session-tree concerns.

Fix plan: pure mechanical split, same package, no signature changes, one commit per move so `git blame -C` follows. Target files: `watch_config.go` (args, validation, install), `watch_registry.go` (clear, events, history), `watch_projection.go`, `watch_fire.go`, `watch_send_rail.go`, `watch_frame.go`, and move 5709-6172 to `session_jobtree_drain.go` / `subagents.go`. Do this first: it makes every later finding reviewable. Update `docs/architecture.md` references and S12 row in `docs/product/subsystems.md` in the same change.

Size: ~0 net lines, ~6,500 lines moved. Risk: low (compiler-checked), but do it when no other PR is mid-flight on the file.

---

### agent-jobs-2 (high): the "fire a watch" funnel is hand-copied across six fire sites

Locations:
- `agent/job_watch.go:3279-3342` (`onSessionEvent`)
- `agent/job_watch.go:3576-3640` (`feedJobOutputWithProvenance`)
- `agent/job_watch.go:3704-3752` (`fireAttachScan`, two branches)
- `agent/job_watch.go:3761-3805` (`runTerminalCatchup`)
- `agent/job_watch.go:4167-4240` (`fireProgressTick`)
- `agent/job_watch.go:3836-3890` (`completeExpiredJobWatchesLocked`)

Evidence: each site repeats the same decision tree: `noteConditionFireLocked(cfg)` -> if refused skip -> `if cfg.send != nil { deliveries = append(..., jm.watchSendSnapshot(...).withSelfInfluence(jm.classifySelfInfluenceLocked(...))) } else { notifications = append(..., jm.watchNotificationFromWatch(...)); jm.countWatchDeliveryLocked(cfg) }` -> if `crossedBudget` track for teardown -> after unlock `enqueueWatchNotifications`, `recordWatchSendsAndKick`, `autoClearOverBudgetWatches`. They differ only in reason text, provenance source and lock timing, and the differences are documented in comments rather than in types. `fireAttachScan` even spells the funnel out twice (send and no-send), each with its own lock/unlock pair. The ordering contract ("sends recorded BEFORE the over-budget teardown", `onSessionEvent` comment at 3326-3335) is restated at each site and is easy to break in one of them.

Impact: any change to the breaker, self-influence, or delivery counting must be made six times; drift is already visible (`feedJobOutput` breaks out of the match loop on budget crossing, `onSessionEvent` cannot).

Fix plan: add a small `watchFireBatch` collector in the new `watch_fire.go`: `batch.fireLocked(cfg, trigger, jobID, root, provenance)` does the accepted/send/notify/count/overBudget logic under jm.mu; `batch.flush(jm)` does the three post-unlock steps in the one documented order. Convert sites one at a time, keeping existing tests (`job_watch_breaker_test.go`, `job_watch_condition_fire_guard_test.go`, `job_watch_events_test.go`, `watch_batch_seam_test.go`) as the guard; add one characterization test per site for "budget crossing in the middle of a chunk" before touching `feedJobOutput`. Net deletion, no behavior change. Depends on finding 1 (file split) only for convenience.

Size: about -150 lines. Risk: medium (lock ordering); do not merge sites that intentionally hold the lock differently (`fireProgressTick`'s rail split) without a test.

---

### agent-jobs-3 (high): the watch teardown transaction is open-coded in eight places with three near-identical rollback helper pairs

Locations:
- `agent/job_watch.go:1639-1682` (`clearWatch`)
- `agent/job_watch.go:1698-1771` (`clearWatchByIDMatchingWithReason`)
- `agent/job_watch.go:898-966` (replacement branch of `configureWatchWithHooks`)
- `agent/job_watch.go:2018-2037` (`autoClearWatchOverBudgetNotification`)
- `agent/jobs.go:778-852` (`closeRuntimeState`)
- `agent/jobs.go:913-949` (`abandonRunningJobs`)
- `agent/jobs.go:953-983` (`abandonRunningJob`)
- helpers: `job_watch.go:5361-5440` (`markWatchConfigSnapshotsRejectingLocked`, `markWatchConfigsRejectingLocked` 5585, `rollbackWatchConfigSnapshotsRejecting[Locked]` 5392/5398, `rollbackWatchConfigsRejecting[Locked]` 5593/5599, `rollbackWatchBudgetTeardown[Locked]` 2077/2085, `closeWatchConfigSnapshots` 5406, `detachWatchConfigSnapshots` 5414, `forgetDetachedWatchSendConfigsIfEmpty` 5607, `removeWatchSendTerminalSnapshots` 5635)

Evidence: counted call sites: `markWatchConfigSnapshotsRejectingLocked` 7, `rollbackWatchConfigsRejecting` 8, `forgetDetachedWatchSendConfigsIfEmpty` 7, `removeWatchSendTerminalSnapshots` 16, `appendWatchTeardownBatch` 5. Every site runs: snapshot targets under lock -> mark `rejectingDelivery` -> unlock -> `appendWatchTeardownBatch` -> on error roll back marks (re-locking) -> on success detach, remove snapshots, forget detached configs. Pairs like `rollbackWatchBudgetTeardown` / `rollbackWatchBudgetTeardownLocked` (2077-2092) are pass-through wrappers that exist only to add one `cfg.budgetTripped = false` line. `abandonRunningJob` (test-only, finding 7) duplicates `abandonRunningJobs` line for line.

Impact: this is the most invariant-heavy code in the slice (the comments on `detachOverBudgetWatch` alone run 15 lines) and each copy must re-establish those invariants. A fix to one rollback path does not reach the others.

Fix plan: introduce one `watchTeardown` value (`begin(jm, targets, detachedCfgs)` under lock; `commit(jm)`; `rollback(jm)`) that owns mark/append/rollback/detach/remove/forget, with the reason strings and `endReason` as fields. Convert call sites one per commit, red-test-first only where behavior is currently untested (the rollback-on-persist-failure paths, which `jobs_seed100_more_test.go` touches only with ignored results). Delete the Locked/unlocked wrapper pairs afterwards.

Size: about -200 lines. Risk: medium-high (persistence rollback); each conversion needs a persist-failure test that asserts the watch is live again and delivery is re-enabled.

---

### agent-jobs-4 (medium): `configureWatchWithHooks` is a 217-line function with test hooks in its production signature and redundant normalization

Locations:
- `agent/job_watch.go:755-980` (`watchConfigureHooks`, `configureWatch`, `configureWatchWithHooks`)
- `agent/job_watch.go:674-690` (`validateWatchConfig`)
- `agent/job_watch.go:1249-1269` (`validateWatchSendTarget`, `validateWatchSendDeliveryTarget`)
- `agent/job_watch.go:693-752` (`normalizeWatchArgs`)

Evidence:
- `configureWatch` is the only production caller and passes `watchConfigureHooks{}` (job_watch.go:761); `configureWatchWithHooks` callers outside it are 8 test call sites in `job_watch_timer_identity_test.go` and `job_watch_core_config_fuzz_test.go`. The two hook fields (`beforeTargetRevalidate`, `afterDetachedTeardown`) exist only to open race windows.
- `validateWatchSendTarget(target, a)` (1249) is a pass-through to `validateWatchSendDeliveryTarget(target, _ watchArgs)` (1253) whose second parameter is unused (blank).
- Send-target handling at 778-787 trims `a.Send.To` into `sendTo` in two separate `if` blocks over the same condition split by `ReceiverSendInternal`, and the key builder at 788-795 trims `ReceiverSessionID`/`ReceiverDelegateID` again, while `newWatchConfig` (1298) and `restoreWatchSendPendingFrom` (1560) trim them a third and fourth time.
- `validateWatchSendTarget` is evaluated in two places (`validateWatchConfig` 679, terminal-catchup branch 822) with the same `a.Send != nil && !a.ReceiverSendInternal` guard.
- The tail of the function (880-980) interleaves three different registration outcomes (idempotent no-op, replace, fresh install with detached-teardown) with `jm.mu` Lock/Unlock pairs (4 `Lock()` calls in the function) and a re-lock inside the detached branch.

Impact: hardest function in the slice to review; each new watch option (timer, every, send, receiver, stable receiver) added another branch here.

Fix plan: split into `prepareWatchRequest(a) (watchRequest, error)` (normalize, resolve key, trim once), `resolveWatchTarget(...)` (the terminal/catch-up/clear policy at 806-850), and `installWatch(...)` (lines 859-980). Delete `validateWatchSendTarget` and the unused parameter. Replace the two hook callbacks by a package-level test seam only if the race windows cannot be driven with real synchronization (they can for `beforeTargetRevalidate` via the existing `jm.mu`); otherwise keep them in one `jm.testHooks` struct (finding 5).

Size: about -60 lines, 3 new smaller functions. Risk: medium; `watch_config_validation_program_fuzz_test.go` and `job_watch_config_test.go` pin the behavior.

---

### agent-jobs-5 (medium): `jobManager` carries a dozen test seams, five mutexes and ~60 fields

Locations:
- `agent/jobs.go:64-252` (struct)
- seams: `appendAbandonSnapshots` (127), `appendTeardown` (128), `appendRegistry` (129), `finalizeShellAsync` (130), `shellBeforeWaitTimeoutDecision`/`shellBeforeBlockTimeoutDecision` (178-179), `testOnlyAfterNotifyPendingAppend` (189), `testOnlyAfterRematerializeDurableLoad` (199), `stopReceiptBeforeWait` (203), `stopReceiptsAfterCapture` (207), `watchReceiptBoundary` (212), `stableWatchArmFailure` (216)
- wiring: `agent/jobs.go:702-705`

Evidence: `appendAbandonSnapshots`, `appendTeardown` and `appendRegistry` are assigned in the constructor to `jm.appendWatchSendTerminalSnapshots`, `jm.appendWatchTeardownBatch`, `jm.appendWatchRegistryEvents` and are overridden only in tests (`jobs_seed100_more_test.go:38-49,223-226`). `appendRegistry` is read at `jobs.go:1753,2079`. Mutexes: `mu`, `watchActivityMu`, `watchNotifyMu`, `watchPersistMu`, plus `retirementOwner` indirection. Roughly 12 of ~60 fields exist only to let tests inject failures or open race windows. The same pattern appears as package vars (`delegateQuietWindow`, `defaultCloseGrace`, `laneSweepDelay`, `laneGrace`) plus per-manager copies (`quietWindow`, `quietCheckInterval`, `closeGrace`) for the same values.

Impact: production readers must mentally subtract 12 fields to find the real state; every test seam is a place production semantics can silently diverge from what tests exercise.

Fix plan: group all test-only seams into one `jobManagerTestHooks` struct reached through a single nil-in-production pointer, so the live struct is visibly smaller and each hook is documented in one place. For the three `append*` indirections, inject the fault through the existing store/fs layer instead (`jobstore` already has fault-injectable opens) and delete the fields. Do not add new seams; this is a consolidation only. Discuss with Jesse before deleting any hook whose test would need a larger rewrite.

Size: about -60 lines in production, test edits proportional. Risk: low-medium.

---

### agent-jobs-6 (high): `createShell` is production dead code that 238 test call sites use as their shell-job fixture, duplicating the real start path

Locations:
- `agent/jobs.go:991-1074` (`createShell`, 84 lines), `jobs.go:623-627` (`createShellOpts`)
- real path: `agent/job_shell.go:512-562` (`newDelayedShell`), `job_shell.go:594-662` (`commitDelayedShell`)

Evidence: `createShell` and `createShellOpts` have zero non-test references anywhere in the repo; tests reference `createShell` 238 times. The production start path is `newDelayedShell` + `commitDelayedShell`, which build the same `JobRecord` (identical 14-field literal: JobID, Type, Status, Command, Intent, Description, OwnerSessionID, VisibleToSession, ParentJobID, ParentDelegateID, StartedAt, Phase, LastActivity, OutputPath, Provenance) and the same `EventJobStarted` event (jobs.go:1033-1046 vs job_shell.go:620-636), then handle `forwardLocked` failure with the same `appendStartForwardFailure` dance. The copies have already diverged: `createShell` omits `WorkingDir` and `Background`, and does not take a `stableDelegateShellReceipt`, so tests built on it never exercise delegate-shell receipts or the delayed commit.

Impact: 238 tests validate a lifecycle that production never runs; a bug in `commitDelayedShell` or `newDelayedShell` can pass all of them. Two sources of truth for the started-event shape.

Fix plan: (1) add a test helper `startTestShell(t, jm, opts)` that calls `newDelayedShell` then `commitDelayedShell`; (2) sed the 238 call sites; (3) delete `createShell` and `createShellOpts`; (4) extract `startedEventFromRecord(rec)` and `newShellRecord(jm, args, jobID, outputPath)` so the event and record are built once. Run the job tests in CI after step 2 before deleting; a few tests may depend on `createShell`'s missing receipt and need a one-line adjustment (that is the point).

Size: about -90 lines production, ~238 one-line test edits. Risk: medium (test behavior changes expose real paths); worth it.

---

### agent-jobs-7 (medium): at least 20 production functions are reachable only from tests, six marked `//nolint:unused`

Locations (zero production refs; test refs in parentheses):
- `agent/jobs.go`: `setParentJobID` 291 (3), `noteJobActivity` 462 (18, nolint), `liveWorkHandles` 489 (12), `createJobOutputForID` 743 (10), `abandonRunningJob` 953 (8, nolint), `readOutputHead` 1378 (16, nolint), `outputDropped` 1402 (9, nolint), `grepOutput` 1444 (23), `isJobNotFoundErr` 1297 (7), `finalizeWithRunNoNotification` 1780 (10, nolint), `headOutput` 2311 (nolint), `SetLaneClosePassBudget` 272 (5)
- `agent/job_watch.go`: `watchListToolResultForReceiver` 2540 (12), `aggregateWatchStatuses` 2842 (6), `jobManager.liveWatchStatuses` 2741 (18), `Session.liveWatchStatuses` 2866, `LiveWatchesForDescendant` 3015 (11), `LiveWatchesForSession` 3045 (6), `recordWatchSendPending` 5217 (8, nolint), `feedJobOutput` 3572 (22), `watchCount` 6560 (85)
- `agent/jobs_activity.go`: `projectActivitySession` 1058 (11), `projectStableLiveActivityTree` 404 (1)
- `agent/job_shell.go:731` `shellTerminal` (7); `agent/jobs_panel.go:17` `isOutputNotExistErr` (3)

Evidence: the code says so itself. `aggregateWatchStatuses` doc (job_watch.go:2835-2841): "Superseded as a production entry point ... it is reached only from tests, which keep the receiver-rollup contract pinned through it." `LiveWatchesForDescendant` doc (3009-3013): "this single-ID form and LiveWatchesForSession below are reached only from tests". The `//nolint:unused` reasons read "retained for the tagged ... fuzz owners" (jobs.go:461,952,1377,1401,1779,2310; job_watch.go:5216). `liveWorkHandles` (jobs.go:489-504) is a byte-for-byte copy of `liveShellHandles` (511-526), which is the one production uses. `watchListToolResultForReceiver` (2540) is the non-"Shapes" twin of the one `session_tools_jobs.go:297` calls.

Impact: roughly 500 lines (including their ~150 lines of doc comments) of production surface that nothing ships; tests pin contracts on the dead seam instead of on the production entry point (`LiveWatchRowsForSessions`, `watchListToolResultForReceiverShapes`), so a change to the real path can pass while the dead copy still agrees.

Fix plan: for each, move tests onto the production entry point (or a `_test.go` helper if the helper is a convenience), then delete. Order: delete the pure duplicates first (`liveWorkHandles`, `abandonRunningJob`, `watchListToolResultForReceiver`, `aggregateWatchStatuses`, `LiveWatchesFor*`, `Session.liveWatchStatuses`), then the nolint set (move to `_test.go` files carrying the `evenerfuzz` tag where the sole users are tagged fuzz). Anything the tagged fuzz harnesses need and cannot reach through real paths moves into the tagged test files. No new compat shims.

Size: about -450 lines production. Risk: low; compiler finds every missed caller, including tagged builds (`go vet -tags evenerfuzz ./agent`).

---

### agent-jobs-8 (medium): five copies of "snapshot subagents, skip closed ones" in the slice, with `directSubagents()` already existing

Locations:
- `agent/jobs_nested.go:194-217` (`liveSubagentSessions`)
- `agent/jobs_nested.go:271-299` (`liveDirectSubagents`)
- `agent/jobs_nested.go:301-318` (`liveSubagentSession`)
- `agent/jobs_nested.go:34-40` (`ownerJobManagerFor`)
- `agent/jobs_activity.go:740-745` (`resolveActivityChildByID`)
- canonical snapshot helper: `agent/subagent_manager.go:245-255` (`directSubagents`); also `subagent_manager.go:364` reads `sub.closed` the same way

Evidence: `liveSubagentSessions` and `liveDirectSubagents` are identical except for the return type: both lock `s.subagents.mu`, copy `subs` into a slice, unlock, then for each non-nil `sub.sess` lock `sub.mu`, read `closed`, unlock, filter. That is the exact snapshot `subagentManager.directSubagents` (which also filters nil sub/sess) already performs. The `sub.mu.Lock(); closed := sub.closed; sub.mu.Unlock()` triple appears at jobs_nested.go:36, 211, 288, 310, jobs_activity.go:743 and subagent_manager.go:364. `liveDirectSubagents` has 17 production callers (job_watch.go, session_jobtree_drain*.go, subagents.go).

Impact: the leaf-lock discipline that the comments stress is re-implemented per copy; any change to what "live" means (for example the `closeTimedOut` state the manager already tracks) has to be made in six places.

Fix plan: add `func (sub *subagent) isClosed() bool` in `subagent_manager.go`; rewrite `liveDirectSubagents` as `directSubagents()` + `!isClosed()`; rewrite `liveSubagentSessions` as a map over `liveDirectSubagents`; replace the four inline triples with `isClosed()`. Existing tests (`job_nested_test.go`, `jobs_nested_seed100_more_test.go`) guard it; no new behavior.

Size: about -45 lines. Risk: low.

---

### agent-jobs-9 (medium): three `JobRecord` clone helpers with different depth, one of them shallow

Locations:
- `agent/jobs.go:2358-2364` (`cloneJobRecord`: `clone := *rec`, copies no pointer fields)
- `agent/jobs_activity.go:1031-1049` (`cloneActivityRecord`: deep-copies `EndedAt`, `ExitCode`, `LastActivity` only)
- `agent/internal/jobstore/store.go:945-985` (`cloneFoldJobs` / `cloneFoldOrdered`: full, 7 fields incl. `StructuredResult`, `StructuredResultValid`, `Provenance`, `NotificationProvenance`, with the identical 8-line body pasted twice)

Evidence: `JobRecord` has pointer/reference fields (`EndedAt`, `ExitCode`, `LastActivity`, `StructuredResult`, `StructuredResultValid`, `Provenance`, `NotificationProvenance`). `cloneJobRecord` is used 10 times in production for snapshots that cross lock boundaries (`liveJobRecords` jobs.go:1098, `listWithError` 1122, `recordForRead` 1364, `stopRunningJobLocked` 1642, `commitDelayedShell` job_shell.go:612, `session_tools_jobs.go:1225`), yet it shares every pointer with the source, so a caller mutating `rec.Provenance`/`rec.ExitCode` or a later in-place write by the runtime on `run.rec` is visible through the "clone". The store's own clone documents exactly this concern (store.go:938-944). I could not confirm a live mutation bug from reading; the risk is latent.

Impact: three definitions of "a copy of a job record" that disagree; adding a pointer field to `JobRecord` requires updating three places and nothing flags the miss.

Fix plan: red test first in `jobstore`: mutate every pointer/reference field of a clone and assert the source is unchanged. Then add `func (r *JobRecord) Clone() *JobRecord` in `agent/internal/jobstore/record.go` with the full body, make `cloneFoldJobs`/`cloneFoldOrdered` call it, and delete `cloneJobRecord` and `cloneActivityRecord`. Update the 10 + 2 call sites.

Size: about -45 lines. Risk: low; slight extra allocation per clone (`provenance` and `StructuredResult` copies on the hot `list` path); measure on CI only.

---

### agent-jobs-10 (medium): `watchConfig` plays three roles and carries a duplicate `id`/`watchID`; the "detached config" is hand-built three times

Locations:
- `agent/job_watch.go:207-294` (`watchConfig`, 45 fields)
- `agent/job_watch.go:1298-1350` (`newWatchConfig`, live)
- `agent/job_watch.go:1573-1587` (`restoreWatchSendPendingFrom`, detached)
- `agent/job_watch.go:3970-3984` (`noticeUnrestoredWatchEnds`, detached)
- `agent/job_watch.go:4335-4340` (`snapshotWatchSendFrame`, a throwaway `watchConfig{watchID, generation, note, send}` just to call `buildWatchFrame`)

Evidence: `id` and `watchID` are set to the same value at all three construction sites (1307-1308, 1574-1575, 3971-3972); `cfg.id` is read three times (2394, 2725, 3059) while `cfg.watchID` is read about 25 times. The two detached constructors (1573, 3970) set the same 12 fields (`id, watchID, sourcePublic, receiverSessionID, receiverDelegateID, target, send, generation, pending, sourceDelegateID, sourceGeneration, stableReceiver`) and the second even says "A detached config, like the one restoreWatchSendPendingFrom rebuilds" in a comment (3965). `buildWatchFrame(cfg, ...)` (6239) reads only `cfg.send`, `cfg.watchID`, `cfg.note`; `snapshotWatchSendFrame` therefore builds a fake 4-field config to satisfy the signature. Tests construct `&watchConfig{...}` 132 times, which also pins the struct shape.

Impact: three roles (live registration, detached pending-frame carrier, frame-render parameter) share one 45-field struct, so no reader can tell which fields are valid in which role; `id` vs `watchID` invites a bug where one is updated.

Fix plan: (1) delete `id` and make the three readers use `watchID` (mechanical, compiler-checked, plus test sites); (2) add `newDetachedWatchConfig(fields)` used by the two restore sites; (3) change `buildWatchFrame` to take a small `watchFrameSource{watchID, note, send}` value, which removes the fake config. Do not split `watchConfig` further without discussion: the `terminalFlush map[*watchConfig]bool` identity semantics depend on it.

Size: about -50 lines. Risk: low-medium (132 test constructors touch `id:`; `rg 'id:\s+' agent/*_test.go` first).

---

### agent-jobs-11 (medium): receiver and visibility matching is duplicated across config, history and key types, and the list and inspect paths disagree

Locations:
- `agent/job_watch.go:2540-2590` (`watchListToolResultForReceiver`, `...ForReceiverShapes`)
- `agent/job_watch.go:2648-2678` (`inspectReceiverWatchByID`, `inspectReceiverWatchByIDShapes`)
- `agent/job_watch.go:3200-3225` (`watchConfigMatchesReceiver`, `watchConfigVisibleToSession`, `watchHistoryMatchesReceiver`, `watchHistoryVisibleToSession`)
- `agent/job_watch.go:5528-5583` (`watchSendKeyMatchesWatchKey`, `watchConfigMatchesWatchKey`, `watchConfigSendToMatchesWatchKey`, `watchConfigReceiverMatchesWatchKey`)
- `agent/job_watch.go:1863-1879` (`watchKeyMatchesClearRequest`)
- `agent/job_watch.go:2367-2380` (`watchHistoryEntry`: copies `receiverSessionID`, `receiverDelegateID`, `sendTo`, `source`, `target`, `condition`, `note`, `deliveries` from the config)

Evidence: there are 11 predicate functions over (config, history entry, durable key, live key) x (receiver match, session visibility, clear match). `watchConfigSendToMatchesWatchKey` (5564) re-runs `watchConfigReceiverMatchesWatchKey`, which its only caller `watchConfigMatchesWatchKey` (5545) already checked. The list path computes both receiver-key shapes in one pass (comment 2557-2565: formatting each shape separately "would interleave an older entry ahead of a newer one"), but the inspect path calls `inspectReceiverWatchByID` twice (2677: `jm.inspectReceiverWatchByID(watchID, receiverSessionID, "")`), the two-pass shape the list comment warns against. `inspectReceiverWatchByID` is also called directly by production (`session_tools_jobs.go:377`) without the `Shapes` variant, so whether a caller sees one or both shapes depends on which of two similarly named functions it picked.

Impact: the "which receiver shapes does this tool see" rule lives in call-site choice; two of the helpers exist in twin and "Shapes" forms, one twin of which is test-only (finding 7).

Fix plan: a single `receiverMatcher{sessionID, delegateID string; bothShapes bool}` with `matchesConfig(*watchConfig)` and `matchesHistory(watchHistoryEntry)`; make list and inspect take it; give `watchHistoryEntry` an embedded `watchReceiver{sessionID, delegateID}` struct shared with `watchConfig` so the predicate is written once. Characterization test: a receiver with both session-keyed and delegate-keyed watches queried through list and inspect returns the same set.

Size: about -80 lines. Risk: low-medium.

---

### agent-jobs-12 (medium): the send-rail delivery state is spread across ten maps and sets with invariants kept by comments

Locations:
- fields: `agent/jobs.go:81-130,226-251` (`deliveredWatchSendIDs`, `deliveringWatchSends`, `supersededWatchSends`, `unsettledWatchSends`, `stableWatchReceipts`, `stableWatchSettlementRetries`, `stableWatchSettlementRetrying`, `terminalFlush`, `watchHistory`, `lastFedOffset`) and on `watchConfig`: `pending`, `pendingOrder`, `settledUpdateSeq`, `settledOrder`, `rejectingDelivery`, `nextUpdateSeq`
- code: `agent/job_watch.go:4268-5705` (about 1,440 lines): `deliverStableWatchSend` 4552, `persistPendingWatchSend` 4782, `planWatchSendPending` 5137, `storeWatchSendPendingRecord` 5244, `retryStableWatchSettlements` 5068 and the nine `*StableWatchSettlementRetry*` helpers 4987-5066

Evidence: `deliverStableWatchSend` (4552-4644) handles "receiver took the frame but the config is no longer the pending head" and "still the head" as two near-identical branches that each call `armAcceptedStableWatchFrame`, then `settleWatchSendDelivered`, differing only in the failure bookkeeping (`rememberStableWatchSettlementRetry` vs `noteUnsettledWatchSend`). Eight different helpers consult `jm.unsettledWatchSends` / `jm.stableWatchReceipts` / `jm.stableWatchSettlementRetries` under `jm.mu`. The commentary refers to issue numbers (#327, #940, #3723) to explain why each exists.

Impact: the hardest-to-reason-about code in the repo (delivery receipts across restore and coalescing) is not a type. Every fix in the 259-commit history added a map or a flag.

Fix plan: this is an architectural change, so discuss with Jesse before starting. Proposal: extract `watchSendRail` (own file, own tests) that owns the receipts/claims/retries maps and exposes `claim(deliveryID)`, `release`, `noteUnsettled`, `settle`. Start by only moving the existing maps and their 12 accessors into the struct (no logic change), then collapse the two `deliverStableWatchSend` branches. Keep `jm.mu` as the guard to avoid a new lock order.

Size: about -150 lines after the move. Risk: high; require a concurrency test per moved accessor and CI-only validation.

---

### agent-jobs-13 (medium): `runShell` repeats its failure and result assembly, and a third start-failure literal appears six times

Locations: `agent/job_shell.go:170-357`; literal sites `175, 182, 238, 322, 464` (plus `193`).

Evidence: the block `if errors.Is(err, errDelayedShellStartForwardTerminalFailed) { go jm.finalizeShellUntilDurable(..., "forward_failed", nil) } else if !errors.Is(err, errDelayedShellStartForwardFailed) { jm.discardDelayedShell(run) }` is pasted verbatim at 234-240 (background) and 318-324 (foreground timeout). `shellResult{Type: string(jobstore.JobShell), Status: string(jobstore.StatusFailed), Reason: "start_failed"}` is written out five times. The `Output/Truncated/TotalBytes/DroppedBytes: run.output.RetainedStart()` field group is assembled three times (270-282, 337-346, 349-357) from `fullOutput`/`tailOutput`.

Impact: the "commit failed" policy is a subtle three-way rule; two copies will drift.

Fix plan: `startFailedResult()` and `(jm) handleCommitFailure(run, handle, err) shellResult`, plus `(jm) withOutput(res shellResult, run, out, total, truncated)`. No behavior change; `job_shell_test.go`, `job_shell_fuzz_test.go`, `job_shell_promotion_background_test.go` are the guards.

Size: about -35 lines. Risk: low.

---

### agent-jobs-14 (low): the watch event-kind vocabulary is defined three times, one copy guarded by a "keep in sync" comment

Locations:
- `agent/job_watch.go:24-31` (`WatchEventKindNames`, exported, plus `availableEventKindNames()` wrapper that copies it)
- `agent/job_watch.go:34-38` (`modelEventKinds` map with the same three keys)
- `agent/provider/profile.go:111-114` (`jobWatchEventKindNames`, "Keep in sync with agent.WatchEventKindNames / agent.modelEventKinds")

Evidence: `WatchEventKindNames` has no consumers outside `job_watch.go` (the only use is the wrapper at `session_tools_jobs.go:117`); `profile.go` already imports `agent/internal/tool` for `tool.DefJobWatch`. A drift (adding a fourth kind in one place) is caught only by a human reading the comment.

Fix plan: define the names once in `agent/internal/tool` next to `DefJobWatch` (`tool.WatchEventKindNames`), have `provider/profile.go` and `session_tools_jobs.go` use it, and keep `modelEventKinds` in `agent` with a test asserting its keys equal `tool.WatchEventKindNames`. Delete `availableEventKindNames` and the exported var.

Size: about -12 lines. Risk: low.

---

### agent-jobs-15 (low): lane-disposal tunables and a push/pop override stack live in `jobs.go`

Locations: `agent/jobs.go:224-289` (`defaultLaneClosePassBudget`, `laneClosePassBudgetMu`, `laneClosePassBudgetOverrides`, `laneTailWarnThreshold`, `laneSweepDelay`, `laneGrace`, `closePassBudgetOverride`, `laneClosePassBudget`, `SetLaneClosePassBudget`); consumers `agent/session_worktree_close.go:98,207,223,283`, `session_worktree_sweep.go:40,168`, `session_worktree_relock.go:181`.

Evidence: nothing in the job manager uses these; every consumer is a `session_worktree_*` file. `SetLaneClosePassBudget` has zero production callers (5 test refs) and implements an order-independent override stack with `sync.OnceFunc` and a `sync.RWMutex` to shorten a 30-second constant, while the three sibling tunables (`laneTailWarnThreshold`, `laneSweepDelay`, `laneGrace`) are plain package vars that tests assign directly.

Fix plan: move the block to `session_worktree_close.go`. Replace the override stack with a plain package var matching its siblings (tests restore with `t.Cleanup`), unless a concurrent test genuinely depends on nested out-of-order restore (grep says 5 test refs; check them in CI). If one does, keep the stack but still move it.

Size: about -30 lines. Risk: low.

---

### agent-jobs-16 (medium): four on-disk "read one job record from a session journal" paths with different safety checks

Locations:
- `agent/jobs_panel.go:83-103` (`loadSessionJobRecord`)
- `agent/historical_jobs.go:33-69` (`LoadSessionHistoricalJobRecords`, adjacent to the slice)
- `agent/job_transcript_read.go:254-325` (`findLocalJobInProject`, with `validateLayoutPrefix`, `symlinkErrorDeep`, `Lstat` regular-file check)
- `agent/jobs_activity_past.go:98-132` (`extendHistoricalJobFold`, cached incremental scan)

Evidence: the first two are the same code: `schema.ValidateSessionID` -> `filepath.Join(jobsDir(...), "jobs.jsonl")` -> `historicalJobsStat` (`os.Stat`, which follows symlinks) -> `os.IsNotExist` -> `jobstore.ReadEvents` -> `jobstore.Fold`. `findLocalJobInProject` documents why that is not enough (a FIFO blocks `os.Open` indefinitely; symlinked ancestors) and adds `symlinkErrorDeep` and an `Lstat` regular-file check, but `loadSessionJobRecord`, reached from the hub's past-session fallback (`cmd/evener-hub/app_jobs.go:108,120`), does not. Which hardening a caller gets depends on which of the four it landed on.

Impact: inconsistent defense for the same read; two verbatim copies of the happy path.

Fix plan: one `readSessionJobJournal(stateDir, sessionID) (records map[string]*jobstore.JobRecord, found bool, err error)` in `historical_jobs.go` with the hardened checks from `findLocalJobInProject`; `loadSessionJobRecord` and `LoadSessionHistoricalJobRecords` call it. Leave `extendHistoricalJobFold` alone (it is the incremental, budgeted reader and legitimately different). Add a FIFO-journal test (red first) for the panel path.

Size: about -30 lines, +1 test. Risk: low.

---

### agent-jobs-17 (low): evergreen docs point at stale line anchors and moved function homes

Locations:
- `docs/product/friction.md:572-575,589,1444` (six `agent/jobs.go#L...` / `agent/job_watch.go#L...` anchors)
- `docs/architecture.md:330,389,415,423` (names `job_watch.go` as the home of `drainPendingWatchSends`, `driveChildrenWithUndeliveredAttention`, `renderUnreachableChildPendings`)

Evidence: current locations versus the anchors: `newJobManagerWithStoreOpen` anchor L647, actual 663; `clearUnrestoredActiveWatches` L1526, actual 1540; `watchConfigSnapshot` L2174, actual 2233; `noticeUnrestoredWatchEnds` L3830, actual 3921; `closeRuntimeState` L757, actual 778; `reconcileLostJobs` L1457, actual 1467. All six are off by 10 to 90 lines. `AGENTS.md` asks that product docs stay evergreen with stable references.

Fix plan: replace line anchors with symbol names (file link without `#L`), and update `architecture.md` function homes in the same PR as finding 1. A cheap lint (`rg '\.go#L[0-9]+' docs/product docs/architecture.md`) can keep it from returning, but do not add a script just for this without asking.

Size: about 10 lines of docs. Risk: none.

---

### agent-jobs-18 (medium): coverage-driven test files assert on phrasing, carry stale line references and ignore results

Locations (examples):
- `agent/jobs_pure_covtest_test.go:14-27` (`TestCovTerminalRecordPersistError`: `strings.Contains(got, "persist terminal job record")`), 30-47
- `agent/jobs_seed100_more_test.go:38-49,210-226` (35 `_ =` discards in 311 lines; `_ = jm3.finalizeKeptSync(...)` with an injected error and no assertion)
- 35 files named `*cov*`, `*covtest*`, `*seed100*` in the slice (about 11,970 lines): `session_tools_jobs_covtest2_test.go` 1,540; `watch_pure_covtest_test.go` 971; `job_watch_covtest2_test.go` 922; `jobs_covtest2_test.go` 765; `jobs_activity_past_covtest_test.go` 550; and others

Evidence (counted over the 148 job/watch test files): 124 comments of the form "covers X (jobs.go lines N-M)" with stale line numbers (the `keepListedJobRow` test says "jobs.go lines 991-1007"; it is now at 1150); 159 `TestCov*` functions; 282 `// Test... covers/exercises` comments; 340 `strings.Contains(<got>, "<literal of 8+ chars>")` assertions. `AGENTS.md` says "no test may check that copy, error messages, or other human-readable strings match a hardcoded phrase" and `docs/developing-evener/testing.md` ("Copy Is Not a Test Oracle"). The test-to-production ratio is 3.9:1 (56,690 to 14,378 lines), mostly from files whose stated purpose is line coverage.

Impact: tests that break on a wording change and protect no behavior; maintenance drag on every refactor in this audit (renaming `createShell` or `cloneJobRecord` touches hundreds of lines).

Fix plan: do not delete coverage. Per file: (a) for each phrase assertion, assert the error kind (`errors.Is`, a code field, or the structured result) instead, or drop the assertion if a sibling behavioral test covers it; (b) remove the stale "covers ... lines N-M" comments; (c) where a test only calls a function and discards the result, either assert the post-state (job record, journal contents, watch live/not live) or delete it if the behavioral tests listed in this audit already cover the same branch; (d) fold the tiny `TestCov*` pure-function tests into table tests in the `*_test.go` file named for the production file. Verify with the CI coverage report before and after, per file, and never reduce the covered-line count. Sequence this after findings 3, 6 and 7 so the churn is not done twice.

Size: about -2,500 to -4,000 test lines. Risk: low-medium (coverage regressions are visible in CI).

---

### agent-jobs-19 (low): narrating and issue-history comments in production code

Locations (examples): `agent/job_transcript_read.go:292-315` ("round 10", "round 11", "this PR established"); `agent/job_watch.go:3307` ("kata 673k"); `agent/jobs_activity.go:366-388` (22-line known-gap essay in `JobActivityTree`); `agent/jobs.go:1148` ("Behavior mirrors the original clause order exactly"); `agent/job_watch.go:2360` ("no longer holds the lock"); and others citing "spec §N" (51 occurrences) and issue numbers (12).

Evidence: grep over the slice's non-test files: comment lines mentioning `this PR` 6, `kata` 2, `round N` 2, `no longer` 12, `#NNN` 12, `spec §` 51, `Task N` 2, of 2,861 comment lines. `CLAUDE.md` rule: history belongs in the commit message.

Fix plan: rewrite each to state the invariant, not its provenance; keep the reasoning, drop "round 10"/"kata"/"previously". Do it in the same PR that splits each file (finding 1) so blame is not churned twice.

Size: about -150 comment lines. Risk: none.

---

### agent-jobs-20 (low): small pass-through wrappers and double-field filters

Locations: `agent/jobs.go:629-643` (`listFilter` has both `Status`/`Statuses` and `Type`/`Types`), `jobs.go:1150-1175` (`keepListedJobRow`, `statusAllowed`, `typeAllowed`), `jobs.go:1100-1105` and `jobs.go:2262-2266` (identical StartedAt-desc then JobID sort comparator, a third variant at `jobs_activity.go:1051`), 

Evidence: `statusAllowed`/`typeAllowed` are one-line `slices.Contains` wrappers; `Status` is set by one production caller (`session_tools_jobs.go:2149`, `Status: running`) while `Statuses` carries the tool's filter, so the single-value fields exist only for that call and for tests. The comparator is pasted twice in `jobs.go`.

Fix plan: delete the two wrappers, collapse `Status`/`Type` into the slice fields (update the one prod caller and tests), add one `sortJobsNewestFirst(recs)` helper. Small and mechanical.

Size: about -25 lines. Risk: low.

---

## Recommended fix sequence

1. **Finding 7 (test-only prod code) and 6 (`createShell`)**: smallest blast radius and they delete the most code; they also remove ~20 symbols that later findings would otherwise have to carry through refactors. Move tests onto the production entry points first.
2. **Finding 1 (split `job_watch.go`) with 17 and 19**: pure moves plus doc/comment fixes in the same PR, so later PRs diff against the new layout. Land when no other PR is mid-flight on the file.
3. **Findings 8, 9, 14, 15, 16, 20**: independent small consolidations (subagent liveness helper, `JobRecord.Clone`, event-kind vocabulary, lane tunables, journal reader, filter wrappers). Can be parallel PRs.
4. **Finding 10 (watchConfig `id`, detached constructor, frame source)**, then **11 (receiver matcher)**: prepare the config shape before touching teardown and fire code.
5. **Finding 3 (teardown transaction)**, then **2 (fire funnel)**: each needs persist-failure and budget-crossing characterization tests written first; do them in separate PRs.
6. **Finding 4 (split `configureWatchWithHooks`) and 13 (`runShell` helpers)**: after 3 so the install path reuses the teardown type.
7. **Finding 5 (test-hook consolidation)**: after 6 and 7 have removed the hooks that were only there for dead paths.
8. **Finding 18 (test cleanup)**: after the refactors above, per file, with CI coverage diffs.
9. **Finding 12 (send-rail extraction)**: last, and only after discussing the design with Jesse; it is the one finding that changes how state is owned.

Nothing here changes watch restore semantics (R03), activity read scope, or any client-visible contract.
