# Tech-debt audit: agent-session-features

Slice: `agent/session_*.go` feature files (attention, notes, activity, jobtree, model, compaction, memory, skill, goal, outline, namer, retirement, worktree, plus the small session_* files). Worktree base: origin/main at a6c0b9c561. Read-only audit, no tests run.

## Inventory

- 82 non-test `agent/session_*.go` files, 56,071 lines. The 29 files named in the slice scope total 18,715 lines.
- 398 `agent/session_*_test.go` files, 182,157 lines (about 3.2x the production code). Of those, 20,548 lines sit in files named `*covtest*`, `*seed100*`, `*_more_*` or `*fuzz*`. Test files are out of scope here except where they prove a production symbol is test-only.
- Largest files in the slice: session_attention.go 2058, session_model_call.go 1894, session_jobtree_drain.go 1750, session_notes_rpc.go 1294, session_memory.go 1013, session_compaction.go 998.
- Functions of 130+ lines in the slice: `drainJobTreeWith` 484, `stageCompactionEffects` 260, `resumeWorktreeReentry` 202, `prepareCompactedSkillReloads` 181, `publishFoldTransaction` 166, `admitCompactedSkillReloads` 158, `isNotesSelfEchoTurn` 144, `sessionActivityRead.summary` 146, `advanceJobs` 139. (Larger ones such as processOneInput 663 and RestoreSessionFromMetaWithConfig 777 belong to the core slice.)
- 152 `testOnly` references in non-test agent code; `testConfig` is a 481-line struct with 91 fields (session_config.go:318).
- 168 issue-tracker / kata tags (`kata ab12`, `PRI-1234`, `#865`) embedded in session_* production comments.

Overall: the code is carefully reasoned, heavily commented and well covered, but the same few shapes get re-implemented rather than shared (timer backoff, file-resume probes, List/Load pairs), test seams have leaked into production API, and several functions and files have accreted past what one reader can hold.

## Product-model notes

- `session_fail_closed.go` is already tracked in docs/product/friction.md:509. Not re-litigated here; honor the recorded state.
- Finding 11 raises one item that touches the recovery-over-lockdown principle (retirement evidence fails closed on missing journals). That needs a conversation with Jesse before code changes.

---

## Findings

### agent-session-features-1 (high) Seven hand-rolled copies of the generation-guarded exponential-backoff timer

**Locations**
- agent/session_attention.go:1268 `scheduleRootAttentionRetryLocked`, :1338 reset
- agent/session_attention.go:1400 `scheduleDelegateAttentionArmRetryLocked`, :1445 reset
- agent/session_attention.go:1451 `scheduleStableDelegateAttentionRetry`, :1502 and :1518 resets
- agent/session.go:1386-1448 (`jobNotifyRetry`), agent/session_active_turn.go:168-194 (`turnNameRetry`), :327-331 (`runningTurnReleaseRetry`), agent/delegate_delivery.go:1029-1061 (`delegateDeliveryRetry`)
- agent/session.go:1132 `notificationRetry{active, delay, generation}` and :1145 `runningTurnReleaseRetryState`
- Separate backoff helpers: agent/session_fail_closed.go:133 `durabilityRetryBackoff`, agent/job_shell.go:720 `shellFinalizeBackoff` (both a `for range attempt { delay *= 2 ... }` loop)

**Evidence**
`notificationRetry` is only a data holder. Every user re-types the same sequence: `if active {return}; delay := x.delay; if delay <= 0 {delay = jobNotificationRetryInitialDelay}; active = true; generation++; gen := generation; clock.AfterFunc(delay, func(){ lock; if generation != gen {return}; active = false; ...; x.delay = min(delay*2, jobNotificationRetryMaxDelay) or reset to initial })`. `generation++` appears 14 times across session.go, session_active_turn.go, session_attention.go and delegate_delivery.go. `min(delay*2, jobNotificationRetryMaxDelay)` appears in 6 places. The copies have drifted: the stable rail doubles `s.stableAttentionRetry.delay` read at fire time while the others double the captured `delay` (session_attention.go:1491 vs :1319, :1438), and only the root rail has a "refused admission re-arms synchronously" branch. A new rail author has no single place to learn which variant is right. Three further doubling helpers exist (above).

**Impact**
Each fix to retry semantics (the root rail's refusal re-arm, the stable rail's reset-at-fire rule) has to be found and re-applied by hand. The comment weight (about 60 lines of prose in session_attention.go:1268-1534 alone) exists mostly to explain per-copy differences.

