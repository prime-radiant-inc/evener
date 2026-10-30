# x-dead-code: cross-cutting dead code audit (Go + TS)

Worktree: `.worktrees/debt-audit` at origin/main (a6c0b9c561). Read-only audit; no tests were run.

## Method and inventory

Tools actually run (all offline, none of them run tests):

- `golang.org/x/tools/cmd/deadcode` v0.49.0, built from the local module cache with `GOTOOLCHAIN=go1.27.0`, run once over the whole go.work program (`./...` plus every workspace module). It reports functions unreachable from any `main` package. 589 functions flagged.
- A name-based classifier (my own script) that, for every flagged function, counts identifier uses in non-test Go files and in `_test.go` files across the repo. Result: **0 flagged functions are unused everywhere; 76 have zero production references and only test references**; the remaining 513 are reachable only through other unreachable code, through test-seam package variables, or through reflection-style registration.
- A global declaration scan (go/ast) of every top-level func/type/var/const/method with zero production references (582 hits, of which ~39 have zero references anywhere).
- `go list` import graph: packages unreachable from every `main`.
- A TS script over `cmd/evener-hub/frontend/src`, `appwire-client/typescript` and `mobile-native/src`: exported symbols with no importer, files with no importer, re-exports of `index.ts` with no frontend/mobile consumer, npm dependencies with no reference.
- Grep passes over `envvars/envvars.go` (111 vars), `make/*.mk` (71 targets) and ~185 scripts.

Inventory: 1164 non-test Go files (392,250 lines) against 1,085,858 lines of Go test code. TS: 1280 files in the frontend `src`, 324 in `appwire-client/typescript`, 838 in `mobile-native/src`.

Negative results (checked, nothing worth a finding):

- **Make targets**: all 71 targets are referenced by CI, docs, or other targets, and `makefiletargets_audit_test.go` already polices them. No dead target.
- **Env vars**: of 111 registered vars, 42 have zero Go production references. All the provider ones (`GROQ_*`, `XAI_*`, `DATABRICKS_HOST`, ...) are resolved by name from the embedded models.dev snapshot (`llm/registry/data/models.dev.json.gz`), so they are live. `HOMEDRIVE`/`HOMEPATH` are inherited-passthrough names that code deliberately ignores (`cmdutil/statedir.go:61`). Only `envvars.ByVisibility` is dead API (finding 6).
- **Scripts**: every script has at least one referrer. A handful are one-shot tools (finding 14).
- Whole Go packages unreachable from a `main`: only test-support packages, plus `frontmatter` (finding 5).

Overall: dead code in this repo is rarely "never referenced". It is mostly **code that only tests reference**: wrappers, parallel implementations and legacy ladders kept alive by tests that exercise them instead of the production path. That is the more dangerous kind, because the tests give false assurance.

---

## Findings

### x-dead-code-1: 75 production functions are referenced only by tests (about 600 lines)

Severity: medium. Category: dead-code.

Locations (deadcode-unreachable AND zero non-test references; test-reference counts in brackets):

- Thin wrappers whose only caller is a test: `agent/aside.go:29 asideSessionFS` -> `asideSessionFSWithConfig`; `agent/delegate_runtime.go:554 delegateQuietAttentionID` [18]; `agent/job_shell.go:731 shellTerminal` [7]; `agent/jobs.go:1780 finalizeWithRunNoNotification` [10]; `agent/jobs_activity.go:404 projectStableLiveActivityTree` and `:1058 projectActivitySession`; `agent/session_attention.go:436 appendColdAttentionResolution`; `agent/session_namer.go:442 nameSessionFromCompactionTurn` [13]; `agent/session_tools_jobs.go:141 jobWatchTool` [35] -> `jobWatchToolWithContext(context.Background(), ...)`; `:370 clearDescendantReceiverWatchByID` -> `clearStableReceiverWatchByID`; `agent/session_tools_transcript.go:401 parseRetainedReadArgs` [19]; `agent/subagents.go:399 isRootOnlyJobPresenceTool`; `agent/execenv/local.go:3542 filteredEnvWithPolicy` and `:3594 filteredEnv`; `agent/internal/tool/registry.go:1041 truncateLines`; `cmd/evener-hub/app_navigation.go:51 navigationReadKey`; `cmd/evener-hub/app_threadread.go:181 pastThreadReadResponse` [18]; `:1635 reconcileDelegateThreadItemForTest`; `cmd/evener-hub/spawn.go:815 resolveEvenerStateDir`; `internal/appserver/websocket.go:266 runWebSocketSendLoop`; `internal/plugins/paths.go:140 Manager.pluginCacheDir` [76]; `cmd/evener-hub/internal/appsource/local_daemon.go:135 NewLocalDaemonSource` [28]; `cmd/evener-hub/internal/launchconfig/resolver.go:146 loadRepoLayer`.
- Locked/unlocked pairs where production calls only the `...Locked` half: `agent/delegate_tree_attention.go:208 noteDelegateAttention` [27] and `:679 nextIdleDelegateAttention` [25]; `agent/delegate_tree_controller.go:975 completionSnapshot` [25] and `:1252 capacityInUse` [18]; `agent/jobs.go:953 abandonRunningJob`; `agent/jobs.go:1444 grepOutput`; `agent/job_watch.go:6560 watchCount` [85].
- Whole alternative helpers: `cmd/evener-hub/app_threadread.go:1639 reconcileDelegateThreadItems` (36 lines; production uses the singular `reconcileDelegateThreadItem`), `:1699 isTerminalHistoricalJobStatus`; `cmd/evener-hub/navigation_normalize.go:166 cloneNavigationSnapshot`; `cmd/evener-hub/navigation_projection.go:277 cloneNavigationInputs`; `cmd/evener-hub/web_api.go:23 writeAPIWireError`; `cmd/evener-hub/web_api_tree.go:613 navigationTreeInputs`, `:1523 liveEntry`; `cmd/evener-hub/spawn.go:1072 envLookup`; `cmd/evener-hub/web.go:440 splitProviderModel`; `server/appwire_runtime.go:3053 inputFromItems` (22 lines); `agent/subagents.go:579 localEnvPolicyFromName`, `:1439 Session.sendInput` (starts with `_ = ctx`); `agent/subagent_manager.go:158 beginReconstructionSideEffects`; `agent/delegate_tree_start.go:930 inputPersistFailureBatch`; `agent/jobs.go:743 createJobOutputForID`; `llm/providers/internal/transport/http_attempts.go:431-435 observedBytes/observedExactly`; `cmd/evener-hub/internal/sshconn/manager.go:3688 setPendingRestart`.

