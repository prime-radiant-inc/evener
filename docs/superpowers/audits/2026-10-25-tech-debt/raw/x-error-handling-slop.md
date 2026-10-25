# x-error-handling-slop: cross-cutting Go slop audit

Base: origin/main at a6c0b9c561. Scope: non-test Go code repo-wide (1164 files). Read-only audit; no tests were run.
Measurement tools were throwaway Go AST scripts in the session scratchpad (function length, pass-through detection) plus ripgrep.

## Inventory

| Metric | Count |
|---|---|
| Non-test .go files | 1164 |
| Files over 2000 lines | 26 (largest: agent/job_watch.go 6564, appwire/types.go 5898, sshconn/manager.go 3965, app_host_manage.go 3854, session_tools_worktree.go 3798) |
| Files over 1500 lines | 46 |
| Functions over 150 lines | 116 (66 over 200, 27 over 300, 9 over 500). By tree: cmd/evener-hub 41, agent 36, llm/providers 8, agent/internal 7 |
| `_ =` / `_, _ =` discards | ~1550 (803 `_ = call(`, 750 `_, _ =`) |
| of which `fmt.Fprintf/Fprintln/Fprint` | 566 |
| of which `hash.Write` | 128 |
| of which Close/Remove/RemoveAll/Rollback/Release | ~330 |
| `_ = ctx` / `_ = env` / other silenced params | 62 lines (35 are `ctx`/`env`) |
| `_ = reg.Register(...)` dropping an error | 15 |
| `_ = json.Unmarshal(...)` | 20 |
| `//nolint:errcheck` | 107 (plus 40 nilerr, 33 noctx, 15 unused) |
| `if err != nil { }` empty blocks | 0 (clean) |
| Package-level `var x func(...)` / `var x = func` seams | 104 (31 are bare `var x func`) |
| Package-level aliases of os/filepath/io funcs in internal/plugins | 35 across 9 files |
| Exported or unexported `...ForTest/ForTesting` helpers in prod code | 21 |
| Interfaces in non-test code | 150 |
| Pure pass-through functions (body is one call forwarding all params unchanged) | 146 |
| `if s == nil` nil-receiver checks in agent | 140 |
| Comments with history narration ("used to", "previously", "was removed", "retained for", "kept for") | ~200 lines; `no longer` 275, `legacy` 267, `spec §` 939, `rev-N`/`finding X1` 15 |
| Temp-file + rename atomic write sites | 33 files with `Rename(tmp...)`, at least 7 distinct helpers |

Overall: error handling is mostly deliberate (zero empty `if err` blocks, discards are mostly cleanup paths). The real slop is structural: god functions, test seams compiled into production, and several accreted helper families.

## Findings

### x-error-handling-slop-1: Giant constructor/handler functions (1829, 1597, 834, 777, 776, 682, 663 lines)
Locations: cmd/evener-hub/app_relay.go:471 newHubRelayFunctions (1829 lines, 28 depth-1 closures sharing relayMu and four maps, returns a struct of 9 closures at :2290); cmd/evener/serve.go:401 runServeWithDeps (1597); cmd/evener-hub/internal/hubcore/tree.go:921 buildTreeAtWithProjects (834); agent/session_init.go:852 RestoreSessionFromMetaWithConfig (777); cmd/evener-hub/main.go:253 runMain (776); cmd/evener-hub/app_rpc.go:1304 registerThreadHandlers (682); agent/session_lifecycle.go:1945 processOneInput (663) and :1118 processInputKindWithProvenance (584).
Evidence: 116 functions over 150 lines. serve.go:401-470 is ~60 `fs.String/Bool/Var` flag declarations inline in the function that then also wires the whole daemon. newHubRelayFunctions defines `removeStateRoutesLocked`, `bindStateRouteLocked`, `removeRelayKeyStateLocked`, `registerPendingStateLocked`... all closing over shared mutable maps: a hand-rolled object with no type.
Impact: review and race reasoning are unbounded; the closure-captured state cannot be unit-tested without the whole server. Large maintenance drag, and the product docs rule about recovery ownership is hard to verify inside 1800 lines.
Fix plan: Convert newHubRelayFunctions to a `relayManager` struct (maps and mutex as fields, closures to methods) in its own file; keep `hubRelayFunctions` as the thin interface to callers. Extract serve flag declarations into a `serveFlags` struct with `parse(fs, args)`. Split runMain by subcommand dispatch table plus a `startHub` function. Do each as a pure move-to-methods refactor with no behavior change; existing tests are the guard (run in CI). Add a lint threshold (gocyclo/funlen with a baseline list) so new giants fail.
Size: ~0 net, ~6000 lines moved across 8 functions. Risk: medium (shared-state ordering; keep lock discipline identical, one function per PR).

