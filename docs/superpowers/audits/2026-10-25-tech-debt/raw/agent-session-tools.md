# Audit: agent-session-tools

Scope: `agent/session_tools*.go`, `agent/session_tool_*.go` (non-test), package `agent`.

## Inventory

| File | Lines | Funcs |
|---|---|---|
| session_tools_worktree.go | 3798 | 97 |
| session_tools_jobs.go | 2888 | 99 |
| session_tools.go | 1822 | ~75 |
| session_tools_transcript.go | 1642 | 51 |
| session_tools_find.go | 958 | 34 |
| session_tools_ask.go | 953 | 26 |
| session_tools_shell.go | 876 | 27 |
| session_tools_task.go | 731 | 16 |
| session_tool_repair.go | 640 | 20 |
| session_tool_round.go | 612 | 13 |
| session_tools_communicate.go | 594 | 16 |
| session_tool_registry.go | 534 | 17 |
| session_tools_worktree_dispose.go | 502 | 15 |
| session_tools_memory.go | 420 | 19 |
| doctor / notes / file / goal / compact / web / model_list / artifacts | 53-327 each | small |

Total 17,963 non-test lines in 21 files. 87 test files for the slice hold 39,083 lines (2.2x the production code). Functions over 120 lines: worktreeRemove 391, execTool 273, registerWorktreeTool 242, registerTaskTools 216, prepareToolCall 188, registerCommunicateTool 186, registerShellTools 185, worktreeCreateCore 165, disposeStableDelegateLane 147, worktreePruneSweep2 146, describeImageCall 146, worktreeAdopt 143, dispatchDoctorEvener 142, worktreePruneSweep1 131, readMarkdownPage 122.

Overall: behavior-rich and carefully commented, but the package has no shared tool-argument layer, carries test-only seams inside production code, has several dead-but-retained functions, and two files (jobs, worktree) are far past a navigable size. Narrating comments with review-round and issue numbers are widespread.

---

## agent-session-tools-1: No shared tool-argument decoder; a dozen drifting helpers plus inline assertions (medium)

Locations: session_tools.go:1791,1799; session_tools_find.go:280-340; session_tools_shell.go:486-500, 203-212, 856-876; session_tools_jobs.go:2244-2320, 2556-2575, 2607-2630; session_tools_model_list.go:19-26; session_tools_file.go:62,85,87,110,116; session_tools_web.go:44-61; session_tools_goal.go:20; session_tools_communicate.go:362; session_tools_shell.go:331,349.

Evidence:
- Helper families doing the same job with different semantics: `stringArg` (silent ""), `optionalStringArg` (errors on wrong type, "%T"), `watchStringArg` (errors, different message), `optionalIntArg` (silently ignores a non-number, no integrality check), `shellIntArg` (truncates 1.9 to 1, accepts int), `optionalWholeIntArg` (rejects fractional/negative), `watchIntArg` (rejects NaN/Inf, handles overflow), `shellBoolArg` vs `optionalBoolArg`, and three near-identical `[]any` of strings loops: `stringArrayArg`, `jobStatusArrayArg`, `jobTypeArrayArg`.
- Nine inline `.(float64)` assertions in prod (shell.go:203,207,211,866,872; model_list.go:21,24), each with its own `> 0` clamp.
- 12 sites do `fmt.Sprint(args["key"])`, which renders an absent key as the literal string `<nil>` (file.go:62,85,87,110,116; web.go:44,45,61; goal.go:20; shell.go:331,349; communicate.go:362). Schema validation presumably prevents this on the normal path, but direct registry calls and hook `updatedInput` paths reach these handlers unguarded.
- The same shape decision ("is a float64 that is integral") is made differently in four places, so the same malformed model input is accepted by one tool and rejected by another.

Impact: model-facing validation behavior differs per tool without any product reason; every new tool adds another variant.

Fix plan: add one small file (`agent/toolargs.go`, or `internal/tool/args.go` so `tool` package handlers can reuse) with `String`, `Bool`, `Int` (strict whole number, optional min/max), `StringList[T ~string]` returning `invalid_request:`-prefixed errors. Migrate call sites one file at a time, keeping each tool's current accept/reject set by test first (characterization tests on odd inputs: 1.5, "3", null, negative), then delete the per-file helpers. Where today's lenient behavior is relied upon (list_dir depth), keep a `Lenient` variant rather than changing behavior. Note any behavior change for Jesse before landing.