Evidence: `deadcode` lists each as "unreachable func"; a repo-wide identifier count over non-test Go files gives 0 references besides the declaration. Examples of the pattern:

```go
// agent/session_tools_jobs.go:141
func jobWatchTool(s *Session, args map[string]any, maxChars int) (any, error) {
	return jobWatchToolWithContext(context.Background(), s, args, maxChars)
}
```

Impact: about 600 lines of production code exist so tests can call a shorter spelling. Each pair doubles the surface a reader has to reason about, and tests that call the wrapper bypass the context/lock/authority the production caller passes (`jobWatchTool` supplies `context.Background()`; `noteDelegateAttention` takes the retirement mutation that the production path takes elsewhere). That is the same shape as "tests of mocks": the test is not covering the production call path.

Fix plan: for each function, move the one-liner into the `_test.go` file that uses it (a test helper next to its callers) or, better, change the tests to call the production function with explicit arguments and delete the wrapper. Where a function has real logic (`reconcileDelegateThreadItems`, `cloneNavigationSnapshot`, `cloneNavigationInputs`, `inputFromItems`, `writeAPIWireError`, `localEnvPolicyFromName`, `sendInput`, `grepOutput`, `abandonRunningJob`, `createJobOutputForID`), delete it together with the tests that only exist for it, after checking each test asserts something the production function does not already cover. Do it package by package (agent, cmd/evener-hub, the rest), each PR behavior-preserving; the compile is the guard. No doc updates needed except where `docs/product/subsystems.md` names a deleted symbol (grep first).

Size: about 600 lines removed from production, 300 to 800 lines moved or removed in tests. Risk: low; the compiler finds every missed caller.

---

### x-dead-code-2: `jobManager.createShell` is an 85-line second implementation of shell-job start that only tests use

Severity: high. Category: duplication (dead production, live in tests).

Locations: `agent/jobs.go:991-1075` (`createShell`), `agent/jobs.go:623` (`createShellOpts`), versus the real start path at `agent/job_shell.go:616` (`Kind: jobstore.EventJobStarted`) and `agent/jobs.go:1208` (`emit(events.EventJobStarted...)`).

Evidence: `deadcode` reports `createShell` unreachable; the only production reference is the declaration, and `_test.go` files call it **238 times** (25 in `jobs_test.go`, 22 each in `job_watch_events_test.go` and `job_watch_config_test.go`, 12 in `session_retirement_evidence_test.go`, 10 in `job_watch_loopguard_test.go`). It builds a `JobRecord`, appends `EventJobStarted`, forwards it and registers the `runningJob` exactly as the shell start does, but with a different field set (`done: make(chan struct{})`, `signal: func() {}`, `durableStarted: true`).

Impact: the watch/notification/retirement tests construct jobs through a code path production never executes, so a regression in the real start sequence (`job_shell.go`) passes all of them. Any change to job-start fields has to be made in two places, and only the one that tests use is checked. This is the highest-risk item in the slice because it is false coverage, not just dead weight.

Fix plan: (1) read `job_shell.go` around line 616 and extract the shared "build started record + event + register run" step into one unexported function used by both the real start and tests, or (2) drive the tests through the real shell start with a trivial command, as the repo's testing guide prefers ("real Evener code below the LLM boundary"). Prefer (2) for tests that care about lifecycle, and a small helper in a `_test.go` file for tests that only need a running record. Delete `createShell` and `createShellOpts` from production. Run the affected packages' tests in CI, since local runs are off limits.

Size: -85 production lines, about 238 call sites touched in tests (mechanical if a test helper keeps the same signature). Risk: medium, because the 238 call sites are mechanical but each test's timing assumptions change if the helper stops being synchronous. Do it in a PR of its own.

