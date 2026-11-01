# Audit slice: agent-misc-root

Scope: every non-test `agent/*.go` file at the package root that does not match
`session_*`, `delegate_*`, `subagent*`, `job*`, `jobs*`. Plus a judgment on
whether package `agent` should be split.

## Overview and inventory

| Measure | Value |
|---|---|
| Slice files | 58 non-test files, 17,162 lines |
| Whole package root | 176 non-test files, 105,781 lines |
| Package root tests | 957 `_test.go` files, 374,987 lines (3.5x production) |
| Test files named `*cov*` | 143 files, 32,025 lines |
| Test files named `*fuzz*` | 114 files, 34,725 lines |
| Slice lines in files that never mention `Session` | 9,552 (56%) |
| Slice comment lines | 4,305 of 17,162 (25%) |
| `#NNNN` issue refs / `kata` ids / `round N` in slice code comments | 35 / ~13 / 8 |

Largest slice files: `session.go` 2,784 (569 commits), `transcript_render.go`
1,978 (62), `retirement.go` 817, `resource_limits.go` 804, `apilog_read.go` 794,
`sandbox_delegate.go` 772, `memory_migrate.go` 671, `status.go` 649,
`transcript_lookup.go` 563, `retained_output_read.go` 476, `transcript_read.go` 458,
`workspace_info.go` 392, `fork.go` 374, `failure_steering.go` 370.

Module context that constrains any split: `agent/go.mod` is its own module and
`require`s `primeradiant.com/evener` (root), while the root `go.mod` requires
`primeradiant.com/evener/agent`. Package `agent` already imports root-module
packages (`appwire`, `internal/apptranscript`, `internal/bundled`,
`internal/plugins`). That is why `/frontmatter` (root) cannot import
`agent/internal/frontmatter` (finding 15), and why new leaf packages must not import
back into `agent`.

Product-model notes used for the fix plans (AGENTS.md, docs/product/README.md):
shared derivations belong to a single owner (here: the wire/read-model types),
fixes must be behavior-preserving and test-guarded, and `docs/product/subsystems.md`
plus any linked file paths (for example `docs/product/session-activity.md:292`
links `agent/status.go`) must be updated in the same change as a move.

Method note: dead-code claims below come from a name-reference scan of all 176
production files (comments excluded) plus repo-wide greps; "test-only" means no
reference in any non-test `.go` file in the repository. No test suites were run.

---

## Findings

### agent-misc-root-1 (high) Package `agent` is a god package; Session-free subsystems should leave it

Locations
- `agent/session.go:109-1127` (the `Session` struct is 1,017 lines).
- `agent/session_config.go:318` (`testConfig`, 91 fields, 481 lines) read at 122 production sites (`grep -c cfg.testOnly. ` outside tests).
- `agent/session.go:1127-1456` (job-notification retry machinery inside the session file).
- Session-free clusters in this slice: `transcript_render.go`, `transcript_read.go`, `transcript_lookup.go`, `transcript_ref.go`, `transcript_util.go`, `retained_output_read.go`, `apilog_read.go`, `resource_limits.go`, `memory_page.go`, `memory_index_render.go`, `memory_migrate.go`, `memory_scope_pages.go`, `memory_write_page.go`, `workspace_info.go`, `git_snapshot.go`, `project_docs.go`, `salvage.go`, `responses_continuation_eligibility.go`, `sandbox_infra.go`.

Evidence
- `Session` has ~274 fields, 22 mutex/once/atomic members (`eventsMu`, `closeCtxMu`, `memoryMu`, `mu`, `metaSaveMu`, `goalUpdateMu`, `notesUpdateMu`, `attentionMu`, `delegateDeliveryMu`, ...), and a hand-written lock-order essay in the struct comment (`session.go:280-330`).
- 1,082 `func (s *Session)` methods are spread across 92 files; `session.go` alone has 569 commits.
- 56% of the slice (9,552 of 17,162 lines) is in files that never name `Session`, yet they all share the package namespace with generic unexported names (`maxEntries`, `excludedDirs`, `parseArgs`, `oneLine`, `collapseWhitespace`, `scalarString`, `formatNumber`).
- Coupling of the transcript reader cluster to the rest of the package is small: scanning the other 170 production files, only ~50 of the cluster's 125 top-level identifiers are referenced from outside it (`transcriptPath` 37, `encodeRef` 19, `validateLayoutPrefix` 6, `readTranscriptFull` 6, `parseRangeErr` 5, `symlinkErrorDeep` 5, `decodeRef` 5 ...).
- `resourceCapsFromEnv` (804 lines, `resource_limits.go`) has exactly one caller, `env_info.go:26`.
- The 375k lines of tests all live in one package, so every edit to any file recompiles and re-links one 480k-line test binary.

Impact
Every change anywhere in the runtime touches the same 100k-line package, so review,
build and test cost scale with the whole thing; the generic-name namespace invites
the duplicate helpers found below; `Session` invariants cannot be reasoned about
locally.

Fix plan (simplest first, each step is a pure move guarded by the existing tests)
1. `agent/hostresources` (new, leaf): move `resource_limits.go` (804) and the `schema.ResourceCaps` producer. Single caller (`env_info.go`). Export `CapsFromEnv(env)`. No Session references.
2. `agent/memorypages` (new, leaf): move `memory_page.go`, `memory_index_render.go`, `memory_scope_pages.go`, `memory_write_page.go`, `memory_migrate.go` (~1,380 lines). First do findings 15/16 so the frontmatter editors have a proper home. `session_memory.go`/`session_tools_memory.go` then import it.
3. `agent/transcriptview` (new): move `transcript_render.go`, `transcript_read.go`, `transcript_lookup.go`, `transcript_ref.go`, `transcript_util.go` (~3,100 lines) and export the ~20 identifiers listed above. Do findings 3, 11 and 14 first so the exported surface is small.
4. `agent/artifactread` (or into `agent/internal/jobstore`, which already owns `jobstore.SearchOptions` that `retained_output_read.go` embeds): move `retained_output_read.go` and `apilog_read.go`.
5. `agent/envsnapshot`: `workspace_info.go`, `git_snapshot.go`, `project_docs.go`, `env_info.go` (prompt-input collection; no Session state).
6. Leave `Session`, `retirement.go` (holds `*Session`, `*delegateTreeController`), `status.go`, `sandbox_delegate.go` in place; do not attempt to split `Session` itself now. The struct comment already documents which fields `mu` guards; the realistic next cut is extracting the job-notification retry state (`notificationRetry`, `runningTurnReleaseRetryState`, `session.go:1127-1456`) into a small struct, but that needs its own design pass.
7. Move tests with their subjects (tests currently named `transcript_render_*_test.go`, `apilog_read_covtest_test.go`, `memory_migrate_*_test.go`, `resource_limits*_test.go` follow mechanically).
8. Docs: update `docs/product/subsystems.md` rows that name agent files, `docs/product/session-activity.md:292` (links `agent/status.go`), and the contributor map in `docs/developing-evener/` if it lists agent files. Cross-module cycle: new packages go inside the `agent` module and import only `schema`, `execenv`, `transcript`, `llm`; verify with `go list -deps`.