Size: ~-150 / +80 LOC. Risk: medium (model-visible validation); mitigated by characterization tests. Docs: none beyond a line in docs/developing-evener if a tool-authoring doc exists.

---

## agent-session-tools-2: Dead code retained under `//nolint:unused` and an unused constant (low)

Locations: session_tools_jobs.go:369-372 (`clearDescendantReceiverWatchByID`), :2576-2605 (`watchSendArg`, `isEmptyWatchSend`), :2673-2676 (`projectJobRecord`), :36 (`jobKindAgent`), :82-83 (`_ = deps`).

Evidence:
- `//nolint:unused // retained for internal watch-send parsing; public job_watch currently rejects send.` Non-test grep for `watchSendArg(` returns nothing; only `*_test.go` call it (session_tools_jobs_cov_test.go:96-116, covtest2_test.go:534, contract_program_fuzz_test.go:138).
- `clearDescendantReceiverWatchByID` is a one-line forward to `clearStableReceiverWatchByID`; only tests call it (jobs_seed100_fuzz_test.go:241 and others). Comment says "retained for tagged ... owners" (build-tagged code that may not exist).
- `projectJobRecord` is `projectJobRecordForViewer(s, s, rec)`, tests only.
- `jobKindAgent = "agent"` has zero references anywhere (`grep jobKindAgent` hits only its declaration).
- `registerJobToolsWithRegistrar(... deps *toolDeps)` begins with `_ = deps`.

Impact: tests pin dead behavior (coverage-padding), lint is silenced, readers assume `send` is a live feature.

Fix plan: delete the four functions, the constant and the unused `deps` parameter; delete the tests that exercise only them (they test nothing reachable). If a build-tagged owner exists (`grep -r "go:build" agent | xargs grep clearDescendant`) confirm before deletion; none found in this slice. Keep `watchSendArgs` struct only if `watchConfig.send` is still populated by non-test code (it is read by job_watch.go).

Size: ~-90 prod, ~-150 test. Risk: low.

---

## agent-session-tools-3: Test-only injection seams compiled into production code (medium)

Locations: session_tools_worktree.go:27-75 (`worktreeTestSeams`, global `worktreeSeams sync.Map`, 4 wrapper methods + `enterWorktree` hook at :919); session_tools_jobs.go:72-86 (`jobToolRegisterFunc`, `jobToolRegistrar`, `registerJobToolsWithRegistrar`, `deps.registerTool`); session_tools.go:1048 (`execToolCheckpoint`, 9 call sites across the package).

Evidence:
- `var worktreeSeams sync.Map // map[*Session]worktreeTestSeams` is consulted on every sidecar write/delete/update in production, and non-test code never writes to it (grep: only `.Load`). It is a process-global keyed by session pointer.
- `jobToolRegistrar` is a single-implementation interface (the `Registry`) plus a func adapter whose only purpose is "deterministic failure injection" (its own comment); the one production branch `deps.registerTool` is also test plumbing.
- `s.cfg.testOnly.execToolCheckpoint` is called six times inside execTool.

Impact: global mutable state and extra indirection in the highest-risk code (worktree lifecycle) purely for fault injection; AGENTS.md prefers real boundaries with scripted providers.

Fix plan: replace the sync.Map with an unexported function-typed field on Session (or on the already-existing `cfg.testOnly`) so no global is read. Delete `jobToolRegistrar`/`jobToolRegisterFunc`; make the failure-injection test use a real `tool.Registry` with a pre-registered conflicting tool name, or test the individual register errors via a registry that rejects. Leave execToolCheckpoint if the concurrency tests need it, but concentrate the calls.

Size: ~-60 / +20 LOC. Risk: low-medium (touches test helpers `installWorktreeSeams` in ~4 test files).

---

## agent-session-tools-4: Tool-registration boilerplate: `_ = ctx; _ = env`, swallowed Register errors, repeated Limit literals (low)

Locations: session_tools_jobs.go:82-139 (six identical Register blocks); session_tools_notes.go:23-110; session_tools_goal.go:17-19; session_tools_file.go:84; session_tools_doctor.go:36; counts: `_ = env` lines in 16 places across 7 files; `_ = reg.Register(` in 14 production places.