---

### x-dead-code-3: path-based job-output snapshot readers (about 330 lines) are a test-only twin of the fd-based readers production uses

Severity: medium. Category: duplication / dead-code.

Locations: `agent/internal/jobstore/output_snapshot.go:63-427` (`ReadOutputSnapshot`, `ReadOutputWindowSnapshot`, `ReadOutputPageSnapshot` and the `...Fs`, `...WithRetry`, `...Once`, `...Attempt`, `readOutputRawSnapshotWindow`, `readOutputSnapshotWindow` chain), `agent/internal/jobstore/output.go:690 readOpenOutputFileView`; production callers at `agent/job_transcript_read.go:121,127,133` use `ReadOutputSnapshotFromFile`, `ReadOutputWindowSnapshotFromFile`, `ReadOutputPageSnapshotFromFile` from `output_snapshot_fd.go` (302 lines). Also `agent/retained_output_read.go:91` has a comment naming the path-based `ReadOutputWindowSnapshot`.

Evidence: `grep` for the three exported path-based functions in non-test code outside `jobstore/output_snapshot.go` finds only that comment. 27 functions in this package are flagged unreachable (308 lines). Tests: `output_snapshot_test.go` 1182 lines and `output_snapshot_covtest_test.go` 265 lines reference the path-based set (48 + 22 references), while `output_snapshot_fd_test.go` (528 lines) covers the fd set. The two sets have mirrored structure: `readOutputSnapshotAttempt` (output_snapshot.go:307) vs `readOutputSnapshotFromFileAttempt` (`_fd.go:135`), `readOutputRangeSnapshotAttempt` vs `...FromFileAttempt`, `observeOutputSnapshot` vs `observeOutputSnapshotFromFile`. Several helpers have zero test references too (`readOutputPageSnapshotOnce`, `readOutputRangeSnapshotOnce`, `readOutputRangeSnapshotAttempt`, `readOutputSnapshotAttempt`).

Impact: two copies of a delicate consistency protocol (before/after observation, retry on concurrent change). Fixes to the race handling must be made twice, and the copy that gets the test attention is the one production does not run.

Fix plan: delete the path-based set from `output_snapshot.go`, keeping the shared pieces (`outputSnapshotWindow`, `trimOutputSnapshotWindow`, `readOutputMetaForSnapshot`, `outputSnapshotObservation`, `changedFrom`) that the fd variants use. Port any test case that only the path-based tests cover (concurrent-append, truncation, rotated file) to the `_fd` tests; delete the duplicates. The `newOutputSnapshotFileFS` shim in `_fd.go:277` exists to adapt an open file to the `afero.Fs` readers; once the path set is gone, consider whether the fd readers still need `afero` at all (follow-up, not part of this PR).

Size: about -330 production lines, about -1000 test lines (after porting unique cases). Risk: medium; the race-handling tests must be preserved against the fd readers. Update no docs (internal package).

---

### x-dead-code-4: the high-level streaming API in `llm` has no production caller (about 700 lines)

Severity: medium. Category: dead-code.

Locations: `llm/stream_generate.go` (425 lines: `StreamResult`, `StreamGenerate`, `TextStream`, `PartialResponse`, `TotalUsage`, `Steps`), `llm/generate_object.go:68-300` (`StreamObjectResult`, `StreamGenerateObject` 84 lines, `tryParsePartialJSON` 67 lines, `tryUnmarshal`), seam at `llm/generate_object.go:23` (`streamGenerateObjectStart = StreamGenerate`).

Evidence: `grep -rn 'StreamGenerate('` in non-test Go finds only the definition and that package-level seam variable; `StreamGenerateObject` has zero non-test references (deadcode lists it unreachable). `StreamGenerate` itself is not in deadcode's output only because the seam variable assignment makes it reachable. `GenerateObject` (non-streaming) is used by `tools/tool-fluency/cmd/evener-fluency/respond.go:240`; the streaming forms are not. Test references: 6 files, fuzz harness `llm/generate_core_fuzz_test.go:19` names them. Methods flagged unreachable: `StreamResult.TextStream` (38 lines, 13 test refs), `PartialResponse`, `TotalUsage`, `Steps`.

Impact: a public-looking streaming/step-loop API with its own retry loop (`llm/stream_retry.go:76` mentions "the high-level StreamGenerate loop") and incremental-JSON parser that nothing in the product calls. Agents consume the lower-level `Client.Stream`. It also anchors fuzz targets and docs (`docs/subagent-management/08-standalone-llm-calls.md`) to an unused API.

Fix plan: this is a design call (the `llm` module is separately versioned, so someone may want the SDK shape). Recommend Jesse decide: if the module is not a product, delete `stream_generate.go`, the streaming half of `generate_object.go`, the seam variable, and the tests and fuzz targets for them; fix the comments in `stream_retry.go` and `classify.go:11` that name `StreamGenerate`, and the doc. If it is kept, say so in `llm/doc.go` and keep exactly one caller (e.g. `evener-fluency`) so it is exercised.

