# x-compat-legacy: backward-compat shims, legacy paths, migrations, fallbacks

Cross-cutting audit of the whole repo (Go and TS). Read-only; nothing in the tree was changed except this file.

## Overview and inventory

The repo's first commit is 2026-02-08, so the whole project is about eight months old. The hub itself (host registry, index.db, hub.toml, plugins store, transcript read model, native app) dates from late August to October 2026. Almost every "legacy" thing below is therefore 1 to 8 weeks old. There is no stated version-skew or compatibility policy anywhere in `docs/product` (grep for backward/compat/migrat/legacy in `README.md` and `principles.md` returns nothing), so each author invented a compat path locally.

Raw term counts, non-test source files only (Go, TS, TSX, MTS, sh; excludes docs, node_modules, testdata):

| term | all hits | non-test hits |
|---|---|---|
| legacy | 3055 | 889 |
| fallback | 4760 | 1488 |
| migrat* | 1581 | 503 |
| compat | 1434 | 383 |
| v1 | 1758 | 307 |
| backward | 275 | 140 |
| deprecated | 36 | 19 |
| old (format/shape/client/version) | 47 | 18 |

(Most `fallback`/`compat` hits are legitimate provider quirk handling in `llm/` and UI default-value logic; the findings below are the hand-checked subset that is real accretion.)

Top "legacy" files by non-test hit count: `agent/memory_migrate.go` 85, `cmd/evener-hub/app_host_manage.go` 57, `hubcore/index_schema.go` 33, `frontend/src/stores/prefs.ts` 30, `appwire-client/typescript/keybindingDefaults.ts` 24 (checked, these are key bindings named "legacy"; not compat), `internal/transcriptindex/build.go` 23, `cmd/evener-migrate/main.go` 19, `appwire-client/typescript/transcriptDisplayConfig.ts` 19, `agent/delegate_legacy_state.go` 19.

Dedicated migration/compat code, non-test lines: `cmd/evener-migrate` 801 (2760 with tests), `cmdutil/legacymigration.go` 131, `agent/memory_migrate.go` 671, `internal/plugins/marketplace_migration.go` 1282 + `metadata_migration.go` 343 + `store_format.go` 94, `hubcore/index_schema.go` 255, hub host sidecar migration ~150 inside the 3854-line `app_host_manage.go`, `cmd/evener-transcript-v2-upgrade/main.go` 408, `agent/delegate_legacy_state.go` 78.

TODO/FIXME/XXX inventory: effectively empty. A comment-anchored grep (`// TODO`, `# FIXME`, etc.) over Go/TS/sh/Makefile/yml finds exactly one real marker: `scenariosourcecite_audit_test.go:423` (`TODO: someday resurrect this:` inside a test fixture string, git blame 2026-08-19, not actionable). The other TODO hits are test data (`context.TODO()`, `"pattern":"TODO"`). The debt here is not parked under TODO; it hides under the words "legacy", "retained", "compatibility", and "older hub". `//nolint:unused // retained for ...` is the closest thing to a TODO marker (finding 11).

Where debt is real versus fine:
- Fine: `message_search.go:218 migrateMessageSearch` drops and rebuilds a derived index on version mismatch (the right pattern for a cache). Provider/OpenAI-compat quirk code in `llm/`. `llm/registry` `ErrOldSchema` refusal.
- Not fine: the items below.

All fixes below need Jesse's explicit approval before removal because they delete compat behavior (global rule: backward compatibility only with approval). Most of these were never approved; the "who owns removal" answer for nearly all of them is "nobody", since none carries a removal condition.

---

## x-compat-legacy-1: No version-skew contract; 46 ad hoc "older hub" tolerance sites

- Severity: high (root cause of most accretion)
- Locations:
  - `mobile-native/src/board/hubSeen.ts:37,148` (`withoutSeenSet` WeakSet when `evener/session/seen/set` is method-not-found)
  - `mobile-native/src/board/boardData.ts:425-439` (notices/list absent)
  - `appwire-client/typescript/state/extensions/plugins.ts:172-175`
  - `appwire-client/typescript/hubUpdate.ts:192` ("An older hub omits installAvailable")
  - `appwire-client/typescript/errors.ts:34-39` (`isMethodNotFound`)
  - `appwire/types.go:35` (`ProtocolVersion = "evener-appwire-v7"`)
  - `server/appwire_runtime.go:832` (`legacyTaskPublication`), `server/thread_envelope.go:290-292`
  - `appwire-client/typescript/types.gen.ts` has ~40 generated doc comments of the form "absent from an older hub/daemon/build"