Evidence:
- session_tools_jobs.go:116-119: `_ = ctx; _ = env; return jobWatchToolWithContext(ctx, ...)` uses ctx on the very next line, so the discard is false noise.
- Params that are unused can simply be named `_`; the repo already does that elsewhere (model_list.go:19 `func(_ context.Context, _ execenv.ExecutionEnvironment, ...)`).
- `_ = reg.Register(...)` discards a duplicate-name/schema-compile error at registration: a typo'd definition would silently lose the tool (notes.go has 4, web.go 2, shell.go 2).
- `Limit: schema.ToolOutputLimit{MaxChars: jobToolResultDefaultMaxChar, Strategy: schema.TruncTail}` repeated 6 times in jobs registration.

Fix plan: mechanical sweep: rename unused params to `_`, delete `_ = ctx/env`, collapse the six job registrations into a slice of `{def, readOnly, name, fn}` with one loop, and make `register*Tools` return errors (some already do) so `session_tool_registry.go` propagates them. 

Size: ~-120 LOC. Risk: low; compile-checked.

---

## agent-session-tools-5: Tool-name mapping helpers sit in Session grab-bag file and duplicate each other (low-medium)

Locations: session_tools.go:677-770 (4 Session methods + 5 package funcs), 1542-1600 (`wireToolDef`, `profileWireToolDefs`), 786-788.

Evidence: `canonicalToolName`/`canonicalizeToolNames`/`providerToolName`/`providerVisibleToolNames` exist as pure functions taking `nameMap`, and again as thin Session methods that only inject `s.currentProfile().ToolNameMap()` (session_tools.go:677-683). `providerVisibleToolNames` reads `s.profile` directly while its sibling `providerVisibleToolName` takes the lock; comments explain the lock-order trap in prose, which is a hazard to maintain. `canonicalToolName` scans the whole map linearly per name (map is canonical->provider, inverse is needed). The mapping is a property of `provider.Profile.ToolNameMap()`, not of Session.

Impact: a model switch race is documented only in comments; two call styles for the same thing.

Fix plan: add `Profile.CanonicalToolName/ProviderToolName` (with a cached inverse map) in `provider`, make Session callers snapshot the profile once and call those; delete the pure copies and the wrappers. Tests: move existing mapping tests to the provider package.

Size: ~-70 / +40. Risk: low-medium (provider package is shared).

---

## agent-session-tools-6: `execTool` is a 273-line function with three copies of "emit canceled tool end" (medium)

Locations: session_tools.go:773-1046 (copies at :928-942 `emitCanceledEnd`, :954-968 inline, :972-980).

Evidence: the closing-fence path builds `events.ToolCallEndData{ToolName, CallID, ArgumentsJSON, Error: skippedToolResult(...).FullOutput}` inline twice with slight drift from `emitCanceledEnd` (one holds `responseSideEffectsMu`, the closure takes it itself, the inline copy is already holding it). Error `ExecResult` literals `{ToolName, CallID, Output: msg, FullOutput: msg, IsError: true}` are written out four times (:818, :828-834, :838-846 plus post-hook). The function interleaves repair telemetry, pre-hook handling, event bracket, dispatch, sandbox escalation, output chunking and post-hook.

Impact: any change to end-event shape must be made in 3 places; ordering bugs around `responseSideEffectsMu` are hard to review.

Fix plan: extract `errorResult(call, msg, state...)`, a `toolEventBracket` struct (open/close/emitEnd), and split into `runPreHooks`, `dispatch`, `emitEnd`, `runPostHooks`. Keep behavior identical; guard with existing execTool tests plus one test that cancels at each checkpoint (the checkpoint seam already exists).

Size: ~-60 net after extraction. Risk: medium (concurrency ordering) — do it as pure extraction without reordering.

---

## agent-session-tools-7: session_tools.go is a grab-bag: delegate create, vision side channel, tool-def cache, task store, helpers (medium)

Locations: session_tools.go:97-250 (stable delegate create), 295-675 (vision side-channel, ~380 lines), 1422-1680 (tool definitions/cache), 1727-1790 (task/goal store accessors, reminder).