Size: -700 production lines, about -1200 test/fuzz lines. Risk: low technically; needs the product decision.

---

### x-dead-code-5: root `frontmatter` package duplicates `agent/internal/frontmatter` and only one test imports it

Severity: medium. Category: duplication / dead-code.

Locations: `frontmatter/frontmatter.go:1-61` (plus `frontmatter_test.go` 220 lines, `frontmatter_fuzz_test.go`, `dos_regression_test.go`), importer `internal/bundled/bundled_test.go:9,51`, canonical copy `agent/internal/frontmatter/frontmatter.go` (601 lines with tests; production importers: `agent/memory_migrate.go:21`, `agent/memory_write_page.go:7`, `agent/prompt_data.go:7`, `agent/memory_page.go:16`, `agent/plugin/settings.go:7`, `agent/plugin/commands.go:9`, `agent/plugin/agents.go:10`, `agent/skill/metadata.go:10`, `agent/skill/description.go:12`).

Evidence: `go list` shows `primeradiant.com/evener/frontmatter` unreachable from every `main` with exactly 1 test importer. Its own doc comment admits the duplication: "It matches agent/internal/frontmatter, which this module cannot import; that package additionally reports how the document was framed." It is also named in `testing-budget.json`, `scripts/fuzz/fuzz-targets.txt`, `scripts/fuzz/fuzz-mutation-score.sh` and `fuzz/mutations/frontmatter-determinism-naive.patch`, so the duplicate carries a fuzz target and a mutation patch.

Impact: two parsers of the same format, one of them production-dead but fuzzed and mutation-scored (CI time spent on code that ships nowhere); a fix to CRLF/BOM handling must be made twice.

Fix plan: change `internal/bundled/bundled_test.go` to use `agent/internal/frontmatter` if the module boundary allows (the test lives in the root module, `agent/internal` is not importable from there, which is presumably why the copy exists). If it cannot, inline the 4-line `---` split the bundled test needs, or move that assertion into an `agent` package test where `agent/internal/frontmatter` is importable. Then delete `frontmatter/`, its fuzz target from `scripts/fuzz/fuzz-targets.txt`, the mutation patch, and the `testing-budget.json` entry.

Size: -364 lines plus fuzz wiring. Risk: low.

---

### x-dead-code-6: zero-reference exported API (identifier, appwire, llm, envvars, hostops, sshconn)

Severity: low. Category: dead-code.

Locations and evidence (references counted over all non-test and test Go; "0/0" = none anywhere unless stated):

- `identifier/domains.go:34-44` `ValidateDelegateID`, `ValidateWatchID`, `ValidateWatchGeneration`, `ValidateWatchDeliveryID`, `ValidateAgentCallID`, `ValidateSyntheticCallID`, `ValidateClientMutationID`, `ValidateTerminalGeneration`, `ValidateTurnID`, `ValidateRoundID`; `:55 MustNewInstallationID`, `:63 MustNewClientMutationID`. Zero production uses (`ValidateTurnID`/`ValidateRoundID` have 1 test use each). 12 of the 15 flagged identifier functions; the table-style one-liners were generated alongside the ones in use (`ValidateSessionID`, `ValidateAPIAttemptID`).
- `appwire/status.go:3 IsActiveThreadStatus`, `:16 IsActiveTurnStatus`, `:29 IsActiveItemStatus`, `:33 IsTerminalItemStatus` (the last is a verbatim copy of `IsTerminalTurnStatus` at `:20`, and `IsActiveItemStatus` is `status == TurnStatusInProgress`, comparing an item status to a turn constant). Also `appwire/errors.go:966 QueuedDrainPartial`.
- `llm/api_attempt_sanitize.go:42 BuildAPILogCredentialMaterial` (18 lines): its doc says "This is the shared implementation used by all provider adapters"; no adapter calls it (adapters call `NewAPILogCredentialMaterial` directly, e.g. `llm/api_attempt.go:466`; `llm/providers/internal/protocolhttp/call.go:159` still mentions it in a comment). So the doc is false. `llm/registry/derive.go:226 Caps.RequestEffortCapable`; `llm/types.go:418 FinishReasonError`; `llm/registry/load.go:96,118,122 WithFetcher/WithOverlay/WithLog` (test refs 2/25/1).
- `envvars/envvars.go:187 ByVisibility` (0 prod, 8 test refs).
- `cmd/evener-hub/internal/sshconn/version.go:83 ValidateHubAddrShape` (0/0), `cmd/evener-hub/internal/hostops/store.go:444 RecordOrReplay` (0/0), `agent/session_worktree_close.go:282 CloseStopJoinBudget` (0/0; its comment says it is EXPORTED so cmd/evener tests can use it, and they do not), `cmd/evener-hub/internal/launchconfig/schema.go:33,37 LaunchPathNone, LaunchPathCommand`.
- Unused unexported constants: `agent/delegate_tree_intents.go:51 finishStalePropagate`, `agent/jobs.go:48 structuredResultReasonProjectionTooLarge`, `agent/session_tools_jobs.go:36 jobKindAgent`, `:38 jobPhaseStarting`.
- `appwire/client.go:835 FavoriteSet`, `:883 UpdateCheck`, `:973 PluginCheckUpdates`, `:1027-1042 DevicePairingCreate/Read/Approve/Cancel`: typed RPC wrappers with no caller anywhere (deadcode treats exported methods as roots, so it does not list them). `appwire/client.go` holds 97 hand-written one-method-per-RPC wrappers of identical shape.