**Fix plan**
1. Add one small type in agent (for example `backoffTimer` in a new `agent/backoff_timer.go`) owning `active`, `delay`, `generation`, `Schedule(clock, fire func(gen) ...)`, `Cancel()`, `ResetDelay()`, `Stale(gen) bool`, `Next(delay)` (doubling to a ceiling that is a field, so `turnNameStoreRetryMaxDelay` fits). Keep the caller's lock model: the type is not self-locking; it is used under `attentionMu` / `mu` exactly as today.
2. Migrate one rail at a time, behavior preserving, starting with `jobNotifyRetry` (simplest). The existing retry tests (session_retirement_evidence_test.go retry/overlap/refused-rearm cases, session_jobtree_drain_backoff_test.go) are the guard; add one table test for the new type covering stale-generation, reset, ceiling.
3. Replace `durabilityRetryBackoff` and `shellFinalizeBackoff` with one `backoffDelay(attempt, initial, max)` helper (about 8 lines) in the same file.
4. Delete `notificationRetry` and `runningTurnReleaseRetryState` once migrated. The `runningTurnReleaseRetry` attempt counter becomes a field of its owner, not of the shared type.
5. Docs: no product-doc change; update the code comment on `notificationRetry` references only.

**Size** about 250 lines removed, 90 added (net -160), touching 5 files.
**Risk** medium: timing-sensitive code with subtle lock ordering. Mitigate by migrating one site per commit and keeping the callbacks' bodies untouched.

---

### agent-session-features-2 (high) `drainJobTreeWith` is a 484-line state machine with 15 locals and several near-identical "confirm with wake edge / re-kick once" returns

**Locations**
- agent/session_jobtree_drain.go:1036-1520 `drainJobTreeWith`
- agent/session_jobtree_drain.go:960-1034 (`DrainJobTree`, `drainJobTreeUsing`, `keepingReport`, `drainJobTree`) and :1682-1726 (`kickDriveTree`, `kickDriveTreeWith`, `kickDriveTreeWithFlush`)

**Evidence**
The loop carries `lastResult, stallStart, drivenUpdates, drainStartedAt, bgAnnounced, bgArmed, watchSuppressed, watchHistorySeen, quietPasses, prevOutstanding, wakePending, skipKick, woke` in one scope. The pattern `if takeDrainWake(wake) { quietPasses = 0; continue }` followed by `if skipKick { kick(ctx); quietPasses = 0; continue }` is repeated at the quiescence return (about :1195-1215), the terminal-residue return (:1255-1275) and the other abandonment returns, each with its own 5-10 line comment restating the same argument. The function-type `func(context.Context, string, []ImageAttachment, EntryKind) (string, error)` is spelled out 3 times in this file and 11 times across 7 test files. `kickDriveTree` -> `kickDriveTreeWith` -> `kickDriveTreeWithFlush` is a 3-deep pass-through chain whose middle layer only adds `true`.