### x-error-handling-slop-2: 26 files over 2000 lines, several mixing unrelated concerns
Locations: agent/job_watch.go (6564), appwire/types.go (5898), cmd/evener-hub/internal/sshconn/manager.go (3965), cmd/evener-hub/app_host_manage.go (3854), agent/session_tools_worktree.go (3798), agent/execenv/local.go (3647), agent/internal/tool/repair/explain.go (3638), agent/delegate_runtime.go (3568), agent/session_lifecycle.go (3413), server/appwire_runtime.go (3074), agent/session_tools_jobs.go (2888), agent/internal/contextmgr/context_manager.go (2838), agent/session.go (2784), cmd/evener-hub/app_instances.go (2716).
Evidence: agent/session_tools_jobs.go (2888) holds tool registration, arg parsing (`watchSendArg`), result marshaling (`marshalWatchResult`), projection (`projectJobRecordAt`), and dead code (finding 9). execenv/local.go:3634 still holds `ShellEscapeArgs`, a pass-through to shellquote (finding 6).
Impact: merge conflicts, slow review, hides duplication. Medium.
Fix plan: split by seam, not by size: one file per tool (registration + exec), arg parsing into `*_args.go`, projections into `*_projection.go`. appwire/types.go is a schema file; split by method family only if the TS generator output stays byte-identical (`make lint` freshness check guards it). Do the pure file moves first (no edits to bodies), one package per PR.
Size: ~0 net, file moves only. Risk: low (mechanical), watch generated-output freshness for appwire.

### x-error-handling-slop-3: `_ = reg.Register(...)` discards validation errors while sibling call sites check them
Locations: agent/session_tools_notes.go:24,40,60,79; session_tools_goal.go:14; session_tools_web.go:38,55; session_tools_task.go:485; session_tools_ask.go:482; session_tools_compact.go:58; session_tools_shell.go:198,328; session_tools_worktree.go:368; session_tool_registry.go:449; session_tools_communicate.go:358. Compare checked sites: session_tools_memory.go:30, session_tools_communicate.go:84, session_tools.go:121, mcp/manager.go:382, session_tools_jobs.go:65.
Evidence: tool/registry.go:512 `Register` returns errors for invalid tool name, missing executor, and schema compile failure (:524-537). Fifteen registrations drop that; a typo'd tool name or bad schema silently produces a session missing a tool.
Impact: silent loss of a tool at startup; inconsistent error policy inside one package.
Fix plan: Make the register* helpers return error and have the one caller (registerAllTools) fail session construction on error (same as the already-checked sites). Alternatively add `MustRegister` that panics for the static built-in tools, since their definitions are compile-time constants, and use it uniformly. Add a test that registers every built-in tool and asserts none error (a red test first by temporarily using an invalid name).
Size: ~40 lines. Risk: low.