Impact: low individually; the false `BuildAPILogCredentialMaterial` doc and the generated-looking validator table add noise when searching for the real credential-redaction path.

Fix plan: delete the listed symbols (and the stale comments), one commit per package. For `appwire.Client`, either delete the 7 unused wrappers or (separate, discuss with Jesse first) generate the wrappers from `protocol.go`'s method table so the set cannot drift. Keep `IsTerminalTurnStatus` and make the item variant an alias only if a caller needs it.

Size: about -180 lines. Risk: very low.

---

### x-dead-code-7: `sshconn` keeps a legacy probe/wait ladder (about 400 lines) "retained for direct tests"

Severity: medium. Category: compat-legacy / dead-code.

Locations: `cmd/evener-hub/internal/sshconn/version.go`: `:62-69 hostAddrFor` ("supplies the legacy probe helpers, retained for their direct tests. Managed attachment and restart use the selected endpoint, not this address."), `:290 detectSupervisorsFrom`, `:321 pickSupervisor`, `:740 probeListeners`, `:776 listenerAddrRemote`, `:841 hostIsIPv4`, `:849 hostIsIPv6`, `:959 portCleared`, `:1040 waitHealthy`, `:1068-1127 waitForHealthyHub` ("backs the legacy health-wait tests ... Managed restart uses selected successor proof instead"), `:1135 hubAnswerProvesReplacement`, `:1147 describeHubIdentity`, `:1185 parseHubHealthBuild`; `manager.go:3688 setPendingRestart`; `deploy.go:1063 deploySelected`.

Evidence: deadcode flags 33 sshconn functions, 396 lines (`wc` of the flagged spans); the file's own comments say they exist for tests. `waitForHealthyHub` has zero test references and zero production callers outside the dead `waitHealthy`. The package is 44,036 lines in 111 files (11,410 non-test), and `version_test.go` alone is 3,811 lines.

Impact: the "legacy" ladder sits next to the live one (`expectedServedBuild` at `version.go:1030` is shared), so a reader cannot tell which restart-proof rules are in force. AGENTS.md says no compat without approval; this is compat kept by tests.

Fix plan: delete the unreachable functions, then delete or rewrite the tests that exist for them (list from `grep -l 'waitForHealthyHub\|probeListeners\|detectSupervisorsFrom' *_test.go`). Keep any behavior assertion that the selected-endpoint path does not already make (replacement proof for dev/dirty versions: `hubAnswerProvesReplacement` rules are described in the comment at `version.go:1030` and should be re-verified against the successor-proof code before deletion). Update `docs/evener-hub-remote-operations.md` if it describes the legacy wait.

Size: about -400 production, -1000 to -2000 test lines. Risk: medium; do it as its own PR with a CI-only test run.

---

### x-dead-code-8: unused hub REST proxy and tree-builder convenience entry points

Severity: low. Category: dead-code.

Locations: `cmd/evener-hub/internal/hubcore/proxy.go:1-83` (`RESTProxy`, `NewRESTProxy`, `proxyFor`, `splitLivePath`), `cmd/evener-hub/internal/hubcore/tree.go:672 TopLevelSessionIDs`, `:686 BuildTree`, `:699 BuildTreeAt` (94 test refs), `:1758 BuildProjectTree`, `:1769 BuildProjectTreeAt` (41 lines); `cmd/evener-hub/web_api_tree.go:34` (`hubBuildNavigationTree = hubcore.BuildTreeWithProjects`).

Evidence: no production code references `RESTProxy` or `NewRESTProxy` (3 test files do); no production code serves a `/live/<id>/<endpoint>` route (`grep '/live/'` in non-test hub code finds only `proxy.go`), and no doc mentions it. `BuildTree` and `BuildTreeAt` are wrappers that resolve projects and call `BuildTreeAtWithProjects`, which production calls through the seam variable; tests call the wrapper 94 times.

Impact: a dead HTTP route implementation looks like a live proxy surface during security review; the `BuildTree*` wrappers hide that production uses a different entry point.

Fix plan: delete `proxy.go` and its three tests. For the tree builders, point tests at `BuildTreeAtWithProjects` through a `_test.go` helper that resolves projects, and delete `BuildTree`, `BuildTreeAt`, `BuildProjectTree`. Check `docs/product/subsystems.md` for the REST proxy row (none found by grep, confirm).

Size: about -150 production lines, tests mostly mechanical. Risk: low.

---

### x-dead-code-9: serve's process-wide API logger path is dead (`newServeLLMClient`, `serveAttachAPILogger`)

Severity: low. Category: dead-code.

Locations: `cmd/evener/serve.go:96-98 serveAttachAPILogger`, `:305-323 newServeLLMClient`; production wiring `:270` uses `newUnloggedServeLLMClient` with `attachAPILogger: serveAttachSessionAPILogger`; `cmdutil/api_logging.go:14 AttachAPILogger(..., resumedSessionID ...string)`.