Size: net near zero (moves); ~1,500 lines of import/export-visibility edits in callers; per-step PRs of 300-3,000 moved lines.
Risk: low per step for 1, 2, 5 (leaf); medium for 3 and 4 (many unexported helpers become exported; keep the exported surface to what `session_tools_transcript.go` and `session_attention.go` use). Do not rename behavior.

### agent-misc-root-2 (high) One delegate-status record is copied across four struct types and five hand-written mappers

Locations
- `agent/status.go:124-181` `DelegateStatusInfo` (55 fields) and `status.go:485-567` `delegateStatusInfo`.
- `agent/status.go:577-610` `delegateUpdatedDataFromStatus` into `agent/events/payloads.go:748` `DelegateUpdatedData` (57 `json:` fields).
- `server/server.go:105` `DelegateStatusInfo` (a second copy of the field set), filled by `cmd/evener/serve.go:2315-2345`.
- `server/appwire_runtime.go:2827-2856` `appDelegateFromDetailedStatus`.
- `cmd/evener-hub/app_threadread.go:1278-1307` `appwireDelegateFromAgentStatus`.
- Target type `appwire/types.go:1788` `EvenerDelegateInfo` (60 `json:` fields).

Evidence
Each mapper is a 30-line positional field copy (`DelegateID: delegate.DelegateID, OwnerSessionID: ...`). `DelegateStatusInfo`'s own doc comment says "AppWire maps these values into EvenerDelegateInfo". `agent` already imports `appwire` (`status.go` uses `appwire.EvenerUsage`, `appwire.JobActivityWorktree`), so no dependency forces the copies. The `Usage` and `Worktree` pointers are re-copied into `events.DelegateUsageData` and `events.DelegateWorktreeData` field by field (`status.go:600-609`) although `appwire.EvenerUsage` and `appwire.JobActivityWorktree` are already the types in `DelegateStatusInfo`.

Impact
A new delegate field needs eight coordinated edits (3 structs, 4 mappers, tag set); a missed one silently drops the field on one surface (hub thread read vs. daemon status vs. pushed event). This is the shared-client-owns-derivations rule violated at the Go layer.

Fix plan
1. Make `appwire.EvenerDelegateInfo` the single record. In `agent`, `type DelegateStatusInfo = appwire.EvenerDelegateInfo` (agent already imports appwire; keep the alias only if external callers need the old name, otherwise rename callers: `cmd/evener-hub/app_threadread.go:1256`, `cmd/evener/serve.go:2315`, `server/server.go:105`).
2. Delete `server.DelegateStatusInfo` and the copy loop in `cmd/evener/serve.go:2315-2345`; the daemon status type carries `[]appwire.EvenerDelegateInfo`.
3. Delete `appDelegateFromDetailedStatus` and `appwireDelegateFromAgentStatus` (identity).
4. For `events.DelegateUpdatedData`: either embed/alias the same record (preferred; `events` is in the agent module, appwire is a root dependency of agent already) or keep it and generate the one remaining mapper with a reflection-based round-trip test that fails when field sets diverge.
5. Test: the existing delegate status golden/JSON tests stay; add one test asserting `json.Marshal` of a fully-populated record is byte-identical before and after (capture current output first, red-green not needed because this is behavior-preserving).
6. Docs: `docs/product/session-activity.md` and the status roster note in `subsystems.md` name the owner (appwire record).

Size: about -250 lines (four mappers and two struct copies), +20.
Risk: medium (wire JSON must stay identical; diff the `json` tags of the copies first, since they were not verified identical, and pick the appwire ones, which are the contract).

### agent-misc-root-3 (high) The transcript "read header, then entries" loop is hand-rolled at least six times

Locations
- `agent/transcript_read.go:83-147` `readSemanticTranscript`.
- `agent/fork.go:190-262` `readForkParent`.
- `agent/session_attention.go:80-125` `extendDelegateAttentionFoldFrom`.
- `agent/transcript_render.go:319-490` `rawLinesForRange` (two `ReadLine` loops, lines 335 and 381).
- `agent/doctor/transcript.go:36-75` `loadTranscriptWithMaxLineBytes`.
- Further copies outside agent: `internal/apptranscript/apptranscript.go:1359`, `internal/transcriptindex/index.go:1081`, `cmd/evener-hub/image_serve.go:157`, `server/thread_history.go:806`, `cmd/evener-doctor/reconstruct.go:194`.

Evidence
Same skeleton each time: `bufio.NewReaderSize(f, 64*1024)`, `transcript.ReadLine`, `bytes.TrimSpace`, first non-empty line `DecodeHeader`, later lines `DecodeEntry`. The copies drifted on exactly the parts that matter: `transcript_read.go` special-cases `ErrUnsupportedFormat` and wraps with a corruption sentinel; `fork.go` returns bare `fmt.Errorf` and has a `maxScanToken <= 0` default of `10*1024*1024` while the reader's default is `128<<20` (`transcriptJSONLMaxLineBytes`); `doctor` reports line numbers; `session_attention.go` checks the session id and tracks consumed byte offsets. Fork's 10 MiB cap means a session whose transcript has a line between 10 MiB and 128 MiB reads fine but cannot be forked or asided.

Impact
Every transcript format change (a new record kind, boundary rule, size cap) needs six edits; the 10 MiB vs 128 MiB split is a latent behavior bug (fork refuses what resume accepts).

Fix plan
1. Add to `agent/transcript` one streaming reader: `transcript.ReadAll(r io.Reader, opts ReadOptions) (Header, []Entry, ReadStats, error)` plus an `Each` callback form for `session_attention` (which needs consumed offsets) and `rawLinesForRange` (which needs raw lines). Options: `MaxLineBytes`, `RetainLines`, `ExpectSessionID`, `CorruptSentinel`. Build it by lifting `readSemanticTranscript` (it already has this shape).
2. Port in order of risk: `fork.go` (red-first test: a 20 MiB line transcript forks successfully), `doctor`, `session_attention`, then `transcript_render.rawLinesForRange`.
3. Delete the loops. `readTranscriptFull`, `readStrictChildTranscript*` become thin option presets (or call sites pass options directly).
4. Tests: the existing `transcript_*`, `fork_*`, `doctor` suites; add the fork large-line test.
5. Docs: none beyond the `agent/transcript` package comment.

Size: -250 lines, +120.
Risk: medium (error text and sentinel semantics differ by caller; keep `errors.Is` sentinels stable and port one caller per commit).

### agent-misc-root-4 (medium) XDG state-root resolution is reimplemented seven times with different fallbacks