- Evidence: counting `older hub|an older (build|daemon)|predates S5` in non-test, non-generated TS gives mobile-native 17, frontend 14, appwire-client 15 (46 total). `docs/product/README.md` and `principles.md` contain no skew statement. `ProtocolVersion` is bumped to v7 and `server/appwire_runtime.go` still accepts zero-metadata "old producer" task publications. `notices/list` was added 2026-09-27; the mobile client has carried a method-not-found fallback for it since. A real skew exists (remote SSH hosts can run an older hub binary, sshconn already has an "incompatible protocol" upgrade path, commit 2026-10-23 #4849), but nothing says which skew is promised.
- Impact: every new RPC grows a defensive fallback; fallbacks are never deleted because nobody knows the supported window. Clients render different states for the same hub depending on which fallback they hit.
- Fix plan: (1) Write one short evergreen section in `docs/product/README.md` or `subsystems.md`: the supported skew window (for example "a controller upgrades hosts it connects to via sshconn; clients support hub N and N-1 only; additive optional fields never need a fallback"). Needs Jesse's decision on the window. (2) Add a comment-level convention: a fallback names the release it can be deleted after. (3) Sweep sites older than the window (start with hubSeen, boardData, plugins.ts, `legacyTaskPublication`). (4) Where the hub auto-upgrades hosts, rely on `ProtocolVersion` mismatch handling instead of per-method fallbacks.
- Size: ~30 lines of docs; sweep removes roughly 150 to 300 lines of TS/Go plus tests.
- Risk: medium. Removing a fallback breaks a client talking to a host outside the window. Decide the window first.

## x-compat-legacy-2: Deprecated combined `evener/update/apply` path, 3 days after the install/restart split

- Severity: medium
- Locations: `appwire-client/typescript/hubUpdate.ts:9,27,42,60-62,90,309-321` (`@deprecated` state `applying`/`applyError`, `apply()`); `cmd/evener-hub/frontend/src/panes/settings/sections/hubUpdates.tsx:98-139,228,235` (`legacyApply` branch, separate confirm copy); `mobile-native/src/hub/HubHome.tsx:36-43,69,73,202-205` (`legacyUpdate`, `updates.apply()`); `cmd/evener-hub/frontend/src/stores/hubUpdate.ts:107`; Go `cmd/evener-hub/app_update.go:260-300` (`hubUpdateApply`), `appwire/types.go:146` (`MethodEvenerUpdateApply`).
- Evidence: `git log -S MethodEvenerHubRestart` first appears 2026-10-22 (#4830 "split binary install from hub restart"). The TS comment says apply is "Kept for older hubs and out-of-tree hosts that still call the combined RPC." The web and the phone each re-derive "is this an older hub" with the same cast `(check as { restartSupported?: boolean }).restartSupported === undefined` (hubUpdates.tsx:104, HubHome.tsx:38) instead of the shared client exposing one flag. The Go handler duplicates the stale-check/lock logic of install.
- Impact: two update flows in three UIs, parallel deprecated state fields, double copy, and an RPC that has no non-hub caller in the repo.
- Fix plan: with Jesse's approval, delete `apply()`, `applying`, `applyError`, `legacyApply`, `legacyUpdate`, the web confirm variant, `hubUpdateApply` and its method constant, and regenerate types. If an older-hub window is wanted, move the single "older hub" derivation into `hubUpdate.ts` (one `legacyApply` boolean on the controller state) so web and phone do not each cast.
- Size: net -250 lines (TS ~150, Go ~100) plus test deletions.
- Risk: low to medium; breaks update-from-UI for hosts running a build older than 2026-10-22 (they would need `evener upgrade` by CLI).

## x-compat-legacy-3: Transcript display "dual-write" to legacy prefs keys feeds only test harnesses

- Severity: medium
- Locations: `cmd/evener-hub/frontend/src/stores/prefs.ts:160-166,201-265,336-348,359-380,511-540,591-609`; `cmd/evener-hub/frontend/src/stores/transcriptDisplay/localStore.ts:9-60` (`LEGACY_KEYS`, `verifyLegacyWrite`); `cmd/evener-hub/frontend/src/stores/transcriptDisplay.ts:533-547,667`; `appwire-client/typescript/transcriptDisplayConfig.ts:546-624` (`LEGACY_PREF_KEYS`, `legacyConfigFromValues`, `legacyWritesFromConfig`, `dualWriteLegacyPreferences`).
- Evidence:
  - `prefs.ts:237-240,258,265` export six alias consts (`readLegacyPref`, `hasLegacyPref`, `writeLegacyBool`, `removeLegacyPref`, `migrateLegacyPrefs`, `dualWriteLegacyPrefs`); grep shows each is referenced exactly once, at its own definition. Dead.
  - The comment at `prefs.ts:260` says the dual-write exists to "Keep old transcript consumers in sync". Searching non-test source for readers of the old state: `prefs.transcript` / `setTranscriptStatus` / `showCost` / `setShowCost` have no production consumer outside `prefs.ts`; the real renderers (`TurnSeparator.tsx:52-54`, `transcriptVisibility.ts`) read `metadata.roundTimings` etc. from the display config. The only readers of `prefsStore.transcript` are test harnesses (`TurnSeparator.test.tsx:15-45` builds a `SeparatorHarness` that reads `usePrefsStore(s => s.transcript.roundTimings)` and feeds it into `makeTranscriptDisplayConfig`; `SystemNoticeItem.test.tsx:31,504`).
  - Every config change therefore writes six localStorage keys plus a read-back verify (`transcriptDisplay.ts:533-535`, `545-547`) and can surface a `storageWarning` because a legacy-key write failed.
  - The one-time import `migrateLegacyTranscriptDisplay` (prefs.ts:246) is the only legitimate compat piece (users who set old prefs).
- Impact: write amplification, a spurious failure mode (`reportStorageResult(..., legacyOK)`), ~120 lines of prod code and a store slice that only tests use; tests assert against state the product does not read.
- Fix plan: (1) delete the six dead aliases (no behavior change). (2) Delete `dualWrite*`, `verifyLegacyWrite`, `legacyWritesFromConfig`, `LEGACY_PREF_KEYS` writes and the `legacyOK` param of `reportStorageResult`. (3) Delete `prefs.transcript`, `showCost`, `setTranscriptStatus`, `setShowCost`, `TRANSCRIPT_KEY_NAMES`, and rewrite the harness tests to build the config directly (`makeTranscriptDisplayConfig`) as production does. (4) Keep `migrateLegacyTranscriptDisplay` only if Jesse wants to carry users' old settings; otherwise drop it too and delete `legacyConfigFromValues`. Do the work test-first: first change the harnesses to drive the real config, confirm green, then delete.
- Size: net -300 to -450 lines across prefs.ts, localStore.ts, transcriptDisplay.ts, transcriptDisplayConfig.ts and tests.
- Risk: low for steps 1 to 3 (no reader exists). Step 4 changes whether upgrade preserves old toggles.

## x-compat-legacy-4: `sshconn` legacy probe helpers kept alive only by tests

- Severity: medium
- Locations: `cmd/evener-hub/internal/sshconn/version.go:53-73` (`explicitHostAddr` comment, `hostAddrFor`, `Manager.hostAddr`), `:102-109` (`hubPort`, defaults to "9180"), `:1036-1041` (`waitHealthy`), `:1068-1112` (`waitForHealthyHub`), `:978` (`hubHealthRemote`).
- Evidence: comments say "legacy probe helpers, retained for their direct tests" (line 62) and "backs the legacy health-wait tests ... Managed restart uses selected successor proof instead" (line 1068). `waitHealthy` has no call in non-test code (grep: only `version_test.go:869,893,1697,3198,3208`); it is the only caller of `waitForHealthyHub`, which is the only prod caller of `hostAddr`/`hostAddrFor`/`hubPort`. `explicitHostAddr` is used by `runner.go:359` and `selected_restart.go:452`, so it stays. `version_test.go` references the wait helpers 11 times.
- Impact: ~110 lines of production code (plus `hubAnswerProvesReplacement` and its callers only reachable from there, to be confirmed at deletion) that tests exercise but the product never runs; a second, weaker restart-verification algorithm that can drift from the real one.
- Fix plan: delete `waitHealthy`, `waitForHealthyHub`, `hostAddr`, `hostAddrFor`, `hubPort`, `defaultHubAddr` (if unused after), and the 11 test references; keep or fold any assertions that test shared pieces (`hubAnswerProvesReplacement`, `hubBuildSHAMatches`) into the selected-restart tests. Run `go vet ./cmd/evener-hub/internal/sshconn/` and the package's `deadcode`-style lint to catch the newly unused helpers.
- Size: net -250 lines including tests.
- Risk: low; verify the selected-restart tests already cover replacement proof so coverage does not drop.

## x-compat-legacy-5: Pre-union host mutation adapters (`Add`, `Update`, `Remove`, `committedHostRow`) used only by tests

- Severity: low to medium
- Locations: `cmd/evener-hub/app_host_mutations.go:40-96` (`committedRow`, `committedHostRow`), `:1169-1196` (`Update`, `Remove`), `cmd/evener-hub/app_host_manage.go:2430-2436` (`Add`); `appwire/types.go:4995-5001,5098-5104` (`HostUpdateResponse`, `HostRemoveResponse`).
- Evidence: comments call them "the pre-union adapter ... for callers that predate the union" and "the three compatibility wrappers". Wire handlers call `AddResult/UpdateResult/RemoveResult` directly (comment at app_host_manage.go:2430 says so). Non-test grep: `HostUpdateResponse` and `HostRemoveResponse` are used only inside those two wrapper functions and their own type declarations. `committedRow` has non-wrapper callers only in tests (`app_host_e2e_test.go:289`, `app_host_manage_wiring_test.go:1193`). Test callers: `app_host_manage_update_test.go:380,390`, `app_host_manage_test.go:710,725,730`.
- Impact: three public-looking methods and two wire response types for an API shape no client uses; tests exercise the wrapper's error mapping rather than the real handler results.
- Fix plan: move `committedRow` into a `_test.go` helper (tests that want a committed row use it), delete `Add/Update/Remove`, `committedHostRow`, `HostUpdateResponse`, `HostRemoveResponse`; migrate the five test call sites to `*Result`. Regenerate TS types if the response types are in the generated set (check `types.gen.ts`).
- Size: net -120 lines.
- Risk: low.

## x-compat-legacy-6: Hub state migrations for 1 to 4 week old formats, with test seams in production code

- Severity: medium
- Locations:
  - Host sidecar: `cmd/evener-hub/app_host_manage.go:45-65` (`hub.hosts.json`, `.migrated` suffix, `legacy_sidecar_migrated` key), `:617-760` (parse), `:1622-1630` (boot call), `:1833-1966` (`migrateLegacyHostSidecar`), `:2510-2534` (writes poisoned while a sidecar is unmigrated).
  - Stash name: `app_host_manage.go:2845-2851` ("The legacy spelling an earlier build of this branch wrote; a record naming it keeps its restore source").
  - Compensation phase alias: `cmd/evener-hub/internal/hostops/intent.go:165-181` (`compensating-sidecar` normalized to `compensating-hubtoml`, `valid()` and `next()` both normalize).
  - index.db: `cmd/evener-hub/internal/hubcore/index_schema.go:15-31` (`indexMigrationMu`, `indexMigrationInterleave` "deterministic test seam ... nil in production"), `:50-100`, `:103-255` (`rebuildLegacyIndexTables`, `rebuildSessionPinTable` with host-qualified-ref parsing, `newerLegacyPin`).
  - Launch config: `cmd/evener-hub/internal/launchconfig/resolver.go:127-142`, `paths.go:19,26,49` (`LegacyProject` fallback to `<state>/projects/<id>/launch.toml` with a user-visible diagnostic).
- Evidence (git dates): sidecar migration introduced 2026-09-26 (#2494); the "earlier build of this branch" stash spelling was introduced 2026-09-28 (`cb852bffc34`), i.e. it supports a name that only ever existed on an unmerged branch for two days; `compensating-sidecar` literal appears first 2026-09-26 and is renamed within days, and compensation records live only while an in-flight host mutation is recovering; index_schema rebuild introduced 2026-09-16 (user_version 1); launchconfig `LegacyProject` came from the 2026-08-18 serf rename. `app_host_manage.go` is 3854 lines, with ~144 of them parsing a sidecar format that existed from 2026-09-20 to 2026-09-26. Related tests: sidecar test coverage lives in 2 test files; `intent_test.go` has 2 legacy-alias cases.
- Impact: durable, crash-safety-engineered code (`legacy_sidecar_migrated` marker, rename-aside, fsync ordering, poison-on-failure) for a window of a few days of installs. It also blocks hub.toml writes (`poison`) when a stale sidecar fails to parse, which is the fail-closed shape the product guide warns against.
- Fix plan: (a) Delete the `compensating-sidecar` alias and the `.stash` legacy spelling now (they never shipped in a tag; confirm with `git tag --contains cb852bffc34` and the sidecar commit). (b) Decide the sidecar migration end-of-life: if the earliest tag containing hub.toml-in-place (#2494) is older than the supported window, delete `migrateLegacyHostSidecar` and its helpers plus the poison path. (c) For index.db, keep rebuild (it preserves user favorites/pins) but delete `indexMigrationInterleave` by testing the race with two real connections; or accept losing pre-source pin rows and replace with drop-and-rebuild like `message_search.go`. (d) `LegacyProject` launch layer: delete after confirming no user has the file (it moved with #1050, 2026-09-30).
- Size: (a) -25 lines; (b) -300 lines prod, -400 tests; (c) -60; (d) -40.
- Risk: (a) negligible; (b) users with an unmigrated `hub.hosts.json` lose their host list (a clear one-line startup message could instead point at the file, no code to migrate); (c) low; (d) low.

## x-compat-legacy-7: Serf-to-Evener rename migration: 3 tools, a launch-blocking refusal, and install-time check

- Severity: medium
- Locations: `cmd/evener-migrate/main.go:1-399`, `convert.go:1-259` (pre-registry providers.toml converter), `rewrite.go:143`; `cmdutil/legacymigration.go:1-131`, `cmdutil/userdirs.go:34`; `install.sh:176-178` and byte-identical `internal/remoteinstall/install.sh:176-178`; `appwirelegacypath_audit_test.go:1-102`.
- Evidence: `evener-migrate` main.go header documents three source generations (legacy ~/.serf, interim ~/.evener, final XDG). The rename commit is 2026-08-18 (file created 2026-08-18, last touched 2026-08-31). `checkLegacyDataDirs` (legacymigration.go:68-120) runs from `EnsureUserConfigDirs` at the start of every `evener` and `evener-hub` launch and returns `ErrLegacySerfData` ("run evener-migrate before continuing, or create X yourself"), refusing the launch. Test volume: `cmd/evener-migrate/*_test.go` plus `legacymigration*_test.go` together ~3160 lines against ~930 lines of code. The converter in `convert.go` re-declares a deleted parser (`oldInstance`, `oldTypeTarget`, `vendorKeyEnv`) so that a providers.toml schema from before the registry cut-over (September) converts.
- Impact: AGENTS.md says to prefer automatic recovery over refusal and to explain which guarantee a restriction serves. Here a launch is refused whenever an old directory exists, requiring a manual command. It also keeps `serf` strings in 18 production lines. The install scripts carry a duplicated check (two identical 3-line blocks).
- Fix plan: Discuss with Jesse whether anyone besides Jesse's machines had `.serf` data. If not: delete `cmd/evener-migrate`, `cmdutil/legacymigration.go`, the `checkLegacyDataDirs` call, the two `install.sh` checks, `evener migrate` wiring in `cmd/evener`, fuzz target `scripts/fuzz/fuzz-targets.txt:36`, and the UPGRADING.md migrate mentions. If the migration is still wanted, make it automatic (move on first launch, no refusal) so the refusal path and the opt-in tool go away; at minimum fold `homeRootFiles` into one list (currently shared by two packages by convention only, per the comment at legacymigration.go:12-17).
- Size: delete ~930 prod + ~3160 test lines, or convert to auto-migrate (-100 net).
- Risk: medium; data stranding for any unmigrated user. Needs Jesse's call.

## x-compat-legacy-8: Legacy delegate state is hard-rejected, with the check implemented three times

- Severity: medium
- Locations: `agent/delegate_legacy_state.go:11-78` (called once at `agent/delegate_runtime.go:3236` in `bootstrapDelegateResources`); `agent/doctor/jobs.go:118,276-308` (`legacyDelegateFailures`); `agent/session_tools_jobs.go:711-713,1202-1204` (`legacy_delegate_activation` refusals); `UPGRADING.md:5-47`; tests `agent/delegate_legacy_dormancy_test.go` (126 lines), `agent/upgrade_runbook_test.go` (218 lines).
- Evidence: `rejectLegacyDelegateState` scans the whole `jobs.jsonl` on session bootstrap and returns "use a fresh state root" if any `delegate` job type or `delegate_created`-style event exists. `legacyDelegateFailures` in doctor reimplements the same "find legacy delegate job ids, then watches addressed to them" walk with different traversal (`jobstore.Fold` vs. raw events) and the same two error codes. The introduction was in 2026-08-13/15 (stable delegate authority), so this protects against a state format that was current for two weeks.
- Impact: a session whose journal contains old delegate events cannot be resumed at all, and the user is told to copy their work into a fresh session by hand (UPGRADING.md lines 20-47 contain a 15-line bash recipe). That is a lock-down rather than recovery, and the two detectors can disagree.
- Fix plan: Ask Jesse if any retained state predates 2026-08-15. If not, delete `delegate_legacy_state.go`, the doctor check, the two tool refusals (a `rec.Type == delegate` job simply no longer exists), the two test files, and the UPGRADING.md section. If those sessions must stay readable, change the policy from refusal to "history readable, delegates marked retired" and keep one detector in `jobstore` consumed by both session and doctor.
- Size: -78 prod, -45 doctor, -10 tools, -344 tests, -45 docs.
- Risk: low if no old state exists; otherwise a product decision.

## x-compat-legacy-9: The `purpose` to `intent` fallback is implemented three times

- Severity: low to medium
- Locations: `agent/transcript_render.go:1746-1767` (`toolIntent`), `agent/doctor/transcript.go:324-349` (`toolIntentFromArguments`), `internal/apptranscript/apptranscript.go:217-242`; `UPGRADING.md:79-85`.
- Evidence: all three read `intent` first and fall back to `"purpose"`, citing the same rename (2026-08-29, `7512a736e`, issue #709). The doctor copy's comment says "this mirrors -- rather than shares -- the same reader-side rule applied independently in each of those packages". Behavior already diverges slightly: doctor trims whitespace and accepts only strings; `toolIntent` uses `scalarString`. The window it protects is transcripts recorded before 2026-08-29 (8 weeks).
- Impact: three copies of a compat rule, with drift already.
- Fix plan: with approval, drop the fallback in all three (transcripts from before 2026-08-29 lose the intent line, nothing else). If the fallback stays, put one `schema.ToolIntent(args)` helper in `agent/schema` (all three packages import it per the doctor comment) and call it from all three. Update UPGRADING.md.
- Size: -45 lines (delete) or -30 (consolidate).
- Risk: low.

## x-compat-legacy-10: "Retained for source compatibility" shims with no production caller

- Severity: low to medium
- Locations and evidence (each verified by grepping callers):
  - `agent/execenv/gitpath.go:240-277`: `mainRootFromGitdirPointer`, `parseGitdirPointer`, `mainRootCandidateFromCommonDir` are one-line wrappers labelled "Compatibility wrapper around the shared identifier helper"; non-test callers 0 for all three; test callers 1, 1 and 5.
  - `cmdutil/statedir.go:83-90` `ResolveStateKeyDir` "retained for source compatibility"; only a test (`statedir_test.go:241`) and a comment in `cmd/evener-hub/spawn.go:870` reference it.
  - `agent/task/task_store.go:551-557` `TaskStore.Progress()` "for compatibility with its original public contract"; no non-test caller (the repo is one module family; there is no external consumer).
  - `agent/runtime_dir.go:70-71` `hexHash` is a pass-through to `nonProjectHash`, used only by tests (`runtime_dir_test.go:14,23,32`, `transcript_lookup_test.go:36`).
  - `llm/adapter_timeout.go:121-135` `ApplyAdapterTimeout(ctx, at, _ bool)` ignores its third parameter "retained for source compatibility"; 11 call sites pass `true/false` that do nothing (`llm/client.go:558`, `protocolhttp/stream.go:24`, `exchange.go:37`, plus tests).
  - `agent/session_lifecycle.go:126-129` `Session.close` "retained for internal callers and tests".
  - `agent/session_worktree_close.go:307-311` two package-level `Observe*` vars where the second exists because the first "remains unchanged for compatibility".
  - `agent/session_config.go:186` and `agent/session_init.go:267-272`: ContextStrategy `"recall"` alias for `compact` "so existing configs keep working".
  - `agent/internal/hooks/hooks.go:44-47`: `Input.ToolResult`/`UserPrompt` duplicate `tool_response`/`prompt` ("Legacy Evener aliases retained during migration", introduced 2026-08-18); these are a user-facing hook payload, so removal changes a public contract and is the one item here that needs a changelog line.
  - `agent/internal/contextmgr/checkpoint_format.go:311-324,650-680`: two checkpoint-text legacy parsers (`user_messages`/`agent_responses` bundles, `Original task:` line).
- Impact: dead surface that readers treat as API; tests cement the wrappers.
- Fix plan: delete each, move the callers/tests to the canonical function (`identifier.*`, `agent.RuntimeDir`, `Summarize`). Drop the `bool` parameter from `ApplyAdapterTimeout` (mechanical edit of 11 call sites). One PR per directory group; no behavior change except the hooks fields, the `recall` alias, and checkpoint parsers, which need approval.
- Size: net -150 lines for the no-behavior-change subset, -80 more for the alias/parsers.
- Risk: low.

## x-compat-legacy-11: 15 `//nolint:unused` retentions keep dead production code alive

- Severity: low to medium
- Locations: `agent/jobs.go:461,952,1377,1401,1779` (6 hits in the file), `agent/session_tools_jobs.go:369,2576,2673,(+1)`, `agent/sandbox_delegate.go:725,750`, `agent/subagent_manager.go:157`, `agent/job_watch.go:5216`, `internal/plugins/enabled.go`.
- Evidence: `grep -rn "nolint:unused"` over non-test Go gives 15; 12 say "retained for tagged/evenerfuzz state-machine owners". Example: `agent/job_watch.go:5212-5221` `recordWatchSendPending` "retains the pure runtime-state transition used by the state-machine harnesses. Production persistence uses planWatchSendPending ... "; `agent/session_tools_jobs.go:2576` `watchSendArg` "retained for internal watch-send parsing; public job_watch currently rejects send".
- Impact: production binaries carry code whose only job is to satisfy a build-tagged fuzz harness or a feature that is currently rejected; linters are silenced instead of the code moved.
- Fix plan: move each helper into a `_test.go` file or the tagged harness file that uses it; delete `watchSendArg` and its reject path if `job_watch send` stays unsupported. Remove the `nolint` lines. Verify with `go vet -tags` for each fuzz tag named in the comments (single-package vet only).
- Size: net -200 lines prod (moved to tests), 15 comment lines removed.
- Risk: low; fuzz-tag build breaks are caught by `make lint` tagged compile floors.

## x-compat-legacy-12: The legacy (non-identity) transcript entry projection path

- Severity: medium (needs a product decision)
- Locations: `internal/transcriptindex/build.go:19,45-59,100-130,163-340` (`applyLegacy`, open legacy group state, `turnKindLegacy`), `internal/transcriptindex/window.go:752,852,962`, `internal/transcriptindex/index.go:193,830`, `internal/apptranscript/apptranscript.go:439-447` (`ProjectEntryParts` falls through to `ProjectTurnParts` for `Format != TurnFormatIdentity`), `agent/schema/turn.go:95-98`, `agent/transcript/turn_status.go:8`.
- Evidence: `TurnFormatIdentity` was introduced 2026-09-26 (#2305, "Transcript read model phase 2a"); the only writer is `agent/transcript/placement.go:81`. Every transcript written before that date contains legacy entries, and the index builder keeps a parallel grouping state machine ("the legacy grouping state", `build.go:193-196`; "state applies to legacy entries only", `build.go:105`) to project them "as today's projection groups them". `window.go:962` documents `Context` as written only by `applyLegacy`. Since `cmd/evener-transcript-v2-upgrade` already exists as an offline converter for a different format bump (v1 to v2), a similar offline pass could stamp identity.
- Impact: two projection algorithms in the read model for the core user-visible data, a large fraction of the 12.5k lines in `transcriptindex`, doubled fuzz/test surface.
- Fix plan: (1) Ask Jesse how many pre-2026-09-26 transcripts must stay readable with exact old grouping. (2) Either extend `evener-transcript-v2-upgrade` to stamp identity (one pass that replays `ProjectTurnParts` grouping once and writes `TurnID/TurnKind`), then delete `applyLegacy` and the legacy branch of `ProjectEntryParts`; or accept that old transcripts re-render under the identity rules (cheaper, slightly different grouping) and delete the legacy branch directly.
- Size: -500 to -900 lines in transcriptindex and apptranscript plus tests; +150 for a converter pass if chosen.
- Risk: high; data-bearing. Needs a design discussion and recorded-transcript golden tests (see `agent/testdata`).

## x-compat-legacy-13: Plugin store self-migrations (marketplace renames, `.git` metadata move, store-format audit)

- Severity: medium
- Locations: `internal/plugins/marketplace_migration.go:1-1282`, `metadata_migration.go:1-343`, `store_format.go:1-94`, `internal/plugins/locks.go:253` (`migrateStore`), `marketplaces.go:548,586,593,1349` (legacy name directory handling).
- Evidence: the file header says "An older evener could record a marketplace under a name the store refuses today ... The store renames such an entry the first time it takes its lock (lockStore)". `marketplace_migration.go` was created 2026-09-12; `metadata_migration.go` and `store_format.go` 2026-10-21 (a four day old format audit with `Version == 1`). Migration-related files in the package total 7514 lines including tests of 31,720 for the whole package (about 24 percent).
- Impact: every lock acquisition and every listing pays for crash-recoverable rename markers (`renameMarker`, `migrationRecord`, aliases, recovery of "older merge markers") for names produced by a build that existed for under six weeks. The migration is also the main source of `plugins` complexity.
- Fix plan: ask Jesse which builds had plugin marketplace support and whether names with '@' or scratch names were ever stored in the wild. If not widespread, replace the whole rename machinery with a single check that refuses (or quarantines to a `.invalid` dir and logs) a bad name at read time, keeping the user's installed plugins. The `.git` metadata move and `store-format.json` audit could become "re-audit lazily and ignore state on mismatch". Keep a regression test per surviving behavior.
- Size: -1200 to -1500 lines prod, -2500 to -3500 tests.
- Risk: medium; recovery semantics for half-renamed stores need a design review (AGENTS.md: preserve user work).

## x-compat-legacy-14: Persisted client-state "eras" in web and native stores

- Severity: medium
- Locations:
  - Web layout: `cmd/evener-hub/frontend/src/shell/workspace.ts:360-470,736-770,852` (`migrateLegacyLayout`, `legacyDuplicatePanels`, per-panel transcript-to-session retyping and parent lifting), introduced 2026-10-12 (#4373).
  - Web session cache: `frontend/src/stores/sessionCacheSchema.ts:4-22,132-137` (drops four legacy IndexedDB stores) and `frontend/src/stores/sessionEntryCache.ts:322-345,405` (`legacySupersededByProbe`, handling "pre-authority" page certificates).
  - Native drafts: `mobile-native/src/questionAnswers.ts:66-125` (three "eras" of stored question definitions: canonical, display-wrapped, bound), `mobile-native/src/nativePreferenceDrafts.ts:252-320` (`migrateLegacyTranscriptCheckpoint`, a `legacyBytes` WeakMap), `mobile-native/src/mutationOutboxStorage.ts:400,438,494-497` (`#seedLegacyAttemptCount`), `mobile-native/src/draftDocument.ts:59-61,84,797` (`releasedRecoveryIds` legacy field), `mobile-native/src/newSession.ts:708-722` (old per-launch model/effort override).
- Evidence: the native app's first checkpoint landed 2026-09-11 (#1096) and `questionAnswers.ts` was created that day; `canonicalQuestion` identity was reworked 2026-09-22, so "eras" are 11 days apart. The session cache is documented (`sessionCacheSchema.ts:132`, "Ruling 37") as "rebuilt from the server, never migrated", yet `legacySupersededByProbe` still reasons about old cache rows.
- Impact: every persisted-state reader carries branches for earlier shapes of data that is either cache (rebuildable) or local drafts of a pre-release app (TestFlight only; `.github/workflows/ios-testflight.yml`).
- Fix plan: (1) Cache: bump `SESSION_CACHE_DATABASE_VERSION` to drop all stores and delete `legacySupersededByProbe` plus its probe/chain plumbing. (2) Web layout: drop `migrateLegacyLayout` and `legacyDuplicatePanels`; unrecognized panes reset to the default layout (a layout is cheap to rebuild). (3) Native drafts: with Jesse's approval, treat any draft/outbox record without the current shape as discarded-with-notice only where there is no user text; where draft text exists, keep text and drop metadata. Do not silently discard user prose (AGENTS.md: preserve the user's work).
- Size: -350 to -500 lines prod, similar in tests.
- Risk: medium for native drafts (user text), low for cache and layout.

## x-compat-legacy-15: Comments narrate deleted systems and process IDs, plus a test that polices a comment path

- Severity: medium (cognitive tax, misleads maintainers)
- Locations: 55 occurrences of `assets/<x>.js`, `renderer.js`, `serf-hub/assets` in 31 non-doc source files (counted with `grep -rcE "assets/[a-z-]+\.js|renderer\.js|serf-hub/assets"`), for example `frontend/src/widgets/textarea/index.tsx:24,91` ("cmd/evener-hub/assets/renderer.js:6307-6314"), `frontend/src/panes/session/liveness.ts:9`, `frontend/src/widgets/focusscope/index.tsx:20`, `stores/prefs.ts:41,68,440`, `panes/session/composer/Composer.tsx:608,897,1429-1473` ("floor §2.1, legacy renderer.js:6914"), `appwire-client/typescript/reducer.ts:386`, `appwire/types.go` (22 refs to legacy `web_settings.go` computation/`storage.html`). Also 62 `floor §N` citations, 39 `wave-N` citations, 8 `Round N` and 4 `Ruling N` mentions in non-test source (`mobile-native/src/hubConnection.ts:178` "Round 85's follow-up"; `sessionCacheSchema.ts:132` "Ruling 37").
- Evidence: `cmd/evener-hub/assets` now holds only icons (`icon-192.png`, `icon-512.png`, `icon-maskable-512.png`); the legacy 33-file JS UI was deleted by commit "webui m10: delete legacy assets (33 JS + style.css)" on 2026-07-22. `appwirelegacypath_audit_test.go` (102 lines, root package) exists solely to assert that surviving comments name the historical `cmd/serf-hub/assets/appwire.js` path instead of the never-existing evener-hub assets path for appwire.js; it shells out to git (`appwireLegacyTrackedFiles`).
- Impact: roughly 160 comments cite line numbers in a file that no longer exists or review rounds nobody can look up; the 102-line audit test protects the wording of those comments. Violates the "name code by what it does, no history comments" rule in CLAUDE.md.
- Fix plan: delete the audit test; rewrite each comment to state the rule (for example "textarea grows to fit its content up to the clamp, resetting to auto first so shrinking works") and drop the citation. Do it in a mechanical sweep per directory with a final grep guard added to `make lint` for `renderer\.js|assets/[a-z-]+\.js`. Keep comments that explain behavior; drop only the pointer.
- Size: ~160 comment edits, -102 test lines.
- Risk: low (comment only); use Biome/gofmt to avoid whitespace churn.

## x-compat-legacy-16: UPGRADING.md is stale: four "Unreleased" sections for changes that shipped

- Severity: low
- Locations: `UPGRADING.md:49,58,79,87` (headings "Unreleased"), `:5-47` (delegate runbook).
- Evidence: `git tag | wc -l` is 27, latest `v0.3.16` (2026-10-23); `git tag --contains` on the commit that introduced the evener-dev section lists `v0.2.0` and `snapshot`. So "Unreleased" labels sections that have been released for many tags. The `evener-dev` section itself says the transition-window builds "have aged out".
- Impact: doc drift; a reader cannot tell what applies to which upgrade.
- Fix plan: with whichever decisions land from findings 6 to 9, delete the sections for removed compat; relabel remaining ones with the first tag that contains them; keep the file a short list of still-relevant breaking changes. Update `docs/evener-hub.md#upgrading-and-migrating-safely` references if sections are removed.
- Size: -100 lines of docs.
- Risk: none.

## x-compat-legacy-17: Version-suffixed names with no sibling version

- Severity: low
- Locations: `TranscriptDisplayConfigV1` (218 references, no `V2` anywhere), `TranscriptDisplayAdvancedV1` (17), `responseContextMarkerV1` (15), `transcriptItemCursorV1` (10); `cmd/evener-hub/navigation_service.go:566` `readV3` (94 refs, mostly `navigation_service_test.go`, no `readV1/readV2`); `appwire-client/typescript/reducer.test.ts` `testHydrateV6` (22).
- Evidence: counts above from `grep -rniE '\b[A-Za-z]+V[0-9]\b'` over source; `readV3` doc at navigation_service.go:338-348 describes it as the current reader.
- Impact: low, but names that look like version ladders invite adding compat for "the other version" that never existed; `readV3` forces every test to say V3.
- Fix plan: rename in one mechanical commit each (`readV3` to `read`, `TranscriptDisplayConfigV1` stays only if the wire name is persisted; rename the TS symbol if not). Keep persisted wire/storage keys such as `evener.rail.expanded.v1` since they are genuine format tags.
- Size: ~320 renamed occurrences, net 0.
- Risk: low; generated code requires regenerating from Go.

---

## Recommended fix sequence

All steps need Jesse's explicit sign-off on what to drop; items marked "no approval needed" delete code with no behavior change.

1. **Decide the skew window and write it down (finding 1).** Docs only; unblocks everything else. One question to Jesse: which hub/phone/daemon skew is supported?
2. **No-behavior-change deletions (no approval needed):** the six dead alias exports in `prefs.ts` (3), sshconn test-only wait/probe helpers (4), host mutation pre-union adapters (5), the `execenv/gitpath.go` wrappers, `ResolveStateKeyDir`, `TaskStore.Progress`, `hexHash`, `ApplyAdapterTimeout` bool, `Session.close` (10 subset), and the `nolint:unused` retentions moved into test files (11). Verify each by single-package `go vet` and the tagged compile floors; run focused tests in CI.
3. **Delete compat that only ever existed on a branch:** `compensating-sidecar` alias and legacy `.stash` spelling (6a); the dual-write and unused prefs state (3, with harness tests rewritten first).
4. **Per-decision removals, in this order of payoff / ease:** deprecated update-apply path (2), `purpose` fallback triple (9), `recall` and hooks field aliases (10 remainder), cache/layout migrations (14 parts 1 and 2), delegate legacy-state rejection (8), hub sidecar and `LegacyProject` (6b, 6d).
5. **Bigger design discussions:** serf migration/auto-migrate vs delete (7), plugin store rename machinery (13), native draft eras (14 part 3), legacy transcript projection (12). Each needs a spec; none should begin without a recorded decision.
6. **Comment sweep and docs (15, 16, 17):** do last so comments are not rewritten twice; add the lint guard for `renderer.js` style citations; refresh `UPGRADING.md` and the subsystem map rows touched by earlier steps in the same PRs.