### x-error-handling-slop-4: Silenced unused parameters (`_ = ctx`, `_ = env`, `_ = maxChars`, ...)
Locations: agent/session_tools_notes.go:27-28,43-44,63-64,82-84 (`_ = ctx; _ = env; _ = args`); session_tools_jobs.go:88-89,99-100,110,120-121 (inside closures that then use ctx anyway, e.g. :120-121 sets `_ = ctx` then calls `jobWatchToolWithContext(ctx, ...)`), :83 `_ = deps`, :1233,1818,1952,1989,1994 `_ = maxChars` (five functions that accept and ignore a size budget), :2690-2691 `_ = viewer; _ = assessor`, :2804 `_ = t`; session_skill_delivery.go:348-349 `_ = profile; _ = turns`; cmd/evener/openai_login.go:260 `_ = workDir`; agent/session_tools_transcript.go:933 `_ = rangeArg`; cmd/evener-hub/app_host_manage.go:3396 `_ = intent`; agent/sandbox/seatbelt_darwin.go:33 `_ = cwd`.
Evidence: 62 lines total, 35 of them `_ = ctx/env`. `projectJobRecordAt(viewer, assessor, rec, now)` ignores viewer and assessor entirely; `marshalWatchResult(res, maxChars)` ignores maxChars; `resolveOpenAIStateDir(workDir, override)` ignores workDir.
Impact: dead parameters are API surface that misleads callers (callers compute and pass budgets that do nothing).
Fix plan: For closures with fixed signatures, name params `_` in the signature and delete the discard lines. For the dead parameters, delete them from the function signatures and every call site (compiler-guided). No behavior change.
Size: -80 lines. Risk: low.

### x-error-handling-slop-5: Test seams compiled into production as mutable package globals (104 func vars)
Locations: cmd/evener-hub/navigation_service.go:45-59 (6 empty-func hooks like `navigationBeforeSnapshotCommit = func(context.Context){}`); agent/job_transcript_read.go:46,90,119,125,131 (5 file-open vars); internal/transcriptindex/index.go:113-137 (`testKillAfterRecordWrites` etc., 4 crash-injection vars with "Test seam only; nil in production"); server/thread_history.go:31-51 (4 hooks; comment at :24 admits "read by the projection goroutine without synchronization: tests that set them must not run in parallel"); agent/session_worktree_close.go:197,307,312 (`closeBudgetMintHook`, exported `ObserveCloseStopJoin`, `ObserveCloseStopJoinBudget`); cmd/evener/openai_login.go:53-60.
Evidence: 104 `var x = func`/`var x func` in non-test code (cmd/evener-hub 24, agent 23, cmd/evener 11, transcriptindex 7, server 6, execenv 6). Each is a data race by construction if two tests (or a test and a live goroutine) touch it; server/thread_history.go had to make one hook atomic (:36-39) "unlike the seams above". Exported hooks (`agent.ObserveCloseStopJoin`) form public API only for cmd/evener tests, with a second variant "unchanged for compatibility" (session_worktree_close.go:309-311).
Impact: races under -race/parallel, hidden control flow, test-only code on the hot path.
Fix plan: Do not mass-convert. Policy: new seams must be struct fields set via constructor options (instance-scoped, as hubcore/past.go:125-133 already does), never package globals. Migrate in order of race risk: the unsynchronized ones in server/thread_history.go and transcriptindex first (move hooks onto the Server/Index instance), then the exported agent.ObserveCloseStopJoin pair (fold into SessionConfig). Delete `ObserveCloseStopJoinBudget`-vs-`ObserveCloseStopJoin` duplication by keeping only the one the test uses (cmd/evener/run_drain_wedged_delegate_test.go:265). Add a depguard/grep lint that forbids new `var x func` in non-test files outside an allowlist.
Size: ~600 lines touched over ~6 PRs. Risk: medium (tests that set globals need rewriting; do each package together with its tests).