Locations
- `agent/runtime_dir.go:73-95` (`xdgStateHome`, `xdgCacheHome`; HOME failure falls back to `os.TempDir()`).
- `cmdutil/statedir.go:35-55` (`StateRootFromLookup`; falls back to `"."`).
- `rendezvous/rendezvous.go:52-62` (`defaultDir`; `"."`).
- `auth/openai/storage.go:98-110` (`os.TempDir()`).
- `llm/registry/load.go:200-212` (`defaultStateRoot`, reads `HOME` only; `"."`).
- `appwire/frame_recorder.go:90-103` (`recorderStateRoot`; relative `.local/state/evener`).
- `cmdutil/legacymigration.go:90` (a seventh `.local/state` join).
- Layout string `"evener", "projects"` is joined by hand in `agent/runtime_dir.go:37,49`, `agent/transcript_lookup.go:87,244,245,416,495`, `agent/job_transcript_read.go:80,179`, `agent/doctor/locate.go:124`, `cmd/evener-hub/config.go:397`.

Evidence
Four distinct fallbacks when the home directory is unknown (`os.TempDir()`, `"."`, a relative path, empty) so two processes started with the same env but no HOME can compute different state roots. The `frame_recorder.go` comment says outright "It is duplicated here only to keep appwire ... free of a dependency on the cmd helper layer". `envvars/userdirs` already owns `ConfigRoot`, `DefaultConfigRoot`, `StateHomeForBucketDir` but not the state counterpart. Four names also exist for one hash in the same file: `shortHash` -> `nonProjectHash` <- `webFetchCacheKey`, plus `hexHash` (`runtime_dir.go:62-71`), where `hexHash` is referenced only by tests.