Evidence: `newServeLLMClient` has zero production references (4 test refs); the only remaining production caller of the process-wide `cmdutil.AttachAPILogger` is `tools/tool-fluency/cmd/evener-fluency/main.go:1086` and `cmd/evener/run.go`'s own seam. The variadic `resumedSessionID ...string` parameter exists for compat with older call sites.

Impact: a reader of `serve.go` sees two ways to build the client (logged and unlogged) and has to work out which one runs; tests that use the logged one do not cover production wiring.

Fix plan: delete `newServeLLMClient` and `serveAttachAPILogger`, repoint the 4 tests at `defaultServeDeps().newClient` plus `attachAPILogger`; then check whether `AttachAPILogger`'s variadic parameter has any caller that passes it and drop it if not.

Size: about -30 lines. Risk: low.

---

### x-dead-code-10: dead alias exports and a "compatibility seam" in the web frontend

Severity: low. Category: dead-code.

Locations (all have exactly one occurrence in the repo, the declaration):

- `cmd/evener-hub/frontend/src/stores/prefs.ts:237-240 readLegacyPref, hasLegacyPref, writeLegacyBool, removeLegacyPref`, `:258 migrateLegacyPrefs`, `:265 dualWriteLegacyPrefs` (six `export const x = y` aliases of the real names that `transcriptDisplay.ts:22` and `prefs.test.ts` use).
- `cmd/evener-hub/frontend/src/notifications/attention.ts:55 snapshotFromTree` ("Compatibility seam for the notification owner during the migration").
- `cmd/evener-hub/frontend/src/shell/rail/railRenderObserver.tsx:8 RailRenderObserverProvider = RailRenderObserver`.
- `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts:303 subagentChildrenOf`, `stores/archivedList.ts:35 useArchivedList`, `panes/transcript/jobLogWindow.ts:7 JOB_LOG_FRAGMENT_BYTES`, `widgets/disclosure/disclosureStore.ts:93 clearDisclosureScope`, `stores/sessionCacheSchema.ts:117 EntryClearEpochRow`.
- `appwire-client/typescript/reducer.ts:400-401 isToolResultItemId, isToolCallItemId` (aliases re-exported from `index.ts:530-531`, no consumer), `state/mutation/settledIdentities.ts:33 appliedMutationIdentities` (re-exported, no consumer anywhere).
- 24 more `appwire-client/typescript` exports with no importer outside their own file (types like `HostMutationOutcome`, `ErrorKind`, `PairingClaim`, `WarningFold`; `state/credentials/instances.ts listingChanged`; `transcriptDisplayStore.ts LAYOUTS, isViewportClass`).

Evidence: a script that counts identifier occurrences in all tracked TS/JS/HTML found 16 frontend exports with a single occurrence and 24 `appwire-client` exports referenced only in their own file. The aliases are leftovers of renames (`readLegacyPref` vs `readLegacyPreference`).

Impact: low; aliases make renames look unfinished and widen the shared client's public surface.

Fix plan: delete the aliases and `snapshotFromTree`; un-export (or delete) the single-occurrence symbols; for `appwire-client`, remove the matching lines from `index.ts` and run `npm run check-package-tests` / `publicExports.test.ts` (it asserts the export list, so update it in the same PR).

Size: about -80 lines. Risk: very low (tsc finds any importer).

---

### x-dead-code-11: 78 `ForTests`-style exports in 52 production TS files and 20 in production Go

Severity: low. Category: test-debt.

Locations: TS: `cmd/evener-hub/frontend/src/stores/threads.ts` (8,990 lines; 11 `...ForTests` exports at `:674, 679, 762, 853, 1993, 2020, 2999 (resumeStopFence), 5210, 8691, 8710, 8725, 8729, 8982`), `stores/keybindings.ts:113`, `stores/mutationClientIdentity.ts:30`, `stores/projectionWork.ts:38`, `widgets/toast/store.ts:55` (119 test refs), `shell/rail/railHostChunk.ts:32,36`, `shell/dockHostChunk.ts:175,179`, `panes/spawn/connectDialogChunk.ts:33,37`, `testEndedBodyGuard.ts:56,72`, and others. Go: `agent/session_notes_rpc.go` (3), `internal/transcriptindex/index.go` (2), `cmd/evener-hub/internal/hubcore/past.go` (2), `agent/subagent_tally.go`, `agent/session_jobtree_drain.go`, `agent/execenv/{local,gitpath,search_patterns}.go`, `agent/sandbox/session_tmp.go:53`, `cmd/evener-hub/internal/hubcore/{keybindings_store,transcript_display_store,resumelocks}.go`.

Evidence: the TS count is `^export (async )?(function|const) \w*(ForTests?|ForTesting)` over non-test, non-`testing/` files; deadcode lists several Go ones unreachable. All are test-only by construction (some are used 100+ times).

Impact: production bundles carry test hooks; the big stores accrete reset/seam functions instead of exposing a resettable store factory. This is why `threads.ts` is 9k lines.