### x-error-handling-slop-6: internal/plugins aliases os.* per file solely so tests can fail them (35 vars, 9 files)
Locations: internal/plugins/install.go:14-22 (`installRemoveAll = os.RemoveAll`, `installMkdirAll`, `installRename`, `installAcquireLock`), source.go:17-30 (14 vars incl. `sourceRel = filepath.Rel`, `sourceCopy = io.Copy`, `sourceClose = func(f *os.File) error {return f.Close()}`), marketplaces.go:17-30 (14), gc.go:15-17, doctor.go:21-26, manifest_fallback.go:14-19, atomic.go:18-24, git.go:19, locks.go:19, publication.go:180 `moveRename = os.Rename`.
Evidence: 82 test references to `marketplaceRename`/`installRename` alone. paths.go:28 and locks.go:33 document the per-area duplicates as a feature ("the per-area test seams (installAcquireLock and its siblings)"). `lockAcquirer` type exists purely to type these. Each is a pass-through to one stdlib function, three copies of `RemoveAll`, four of `Rename`, four of `MkdirAll`.
Impact: error injection fidelity at the cost of 35 mutable globals and a naming scheme that triples the vocabulary; tests stub the lock to a no-op in other tests (paths.go:28-32 says the race detector cannot see it).
Fix plan: Introduce one `fsOps` struct field on Manager (Rename, RemoveAll, MkdirAll, Stat, ...) defaulting to os, and one `lockAcquirer` field; replace the 35 package vars with `m.fs.X`. Tests inject faults per Manager instance, removing the global-state race. Alternatively adopt the repo's existing afero.Fs (used in agent/fork.go, launchconfig/io.go) for these operations. Git helpers (gitClone etc.) stay as one `git` interface field.
Size: ~-150 lines of vars, ~400 call-site and test edits. Risk: medium (82+ test edits; mechanical).

### x-error-handling-slop-7: Interfaces that exist only to inject test doubles (single production implementation)
Locations: agent/session_tools_jobs.go:70-82 (`jobToolRegisterFunc` + `jobToolRegistrar` + `registerJobToolsWithRegistrar`, used by one fuzz test jobs_seed100_fuzz_test.go:214; production passes `reg` itself at :69, plus a second wrapper closure at :65); agent/session_stream.go:17-25 (`sessionStreamAccumulator` interface + `newSessionStreamAccumulator` var returning `llm.NewStreamAccumulator()`; swapped only in session_queue_stream_tail_fuzz_test.go:42); cmd/evener-hub/app_plugin_autoupgrade.go:16-27 (`pluginAutoUpgradeTicker` + `realPluginAutoUpgradeTicker` + `newPluginAutoUpgradeTicker` var; swapped in cov_launch_models_plugins_fuzz_test.go:190); cmd/evener-hub/app_relay.go:97-113 (`relayRetryClock` interface, `relayTimerClock`, `relayRetryClockFunc`, `newRelayRetryClock`; the same job is also done by `cfg.RelayHooks.RetryWait` at :481); agent/fork.go:137-141 (`forkTranscriptWriter` interface with one implementation, `transcript.Writer`); agent/job_transcript_read.go:41-46 (`localJobProjectDirectory`).
Evidence: each has exactly one non-test implementation (grep of implementers). The relay clock has three layers (interface, struct, func adapter) for `time.NewTimer` plus a config hook that duplicates it.
Impact: indirection with no polymorphism; 3-4 types per seam.
Fix plan: Replace each with a plain func field (`wait func(ctx, d) error`, `newTicker func(d) (<-chan time.Time, func())`) or delete if the test can use a real implementation with short durations (accumulator, ticker). Remove the `registerJobToolsWithRegistrar` split: `registerJobTools` takes `reg` and the fuzz test injects a failing registrar by registering a duplicate/invalid tool name through the real Registry.
Size: ~-120 lines. Risk: low-medium; fuzz-tagged tests need updating (they are build-tagged, confirm with `go vet -tags` on those packages).

### x-error-handling-slop-8: Duplicated default-writer wiring in fork/aside
Locations: agent/fork.go:101-105 and :129-133 (identical `forkSessionDeps{newWriter: func...{return transcript.NewWriterWithFS(...)}, saveMeta: schema.SaveSessionMetaWithFS, maxScanToken: 10*1024*1024}`), agent/aside.go:54-55.
Evidence: three copies of the same literal; `forkSessionFS` at :128 just builds deps and calls `forkSessionWithDeps`; the other copy is inline in the sibling function.
Impact: drift risk (the 10 MiB scan limit is in two places). Small.
Fix plan: `func defaultForkDeps() forkSessionDeps` once; callers use it. Fold into finding 7 (drop the writer interface).
Size: -20 lines. Risk: low.