**Impact**
The comment-to-code ratio shows the author needed prose to keep the invariants straight (PRI-2441, #329, #865, #2796 are all cited inside this one function). Any change to a verdict has to be re-checked against every other return.

**Fix plan**
1. Introduce a `drainPass` struct holding the loop-carried state (`quietPasses`, `prevOutstanding`, `wakePending`, `stallStart`, `bgAnnounced`, ...) with methods `p.takeWake()`, `p.confirmQuiet(kick)` (the "wake edge or skipped-kick means run another pass" guard, written once), and one method per verdict (notification turn, terminal residue, undisposed background jobs, stall give-up).
2. Name the process function type (`type inputProcessor func(...)`), delete `kickDriveTreeWith` (inline its single call), and keep `kickDriveTreeWithFlush` as the one recursive body.
3. Behavior-preserving; the existing drain tests (session_jobtree_drain_*_test.go, delegate_update_drain_recovery_test.go) are the guard. Do the extraction in steps, running the targeted tests on CI after each.
4. Keep the explanatory comments but attach each to the single method that now owns it.

**Size** about 150 lines net reduction (code and duplicated comments), 5 methods added.
**Risk** medium-high: ordering of wake consumption versus verdict reads is the whole point of the function. Do not reorder statements while extracting; move first, simplify second.

---

### agent-session-features-3 (medium) "Ungated" convenience wrappers and other production functions that only tests call

**Locations**
- agent/session_namer.go:402 `launchCompactionNamer` (calls `...Gated` with `ungatedFoldRevision`), :442 `nameSessionFromCompactionTurn`
- agent/session_compaction.go:446 `steerCompactionTranscriptReminder` (the `ForFold` variant is the only production caller, :751)
- agent/session_attention.go:436 `appendColdAttentionResolution`, :498 `delegateAttentionResolutionTurn` (one non-test caller, :1933, passing generation 0 implicitly)
- agent/session_memory.go:409 `readMemoryIndex`
- agent/session_notes_rpc.go:402-420 `AddSessionURLForTest`, `HumanNoteForTest`, `SessionURLsForTest` (exported production methods used only by server/appwire_notes*_test.go)
- agent/session_jobtree_drain.go:226 `ManagedJobsFinalizedForTest`, session_client_mutation.go:691 `SimulateCrashMidExecutionForTest`
- agent/session_queue_persist.go:43/86 `saveQueues`/`loadQueues` -> `saveQueuesFS`/`loadQueuesFS` (the FS-injecting form is exercised only by session_queue_persist_covtest*_test.go, 257 lines)
- agent/session_namer.go:400 `const ungatedFoldRevision = -1` is defined in the namer but is a fold-publication concept also used by session_queue.go:327,360 and session_compaction.go:447

**Evidence**
A repo-wide non-test grep of each name finds only its definition (`launchCompactionNamer`, `nameSessionFromCompactionTurn`, `appendColdAttentionResolution`, `readMemoryIndex`: 1 reference each, tests reference them in 2-5 files). Every production path goes through the `Gated`/`WithOpen`/`ForFold` form; the plain form exists to keep older tests compiling. The same pattern repeats for `nameSessionFromText` (one production caller, `Gated` variant used elsewhere) and `nameSessionFromTextFunc` (session.go:942), a second test-injection field living on Session beside `cfg.testOnly`.

**Impact**
Doubles the surface a reader must scan to find the real path, and the doc comments ("The ungated wrapper stays for callers outside a fold flush") describe callers that do not exist.

**Fix plan**
1. Delete the plain wrappers listed above; update the tests to call the real form (pass `ungatedFoldRevision` explicitly, or drop the parameter when it is always that value).
2. Move `ungatedFoldRevision` next to `newestPublishedFoldRevision`'s home (session_compaction.go) and rename to say what it means (`noFoldRevision`).
3. Fold `nameSessionFromTextFunc` into `cfg.testOnly`.
4. Turn the three `*ForTest` accessors into an `export_test.go`-style helper if `server` tests are the only users; otherwise keep, but document the reason once.
5. Keep `saveQueuesFS` only if a production caller needs the FS; otherwise inline the `afero.NewOsFs()` and have the covtest file use a temp dir.

**Size** about 90 lines removed, about 40 lines of test edits.
**Risk** low (compile-checked).

---

### agent-session-features-4 (medium) Six near-identical `ListActivity*` / `LoadSessionActivity*` pairs

**Locations**
- agent/session_activity_jobs.go:17 `ListActivityJobs` and :33 `LoadSessionActivityJobs`
- agent/session_activity_watches.go:12 `ListActivityWatches` and :28 `LoadSessionActivityWatches`
- agent/session_activity_delegates.go:37 `ListActivityDelegates` and :53 `LoadSessionActivityDelegates`
- agent/session_activity.go:386 `ActivitySummary` and :396 `LoadSessionActivitySummary`

**Evidence**
The three list pairs are byte-for-byte the same 14-line body except for the response type and the final `read.xxxPage` call: `normalizeSessionActivity`, overwrite `params.Scope`/`params.Limit`, build `SessionActivityReadParams{Ref, Scope}`, obtain `s.activityRead` or `retainedActivityRead`, `defer read.index.release()`, call the page method. 6 copies x about 14 lines. Note also that `normalizeSessionActivity` returns a ref string that all callers discard (`_, scope, limit, err :=`).

**Impact**
Any change to how a read is opened (the budget, release, error mapping) has to be made six times; the live/retained difference is just which constructor is called.

**Fix plan**
1. Add a generic helper in session_activity.go:
   `func withActivityList[R any](ctx, open func(ctx, appwire.SessionActivityReadParams) (*sessionActivityRead, error), params appwire.SessionActivityListParams, page func(*sessionActivityRead, context.Context, appwire.SessionActivityListParams) (R, error)) (R, error)` that normalizes, opens, defers release and pages.
2. Each public method becomes a 1-2 line call with `s.activityRead` or a closure over `retainedActivityRead`.
3. Drop the unused ref return from `normalizeSessionActivity`.
4. The activity tests (session_activity_*_test.go) cover both entry points; no new test needed. Update docs/product/session-activity.md only if the producer names change (they do not).

**Size** about 70 lines removed, 20 added.
**Risk** low.

---

### agent-session-features-5 (medium) Cold-attention writer API is a four-deep wrapper ladder, and the "default the opener" fallback is pasted 8 times

**Locations**
- agent/session_attention.go:372-376 (`appendColdDelegateNotificationDurablyWithOpen` -> `appendColdDelegateAttentionMessageDurablyWithOpen`), :436-444 (`appendColdAttentionResolution` -> `...WithOpen` -> `...ForGenerationWithOpen`), :498-502 (`delegateAttentionResolutionTurn` -> `...ForGeneration`)
- `if open == nil { open = transcript.OpenWriterForSession }` at agent/delegate_shell_repair.go:54, agent/delegate_delivery.go:127 and :520, agent/delegate_tree_restore.go:532 and :763, agent/session_attention.go:437, :1848, :1911
- Opposite check in the same file: agent/session_attention.go:385 `if open == nil { return false, errors.New("cold delegate attention writer opener is nil") }` and :452 for resolution

**Evidence**
The type `delegateAttentionWriterOpener` (session_attention.go:27) has exactly one production value (`transcript.OpenWriterForSession`); it is carried through `delegateTreeController.attentionOpen` (delegate_tree_controller.go:43,115,333), session_config.go:424 and delegate_delivery.go:113 so tests can swap it. Because the nil case is handled by defaulting in 8 places and by erroring in 2 more, the same input is "fine" or "an error" depending on the entry point. `appendColdDelegateAttentionMessageDurablyWithOpen` is also a 60-line function that folds the transcript twice (preflight `readDelegateAttentionFold`, then the writer's entries, then a third verify read), each time re-checking `reflect.DeepEqual(previous, message)`.

**Impact**
Naming ladder (`...DurablyWithOpen`, `...ForGenerationWithOpen`) hides which function is the one real entry point, and duplicated nil handling invites a ninth, divergent copy.

**Fix plan**
1. Make `openAttentionWriter(open delegateAttentionWriterOpener)` a single method/func that returns `transcript.OpenWriterForSession` for nil; call it at each site (or, better, store the resolved opener once in the controller at construction, so call sites never see nil). Remove the two "opener is nil" errors.
2. Collapse the ladders to one function each with a small options struct (`attentionResolution{ids, disposition, resumeGeneration}`); delete the `0`-generation convenience layers.
3. Extract the "content already present with identical message" check (done twice, :390 and :407) into one helper.
4. Guards: existing delegate_tree_restore, delegate_delivery and session attention tests; add one test that a nil opener means the real writer.

**Size** about 80 lines removed. Touches 5 files (delegate slice overlap, coordinate with that audit).
**Risk** low-medium.

---

### agent-session-features-6 (medium) Four independent "is this still the file I folded, only appended to" implementations

**Locations**
- agent/internal/foldcache/foldcache.go:101-160 (`tailProbeBytes = 64`, `probeTailFromHandle`)
- agent/session_attention.go:1700-1779 `delegateAttentionFoldCursor` (`delegateAttentionFoldTailBytes = 4096`, `resumes`)
- agent/session_activity_cursor.go:239-306 `checkSource` / `captureTail` / `captureSessionActivityTail` (64-byte tail, `hmac.Equal`)
- jobstore `fileCursor` rule (referenced at foldcache.go:135)
- Also agent/session_retirement_evidence.go:301 `retirementJournalCoherent` (a fifth variant: SameFile + size + mtime)
- agent/session_attention.go:128-212 `delegateAttentionFoldCache` + `extendDelegateAttentionFold`, which "refolds from byte zero and fromOffset/prior go unused, although the fold itself could resume"

**Evidence**
Each implementation compares `os.SameFile`, `info.Size() < offset`, and a tail-byte probe at `offset - len(tail)`. The attention cursor's own doc comment (session_attention.go:1700+) spends 12 lines justifying why it is "not delegateAttentionFoldCache (foldcache)": the foldcache variant gates on mtime, has no file-identity check, and shares an LRU. The activity cursor says it "follow[s] the existing foldcache incarnation rule" but re-implements it. The attention path therefore has two caches over the same fold (a foldcache memo for read paths that always refolds from zero, and the cursor for the resident session).

**Impact**
Three different tail lengths (64, 64, 4096) and three subtly different change rules for the same property. A bug found in one (for example the "regrew past the offset" case) must be fixed in all.

**Fix plan**
1. Extract the identity-and-tail probe into `foldcache` (or a small `internal/filecursor` package): `type Cursor struct{Info os.FileInfo; Offset int64; Tail []byte}` with `Capture(f, offset)` and `Resumes(f, info) bool`, tail length a parameter.
2. Reuse it from `delegateAttentionFoldCursor`, `checkSource`, and foldcache's epoch state. Keep the three call sites' tail sizes by passing them in; unify only after a measurement says the 4096 is needed.
3. Make `extendDelegateAttentionFold` honor `prior`/`fromOffset` (the fold is a left fold), which would let the foldcache memo and the cursor share one resume path and delete the "not foldcache for three reasons" justification.
4. Tests: foldcache_test.go plus the attention-cursor tests (TestReadExistingDelegateAttentionFold*), session_activity_test.go cache-loss cases. Update docs/product/session-activity.md if it names the cursor rules.

**Size** about 120 lines removed, about 60 added; touches foldcache (shared with another slice).
**Risk** medium: these are cache-correctness paths. Do step 1 and 2 without changing behavior, then step 3 separately.

---

### agent-session-features-7 (medium) Test-mode behavior and test seams live in production types

**Locations**
- agent/session_init.go:64 `testSpeedIO` returns `testing.Testing() && !cfg.ForceRealIO && !cfg.testOnly.forceRealIO`
- agent/session_config.go:296-316 exported `ForceRealIO bool`, :318-798 `testConfig` (481 lines, 91 fields)
- agent/session_memory.go:913-918 `memoryBoundaryWait` (`testing.Testing()` selects a 30 s budget instead of 250 ms)
- agent/git_snapshot.go:49 (`testing.Testing()` picks a timeout), llm/registry/load.go:1234
- agent/session_worktree_close.go:196-300: package vars `closeBudgetMintHook`, exported `ObserveCloseStopJoin`, `ObserveCloseStopJoinBudget`; exported funcs `CloseStopJoinBudget`, `CloseStopJoinBudgetForCascade` wrapping unexported `closeStopJoinBudget`
- agent/session_retirement_release.go:46 `setRetirementReleaseFault`, agent/session_tool_artifacts.go:21,29 `sessionArtifactStoreCacheDir`, `sessionArtifactStoreFactory` package vars

**Evidence**
- `testSpeedIO` makes every `go test` binary, including black-box and live E2E tests in other packages, skip fsyncs unless they opt in via an exported field. The doc comment at session_config.go:296-312 exists to explain why this exported field has to exist. Production code that behaves differently under `go test` means tests do not run the production path by default (the AGENTS.md rule "real data and real APIs").
- `memoryBoundaryWait` picks a different timeout under `go test`, so any test asserting projected memory content runs against a 120x longer budget than shipped.
- Close budget: `ObserveCloseStopJoin` is set by no one (grep: only invoked at session_worktree_close.go:266; cmd/evener tests set only `ObserveCloseStopJoinBudget`, whose doc says "ObserveCloseStopJoin remains unchanged for compatibility"). `CloseStopJoinBudget(remaining)` (:282) has zero callers anywhere. `CloseStopJoinBudgetForCascade` exists so a test in cmd/evener can call the unexported rule.
- `testConfig` has 91 fields, many one-shot `func()` observers, stored on every `SessionConfig`.

**Impact**
The compatibility hook and the dead exported function are API nobody uses; the `testing.Testing()` gates change what the tests prove; the config struct is a second implicit API that new tests extend by default.

**Fix plan**
1. Delete `ObserveCloseStopJoin` and `CloseStopJoinBudget` (no users). Collapse `CloseStopJoinBudgetForCascade`/`closeStopJoinBudget` into one function.
2. Replace `memoryBoundaryWait`'s `testing.Testing()` branch by making the budget a `SessionConfig` field that tests set explicitly (the `testOnly.memoryRealBudget` flag then disappears), and make `testSpeedIO` explicit the same way (a test helper that constructs configs sets `SkipFsync`). Needs Jesse's decision since it changes how ~hundreds of tests construct sessions; a single `newTestSession` helper keeps the change small.
3. Cap growth of `testConfig`: for each field with a single test user, move the observation to a package-level hook set by `export_test.go` or turn it into an interface the test supplies.
4. Update docs/developing-evener/testing.md to state the rule.

**Size** step 1 about 40 lines removed; steps 2-3 larger and discussion-gated.
**Risk** step 1 low; steps 2-3 medium (changes test defaults, which could surface hidden timing assumptions on CI).

---

### agent-session-features-8 (medium) session_notes_rpc.go is four files in one, including a 130-line hand-rolled HTML-entity canonicalizer

**Locations**
- agent/session_notes_rpc.go (1294 lines): RPC mutations :32-400; context rendering/escaping :424-690 (`neutralizeNotesFraming` :466, `canonicalizeNotesAngleBracketReferences` :478, `parseNotesAngleBracketReference` :515, `notesAmpLayerWidth` :546, `parseNotesNumericReference` :562, `isNotesReferenceDigit` :585); committed-cut publication :697-830; projection/compaction :831-962; steering-origin and self-echo detection :963-1294 (`isNotesSelfEchoTurn` 144 lines, `escapeNotesHistoryTurns`, `steeringOrigin`)
- agent/session_notes.go:372-376 `SanitizeNoteTextForDisplay`, `SanitizeURLValueForDisplay`

**Evidence**
The file name says "rpc" but roughly half of it is model-context rendering and steering classification. The entity canonicalizer handles `&LT;`, `&#x3C;`, `&#38;lt;` and arbitrary `amp;` layers so that a note cannot spell a framing tag; its comments cite "roborev's finding on the round that introduced this parser" (notesAmpLayerWidth doc). It is a patch-on-patch accretion with six helper functions to prevent a note from closing `<NOTES_CONTEXT>`.

**Impact**
Hard to find anything; and the escaping contract is far more complex than the threat needs. The mechanism is only necessary because the framing is angle-bracket markup embedded in untrusted text.

**Fix plan**
1. Split by responsibility with no behavior change: `session_notes_rpc.go` (mutations + persistence), `session_notes_context.go` (render, escape, projection, compaction reset), `session_notes_steering.go` (steering origin + self-echo).
2. Separately, propose replacing the entity canonicalizer with a simpler framing: for example render notes values as a JSON-quoted string, or escape only `<` and `>` after decoding references via `html.UnescapeString` until fixed point. This is a design choice to discuss with Jesse before any change (it changes what the model sees). Guard with the existing notes escape fuzz/tests.
3. Evergreen docs: no change for the split; if the framing changes, update the notes row in docs/product/subsystems.md.

**Size** split moves about 1300 lines without net change; the optional simplification removes about 130 lines.
**Risk** split low; framing change medium (model-visible).

---

### agent-session-features-9 (medium) Over-long functions in compaction, skill reload and worktree resume

**Locations**
- agent/session_compaction.go:575 `stageCompactionEffects` (260 lines, returns 4 closures) and :185 `publishFoldTransaction` (166 lines)
- agent/session_skill_reload.go:193 `prepareCompactedSkillReloads` (181) and :412 `admitCompactedSkillReloads` (158)
- agent/session_worktree_resume.go:34 `resumeWorktreeReentry` (202; two closures `reroot` and `notice` defined inline, 27 `return`s)
- agent/session_activity.go:404 `summary` (146), agent/session_activity_jobs.go:155 `advanceJobs` (139)

**Evidence**
`stageCompactionEffects` returns `(context.Context, func(events.EventKind, events.EventData), *foldCommit, func() int)` and `foldCommit` has a 30-line doc comment (session_compaction.go:456-500) explaining which field runs under which lock. `resumeWorktreeReentry` interleaves five unrelated validations (child-session guard, local-env check, existence check, canonical-project resolution, lock adoption), each with a notice path that differs slightly.

**Impact**
Review and change risk; lock-phase reasoning is carried in prose.

**Fix plan**
Extract by phase without changing order: for `resumeWorktreeReentry`, a `worktreeReentryPlan` struct built by a pure `planReentry(meta)` function and an `applyReentry(plan)` step; for compaction, move each staged effect (note claim, skill compaction handoff, namer launch, transcript-reminder steer) into a method on `foldCommit` so `stageCompactionEffects` only assembles them. Do one function per PR; compaction tests (session_compaction_*_test.go, session_skill_*_test.go) and the worktree resume tests are the guard.

**Size** neutral in lines, large readability gain; about 4 PRs of 150-300 lines each.
**Risk** medium (lock phases).

---

### agent-session-features-10 (medium) session_model_call.go holds a second subsystem: Responses continuation

**Locations**
- agent/session_model_call.go:569-860 (`applyResponsesContinuationAnchorPlanning`, `applyResponsesContinuationShadowEstimate`, `estimateResponsesContinuationShadow`, `responsesContinuation*` x12, the `responsesContinuationDisabledKey` type, :1432-1503 fallback request builders)
- :1505-1516 `budgetModelDispatchRequest` -> `budgetModelDispatchRequestWithBudget`
- :1211 `callModelWithFallback(..., _ int)`

**Evidence**
About 25 of the file's 60 functions (roughly 550 of 1894 lines) are about Responses-API continuation anchors, not "calling a model". `budgetModelDispatchRequest` is a pass-through that discards the budget (two callers in session_stream.go:196,260). `callModelWithFallback` takes a trailing `_ int` that its one production caller (session_lifecycle.go:2354) fills with `round`.

**Impact**
File is the second largest in the slice and mixes concerns; the dead parameter and pass-through add noise.

**Fix plan**
Move the continuation functions verbatim to `session_responses_continuation.go`; delete the unused `int` parameter (and update the 3 test callers); inline the two `budgetModelDispatchRequest` call sites to use `...WithBudget` and drop the wrapper or rename the one that stays. Pure moves are guarded by compile plus the existing session_model_* tests.

**Size** about 560 lines moved, about 15 removed.
**Risk** low.

---

### agent-session-features-11 (low) Retirement blockers are stringly typed, and "evidence missing" is treated as unretirable forever

**Locations**
- agent/retirement.go:30 `RetirementBlocker{Category string}`; literal categories at agent/session_retirement_evidence.go (23 `RetirementBlocker{` constructions), session_retirement_admission.go (4), delegate_tree_retirement.go (8)
- agent/session_retirement_evidence.go:313-366 `retirementJobJournal` / `retirementColdJobJournal`

**Evidence**
Category values counted across the retirement files: "notification" x7, "unsupported" x5, "job" x4, "watch" x3, "autonomous" x3, "input" x2, and one each of "delegate", "environment", "question", "turn". They are compared and constructed as raw strings with no constants. The retirement journal reader treats every stat failure, including a missing live journal, as lost evidence and refuses (doc: "Every stat failure fails closed, including absence"; the cold path doc says a missing journal with a surviving output dir "stays unsupported"); there is no recorded recovery that clears the "unsupported" state.

**Impact**
A typo in a category string silently never matches. On the product side, AGENTS.md says fail-closed needs a named restorer; here an "unsupported" session stays resident until a human intervenes.

**Fix plan**
1. Add `type RetirementCategory string` with constants and use them at the 40 sites (about 40 lines touched, compile-checked).
2. Raise the "unsupported forever" case with Jesse as a product question (what should restore a session whose journal is gone: rebuild from transcript, or allow retirement with a diagnostic). Only change behavior after that decision; record it in docs/product/friction.md if deferred.

**Size** step 1 about 40 lines. **Risk** step 1 none.

---

### agent-session-features-12 (low) Three control-character strippers plus load-time legacy cleanup in notes

**Locations**
- agent/session_notes.go:279 `stripNoteControls` (+ `isNoteControl` :381), :319 `stripDisplayControls`, :336 `stripTextControls` (+ `isTextControl` :364), :295 `sanitizeRestoredURLs`, exported aliases :372,376
- agent/session_notes.go:233 `clampNoteRunes` vs agent/internal/runetrim

**Evidence**
Three sibling functions differ only in which runes they keep, each hand-implementing `strings.Map` or a builder loop with its own predicate, and each explained by a paragraph about which call site needs which. `sanitizeRestoredURLs` and the comments at :278 exist to clean "rows written before that check" and "values persisted before the strip existed": legacy compatibility on the load path. AGENTS.md requires approval for new backward compatibility; these are existing, but nothing says when they can go.

**Fix plan**
1. One `stripControls(text string, keep func(rune) (replacement rune, keep bool))`-style helper (or a three-row policy table), with the three named functions as one-liners, then check `agent/internal/runetrim` for the clamp and reuse it.
2. Ask Jesse whether the load-time sanitization of pre-strip persisted notes can be dropped (the product is pre-1.0 per the recorded decisions); if yes, delete `sanitizeRestoredURLs` and the exported display aliases only if the hub does not use them.

**Size** about 50 lines removed. **Risk** low; fuzz/unit tests in session_notes_*_test.go guard.

---

### agent-session-features-13 (low) Misplaced and mis-named helpers

**Locations**
- agent/session_memory.go:107-120 `skillAvailableToSession`, `skillAvailability` (skills code in the memory file)
- agent/session_namer.go:400 `ungatedFoldRevision` (fold concept in namer)
- agent/session_outline.go:420 `joinNonEmpty` (returns a filtered slice, does not join; `slices.DeleteFunc` would do) and :118 `decodeSingleJSON`
- `firstLine` reinvented in execsupport/valueexpr/evaluate.go:345, agent/internal/hooks/hooks.go:558, cmd/evener-hub/internal/sshconn/version.go:1642 and agent/transcript_util.go:10 `firstLineClamp`
- agent/session_execution.go:454 `deliverCommunicate` and session_provenance.go:63-117 (`drainSteeringFor*`, `peekSteeringForTurn`) in files whose names say otherwise

**Evidence** as listed (opened each definition).

**Fix plan** Move `skill*` helpers to session_skill_source.go (43 lines, near-empty), move the revision const next to `newestPublishedFoldRevision`, rename `joinNonEmpty` to `dropEmpty` or replace it, and move the steering drains to session_queue.go. Consolidate the four `firstLine` helpers in a shared text helper only if two or more are in the same module (hooks and valueexpr are in different modules; skip those).

**Size** about 60 lines moved, no net change. **Risk** none.

---

### agent-session-features-14 (low) Narrating history and tracker-id comments

**Locations** (examples)
- agent/session_attention.go:154 ("the hand-rolled size-and-mtime gate this used to be"), :1386 ("the in-memory flag this replaced had")
- agent/session_capabilities.go:219 and :229 ("replaced the earlier line", "The macOS xcrun line that used to sit beside it")
- agent/session_active_turn.go:146 ("replaces the immediate notify() that stand-down used to fire")
- agent/session_config.go:291 ("matching the pre-refactor json:"-" behavior")
- agent/session_notes.go:130 ("costs only the walk this used to do")
- agent/session_lifecycle.go:1470 ("This used to read s.State() == SessionAwaiting alone")
- 168 occurrences of `kata xxxx`, `PRI-nnnn`, `#nnn` in session_* production comments

**Evidence** grep for `used to|previously|replaced|pre-refactor|legacy` in non-test session_* files returns 30+ hits.

**Impact** History belongs in commit messages (Jesse's naming-and-comments rule); tracker ids rot.

**Fix plan** Rewrite each comment to state the current invariant only and drop tracker ids that do not carry an explanation not already in the text. Do it opportunistically when touching the file, not as a sweep. About 150 comment lines.

**Size** about 150 comment lines. **Risk** none.

---

### agent-session-features-15 (low) Coverage-driven seams and test files in the session feature area

**Locations**
- agent/session_queue_persist.go:47,90 (`saveQueuesFS`, `loadQueuesFS`) used by agent/session_queue_persist_covtest_test.go (138 lines) and session_queue_persist_covtest2_test.go (119 lines) only
- 20,548 lines in `session_*` test files named covtest / seed100 / fuzz (for example session_tools_jobs_covtest2_test.go 1540, session_tools_worktree_fault_fuzz_test.go 1870)

**Evidence** The covtest files test error branches of the FS seam with read-only and failing filesystems; the seam has no production caller. File names ("covtest2", "seed100_more") describe how they were generated, not what they cover.

**Fix plan** For slice code, rename test files after the behavior they pin when touched, and fold covtest cases into the owning `_test.go` file. Do not delete coverage; this is an organization change. The test-suite audit slices should own the broader file hygiene. Flag any assertions on error text or mocked behavior to those slices (not inspected here).

**Size** moves only. **Risk** none.

---

## Recommended fix sequence

1. Finding 3 (delete test-only wrappers, move the revision const) and finding 10 step 1 (pure file move of Responses continuation, drop the dead parameter). Small, compile-checked, shrinks everything that follows.
2. Finding 7 step 1 (delete `ObserveCloseStopJoin`, `CloseStopJoinBudget`; collapse the budget wrappers). Dead code, no behavior change.
3. Finding 4 (activity List/Load generic helper). Low risk, immediate duplication win.
4. Finding 1 (one backoff timer type), one rail per commit, then replace the two doubling helpers. Do this before finding 2 because the drain loop depends on the attention retry rails.
5. Finding 5 (opener defaulting and wrapper ladders), coordinated with the delegate-slice audit that owns the other 5 call sites.
6. Finding 6 (shared file-resume probe), first step behavior preserving, then make the foldcache memo resume.
7. Finding 2 (`drainJobTreeWith` decomposition) and finding 9 (other long functions), one function per PR, after the above removes surrounding noise.
8. Finding 8 (split session_notes_rpc.go; discuss the framing simplification with Jesse) and finding 11 step 1 (typed retirement categories).
9. Discuss with Jesse before coding: finding 7 steps 2-3 (`testing.Testing()` gates and `testConfig` growth), finding 8 framing change, finding 11 step 2 (unretirable sessions), finding 12 step 2 (legacy note sanitization).
10. Findings 12 (step 1), 13, 14 and 15 opportunistically alongside the above.