Fix plan: do not rewrite. Move each hook next to its store into a sibling `*.testSupport.ts` (the repo already uses `openInNewTab.testSupport.ts`, `detailLine.testFixture.ts`) when the hook only needs exported internals; where it needs module-private state, keep it but group it in one clearly named section per file. Enforce with a lint rule that forbids `ForTests` exports outside `*.testSupport.ts` once the existing ones are moved. Prioritize `threads.ts` because it interacts with the large-file finding owned by the frontend-stores slice.

Size: about 600 lines moved, no deletion. Risk: low; skip if Jesse considers this seam acceptable.

---

### x-dead-code-12: design-review mockup pages for shipped features (about 2,700 lines)

Severity: medium. Category: dead-code.

Locations: `cmd/evener-hub/frontend/mockups.html` (11 lines), `vite.mockups.config.ts`, `src/dev/mockups/mockups-entry.tsx` (1,216 lines) + `mockups.module.css` (189); `taskcardmockups.html` (12), `taskcardmockups.vite.config.ts`, `src/dev/taskcardmockups-entry.tsx` (297), `src/dev/taskcardmockups/{fixture.ts 389, variants.tsx 407, taskcardmockups.module.css 167}`, plus a CSS exception in `src/styles/token-contract.test.ts:504-585` that exists only for the task-card mockup stylesheet.

