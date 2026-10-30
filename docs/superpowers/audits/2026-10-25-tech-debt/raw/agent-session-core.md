# Tech-debt audit: agent-session-core

Slice: `agent/session.go`, `session_client_mutation*.go`, `session_config.go`,
`session_capabilities.go`, `session_events.go`, `session_execution.go`,
`session_init.go`, `session_lifecycle.go`, `session_queue*.go`,
`session_state.go`, `session_stream.go` (non-test). Worktree at `a6c0b9c561`.

## Overview and inventory

| File | Lines | Funcs | Comment lines |
| --- | ---: | ---: | ---: |
| session.go | 2784 | 85 | 1251 |
| session_lifecycle.go | 3413 | 64 | 1396 |
| session_init.go | 2672 | 54 | 726 |
| session_client_mutation_queue.go | 2064 | 67 | 466 |
| session_client_mutation.go | 2053 | 49 | 616 |
| session_queue.go | 1626 | 85 | 482 |
| session_config.go | 1098 | 5 | 638 |
| session_state.go | 794 | 40 | 262 |
| session_events.go | 763 | 23 | 360 |
| session_stream.go | 579 | 10 | 115 |
| session_execution.go | 513 | 26 | 152 |
| session_capabilities.go | 445 | 17 | 176 |
| session_client_mutation_persist.go | 444 | 15 | 27 |
| session_queue_persist.go | 148 | 7 | 45 |
| **Total** | **19396** | **547** | **6712 (34.6%)** |

Context numbers: `session.go` has 569 commits and `session_lifecycle.go` 534 (via
`git log --follow`), so these are the hottest files in the repo. The package
has 83 non-test `session*.go` files and `agent/session*_test.go` totals 182,520
lines. The `Session` struct literal spans `session.go:109-1125` (1016 lines,
about 274 fields, 22 `sync.Mutex`/`RWMutex` fields).

Longest functions in the slice (measured from `^func` to `^}`):

| Lines | Function |
| ---: | --- |
| 777 | `RestoreSessionFromMetaWithConfig` (session_init.go:852) |
| 663 | `processOneInput` (session_lifecycle.go:1945) |
| 584 | `processInputKindWithProvenance` (session_lifecycle.go:1118) |
| 388 | `NewSession` (session_init.go:298) |
| 361 | `releaseRuntimeOnce` (session_lifecycle.go:409) |
| 207 | `consumeModelStream` (session_stream.go:306) |
| 197 | `acceptUserInputWithSkillSelection` (session_lifecycle.go:2703) |
| 195 | `initSessionState` (session_init.go:1646) |
| 193 | `InterruptClientMutation` (session_client_mutation.go:924) |
| 156 | `claimClientMutationStart` (session_client_mutation.go:523) |