Evidence: the file name says "session tools" but it holds five unrelated areas. The delegate create tool lives here while status/stop/send/list live in jobs.go (`registerStableDelegateTool` registers delegate + delegate_send from session_tools.go:119-140, while `jobStatusTool` etc. are in jobs.go). The docs map (docs/product/subsystems.md S14) cites session_tools.go as "Tool dispatch", which the vision code obscures.

Fix plan: pure file moves, no code change: `session_tools_vision.go`, `session_tools_delegate.go` (create/send registration + `marshalStableDelegateCreateResult`), `session_tool_definitions.go`, `session_tools_task_store.go`. Leave dispatch (execTool, appendToolResults) in session_tools.go. Update the S14 row in subsystems.md if file names are cited.

Size: 0 net LOC (moves ~900). Risk: low.

---

## agent-session-tools-8: Output-limit enforcement and max-chars lookup triplicated (low-medium)

Locations: session_tools_shell.go:507-535 (`enforceShellToolJSONLimit`, `shellToolResultMaxChars`), session_tools_jobs.go:2747-2785 (`jobListOutputMaxChars`, `jobToolResultMaxChars`, `enforceJobToolJSONLimits`), session_tools_model_list.go:41-61; wired at session_init.go:1762-1764.

Evidence: `shellToolResultMaxChars` and `jobToolResultMaxChars` are the same function with different constants: nil registry -> default, unregistered or `<=0` -> default, `< min` -> min. Both min constants are 800 (`shellToolResultMinJSONChars`, `jobToolResultMinJSONChars`). `jobListOutputMaxChars` is a third variant that returns the registry value unclamped and is used only for job_list. `enforceJobToolJSONLimits` hardcodes the tool name list `{"job_status","job_list","job_stop","delegate","job_watch","delegate_send"}` separate from the registration sites.

Fix plan: one `minJSONLimit(reg, name, def, min)` and one `enforceMinJSONLimits(reg, names, min)` in `session_tool_registry.go`; model_list keeps its specific logic (it clamps to bytes budget). Delete the three duplicates. Characterize with a table test over (nil reg, missing tool, 0, 500, 5000).

Size: ~-50 LOC. Risk: low.

---

## agent-session-tools-9: Result marshaling: ignored `maxChars` parameters and six-fold progressive-drop copy (low)

Locations: session_tools_jobs.go:1233, 1817-1818, 1951-1952, 1988-1989, 1993-1994 (`_ = maxChars`); session_tools.go:215-250.

Evidence: five `marshal*Result(res, maxChars)` functions accept `maxChars` and immediately discard it; callers thread it through (jobs.go:243-257). `marshalStableDelegateCreateResult` repeats the `if fit, ok, err := marshalBoundedJSONWithFit(out, maxChars); err != nil || ok { return fit, err }` stanza six times between field-clearing assignments. `marshalBoundedJSON` error text says "job tool JSON output" though it is used for delegate and status too.

Fix plan: drop the unused parameter from the five functions and their call chain (`jobWatchToolWithContext` etc. keep it only where used). Replace the stanza with `for _, drop := range []func(*stableDelegateCreateResult){...}`. Behavior unchanged; existing minimum-limit test (named in the comment) guards it.

Size: ~-45 LOC. Risk: low.

---

## agent-session-tools-10: Eight-plus path-containment helpers, two identical in one file (low)

Locations: session_tools_worktree.go:1180 (`pathEqualOrUnder`), :1685 (`pathWithin`), :1789-1807 (`relPathUnderManagedDir`/`isUnderManagedDir`); elsewhere: agent/session_notes.go:705, agent/sandbox/session_scratch.go:213 (`pathWithin`), agent/execenv/local.go:3491, agent/execenv/project_resolver.go:302, internal/plugins/marketplaces.go:1459, cmd/evener-hub/internal/fspaths/paths.go:124, cmd/evener-fuzzregistry/main.go:466, tools/tool-fluency/.../review.go:299.

Evidence: `pathEqualOrUnder` and `pathWithin` both compute `filepath.Rel` and `rel != ".." && !HasPrefix(rel, ".."+sep)`; `pathEqualOrUnder` only adds a redundant `candidate == target` short-circuit (Rel returns "." for equal). Parameter order differs (candidate,target vs path,dir). `relPathUnderManagedDir` repeats the Rel/".." test a third time. Path-containment checks are security-relevant, so drift is costly.

