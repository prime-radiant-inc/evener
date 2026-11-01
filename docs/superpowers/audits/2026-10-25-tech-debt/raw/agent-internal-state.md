# Tech-debt audit: agent-internal-state

Slice: `agent/internal/{contextmgr, jobstore, delegatestore, foldcache, turnwindow, artifactstore, sessionlog, goal, bucketref, atif}` (non-test code).
Worktree: `.worktrees/debt-audit` (origin/main). Read-only audit; no tests were run.

## Inventory

| Package | Non-test LOC | Test LOC | Notes |
|---|---:|---:|---|
| contextmgr | 5,084 | 16,743 | `context_manager.go` alone is 2,838 lines / 104 funcs; 7 strategy files |
| jobstore | 4,747 | 14,637 | `output.go` 1,418, `store.go` 1,019, `watch.go` 484, snapshot readers 729 |
| delegatestore | 2,207 | 5,498 | `fold.go` 1,019 |
| foldcache | 807 | 1,331 | 362 of 807 lines are comments |
| atif | 572 | 2,326 | one 240-line conversion function |
| sessionlog | 268 | 1,360 | |
| goal | 274 | 494 | |
| bucketref | 185 | 171 | |
| turnwindow | 109 | 66 | |
| artifactstore | 106 | 607 | |
| **Total** | **~14.4k** | **~43.2k** | tests are 3x production code |

Tests: 352 `Test*` funcs in contextmgr, 319 in jobstore, 167 in delegatestore. Test files are named after coverage campaigns, not subjects (`cov_s4_jobstore_test.go`, `cov_w2tail_output_test.go`, `cov_w3sub_output_test.go`, `cov_s5_strategies_test.go`, `cov_w3sub_context_manager_test.go`, `*_covtest_test.go`). 92 assertions across delegatestore/jobstore/contextmgr tests match on `err.Error()` text (80 in delegatestore), which AGENTS.md forbids.

## Overall assessment

The slice is carefully engineered and heavily tested, and the crash-safety reasoning (torn tails, rollback, generation fences) is real, not decoration. The debt is structural, not sloppy: the same shapes were grown several times instead of being extracted once. The biggest items are (1) the compaction layer pipeline copy-pasted five times inside contextmgr, four of those copies belonging to strategies no documented surface can select, (2) four independent append-only JSONL journal implementations each re-solving torn-tail recovery, rollback, sync and closed/unusable latching, (3) a 2,838-line `context_manager.go` that mixes pressure estimation, masking, checkpoint, summarization routing and elicitation, and (4) a long tail of test-only wrappers, pass-through chains and history-narrating comments that signal LLM accretion. Comment density is very high (28% of `context_manager.go`, 45% of `foldcache.go`), and many comments narrate issue numbers and "as before this change" history that belongs in commit messages.

---

## Findings

### agent-internal-state-1: Compaction layer pipeline copy-pasted five times (HIGH, duplication)

Locations:
- `agent/internal/contextmgr/context_manager.go:685-787` (`MaybeCompact`), `:863-930` (`ForceCompact`)
- `agent/internal/contextmgr/strategy_obs_mask.go:52-135`
- `agent/internal/contextmgr/strategy_session_log.go:54-181`
- `agent/internal/contextmgr/strategy_checkpoint_pred.go:51-196`

Evidence:
- Each pipeline repeats the same scaffolding: the identical 5-line "One profile snapshot covers the window check..." comment, `profileSnapshot`/`effectiveWindowOf`/`discloseAssumedWindow` preamble, a `pressure` closure, the tripwire capture, and a trailing "reset token measurement" block.
- The block `cm.mu.Lock(); cm.lastInputTokens = 0; cm.historyLenAtMeasure = 0; cm.mu.Unlock()` appears 11 times outside `SetProfile` (context_manager.go:720,776,871,926; obs_mask:83,127; session_log:87,171; checkpoint_pred:82,188).
- The "measure before, mutate, measure after, emit `EventContextCompaction{Layer, TurnsBefore, TurnsAfter, EstTokensBefore, EstTokensAfter}`, call `handleCompactionTurn` if `history[0].Kind == TurnCheckpoint`" block appears 17 times (EventContextCompaction occurrences: context_manager 5, obs_mask 3, session_log 4, checkpoint_pred 5).
- The Layer-4 summarize block (build `result, err := summarizeFold/summarizeWithLLM`, `emitFn(EventWarning, summarizerFailureWarning(err))` on error, else emit and `handleCompactionTurn`) is byte-for-byte the same in `MaybeCompact`, `session_log`, `checkpoint_pred` and nearly in `ForceCompact`.
- Drift already exists: `SessionLogStrategy` calls `summarizeWithLLM` (no `checkpointed` carry) while `MaybeCompact` and `checkpoint_pred` call `summarizeFold(..., checkpointed)`; the tripwire is applied as `p = Threshold` in `MaybeCompact` but as `|| tripwire && assumptionStillGoverns(...)` in the strategies. `context_manager.go:1294` itself documents "Opt-in strategies ... do not carry earlier summaries yet; that gap is tracked in #4181", which is drift caused by the copy.