Fix plan
1. Add `userdirs.StateRoot(xdgStateHome string, userHomeDir func() (string, error)) string`, `DefaultStateRoot()`, `CacheRoot()`, `ProjectsDir(stateHome)` mirroring `ConfigRoot` (envvars is a leaf module every other module already requires). Decide the single HOME-less fallback with Jesse (recommend `os.TempDir()`, which keeps state out of the cwd; the `"."` variants can write state into the user's repo).
2. Replace the seven bodies with one-line calls; keep `cmdutil.StateRootFromLookup` as the Windows/Android home-resolution layer if its tests need it, but make it call `userdirs`.
3. Delete `hexHash`, `shortHash`, `webFetchCacheKey` wrappers (use `nonProjectHash` directly, renamed `shortHash`).
4. Tests: table test in `userdirs` for the fallback matrix; existing per-package tests keep passing.
5. Docs: `docs/developing-evener/environment` page if it documents state locations.

Size: -90 lines, +50.
Risk: low-medium: the HOME-less behavior change is the only observable difference; confirm choice with Jesse before changing any fallback.

### agent-misc-root-5 (medium) The "directory fsync, tolerate unsupported" idiom and its errno predicate exist in at least eight copies

Locations
- `agent/image_persist.go:178-186,225-236` (`syncAttachmentDirs`, `fsyncAttachmentDir`).
- `agent/session_client_mutation_persist.go:423-427` `clientMutationSyncUnsupported`.
- `agent/internal/worktree/sync_unix.go:19-45` `syncDir`, `syncUnsupported`.
- `agent/internal/jobstore/output.go:947-958` `syncParentDir` (no tolerance at all).
- `cmd/evener-hub/internal/fsdurability/fsdurability.go:20` `SyncUnsupported` (the documented "one canonical predicate").
- Wrappers of that predicate: `cmd/evener-hub/app_host_manage.go:1054` `hubTOMLSyncUnsupported`, `cmd/evener-hub/internal/hubcore/state_file.go:127` `deletionSyncUnsupported` (its comment: "the name the deletion store's tests use").
- Package-private copies of the errno test: `auth/openai/storage.go:229` `authSyncUnsupported`, `server/thread_clear_journal.go:155` `journalSyncUnsupported`, inline in `internal/hubendpoint/runtime.go:196-206`. (`hostops/store.go`, `hubcore/state_file.go`, `app_host_running.go` already call `fsdurability`.)

Evidence
Identical `errors.Is(err, syscall.ENOSYS) || ENOTSUP || EINVAL` body in at least six files (agent x2, `auth/openai`, `server`, `hubendpoint`, `fsdurability` itself); `fsdurability`'s package comment says other modules "keep matching package-private copies of the same tolerance in their own modules". Copies disagree on policy: `jobstore.syncParentDir` fails on EINVAL, `image_persist` tolerates it. `syscall.ENOTSUP` is also not portable to Windows builds the way these files assume.

Fix plan
1. Create `execsupport/fsdurability` (leaf module that agent, root and server already require; `auth` needs one `require`) with `SyncUnsupported(err)` and `SyncDir(fs afero.Fs or path)` implementing open+sync+tolerate.
2. Replace the copies; delete the wrapper names (`hubTOMLSyncUnsupported`, `deletionSyncUnsupported`, `clientMutationSyncUnsupported`, `syncUnsupported`); delete `cmd/evener-hub/internal/fsdurability`.
3. For `jobstore.syncParentDir`, decide explicitly whether tolerating unsupported sync is the desired policy; the recovery-over-lockdown principle in AGENTS.md says a durability nicety must not become an outage, so tolerate.
4. Tests: one table test in the new package; remove per-copy predicate tests that only asserted the errno list.

Size: -120 lines.
Risk: low; one behavior change in jobstore (needs a test: unsupported-sync filesystem still lets output metadata write succeed).

### agent-misc-root-6 (medium) Evaluation harness compiled into the production package and used only by tests

Locations: `agent/eval.go` (146 lines), `agent/eval_probes.go` (145 lines).

Evidence
`newEvalCollector`, `runRetentionProbes`, `runOneProbe`, `buildBinaryJudgePrompt`, `parseBinaryJudge`, `turnsToMessages`, `evalCollector`, `evalMetrics`, `f2pResults`, `probeQuestion`, `probeResult` have zero references in any non-test `.go` file in the repo (checked: `grep -rnw` over the repo excluding `_test.go`; the only `probeResult` hits are an unrelated type in `tools/tool-fluency`). Test users: `memory_eval_support_test.go`, `eval_test.go`, `eval_probes_test.go`, `eval_lifecycle_program_fuzz_test.go`. `f2pResults` has no test or code user at all and `Diff`/`F2PResults` fields are never set.

Impact
~290 lines of dead production code, a `cheapmodel` import and an LLM-judge prompt shipped in the daemon binary.

Fix plan
1. Rename `eval.go` -> `eval_collector_test.go`, `eval_probes.go` -> `eval_probes_helpers_test.go` (or move into `agent/internal/liveeval` if a non-agent test needs them).
2. Delete `f2pResults`, `RetentionBreakdown`, `Diff`, `F2PResults` fields and `RetentionScore` if tests never assert them (check `eval_test.go` first).
3. Docs: none (`docs/superpowers/specs/2026-10-25-issue-3728-eval-completed-metric.md` describes the `Completed` semantics; keep that code).

Size: -290 production lines, tests unchanged. Risk: low.

### agent-misc-root-7 (medium) Dead historical-jobs chain across agent and hub

Locations
- `agent/historical_jobs.go:1-69` (`HistoricalJobRecord`, `LoadSessionHistoricalJobRecords`, `historicalJobsStat`).
- `cmd/evener-hub/app_threadread.go:1635-1680` (`reconcileDelegateThreadItems`, `reconcileDelegateThreadItem`, `reconcileDelegateThreadItemForTest`), `:1681-1707` (`delegateJobIDFromRaw`, `isTerminalHistoricalJobStatus`).

Evidence
`LoadSessionHistoricalJobRecords` has no non-test caller anywhere (`grep -rn` over repo, `_test.go` excluded: only its definition). `reconcileDelegateThreadItems` has no non-test caller; `reconcileDelegateThreadItem` is literally `{ _ = rec; return item }` (`app_threadread.go:1676-1679`). `HistoricalJobRecord.DelegateID` and `.TranscriptRef` are declared but never assigned by the loader (`historical_jobs.go:46-58` sets the other nine fields), and a hub test constructs those fields by hand (`app_threadread_test.go:695`), so the test exercises a shape production cannot produce. Tests that keep it alive: `cov_s1_job_log_helpers_test.go`, `delegate_resource_readonly*_test.go`, `agent_misc_program_fuzz_test.go`, `session_misc_fuzz_test.go`.

Fix plan: delete both files' dead symbols and the tests that only test them (`TestS1Cov_LoadSessionHistoricalJobRecords_*`, the fuzz branches); the read-only/no-file-creation assertions in `delegate_resource_readonly*_test.go` should be rewritten against a live read path if they are protecting something real (check what else reads `jobs.jsonl` read-only: `jobs_activity_past.go:324`).
Size: -150 lines production, -300 test. Risk: low.

### agent-misc-root-8 (medium) A long tail of production helpers whose only callers are tests

Evidence (name-reference scan of all 176 production files; each has exactly one non-comment mention, the definition, and 1-6 test files):
- `memory_migrate.go`: `moveLegacyMemoryIndex` (:75), `backUpMemoryIndex` (:98), `removeMigratedMemoryIndex` (:142), `migrateMemoryScope` (:512), `migrateLegacyMemoryIndexes` (:528): each is `return xCtx(context.Background(), ...)` and production calls only the `Ctx` forms (`session_memory.go:201` calls `migrateMemoryScopeCtx`).
- `live_model_metadata.go:58` `resolveLiveModelProfile` (see finding 9).
- `memory_index_render.go:264` `renderMemoryIndex`.
- `retained_output_read.go:267` `retainedMatchesSerializedSize` ("the O(matches) reference", a test oracle living in production).
- `sandbox_delegate.go:126` `prepareSubagentEnvironment`, `:726` `cloneSandboxSnapshot`, `:751` `sandboxPolicyFromSnapshot`.
- `session.go:2372` `appendTurnWithDurableTranscriptMessage`, `:2471` `writeTranscript`, `:2531` `writeTranscriptDurable`, `:2033` `communicateStructuredResult`. The comment at `session.go:2452` states "writeTranscript and writeTranscriptDurable are the only two doors to the writer", but production uses the `*Locked` variants and `recordTranscriptLocked`; the comment is wrong.
- `task_reminders.go:63` `taskReminderFullWithPrompts`.
- `runtime_dir.go:71` `hexHash`; `aside.go:24` `asideSessionFS`.
- `errors.go` (25 lines): `ErrPermissionDenied` and `PermissionDeniedError` have zero references anywhere in the repo, tests included.
- `prompt_data.go:142` `CatalogNameOrName`: no Go caller; confirm templates do not call it (`grep` the embedded prompt templates) before deleting.

Fix plan: delete each symbol, port its test to the real production entry (for the migrate wrappers, tests call `...Ctx(t.Context(), ...)`), and fix the `session.go:2452` comment. Do this after the moves in finding 1 so deletions are not rebased across them.
Size: -170 production, -100 test. Risk: low; compile errors enumerate every user.

### agent-misc-root-9 (medium) Live model profile resolution is six layers of wrappers with the same fill block pasted three times

Locations: `agent/live_model_metadata.go:35-156`; callers `session_init.go:350`, `session_init.go:1205`, `session_set_model.go:111`.

Evidence
Six functions: `resolveLiveModelProfileWithEnumerationTimeout` (:35), `resolveLiveModelProfile` (:57, test-only), `fillLiveModelMetadata` (:75), `resolveLiveModelProfileValidated` (:108, called only by `resolveStartupModelProfile`), `resolveStartupModelProfile` (:118), `resolveStartupModelProfileWithReuse` (:136). The three-line block `if res, err := client.Resolve(profile.ID()+"/"+profile.Model()); err == nil { profile = profile.WithResolved(res) }` appears at :91, :122 and :139. The two `Startup*` functions differ only in whether they also validate membership.

Fix plan: collapse to `resolveStartupModelProfile(client, profile, sessionID, timeout, primed, validate bool)` built from one `applyListing(client, profile, listing)` helper plus the existing `fillLiveModelMetadata`; delete `resolveLiveModelProfile` and `resolveLiveModelProfileValidated`; keep `resolveModelSwitchTarget` callers on `fillLiveModelMetadata`. Tests: existing `session_resolve_profile_test.go`, `session_init_*` tests cover both paths; add one case for "primed listing + validate=false doesn't reject".
Size: -45 lines. Risk: low.

### agent-misc-root-10 (medium) Fork and aside repeat the same construction three times and cap transcript lines at 10 MiB

Locations: `agent/fork.go:75-125,127-190,192-262,279-374`; `agent/aside.go:36-70,72-95`.

Evidence
- The `forkSessionDeps{newWriter: ..., saveMeta: schema.SaveSessionMetaWithFS, maxScanToken: 10*1024*1024}` literal is built in `fork.go:99-104`, `fork.go:128-134` and `aside.go:49-54`; `10 * 1024 * 1024` appears four times (also `aside.go:37`, `fork.go:200`).
- `forkSessionAtUserTurnFS` (fork.go:75) and `forkSessionWithDeps` (fork.go:136) duplicate: validate turn >= 1, `readForkParent`, `divergenceEntryAt`, load parent meta, `writeForkChild`, apply parent fork label (`fork.go:111-117` vs `fork.go:171-178` are the same block). Only the replacement turn differs.
- `writeForkChild` (:274) is a pass-through to `writeForkChildWithConfig` (:278); `asideSessionFS` (aside.go:24) is a pass-through (test-only).
- `RemoveSessionArtifacts` (aside.go:75-92) hardcodes `"sessions"` and the suffix list `.meta.json .meta.json.lock .transcript.jsonl .api.jsonl .log.jsonl` instead of `sessionsSubdir` and a shared layout list; fork.go:198 and :294 hand-build the transcript path (finding 11).
- Same-line comment `Safety net for early error returns` ... only matters because of the `published` flag; fine, but the whole function body (`writeForkChildWithConfig`, 95 lines) mixes header construction, replay, and meta construction.

Fix plan
1. One `forkChild(fs, stateDir, parentID, spec forkSpec)` where `forkSpec{divergenceTurn int; replacement *string; config *schema.ConfigSnapshot; parentLabel string}`; the three exported entry points build a spec. Deps default constructed once in a `defaultForkDeps()`; keep the `forkSessionDeps` seam only if a test injects a failing writer (it does: `fork_test.go`); that is a fault-injection need worth keeping, but build it from one place.
2. Use the finding 3 reader (removes the 10 MiB cap) and the finding 11 path helper.
3. Tests: existing `fork_test.go`, `fork_at_test.go`, `cov_s2_fork_test.go`, `cov_w3init_fork_test.go` cover all three modes.
Size: -90 lines. Risk: low-medium (hub maps the two `ErrDivergencePosition*` errors; keep them).

### agent-misc-root-11 (medium) Session file layout is hand-built at 16 sites despite a path helper

Locations: `agent/transcript_lookup.go:437` defines `transcriptPath(bucketDir, sessionID)` (37 uses), but `.transcript.jsonl` is joined by hand in `agent/fork.go:198,294`, `session.go:2783`, `session_init.go:601,990,1403`, `session_lifecycle.go:738`, `delegate_tree_start.go:160,349,651`, `delegate_tree_attention.go:963`, `delegate_runtime.go:3502`, `jobs_activity_past.go:324`, `aside.go:79`, `apilog_read.go:144`, `session_tools_transcript.go:1640`; and outside the package in `agent/doctor/locate.go:175,218`, `agent/doctor/turnids.go:164`, `cmd/evener/serve.go`, `cmd/evener-hub/{project_delete,image_serve,app_threadread}.go`, `cmd/evener-doctor/reconstruct.go`, `cmd/evener-fuzz-harvest/discover.go`, `cmd/evener-transcript-v2-upgrade/main.go`, `tools/tool-fluency/.../main.go`. Pass-through wrappers `stateHomeFor` (`transcript_lookup.go:388`, wraps `userdirs.StateHomeForBucketDir`) and `refFor` (:381, wraps `bucketref.RefFor`) add two more names.

Fix plan: export the layout in `agent/schema` (which already owns `ValidateSessionID` and the meta load/save): `schema.TranscriptPath(stateDir, id)`, `MetaPath`, `APILogPath`, `SessionDir`, `SessionArtifactSuffixes`. Replace in-package sites first (mechanical, compiler-checked), then cmd sites. Delete `stateHomeFor`, `refFor`, `transcriptPath` after callers move (or keep `transcriptPath` as the schema alias in-package until step 3 of finding 1).
Tests: add one test asserting `RemoveSessionArtifacts` removes exactly the paths the schema list names.
Size: -30 lines, 40 call-site edits. Risk: low.

### agent-misc-root-12 (low) Rune/byte truncation helpers reimplemented about ten times

Locations (all non-test):
- `agent/transcript_render.go:1966` `truncRunes`, `:1976` `oneLine` (comment: "Mirrors doctor.oneLine"), `agent/doctor/transcript.go:466` `oneLine`.
- `agent/transcript_util.go:10` `firstLineClamp`.
- `agent/tool_web_fetch.go:238` `truncateWebFetchContent` (rune cut).
- `agent/project_docs.go:164-176` `truncateDoc` (byte cut on a rune boundary: the loop `for remain > 0 && !utf8.RuneStart(content[remain])` is a copy of `agent/internal/runetrim.Cut`, `runetrim.go:49-58`).
- `agent/session_notes.go:233` `clampNoteRunes`; `agent/internal/tool/breaker.go:591` `TruncateRunes`; `agent/doctor/transcript.go:459` `Truncate`.
- `agent/activitybound/activitybound.go:49` `Truncate` (the allocation-bounded one, with a comment explaining why `[]rune(s)` is wrong for big inputs), `appwire/delegate_roster.go:33` `truncateRosterText` (a copy of it), `cmd/evener-hub/navigation_projection.go:1932` `truncateNavigationRunes`, `cmd/evener-hub/internal/hubcore/tree.go:558` `truncateTitle`, `llm/failuremessage.go:71` `truncateForMessage`, `agent/internal/contextmgr/fork_summarize.go:104` `truncate` (cuts bytes mid-rune and appends "...").
- Whitespace flatteners: `agent/memory_page.go:219` `collapseWhitespace`, `agent/internal/tool/breaker.go:554` `collapseWhitespace`, and nine inline `strings.Join(strings.Fields(...), " ")`.

Evidence: `agent/internal/contextmgr/fork_summarize.go:104-109` slices at byte `limit` (can emit invalid UTF-8) while `activitybound.Truncate` documents that exact hazard. Three different ellipsis spellings ("…", "...", none).

Fix plan: `activitybound.Truncate` (rune-safe, allocation-bounded, ellipsis) and `runetrim.Cut` (byte-bounded) are the two canonical homes; move both into one public leaf package `agent/textbound` (activitybound is already public; `runetrim` is `internal` and so unreachable from root-module users), add `Collapse` and `FirstLine`. Replace the copies; fix `fork_summarize.truncate` first as it is a real invalid-UTF-8 bug (write the failing test with a multi-byte string at the cut).
Size: -120 lines. Risk: low.

### agent-misc-root-13 (medium) Reminder-block wrapping spelled four ways; four near-identical reminder strings

Locations: `agent/system_notification.go:27-65`, `agent/task_reminders.go:175-228`, `agent/session_queue.go:378`, `agent/session_compaction.go:463`, canonical `agent/internal/sysmsg/sysmsg.go` (`ReminderBlock`, `IsReminderBlock`).

Evidence
- Four ways to produce `<SYSTEM-REMINDER>`: `sysmsg.ReminderBlock` (also detects via `IsReminderBlock`), the `systemReminderBlock` pass-through to it, `systemReminderBlockBuilder`/`finishSystemReminderBlock` that re-spell the tags (`system_notification.go:55-65`), and raw string concatenation at `task_reminders.go` (nine lines mention the tag), `session_queue.go:378` and `session_compaction.go:463`. Plus the lower-case `<system-reminder>` helper `systemReminder`.
- `withCurrentStep` (task_reminders.go:157) works by `strings.TrimSuffix(plan, "</SYSTEM-REMINDER>")` and re-appending: it depends on the builder's tag spelling and breaks if `ReminderBlock` ever adds a trailing newline (it does when inner lacks one).
- `taskReminderAllDoneWhileDelegatesRun` and `taskReminderTerminalWhileDelegatesRun` (task_reminders.go:197-247) are a four-way if/else of nearly identical paragraphs ("completed all tasks" vs "No actionable tasks remain" x "delegates running" vs not).
- `hasTaskListAnnouncement` (:116-142) reads and decodes the whole transcript (`readTranscriptFull`, 128 MiB line cap) to find one steering kind, under no cache.
- `formatCurrentTaskSteering` carries a stray "Variant: v09" marker; `taskReminderFullWithPrompts` carries an experiment write-up (Fisher exact, p=0.005) in its doc comment.

Fix plan: delete `systemReminderBlockBuilder`/`finishSystemReminderBlock`; build bodies as strings and wrap once with `sysmsg.ReminderBlock`; compose the completion reminders from `(headline, delegateIDs)` instead of four literals; make `withCurrentStep` take the body before wrapping. Replace the two raw literals in session_queue/session_compaction with the helper. For the transcript scan, use the in-memory steering queue plus the turn log already held (or a bounded tail read) rather than a full parse. Move the experiment note to the spec.
Tests: existing `task_reminders_test.go` (structure assertions only) plus a test that every reminder satisfies `sysmsg.IsReminderBlock`.
Size: -70 lines. Risk: low.

### agent-misc-root-14 (medium) The transcript job-result renderer keeps two hand-maintained key allowlists parallel to a struct

Locations: `agent/transcript_render.go:1355-1600` (`jobResultKnownKeys` 47 keys at :1458, `jobResultMetadataKeys` 35 keys at :1515, `hasNonJobResultKeys`, `jobResultMetadata`), `agent/session_outline.go:49-85` (`jobResult` struct, 19 fields), producers `stableDelegateCreateResult` (`session_tools.go`) and `delegateSendResult` (`session_tools_jobs.go`).

Evidence
Three lists that must agree: the decoded struct, the known-key set, the metadata-key list. The comment at :1500 cites three commit hashes and an issue to explain why a block of keys exists, and says a guard test (`TestJobResultKnownKeysCoversLiveDelegateShapes`) fails "the next time either struct grows a field this allowlist doesn't know". That is a drift detector standing in for a single source of truth. `jobResult` omits fields the allowlist accepts (`child_session_id`, `warnings`, ...) and `jobResultMetadata` re-decodes the same JSON a third time with `UseNumber`.

Fix plan: put the delegate/job result wire shape in one exported struct next to the producers (or in `appwire`, since `appwire.EvenerDelegateInfo` already carries most of it) and have both producers and this renderer use it; derive "known keys" by reflection over its JSON tags (one `sync.OnceValue`), and the metadata list by a `render:"meta"` tag or by "every known scalar not rendered elsewhere". Delete the two literal lists and the history comments. Keep the guard test but make it assert the reflection list equals the struct tags (cheap).
Size: -110 lines. Risk: medium (rendered transcript text is user-visible via `read_transcript`; use golden tests from `transcript_render_job_test.go` as the safety net, red-green only for the new reflection list).

### agent-misc-root-15 (medium) Memory frontmatter editors live in `memory_migrate.go`; frontmatter has two parallel packages

Locations
- `agent/memory_migrate.go:241-380` `memoryYAMLField`, `setMemoryFrontmatterField`, `repairMemoryFrontmatter`, `memoryFrontmatterKeyLines` (used by live `memory_save` at `agent/session_tools_memory.go:200-201` and `agent/memory_write_page.go:27`, not only by migration).
- `agent/internal/frontmatter/frontmatter.go` (86 lines, read side) and `/frontmatter/frontmatter.go` (61 lines, root module copy; its comment: "It matches agent/internal/frontmatter, which this module cannot import").

Evidence
`/frontmatter` is imported by exactly one file in the repo, `internal/bundled/bundled_test.go:9` (`grep -rn evener/frontmatter` over the repo); its 303 lines of tests (`frontmatter_test.go`, fuzz, dos regression) guard a package with no production user. The write-side editors have a 120-line doc-comment-heavy YAML line editor that belongs beside `Split`/`Parse`.

Fix plan: (a) make `agent/internal/frontmatter` public as `agent/frontmatter` (agent module; root already depends on agent) and add `SetField`, `QuoteFreeTextFields`, `YAMLField` there, moving the functions and their tests out of `memory_migrate.go`; (b) change `internal/bundled/bundled_test.go` to import it; (c) delete `/frontmatter`, its testdata, and its entry in `testing-budget.json`. Docs: none beyond the removed budget row.
Size: -61 package, -303 tests, ~0 net for the move. Risk: low.

### agent-misc-root-16 (medium, needs Jesse) Legacy MEMORY.md migration is 671 lines of permanent compatibility code

Location: `agent/memory_migrate.go` (all), invoked per scope load at `agent/session_memory.go:201` with the error dropped (`_ = migrateMemoryScopeCtx(ctx, env)`).

Evidence
Introduced 2026-10-08..15 (`git log --follow`: #4107, #4109, #4113, "generated index, 3/7..5/7"). It parses hand-written indexes (`parseLegacyMemoryIndex`, three regexps, `legacyIndexLink`/`legacyIndexBarePage`), backs them up under `.MEMORY.md.pre-generated`, handles crashed staging files (`.MEMORY.md.migrating-*`, `.MEMORY.md.copying-*`), and explicitly supports alternating with "an older Evener build [that] can write MEMORY.md again after migration" (`memory_migrate.go:34`). Rule 1 says new backward compatibility needs Jesse's approval; this was added anyway and has no stated sunset. Its error is swallowed, so a persistent failure is invisible.

Fix plan (decision first): ask Jesse (a) whether to keep the migration for a bounded window, then delete the file, its tests (`memory_migrate_*_test.go`, ~1,500 lines) and the `legacy` constants; (b) if kept, move it into the `memorypages` package (finding 1) behind one entry point, drop the non-Ctx wrappers (finding 8), and surface its error as a warning instead of discarding it. Update `docs/product/memory.md` to state the supported upgrade window.
Size: -671 production, -1,500 test if deleted. Risk: medium (users with hand-written indexes who skip releases lose the conversion; `friction.md` says prefer automatic recovery, so keep it through a defined release window).

### agent-misc-root-17 (medium) Test seams woven into production code

Locations and evidence
- `testing.Testing()` selects timeouts in production: `agent/git_snapshot.go:46-52` (2s prod / 30s tests, `gitExecTimeout` var), `session_init.go:57-64`, `session_memory.go:912-914`, `session_config.go:302`; imports `testing` into the runtime binary.
- Fault injection through context values: `agent/history_repair.go:14-19` `pendingWatchSendDrainFaultKey` is read on the production path and set only by `session_state_goal_exact_fuzz_test.go:106,133`.
- Package-level stdlib seams: `atif.go:12-16` (`atifMarshalIndent`, `atifMkdirAll`, `atifWriteFile`), `historical_jobs.go:11`, `workspace_info.go:34-37` (`workspaceEntryInfo`, `workspaceReadDir`), `core_tool_names.go:12-16`, `tool_web_fetch.go` (`webFetchRuntime`, a five-function injection struct whose only second user is a fuzz test).
- `cfg.testOnly` (`testConfig`, 91 fields) is read at 122 production sites; `Session.subagentTallyForTest`, `SubagentTallyForTest`, `SimulateCrashMidExecutionForTest`, `ManagedJobsFinalizedForTest`, `AddSessionURLForTest`, `HumanNoteForTest`, `SessionURLsForTest` are exported methods that exist for tests.

Impact: tests that mock `os.WriteFile` verify the mock (the project's "never test mocked behavior" rule); production binaries carry test-only branches; changing a seam means touching production.

Fix plan: replace stdlib seams with real temp-dir tests (the files they guard are plain `os` calls: `atif_test.go` can write to `t.TempDir()` and chmod a directory to force failure); replace `testing.Testing()` timeouts with a field on the struct that tests set via a test helper constructor in a `_test.go` file, or a `Config.GitExecTimeout`; drop the ctx fault key by driving the failure through a real watch-send failure; move `*ForTest` methods to `export_test.go` where the caller is in-package, or to `agent/internal/agenttest` for cross-package callers. Do these opportunistically with each file move in finding 1.
Size: -150 production lines. Risk: medium (concurrency timing tests rely on some seams; convert one seam per PR and keep its tests green).

### agent-misc-root-18 (medium) Tests assert on model-visible and reminder phrasing

Locations: `agent/failure_steering_test.go:21-28,252-340` (`wantDraftSentences`, `wantCapAdvice`, `wantSilentStall` plus a table of full sentences such as "The provider stopped responding mid-stream, 4 times over 2 minutes."), `:362` (`"through report_result and re-issue"`), `:491,494` (`"draft"`, `"fragment"`), `agent/task_reminders_test.go:168` (`"completed all tasks"`).

Evidence: AGENTS.md "Copy Is Not a Test Oracle": no test may check that copy matches a hardcoded phrase. The production comment (`failure_steering.go:39`) says the sentences are "copied verbatim from the spec... product surface", which is the argument for pinning them, so this needs Jesse's call rather than a unilateral rewrite.
Fix plan (after Jesse decides): assert behavior: the steering shape selection (`shapeStall/Cap/InBand`), that the draft sentence appears if and only if `salvagedBytes >= substantialSalvageBytes`, that the result tool name given is the one named (assert the tool-name argument appears, not the sentence), and that exactly one shape template fires. Keep one golden file under `testdata/` for the product text if the spec truly pins it (golden files are reviewed as artifacts, not asserted inline).
Size: about -120 test lines. Risk: low.

### agent-misc-root-19 (low) Delegate sandbox policy engine lives in the session file set and has two error-classification mechanisms

Locations: `agent/sandbox_delegate.go` (772 lines; 16 `"invalid_request: ..."` literals), `:22-44` (`delegateSandboxRequestError` with a typed `Class`), `:592`.

Evidence: `newDelegateSandboxRequestError(fmt.Errorf("invalid_request: %w", err), "sandbox")` carries the class twice, once typed and once as a string prefix. Across the package 149 call sites build errors by prefixing `invalid_request:`/`errors.New("invalid_request: ...")` (`session_tools_transcript.go` 41, `session_tools_jobs.go` 33, `job_watch.go` 29, sandbox_delegate.go 16) and nothing parses the prefix, it is just model-facing copy. The pure functions (`resolveDelegateSandboxRequest`, `buildDelegateSandboxPolicy`, `allowedDelegateModes`, `applyWriteBlockedFloor`, `delegateSandboxModeAllowed`, snapshot conversions at :691-772) take no Session. File naming is inverted relative to its `delegate_*` siblings (`sandbox_delegate.go` vs `delegate_sandbox*.go` convention).

Fix plan: one `invalidRequestError(format, args...)` helper in the tools layer that returns the typed error and formats the prefix once; migrate sandbox_delegate.go first, the other 130 sites opportunistically. Move the Session-free policy functions to `agent/sandbox` (already the policy owner) as `sandbox.ResolveDelegateRequest`. Tests: `sandbox_delegate_*_test.go` already table-test the pure functions.
Size: -80 lines at first step. Risk: low.

### agent-misc-root-20 (low) `git_snapshot.go` has a six-value return and a drifted second implementation of the same probe

Locations: `agent/git_snapshot.go:84-110` (`snapshotGitBranch`, passes shell strings `"git rev-parse --is-inside-work-tree"` to `env.ExecCommand`), `:117-184` (`snapshotGit`, uses `execenv.RunGit` with argv), and hub duplicates `cmd/evener-hub/app_git_head.go:167,185`, `cmdutil/cmdutil.go:23`.

Evidence: two ways to spawn git in one file (shell-string vs argv `RunGit`); `snapshotGit` returns `(inRepo bool, branch string, modifiedFiles int, untrackedFiles int, recentCommitTitles []string, statusKnown bool)`; the `%x20` comment (:160-165) explains a choice made "to avoid fixture churn"; two issue-history comments (:146-150) narrate review rounds.
Fix plan: return a `gitSnapshot` struct; make `snapshotGitBranch` call `RunGit` with the same helper closure; delete history comments; leave hub copies alone (different executor) but note them. Size: -30 lines. Risk: low.

### agent-misc-root-21 (low) `tree_counter.go` mixes unrelated types, has an orphaned doc comment and inconsistent nil handling

Locations: `agent/tree_counter.go:12-50` (`jobActivityClock`, unrelated to turn slots), `:79-104` (the `treeCounter` doc paragraph sits above `slotKind`, so `go doc` attaches it to the wrong type), `:176-185` (`treeCapacityErrorFor` dereferences `s.treeCounter.occupancy()` with no nil check while `reserveTreeSlot` at :155 treats a nil counter as unbounded), `:208` ("the legacy in-process spawn").
Fix plan: move `jobActivityClock` next to `noteJobTreeShapeChange` (`session.go:73`); put the doc comment on `treeCounter`; nil-guard `treeCapacityErrorFor`. Size: ~0. Risk: none.

### agent-misc-root-22 (low) Stale package doc and re-exported clock aliases

Locations: `agent/doc.go:1-46`; `agent/retirement.go:14-24,684-686`.
Evidence: doc.go links `[Profile]`, `[NewOpenAIProfile]`, `[SessionEvent]`, `[EventKind]` and its example calls `agent.NewOpenAIProfile("gpt-5.2")`; those live in `agent/provider` and `agent/events` (`grep 'func NewOpenAIProfile'` finds only `provider/profile.go:51`), so the example does not compile and the doc links dangle. `RetirementTimer`/`RetirementTicker` aliases of `clock.Timer`/`clock.Ticker` are referenced only by test fakes in `server/` and `cmd/evener/`; production uses `RetirementClock` and `RealRetirementClock()` once each (`cmd/evener/serve.go:245,928`).
Fix plan: fix the example and links (or delete the example); move `agent/internal/clock` to public `agent/clock` and delete the four `Retirement*` clock names (callers use `clock.Clock`, `clock.Real()`).
Size: -20 lines. Risk: low.

### agent-misc-root-23 (low) Per-tool call summary switch is a third parallel tool-name table

Locations: `agent/transcript_render.go:1809-1900` (`toolInputSummary`, 12 hard-coded tool names), `agent/internal/contextmgr/context_manager.go:1084-1130` (result summarizer, same tool names), `agent/internal/tool/registry.go:1065`, `cmd/evener-hub/output_images.go:53`.
Evidence: each switch hard-codes argument key names (`file_path`, `pattern`, `query`, the retired `task` key for `delegate`); renaming a tool argument requires finding all of them, and nothing in the tool definition owns "how to summarize me".
Fix plan: add an optional `Summarize func(args map[string]any) string` to the tool registration (`internal/tool.RegisteredTool`), implemented next to each tool's schema; renderer calls `reg.Summary(name, args)` with `unknownToolSummary` as the fallback. Only worth doing when a fourth consumer appears, so treat as backlog.
Size: neutral. Risk: low.

### agent-misc-root-24 (low) `tool_web_fetch.go` unit and field drift

Locations: `agent/tool_web_fetch.go:20-24,138,160-165,238-247`.
Evidence: `webFetchMaxContent = 100_000 // chars passed to cheap model` is applied with `runetrim.Cut` (bytes, :138) while `webFetchRawFallbackMaxContent = 20_000 // chars` goes through `truncateWebFetchContent` (runes); the result map carries both `"url"` and `"source_url"` with the same value (:164-165); `webFetchCacheKey` and `htmlToMarkdown` are one-line wrappers; `http.DefaultClient` is used with a 30s context but no redirect or private-address policy (flag for a security pass, no change proposed here).
Fix plan: pick bytes or runes for both limits and say so in the constant name (`...MaxBytes`); drop `source_url` only if no consumer reads it (grep the tool-result schema and frontend first); inline the wrappers. Size: -15 lines. Risk: low.

### agent-misc-root-25 (low) Comment volume and history narration

Evidence: 4,305 of 17,162 slice lines (25%) are comments; `SessionInfraRoots` has a 62-line doc for a 30-line function (`sandbox_infra.go:14-75`); history and review-round references in code comments: 35 `#NNNN` refs, `git_snapshot.go:148-150` ("#4568 round 2", "#4621 round 15"), `kata ...` ids (`session.go:507,509,882,1162,1172,2449,2629`, `diagnostics.go:118`, `transcript_render.go:1025`), dated rulings (`prompt_data.go:98`, `steering_tool_gate.go:8` "ruled 2026-08-06"), commit-hash provenance (`transcript_render.go:~1500`), an experiment write-up (`task_reminders.go:60-62`). The user rules say history belongs in commit messages.
Fix plan: delete history/provenance sentences when touching a file; keep why-comments. No bulk rewrite. Size: ~-600 comment lines over time. Risk: none.

### agent-misc-root-26 (low) Range-fallback logic is wrapped three layers deep and repeated

Locations: `agent/transcript_render.go:282-306` (`parseRange` over `parseRangeErr` over `turnwindow.Parse`), `agent/apilog_read.go:756-775` (`selectAPILogRange` re-implements "invalid -> default + warning + cap" with its own default constant), `agent/session_tools_transcript.go:1183-1186,1571-1576`.
Evidence: three call sites each construct the default spec string and call the strict parser a second time with it; `parseRangeErr` adds only the empty-spec default to `turnwindow.Parse`.
Fix plan: give `turnwindow` a `ParseOrDefault(spec, defaultSpec, count, maxSpan) (start, end, normalized, warning)` and delete `parseRange`/`parseRangeErr`/`selectAPILogRange`. Tests: `transcript_range_fuzz_test.go` and `apilog_read_covtest_test.go` invariants move with it. Size: -40 lines. Risk: low.

### agent-misc-root-27 (medium) Test layout: 143 coverage-chasing files and 114 fuzz files inside the package under test

Evidence: `ls agent/*_test.go`: 957 files, 374,987 lines; 143 files with `cov` in the name (32,025 lines, e.g. `cov_s1_*`, `cov_s2_*`, `*_covtest_test.go`) and 114 with `fuzz` (34,725 lines, e.g. `*_exact_fuzz_test.go`, `*_seed100_fuzz_test.go`, `agent_misc_program_fuzz_test.go`). Names encode the pass that produced them ("s1", "w3init", "seed100"), not the subject; several mix helpers prefixed `s1cov_` that other files reuse. `session_tools_aux_exact_fuzz_test.go:60` constructs `webFetchRuntime` just to feed a fuzzer.
Impact: one test binary for 480k lines; hard to find the test for a function; coverage-driven tests of unexported wrappers pin dead code (findings 7, 8).
Fix plan: when a subject moves (finding 1) rename its tests to the subject (`transcriptview/render_test.go`); fold `cov_*` cases into the subject's table tests and delete cases that only assert a getter or a wrapper; keep real fuzz targets, merge program fuzzers per subject. Measure binary compile time before/after in CI (not locally, per repo rule).
Size: large, mechanical, incremental. Risk: low if each rename is its own commit with `go test -run` for that file in CI.

### agent-misc-root-28 (low) Small special cases and a heavyweight helper in core

Locations: `agent/coordinator_workflow_plugin.go:5-12` (core hardcodes the plugin name `"coordinator-workflow"` to pick catalog keys), `agent/core_tool_names.go:25-48` (`CoreToolNames` stands up a whole `Session` over a temp dir with an OpenAI profile just to list tool names for `cmd/evener-fuzz-harvest`), `agent/prompt_template.go:34-46` (`promptSource` and `collapseBlankLines` in an otherwise empty file).
Fix plan: ask whether the coordinator special case can become plugin manifest data (`catalog_key: bare`) instead of a name check in core; replace `CoreToolNames` with a registry-only builder (`registerCoreTools` into a bare `tool.Registry`) if the harvester can live without Session wiring, otherwise leave it and move it under `cmd/evener-fuzz-harvest`; merge `prompt_template.go` into `prompt_data.go`. Size: -40 lines. Risk: low.

---

## Recommended fix sequence for this slice

Order is chosen so each step shrinks the next one's work and every step is behavior-preserving and test-guarded.

1. Deletions with no design questions (findings 6, 7, 8, 21, 22): -700 lines, unblocks clean moves. One PR each for eval harness, historical-jobs chain, test-only wrappers.
2. Cross-cutting canonicalization that touches many files but few semantics (findings 12, 4, 5, 11): `textbound`, `userdirs` state root (get Jesse's call on the HOME-less fallback first), `fsdurability`, `schema` path layout.
3. The transcript reader loop (finding 3) and the fork/aside collapse (finding 10); includes the fork 10 MiB cap fix.
4. Delegate status record collapse (finding 2): highest drift risk per line, independent of the rest; do it before the daemon/hub next adds a field.
5. Reminder/steering cleanup (13), live model resolution collapse (9), job-result renderer single source (14).
6. Ask Jesse (16 memory migration sunset, 18 copy-asserting tests, 28 coordinator special case) before touching them.
7. Package extraction (finding 1), in leaf-first order: `hostresources`, `memorypages` (after 15/16), `envsnapshot`, then `transcriptview` and `artifactread` (after 3, 11, 14, 26 have shrunk their interfaces). Move tests with subjects and fold the `cov_*` files as you go (finding 27). Update `docs/product/subsystems.md` and linked paths in the same PR as each move.
8. Test-seam removal (17) and comment cleanup (25) ride along with each file move rather than as their own PRs.
9. Leave `Session` itself, `retirement.go`, `status.go` (post-2) and `sandbox_delegate.go` (19, low priority) in place until the leaf extractions have landed; re-measure `Session` field and method counts after, and open a separate design discussion for splitting job-notification retry state out of `Session`.