### x-error-handling-slop-9: Dead production code retained "for fuzz owners" (15 `//nolint:unused`)
Locations: agent/session_tools_jobs.go:2575-2605 (`watchSendArg`, `isEmptyWatchSend`: "retained for internal watch-send parsing; public job_watch currently rejects send"; only reference is session_tools_jobs_seed100_range_d_test.go:55), :369 `clearDescendantReceiverWatchByID`, :2673 `projectJobRecord`; agent/jobs.go:461 `noteJobActivity`, :952 `abandonRunningJob`, :1377 `readOutputHead`, :1401 `headOutput`, :1779 `finalizeWithRunNoNotification`, :2310 `outputDropped`; agent/job_watch.go:5216 `recordWatchSendPending`; agent/subagent_manager.go:157 `beginReconstructionSideEffects`; agent/sandbox_delegate.go:725,750 `cloneSandboxSnapshot`, `sandboxPolicyFromSnapshot`; internal/plugins/enabled.go:15 `listOrWarn`.
Evidence: the nolint comments themselves say the code is unreferenced in production and kept for build-tagged tests ("tagged job-manager recovery fuzz owners").
Impact: ~15 functions (hundreds of lines in the already 6500-line job_watch.go and 2400-line jobs.go) that production never runs but maintainers must keep compiling and reasoning about; the linter exemption hides further drift.
Fix plan: For each, check the tagged tests: if the test only exercises the helper itself, delete both. If it asserts behavior that production reaches another way, repoint the test at the production path. Moves the survivors into the `_test.go` file that uses them. Update docs only if the watch `send` field is mentioned in a product doc (grep `docs/product` first).
Size: -400 to -700 lines. Risk: low (compiler plus `go vet -tags evenerfuzz` per package; do not run suites locally).

### x-error-handling-slop-10: Production code carrying ForTest/ForTesting shims
Locations: agent/session_notes_rpc.go:402-420 (`AddSessionURLForTest`, `HumanNoteForTest`, `SessionURLsForTest`: "exposing the unexported store for the daemon's server-side tests"); cmd/evener-hub/app_threadread.go:1635 `reconcileDelegateThreadItemForTest` (a one-line wrapper over `reconcileDelegateThreadItem`, same package as its tests, so unnecessary); agent/session_client_mutation.go:691 `SimulateCrashMidExecutionForTest`; agent/session_jobtree_drain.go:226, subagent_tally.go:60; transcriptindex/index.go:55,1407; sandbox/session_tmp.go:53; execenv/gitpath.go:35, search_patterns.go:180, local.go:249; hubcore past.go:216,1267, resumelocks.go:131, transcript_display_store.go:73, keybindings_store.go:70; valueexpr/evaluate.go:107; apptranscript/read_stats.go:23.
Evidence: 21 such functions. `reconcileDelegateThreadItemForTest` is called only from app_threadread_test.go:512,697,723 in the same package.
Impact: exported test API in production packages; cross-package tests depend on it.
Fix plan: Delete the same-package wrappers outright (rename call sites to the real function). For cross-package ones, move into an `internal/<pkg>test` helper or an `export_test.go`-style file only where the consumer is in the same package; otherwise make the tests drive the real RPC (notes URLs have an RPC surface: session_notes_rpc.go).
Size: -100 lines. Risk: low.