Impact: every fix to compaction accounting (the #4564 tripwire, #634 baseline correction, profile-snapshot discipline) must be made in up to five places; the doc comments show past fixes had to be repeated by hand.

Fix plan:
1. Add one unexported helper in contextmgr, e.g. `func (cm *Manager) applyLayer(ctx, emit, history *[]schema.Turn, prof *provider.Profile, name string, fn func() (changed bool))`, that does before/after estimate, emit, `handleCompactionTurn` when `history[0]` is a checkpoint or summary, and sets a `compacted` flag; plus `func (cm *Manager) resetMeasurement()` replacing the 11 lock blocks.
2. Add `func (cm *Manager) summarizeLayer(...)` for the shared Layer-4 block.
3. Express `MaybeCompact`, `SessionLogStrategy.ManageContext`, and `ForceCompact` as short lists of layers over that helper. Characterization tests already exist (`maybecompact_fc1_seqfuzz_test.go`, `forcecompact_doublelayer_seqfuzz_test.go`, `compaction_kind_test.go`); run them to prove behavior parity before and after.
4. Do this after, or together with, finding 2 so the deleted strategies are not refactored first.
5. Decide explicitly whether session-log should gain the `checkpointed` carry (a behavior change, TDD, separate commit).

Size: roughly -350 net lines production, tests unchanged. Risk: medium (hot path, concurrency around `cm.mu`); mitigate by keeping the extraction mechanical and landing it before any behavior change.

### agent-internal-state-2: Four experimental strategies unreachable from any documented surface (HIGH, dead-code)

Locations: `strategy_obs_mask.go` (190 lines), `strategy_checkpoint_pred.go` (274), `strategy_memory_crystals.go` (187), `strategy_recursive_distill.go` (219); `agent/session_init.go:277-284`; `agent/session_config.go:185`; `cmd/evener/main.go:360`; `cmd/evener-hub/internal/launchconfig/schema.go:166`.

Evidence:
- `session_config.go:185` documents `compact|session-log|ooda`; the CLI help at `cmd/evener/main.go:360` says `compact|session-log|ooda`; the hub launch-option choices at `launchconfig/schema.go:166` list `compact, session-log, ooda`. `selectStrategy` still accepts `obs-mask`, `checkpoint-pred`, `memory-crystals`, `recursive-distill` (session_init.go:277-284) but nothing in cmd/ or the hub can offer them.
- `context_manager.go` itself says `ObservationMaskThreshold` and `ThinkingClearThreshold` "are not used by the default compaction path ... They remain here for experimental Strategy implementations".
- Production lines: 870; test lines for the four: 199 + 437 + 306 + 264 + part of 122 (`strategy_aux_failure_test.go`) + 158 (`cov_s5_strategies_test.go`) = ~1,490.
- They also drive cross-package machinery: `WithPostFoldInjectionCallback` / `WithBaselineQuery` (context_manager.go:96-175) exist partly for memory-crystals and recursive-distill, and `agent/session_compaction.go:625-633,766` wires them.
- `auxFailure` (`context_strategy.go:57-69`) exists for the "opt-in memory strategies" and is used only by crystals and distill (plus session-log).

Impact: ~2,350 lines to maintain and keep correct against every compaction invariant change (finding 1), for modes nobody can select without hand-editing a launch arg.

Fix plan: needs Jesse's call (this removes accepted config values; per rules no compat shim without approval). Proposal: delete the four strategy files and tests, remove their `selectStrategy` cases (unknown values then fail with the existing "unknown context strategy" error), delete `auxFailure` if session-log is its only user (it also uses it, so keep a smaller form or inline), collapse the post-fold callbacks to only what OODA needs, and drop `ObsMask`/`CheckpointPred` helpers (`aggressiveMaskObservations`, `predictiveCheckpoint`). Update `docs/product/subsystems.md` S09 row if it names strategies. Verify `grep -rn` for the four names across `docs/` finds only historical plans/specs.

Size: about -870 prod, -1,490 test. Risk: low technically; product risk is only if someone uses them through hand-built launch args, hence ask first.

### agent-internal-state-3: `context_manager.go` is a 2,838-line, 104-function god file (MEDIUM, complexity)

Locations: `agent/internal/contextmgr/context_manager.go` (whole file); contrast `checkpoint_format.go` which was already split out.

Evidence: one file contains context-key plumbing (`:96-205`), the `Manager` and usage accounting (`:299-436`), window/pressure estimation (`:428-636`), `MaybeCompact`/`ForceCompact` (`:685-930`), observation masking and tool-result summarization (`:937-1165`), thinking clearing (`:1172`), checkpoint collection and formatting (`:1239-1720`), partner-message carry and condensation (`:1721-1960`), summarization routing with model fallback (`:2044-2230`), summary reply validation (`:2231-2370`), skill-note elicitation (`:2397-2515`), `summarizeFold` (`:2536-2720`, 185 lines), and string helpers (`:2759-2838`). The test file for it is 3,487 lines.

Impact: navigation and review cost; every PR conflicts here; the size hides the duplication in finding 4.

Fix plan: pure file moves within package `contextmgr`, no API change, in this order: `pressure.go`, `observation_mask.go` (maskObservations, summarizeToolResult, parse helpers), `checkpoint.go` (checkpointData, collect/format), `partner_carry.go`, `summarize.go` (routing, reply checks, `summarizeFold`), `elicit.go`, `compaction_context.go` (the `With*` context plumbing). Move matching tests the same way. Use a formatter-only diff; no logic edits in the same commit. Split `summarizeFold` into `renderFoldHistory` (the turn loop) and the request/response half.

Size: ~0 net, 3,000 lines moved. Risk: very low.

### agent-internal-state-4: Turn-to-text renderers duplicated seven ways, one byte-splitting runes (MEDIUM, duplication)

Locations:
- `context_manager.go:2587-2660` (`summarizeFold` inline loop), `:2452-2517` (`renderHistoryForElicit`/`renderTurnForElicit`), `:1721` (`partnerMessagesMarkdown`)
- `fork_summarize.go:47-91` (`buildSummarizePrompt`)
- `strategy_memory_crystals.go:122-143` (`crystallize`)
- `strategy_recursive_distill.go:151-175` (`microSummarize`)
- `fork_summarize.go:104` (`truncate`), `context_manager.go:2565` (local `truncText`)

Evidence: the loop `case TurnAssistant -> "Assistant: " + truncate(text, N)`; `case TurnTool/TurnToolResults -> for ContentToolResult -> "Tool(name)[ ERROR]: " + truncate(fmt.Sprint(content), N)` is written five times with caps of 100, 200, 200, 300, 200 and an assistant cap of 200/500/500. `truncate` (fork_summarize.go:104) and the closure `truncText` (context_manager.go:2565) are the same function; both slice bytes (`s[:limit]`), which can cut a UTF-8 rune in half and send an invalid string to the model, while `runetrim.Cut` already exists and is used elsewhere in the same package (context_manager.go:1681, 1902; strategy_ooda.go:58). More byte slices: `text[:5000]` (:2619), `content[:200]` (:2652), `cmd[:60]` (:1106), `s[len(s)-rem:]` in `renderHistoryForElicit` (:2459).

Impact: a real correctness bug class (invalid UTF-8 in summarizer prompts for non-ASCII sessions) repeated at 6 sites; five renderers drift on caps and labels.

Fix plan: TDD first: a test with a multibyte string straddling the cap through each renderer, asserting valid UTF-8. Then add `renderTurnLine(t schema.Turn, caps renderCaps) string` (one home, e.g. `turn_render.go`) and make `truncate` call `runetrim.Cut`; replace the five callers (delete if finding 2 removes crystals and distill). Keep `renderTurnForElicit` separate only because it deliberately does not truncate; express it as the same helper with zero caps.

Size: -120 net lines. Risk: low-medium (prompt text changes must be byte-identical for ASCII; assert that in a characterization test over existing fixtures).

### agent-internal-state-5: Four append-only JSONL journal implementations re-solve the same crash-safety problems (HIGH, duplication, cross-cutting)

Locations:
- `agent/internal/jobstore/store.go:152-840` (torn-tail recovery `:637-743`, rollback `:786-808`, `ensureOpenLocked :745`, `syncLocked :757`)
- `agent/internal/delegatestore/store.go:14-285` (`initializeOrRecover :160`, `rollbackLocked :235`, `latchRollbackFailureLocked :251`, `ensureUsableLocked :257`, `fileOps :14`)
- `agent/transcript/transcript.go` and `append_tail.go` (`ErrRollbackFailed`, `rollbackAppendLocked`, partial-line handling at `:601-815`; 1,212 + 248 lines)
- `agent/internal/sessionlog/sessionlog.go:109-190` (`tornTail`, `appendToDisk`)

Evidence: all four implement open-or-create, validate, truncate a torn trailing line, append with fsync, roll back by truncate+seek+sync on failure, latch an "unusable after failed rollback" state, and a closed latch. `jobstore.rollbackAppendLocked` and `delegatestore.rollbackLocked` are the same sequence (truncate, seek, sync, join errors) with different error wording. Three different fault-injection seams are used for the same purpose: `afero.Fs` (jobstore, sessionlog, jobstore output), a `fileOps{write,sync,truncate}` struct (delegatestore), and a `marshal func(any)([]byte,error)` field on `SessionLog` plus a `marshal` parameter on `writeOutputMetaFileFsSyncMarshal`. Event shapes also diverge: jobstore uses one flat 45-field `Event` union (event.go:30-85); delegatestore uses typed per-kind payload pointers with `eventKindSpecs` (fold.go:169-273).

Impact: a crash-recovery fix (for example the unterminated-tail handling) has to be rediscovered in each journal; three distinct test harnesses must be maintained; behavior differs on identical failure inputs (jobstore repairs a torn tail that parses as complete JSON by appending a newline, delegatestore truncates, sessionlog only remembers `tornTail` and writes a blank separator).

Fix plan: needs a design conversation with Jesse before code (architectural). Sketch: new `agent/internal/journal` package owning `Appender{Open, AppendLine(s), Truncate-to-offset rollback, Sync, Close, Unusable latch}` over one file-ops seam (afero or a tiny interface), with torn-tail policy passed as a func. Migrate in order: sessionlog (smallest, lowest risk), delegatestore, jobstore, transcript last. Each migration keeps the existing package's tests green and deletes its local rollback code. Do not change on-disk formats. Update `docs/product/subsystems.md` ownership row for journal recovery.

Size: -500 to -700 net across packages; +250 new. Risk: high (durability code); every step must keep fault-injection tests passing; run on CI only.

### agent-internal-state-6: jobstore fold cache is four copies of one pattern and three overlapping caching layers (MEDIUM, duplication)

Locations: `jobstore/store.go:138-150` (`foldCache` struct), `:848-943` (`fold*HitLocked`, `noteFolded*Locked`), `:945-1019` (`cloneFold*`); `agent/internal/foldcache/foldcache.go`; `delegatestore/store.go:49-80`.

Evidence:
- `foldJobsHitLocked`, `foldOrderedHitLocked`, `foldWatchesHitLocked`, `foldSendsHitLocked` differ only by the field and clone function; so do the four `noteFolded*Locked`. `cloneFoldJobs` and `cloneFoldOrdered` copy the same 8-line field block (`LastActivity, EndedAt, ExitCode, StructuredResult, StructuredResultValid, Provenance, NotificationProvenance`) twice (`:954-964`, `:975-985`) and `event_clone.go` already has `clonePtr`, `cloneSlice`, `cloneCausal` helpers.
- The package comment in `foldcache.go:1-32` says it "mirrors two proven patterns" (jobstore's `fileCursor` and the hub navigation cache) and restates the same staleness rule; `jobstore` keeps its own writer-owned cursor+generation+fold cache, `foldcache.Cache` serves readers, and `delegatestore.Load` has no cache and re-reads, re-decodes, re-folds, and re-clones the whole log on every call (`loadLocked`, store.go:58-80).

Impact: any new fold output (a fifth projection) means two more near-identical functions plus another clone function; three cache dialects to reason about.

Fix plan: replace the four `ok` flags and paired methods with a generic `cachedFold[T any]{ok bool, v T}` plus one `func hit[T](s *Store, c *cachedFold[T], clone func(T) T) (T, bool, error)`; factor `cloneJobRecord(*JobRecord) *JobRecord` and use it in both map and slice cloners. Leave the foldcache/jobstore relationship alone for now, but record in `foldcache.go` why jobstore's writer-owned cursor cannot use it, in one short paragraph instead of the current dozen. Optionally evaluate backing `delegatestore.Load` with a foldcache-style incremental path (measure first; not a debt fix by itself).

Size: -90 lines. Risk: low-medium; existing `TestStoreIncrementalReloadIndependentSnapshots` and fold fuzz tests guard it.

### agent-internal-state-7: Parallel path-based and fd-based output snapshot readers (MEDIUM, duplication)

Locations: `jobstore/output_snapshot.go:63-427` and `jobstore/output_snapshot_fd.go:17-302`; also `output.go:408-520` (`ReadWindow`, `readRangeLocked`).

Evidence: pairs `readOutputRangeSnapshotOnce` / `readOutputRangeSnapshotFromFileOnce`, `readOutputRangeSnapshotAttempt` / `readOutputRangeSnapshotFromFileAttempt`, `readOutputSnapshotOnce` / `readOutputSnapshotFromFileOnce`, `readOutputSnapshotAttempt` / `readOutputSnapshotFromFileAttempt`, `readOutputRawSnapshotWindow` / `readOutputRawSnapshotWindowFromFile`. The bodies share the `OutputWindowSnapshot{Start, End, TotalBytes, RetainedStart, RetainedStartPartial}` construction, the `view.total-view.fileStart != retainedBytes` check, and the `Truncated = view.visibleStart > 0 || offset > view.visibleStart || end < view.total` expression verbatim. A comment at output_snapshot.go:140 says the two "intentionally remain separate", and the fd variant needs an `outputSnapshotFileFS` shim (`:271-302`) to feed path-based metadata readers.

Impact: about 730 lines in two files that must be kept in step; a fix to `Truncated` or bounds logic goes in two places (and a third in `OutputStore.readRangeLocked`).

Fix plan: keep the observable differences (error precedence, generation fences) but extract the pure middle: `buildRangeSnapshot(view outputView, selectRange, retainedBytes) (OutputWindowSnapshot, offset, end int64, err error)` and `finishSnapshot(...)`, and have both flavors call it; the file-opening and fence calls stay per flavor. Pin both flavors with one table test over shared fixtures before refactoring.

Size: -150 lines. Risk: medium (they are marked separate because of subtle error-precedence semantics); do it only behind the existing `output_snapshot*_test.go` and fuzzers.

### agent-internal-state-8: Constructor and wrapper ladders with test-only entry points in production code (MEDIUM, slop)

Locations: `jobstore/output.go:125-165` (`OpenOutput`, `OpenOutputNoSync`, `CreateOutput`, `CreateOutputNoSync`, `openOutputFs`, `openOutputFsNoSync`, `createOutputFsWithSync`, `openOutputFsWithSync`), `:880-905` (`writeOutputMetaFile` -> `Fs` -> `FsSync` -> `FsSyncMarshal`), `jobstore/store.go:152-172` (`Open`, `OpenNoSync`, `openFs`); `agent/jobs.go:650-662`.

Evidence: `writeOutputMetaFile` and `writeOutputMetaFileFs` have no non-test callers (only `writeOutputMetaFileFsSync` is called at output.go:781,845); they carry a `//go:fix inline` directive for no consumer. `openOutputFs` and `openOutputFsNoSync` are only reached from tests and `OpenOutput*`. `OpenNoSync`/`OpenOutputNoSync`/`CreateOutputNoSync` ship in the production API; the only non-test consumer is `newJobManagerNoSync` (jobs.go:660), which exists to feed tests.

Impact: eight constructors for what is one function with two booleans; readers must trace four wrapper hops to see how the metadata file is actually written.

Fix plan: collapse to `openOutput(fs, path, capBytes, opts outputOpenOpts{create, noSync bool})` with `OpenOutput`/`CreateOutput` as the only exported entry points, and one `writeOutputMeta(fs, path, meta, syncWrites)`; let tests pass options. Move the `NoSync` variants behind `_test.go` helpers or an `export_test.go` style test constructor so they leave the production API. Delete the two unused wrappers.

Size: -80 lines. Risk: low. Test files calling the removed names (about 60 call sites) need a mechanical update.

### agent-internal-state-9: Pass-through and test-only methods on `Manager` (LOW, dead-code)

Locations (`agent/internal/contextmgr/context_manager.go`): `Pressure :428` -> `estimatePressure :437` and `EstimatePressure :555` (three names, one body); `summarizeWithLLM :2519` -> `summarizeWithLLMSteered :2525` -> `summarizeFold :2536`; `estimateUsedTokens :538`; `strategy_session_log.go:183 sessionLogCheckpoint`; `sameEstimatorTarget :406`; `EstimateUsage :590` (a one-line wrapper over `EstimateStatus`).

Evidence: grep across `agent/` shows `EstimatePressure(` is called only from three test files; `summarizeWithLLMSteered(` has no production caller except the wrapper above it; `estimateUsedTokens(` has no caller other than its definition; `sessionLogCheckpoint(` has no production caller (production uses `sessionLogCheckpointWithMeta`); `sameEstimatorTarget` forwards to `llm.EstimatorTargetsEquivalent` with no added logic. `ApplyThresholdScale` (`:600`) is a test-only configuration helper (`cfg.testOnly.compactionThresholdScale`) living in the production API.

Fix plan: delete `EstimatePressure`, `estimateUsedTokens`, `summarizeWithLLMSteered`, `sessionLogCheckpoint`, `sameEstimatorTarget`; make `SessionLogStrategy` call `summarizeFold(..., "", nil)` directly; fold `Pressure` and `estimatePressure` to one name. Update the handful of test call sites. Do after finding 2 so tests are not edited twice.

Size: -45 lines. Risk: very low.

### agent-internal-state-10: Checkpoint text is a rendered markdown format parsed back by a 680-line hand-written parser, with three legacy formats (MEDIUM, compat-legacy)

Locations: `contextmgr/checkpoint_format.go:1-680` (fence tracker `:503-630`, `extractCheckpointConversation :292-322`, `extractCheckpointJSON :653-680`), consumer `context_manager.go:1294-1306`; `strategy_session_log.go:233-268` (`extractOriginalPrompt` reading "current and legacy checkpoints").

Evidence: each compaction re-reads the previous checkpoint's text to carry the conversation, working notes and earlier summaries forward (`collectCompaction`). To make that round-trip safe the file implements a CommonMark-ish fence tracker (`fenceTracker`, `fenceRun`, `fenceRunInList`, `listMarker`), heading-span scanning, and marker de-framing. On top of the current format it still parses: `<conversation>` JSON blocks (`:299-308`), `<user_messages>`/`<agent_responses>` XML-style tags (`:311-321`), and "Original task:" (`:669-677`), explicitly commented "Backward compatibility ... migrating into the new conversation shape".

Impact: ~680 lines of parsing code for data the Manager itself produced moments earlier; the three legacy branches have no producer in the codebase today. The structure (conversation entries, notes, earlier summaries) exists in `checkpointData` before it is rendered, then is re-derived by text parsing.

Fix plan (needs Jesse's decision on both parts, since it touches durable transcripts and "no new backward compat without approval" cuts both ways):
1. Legacy removal: decide the support window for sessions whose transcripts hold pre-fenced checkpoints; if none, delete the `<conversation>`, `<user_messages>`, `<agent_responses>` and "Original task:" branches and their tests.
2. Longer term (design discussion): carry the structured sections on the checkpoint `schema.Turn` (a typed side field) so the next fold reads them directly and the markdown is purely presentation; then the fence tracker only guards model-authored summary text and can shrink to the summary path.

Size: step 1 about -120 lines; step 2 about -400 lines plus a schema change. Risk: medium for step 1 (resumed old sessions), high for step 2.

### agent-internal-state-11: Heavy "narrating history" and issue-number comments (MEDIUM, slop)

Locations: `context_manager.go` (788 of 2,838 lines are comments; 19 issue or kata references), `foldcache.go` (362 of 807 lines), `jobstore/store.go:60-150` (a 60-line comment block over `fileCursor` and `foldCache`), `delegatestore/event.go:63-71`, `strategy_recursive_distill.go:129` ("as before this change"), `jobstore/store.go:74,90,847` ("exactly as before"), `jobstore/watch.go:23`, `delegatestore/read_events.go:128`, `atif/atif.go:263-296` (a 30-line justification with a kata id for one `default:` arm), `contextmgr/context_manager.go:44` ("ruled 2026-08-06"), `:490` ("A zero window used to ...").

Evidence: comments of the form "was not hypothetical. TURN_FAILURE and HOOK_COMPLETED both landed in this position ... (kata 3d56)" and "A zero window used to ..." describe past states. The user's rule: comments explain what and why now; history goes in commit messages.

Fix plan: during the file splits in finding 3, trim comments to the invariant plus the reason it exists; keep the genuinely load-bearing crash-safety reasoning (fileCursor CRASH SAFETY paragraph, foldcache residual-risk statement) but cut each to a short paragraph; replace issue/kata archaeology with one-line `// see #NNNN` only where a test pins the behavior. Do not touch whitespace-only formatting separately.

Size: about -700 comment lines. Risk: none to behavior; review cost only. Do it package by package, not in one sweep.

### agent-internal-state-12: Tests assert on error and copy strings, which AGENTS.md forbids (MEDIUM, test-debt)

Locations: `delegatestore/store_covtest_test.go` (27 hits, e.g. `:32 "short write"`, `:163 "rollback truncate"`, `:268 "missing version header"`), `delegatestore/fold_covtest2_test.go` (26 hits), `delegatestore/fold_test.go` (7), `delegatestore/store_test.go` (8), `delegatestore/read_events_test.go` (5), `delegatestore/delegatestore_covtest_test.go:35-100` (e.g. `` `delegate "dlg_1" does not exist` ``), `jobstore/cov_s4_jobstore_test.go` (4), `contextmgr/strategy_ooda_test.go:268,324 "session log truncated"`, `strategy_session_log_test.go:316`.

Evidence: 92 matches of `Error(), "..."` in these three packages, 80 in delegatestore alone. AGENTS.md: "Never assert on phrasing: no test may check that copy, error messages ... match a hardcoded phrase". Files are also named by coverage wave (`cov_w2tail`, `cov_w3sub`, `cov_s5`) rather than subject, which hides what is covered.

Fix plan: introduce sentinel errors (or typed errors) for the distinct failure kinds the tests are probing in delegatestore (`ErrMissingHeader`, `ErrUnterminatedBatch`, `ErrClosed`, `ErrUnusable`, `ErrDelegateMissing`, ...) and assert with `errors.Is`; where a failure is only an injected fault, assert `errors.Is(err, injected)`. For the OODA truncation tests assert on length and presence of the transcript tool name, not the sentence. Rename `cov_*` files by subject as each is touched (a rename is a separate commit). Add a lint (grep in `make lint`) for `Error(), "` in `_test.go`, if one does not exist.

Size: about +60 production (sentinels), ~250 lines of test edits. Risk: low.

### agent-internal-state-13: `sessionlog` consumers each reopen the log and rebuild the path; `EntriesRange` is test-only (LOW, duplication)

Locations: `agent/session_namer.go:658-671`, `agent/session_init.go:2311-2323` and `:2356-2368`, `contextmgr/strategy_session_log.go:31-33`; `sessionlog/sessionlog.go:210`.

Evidence: `filepath.Join(s.stateDir, sessionsSubdir, s.id+".log.jsonl")` is written four times (three in agent, one in contextmgr with a different base expression). Each advisory call does `sessionlog.NewSessionLog(path)`, which parses the entire existing file just to append one line, and creates a second `SessionLog` instance on the same file the strategy holds open, so `tornTail` bookkeeping is per-instance. `EntriesRange` has no production caller (only a fuzz test). Advisory-vs-action filtering is by the magic string `"advisory"` written in three places and read in `String()`.

Fix plan: add `sessionlog.PathFor(stateDir, sessionID string) string` and `sessionlog.AppendAdvisory(path string, entry SessionLogEntry) error` (open lazily, no full load, sets `Kind` itself); replace the three agent call sites; export a `KindAdvisory` const; delete `EntriesRange`. TDD: a test that appends advisory after a torn tail without loading prior entries.

Size: -40 lines. Risk: low.

### agent-internal-state-14: `goal` persistence uses 9-value returns and an 8-positional-argument restore (LOW, complexity)

Locations: `goal/goal.go:154-173` (`PersistSnapshot`, `Restore`); callers `agent/session_state.go:388-403`, `agent/session_init.go:1275`.

Evidence: `PersistSnapshot()` returns `(objective, status, stopReason string, iterations, noProgressStreak int, madeProgressOnce bool, created, updated time.Time, ok bool)`; `Restore(objective, status, stopReason string, iterations, noProgressStreak int, madeProgressOnce bool, created, updated time.Time)`. The caller immediately repackages into `schema.GoalSnapshot` field for field, and the restore side unpacks the same struct back into positional arguments. The public `Snapshot` omits only `madeProgressOnce` and timestamps, hence the second shape.

Fix plan: make `Snapshot` carry `MadeProgressOnce`, `CreatedAt`, `UpdatedAt` (the status chip ignores extra fields), have `PersistSnapshot` return `Snapshot` and `Restore(Snapshot)`; session code maps `Snapshot <-> schema.GoalSnapshot` in one function in each direction. Keep `reported` runtime-only. Tests: the existing round-trip test in `goal_test.go:128` moves to the new signature.

Size: -30 lines. Risk: very low.

### agent-internal-state-15: `atif.ConvertTranscriptWithOptions` is a 240-line switch with seven copy-pasted `Step` literals and a three-name wrapper chain (MEDIUM, complexity)

Locations: `atif/atif.go:107-115` (`Convert` -> `ConvertWithOptions` -> `ConvertTranscriptWithOptions`), `:115-352`, `:263-296`; caller `agent/atif.go:29`.

Evidence: `Convert` and `ConvertWithOptions` exist only to forward; production calls only `ConvertTranscriptWithOptions` (agent/atif.go:29), while 64 test call sites use the shorter names. The `Step{StepID, Source:"system", Message: turn.Message.Text(), Timestamp: formatTimestamp(turn), Extra: ...}` literal followed by `steps = append(steps, step); stepID++` appears for user, system, checkpoint, summary, model_switch, orphaned results, and default (seven times), differing only in `Source` and `Extra["evener_kind"]`. `FinalMetrics.Extra` duplicates `TotalCachedTokens` and `TotalPromptTokens` as `cache_read_tokens` and `prompt_wire_tokens` ("kept as the #4562 alias"). `agent/atif.go:14-18` also keeps three package-level function variables (`atifMarshalIndent`, `atifMkdirAll`, `atifWriteFile`) as test seams.

Fix plan: keep one exported `Convert(header, entries, opts)`; add `systemStep(turn, kind string)` and an `emit(step)` closure that assigns `StepID`; move assistant/orphan handling into `convertAssistantWithResults` and `convertOrphanedResults`. Ask Jesse whether the `cache_read_tokens` / `prompt_wire_tokens` aliases still have consumers (grep the eval tooling) before removing. Replace the three `atif*` vars with direct calls and a temp dir in tests.

Size: -110 lines. Risk: low; 2,326 test lines including fuzz guard the output shape.

### agent-internal-state-16: `turnwindow` grammar is re-classified by its consumers instead of returning a parsed value (LOW, duplication)

Locations: `turnwindow/turnwindow.go:30-86,89-117`; `agent/apilog_read.go:603-618`; `agent/transcript_render.go:830-840` (`isFrontAnchored`), `:296-301`.

Evidence: `Parse` switches on `last:` / `start:` / contains `-`; `apilog_read.go` repeats `HasPrefix(rangeArg, "start:")` + `ParsePositiveInt`, then `Contains(rangeArg, "-")` + `ParseDash`; `isFrontAnchored` repeats the same three-way prefix test. `ParsePositiveInt` and `ParseDash` are exported solely so those callers can re-derive the grammar. The doc promise "a range written for one selects the same turns in the other" depends on every caller re-implementing the dispatch in lockstep.

Fix plan: add `turnwindow.Parse(spec) (Spec, error)` returning `Spec{Kind: Last|Start|Span, N, Lo, Hi}` plus `(Spec).Resolve(count) (start, end)`; `FrontAnchored()` becomes a method. Make `ParsePositiveInt`/`ParseDash` unexported. Keep the empty-spec default with callers. Tests: port the existing table tests and add one that `apilog_read`'s retention mode agrees with `Resolve` for each form.

Size: -25 lines net. Risk: low.

### agent-internal-state-17: `bucketref` documents itself as the dialect's single owner but only owns constants and two helpers (LOW, doc-drift)

Locations: `bucketref/dialect.go:1-48`, `bucketref/bucketref.go:96-137`; `agent/transcript_ref.go:13-48`; `agent/transcript_lookup.go:379-383`; `agent/doctor/locate.go:197-199`; `agent/doctor/selector.go:32-75`.

Evidence: the package comment says the package "owns the shared session-ref dialect" and "single owner of the selector grammar", but parsing lives in `doctor.parseSelector` and `agent.decodeRef`, which cut at the first colon (`strings.Cut(rest, ":")`, transcript_ref.go:37) while the comment says "cuts at the LAST colon". Three entry points format a ref: `bucketref.RefFor`, `agent.encodeRef` (concatenates directly, skips the `ValidateProjectID` check), and the two one-line `refFor` wrappers in `agent/transcript_lookup.go:381` and `agent/doctor/locate.go:197`. `EnumerateBuckets` has a convoluted Lstat/Stat branch (`:96-124`) that computes the same bucket twice, plus a functional-options type `func(*Options)` with a hook (`WithGlob`) that exists for test overrides in two callers.

Fix plan: either move `ParseRef` into bucketref with a `Strict bool` option (agent strict, doctor permissive) and delete both local parsers, or reduce the doc to what the package is (scheme constants + `RefFor` + `EnumerateBuckets`). Delete the two `refFor` wrappers and `encodeRef`, calling `bucketref.RefFor`. Rewrite the Lstat branch as: skip on Lstat error; if symlink and policy refuses, skip; `os.Stat`; add if dir. Fix the "LAST colon" comment or behavior (add a test pinning which).

Size: -60 lines. Risk: low-medium (ref parsing is a model-facing boundary; keep the strictness tests).

### agent-internal-state-18: delegatestore small leftovers: dead branch, legacy field, undocumented exports (LOW, dead-code)

Locations: `delegatestore/store.go:287-327` (`decodeLog`), `delegatestore/event.go:56-71`, `delegatestore/fold.go:399,424,784`, `delegatestore/store.go:20-47`.

Evidence: `decodeLog(raw, tolerateUnterminatedTail bool)` is called with `false` from both production sites (`store.go:66,190`); the `true` path is exercised only by tests (store_covtest_test.go:287,651,676), while its own doc comment says bounding parameters "would carry dead branches nothing exercises". `ObserverCallbackDelivered` is documented as set by nothing since "kata z5fm" and kept so old journals fold the same; three fold arms honour it. `Open` and `Store` have no doc comments while neighbors are heavily annotated.

Fix plan: remove the parameter and its branch (and its two tests); for `ObserverCallbackDelivered`, ask Jesse whether any pre-z5fm delegate journals can still exist on disk (it is a durable-record compat question; do not decide unilaterally); if not, remove the field, the three arms, and eight test references.

Size: -45 lines. Risk: low for the parameter; medium for the field (durable data).

### agent-internal-state-19: `OutputMatcher` lives in the journal store package, with two test-only entry points (LOW, architecture)

Locations: `jobstore/watch.go:1-484`; callers `agent/job_watch.go:3600,3670-3671,3706,3779`.

Evidence: `watch.go` (484 lines of rolling-window regexp matching) shares a package with the event log and output store but imports none of their internals; production uses only `FeedAtWithProvenance`, `SetScanOffset`, `SeedCarry`, `ScanRetained`, while `Feed` and `FeedAt` have only test callers (55 uses in `watch_test.go`), and `Regexp()` is an accessor. `CompileOutputMatch` is "the single home" for the multiline decision but sits in the store package.

Fix plan: move `watch.go` and its tests to `agent/internal/outputmatch` (mechanical; update the import in `job_watch.go`); collapse `Feed`/`FeedAt` into test helpers. No behavior change.

Size: ~0 net (move) and -15 lines. Risk: low.

### agent-internal-state-20: Small package hygiene nits in `artifactstore` (LOW, slop)

Locations: `artifactstore/store.go:32-37,50-60,68-90`.

Evidence: `os.MkdirTemp` already creates the directory with mode 0700, so `os.Chmod(dir, 0o700)` is redundant; `os.WriteFile(path, data, 0o600)` followed by `os.Chmod(path, 0o600)` repeats the same mode (umask can only clear bits); `refs` stores a path that is always `filepath.Join(s.dir, id)`, so the map could be a set. `Close` takes the already-closed branch to `RemoveAll` again. No doc comments on exported `Store`, `New`, `Put`, `Open`, `Close`.

Fix plan: drop both `Chmod` calls (keep if a test shows umask or filesystem quirks matter; cite it), make `refs` a `map[string]struct{}`, add one-line docs. Do only if touching the file for another reason.

Size: -12 lines. Risk: very low.

---

## Recommended fix sequence

1. Ask Jesse about finding 2 (delete the four experimental strategies) and finding 10 step 1 (legacy checkpoint formats) and finding 18 (`ObserverCallbackDelivered`). These are product-surface or durable-data decisions, so they gate the rest.
2. Finding 12 (error-string assertions): sentinel errors + `errors.Is` in delegatestore/jobstore tests. Independent, and unblocks safe refactors of the stores.
3. Finding 2 (delete strategies), then finding 9 (dead wrappers), as one deletion PR. Removes ~2,350 lines before any extraction.
4. Finding 1 (layer pipeline helper) with characterization tests, then finding 4 (shared turn renderer; fixes the UTF-8 bug with a failing test first).
5. Finding 3 (split `context_manager.go`) as a pure-move PR, together with the comment trim in finding 11 for contextmgr only.
6. Small independent PRs, any order: 14 (goal), 16 (turnwindow), 17 (bucketref), 13 (sessionlog), 15 (atif), 19 (outputmatch move), 20 (artifactstore).
7. jobstore: finding 8 (constructor ladder), then 6 (fold cache generics), then 7 (snapshot readers), each guarded by the existing fuzzers; run on CI, not locally.
8. Finding 5 (shared journal appender) last, after a design discussion with Jesse; migrate sessionlog, then delegatestore, then jobstore, then transcript.
9. Update `docs/product/subsystems.md` (S09 row for contextmgr strategies; journal recovery ownership) in the same PRs as steps 3 and 8.