Fix plan: one `pathWithin(path, dir)` in this file now (delete `pathEqualOrUnder`, rewrite its callers), then a follow-up to export a single helper from `agent/internal` (e.g. `fspath.Within`) and migrate the other copies. Test: table test for equal, child, sibling-prefix (`/a/bc` vs `/a/b`), `..`.

Size: ~-20 now, ~-60 across the repo. Risk: low.

---

## agent-session-tools-11: session_tools_jobs.go (2888 lines) mixes delegate, job, watch and formatting concerns under a misleading name (medium)

Locations: session_tools_jobs.go whole; e.g. watch ~141-545 and 1951-2130 and 2279-2610, delegate args 551-698, status 699-760, list 800-1177, stop 1178-1700, send 1798-1950, projections 2674-2888.

Evidence: 99 functions; the "stable" prefix appears 90 times across the slice (`stableDelegateStatusTool`, `stopStableDelegate`, `stableJobListCandidates`, ...) describing a generation that no longer has a non-stable counterpart in the slice, so the qualifier is history, not domain. Watch code is split between this file (tool surface, arg parsing, formatting) and job_watch.go (6.5k lines, outside the slice). Tool-facing result structs (`jobWatchToolResult`, `delegateWorktreeToolResult`, `delegateSendResult`, ...) are mirrored 1:1 from internal structs by `*From` converters (:1798-1815), i.e. a second set of DTOs.

Fix plan: split mechanically by tool: `session_tools_job_status.go`, `_job_list.go`, `_job_stop.go`, `_job_watch.go` (surface + args + formatting), `_delegate_send.go`, `_delegate_args.go`. In the same change rename `stableDelegate*` to `delegate*` where no collision exists (compile-checked). Then consider whether the `*ToolResult` mirror structs can be the internal ones with json tags. No behavior change.

Size: 0 net (moves ~2800). Risk: low for moves, medium for renames touching tests (~dozens of test files).

---

## agent-session-tools-12: session_tools_worktree.go (3798 lines) with 391-line and 242-line functions; sweep naming by ordinal (medium)

Locations: session_tools_worktree.go:2387-2777 (`worktreeRemove`), 367-609 (`registerWorktreeTool`), 3188-3330/3555-3700/3709 (`worktreePruneSweep1/2/3`), 1235-1400 (`worktreeCreateCore`).

Evidence: 97 functions, 132 `spec §` comment references (e.g. "spec §5 remove step 7"). `worktreeRemove` is numbered Step 1..N inline; sweeps are named by ordinal rather than what they do ("registered managed worktrees", "orphan lanes", ...). The `prunePolicy()` literal duplicates its `disposableAt` and `disposableBranch` error-wrapping (`"merge check failed: " + dErr.Error()` twice, :3158,:3170). The spec the comments cite is not in the evergreen docs path (docs/product only has S13 summary), so the numbering is un-checkable by a future reader.

Fix plan: split into `session_tools_worktree_{create,switch,remove,list,prune,git}.go`; break `worktreeRemove` into guard steps (`removeLockGuard`, `removeLiveWorkGuard`, `removeRestore`) returning typed refusals; rename sweeps `pruneRegisteredLanes`, `pruneOrphanLanes`, `pruneBranchesOnly`. Replace `spec §N` references with a pointer to docs/developing-evener/worktrees.md plus the invariant in words, or move the spec's step table there. Pure refactor guarded by the existing ~(worktree_*) tests run on CI.

Size: 0 net moves, ~-100 from dedupe. Risk: medium (long stateful function); do extraction one guard at a time.

---

## agent-session-tools-13: History and review-round narration in comments (low)

Locations: session_tools_ask.go:223,247,599,812,826,850 ("RoboRev #1806 round-5 Medium", "#1906/#1907 round-2"); session_tools_jobs.go:1768,1792 ("roborev finding 2718-2", "RoboRev PR #954"); session_tool_registry.go (4), session_tools_communicate.go (5), session_tools_shell.go (5), session_tools_worktree.go (6), worktree_dispose.go (4): 49 hits of roborev/RoboRev/issue-number references in non-test files; session_tools.go:900 style comments naming a test (`TestDelegateResourceCreate_RegisteredPostCommitFailureRetainsStableIdentityWithinMinimumLimit`).