### x-error-handling-slop-11: Pass-through wrapper chains and renamed aliases (146 pure forwarders)
Locations: cmd/evener-hub/app_compact.go:11-17 (`shouldResumeAfterTurnStartError` -> `shouldResumeAfterSessionUnavailable` -> `isSessionUnavailableError`: three names, one body); agent/runtime_dir.go:71 `hexHash` -> `nonProjectHash` ("retained for non-project cache/test callers") and agent/tool_web_fetch.go:34 `webFetchCacheKey` -> `nonProjectHash`; agent/transcript_lookup.go:381 and agent/doctor/locate.go:197 both define `refFor` -> `bucketref.RefFor` (and :388 `stateHomeFor` -> `userdirs.StateHomeForBucketDir`); agent/execenv/local.go:3647 `ShellEscapeArgs` -> `shellquote.Args` ("this name is kept for its callers"); agent/execenv/gitpath.go:247,255,267 (three forwarders to `identifier`); agent/internal/clock/clock.go:57-58 `Sleep`/`After`; cmd/evener-hub/internal/hubcore/roster.go:218,222 and appsource/local_daemon.go:1344,1348 (`cloneRunningJobs`, `cloneLocalDaemonJobs` -> `appwire.CloneEvenerJobs`, duplicated in two packages); app_host_manage.go:1054 `hubTOMLSyncUnsupported` and hubcore/state_file.go:127 `deletionSyncUnsupported` -> `fsdurability.SyncUnsupported`; agent/subagents.go:359 `hasString` -> `slices.Contains` and fuzz/schemagen/schema.go:134 `contains` -> `slices.Contains`; agent/session_client_mutation_queue.go:566,861,973,1103,1188 (`AcceptClientMutation*` -> `clientMutation*`).
Evidence: the AST scan found 146 functions whose body is a single call forwarding every parameter unchanged; roughly 60 are platform-split stubs or interface adapters (legitimate), the rest are renames. `hexHash` is documented as retained "for non-project cache/test callers".
Impact: vocabulary bloat (two or three names for one thing, e.g. `nonProjectHash`/`hexHash`/`webFetchCacheKey`), grep noise, every wrapper is a place where a doc comment can drift.
Fix plan: Inline wrappers whose only value is a rename; keep exported ones only if they are the package's deliberate API. Order: app_compact chain, hexHash/webFetchCacheKey, duplicate `refFor`, `hasString`/`contains`, then the clone and SyncUnsupported duplicates (point both packages at the shared function directly). Compiler-guided; no behavior change.
Size: -250 lines. Risk: low.