Overall health: the logic is careful, and the concurrency and durability
reasoning is real. The debt is structural. Two construction paths are
copy-pasted. Two turn-loop functions carry about 1,250 lines between them. Seven
RPC handlers share one hand-copied skeleton. A "legacy" public API that no
production code calls is kept alive by hundreds of tests. About a third of the
text is comments, and many narrate history (kata ids, review ids, "used to read
...") instead of stating the invariant. Test seams are threaded through
production code (91-field `testConfig`, `testing.Testing()` forks). The product
docs (`docs/product/subsystems.md` S09 row) do not describe any of this, so
refactors here need only the S09 row touched if ownership moves.

Method note: I did not run any tests. "No production callers" claims come from
`grep -rnw` over the whole worktree excluding `_test.go` and comment lines,
including method-value uses (`x.Method` without parens), which I checked
explicitly after the first pass missed two.

---

## Findings

### agent-session-core-1: NewSession and RestoreSessionFromMetaWithConfig are two copies of one constructor (high)

Locations: `agent/session_init.go:298-685` (NewSession), `:852-1629` (Restore).

Evidence. The same blocks are pasted in both, with small drift:
- Context and cancel setup: `owner := cfg.LifetimeContext ... context.WithCancel(owner)` at `:362` and `:1095`.
- Delegation allowance and tree/drive counters: `:375-390` vs `:1100-1115`, identical including the 4-line "Mirror the live counter onto cfg.spawn" comment.
- Job activity clock: `:405-409` vs `:1120-1128`.
- Client mutation store, artifact store resolve, `bootstrapDelegateResources`, `newSubagentManager`, `newJM` selection: `:360,411,482` vs `:969,975,1196`.
- Job manager wiring: 13 identical `jm.* = s.*` assignments at `:511-524` and `:1229-1243`. They differ in one line only. NewSession passes `s.enqueueJobNotificationAndNotify` to `newJM`, and Restore passes `nil` then sets `jm.enqueue` afterwards.
- Three paired `closeXOnError := true` / `defer` / `= false` flags for job manager, MCP manager, delegate store: `:482-545` vs `:1202-1377`, each flipped at the end (`:675-678`, `:1623-1628`).
- Transcript header construction: `transcript.Header{SessionID, ParentSessionID, ParentToolCallID, Task, CreatedAt, ProfileID, Model, WorkingDir, Depth, BuildVersion, SystemPrompt}` at `:587` vs `:1406`, plus the `sessionInitFault` / `testSpeedIO` / `NewWriter` selection ladder right after each.
- Strategy selection and registration loop at `:649-668` vs `:1536-1556`, including the `ApplyThresholdScale` line.
- The `&Session{...}` literal (about 35 shared fields) at `:422` and `:1130`.
- The nil-client / nil-profile / nil-env guards at `:303-311` and `:853-861`.

The Restore function is 777 lines and interleaves roughly 20 `refreshFromDisk`-style recovery steps with construction. A fix to one path (for example a new `closeXOnError` resource) has to be remembered in the other.

Impact. Every construction-time change is a two-site edit. The 777-line function is not reviewable. Drift is already present (`newJM(..., nil)` plus later assignment vs a constructor arg).

Fix plan.
1. Extract a `sessionBuilder` (unexported struct in `session_init_build.go`) holding `cfg`, `client`, `profile`, `env`, the cleanup stack, and the shared steps as methods: `newRuntime(id)` (ctx, counters, clock, literal), `wireJobManager(jm)`, `openOrCreateTranscript(hdr, tpath, ...)`, `installStrategy()`, `commit()`. Use a single `cleanup []func()` stack instead of three booleans.
2. Do it as pure moves first, one block at a time, with the existing restore and new-session tests as the guard (`session_init*_test.go`, `cov_w3init_restore_test.go`, fuzz seeds). No behavior change.
3. After the extraction, Restore keeps only its genuinely restore-only parts (transcript replay, `refreshFromDisk`, crashed-execution closing, derived state), which should land near 250 lines and split into named phases.
4. Doc: none required (internal). Note the builder in the S09 source list if the file name changes.

Size: about 450 lines removed, 200 added. Risk: medium-high. This is the most failure-sensitive code in the repo, so move in tiny steps and rely on CI for the suite.

---

### agent-session-core-2: processInputKindWithProvenance and processOneInput are 1,250 lines of nested control flow with the session-end/idle bookkeeping repeated at every exit (high)

Locations: `agent/session_lifecycle.go:1118-1702`, `:1945-2608`, `:1714-1750` (endInputAtTurnFailure).

Evidence.
- `s.mu.Lock(); s.sessionEndEmitted = false; s.mu.Unlock()` appears at `:1413`, `:1579`, `:1587`, `:1665`, and `sessionEndEmitted = true` plus an `emit(EventSessionEnd, ...)` block is written out three times: cancellation branch (`:1350-1370`), the successful settle tail (`:1685-1700`), and `endInputAtTurnFailure` (`:1714-1750`). A fourth sits in `releaseRuntimeOnce` (`:451`).
- `strings.Join(outputs, "\n")` returned from 5 exits. `releaseSteeringCarrierClaim(...)` and `completeClientMutationTurn(...)` give-back logic is repeated at the top-of-loop gate (`:1141-1173`) and again in `processOneInput` (`:3171`).
- The tail of `processInputKindWithProvenance` interleaves five concerns in one `for`: transcript-health gate, cancellation/interrupt marker, error handling, drain-ladder selection, goal gate. `processOneInput` interleaves slash-command expansion, rest/restMu locking, name minting, per-kind acceptance, skill batch admission, then the entire round loop starting at `:2290`.
- A single 25-line comment at `:1458-1485` explains a historical behavior of `awaiting` ("This used to read s.State() == SessionAwaiting alone, on the premise ...").

Impact. These two functions are where most of the 534 commits to this file land. Any new entry kind or exit needs the session-end/idle bookkeeping added by hand at the right branch.

Fix plan (behavior-preserving, tests exist in `session_lifecycle*_test.go` and the seq-fuzz tests):
1. Introduce `inputRun` (struct: outputs, next input/kind/images/provenance, ctx, `haveDeferredCont`, `ranKind`) so loop state is not eight loose locals.
2. Extract three methods from the loop body without changing logic: `gateTurn()` (the transcript-health refusal and give-back), `settleTurnError(err)` (the whole `if err != nil` arm, including interrupt-marker handling), `selectAndPrepareNext()` (drain ladder). One `emitSessionEndOnce(reason, interrupted)` replaces the three hand-written blocks, and one `markSessionEndPending()` replaces the lock/unlock pairs.
3. In `processOneInput`, extract `beginTurnState()` (the restMu/delegateDeliveryMu/mu block `:2060-2090`), `mintTurnName(kind)` (the 80-line notification stand-down block at `:2137-2200`), `admitInput(kind)` and `runRounds()` (the loop). Keep the defers (`finishAttentionTurn`, autosave, release turn id) in the shell.
4. Trim comments to the invariant while moving (see finding 11).

Size: about 0 net lines at first (moves), then 150-250 removed. Risk: high. Guard with the existing seq-fuzz and tail-coverage tests, run on CI only; do one extraction per PR.

---

### agent-session-core-3: Seven client-mutation RPC handlers hand-copy one skeleton, plus five pure pass-through `Accept*` wrappers (high)

Locations: `agent/session_client_mutation_queue.go:112` (Queue), `:742` (Steer), `:865` (Drain), `:1009` (Promote), `:1107` (Cancel); `agent/session_client_mutation.go:267` (Start), `:924` (Interrupt); wrappers at `session_client_mutation_queue.go:566,861,973,1103,1188`. A ninth and tenth copy lives outside the slice in `session_notes_rpc.go:55,104,107,198,201`.

Evidence. Each handler is: `beginRetirementMutation("input")` + `defer release()`; `appwire.NormalizeMutationInput` (only Queue/Steer/Drain/Start); `ensureClientMutationStore()`; `newClientMutationRequest`; `executeAtomic(request, prepare, effect)` where prepare starts with the same `if snapshot.InterruptFence != nil { rejectClientMutation(record, appwire.Conflict("turn interrupt is pending")) }` (the string appears at `session_client_mutation_queue.go:134,763,888,1026`, `session_client_mutation.go:289`); `json.Marshal(response)` then `applyClientMutationRecord`; after the call `if lookup.Record.OperationState == clientMutationOperationRejected { return clientMutationRejectionError(...) }` (`:176,787,946,1076,1170`, `session_client_mutation.go:372`); then a replay branch calling `replayClientMutationResult` and overwriting `Receipt.Disposition`/`ProjectionState` (`:791,954,1084,1174`). Queue alone uses `queueResponseFromRecord` for the same replay step.

The five `AcceptClientMutation{Queue,Steer,DrainAsSteer,PromoteQueuedAsSteer,CancelQueued}` methods are one-line `return s.clientMutationX(params)` (`:566-568`, `:861-863`, `:973-975`, `:1103-1105`, `:1188-1190`). Their only production caller is `cmd/evener/serve.go:1512-1524`. The private versions are called by about 90 test sites and by the "legacy" wrappers in session_queue.go (finding 5).

Impact. Fixing a replay or rejection bug means editing 7-9 places. Replay handling already differs between handlers (Queue vs the rest).

Fix plan.
1. Add one generic helper in `session_client_mutation.go`: `func acceptClientMutation[P any, R any](s *Session, spec mutationSpec[P, R]) (R, error)`, with `spec` supplying the method name, id, params, optional input normalization, `prepare`, `effect`, a `buildResponse`, and `afterApplied`/`afterReplayed` hooks (the `reflect*`/`wake*` calls). It owns lease, store init, interrupt-fence rejection, rejection mapping, and replay decoding.
2. Port handlers one per commit; keep each handler's `prepare`/`effect` bodies verbatim. Start with Cancel (smallest).
3. Delete the `AcceptClientMutation*` wrappers by renaming the private methods to the exported names and updating the ~90 test sites mechanically (or keep the exported name and delete the private one; pick the exported name because `serve.go` uses it).
4. Also port `session_notes_rpc.go` to the helper (outside this slice, flag to that audit).

Size: about 600 lines removed, 150 added. Risk: medium (idempotency/replay is subtle) but the existing replay tests (`session_client_mutation_queue_test.go` has replay cases for each) cover it.

---

### agent-session-core-4: Three parallel "claim a durable input, announce it, run it, give it back on refusal" drivers (high)

Locations: `agent/session_client_mutation.go:745-880` (`ProcessClientMutationStart`), `agent/session_client_mutation_queue.go:249-396` (`ProcessPendingUserInput`), and the in-loop claim in `agent/session_lifecycle.go:1590-1720` plus `:1141-1173`.

Evidence. All three: probe without a lease (comment pasted twice, `session_client_mutation.go:746-755` and `session_client_mutation_queue.go:250-262`); `beginRetirementMutation("turn")`; claim with a poison-refusing store commit; announce under `attachedTranscript().WhileHealthy(func(){ onRunnable(...) })` and give the claim back if `announceRefusal != nil` (`:782-797` vs `:333-347`); build ctx with `withQueuedClientMutation` + `contextWithSelectedSkills`; call `ProcessInputKind`; then `if transcriptRefusedClaim(err) && !...Incorporated(...) { give back }` with the same 8-line explanatory comment (`session_client_mutation.go:815-826`, `session_client_mutation_queue.go:349-361`, plus the top-of-loop gate in lifecycle). Three different give-back functions exist for the same idea: `returnUnrunStartClaim`, `completeClientMutationTurn`, `releaseSteeringCarrierClaim`. Three wake funcs (`notify`, `wakePendingUserInput`, `wakeClientMutationStart`) are separate callbacks wired in `cmd/evener/serve.go:1344-1354`.

Impact. The start path and the queue path already diverge: the start path has an extra recovered-turn give-back with a 23-line comment, the queue path has carrier stand-down accounting. A fix to the "claim given back exactly once" rule has to land in each.

Fix plan.
1. Define `type durableClaim struct{ input queuedInput; giveBack func() error }` and `func (s *Session) runClaimed(ctx, claim, onRunnable) (result string, err error)` owning announce, ctx setup, `ProcessInputKind`, transcript-refusal give-back. The three call sites supply only their claim and their extra give-back rule.
2. Collapse the three give-back functions into `claim.giveBack`.
3. Defer the wake-func unification to a later step (finding 15).
4. Tests: `session_recovered_turn_start_test.go`, `session_poisoned_wake_gate_test.go`, `session_client_mutation_queue_test.go`.

Size: about 250 lines removed. Risk: high (durable-claim ordering). Do after finding 3.

---

### agent-session-core-5: A "legacy" public Session API with no production callers is kept alive by tests (medium)

Locations: `agent/session_queue.go:192-295,401-415,461-632,661-670,886-917,1580-1600`; `agent/session.go:1940`; `agent/session_init.go:846`; `agent/session_client_mutation_queue.go:1876`.

Evidence. Repo-wide `grep -rnw` over non-test, non-comment code finds no caller for: `Enqueue`, `EnqueueWithImages` (only `Enqueue` calls it), `DrainAsSteer`/`DrainAsSteerWithInput`, `PromoteQueuedAsSteer`, `CancelQueued` (the hub's `source.DrainAsSteer` etc. are a different type), `SteerFromUser`, `SteerFromUserWithImages`, `SteerWithImages`, `FollowUp`, `QueuePreview`, `QueueIDs`, `QueueTexts`, `SteeringQueueSnapshot`, `WithQueuedInputDrainOnInterrupt` (only the `...Handler` variant is used, `cmd/evener/serve.go:1797,1810`), `DroppedModelFallbacksFromLastSwitch`, `RestoreSessionFromMeta` (production uses `...WithConfig`), `completeClientMutationInterruptedTurn` (defined at `session_client_mutation_queue.go:1876`, referenced only by tests), `communicateStructuredResult` (`session.go:2033`).

Tests, by usage count: `Enqueue` 155, `QueueDepth` 85, `FollowUp` 41, `QueuePreview` 40, `QueueIDs` 25, `RestoreSessionFromMeta` 178 (all `_test.go`).

These wrappers mint client ids as `"legacy_" + newQueueEntryID()` (`session_queue.go:281,485,543,586,624`) and wrap the real handlers with differently-worded checks (`"queue: session is closed"`, `"drain: no active turn to steer"`). `QueuePreview`/`QueueIDs`/`QueueTexts` re-implement, three times, what `queueChangedDataLocked` (`:919-948`) already builds in one pass. Their doc says "consumed by both UIs via the appwire QueueState", which is not true anymore (no callers).

Impact. About 400 lines of production API with its own checks and error strings that no product path exercises. Tests exercise the legacy shape, so they do not test the path that actually runs (`AcceptClientMutation*`).

Fix plan (per Jesse's rules there is no backward-compat to keep; this is internal API):
1. Add `agent/queue_helpers_test.go` with `testEnqueue`, `testDrainAsSteer`, `testPromote`, `testCancel`, `testSteerFromUser`, `testQueuePreview` that call the real `clientMutation*` handlers (these helpers keep `"legacy_"` out of production).
2. Mechanical rewrite of test call sites (`sess.Enqueue(ctx, x)` becomes `testEnqueue(t, sess, x)`); about 500 sites, scriptable with `gofmt -r`/`sed`.
3. Delete the production wrappers, `QueuePreview/IDs/Texts`, `SteeringQueueSnapshot` (check `SteeringEntry` type is still used elsewhere first), the unused `With...DrainOnInterrupt`, `RestoreSessionFromMeta` (keep one in a `_test.go` helper), `DroppedModelFallbacksFromLastSwitch` if its tests move to the field, and the two dead helpers.
4. Doc: none (no doc mentions these). Update session.go lock comment that names `Enqueue/DrainAsSteer`.

Size: about 450 production lines removed, about 80 test lines added, about 500 test lines touched mechanically. Risk: low-medium (large but mechanical diff).

---

### agent-session-core-6: FollowUp is production-dead but wired through the drain ladder and the retirement check (medium)

Locations: `agent/session_queue.go:401-415,1617-1626`; `agent/session_lifecycle.go:985-1090` (`drainInputs.FollowUp`, `runFollowUp`, `selectDrainNextAction`), `:1455-1520` (`popFollowUp` and the awaiting guard comment), `:1576-1580`; `agent/session.go:492` (`followups []string`); `agent/session_retirement_admission.go:31`.

Evidence. `FollowUp` has zero non-test callers (finding 5). Production input comes from the durable queue only. Yet `processInputKindWithProvenance` pops a follow-up first (`popFollowUp()` at `:1496`), `selectDrainNextAction` has a top-priority `runFollowUp` rung with a dedicated field, the awaiting guard is documented around it, `ProcessInputKind`'s doc says it "drains per-turn follow-ups", and retirement counts `len(s.followups)` as pending work. Test counts mentioning it: `FollowUp` 41 (for example `fuzz_lf_drain_test.go` 11, `session_recovered_turn_start_test.go` 28 matches overall for the pattern).

Impact. A whole priority rung and in-memory-only queue (which, per the comment at `:1496`, "lose[s] nothing a restart could have recovered") that only tests can reach. It adds a branch to every drain decision and to the `drainInputs` table tests.

Fix plan. Needs Jesse's call that in-memory follow-ups are not a planned feature (YAGNI says delete). If agreed: remove `FollowUp`, `popFollowUp`, `followups`, `runFollowUp`, the `FollowUp` field in `drainInputs`, the retirement check, and update `selectDrainNextAction` table tests (`session_lifecycle_test.go`, `fuzz_lf_drain_test.go`). Tests that used `FollowUp` to create a second turn switch to the test queue helper from finding 5.

Size: about 120 production lines removed, about 150 test lines touched. Risk: low in production, medium for fuzz seed corpora that encode follow-up ops (regenerate seeds).

---

### agent-session-core-7: releaseRuntimeOnce is two lifecycles (terminal close vs retirement release) fused with 16 `if retirement` branches (medium)

Locations: `agent/session_lifecycle.go:409-770`; policy type in `agent/session_retirement_release.go:22-27`.

Evidence. 361 lines. `awk` over the range counts 16 `if retirement` / `if !retirement` conditions, spread through the whole body (`:452` ... `:735`), with the shared ordering (budget ctx, mark closing, cancel, join WaitGroups, close job manager, close artifact store, cleanup env, transcript close, events close) interleaved with policy-specific steps. The entire body sits in a bare `{ ... }` block (`:420`), which is leftover scoping from an earlier refactor. Error accumulation is `releaseErr = errors.Join(...)` only under `if retirement`, while terminal close logs warnings.

Impact. A step added for terminal close can silently be skipped (or wrongly run) for retirement. The step order is the real specification and cannot be seen.

Fix plan.
1. Split into `type releaseStep struct{ name string; terminal, retirement bool; run func(ctx) error }` listed in order in one slice, or two ordered step lists (`terminalSteps`, `retirementSteps`) that share named helper methods. A test can assert the two lists share their common prefix (this replaces reading 16 conditions).
2. Move `releaseRuntime`, `close`, `joinWithinCloseBudget`, env-work tracking (`:173-408`) and `releaseRuntimeOnce` into `session_close.go`.
3. Remove the bare block.
Tests: `session_retirement_release_race_test.go`, `session_lifecycle_test.go` close cases.

Size: about 0 net, function shrinks to under 100 lines. Risk: medium-high (ordering). Move in a no-op commit first.

---

### agent-session-core-8: The Session struct is a 274-field, 22-mutex god object whose lock contract lives in a comment (high)

Locations: `agent/session.go:109-1125`; lock contract `:280-330`.

Evidence. 1016-line struct, about 274 fields, 22 mutexes/RWMutexes (`eventsMu closeCtxMu memoryMu mu metaSaveMu goalUpdateMu notesUpdateMu responseSideEffectsMu drainAbandonedMu childCommittedSendMu queueEventsMu queuePersistMu clientMutationsInitMu restMu pendingJobNotifsMu readFilesMu attentionMu delegateDeliveryMu turnNameRetryMu releaseRetryMu outputReductionMu delegateAttentionWarnMu`), four `sync.Once`, four `WaitGroup`s, plus test hooks (`testOnlyBlockedSendEntered`, `session.go:173`). The `mu guards:` list at `:293-306` enumerates about 40 fields by name and omits many that the code locks under `mu` (for example the rest/awaiting counters `restGeneration`, `restOwedGeneration`, `quietRestBeforeInput`, `endReasonBeforeInput`, `resumeQuietRest`, `steeringParked`, `pendingSessionStart*`). Lock-order rules are scattered across field comments ("always acquired before mu", "acquired before mu by maybeAutoSave").

Impact. Nothing enforces which lock guards which field. Adding a field means guessing. This is the structural reason functions like finding 2 are long.

Fix plan. Do not do a big-bang split. Peel off cohesive sub-structs that already have their own mutex and a clear field set, each as a pure move:
1. `restState{restMu, generation, owedGeneration, quietBeforeInput, endReasonBeforeInput, resumeQuiet}` (see finding 10).
2. `sessionStartHooks{kind, result, inFlight, cond}` (4 fields + Cond, `session_init.go:2148-2300`).
3. `notificationRetry{...}` for the job-notification retry fields in `session.go:1380-1450`.
4. `eventBus{eventsMu, events, eventsClosed, closeCtx, closeSignal, authoritativeConsumer}` owning `sendEventContext`.
Each sub-struct gets its own file and a short invariant comment, and the `mu guards:` list shrinks accordingly. Use `go vet -copylocks` plus the existing race lanes (CI only).

Size: about 600 lines moved across 4 PRs. Risk: medium (mechanical, but lock-nesting changes need careful review).

---

### agent-session-core-9: Queue state has three representations reconciled by "reflect" functions (medium, architecture)

Locations: `agent/session_client_mutation_queue.go:665-685` (`reflectDurableInputQueue`), `:1352-1380` (`reflectDurableClientSteering`), `:1431-1555` (`restoreDurableClientMutationQueues`); `agent/session_queue_persist.go:1-148`; `agent/session_queue.go:120-170,993-1100`; `agent/session.go` fields `inputQueue`, `steeringQueue`, `steeringInFlight`, `publishedQueueRevision`.

Evidence. The authority is the client-mutation snapshot (`InputQueue`, `SteeringOrder`, `PendingExecutions`). `s.inputQueue` is a copy rebuilt on every change (`reflectDurableInputQueue` sets `s.inputQueue = queue`). `s.steeringQueue` is a merge of daemon-authored entries and a re-projection of the snapshot (`reflectDurableClientSteering` filters by `ClientMutationID == ""` and re-appends). A third store, `queues/<id>.json` (`queuePersistSubdir`), holds only daemon-authored steering (`persistQueuesSnapshot`, `daemonSourcedSteering`). The source comment at `session_queue.go:366-368` says "the legacy snapshot has one remaining authority: daemon-authored steering", and `daemonSourcedSteering` special-cases `SteeringKindHookContext` to avoid double injection (`session_queue_persist.go:138-148`).

Impact. Each queue operation must touch the snapshot, the mirror, the event, and conditionally the file. Ordering bugs between them are what the many "(kata ...)" races in comments describe.

Fix plan. This is an architecture decision for Jesse, not a refactor to start unasked:
1. Option A: make daemon steering a second journal in the mutation store (one authority), delete `queues/*.json` and `session_queue_persist.go`. Requires a state migration or an explicit "drop pending daemon steering on upgrade" decision; recovery-over-lockdown says it must not lose work, so it needs care.
2. Option B (smaller): keep the file but make `s.inputQueue` a computed read of the snapshot (delete the mirror and `reflectDurableInputQueue` writing it), leaving only the event publication.
Start with B: replace readers of `s.inputQueue` (`QueueDepth`, `queueChangedDataLocked`, `pendingQueueDepth`, ...) with reads of `s.clientMutations.snapshot()`.
Docs: S11 durable-intent row and S09 row (source of truth for queued input).

Size: B about 150 lines removed. Risk: medium. A is larger and needs a plan.

---

### agent-session-core-10: "Awaiting vs idle" resting state is derived in six places, with documented drift bugs (medium)

Locations: `agent/session_state.go:458-500` (`finishProcessingAtFailureBoundary`, `...AtRestoredFailureBoundary`), `:634-650` (`settleTerminalState`), `:657-686` (`recomputeRestoredState`), `:693-765` (`armAwaitingAtSettle`); `agent/session_tools_ask.go:696` (`deriveRestoredState`) and `deriveRestoredAskPending`; the inline state decision in `session_lifecycle.go:1458-1500` and `session_init.go:1572-1600`.

Evidence. The failure path comment at `session_lifecycle.go:1432-1436` records a bug where settling idle "diverg[ed] from restore's deriveRestoredState for the identical transcript". A separate `finishProcessingAtRestoredFailureBoundary` exists solely to re-run restore's rule at runtime. The "rest" machinery is five counters/flags (`restGeneration`, `restOwedGeneration`, `quietRestBeforeInput`, `endReasonBeforeInput`, `resumeQuietRest`) plus `restMu` held across an emit, with the timer closure at `session_state.go:733-760` re-checking everything.

Impact. Live and restored rules must agree by construction, and today they agree by test and comment.

Fix plan. Introduce one pure function `restingState(facts restFacts) SessionState` taking explicit facts (`pendingAsks`, `lastEndReason`, `hadOutput`, `goalKicked`, `queuePending`, `autonomyPending`, `closing`) and have `settleTerminalState`, `deriveRestoredState`, both failure-boundary helpers, and `recomputeRestoredState` call it. Add a property test that replays a transcript through the live settle path and through restore and compares results (this is the behavior that must not drift). Group the five rest fields into the `restState` sub-struct from finding 8.

Size: about 120 lines removed, 80 added. Risk: medium. Doc: S09 row mentions boundary derivation if you add one sentence.

---

### agent-session-core-11: One third of the slice is comments, and many narrate history, incident ids or reviewer findings (medium)

Locations: whole slice; worst offenders `agent/session_lifecycle.go` (1396 comment lines of 3413), `agent/session.go` (1251 of 2784), `agent/session_config.go` (638 of 1098), `agent/session_init.go` (726 of 2672).

Evidence. Counts across the 14 files: 6,712 comment lines out of 19,396 (34.6%). 39 comment lines cite a `kata xxxx` id, 61 cite `/par`, `WS<n>`, `round <n>`, `issue #N`, `PR #N`, or a bare `#NNN`, and 31 contain "used to / previously / formerly / no longer / legacy". Examples: `session_lifecycle.go:1470-1473` ("This used to read s.State() == SessionAwaiting alone, on the premise that ... used to clear askPending unconditionally"), `session_config.go:954-960` ("The previous default of 200 killed long-running agentic sessions"), `session_config.go:1045-1048` ("matching the pre-carve behavior"), `session_config.go:340` ("RoboRev #1806 member-3 Medium's fix depends on"), `session_init.go:1270-1272` ("/par A4", "/par #2"). Several comments exceed 20 lines inside a function body (`session_lifecycle.go:1141-1170`, `:1458-1485`, `session_client_mutation.go:815-854`). Docs also drift: `ProcessInput` says "thin delegate to ProcessInputKind" but calls `processInputKindWithProvenance` (`session_lifecycle.go:1096-1100`); `QueuePreview` claims both UIs consume it (finding 5).

Impact. History belongs in commit messages (Jesse's naming/comment rule). The invariant is buried, and the stale parts mislead.

Fix plan. Fold this into each structural PR: when a function moves, rewrite its comments to state the invariant and the reason in at most a short paragraph, and drop incident ids. Do not run a separate comment-only sweep (high churn, merge-conflict risk on the two hottest files). Optionally add a lint-style grep in `scripts/` to flag new `kata [a-z0-9]{4}` references, but only if Jesse wants that mechanism (see "Simplicity over mechanism").

Size: about 1,500-2,500 comment lines removed over the sequence. Risk: low.

---

### agent-session-core-12: Test seams and `testing.Testing()` forks live inside production code (medium)

Locations: `agent/session_config.go:318-808` (`testConfig`, 91 fields), `agent/session_init.go:63-65` (`testSpeedIO`), `agent/session_lifecycle.go:30-34` (`sessionLifecycleFault`, 18 call sites), `agent/session_init.go:2429-2434` (`sessionInitFault`, 7 call sites), `agent/session_client_mutation.go:680-702` (`SimulateCrashMidExecutionForTest`), `agent/session.go:173` (`testOnlyBlockedSendEntered`), `agent/session.go:2649-2660` (`sclock` nil fallback).

Evidence. 152 `testOnly` references in non-test package code and 337 in tests. `testSpeedIO` returns `testing.Testing() && !cfg.ForceRealIO && !cfg.testOnly.forceRealIO`, so every test of `NewSession` runs with no-sync job store and transcript writers (`session_init.go:520-523`, `:580-585`) while production runs fsync. `ForceRealIO` is an exported config field only so another package's black-box tests can opt back in (`session_config.go`, comment at `:304`). `sclock()` falls back to a real clock because "a few tests build a *Session struct literal directly without one". `SimulateCrashMidExecutionForTest` is an exported method used by one test in `server/`.

Impact. The most durability-critical paths (fsync) differ between the tested and shipped binaries. The 490-line `testConfig` block inside the production config file hides what the real config is.

Fix plan.
1. Move `testConfig` into `session_testhooks.go` (still package `agent`, but a file whose name states the intent) as a pure move, then audit each hook: hooks used by 0-1 tests get deleted or replaced by a behavior test.
2. Replace `testing.Testing()` with an explicit constructor option set by test helpers only (`newTestSession` already exists in many test files) so production code has no `testing` import. Keep one test per durability path that sets the real-IO option (the existing `BenchmarkNewSession` already does).
3. Delete `SimulateCrashMidExecutionForTest` by letting `server/transcript_parity_test.go` drive a real mid-turn close, or move it to an `export_test`-style shim in a `testsupport` file.
4. Remove the `sclock` nil fallback by fixing the struct-literal test constructors.

Size: about 200 lines removed, 490 moved. Risk: medium (hook removal changes tests, not production).

---

### agent-session-core-13: SessionConfig, schema.ConfigSnapshot and RestoreSessionConfig are three hand-synced copies of the same fields (medium)

Locations: `agent/session_config.go:36-317` (34 json-tagged fields), `agent/schema/config_snapshot.go:13-50` (same 34, "same names, same tags, same declaration order"), `agent/session_config.go:1005-1085` (`toSnapshot`/`configFromSnapshot`, 80 lines of field-by-field copy), and `agent/session_init.go:755-840` + `:905-967` (`RestoreSessionConfig` copied into `cfg` with about 25 assignments).

Evidence. The schema file states it "holds exactly the json-tagged fields of the engine's SessionConfig — same names, same tags, same declaration order". Adding a persisted knob requires edits in four places, with a round-trip test as the only guard. The restore copy block includes drift-prone special cases (`strings.TrimSpace(restoreCfg.ProviderIdleTimeout) != ""`, `AgentsDocPath` always overwritten, `ReasoningEffort` normalized then blanked on invalid).

Impact. Each new persisted option costs four edits and a test update. 1,720 `SessionConfig{` literals in the package make embedding impractical right now.

Fix plan.
1. Short term: in `agent`, define `type persistedConfig = schema.ConfigSnapshot` and embed it once in `SessionConfig` only if the literal-site churn is acceptable (it is not: about 1,720 test literals). So instead add a reflection-based test that asserts `SessionConfig`'s json-tagged fields equal `ConfigSnapshot`'s, and delete the hand-maintained converters in favor of a copy by field name for those fields (`copyJSONFields`). One generic function replaces 80 lines, and the test replaces the comment.
2. For `RestoreSessionConfig`, build `cfg` once through a small `func (c *SessionConfig) applyRuntime(restoreCfg RestoreSessionConfig)` method so the 25 assignments are in one named place, not inline in the 777-line function (do it as part of finding 1).

Size: about 100 lines removed. Risk: low-medium. Decision for Jesse: reflection helper vs generated code vs accept the duplication.

---

### agent-session-core-14: consumeModelStream is a 207-line closure state machine, and communicate-preview reset is implemented three times (low-medium)

Locations: `agent/session_stream.go:46-105` (`resetCommunicatePreviews`), `:306-513` (`consumeModelStream`), `agent/session_lifecycle.go:1945-1952` (the `communicatePreviewCalls` defer), `session_stream.go:322-327` (the recover that resets previews).

Evidence. Local state `toolArgs`, `toolNames`, `communicateText`, `communicatePreviewStarted`, `communicatePreviewOrder`, `streamedAssistant`, `assistantStarted`, `finished`, `contentSeen`, `firstContent`, `lastContent` captured by 6 closures. `ToolCallStart`, `ToolCallDelta` and `ToolCallEnd` cases (`:446-490`) each repeat "canonicalize name, ensure builder, write args, `noteContent`, maybe `emitCommunicatePreview`". The `EventCommunicatePreviewReset` emit loop appears in `processOneInput` (`session_lifecycle.go:1946-1950`), in the `consumeModelStream` recover (`session_stream.go:322-327`), and in `resetCommunicatePreviews`.

Fix plan. Extract `type streamConsumer struct{...}` with methods `onText`, `onReasoning`, `onToolCall(ev)`, `observe(err)`; collapse the three tool-call cases to one `onToolCall` that distinguishes by event type only where they differ (Start has no args; End replaces the builder). Make the three reset sites call `resetCommunicatePreviews`. Existing `session_stream*_test.go` (463 lines) and the fuzz tail test cover the behavior.

Size: about 60 lines removed. Risk: low.

---

### agent-session-core-15: Small duplicates and pass-through wrappers (low)

Locations and evidence.
- `clientMutationInput` is a one-line forward to `clientMutationInputWithMentions` (`session_client_mutation_queue.go:714-720`); `clientMutationQueueIDs` and `clientMutationQueueClientMutationIDs` (`:1275-1293`) differ only in the field read. `completeClientMutationTurn` / `completeClientMutationInterruptedTurn` (`:1871-1884`) wrap `completeClientMutationTurnWithState` with string literals `"terminal"`/`"interrupted"` that appear raw elsewhere (`ExecutionState = "accepted"` at 15 sites across the two files).
- `ProcessInput`, `ProcessInputKind`, `processInputWithProvenance`, `processInputKindWithProvenance` (`session_lifecycle.go:1098-1118`): four entry points for one function (three are one-liners).
- Closing checks: `closingOrClosedLocked` has 37 call sites; `isClosingOrClosed`, `errIfClosing`, `abortIfClosing`, `abortResponseProcessing` (`session_state.go:576-625`) each inline the lock/check/unlock, and 8 more sites inline `s.mu.Lock(); closed := s.closingOrClosedLocked(); s.mu.Unlock()` instead of calling `isClosingOrClosed`.
- Warning copy: the string `"transcript write failed: %v"` is formatted at 18 production sites (`session.go:2233,2398,2436,2584,2633,2716`, `session_lifecycle.go:2691`, `session_execution.go:306,333,430`, `session_queue.go:1274,1497`, `session_compaction.go`, `session_tools.go:1318`); `"steering admission failed: %v"` at 6 sites (`session_events.go:594`, `session_init.go:570,2286`, `session_queue.go:300`, ...). One test even matches the first literal (`session_poisoned_wake_gate_test.go:499`).
- Session-start hook delivery: `deliverSessionStartHookResult` and `deliverSessionStartHookResultForUserTurn` (`session_init.go:2231-2238,2283-2292`) differ only in `appendSteeringTurn` vs `deliverHookContext`; `appendPairedTurnVia` and `appendSteeringTurnDurablyForOwner` (`session.go:2388-2405`, `session_queue.go:1489-1500`) are the same "append + synced write + warn" body.
- `web_search` tool registration with the net-off egress gate is written twice (`session.go:1622-1631` and `session_tools_web.go:56-64`).
- `emitSteeringCarrierTurnFailure` is `emitTurnFailure` with one boolean (`session_events.go:275-301`), and its comment says it has exactly one caller.

Fix plan. One small PR per cluster: delete the one-line forwarders and rename callers; add `s.warnTranscriptWrite(err)` and `s.warnSteeringAdmission(err)` helpers (also fixes the copy-oracle test); make the closing helpers the only way to ask (`isClosingOrClosed()` everywhere outside `*Locked` code); call one hook-result delivery function with a delivery mode; share the web_search definition via a `webSearchTool(deps)` constructor; replace the string literals with typed constants (`executionAccepted`, `executionTerminal`, ...).

Size: about 200 lines removed. Risk: low.

---

### agent-session-core-16: Files are organized by history, not by responsibility (medium)

Locations: `agent/session_lifecycle.go` (3413 lines), `agent/session.go` (2784), `agent/session_init.go` (2672).

Evidence. `session_lifecycle.go` contains: close/teardown and env-work tracking (`:80-408`), runtime release (`:409-860`), the drain ladder and entry-kind routing (`:949-1100`), the turn loop (`:1118-2608`), transcript-health refusal predicates (`:1751-1935`), the per-kind `accept*Input` functions (`:2696-3170`) and job-notification filtering/formatting (`:3177-3413`). `session_init.go` contains state-dir canonicalization (`:93-205`), the constructors, model-fallback validation and `modelFallbackEligible`/`retryLoopDeclined` retry predicates (`:1842-1945`), plugin loading, session-start hook deferral, MCP init, and hook warning-text formatting (`:2447-2570`). `session.go` holds job-notification queueing (`:1176-1450`), model switching (`:1559-1950`), communicate result accessors, environment-context append and the whole transcript recording family (`:2097-2630`).

Impact. Navigation and merge conflicts. A reader cannot find "where does the session end an input".

Fix plan. Pure file moves in this order, one PR each, no code edits: `session_close.go` (close, env work, release), `session_input_accept.go` (`accept*Input`), `session_notifications.go` (job notification queue and filtering from session.go and lifecycle), `session_transcript.go` (the `appendTurn*`/`write*`/`record*` family), `session_model_switch.go` (merge into the existing `session_set_model.go`), `session_statedir.go` (path canonicalization), `session_hook_warnings.go`. Update the S09 row link list in `docs/product/subsystems.md` (it links `session.go` and `session_execution.go` by name). Verify with `git diff --stat` that only moves happened (name-only gate per memory).

Size: 0 net, about 6,000 lines moved. Risk: low (conflicts with in-flight branches).

---

### agent-session-core-17: Tests in the slice assert on human-readable copy (low)

Locations: `agent/session_lifecycle_test.go:650,728,769` (`"session is closing"`), `:1163,1166` (`"stuck in a loop"`, `"reasoning effort has been increased"`), `:1921` (`"held work"`); `agent/session_capabilities_test.go:169,174,197,238` (`"under restricted"`, `"On PATH: go=yes node=yes rg=no"`, `"Scratch (...): /scratch/s1"`); `agent/session_execution_test.go:305` (`"invalid notice"`); `agent/session_poisoned_wake_gate_test.go:499` (`"transcript write failed"`).

Evidence. `AGENTS.md` ("Copy Is Not a Test Oracle") forbids hardcoded phrasing assertions. Counts of `strings.Contains(..., "<8+ char literal>")` in slice-matching tests: lifecycle 9, config 8, capabilities 4, execution 1, init 1, client_mutation 2. Some are legitimate prompt-content checks (system prompt contains the custom agent text) but the lifecycle/capability ones assert wording.

Fix plan. Assert the structured field instead: error kind via `errors.Is` (a sentinel exists for closing in some paths), `WarningData.Source`/typed code for warnings, and for capabilities assert the `capabilityFacts` fields rather than the rendered line (`capabilityPreambleLines` already takes `capabilityFacts`, so test the facts-to-lines mapping by presence of a typed fact, or keep one golden file). Pair with finding 15 so the warning helpers expose a typed code.

Size: about 40 lines changed. Risk: low.

---

## Recommended fix sequence

Each step is its own PR, red/green-guarded by existing tests (run on CI), and each updates the S09 row in `docs/product/subsystems.md` only if a file link it names moves.

1. Finding 5 (retire the legacy test-only API behind test helpers) plus finding 6 (decide on `FollowUp`; Jesse to confirm). This is the lowest-risk way to shrink what finding 3 and 4 must refactor, and it forces tests onto the real `AcceptClientMutation*` path.
2. Finding 3 (generic mutation-handler helper). Removes the Accept wrappers while the tests are already pointed at one name.
3. Finding 16 (pure file moves) so later diffs are readable. Do it before the big function surgery but after the above to avoid conflicts.
4. Finding 1 (construction builder). Keep it as moves first, then dedupe.
5. Finding 13 (config mirrors) while touching `Restore`.
6. Finding 4 (claim-and-run driver), then finding 2 (turn loop extraction), in small steps.
7. Finding 7 (release steps) and finding 10 (resting-state function), with finding 8 sub-structs (`restState`, `sessionStartHooks`) introduced as each needs them.
8. Finding 12 (test seams out of production), then finding 17 and 15 as cleanups; fold finding 11 (comments) into every PR above rather than a separate sweep.
9. Finding 9 (queue state) is an architecture discussion. Raise Option A vs B with Jesse before any code; the first safe step is Option B.
10. Finding 14 anytime as an independent low-risk PR.