Evidence: user rule: history belongs in commit messages. Several comments are only a PR/round number, giving a future reader nothing.

Fix plan: rewrite each comment to state the invariant and delete the provenance; drop test names from comments (the test already names itself). Mechanical, 49 sites.

Size: ~-80 comment lines. Risk: none.

---

## agent-session-tools-14: read_transcript argument handling is spread across five functions with overlapping parsing (medium)

Locations: session_tool_repair.go:379-460 (`normalizeRetainedReadArgs`, `handlerJudgedArgs`), :464 (`...ForValidation`); session_tools_transcript.go:401-466 (`parseRetainedReadArgs`, `...WithIssues`, `retainedReadIncompatibleFields`), :844 (`parseReadSessionTranscriptArgs`).

Evidence: "is this field a neutral default" is decided in repair (`isNeutralRetainedInteger`, `isNullPlaceholder`, `isNeutralJobFormat`) and then re-judged in the handler (`retainedReadIncompatibleFields` skips neutral job format again at :458). `parseRetainedReadArgsWithIssues` and `parseReadSessionTranscriptArgs` both parse `offset_bytes` through `optionalIntArg` (which silently ignores `"5"`), then validate ranges separately. `normalizeRetainedReadArgs` is 80 lines of order-sensitive removals depending on `jobRef`/`artifactRef`.

Impact: three layers must agree on what counts as an absent field; adding a field means editing repair, parse, incompatibility and validation.

Fix plan: define one table `retainedReadFields` (name, kind, neutral predicate, allowed ref kinds) and drive normalization, incompatibility and parse issues from it. Keep the existing tests as characterization first; replace the four functions' bodies with table walks. Do after finding 1 so numeric parsing is shared.

Size: ~-120 / +80. Risk: medium (repair telemetry strings are pinned by tests: those tests assert behavior-level Changes, keep them).

---

## agent-session-tools-15: Tests dominated by coverage-padding files and fuzz "seed100" families (low-medium)

Locations: session_tools_jobs_seed100_{final,more,range_a..d}_test.go, session_tools_jobs_cov_test.go, session_tools_jobs_covtest2_test.go, jobs_seed100_fuzz_test.go, job_watch_covtest2_test.go; 87 test files, 39k lines for 18k lines of code.

Evidence: tests named by coverage range ("range_a".."range_d") and call dead functions with `_ , _ =` discarded results (session_tools_jobs_seed100_range_d_test.go:55 `_, _ = watchSendArg(args)`; seed100_more_test.go:40-45 `_, _, _ = root.clearDescendantReceiverWatchByID("missing")`). Such tests assert only "does not panic". Also `job_watch_covtest2_test.go:898` tests the func adapter `jobToolRegisterFunc.Register`, a pure pass-through (finding 3).

Fix plan: after findings 2, 3 and 11, delete tests that exercise only removed code; fold surviving assertions into behavior-named files per tool; keep fuzz targets that assert invariants. Do not reduce coverage of live behavior: check with a before/after `go test -cover` run on CI, not locally.

Size: ~-1500..-3000 test LOC (estimate; needs per-file review). Risk: low if coverage of live code is checked on CI.

---

## Recommended fix sequence

1. Finding 13 (comments) and 4 (boilerplate sweep): mechanical, compile-checked, no behavior change.
2. Finding 2 (dead code) then 15 (delete tests that only covered it).
3. Finding 10 (single path-containment helper in this file), 9 (unused maxChars, loop for drops), 8 (shared limit helpers).
4. Finding 3 (remove test-only seams from production) before the big file splits so moves carry less baggage.
5. Findings 7, 11, 12 file splits as pure moves, one PR per file; renames of `stableDelegate*` in their own PR.
6. Finding 1 (shared argument decoder) with characterization tests, then 14 (retained-read table) on top of it. Flag any accept/reject behavior change to Jesse first.
7. Finding 6 (execTool extraction) and 5 (tool-name mapping onto provider.Profile) last, each behavior-preserving with CI as the test run.

Docs: update the S13/S14 rows in docs/product/subsystems.md when files move (finding 7, 11, 12 change the linked paths).