### x-error-handling-slop-12: Hand-rolled temp-file+rename atomic writers (7+ distinct helpers, 33 files)
Locations: internal/transcriptindex/index.go:1294 `writeFileAtomically` (fixed `path+".tmp"` name, no fsync), llm/registry/refresh.go:48 `writeAtomic` (CreateTemp, no fsync, no dir sync), internal/plugins/atomic.go:32 `atomicWriteFile`, cmd/evener-dev/agentshards.go:750 `writeFileAtomic` (CreateTemp + chmod 0644), launchconfig/io.go:53 `writeAtomicFS`, hubcore/state_file.go:35 `writeStateFileAtomic`, agent/execenv/securepath_fdops_unix.go:916 `atomicWriteAt`; plus inline copies in auth/openai/storage.go, internal/credentials/store.go, agent/task/task_store.go:350, agent/schema/snapshot.go, session_queue_persist.go, session_client_mutation_persist.go, hostops/store.go, etc.
Evidence: 33 non-test files call `Rename(tmp...)`; 16 files use `os.CreateTemp`. Durability guarantees differ silently: transcriptindex and registry/refresh skip fsync, plugins/hubcore do it; each copy re-implements `_ = os.Remove(tmp)` cleanup on every failure branch (the largest source of the `_ = os.Remove` count: 60).
Impact: a durability bug fixed in one copy stays in the others; this is the exact case AGENTS.md describes (user's work preserved across crash).
Fix plan: Add one package (e.g. `internal/atomicfile`: `Write(path, data, perm, Options{Fsync, DirSync, Fs})` and a streaming `Writer` variant) next to cmd/evener-hub/internal/fsdurability's logic (move that to the shared package since hub-internal is not importable by agent/ and llm/). Migrate callers one package per PR, red-test-first with a fault-injecting Fs where the behavior changes (adding fsync). Delete private helpers as they empty out.
Size: +150 new, -600 removed. Risk: medium (permission and fsync semantics must be preserved per caller; keep per-caller options).

### x-error-handling-slop-13: Hash-writer discards (128 `_, _ = h.Write`) and fingerprint helper drift
Locations: cmd/evener-hub/internal/hubcore/roster.go:480-508 `rosterFingerprint` (102 consecutive `_, _ = h.Write([]byte{0})`-style lines), hubcore/past.go (9), appsource/item_paging_state.go:123-129 (8), hubcore/past_root_fingerprint.go:116 `writeHashString` (the helper that should have been reused).
Evidence: `hash.Hash.Write` never returns an error by contract; the code writes a field then a `0` separator manually, over 100 times in roster.go alone. `writeHashString(h, s)` already exists in the same package doing precisely that.
Impact: noise that inflates the swallowed-error count and invites field-order mistakes.
Fix plan: Use `writeHashString`/a small `fieldHasher` type with `String(s)`, `Bool(b)`; rewrite rosterFingerprint using it. The output hash must stay identical or fingerprints churn once; if stability matters, add a golden test over a fixed roster before refactoring.
Size: -90 lines. Risk: low-medium (hash value stability; guard with golden test).

### x-error-handling-slop-14: Legacy/migration code and history-narrating comments
Locations: agent/memory_migrate.go (85 `legacy` mentions), agent/delegate_legacy_state.go (19), cmdutil/legacymigration.go (14), cmd/evener-migrate/main.go, hubcore/index_schema.go (33), transcriptindex/build.go (23); narrating comments: agent/session_notes.go:130 ("costs only the walk this used to do unconditionally"), server/appwire_runtime.go:2460 and :2999 ("They used to be separate expressions"), agent/session_tools_ask.go:119 ("Two call sites used to assign s.askPending directly"), agent/session_attention.go:154, cmd/evener-hub/internal/sshconn/version.go:662 ("used to run lsof unconditionally"), agent/doctor/audit.go:808 ("finding 1: the sweep previously used followSelector"), cmd/evener-fuzzcov/main.go:13 ("The per-target coverage reporter that used to live here was deleted"), agent/session_lifecycle.go:171,465 ("spec §P1, rev-9.1 finding O1"), session_worktree_resume.go:248 ("rev-7 review finding"), session_tools_worktree.go:2599 ("deferred from step 4; code-review finding I1"), cmd/evener/plugincmd.go:78 ("added in Task 4").
Evidence: ~200 comment lines match used-to/previously/was-removed/retained-for/kept-for; 15 cite review revisions or findings ("rev-9.1 finding O2", "G1"); 939 cite `spec §`. Many "no longer" hits are legitimate state descriptions and were not counted as slop.
Impact: comments rot; reviewer ids point to documents a newcomer cannot resolve. Per project rule, history belongs in the commit message.
Fix plan: Mechanical comment-only pass: delete sentences that narrate old behavior or cite review round ids; keep the invariant they explain, restated in present tense. Separately, ask Jesse which legacy migrations can be retired (memory_migrate, cmdutil/legacymigration, delegate_legacy_state): removal of compat needs his explicit sign-off, so list them with last-touched dates (`git log -1`) rather than deleting. Optionally add a lint grep for `rev-[0-9]|finding [A-Z][0-9]|used to |previously` in new comments.
Size: ~-300 comment lines; migration removal separately sized after approval. Risk: low (comments); medium (migrations, user data).

### x-error-handling-slop-15: Nil-receiver checks on `*Session` methods (140 in agent)
Locations: agent/session_tools_jobs.go (15), session_attention.go:567,715,725,744,908,926 (14 in file), delegate_runtime.go (14), job_watch.go (13), session_init.go (9), session_jobtree_drain.go (8); jobs_nested.go:14,245,361.
Evidence: `if s == nil { return false }` at the top of methods like `hasPendingDelegateAttentionArmRetry` and `hasPendingRootDelegateAttention`; session_attention.go:567 returns `errors.New("delegate attention session is nil")`. `NewSession` (session_init.go:298) never returns a nil Session with a nil error.
Impact: low per instance, but 140 copies tell readers sessions may be nil anywhere, masking the few real optional-parent cases (parent session in subagent trees). I did not trace every caller, so some are likely load-bearing for the root-has-no-parent case.
Fix plan: Audit by caller: where callers hold `s.parent` that can legitimately be nil, move the nil test to the call site once (`if parent != nil`); delete the receiver check elsewhere. Low priority; do opportunistically when touching a file. No product doc impact.
Size: -300 lines at most. Risk: low-medium (a missed nil caller becomes a panic; review each).

### x-error-handling-slop-16: Swallowed errors on state-bearing paths worth explicit policy (small set)
Locations: agent/session_execution.go:446 `_ = s.recordTranscriptOnlyAt(...TurnNotice...)`; agent/session_events.go:150 `_ = store.MutateAndPublish(...)`; agent/session_memory.go:201 `_ = migrateMemoryScopeCtx(ctx, env)`; cmd/evener-hub/internal/appsource/item_paging_state.go:88 `_ = c.putContext(...)`; auth/openai/service.go:234 `_ = s.openBrowser(authorizeURL)`; agent/delegate_runtime.go:1637 `_ = s.executeDelegateMutationPlans(plans)`; agent/session.go:2073 `_ = s.delegateController.recordTerminalSeen(lease)`; internal/plugins/resolve.go:135 `_ = os.WriteFile(...stagingMarker...)`; agent/session_lifecycle.go:732,892 `_ = s.closeAttachedTranscript()`.
Evidence: most of the ~1550 discards are cleanup (Close/Remove) and are fine; these few drop errors from operations that persist or publish user-visible state, with no log line or comment saying why failure is acceptable.
Impact: a failed transcript notice or mutation plan vanishes silently, against the AGENTS.md preservation-of-work principle. I did not verify each for a compensating recovery path; some likely have one.
Fix plan: For each, either log via the existing logf at warn level or add a one-line comment naming the recovery path that makes the drop safe. Introduce a tiny `bestEffort(err, what)` helper that logs, replacing bare `_ =` on this class. Add behavior tests only where a drop is found to lose work.
Size: ~60 lines. Risk: low.

### x-error-handling-slop-17: Unchecked `fmt.Fprintf` noise (480 `_, _ = fmt.Fprintf`)
Locations: cmd/evener-hub/main.go (61 discards), cmd/evener/plugincmd.go (53), cmd/evener-dev/agentshards.go (46), cmd/evener/main.go (41), cmd/evener/openai_login.go (37), cmd/evener/providers.go (26), cmd/evener-dev/covstmt.go (26).
Evidence: 566 `_, _ = fmt.Fprint*` lines in CLI code writing to stderr/stdout writers; `cmd/evener-hub/main.go:282-315` is a run of them.
Impact: cosmetic, but it is the largest bucket of "swallowed error" counts and hides the 15 real ones above from any errcheck-style review.
Fix plan: Per command file, add `say := func(format string, a ...any){ fmt.Fprintf(w, format, a...) }` or an `errWriter` that latches the first error; excluding `fmt.Fprint*` from errcheck in golangci config is the one-line option. Prefer the lint config exclusion (fewest changes), then delete the `_, _ =` prefixes in files being touched anyway.
Size: -0 to -500 (prefix removal). Risk: very low.

## Recommended fix sequence

1. x-error-handling-slop-3 and -4: small, compiler-guided, remove real silent failures and dead parameters (about 120 lines).
2. x-error-handling-slop-9: delete the 15 `nolint:unused` functions with their only-test callers; shrinks job_watch.go and jobs.go before any file split.
3. x-error-handling-slop-11 and -8: inline pass-through chains and duplicate defaults (mechanical).
4. x-error-handling-slop-10 and -7: delete ForTest shims and single-impl interfaces; this also removes seam globals.
5. x-error-handling-slop-5 then -6: instance-scoped seams, starting with the unsynchronized hooks (server/thread_history.go, transcriptindex), then the internal/plugins fsOps struct; add the "no new `var x func`" lint after.
6. x-error-handling-slop-12: shared atomicfile package; migrate callers one package at a time.
7. x-error-handling-slop-13 and -17: hash helper and errcheck exclusion for fmt.Fprint*, after which the remaining swallowed-error list is short enough to review (finding 16).
8. x-error-handling-slop-1 and -2: structural splits (relay manager first, then serve flags, runMain), one function per PR, after the dead code is gone so the moves are smaller. Add a funlen baseline lint.
9. x-error-handling-slop-14 and -15: comment cleanup and nil-receiver audit opportunistically; legacy migration removal only after Jesse approves each.