Evidence: `mockups-entry.tsx:1-5` describes itself as "Mockups for two proposed changes ... NOT product code and not a guard: this page exists so the design can be reviewed before anything ships" (remote-session Host picker and a "host->project" sidebar grouping); the Host picker now exists in `panes/spawn/startThread.ts` and the spawn CSS. `taskcardmockups-entry.tsx:1-12`: "Five variants ... Dev-support scaffolding for a design decision, not production code"; the task card rework shipped (commits #2211, #3301). No gate, script, Makefile target or doc references either page apart from their own configs and the token-contract exception; neither is a guard (compare `shellguard`, `spawnguard`, etc. in `package.json:17-24`). Last meaningful change to `mockups-entry.tsx` was 2026-10-12 (a mechanical widget migration, #4379), which means the migration cost is still being paid for pages nobody opens.

Impact: about 2.7k lines of TSX/CSS that must be migrated with every widget-library change and that carry a design-token exception.

Fix plan: confirm with Jesse that the two designs are final; if yes delete both HTML entries, both vite configs, the two `src/dev/` directories and the entry files, and the token-contract exception tests (`token-contract.test.ts:504-585`). If a mockup is wanted again, the design is recoverable from git history. The editorial-preview and the real `*guard` harnesses stay.

Size: about -2,730 lines. Risk: low.

---

### x-dead-code-13: dead and stale npm dependencies

Severity: low. Category: dead-code.

Locations: `cmd/evener-hub/frontend/package.json:55-56` `@ibm/plex-mono`, `@ibm/plex-sans`; `:59` `@types/dompurify`.

Evidence: `src/styles/global.css:1-35` loads `@fontsource-variable/{source-serif-4,inter,jetbrains-mono}`; the file header says "static Plex files of the pre-retheme system". `@ibm/plex-mono` is referenced nowhere outside `package.json` and the lock file. `@ibm/plex-sans` is referenced only by `scripts/editorial-preview.test.mjs:68,88-89`, which uses `IBMPlexSans-Text.woff2` as an arbitrary file to prove private copying (the same assertion works with any installed file). `dompurify` is pinned at `^3.4.16` (ships its own types) while `@types/dompurify` is `3.0.5`; I did not verify whether the stub is redundant, so treat that one as unconfirmed. For `mobile-native/package.json`, `expo-system-ui` and `react-native-worklets` have no source references but are probably plugin/peer requirements (not verified).

Impact: install size and licence-notice surface (`LICENSES/`), plus a misleading hint that Plex is still the UI font.

Fix plan: remove both Plex packages, change the editorial-preview test to use `@fontsource-variable/inter/files/inter-latin-wght-normal.woff2`, regenerate `package-lock.json` through `make test-web` preflight (never `npm ci` through a symlinked `node_modules`), drop any licence entries in `LICENSES/beautiful-ui.txt` if present.

Size: about -30 lines plus lock churn. Risk: low.

---

### x-dead-code-14: one-shot migration and measurement scripts still in the tree

Severity: low. Category: dead-code.

Locations: `scripts/sdk/rewrite-package-imports.mjs` (460 lines; its own header: "for SDK migration row A4 ... this script is the one-time rewrite, not a gate"), with `cmd/evener-hub/frontend/scripts/rewrite-package-imports.test.mjs`; `scripts/measure-transcript-read-model.sh` (86 lines, "acceptance measurements" for the 2026-09-25 transcript read model), `scripts/ops/measure-transcript-index-latency.sh` (111); `scripts/ops/seatbelt-smoke.sh` (62, "after building M6"); `mobile-native/scripts/{auth-harness-smoke,launch-settings-smoke,plugin-fixture}.mts`, `check-markdown-file-links.mts` (868 lines), `scripts/coverage/test-cost.sh` (154).

Evidence: only `docs/superpowers/` plans, other audit reports, or a self-referencing audit test mention them; no Makefile target, CI workflow or package.json script calls them. The transcript read model has landed (memory: 2026-09-28), and `make lint-package-imports` is the standing check for the import rewrite.

Impact: low; they look maintained and cost review time when module paths change. `test-cost.sh` and `seatbelt-smoke.sh` are plausibly still useful on demand; the migration ones are not.

Fix plan: delete `rewrite-package-imports.mjs` (+ its test) and the two transcript measurement scripts; for the rest ask Jesse whether to keep (they are documented, on-demand tools). Do not add selftest scripts.

Size: about -1,500 lines. Risk: low.

---

### x-dead-code-15: `appwire-client` index re-exports 294 names that no frontend or mobile file imports

Severity: low. Category: architecture.

Locations: `appwire-client/typescript/index.ts` (908 lines) and `state/*/index.ts`.

Evidence: of 1,073 names re-exported, 294 have zero occurrences in `cmd/evener-hub/frontend` or `mobile-native` (96 of them values, e.g. `applyTasksFetchResult`, `classifyTasksResponse`, `fromWireChange`, `fromWireConfig`, `foldWatchSummaries`, `formatSequence`, `watchTitle`, `threadUsageSummary`, `webFetchByteCount`, `normalizeSnapshot`, `isGenerationMismatch`, `cloneAndDeepFreezeJSON`). 14 of the 96 values are used nowhere except their own file. Caveat: the package is meant to be consumable by other clients; this counts consumers inside this repo only.

Impact: the shared client's public API is about 27% wider than its consumers need, and every public name is a contract that `publicExports.test.ts` and the package qualification scripts guard.

Fix plan: not a mechanical delete. Produce the list (script is easy to rerun), then un-export names that are internal derivations; the product guide says the shared client owns shared derivations, so keep any name that is a deliberate extension point and document it in the package README. Do this after finding 10.

Size: about -300 lines from `index.ts`, plus test updates. Risk: low to medium (semver-like contract of the package; discuss with Jesse).

---

### x-dead-code-16: test-support packages hold unused helpers

Severity: low. Category: dead-code.

Locations: `agent/internal/agenttest` (77 functions flagged, 515 lines; includes `clock.go:221 FakeClock.fireLocked`, `:232 notifyBlockersLocked`, `:245 removeWaiter`, `:259 resetWaiter` with zero test refs), `agent/sandbox/sandboxtest/sandboxtest.go:121 newHostTempRoot, :151 HostTemp.setenv, :199 hostTempBase` (0 test refs), `cmd/evener-hub/internal/hubtestenv/hubtestenv.go:211 canonicalizeExisting`, `agent/sandbox/contract.go:118-154 refuseTier, backendTag, modeCase, refuseCase, netTag` (0 test refs), `internal/apptranscript/apptranscript.go:1173 ItemTurnProjectionFromFile, :1224 ItemTurnProjectionFromEntries` (0 test refs), `appwire/boot_generation.go:73 parseBootGeneration`, `agent/eval.go:124 evalCollector.addUsage`, `agent/eval_probes.go:66 runOneProbe`.

Evidence: for these, production references are 1-7 but all of them sit inside other unreachable functions (deadcode is transitive), and test references are zero.

Impact: low; they are helper chains whose only callers are dead.

Fix plan: delete in the same pass as finding 1; the compiler decides. For `agent/sandbox/seatbelt.go` (44 unreachable functions under linux), do not delete: it is the macOS backend and deadcode on linux cannot judge it.

Size: about -250 lines. Risk: low.

---

## Recommended fix sequence

1. **Finding 2 (createShell)** first: it is the only item that gives false coverage. Own PR, CI test run.
2. **Finding 1 plus 16** package by package (agent, then cmd/evener-hub, then the rest): delete or demote wrappers, keep logic-bearing deletions in separate commits. Behavior-preserving; the compiler is the guard.
3. **Finding 3 (jobstore path readers)** and **finding 7 (sshconn legacy ladder)**: each its own PR, porting unique race/replacement assertions to the surviving implementation first.
4. **Finding 5 (frontmatter duplicate)**: small, includes fuzz wiring and the mutation patch.
5. **Findings 8, 9, 6**: quick deletions; one PR for hub, one for `cmd/evener` + `cmdutil`, one for zero-reference exports.
6. **Finding 12 (mockup pages)** and **13 (Plex deps)**: after Jesse confirms the designs are final; run `make test-web` preflight for the lock file.
7. **Findings 10 and 15**: delete aliases first, then shrink `index.ts` and update `publicExports.test.ts` in the same PR.
8. **Finding 4 (llm streaming API)**: needs a product decision on whether `llm` is an SDK; do not start before that.
9. **Findings 11 and 14**: discuss with Jesse; both are policy calls rather than clear deletions.

Docs to update when touching these: `docs/product/subsystems.md` (grep for deleted symbols), `docs/subagent-management/08-standalone-llm-calls.md` (finding 4), `docs/evener-hub-remote-operations.md` (finding 7), `docs/developing-evener/testing.md` (finding 11 if a lint rule is added).
