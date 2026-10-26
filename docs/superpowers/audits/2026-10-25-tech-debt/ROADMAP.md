# Tech-debt cleanup roadmap, 2026-10-25

Execution plan for the findings in [SYNTHESIS.md](SYNTHESIS.md) (791 verified findings in `verified/*.json`, 42 raw slice reports in `raw/`). Written against `origin/main` at `a6c0b9c561`. Nothing was run or edited to produce this plan; the few facts re-checked while writing are listed under "Facts re-checked" at the end. Finding ids are `slice-N`.

LOC figures are net lines of change (negative means removed), prod plus tests, and are estimates taken from the audit's counts. Where the audit gave no count, the figure is a rough order of magnitude and says so with `~`. Pure file moves count as 0 net and note the moved volume.

## Summary

| Wave | Roadmap entries | Expected PRs incl. splits | Net LOC (est.) | Can start now (no open decision) |
|---|---|---|---|---|
| 0: guardrails, defects, dead-code deletions | 27 | 28 | -12,100 | 27 entries, -12,100 (W0-01, W0-05 and W0-18 were gated on Q12, Q10 and Q17; all answered) |
| 1: consolidations | 69 | about 98 | -46,800 | 69 entries, about -46,800. Every gate is answered in [DECISIONS.md](DECISIONS.md); the Deps: fields still order the work. The answers shrank several entries (W1-27 dedupe only, W1-56 keep-and-fix, W1-60 and W1-62..W1-64 simplify-only) |
| 2: structural splits (design records in [designs/](designs/)) | 19 | about 45 | -9,300 (about 330,000 lines moved with no net change) | All 19: W2-09 is comment and dead-code trimming only (Q18); the other 18 follow their design records under Jesse's full-autonomy grant (DECISIONS.md, Wave 2). Each starts once its Wave 0/1 dependencies merge |
| Total | 115 | about 172 | about -68,200 | 115 entries, about -68,200; the Deps: fields still order the work |

Net LOC is production plus tests; markdown deleted under W1-58 (about 300,000 lines) and binaries under W0-14 (about 69 MB) are not counted. The sum is the arithmetic total of the per-PR estimates below, which come from the slice reports and verifier notes; SYNTHESIS section 1 estimated 25-35k production lines and 60-90k test lines removable, so this is consistent but not independently measured. Entries marked `~` with no audit count are rough.

Recommended starting set (all independent, all low risk): W0-02, W0-03, W0-04, W0-06, W0-07, W0-08, W0-09, W0-10, W0-11, W0-12, W0-13, W0-14..W0-16, then the defects W0-17..W0-27 in parallel lanes. They remove roughly 12,000 lines before any decision is needed and make every later PR smaller.

## How every PR is run

1. One worktree per PR, created off main: `git worktree add .worktrees/<id> -b debt/<id> origin/main` (slug is the lower-case PR id, for example `w0-04`). Keep unrelated changes out of it.
2. Behavior change or bug fix: red test first, watch it fail, then fix (TDD). Pure refactor: write a characterization test that pins current behavior first, keep it green across the change. Pure move: no logic edits in the same commit; prove it with `git diff --stat -M` and a before/after function-set diff, and a `git diff --name-only origin/main` check after any squash or rebase.
3. Tests run in CI, not on the workstation. Locally: build, `go vet` and `make lint` one at a time, and targeted `go test` on the touched package only when it is fast. No race loops, no full `make test`.
4. Frontend/native PRs: run Biome autofix from `cmd/evener-hub/frontend` on touched files, then `make test-web` from the repo root. Native PRs also `make test-native`. Import the shared client by name, never by relative path.
5. Never assert on copy or error text in new or touched tests (`docs/developing-evener/testing.md`, "Copy Is Not a Test Oracle"). Assert sentinel errors, wire codes, state.
6. Deleting production code that tests call: migrate those tests to the production path first or delete them with the code, and say in the PR which behavior test still covers each removed path. Coverage must not drop silently.
7. Update the evergreen docs in the same PR (`docs/product/subsystems.md` row, `docs/architecture.md`, the specific guide). No dated narrative. Remove narration comments only in files the PR already touches.
8. Before opening: `/simplify-code`. After: `/shepherd-pr`. Squash merge only on green CI plus clean RoboRev.

Each entry below lists: findings, files, steps, test strategy, docs, LOC, risk, gate (a decision in the last section) and dependencies. "Gate: none" means it can start now.

## Parallel lanes

Work in different lanes does not conflict, so several worktrees can run at once. Within a lane, merge in the listed order.

| Lane | Areas | PRs in order |
|---|---|---|
| shared-go-helpers | cross-module helpers: `execsupport`, `envvars/userdirs` | W1-01, W1-02, W1-03, W1-04 |
| agent-core | `agent/*.go` | W0-02, W0-03, W0-12, W0-17, W0-19, W1-15..W1-19, W1-21..W1-24, W1-66, W1-67, then W2-01, W2-02, W2-05, W2-06, W2-07 |
| agent-subpackages | `agent/internal/*`, `agent/plugin`, `agent/skill`, `agent/schema`, `agent/execenv` | W0-11, W0-13, W0-18, W0-20, W1-05..W1-08, then W2-03, W2-04, W2-08, W2-09 |
| llm | `llm/**` | W0-21, W1-25..W1-28, then W2-13 (registry part) |
| hub | `cmd/evener-hub/**`, `hubapi`, `cmd/evener-hub/internal/**`, `cmd/evener` | W0-06..W0-09, W0-24, W0-26, W1-14, W1-29..W1-36, W1-68, then W2-10, W2-11 |
| appwire-server | `server/`, `appwire/`, `internal/appserver`, `internal/appwirets` | W0-10, W0-23, W1-09..W1-13, W1-20, then W2-12 |
| ts-shared | `appwire-client/typescript` | W0-22, W0-25, W1-37..W1-42, then W2-14 |
| web | `cmd/evener-hub/frontend/src` | W0-05, W0-27, W1-44..W1-47, W1-53, then W2-15..W2-17 |
| native | `mobile-native`, `mobile/` | W1-43, W1-48, then W2-18 |
| tests-and-gates | test-only files, `internal/devtool`, `scripts/`, `Makefile` | W0-16, W1-49..W1-52, W1-54..W1-56, W1-69, then W2-19 |
| docs | `docs/**`, `AGENTS.md` | W0-01, W0-14, W0-15, W1-57..W1-59 |
| compat-gated | any; Q1 and Q15 are answered, so classify by version direction first (W1-59) | W1-60..W1-65 |

## Wave 0 status (2026-10-25)

All 27 entries merged, one PR each, plus fast-follows #5052 (W0-14) and #5059 (W0-15). Roughly -16,000 lines net, plus 69 MB of PNGs.

| Entry | PR | Entry | PR | Entry | PR |
|---|---|---|---|---|---|
| W0-01 | #5049 | W0-10 | #5042 | W0-19 | #5054 |
| W0-02 | #5039 | W0-11 | #5041 | W0-20 | #5058 |
| W0-03 | #5057 | W0-12 | #5064 | W0-21 | #5034 |
| W0-04 | #5063 | W0-13 | #5047 | W0-22 | #5036 |
| W0-05 | #5050 | W0-14 | #5035 | W0-23 | #5060 |
| W0-06 | #5040 | W0-15 | #5055 | W0-24 | #5061 |
| W0-07 | #5038 | W0-16 | #5062 | W0-25 | #5044 |
| W0-08 | #5043 | W0-17 | #5037 | W0-26 | #5048 |
| W0-09 | #5046 | W0-18 | #5051 | W0-27 | #5053 |

Where the PRs departed from the entries:

- Test-only helpers were moved into `_test.go` files or test packages instead of deleted when many tests call them (W0-02, W0-08, W0-10, W0-12). The production surface is gone; the helpers still exist in tests.
- Left for later, routed to Wave 1 entries:
  - agent root: the remaining `deadcode` entries reachable only through dead chains (eval, session queue and notes, subagent spawn paths, `memory_page`); `CloseResumability`, `inputPersistFailureBatch` and the reconstruction-wait barrier; `jobKindAgent` and the `_ = deps` in `registerJobToolsWithRegistrar`; the `nameSessionFromTextFunc` fold; the rest of the agent-jobs-7 list (`setParentJobID`, `feedJobOutput` and others); the `jobToolRegistrar` half of agent-session-tools-3. Goes to W1-21..W1-23.
  - agent sub-packages: `tool.ApplyPatch` test sites, `Registry.Unregister`/`Restrict`, the `truncate*` wrappers, `compileSchemaWith`, plugin manifest-probing dedup (agent-subpackages-9 steps 1, 2, 4). W1-08 takes the truncation wrappers; the rest goes to W1-07.
  - hub: `SetFs` on the SQL stores (needs another fault seam for `project_delete_test.go`), `Store.RemovedHosts()`, `preDispatchRefusalError` (hub-app-rest-5; Jesse approved deletion, goes to W1-32), `remoteFetchGeneration` and the nil-cache fallback in `remoteThreadFetch`, the test-only read wrappers, the optional-interface tiers (hub-root-rest-6), a duplicate-registration panic in `Router.Handle`, the `deploySelectedWith*` wrapper ladder, `cmd/evener-hub/transcript_limits.go`'s own max-line copy. Goes to W1-29..W1-35 and W1-06.
  - appwire: `appserver.Subscribe`, `Connection.Subscribe` and `Server.SetBeforeSubscriptionGate` (about 20 tests to re-seed through the real subscribe path), `SetStatus`. Goes to W1-20.
  - docs: a docs path-resolution gate (x-docs-4), not built.
- Process gaps, fixed in how Wave 1 runs: several admin merges landed while the branch was behind main (strict up-to-date is bypassed by `--admin`); W0-09 and W0-24 wrote the test after the change; workflow subagents have no subagent tool, so `/simplify-code`'s reviewer fan-out ran as a single self-review.

## Wave 0: guardrails, defects, and pure deletions of verified-dead code

Cheap, safe, mostly independent. Defects are small and red-test-first. Deletions follow the rule in SYNTHESIS T1: a production symbol with no production caller goes, along with the tests that exist only for it.

### Guardrails

### W0-01 Contributor rules in AGENTS.md
- Findings: T1, T2, T6, T7, T8 guardrails; x-docs-8 (typo `**Simplify-code*` at AGENTS.md:209, wrong `scripts/layoutguard` path at :125).
- Files: `AGENTS.md` only.
- Steps: add five one-line rules: (a) a production symbol needs a production caller; a test seam needs a behavior test that cannot reach the failure otherwise; (b) before writing a path, fsync, truncate or clone helper, grep for the shared one; (c) comments state the invariant, history/review rounds/issue ids/spec section numbers go in the commit message; (d) no test file or test name carries a sprint, round, pass or coverage-percentage label; (e) new compat code names its removal condition in the PR description. Fix the typo and the path. Do not trim existing text.
- Tests: none (docs). `make docs-check` if AGENTS.md is in its scope.
- Docs: that is the doc. There is no RoboRev instruction file in the repo (`git ls-files | grep -i roborev` finds only a test file), so rule (c) lives in AGENTS.md; if the reviewer reads instructions from elsewhere, Jesse adds it there.
- LOC: +30
- Risk: none. Gate: answered (DECISIONS.md, Q12: five-rule wording plus the typo and path fixes approved). Deps: none.
- Status: Wave 0 is executing.

### W0-02 Sweep `//nolint:unused` retentions, then enable `nolintlint`
- Findings: agent-delegates-2, x-compat-legacy-11, x-error-handling-slop-9, agent-session-tools-2, agent-session-features-3, agent-jobs-7 (pure duplicates: `liveWorkHandles` byte-identical to `liveShellHandles`, `abandonRunningJob`, `watchListToolResultForReceiver`, `aggregateWatchStatuses`, `LiveWatchesFor*`, `Session.liveWatchStatuses`).
- Files: the 15 non-test `nolint:unused` lines: `agent/jobs.go` (6), `agent/session_tools_jobs.go` (4), `agent/sandbox_delegate.go` (2), `agent/job_watch.go`, `agent/subagent_manager.go`, `internal/plugins/enabled.go`; plus `agent/session_launch...` wrappers named in agent-session-features-3 (`launchCompactionNamer`, `nameSessionFromCompactionTurn`, `appendColdAttentionResolution`, `readMemoryIndex`); `.golangci.yml`; the test files that call them.
- Steps: for each helper, grep every build tag first (x-compat-legacy-11 says some are kept for tagged harnesses; `go vet -tags evenerfuzz ./agent` and any other tag in use must still compile). Delete the helper and the tests that exist only for it; repoint tests that exercise real behavior at the production entry point. Then enable `nolintlint` in `.golangci.yml` with the options the pinned golangci-lint version supports (check `.tool-versions` and the linter's docs; do not guess option names) so a new `nolint:unused` needs an explanation.
- Tests: `go vet` on `./agent` with and without `-tags evenerfuzz`; CI runs the rest. Name in the PR the behavior test covering each repointed path.
- Docs: none unless `docs/architecture.md` names a deleted helper (grep).
- LOC: ~-450
- Risk: low. Gate: none. Deps: none; land before the `job_watch.go` split (W2-01).
- Status: Wave 0 is executing.

### W0-03 Move `createShell` into a test helper
- Findings: agent-jobs-6 (high), x-dead-code-2 (verifier-revised plan supersedes the "migrate 238 call sites" plan: it is a fixture helper that creates a job with no process, 197 test call sites).
- Files: `agent/jobs.go` (`createShell` at :991, `createShellOpts` at :623), new `agent/jobs_testhelpers_test.go`, `agent/job_shell.go` (~:594-634 commit path).
- Steps: move `createShell` and `createShellOpts` verbatim into a `_test.go` file (about 85 plus 10 lines moved, zero call-site churn). Extract the single `EventJobStarted` construction that both `jobs.go:1033-1049` and `job_shell.go:~614-634` build into one builder used by both (the one real drift risk). Do not convert tests to spawn real shells.
- Tests: existing job tests are the guard; add one table test asserting the helper and the production commit path emit the same `EventJobStarted` payload for the same inputs.
- Docs: none.
- LOC: ~-25 (about 95 moved)
- Risk: low. Gate: none. Deps: none; must land before the `job_watch.go` split (W2-01).
- Status: Wave 0 is executing.

### W0-04 Delete the root `frontmatter` package
- Findings: x-dead-code-5, agent-internal-tools-9, agent-misc-root-15 (first half).
- Files: `frontmatter/` (364 lines incl. tests, fuzz, `testdata`), `internal/bundled/bundled_test.go:9` (sole importer; uses `frontmatter.Parse` at :51), `testing-budget.json:96`, the two fuzz-script entries naming it, `go` build files if any list it.
- Steps: give `bundled_test.go` a small local parse helper (or import `agent/internal/frontmatter` if the module boundary allows; it may not, the package is under `agent/internal`). Delete the package, its budget row and the two fuzz-script entries.
- Tests: `go vet ./internal/bundled/...`; CI for the rest.
- Docs: grep `docs/` for `evener/frontmatter`.
- LOC: -350
- Risk: low. Gate: none. Deps: none; the memory frontmatter editor move in W2-03 comes after this.
- Status: Wave 0 is executing.

### W0-05 Delete design-mockup harnesses for shipped designs
- Findings: web-dev-and-tests-1, x-dead-code-12.
- Files: `cmd/evener-hub/frontend/{mockups.html,vite.mockups.config.ts,taskcardmockups.html,taskcardmockups.vite.config.ts}`, `src/dev/mockups/*`, `src/dev/taskcardmockups-entry.tsx`, `src/dev/taskcardmockups/*`, token-contract exception entry (`styles/token-contract.test.ts` ~:504-510) and its scoping test (~:580-585).
- Steps: confirm no `package.json`, Makefile or CI reference (audit grep found none). Delete all of it including the token-contract exception and its test.
- Tests: `make test-web` including token-contract test.
- Docs: `docs/developing-evener/design-system.md` if it points at the mockup pages (grep).
- LOC: -2,750
- Risk: low. Gate: answered (DECISIONS.md, Q10(a): the harnesses are final, delete them). Deps: none.
- Status: Wave 0 is executing.

### W0-06 Delete the superseded port-based sshconn restart/health ladder
- Findings: hub-sshconn-1 (high), x-dead-code-7, x-compat-legacy-4, hub-sshconn-9 (the dead `deploySelected` wrapper).
- Files: `cmd/evener-hub/internal/sshconn/version.go` (`detectSupervisor`:476, `detectSupervisorsFrom`:290, `pickSupervisor`:321, `waitHealthy`:1040, `waitForHealthyHub`:1071, `hubAnswerProvesReplacement`:1135, `probeListeners`:740, `hubListenerAddrs`:786, `listenerOwnsAddr`:819, `findHubPID`:860, `waitPortClear`:886, plus `hostAddr`, `hubPort`, `defaultHubAddr`), `deploy.go:1063` `deploySelected`, `sshconn.ListenerProbeRemote` export, `legacy_restart_test.go`, the tests that pin the other 30 functions.
- Steps: delete the 30 unreachable functions (342 lines, reproduced by the verifier with a name-based reachability pass) in one PR, rerun the pass until empty. Delete `legacy_restart_test.go`. Inline `ListenerProbeRemote`'s three test-file uses or keep one unexported probe string in a hub test helper and drop the export. For each deleted function's tests, name the surviving live-path test in the PR body. Seed `pendingRestarts` in tests through the live path.
- Tests: reachability pass output (before: 30, after: 0) pasted in the PR; CI for sshconn and `cmd/evener-hub` suites.
- Docs: `docs/evener-hub-remote-operations.md` or subsystems row if they describe the port ladder (grep).
- LOC: ~-1,200 (342 prod, remainder tests; test suite is 32.6k lines)
- Risk: medium (tests are rewritten around the live path). Gate: none. Deps: none; precedes W0-26 and W1-34.
- Status: Wave 0 is executing.

### W0-07 Delete dead code in the hub `main` package
- Findings: hub-root-rest-5, hub-root-rest-6, hub-app-rest-5, hub-app-rest-6, hub-app-rest-11.
- Files: `cmd/evener-hub/web_api_tree.go` (`memoTree`:223, `memoTreeWithAuthority`:228, `remoteTreeThreads`:667, `refreshRemoteThreads`:678, `navigationTreeInputs`, `rowRenameable`, `liveEntry`, `remoteFetchGeneration`), `web.go:85,195` `treeCache` and `hubcore.TreeCache` (+test), `spawn.go:435,556` `SpawnDaemon`/`ResumeDaemon` (zero production callers) and the `spawnDaemon->spawnDaemonWithProviderConfig` forward, `app_threadread.go:1676` `reconcileDelegateThreadItem` (body is `_ = rec; return item`) with `reconcileDelegateThreadItems`, `delegateJobIDFromRaw`, `isTerminalHistoricalJobStatus`, `app_session_resume.go:30-41,109-113,166-170` unreachable `preDispatchRefusalError` branch, `router.go:29-31` duplicate registration (`registerArchiveHandler` at `app_rpc.go:1224` and `web.go:250`; `registerSessionDeleteHandler(server, nil)` at `app_rpc.go:1226`), tests `app_threadread_gaps_test.go`, `app_threadread_test.go:512-746`, `cov_threadread_images_fuzz_test.go:140-152`.
- Steps: delete per the verified lists; for the double registration, remove the placeholder and the silent-overwrite reliance (one registration site). Re-run a whole-repo `git grep` per symbol before deleting.
- Tests: handler registration test asserting each method registered once with the live handler (behavior, not text); CI.
- Docs: `docs/product/subsystems.md` hub row if it mentions the tree cache.
- LOC: ~-900
- Risk: low to medium (registration change). Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-08 Delete dead surface in `hubapi`, `hubcore` and hostops
- Findings: hub-internal-small-13, hub-hubcore-7, hub-hostops-appsource-5, hub-hostops-appsource-7, hub-app-host-8, hub-hubcore-4 (production `SetFs` on SQL stores has no non-test caller).
- Files: `hubapi/client.go` (`NewClient`, `Client`, `HTTPError`, `URL`; no importer outside hubapi), `hubapi/attention.go` `StateWord`, `cmd/evener-hub/internal/hubcore/proxy.go` (+tests), `SendMaxRequestBytes` in `types.go`, `FavoriteCandidates`, `MarkSeen`/`MarkUnread` (migrate tests to `MarkBatch`), `TopLevelSessionIDs` if still test-only, `appsource/source.go:117-120` `CombinedItemReadSource` and `app_relay.go:1451-1457` branch, hostops store `RecordOrReplay` (`store.go:444`), `RemovedHosts()` (`retention.go:382`), `RevokeTokens`/`ConsumeToken`/`MintToken` (`token.go:522,377`), `CursorEpoch()` method (keep the type), `app_host_*` `writeHubTOMLHosts` chain and Add/Update/Remove adapters (`manage.go:2434`, `mutations.go:1172,1186`, `committedHostRow`), `SetFs` on SQL stores.
- Steps: one commit per cluster so the PR reads as five small deletions. Use `mustAdd`/`mustUpdate`/`mustRemove` test helpers for the host adapters (hub-app-host-8 plan). Rewrite the single test that uses `itemPackingRPCSource` to drive the real `ReadItemCandidates` path.
- Tests: compile-guided plus CI; name surviving behavior tests in the PR.
- Docs: `docs/architecture.md` if it lists the hubapi client.
- LOC: ~-1,300
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-09 Delete the remote capability probe nobody reads
- Findings: hub-hostops-appsource-1 (high).
- Files: `cmd/evener-hub/internal/hostops/...` and `appsource`: `HostCapabilities`, `CapabilitySource`, `remoteHubProbe` cache, `cachedProbe`/`cachedCapabilities`/`cachedCapabilityFeatures` (`remote_hub_refs.go:138`), clone helpers, `app_host_attach.go:~196-198` (the attach block discards the value) and `hostCapabilityProbeBudget`; `forwardedThreadCapabilitiesFor` reads `client.Features()` directly.
- Steps: check whether `SetHostFacts`/`SetHostHandshake` have other uses before touching them. Delete the whole probe (5+N wire reads per attach), not just its payload.
- Tests: attach test asserting the attach wire-read count no longer includes the probe reads (count of requests, not text); forwarded thread capabilities still come from `Features()`.
- Docs: the product doc row the verifier noted (search `docs/product/` for "capability probe").
- LOC: ~-400
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-10 Delete dead appwire server and appserver surface
- Findings: appwire-server-3, appwire-server-4, appwire-server-5, internal-app-layer-3.
- Files: `appwire/client.go` (48 exported methods with no caller outside client.go, including `SetPendingCoordinator`; `PendingCoordinator`), `server/server.go:229-293` `StatusInfo` (~15 fields and `ActionCapabilities` nothing writes or reads; `SetStatus`), `appReservedTurnID` (read at `bridge.go:191`, `appwire_runtime.go:646,2179,2220,2250,2483,2897`, cleared at `server.go:860`, `appwire_runtime.go:210`; no non-test writer), `internal/appserver`: package `Subscribe`/`ReplaceSubscriptions`, `Connection.Subscribe`/`ReplaceSubscriptions`, `Subscriptions.IsSubscribed`, `Subscriptions.Connections`, `Notifier.RetainedWindow`/`RetainedWindowCurrent`, the `Set...` test hooks.
- Steps: grep every symbol non-test across the repo (including `cmd/`, `tools/`); delete; keep the generated wire types untouched. If `appwire/client.go` wrappers are part of a documented public surface (docs/appwire), keep and journal rather than delete.
- Tests: compile plus the existing appserver and appwire suites; delete tests that only called the dead wrappers and list them.
- Docs: `docs/appwire*.md` method list if wrappers are documented; `docs/product/subsystems.md` appserver row.
- LOC: ~-1,000
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-11 Delete dead and test-only APIs in agent sub-packages
- Findings: agent-subpackages-6 (`skill.DiscoverSkills`/`LoadSkillBody`/`CatalogEntries`/`ExtractEmbeddedSkills`/`EmbeddedSkills`/`ResolveSkill` and the map-based `ResolveSkillContent`), agent-subpackages-9 (`plugin.LoadAll`, `LoadSettings`, `parsePluginHooks`, `discoverPluginHooks`), agent-internal-state-15 step 1 (atif `Convert`->`ConvertWithOptions`->`ConvertTranscriptWithOptions` forwarders at `atif.go:106-115`; keep one exported `Convert`), agent-internal-tools-3 (`executeCommandHook`, `Runner.Summary`, `RunSessionStart`, `NewManagerWithClock`, `transportForConfig`, `mergeEnv`; keep `ApplyPatch` unless the 45 test sites take a ctx mechanically), agent-internal-tools-5 (stale `(line N)` comments in `explain_covtest_test.go`).
- Files: `agent/skill/*`, `agent/plugin/*`, `agent/internal/atif/atif.go`, `agent/internal/hooks/*`, `agent/internal/mcp/*`, `agent/internal/tool/*`, their tests.
- Steps: repo-wide non-test grep per symbol before deletion; one commit per package. Do not touch atif cache aliases (agent-internal-state-15 says ask Jesse).
- Tests: the packages' own suites in CI; repoint tests to production entry points where they protect behavior.
- Docs: grep `docs/` for deleted symbols.
- LOC: ~-900
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-12 Delete production helpers only tests call (agent root)
- Findings: x-dead-code-1 (75 production functions only tests reference; spot-checked `jobWatchTool`, `noteDelegateAttention`, `nextIdleDelegateAttention`, `completionSnapshot`, `watchCount`, `sendInput`, `reconcileDelegateThreadItems`, `cloneNavigationSnapshot`, `localEnvPolicyFromName`, `writeAPIWireError`, `grepOutput`, `abandonRunningJob`, `createJobOutputForID`), agent-misc-root-7 (`agent/historical_jobs.go`: `LoadSessionHistoricalJobRecords`, `HistoricalJobRecord`), agent-misc-root-8 (`moveLegacyMemoryIndex`, `backUpMemoryIndex`, `removeMigratedMemoryIndex`, `migrateMemoryScope`, `migrateLegacyMemoryIndexes`, non-Ctx wrappers, `resolveLiveModelProfile`), agent-session-tools-3 step (worktree seam `sync.Map` keyed by `*Session` at `session_tools_worktree.go:39`).
- Files: `agent/*.go` per the list in `raw/x-dead-code.md` finding 1 (re-derive the list at PR start; the audit's "75" was not re-derived by the verifier), `agent/memory_migrate.go`, `agent/historical_jobs.go`, tests.
- Steps: regenerate the list with a name-based reachability pass; exclude `sendInput` (belongs to W1-22) and `CatalogNameOrName` (agent-misc-root-8 says keep). Split into two PRs if the diff passes ~800 lines (W0-12a jobs/delegates files, W0-12b memory/session/misc).
- Tests: as in W0-06: name the surviving behavior test per removed helper; keep the test-only invariants only where they pin real behavior.
- Docs: grep `docs/` for removed names (`docs/architecture.md:330,389,423` cite job_watch pieces).
- LOC: ~-1,500
- Risk: low to medium (count not re-derived). Gate: none. Deps: after W0-02 (shared files).
- Status: Wave 0 is executing.

### W0-13 Delete path-based job-output snapshot readers
- Findings: x-dead-code-3, agent-internal-state-7 (partial).
- Files: `agent/internal/jobstore/output_snapshot.go` (427 lines; `ReadOutputSnapshot`:63, `ReadOutputWindowSnapshot`:91, `ReadOutputPageSnapshot`:109, `readOutputSnapshotOnce/Attempt`, `readOutputRangeSnapshotOnce/Attempt`, `readOutputRawSnapshotWindow`, `readOutputSnapshotWindow`, retry wrappers), tests, stale comment `agent/retained_output_read.go:88-90`.
- Steps: delete only functions with no caller after a grep per function; keep `observeOutputSnapshot` (used by `output_snapshot_fd.go:242`), `readOutputMetaForSnapshot`, `outputSnapshotWindow`, `trimOutputSnapshotWindow`. Port the unique race and rotation cases from the path-based tests onto the fd-based readers before deleting them. Fix the stale comment.
- Tests: ported tests must fail if the fd reader loses a rotation case (mutation-check by temporarily breaking the guard in a scratch worktree, not locally on the shared box).
- Docs: none.
- LOC: ~-450 (about -250 prod)
- Risk: medium (race semantics). Gate: none. Deps: none.
- Status: Wave 0 is executing.

### Documentation and script cleanup

### W0-14 Delete unreferenced PNG evidence
- Findings: x-docs-2 (377 tracked images, 301 with no basename reference anywhere in tracked files, 69.46 MB). The verifier corrected the impact claim: history keeps the blobs, so clone size does not shrink; only worktrees do.
- Files: a small named script `scripts/docs/list-unreferenced-images.sh` (lists candidates by basename grep), then `docs/**/*.png` it prints.
- Steps: run the script, review the list, delete in one commit. State worktree savings, not clone savings, in the PR. Keep the script only if Jesse wants the ongoing check; otherwise run it from the scratchpad and do not commit it.
- Tests: `make docs-check` link validity.
- Docs: AGENTS.md/testing.md rule "PR screenshots go on the PR" folded into W0-01.
- LOC: 0 lines of code (about -69 MB of binaries)
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-15 Fix stale evergreen docs
- Findings: x-docs-4 (cites of non-existent `internal/appwire`, `internal/appsource`, `internal/hubapi`, `internal/launchconfig`, `agent/transcript.go`, `agent/mcp_config.go`; `naming.md:183` contradicts the lint config), x-docs-5 (`environment.md:14` `EVENER_ALLOWED_DECISIONS` contradicts `envvars.go:61`; `EVENER_OFFLINE`), x-docs-7 (finished `resume-model-context-fix-plan.md`, 304 lines; private runbook in the evergreen folder), agent-jobs-17, x-docs-3 narrow step (broken `web-ui/history` link; add `design/context.md`, `stop-cancellation-outbox.md`, `subagent-management/11` to `LIVING_IN_ARCHIVE` in `docs-site/scripts/check.mjs:11-35`), hub-root-rest-3 doc sentence at `docs/evener-hub.md:306` pointing at a nonexistent doc.
- Files: `docs/developing-evener/naming.md`, `docs/developing-evener/environment.md`, `docs/architecture.md`, `docs/job-control.md`, `docs/evener-hub.md`, `docs-site/scripts/check.mjs`, the finished-plan file.
- Steps: replace `file:line` anchors with symbol names; point stale paths at real ones (`appwire/`, `cmd/evener-hub/internal/appsource`, ...); make `environment.md` link to `envvars.go` as the source rather than copying semantics; delete the finished plan file.
- Tests: `make docs-check`.
- Docs: that is the change.
- LOC: ~-450
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-16 Remove the broken live-compaction shell harness
- Findings: build-gates-scripts-9.
- Files: `test/live-compaction-dense-value-test.sh`, `test/live-compaction-retention-eval.sh`, `scripts/lib/live-eval-isolation.sh` (the two scripts source `$SCRIPT_DIR/../scripts/live-eval-isolation.sh`, which does not exist; the lib lives under `scripts/lib/`), docs that mention them.
- Steps: confirm the Go eval suites cover the same scenarios (finding says they duplicate), then delete the two scripts and the lib if nothing else sources it (`live_eval_begin` is referenced only by those three).
- Tests: `make lint` (script lint) and `git grep live-eval-isolation`.
- Docs: grep `docs/` and Makefile help.
- LOC: ~-300
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### Small defect fixes (red test first)

### W0-17 Fork and aside fail on transcript lines over 10 MiB
- Findings: agent-misc-root-3 (concrete bug), agent-misc-root-10.
- Files: `agent/fork.go:99-104,128-134,191,211`, `agent/aside.go:49-54,58` (hard-coded `10*1024*1024`), `agent/transcript/transcript.go:32` (`DefaultMaxLineBytes = 128<<20`), `agent/transcript_read.go:16` (third copy `transcriptJSONLMaxLineBytes`).
- Steps: red test forking a transcript with a line over 10 MiB (fails with `ErrLineTooLong` today). Make fork and aside use `transcript.DefaultMaxLineBytes` (keep the `maxScanToken` test seam); delete the redundant `transcriptJSONLMaxLineBytes`; fold the three identical `forkSessionDeps` literals into one `defaultForkDeps()`.
- Tests: the new test, using a sparse in-memory fixture, not a 10 MiB file on disk if avoidable.
- Docs: none.
- LOC: ~-20
- Risk: low. Gate: none. Deps: none; precedes W1-06.
- Status: Wave 0 is executing.

### W0-18 `plugin.ResolveCommand` is nondeterministic on ambiguity
- Findings: agent-subpackages-8.
- Files: `agent/plugin/commands.go:126-137` (iterates a map, returns first unqualified suffix match), sole caller `agent/session_slash_command.go:59`; compare skill resolution which reports ambiguity.
- Steps: red test with two plugins defining the same unqualified command, run repeatedly, asserting a stable result. Mirror the skill package's ambiguity reporting (an ambiguity sentinel, caller surfaces it). This changes user-visible behavior from "random pick" to "ambiguity reported"; call it out in the PR for Jesse.
- Tests: test asserts the sentinel with `errors.Is`, not wording.
- Docs: slash-command section of `docs/` (grep "slash").
- LOC: +40
- Risk: low. Gate: answered (DECISIONS.md, Q17: report ambiguity with a sentinel error, as skills do). Deps: none.
- Status: Wave 0 is executing.

### W0-19 Explicit `10000` timeout is overridden
- Findings: agent-subpackages-12.
- Files: `agent/session_init.go:357` (overrides when value equals `10_000`, which `applyDefaults` sets at `session_config.go:967`), `agent/profile.go:258-265` (constant-true capability methods), `docs/job-control.md:385`.
- Steps: red test: explicit `10000` must stay `10000`. Move the 120_000 default into `applyDefaults`, delete the sentinel override, delete the constant-true Profile methods and inline their two branches.
- Tests: the red test plus existing config tests.
- Docs: `docs/job-control.md:385`.
- LOC: ~-50
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-20 Byte-slice truncation splits UTF-8 runes in model-facing text
- Findings: agent-internal-state-4, x-go-duplicate-functions-5, agent-misc-root-12 (first step).
- Files: `agent/internal/contextmgr/fork_summarize.go:104`, `context_manager.go:1092,1434,2462,2560,2614,2660`, `cmd/evener/run.go:574`, `cmd/evener-hub/app_search.go:263`, `agent/prompt_data.go:212`; route through the existing `agent/internal/runetrim.Cut`.
- Steps: one multibyte-straddling-the-cap test per site asserting `utf8.ValidString`; route the agent-module byte-slicing sites through `runetrim.Cut`. `cmd/evener` and `cmd/evener-hub` cannot import `agent/internal/runetrim` (Go internal-package rule), so those two sites use a rune-safe cut in an importable place (an existing shared package, or a local helper until W1-08 picks the shared home). Do not unify the ellipsis choice (`…` vs `...`); some strings are wire values (x-go-duplicate-functions-5).
- Tests: the straddling tests; CI.
- Docs: none.
- LOC: ~+60
- Risk: low. Gate: none. Deps: none; precedes W1-09.
- Status: Wave 0 is executing.

### W0-21 Vertex model listing ignores the listing timeout
- Findings: llm-core-providers-7 (first step only).
- Files: `llm/providers/google/vertex_models.go:110` (passes `llm.Request{Model: "*"}` without `AdapterTimeout`; anthropic, chatcompletions, responses and google protocol listings set it).
- Steps: red test: Vertex `ListModels` carries `ModelListingTimeout`; then fix.
- Tests: the new test with the scripted HTTP boundary.
- Docs: none.
- LOC: +25
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-22 Cache validators reject current delegate fields
- Findings: appwire-ts-core-2 (high, drift), x-go-ts-protocol-4 (TS half).
- Files: `appwire-client/typescript/src/reducer.ts:1749-1853` `validStableDelegate` keys array vs `EvenerDelegateInfo` in `types.gen.ts:590-660`; absent: `latestIntent`, `logicalOwnerSessionId`, `reportPreview`, `reportPreviewTruncated`.
- Steps: red test: a delegate with every generated field round-trips through the cache validators; add the four keys. This is the minimal fix; deriving the list from the generator is W1-14.
- Tests: the round-trip test (build the fixture from a generated field list so the next missing field fails it).
- Docs: none.
- LOC: +40
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-23 `appitempaging.cloneThreadItem` aliases pointers
- Findings: internal-app-layer-16.
- Files: `internal/appitempaging/page.go:209-224` (copies Position, Images shallow, OutputImages, Raw only), `appwire/clone.go:58` (also clones StartedAt, CompletedAt, DurationMS, ExitCode).
- Steps: red test for pointer aliasing through `RegroupTurnFragments`; export `appwire.CloneThreadItem`; delete `appitempaging.cloneThreadItem` and `cloneTurnWithoutItems`. Skip the cursor-codec unification (the verifier found that half weak).
- Tests: the aliasing test.
- Docs: none.
- LOC: ~-50
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-24 Settings page shows a hard-coded spawn timeout
- Findings: hub-root-rest-4 (first item only).
- Files: `cmd/evener-hub/web_settings.go:20-24` (`settingsSpawnTimeoutDisplay = "30s"`, claims no configurable timeout) vs `spawn.go:167,226` (`Cfg.SpawnTimeout`, `config.go:67`, default at 338 and 705).
- Steps: red test: with `Cfg.SpawnTimeout` set, the settings view model carries it (assert the value, not copy). Read the configured value. The dead config keys and repeated defaults named in the same finding are a separate cleanup if wanted.
- Tests: view-model test.
- Docs: none.
- LOC: ~+10
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-25 Duplicated TS constants
- Findings: x-client-duplication-2 step 1, x-go-ts-protocol-1 (smaller first step), x-compat-legacy-3 (six unreferenced alias consts).
- Files: `mobile-native/src/board/trayLine.ts:27` (`STUCK_AFTER_MS = 10 * 60_000`, identical to `appwire-client/typescript/src/sessionActivity.ts:11`, exported at `index.ts:577`), `appwire-client/typescript/src/client.ts:82` and `errors.ts:30` (`CODE_INVALID_REQUEST` twice), `jobOutput.ts:125` literal `-32014`, `frontend/src/stores/prefs.ts:237-265` six alias consts.
- Steps: import the package constant in `trayLine.ts`; delete the duplicate `CODE_INVALID_REQUEST`; name `-32014` once and bind it with the existing `goConstants` drift-test pattern; delete the six alias consts (no references outside their definitions; check `TurnSeparator.test.tsx` readers).
- Tests: `make test-web`, `make test-native`, `make test-api-package`.
- Docs: none.
- LOC: ~-40
- Risk: low. Gate: none. Deps: none.
- Status: Wave 0 is executing.

### W0-26 Hostreg registration comparison and sshconn sentinels
- Findings: hub-sshconn-6, hub-internal-small-12, hub-sshconn-11 (sentinel twins only).
- Files: `cmd/evener-hub/internal/hostreg/hostreg.go`, `sshconn/{selected_restart.go:189,ownership.go:212,226,ownership_darwin_remote.go:190,ownership_listener.go:25,deploy.go:1121,1240,1255}`, `sshconn/{preflight.go:424,manager.go:293}` alias twins (`ErrExecutableMissing`, `ErrControllerDirty`, ...).
- Steps: add `hostreg.SameRegistrationAndPresence` with a table test; replace the eight sshconn sites. Do not change `SameRegistration` semantics (hub-sshconn-6: its other callers do not carry the epoch; hub-internal-small-12 proposes the opposite and its own verifier found no latent fence hole, so this is a refactor, not a defect). Export the seven sentinels directly and delete the private twins (about -50). Leave `ErrSSHAuth` alone; fix its doc comment if it contradicts the code.
- Tests: hostreg table test; sshconn suites in CI.
- Docs: none.
- LOC: ~-90
- Risk: low. Gate: none. Deps: after W0-06.
- Status: Wave 0 is executing.

### W0-27 Delete the dead entry-transcript fixture route and dead test exports
- Findings: web-stores-2, web-stores-3 step 1.
- Files: `cmd/evener-hub/frontend/src/stores/threads.ts` (`installEntryTranscriptFixtureForTests`, `entryTranscriptFixtureForTests`, `EntryTranscriptFixtureOptionsForTests`; hits only threads.ts; the variable is never assigned outside the dead installer), `threadRoutingIndexesForTests`, `resetEntryWriteAdmissionForTests` (0 callers).
- Steps: delete the installer and the branches that consult the never-assigned variable; delete the two zero-caller exports.
- Tests: `make test-web`, `npm run typecheck`.
- Docs: none.
- LOC: ~-120
- Risk: low. Gate: none. Deps: none; do before the `threads.ts` split (W2-15).
- Status: Wave 0 is executing.

## Wave 1: high-value consolidations

Each consolidation names one canonical home, migrates callers, deletes the copies. Items that need a policy answer say which and cannot start until it is given. Within a theme, the PRs are ordered; across themes they are independent unless a dependency says otherwise.

### Theme A: shared Go helpers (SYNTHESIS T2)

### W1-01 Shared durable-write package and sync predicate, plus the fixed-name `.tmp` writers
- Findings: x-go-duplicate-functions-1 (partial; counts inflated: 22 non-test files have `Rename`), x-go-duplicate-functions-2, internal-misc-cmds-1 (partial), agent-misc-root-5, hub-internal-small-1, hub-internal-small-15, hub-app-host-9, x-error-handling-slop-12.
- Files: new package in the `execsupport` module (Q4; `execsupport/fsatomic` is the working name and `auth` takes the dependency; `execsupport/go.mod` today requires only `golang.org/x/sync` and stays free of `afero` because the package uses the real `os` package, per the G7 decision); `cmd/evener-hub/internal/fsdurability` becomes a thin import or is deleted; first callers: `rendezvous/rendezvous.go:96`, `agent/session_queue_persist.go:71`, `cmd/evener-migrate/convert.go:242`, `cmd/evener-hub/internal/launchconfig/io.go:53`, `hubedge/auth_token.go:105`. These use fixed names or no fsync, so they gain symlink-plant protection and crash safety (a real fix, as `llm/registry/write.go:219` documents the rationale).
- Steps: (1) characterization tests of the strongest existing writer (credentials store: `afero.TempFile`, fsync, rename; `hubcore/state_file.go:35 writeStateFileAtomic`: renamed/err contract and fault hooks) become the package's tests; (2) implement `Write(path, data, Options{Perm, SyncDir})` with durability always on and one `SyncUnsupported` predicate (ENOSYS/ENOTSUP/EINVAL); a plainly named variant for scratch/cache writes that intentionally skip fsync; (3) migrate the five fixed-name sites first, one commit each, with a test per site that a pre-planted symlink at the temp name is not followed and that the directory is fsynced.
- Tests: the behavior change per caller group is explicit (durability and symlink safety improve), so each migration commit carries its own red-then-green test; tests use `t.TempDir()` (G7: `afero` is phased out); inject faults through a narrow test hook only where a real filesystem cannot reproduce the failure, not `testing.Testing()`.
- Docs: `docs/architecture.md` module boundaries (if a new import edge appears); `docs/developing-evener/README.md` if it lists helper packages.
- LOC: ~+250 (package and tests +350, five callers -100)
- Risk: medium (durability change on hot state files). Gate: answered (DECISIONS.md, Q4: `execsupport`, durable default; G7: `afero` phased out). Before migrating a caller, trace its call frequency; a hot path that writes rebuildable state uses the explicitly named no-sync variant, and the PR says why (GAPS.md section 3, item 1). The `afero` question is decided (DECISIONS.md, G7): no FS interface. Deps: none; precedes W1-02, W1-03.

### W1-02 Migrate the hub CreateTemp writers to the shared writer
- Findings: hub-app-host-9, hub-hostops-appsource-10, hub-internal-small-1 (hub half), x-go-duplicate-functions-1.
- Files: `cmd/evener-hub/app_host_manage.go` (`writeHubTOMLHostsRecords` tail at ~:911-955 and `hubTOMLReplaceWithBytes` at ~:2907: same MkdirAll/CreateTemp/Chmod/Write/Sync/Close/Rename/dir-sync sequence), `hubcore/state_file.go:35` (`writeStateFileAtomic`), `hostops` `saveFS` and `writeQuarantineFile`, `fsdurability`, `hubcore/state_file.go:127` predicate, `app_host_manage.go:1054` predicate.
- Steps: replace each sequence with the W1-01 call; keep `recoveryStore` behavior per hub-hostops-appsource-10 (move `writeStateFileAtomic`'s renamed/err contract into the shared package so hostops callers can distinguish "rename happened"); delete the local predicates. Each migrated file drops its `afero` use and its tests move to `t.TempDir()` (G7).
- Tests: existing crash-recovery tests for host records and quarantine; add a test that the renamed/err contract survives (post-rename sync failure reports renamed=true).
- Docs: none.
- LOC: ~-250
- Risk: medium. Gate: answered (DECISIONS.md, Q4). Deps: W1-01.

### W1-03 Migrate the remaining writers and delete the other sync predicates
- Findings: x-go-duplicate-functions-1, -2, internal-misc-cmds-1, agent-misc-root-5, appwire-server-13, hub-app-host-9 (other-package copies), llm-registry-12, x-error-handling-slop-12.
- Files: `credentials/store.go:196-225` (add the missing parent-dir fsync), `internal/plugins/atomic.go:32`, `internal/transcriptindex/index.go:1294`, `llm/registry/refresh.go:48`, `llm/registry/write.go`, `server/thread_clear_journal.go:155`, `auth/openai/storage.go:229` (auth is a separate module and takes the `execsupport` dependency, per Q4), `agent/session_client_mutation_persist.go:423`, `agent/internal/worktree/sync_unix.go:36`, jobstore tolerance (decide with a test, agent-misc-root-5), `hubendpoint/runtime.go:196 syncDir`.
- Steps: three or four commits grouped by durability need: user state (credentials, plugins) first; then derived data where fsync is intentionally skipped (llm cache, transcriptindex) calling the plainly named no-fsync variant; then delete the seven non-test sync predicates. Keep selfupdate `stageExecutable` out (internal-misc-cmds-1 verifier scoped it out). Each migrated file drops its `afero` use and its tests move to `t.TempDir()` (G7); `afero` leaves `go.mod` after the last caller.
- Tests: per caller group, a test that parent-dir fsync happens where the group's contract requires it; characterization for the no-fsync groups.
- Docs: none.
- LOC: ~-500
- Risk: medium. Gate: answered (DECISIONS.md, Q4). Deps: W1-01.

### W1-04 One XDG state-root resolver
- Findings: agent-misc-root-4 (7 copies, 4 fallbacks), agent-subpackages-4, internal-misc-cmds-2, appwire-server-9 (state-root half), llm-registry-12.
- Files: `envvars/userdirs/userdirs.go` (add `StateRoot` beside `ConfigRoot`), callers: `rendezvous/rendezvous.go:42-60` (falls back to "."), `cmdutil/statedir.go:25-55` ("."), `auth/openai/storage.go:90-110` (`os.TempDir()`), `agent/runtime_dir.go:73-95` (`os.TempDir()`), `agent/doctor/statedir.go:30-37`, `appwire/frame_recorder.go:95-103` (relative path), `cmd/evener-migrate`, legacy migration helpers, skill/plugin cmdutil helpers.
- Steps: add `userdirs.StateRoot` with a table test. Q5 is answered (DECISIONS.md): it returns a clear error when `HOME` and `XDG_STATE_HOME` are unset, with no `os.TempDir()` or `"."` fallback. Port the pure duplicates first; each caller then surfaces the error, and the per-caller fallbacks are deleted.
- Tests: one table test (XDG set, XDG unset with HOME, both unset returns the error); one test per caller that the error reaches the user.
- Docs: `docs/developing-evener/environment.md`.
- LOC: ~-120
- Risk: low to medium (HOME-less callers now fail instead of writing to a temp or relative directory). Gate: answered (DECISIONS.md, Q5). Deps: none.

### W1-05 One session on-disk layout helper
- Findings: agent-misc-root-11 (29 hand-built `.transcript.jsonl` sites; ~16 in agent, ~12 outside), agent-subpackages-2 (`sessionsSubdir` declared in both `agent/session.go:2775` and `agent/schema/snapshot.go:343`; doctor uses bare `"sessions"`), hub-root-rest-20, agent-internal-state-13.
- Files: `agent/schema` (add `TranscriptPath`, `MetaPath`, `APILogPath` and the artifact suffix list; Q: `agent/schema` mixes wire types and a locking engine per agent-subpackages-14, which is deferred; the helper is pure and small so it does not depend on that decision), call sites e.g. `agent/transcript_lookup.go:437-438`, `fork.go:198,294`, `session.go:2783`, `session_init.go:601,990,1403`, `delegate_tree_start.go:160,349,651`, hub `image_serve.go:60`, `app_threadread.go:1491`, `project_delete.go:668`, `agent/doctor/locate.go:169,218`.
- Steps: add helpers with a table test; migrate in-package sites first (one commit), then outside sites (second commit). Replace the two `sessionsSubdir` consts with one.
- Tests: table test of path construction plus a test that every artifact suffix listed there is the set the delete/fork code iterates (single source).
- Docs: `docs/architecture.md` if it describes the layout.
- LOC: ~-100
- Risk: low. Gate: none. Deps: none.

### W1-06 One whole-transcript read helper that takes its limits
- Findings: agent-misc-root-3 (rest), agent-subpackages-3, internal-app-layer-9 (safe half: replace the second failure counter with `transcript.FailureCounter` via a narrow-decode observe entry).
- Files: `agent/transcript`, readers in `agent/fork.go`, `aside.go`, `transcript_read.go`, `doctor`, `internal/apptranscript/apptranscript.go:1359-1370`, `internal/appserver`/hub read paths (13 non-test files use `ReadLine`/`DecodeHeader`).
- Steps: add `ReadTranscript(header, entries, limits)` where limits and error policy are parameters (the loops differ on limits and errors per agent-misc-root-3); port callers where two or more port cleanly; leave indexers and doctor on their own loop if they do not.
- Tests: per-caller characterization of limit and error behavior before the port; one test for torn tail, one for over-limit line, one for unterminated tail.
- Docs: `docs/product/subsystems.md` transcript-read-model row only if ownership text changes.
- LOC: ~-250
- Risk: medium. Gate: none. Deps: W0-17, W1-05.

### W1-07 Export git pointer parsing, `ResolveClean` and the path-containment helper
- Findings: agent-execenv-sandbox-9, x-go-duplicate-functions-7, x-go-duplicate-functions-12, x-go-duplicate-functions-6, agent-execenv-sandbox-7, agent-session-tools-10 (in-file merge), agent-execenv-sandbox-8.
- Files: `identifier/git.go` (`resolveClean`:85, `pointerTarget`:127, `isSubmoduleGitDirShape`:180), `agent/execenv/project_resolver.go` (`pointerTarget`:207, `isSubmoduleGitDirShape`:218, `pathContains`:302), `agent/execenv/gitpath.go` (`resolveClean`:275, dead `mainRootFromGitdirPointer`:247 and `parseGitdirPointer`:255), `agent/sandbox/resolve.go:293`, `sandbox/gitdir.go:627`, `execenv/securepath.go:497`, `session_tools_worktree.go:1180,1685` (`pathEqualOrUnder` equals `pathWithin`).
- Steps: two commits. (1) Export `identifier.ResolveClean`, the validators and `pointerTarget`; delete the execenv copies and the two dead gitpath wrappers (zero non-test callers). Leave `sandbox.ClassifyWorkspace` policy local. (2) One `Within(root, path)` in a package every user already imports; a characterization table first (equal, child, sibling-prefix, `..`, root `/`, symlink-resolved); migrate security-relevant sandbox/execenv users first. The only intended behavior change is the root `/` fix at `local.go:3491`; give it its own red test. Merge `pathEqualOrUnder` into `pathWithin` inside the worktree file.
- Tests: the edge table runs against every old variant first to record which one wins and why, then against the new function (argument order differs among the 13 copies: that is the trap).
- Docs: none.
- LOC: ~-300
- Risk: medium (security-adjacent). Gate: none. Deps: none.

### W1-08 Consolidate truncation and whitespace collapse
- Findings: x-go-duplicate-functions-5, x-go-duplicate-functions-14, agent-misc-root-12, agent-subpackages-5, agent-internal-tools-13, agent-internal-tools-19.
- Files: `agent/internal/runetrim` (home), `agent/jobs_activity.go:109 truncateActivityText` (pure pass-through, delete), `appwire` `truncateRosterText` (`delegate_roster.go:31-48`, same walk-to-cap as `activitybound.Truncate`), `oneLine` variants including the "mirrors doctor.oneLine" comment at `agent/transcript_render.go:1975` and the "keep in step with the hub's helper" comment at `apptranscript.go:409`.
- Steps: replace the roster copy with `activitybound.Truncate` (appwire may import it if the module layout allows; check with `go list`); delete the pass-through; collapse only copies that are byte-identical in behavior. Do not unify ellipsis choice across wire values.
- Tests: characterization table per helper recording ellipsis and rune behavior.
- Docs: none.
- LOC: ~-150
- Risk: low. Gate: none. Deps: W0-20.

### W1-09 Export shared pointer/JSON clone helpers
- Findings: hub-app-rest-16, internal-app-layer-14 stage 1, x-go-duplicate-functions-10, x-go-ts-protocol-6, appwire-server-9 (clone half), agent-internal-tools-12, hub-internal-small-4.
- Files: `appwire/clone.go:125` (existing unexported generic `clonePointer[T any]`), the `cloneServerBool/Int64` (`server/appwire_runtime.go:2858,2866`), `cloneServeBool/Int64` (`cmd/evener/serve.go:2347,2355`), `cloneHubBool/Int64` (`app_threadread.go:1309,1317`), `cloneBoolPointer/Int64Pointer` (appprojector), the projector clone copies.
- Steps: export `appwire.ClonePointer`, `appwire.CloneDelegateInfo`, `appwire.CloneQueueState`; delete the copies; point jobstore at it only if its module allows.
- Tests: aliasing test (mutating a clone does not change the source) for each exported clone.
- Docs: none.
- LOC: ~-200
- Risk: low. Gate: none. Deps: W0-23. Precedes W1-10.

### W1-10 Delegate status: stop dropping `LatestIntent` and alias the server type
- Findings: agent-misc-root-2 (high), appwire-server-2 (high), x-go-ts-protocol-4 (high), x-go-ts-protocol-5, internal-misc-cmds-12, internal-app-layer-14.
- Files: `server/server.go:105` (`DelegateStatusInfo`, 50 fields) vs `agent/status.go:124` (51 json tags; `LatestIntent` at :158, populated at :517/:588) and `events.DelegateUpdatedData` (52, `payloads.go:786`) and `appwire.EvenerDelegateInfo` (54, `LatestIntent` at `types.go:1839`); converters `cmd/evener/serve.go:2232-2345` (`agentToServerDetailedStatus`, delegate literal 2293-2316), `server/appwire_runtime.go:2827-2850`, `cmd/evener-hub/app_threadread.go:1278-1306`, `server/server.go:204` `MarshalJSON` plugin nil-vs-empty workaround.
- Steps: (1) red test: a `DetailedStatus` carrying `LatestIntent` reaches the appwire delegate on all three paths (fails today on two). `latest_intent` flows on the detailed-status path (additive; Q17 answered in DECISIONS.md). (2) Make `server.DelegateStatusInfo` a type alias of `agent.DelegateStatusInfo` (server already imports agent); delete the `serve.go` copy loop and its `cloneServe*` helpers; pin the plugin nil-vs-empty behavior with a test before deleting `MarshalJSON`.
- Tests: the round-trip test over every field (built by reflection over `agent.DelegateStatusInfo` so a new field fails it).
- Docs: `docs/product/subsystems.md` delegate row; protocol docs regenerate (`make` generated-output freshness) if wire description changes.
- LOC: ~-450
- Risk: medium (wire-visible, additive). Gate: answered (DECISIONS.md, Q17). Deps: W1-09.

### W1-11 Delegate status: one exported mapper to the wire
- Findings: agent-misc-root-2 step 2, appwire-server-2, internal-app-layer-14 stage 2, x-go-ts-protocol-4.
- Files: new method on `agent.DelegateStatusInfo` returning `appwire.EvenerDelegateInfo` (deep-copies pointers once), replacing `appDelegateFromDetailedStatus` (`server/appwire_runtime.go:2827`) and `appwireDelegateFromAgentStatus` (`cmd/evener-hub/app_threadread.go:1278`); the projector mapping `appwireDelegateInfo` (`appprojector`:466) if the structs can be unified (internal-app-layer-14 says only after the agent and events status structs converge; otherwise leave the projector and share its clones).
- Steps: one mapper; delete the two hand converters; keep the projector if structs differ.
- Tests: field-by-field reflection test (mapper output covers every `EvenerDelegateInfo` field that has a source).
- Docs: none.
- LOC: ~-300
- Risk: medium. Gate: none. Deps: W1-10.

### W1-12 Generator emits more of the Go-owned constants
- Findings: x-go-ts-protocol-2 (staged), x-go-ts-protocol-1, x-go-ts-protocol-8, x-go-ts-protocol-10, x-go-ts-protocol-11 step 1, x-go-ts-protocol-3.
- Files: `internal/appwirets/emit.go:36-37,304-313,625-639` (only `threadItemEventKindType` and `navigationTargetKindType` handled; three hand-wired enums), `appwire/types.go`, `appwire/errors.go` (45 `ErrorInfo` consts; `hostOps.ts:49-66` lists 15 kinds as a union), `appwire/errors_test.go:274-300` (regexes `RETRY_SAFE_MUTATION_METHODS` out of `dispatcher.ts`), `transcriptDisplayConfig.ts:23-25,59-60,233-245` vs `transcript_display.go:15 TranscriptDisplayShippedDefaults()`, generated `types.gen.ts`.
- Steps: start with `TaskStatus` and the transcript-display enums (concrete duplicated sets) to prove the registry approach; do not migrate all 20 string-typed enums (YAGNI). Bind shipped defaults to the Go function output through a generated constant. For limit pairs (`MAX_ATTACHMENTS` etc.) add `goConstants`-style binding tests rather than emitter work; skip protocol-version generation (deliberate gates). Replace the Go test that regex-scrapes `dispatcher.ts` with a generated constant.
- Tests: the existing generated-output freshness gate in `make lint`; TS test that fails when Go adds a status value.
- Docs: `docs/appwire*` generated docs regenerate; `docs/developing-evener/README.md` generator note.
- LOC: ~+100 (generator +250, hand copies -150)
- Risk: medium (generator change). Gate: none. Deps: none.

### W1-13 Cache validators take their key lists from the generated types
- Findings: appwire-ts-core-2 (structural half), appwire-ts-state-testing-8.
- Files: `appwire-client/typescript/src/reducer.ts:1395,1570-2020` (`ITEM_MODEL_KEYS`, `valid*` functions), `types.gen.ts`, `internal/appwirets/emit.go`.
- Steps: emit per-type key arrays (or a keyed const) from the generator and use them in the validators; delete the hand-listed arrays.
- Tests: W0-22's round-trip test stays; add a test that validators accept every generated field.
- Docs: none.
- LOC: ~-100
- Risk: medium. Gate: none. Deps: W0-22, W1-12.

### Theme B: agent core skeleton dedupe (SYNTHESIS T4 step 2)

### W1-14 Share the bearer-header parse between `httpguard` and `hubedge` (optional)
- Findings: hub-internal-small-18, x-go-duplicate-functions-18.
- Files: `httpsec/httpguard.go:70 HubTokenAuthorized` (empty expected authorizes everything) and `hubedge/auth_token.go:121 tokenFromRequest`, `:132 tokensEqual` (empty never equal).
- Steps: the verifier found the opposite empty-token semantics intentional per guard (hubedge also accepts a cookie; `AuthGuard` treats an empty token as disabled). Share only the bearer-header parse and the constant-time compare, with a table test pinning each guard's current empty-token result first. If the saving is under about 25 lines, skip the PR.
- Tests: table of (expected, presented, empty combinations) at both entry points.
- Docs: none.
- LOC: ~-30
- Risk: low. Gate: none. Deps: none.

### W1-15 Sleep and backoff helpers
- Findings: x-go-duplicate-functions-3, x-go-duplicate-functions-4, agent-session-features-1 (trivial half).
- Files: `durabilityRetryBackoff` and `shellFinalizeBackoff` (byte-identical), sshconn `nextBackoff` (saturating), `agent/job_shell.go:720`, `delegate_delivery.go:1047`; a single `backoffDelay(attempt, initial, max)`.
- Steps: dedupe the two identical agent functions; leave `relayRetryBackoff` (stateful `Next()/Reset()` with a zero-start rule) and lock-retry alone (x-go-duplicate-functions-3).
- Tests: table test for the helper including saturation.
- Docs: none.
- LOC: ~-30
- Risk: low. Gate: none. Deps: none.

### W1-16 One generation-guarded retry timer for the seven backoff rails
- Findings: agent-session-features-1 (high).
- Files: `agent/session_attention.go:1400-1510`, `session.go:1132,1386`, `session_active_turn.go:171,331`, `delegate_delivery.go:1032-1131`, `session_fail_closed.go:133`, `job_shell.go:720`; 16 non-test `Retry.generation++` sites across six timer fields; `min(delay*2, ...)` at seven sites.
- Steps: introduce one small timer type owning generation, arm, cancel, fire, with the stable rail's fire-time doubling as an explicit option (the verifier says do not normalize it away). Migrate one rail per commit, each with a test of the reset-while-armed case before the move.
- Tests: characterization per rail (arm, reset while armed, stale generation fire ignored); fake clock from `sclock`.
- Docs: none.
- LOC: ~-250
- Risk: medium (timers; races). Gate: none. Deps: none.

### W1-17 Derive event kind from the payload
- Findings: agent-subpackages-11.
- Files: `agent/events` (`events.New(data)`, add exported `events.KindOf`), `agent/session_events.go:231 Session.emit(kind, data)` (about 250 emit sites), 12 dead `case` pointer arms, dead `ToStreamEvent` and its test.
- Steps: add `KindOf`; mechanical rewrite of `emit` call sites in a codemod (single commit, `git diff --stat` shows only call-site lines); keep `enrichDiagnosticData` and the `EventWarning` special case; delete the pointer arms and `ToStreamEvent`. Do not claim the `llm` import disappears.
- Tests: a table test that every event payload type maps to exactly one kind; existing event tests.
- Docs: none.
- LOC: ~-150
- Risk: medium (wide mechanical diff, so land alone). Gate: none. Deps: none.

### W1-18 `NewSession` and `Restore` share their identical blocks
- Findings: agent-session-core-1 (high).
- Files: `agent/session_init.go` (`NewSession` :298-685, `RestoreSessionFromMetaWithConfig` :852-1628): identical delegation-allowance / tree-counter blocks (:375-390 vs :1100-1115), 13 matching `jm.*` assignments, three `closeXOnError` flag pairs (:485/505/542 vs :1208/1223/1374).
- Steps: extract only the verified-identical blocks into builder methods. The `nil`-then-`jm.enqueue`-at-:1231 difference is deliberate ordering around `loadQueues` (:1244); keep it. No merge into one super-constructor.
- Tests: construct via both entry points and compare resulting field state with a differential test (excluding live pointers), guarding both orderings.
- Docs: none.
- LOC: ~-200
- Risk: medium-high (core lifecycle). Gate: none. Deps: after W0-19 (same file).

### W1-19 Client-mutation handler skeleton
- Findings: agent-session-core-3 (high), agent-session-core-4 (shared claim/announce/give-back only).
- Files: `agent/queue.go` (`clientMutationQueue`:112, `clientMutationSteer`:742, five `Accept*` wrappers at :566,861,973,1103, ...), `agent/mutation.go:745` (`ProcessClientMutationStart`), `queue.go:249` (`ProcessPendingUserInput`).
- Steps: one skeleton covering lease, normalize, ensureStore, newRequest, executeAtomic, fence-reject, marshal, rejected-mapping, with the replay branch as a parameter (the only real difference). Delete the pass-through `Accept*` wrappers. Convert one handler per commit; leave the three claim drivers' give-back rules distinct (they legitimately differ).
- Tests: per-handler characterization of error codes and replay results before conversion.
- Docs: none.
- LOC: ~-300
- Risk: medium-high (durable input). Gate: none. Deps: none.

### W1-20 Retry-safe turn handlers and activity list handlers in `server`
- Findings: appwire-server-1, appwire-server-7 (safe parts), agent-session-features-4.
- Files: `server/appwire_runtime.go:1592-1830` (seven retry-safe handlers repeat trim, `lockRetrySafeMutation`, RLock-copy, `Unavailable`, `refreshFacets`, `NormalizeClientMutationError`; `ClientMutationID` trimmed 10 times), four session-activity handlers (`appwire_session_*`), `agent/session_activity_jobs.go:17,33`, `watches.go:12,28`, `delegates.go:37,53` (six near-identical `ListActivity*/LoadSessionActivity*` pairs).
- Steps: characterization tests per handler on error codes and `EvenerErrorInfo`; one `retrySafeMutation` helper; one generic `sessionActivityHandler[P,R]` and `withActivityList[R]` (keep `normalizeSessionActivity`'s id return); give `JobsListResponse.Data` a real type and regenerate; pin one unwired-hook policy. The generic `hook[T]` and the 50-method interface shrink are deferred (appwire-server-7 verifier).
- Tests: as above; drift in the seven handlers is itself the bug the characterization tests will expose; fix any discovered difference test-first.
- Docs: none.
- LOC: ~-350
- Risk: medium. Gate: none. Deps: none.

### W1-21 Migrate tests off the legacy in-process spawn API
- Findings: agent-delegates-1 (high), part 1.
- Files: 32 test files that call `spawnAgent(` or `prepareSubagentRun(` (the verifier recounted 32, not 22); target harnesses `delegateRuntime.create` / `prepareStableDelegateRun`.
- Steps: convert the tests that protect real behavior to the stable-controller harness; delete tests that only exercise the legacy path (list them in the PR with the surviving test per behavior). Several PRs if the diff passes ~800 lines (split by test file group).
- Tests: it is the tests. Compare per-file test names before and after; any dropped test needs a stated reason.
- Docs: none.
- LOC: ~-1,000 (tests)
- Risk: medium. Gate: none. Deps: none; precedes W1-22.

### W1-22 Delete the legacy in-process spawn API
- Findings: agent-delegates-1 (high), part 2; agent-delegates-4 prerequisites; x-dead-code-1 (`sendInput`).
- Files: `agent/subagents.go` (`spawnAgent`, `sendInput`, `cancelAgent`, `startOrSteerSubagentRun`, `trackAndLaunchPreparedSubagent`, `prepareSubagentRun`, `prepareSubagentRunWithModelSelection`, `releasePreparedTreeSlot`, the `subagentAfterPrepare`/`subagentReserveSlot`/`subagentReserveTreeSlot` hooks, and the `!preparedEnv.stableController` gates at `subagents.go:1231,1255`; `delegate_runtime.go:2662` is the only place `stableController` is set).
- Steps: split into three PRs that each compile on their own: (a) delete `sendInput`, `cancelAgent` and the `subagentAfterPrepare`/`subagentReserveSlot`/`subagentReserveTreeSlot` hooks; (b) delete `spawnAgent`, `startOrSteerSubagentRun`, `trackAndLaunchPreparedSubagent`, `prepareSubagentRun*` and `releasePreparedTreeSlot`; (c) remove the now-constant-true `stableController` branches and the flag. Before (a), add a delegate-lifecycle behavior test that drives the controller path, so tests move before code is deleted.
- Tests: compile plus the converted suite from W1-21.
- Docs: `docs/product/subsystems.md` delegates row if it names the legacy path; `docs/architecture.md:330,389,423` anchors.
- LOC: ~-2,500 (the verifier says the surface is larger than reported; re-measure at PR start)
- Risk: medium. Gate: none. Deps: W1-21.

### W1-23 Delete the unused public `Session` queue API
- Findings: agent-session-core-5.
- Files: `Session.Enqueue`, `EnqueueWithImages`, `SteerFromUser(+WithImages)`, `QueuePreview/IDs/Texts`, `SteeringQueueSnapshot`, `WithQueuedInputDrainOnInterrupt`, `DroppedModelFall...` in `agent/queue.go` and neighbors, plus tests.
- Steps: repo-wide non-test grep per symbol; migrate real-behavior tests onto the production entry (`ProcessPendingUserInput` / client mutation path); delete the rest. `FollowUp` is a separate PR (W1-67).
- Tests: as W1-21.
- Docs: none.
- LOC: ~-800
- Risk: medium. Gate: none. Deps: none.

### W1-24 One "untrack from stop-state and signal" helper in the delegate controller
- Findings: agent-delegates-4.
- Files: `agent/delegate_delivery.go:321,333,371`, `delegate_tree_work.go:77,96`, `delegate_tree_steer.go:295,305`, `delegate_tree_stop.go` (14 `if _, tracked := c.stop.` sites; 20 `signalStopProgressLocked` call sites).
- Steps: add `untrackStopLocked[K comparable, V any](c *delegateTreeController, pick func(*delegateStopState) map[K]V, key K)` doing nil-check, check-and-delete, signal; convert the 14 sites one commit each. Leave the claim protocol alone (SYNTHESIS section 6).
- Tests: a table test per stop-state map for track/untrack/signal; existing controller tests.
- Docs: none.
- LOC: ~-120
- Risk: low to medium. Gate: none. Deps: none.

### Theme C: llm

### W1-25 Delete private dead code in `llm` (public SDK surface stays)
- Findings: llm-core-providers-3, llm-core-providers-7 (second half), llm-core-providers-4 (exported helpers with only test/doc callers: `MergeHeaders` at `headers.go:34`, `IsImageFile`, `OrderedEffortLevels`, `(*APIAttempt).SetWireRequestMetadata`, `ErrorHint`, `AdapterTransport`, `BuildAPILogCredentialMaterial`, pass-through `clampEffort` at `anthropic/response.go:246`), llm-core-providers-19 (continuation scaffolding: `ProductionPathProven`, `StorageShapeProofID`, `ProductionPathProofID`, `ProviderOptionKeysByContinuation` never read in production; only `StorageShapeProven` is read, at `agent/session_model_call.go:610`; package-level `PlanResponsesContinuation` at `responses_continuation.go:161` and `ClearResponsesContinuationStoreOverride` have only test callers).
- Files: `llm/` `parseSSEWithTimeout`, `sseActivityReader`, `sseReadTimer`, `WithStreamReadTimeout`, `sseOptions`/`SSEOption`, `StreamReadSSEOptions` (called only from `core_contracts_fuzz_test.go`, `client_config_edges_fuzz_test.go`), `StreamRunner.SSEOpts` (`runner.go:50`, never assigned), the `*sseReadTimeoutError` case; `llm.Protocol.PrunablePaths` interface method, four `prunablePaths` tables and their `TestPrunablePathsMatc...` tests.
- Steps: Q3 (DECISIONS.md): `llm` is a public SDK, so an exported symbol is not dead just because nothing in this repo calls it. Keep every exported API and its behavior, including `ParseSSE`, `SSEOption`, `WithStreamReadTimeout`, `StreamReadSSEOptions` and the exported helpers listed under llm-core-providers-4 and -19; `llm/doc.go` exposes `ParseSSE` for custom adapters. Delete only unexported dead code (for example a never-assigned unexported field, or unexported tables with no reader), after a repo-wide grep. Removing any exported symbol is a separate compatibility decision for Jesse.
- Tests: provider difftests (`providers/difftest`) unchanged; delete tests that only pin removed code.
- Docs: grep `docs/` for `SSEOpts`.
- LOC: ~-150 (unexported code only)
- Risk: low. Gate: none. Deps: none.

### W1-26 Provider stream decoders share their finish and credential-material code
- Findings: llm-core-providers-5 (high), llm-core-providers-11.
- Files: `llm/providers/anthropic/stream.go:410-418`, `chatcompletions/stream.go:162-172`, `responses/stream.go:513-521`, `google/stream.go:200-208`, `llm/runner.go`; `llm/api_attempt.go:449 mergeAPILogCredentialMaterial` and `llm/transport/http_attempts.go:215 mergeTransportCredentialMaterial` (line-for-line identical), `protocolhttp/call.go:157 credentialMaterial`.
- Steps: add `StreamRunner.Finish(resp llm.Response)` owning the `attempt.Active()`/`Send` branch, set Finished and cancel; unify the decoder parameter list to `(sctx, cancel, resp, s, req, res, r, attempt)`; add `(APILogCredentialMaterial).Merge(other)` and delete both private merges. One provider per commit under `providers/difftest`. Defer `DecodeObject`/passthrough extraction unless it stays under about 15 lines.
- Tests: `providers/difftest` recorded streams per provider are the equivalence proof; add a merge test for the credential-material method.
- Docs: none.
- LOC: ~-250
- Risk: medium. Gate: none. Deps: none.

### W1-27 Dedupe the llm streaming `Generate` family and the multi-round tool loop (keep both)
- Findings: llm-core-providers-1 (high), x-dead-code-4, llm-core-providers-2, llm-core-providers-21 (tests of removed code).
- Files: `llm/stream_generate.go` (425 lines: `StreamGenerate`, `StreamGenerateObject`, `StreamObjectResult`), `llm/generate_object.go:23,117` (`streamGenerateObjectStart` seam, streaming half), `ToolCallContext`/`ToolCallContextFromCtx` in `llm/generate.go`, the multi-round tool loop.
- Steps: Q3 is answered (DECISIONS.md): `llm` is a public SDK. The streaming family, the multi-round tool loop and their public tests all stay, even though production callers today pass `Tools: nil` (`cmd/llmcall/main.go:256`). The work is dedupe only: share request building, finish handling and error mapping between the streaming and non-streaming paths, and collapse any copy of the same logic inside these files. Delete a test seam such as `streamGenerateObjectStart` only if a behavior test through the public API reaches the same failure. No exported symbol is removed.
- Tests: every surviving `Generate`/`GenerateObject`/`StreamGenerate*` test unchanged; add a characterization test for any path the dedupe touches that no public-API test reaches.
- Docs: `docs/` standalone-llm doc stays accurate; no mention removed.
- LOC: ~-200
- Risk: low to medium (public surface, so no behavior change). Gate: answered (DECISIONS.md, Q3). Deps: none.

### W1-28 Typed credential diagnosis instead of prose parsed by prefix
- Findings: llm-registry-3 (high), llm-registry-4 (table test only), llm-registry-9 (`ResolveGateCredential` vs `...Candidate` differ only in flags).
- Files: `llm/authenticators.go:92` (`strings.HasPrefix(w, "no credential")` and case-folded `credential header %q:`), `llm/registry/instances.go:982,1005,1036,1041` (builders of the prose), `instances.go:713,751` (gate credential twins; sole caller `cmd/evener-hub/spawn.go:884-887`), `instances_test.go:816` asserts the exact wording.
- Steps: red test: credential diagnosis is decided from a typed field (reason code), not wording; add the typed reason on the resolved result, keep the human text as display only; update the one consumer; test assertions use the reason. Add the slot-state table test (llm-registry-4) before touching the four "usable" decision sites; extract `classifySlot` only if the table shows identical classification. Merge the gate twins.
- Tests: reason-code table; ~53 wording asserts in registry tests (count unverified) move to reason codes as touched.
- Docs: `docs/` provider config doc if it describes warnings.
- LOC: ~-100
- Risk: medium. Gate: none. Deps: none.

### Theme D: hub

### W1-29 Host-record plumbing: one `preserveUnowned` and one record-set type
- Findings: hub-app-host-1 (high), hub-app-host-2 (high).
- Files: `cmd/evener-hub/internal/hostops` or `app_host_*` `records.go:265-559`, `manage.go:2591,2737,2767`, `tombstones.go:116-157` (`hostTOMLRecords`: 8 record maps, 7 dropped sets plus `raisedHighWater`), `record_pruning.go:27,50` (`recordsSnapshot`, `nonNilRecords`).
- Steps: two PRs. (a) Table test first (file-only unowned kept, owned not resurrected, dropped skipped, carried-wins), then `preserveUnowned[V](out, fileSection, ownerOf, owned, dropped)`; convert Receipt, Tombstone, PrunedReceipt, Staged, Remnant, Attempt, StoreSync; use `ownedRecordNames` in `RecordTables` and `manage.go:2591`; leave MirrorCommit's generation derivation. The carried-wins guard is absent in Receipt, Tombstone, PrunedReceipt today (verifier found Tombstone too): the test decides whether that is intended before unifying. (b) `recordSet[V]` with live and dropped, one `records` value in `hostStore`, delete per-kind `set*`/`Snapshot` and `nonNilRecords`; round-trip test over every kind first.
- Tests: described. Leave the crash-safe two-phase commit protocol and recovery paths alone (SYNTHESIS section 6).
- Docs: `docs/product/subsystems.md` hub host-operations row.
- LOC: ~-650 (a -250, b -400)
- Risk: medium-high. Gate: none. Deps: (b) after (a). Split into W1-29a, W1-29b.

### W1-30 Host-operation admission preamble and stale slice-boundary seams
- Findings: hub-app-host-3, hub-app-host-4, hub-app-host-10, hub-app-host-15.
- Files: `mutations.go` (`AddResult`:215, `UpdateResult`:609, `RemoveResult`:884; 33 hand `Unlock`s in mutations.go, `_ = stored` at :793, unreachable branch :828), `manage.go:1468` (`cfg.remnantFence = m.openRemnantID` unconditional) with nil-branches at `deploy.go:203` and `record_pruning.go:204` and comments at `deploy.go:199`, `record_pruning.go:198`, `manage.go:1360-1370`, `deploy.go` (`runOperationWorker` 240 lines, `startOperation` 225), `teardown_ops.go` (`mintAttemptID` at :135,:316; `now.UTC().Format(time.RFC3339)` 17 times).
- Steps: first delete the stale nil branches and `testOnlyRemnantGated` (budget test rewrites at `deploy_test.go:255/468/476`). Lock-balance tests, then `admitMutation`, `lockName` returning unlock, `finishMutation`, `teardownFailureArm`. Split the two deploy functions into phases. Ask before sharing the `RemovedRow` field set.
- Tests: lock-balance test per mutation (every exit path leaves the lock state as before), retry/recover attempt transitions identical.
- Docs: none.
- LOC: ~-350
- Risk: medium-high. Gate: none. Deps: W1-29a.

### W1-31 Mutation error data helpers in appwire
- Findings: hub-app-rest-1, hub-hostops-appsource-3 (step 1).
- Files: typed-vs-map `ErrorData` switch at `cmd/evener-hub/app_rpc.go:677,903`, `app_restart_required.go:301,318`, `web_api.go:62,82`, `appsource/source.go:612`; `localDaemonMutationCallError` (`local_daemon.go:1030`) vs `remoteHubMutationCallError` (`remote_hub_mutations.go:51`).
- Steps: add `appwire.ErrorDataOf(WireError)` and `WithErrorData` stamp helper; migrate the seven sites; add one `mutationOutcomeUnknown(msg, clientMutationID, disposition)` builder and one `unavailable(label,id,err)` constructor for the three hostops sites (step 1, behavior-preserving). The sentinel-list drift (step 2) is a behavior change: separate red-test PR.
- Tests: table of the three shapes (typed, map, embedded) read and stamped.
- Docs: none.
- LOC: ~-200
- Risk: low to medium. Gate: none. Deps: none.

### W1-32 Handler skeleton and resume-then-retry wrappers in the hub
- Findings: hub-app-rest-3, hub-app-rest-4, hub-app-rest-8 (three wrapper vars), hub-app-rest-9 (phase 1 only).
- Files: `cmd/evener-hub/app_rpc.go` `registerThreadHandlers` (8 `ClientMutationID` checks; shared `withDeletionTargetOwnership` + `adoptCallerMutationResult` shape), `compactThreadWithResume`/`setThreadModelWithResume`/`setThreadVisionModelWithResume` (identical bodies modulo action string) and the `*Once` skeletons, `authLoginComplete`/`authDevicePoll`/`launchTrustRepo` function vars (`app_rpc.go:195-203`; writers only `app_rpc_auth_origin_test.go:141-144`, `cov_exact_app_rpc_fuzz_test.go`), `app_auth.go:1404,1486` (endpoint-fingerprint verifiers).
- Steps: `requireClientMutationID` plus a helper owning the trailing `adoptCallerMutationResult` for the seven non-start handlers (turn/start is not a duplicate of `withSessionResume`: share only its retry tail); one resume-then-retry helper; call the three wrapper vars' methods directly and rewrite the two test sites to a scripted HTTP boundary. For the endpoint-fingerprint guard do phase 1 only (one `endpointGuard.Check` shared by three verifiers; unkeyed wrappers that only tests call); the behavior change (discard and restart the flow when the key cannot be computed) is Q15(c), done in W1-65, which builds on this shared check.
- Tests: per-handler characterization of codes; no change to guard outcomes.
- Docs: none.
- LOC: ~-450
- Risk: medium. Gate: none (Q15 is answered in DECISIONS.md; the guard policy change itself lives in W1-65). Deps: W0-07.

### W1-33 `PinSectionStore` retry loop and `rosterFingerprint`
- Findings: hub-hubcore-3, hub-hubcore-6.
- Files: `cmd/evener-hub/internal/hubcore/pin_section.go:250,327,401,502,567` (five identical `for attempt := range 8` loops; 27 `isSQLiteRetryable` mentions; 12 `Contains(err.Error(),"locked")` in tests), `roster.go:472-679` (`rosterFingerprint`, 207 lines of `h.Write`), `past_root_fingerprint.go` (152 lines, the reflect-based guard pattern to copy).
- Steps: (a) `writeTx(label, fn)` owning the attempt loop, retry semantics (including BUSY_SNAPSHOT) unchanged in the first commit; later replace substring matching with `errors.As` on the driver error code. (b) Reflect-based fingerprint with a guard test that fails when a field is added without handling (golden pair first; the invalidation-gate risk is medium).
- Tests: existing retry tests; golden fingerprint pair before and after.
- Docs: none.
- LOC: ~-300
- Risk: medium. Gate: none. Deps: none. Split into W1-33a, W1-33b.

### W1-34 Break `ensureOnce` into phases and share the deploy leg
- Findings: hub-sshconn-3 (high), hub-sshconn-2, hub-sshconn-10 (store/restore helpers only), hub-sshconn-9.
- Files: `cmd/evener-hub/internal/sshconn/manager.go:1857-2207` (`ensureOnce`, 351 lines; `finishEnsureOp` has 6 call sites at :2051,2075,2081,2090,2153,2158), `operations.go:145-181` (deploy leg respelled), `manager.go:2037-2092`, `deploy.go:1063-1223` (the `deploySelected` ladder; `deploySelected` goes in W0-06), `manager.go:553-825,916-1033,2766-2863` (Ensure, AttachUnderGate, reconnectOnce).
- Steps: start with the red test the verifier names (an open record is left when validation fails). Then: split `ensureOnce` into named phases with a deferred finish using a named return; make `DeployForOperation` / `restartForOperation` call the same `runDeployLeg` / `runRestartLeg`; fold `restartHubWithStartProof`'s prepare-and-store block into one `prepareAndStoreRestart`; extract `mapAttemptErr` and `validatePublishable` (genuinely identical pieces only); route the 17 direct `m.pendingRestarts[...] =` writes through `storePendingRestart`/`restoreWithPrevious`.
- Tests: sshconn fake-runner suites, plus the new open-record test; do not touch the file layout yet (W2-09).
- Docs: `docs/evener-hub-remote-operations.md` if flow text changes.
- LOC: ~-400
- Risk: medium-high. Gate: none. Deps: W0-06, W0-26.

### W1-35 Item-paging comparators, hostops forwarders
- Findings: hub-hostops-appsource-4, hub-hostops-appsource-2, hub-hubcore-9 (step 1), hub-hostops-appsource-8.
- Files: `remote_hub_source.go:1158 remotePositionCompare`, `hubcore/message_search.go:693 compareItemPositions` (both byte-identical to `appitempaging.ComparePosition`), `appitempaging/page.go:194 comparePosition` (pass-through), a fourth comparator that ignores `Sub`, duplicated LRUs; `local_daemon.go:471-640` and `remote_hub_mutations.go:73-260` forwarders (about 55); store `recordFile.record` (`store.go:831`) and `tombstoneFile.tombstone` (`:766`) progress/result decode; `ownedObjectKeys` (`store.go:1290-1330`) lists 16 token keys twice; 15 `"local"` literals in non-test hubcore.
- Steps: (a) red test that the `Sub`-ignoring comparator misorders, then delete the pass-through and the byte-identical copies in favor of `appitempaging.ComparePosition` (if `hubcore` importing it makes no cycle). (b) `remoteForward[P,R]` and `localForward` helpers taking explicit closures; convert only the uniform methods (`SetThread*`, `ListTasks`/`ListJobs`/`JobGet`/`JobOutput`, Compact/Shutdown/Rename, Cancel/Promote); leave methods with `gateSkillInputOnClient` or special error mapping. (c) `decodeProgressAndResult` and shared key lists. (d) `appwire.LocalSourceID` constant replacing the literals.
- Tests: comparator table including `Sub`; forwarder coverage by existing source tests.
- Docs: none.
- LOC: ~-450
- Risk: medium. Gate: none. Deps: W0-08. Split into W1-35a (comparators + local const), W1-35b (forwarders), W1-35c (store decode).

### W1-36 One ref grammar
- Findings: hub-hubcore-9 (step 2), hub-internal-small-14, hub-root-rest-17, x-go-ts-protocol-7.
- Files: `hubapi/refs.go` (`refPattern` allows `:`, splits at first colon so `ParseRef("local:a:b")` yields SessionID `a:b`) vs `appwire/refs.go:22` (`refPartPattern` rejects `:`, checks both parts); `hubapi.AttentionSummary` (`types.go:41`, field-identical to appwire's); 54 `appwire.ParseRef` vs 10 `hubapi.ParseRef` in `cmd/evener-hub` non-test; stale "does not import appwire" comment.
- Steps: characterization tests for both `ParseRef` grammars first (pin the `a:b:c` divergence); Q16 is answered (DECISIONS.md): reject `:` in session ids and keep one strict `ParseRef` (appwire's grammar), so move the other callers to it and make `hubapi.Ref` a thin layer over or alias of `appwire.Ref` (a test pins that `local:a:b` is rejected); alias `AttentionSummary`; fix the comment; remove `hubapi.Ref` only after callers migrate.
- Tests: the characterization table; emit_test dedup fixture adjusted.
- Docs: none.
- LOC: ~-200
- Risk: medium (identifier grammar). Gate: answered (DECISIONS.md, Q16). Deps: W0-08.

### Theme E: shared TypeScript client (SYNTHESIS T5)

### W1-37 One duration formatter family
- Findings: x-client-duplication-3, appwire-ts-core-4, web-panes-a-7, native-session-board-5, native-rest-18.
- Files: `appwire-client/typescript/src/displayFormat.ts` (home), `watchRows.ts:41-80`, `steeringNotifications.ts:840-870` (identical ladders differing only in prefix; stale comment at :831-834), `selectors.ts:30 relativeAge` (floors) vs `taskListTime.ts:11-16 relativeTime` (rounds), `activityFormat.ts:50 formatQuietAge`, native `format.ts:16 durationIn`, `compactDuration`, `spokenDuration`, `durationLabel` hack.
- Steps: add `durationParts(ms)` (move native `durationIn`), rebuild the formatters on it; keep existing test tables unchanged; decide floor vs round for relative time and record the decision in the package README (this is user-visible; call it out in the PR). Delete web and native copies.
- Tests: existing tables in the package plus a boundary table at 59s/60s/119s/120s/3599s/3600s across formatters.
- Docs: package README.
- LOC: ~-250
- Risk: low to medium (user-visible rounding). Gate: none (rounding decision named in PR). Deps: none.

### W1-38 One attention-class derivation
- Findings: x-client-duplication-1, native-session-board-4, web-shell-widgets-6.
- Files: web `sessionStatus.ts:4`, `liveness.ts:30`, `railNodes.ts:373`, native `attention.ts:6,92-95` (`boardState`), shared `railSessionState.ts:22`, hub `hubapi/attention.go:16` (`AttentionState` turns `approval_pending` into "awaiting" for every state except errored).
- Steps: phase 1 (native only) make `sessionStateLine` and `boardState` share one precedence table. Phase 2 add `normalizeWireState` (closed->ended, systemError->errored) and one `attentionState()` to `appwire-client`; delete the web copies. Red test first for the case where the copies disagree; Q7 is answered (DECISIONS.md): `restartRequired` or ended wins over `approval_pending`, and hub `AttentionState` follows the same precedence.
- Tests: precedence table over every (state, flags) combination, run against hub `AttentionState` fixtures as well.
- Docs: `docs/product/subsystems.md` client row; package README.
- LOC: ~-300
- Risk: medium (user-visible class changes). Gate: answered (DECISIONS.md, Q7). Deps: none.

### W1-39 One stuck/quiet policy
- Findings: x-client-duplication-2 (step 2), native-session-board-6.
- Files: web `flow/liveness.ts:27-28,180-191` (20s quiet / 180s stuck), package `sessionActivity.ts:11` (10 min), native `trayLine.ts`.
- Steps: Q6 is answered (DECISIONS.md): one stuck rule in the package, 10 minutes, and a session is never stuck while its delegates are actively running. Web and phone consume it. Web's short quiet hint (20s) may stay as a separate, softer state, encoded as a named option or a distinct state in the package, not hand-copied constants.
- Tests: boundary table at the thresholds; web and phone render tests assert state, not text.
- Docs: `docs/product/` session-activity guide.
- LOC: ~-100
- Risk: medium (user-visible). Gate: answered (DECISIONS.md, Q6). Deps: W0-25.

### W1-40 One outbox transition rule set
- Findings: x-client-duplication-7, native-rest-5, native-rest-6, web-stores-9.
- Files: web `mutationOutboxIndexedDB.ts:675-676,685` (retains accepted copy for `carriesOptimisticInput(source) || method==='notes/human/set'`), native `mutationOutboxStorage.ts:553` (only `carriesOptimisticInput`), `mutationOutboxStorage.ts:221-289` (five ALTER TABLE ladders), `navigationActionRepository.ts:39`, `pinAssignmentDrafts.ts:7`, `nativePreferenceDrafts.ts:112` (three Backend interfaces), `valid()` copies.
- Steps: (a) move the transition decisions into the package with unit tests there (drifted retention rule first, since it is user-visible); both adapters call it. (b) native checkpoint repositories: one validator helper taking a message and one backend interface; keep stored shapes; test key-reorder equality before switching to `sameJsonValue`. (c) The ALTER TABLE ladder and `#seedLegacyAttemptCount` are persisted client-state eras, and Q1 says on-disk migrations stay. Part (c) is simplification only (for example one shared step helper for the ladder) and keeps every upgrade path and its behavior test; do not collapse the ladder to one `CREATE TABLE`.
- Tests: the same transition table runs through both adapters (IndexedDB fake, SQLite fake).
- Docs: `docs/product/message-recovery.md` if retention behavior changes.
- LOC: ~-500
- Risk: medium-high (message recovery). Gate: answered (DECISIONS.md, Q1; (c) is simplification only). Deps: none. Split into W1-40a, W1-40b.

### W1-41 Move shared web/native code into the package
- Findings: native-rest-2 (high), x-client-duplication-11, native-rest-17, x-client-duplication-8, x-client-duplication-12.
- Files: `ansi.ts` (579 lines) + test and `forbidTags.ts` (29) from the web frontend into `appwire-client/typescript` with package exports; `usePluginPreview` (110 lines, takes the host request as an injected port; `hostRequest` stays in web); native imports at `ansiOutputStyles.ts:6`, `AnsiOutputList.tsx:6`, `AnsiOutputLine.tsx:2`, `session/StepEvidence.tsx:8`, `newSession/sheetPlugins.ts:19`, `newSessionContext.tsx:11`, `scripts/build-mermaid-page.mts:18`, `metro.config.js:27` whitelist; `canWriteSharedNotes` beside `canReadSharedNotes` (copies in `humanNoteDrafts.ts:370`, `sessionNotes.ts:59-65`); markdown fence helpers and mermaid regexes (`markdownSegments.ts:10-12`, web `segments.ts:18`; keep differing bodies local); composer verb to wire-method map (seven places) and host constants.
- Steps: PR per move (ansi + forbidTags; usePluginPreview; notes gate + fence helpers; verb map). Drop the Metro whitelist entry; extend `lint-package-imports` to forbid native imports from `cmd/evener-hub/frontend` paths.
- Tests: tests move with the code; `make test-api-package`, `make test-web`, `make test-native`.
- Docs: package README (exports list), `docs/developing-evener/README.md` import rules.
- LOC: ~-150 net (moves ~1,000 lines)
- Risk: low to medium. Gate: none. Deps: none. Split into W1-41a..d.

### W1-42 Phone store reads reducer internals through exports; projector join rule once
- Findings: x-client-duplication-4 (steps 1-2), appwire-ts-core-3, appwire-ts-core-4 follow-ups.
- Files: `mobile/src/state/conversation.ts:124-141` (`SNAPSHOT_AUTHORITY_FIELDS` identical to `reducer.ts` `ITEM_OMISSION_TOLERANT_FIELDS`, 14 fields), `THREAD_SCALAR_ONLY_METHODS`, `reducer.ts:55-63 laterActivity` (Date.parse, ms) vs `sessionActivityStore.ts:1300-1330` (strict RFC3339Nano parser).
- Steps: export the field list and a `notificationTouchesTranscript` predicate next to `applyNotificationToThread` with a test that every method is classified. Red test for sub-millisecond ordering, then move `activityInstant`/`activityAfter`/`revisionOf`/`joinDelegateState` into `stableDelegate.ts`, use it from both stores, delete `laterActivity`. Moving cross-turn merge logic is out of scope here.
- Tests: described; package unit tests.
- Docs: `docs/product/session-activity.md` if the join rule is described.
- LOC: ~-150
- Risk: medium. Gate: none. Deps: none.

### W1-43 Delete the write-only phone `ActivityStore`
- Findings: native-tests-legacy-mobile-2 (high), x-client-duplication-6.
- Files: `mobile/src/state/activity.ts` (450 lines; 422-line view, 1590 + 1224 test lines), `mobile/src/services/conversation.ts:678` (`runs projectActivity inside readProjection`, `validateHierarchy` throws `ActivityProjectionError`; `conversation.test.ts:1951` documents the throw), `mobile-native/src/nativeConversationSessions.ts:14,159`.
- Steps: no `ActivitySheet` exists and nothing reads `.view`. First establish what the sink's rehydrate triggers protect against now that activity screens read the package `SessionActivityStore`; if those cases are covered there, delete store, view and tests (the throw that blocks reads goes with it). Red test: an activity hierarchy violation no longer blocks conversation reads.
- Tests: the red test; `make test-native`.
- Docs: `docs/product/session-activity.md` and subsystems row mention of a phone activity store, if any.
- LOC: ~-3,200 (prod ~900, tests ~2,800 less rewrites)
- Risk: medium. Gate: answered (DECISIONS.md, Q10(c)). Deps: none.

### Theme F: web and native components

### W1-44 Composer and QueueStrip share one submit protocol
- Findings: web-panes-a-1 (high).
- Files: `cmd/evener-hub/frontend/src/panes/Composer.tsx:1063-1072` and `QueueStrip.tsx:427-436` (skill/command gate strings verbatim; QueueStrip comment says "mirroring Composer's own submitAction"), the "changed in another tab" toast (8 non-test occurrences across `sourceState`, `QueueStrip`, `Composer`).
- Steps: extract the submit/gate/recover protocol to one module both call; keep recovery tests green throughout (the plan touches recovery). Pair with `useThreadModel` ensure/release dance (web-panes-a-8: byte-identical effects in `ConversationBody.tsx:131-149`, `DocPane.tsx:162-180`, `stores/useThreadModel.ts:10-28`).
- Tests: existing recovery tests (do not weaken); one test that both surfaces reach the same decision for the same input.
- Docs: `docs/product/message-recovery.md` if ownership statement changes.
- LOC: ~-250
- Risk: medium-high (recovery). Gate: none. Deps: none.

### W1-45 Spawn uses `launchConfigStoreForHost`
- Findings: web-panes-b-3, web-panes-b-8, web-panes-b-9 (PR A).
- Files: `panes/Spawn.tsx:1322,1343,1372,1450,2648` (raw `hostRequest` for `paths/complete`, `path/validate`, `dirs/create`, `launch/resolve` with hand-written `?? []`), `settings/project.tsx:68-80`, `usePluginPreview.ts` and `useSpawnSlashCatalog` (110 vs 144 lines, same shape), `AdvancedOptions.tsx:278,318-322` and `LaunchConfigForm.tsx:110` (`resolvedValue` duplicated), `launchSchema.ts:~230`.
- Steps: bind `validatePath/createDirectory/complete/resolveConfig` to `launchConfigStoreForHost(submittedSource)`; keep `listRecents` on `hostRequest` and the `activeHostRef` stale-answer loop around the store call. One `useScopedHostRead<T>` for the two hooks (keep the `active` cleanup flag). Share `resolvedValue` and the default placeholder text. The `SpawnForm` split is W2-13.
- Tests: A-B-A host switch tests exist; add a regression for stale answers; assert behavior only.
- Docs: none.
- LOC: ~-250
- Risk: medium. Gate: none. Deps: none.

### W1-46 Tracked and watched thread hydration share one path
- Findings: web-stores-4 (high).
- Files: `stores/threads.ts` `hydrateAndSubscribe` (:3845) and `hydrateAndSubscribeWatch` (:4032; comment at :4080 "See hydrateAndSubscribe's identical comment"), `inflightHydrates*` (:660-662) and `inflightWatchHydrates*` (:3724-3727).
- Steps: characterization tests first (behavior differences are intentional in places); then unify with the differences as explicit parameters. The audit's -400 to -600 is speculative.
- Tests: characterization per difference (latest-wins, abort on unsubscribe, error path), `make test-web`.
- Docs: `docs/product/subsystems.md` client store row if it names the two paths.
- LOC: ~-300
- Risk: high. Gate: none. Deps: W0-27; precedes W2-15.

### W1-47 Four unused widgets
- Findings: web-shell-widgets-8.
- Files: `src/widgets/{table,insightcard,inspectorcard,recommendationcard}/*`, `widgets/index.ts` exports, `src/dev/gallery-sections/*`, design-system rows in `docs/developing-evener/design-system.md`, `styles/token-contract.test.ts` allowlist entries, `DiffTable` / panescaffold comments.
- Steps: delete as listed (AGENTS.md restricts adding widgets, not deleting; Q10(b) approved the deletion).
- Tests: `make test-web`.
- Docs: design-system doc rows.
- LOC: ~-1,100
- Risk: low. Gate: answered (DECISIONS.md, Q10(b)). Deps: none.

### W1-48 Test fixtures and mocks on the native side
- Findings: native-tests-legacy-mobile-8, native-tests-legacy-mobile-9.
- Files: `vitestSetup.ts:56` mocks `expo-symbols` as `{ SymbolView: 'SymbolView' }` yet 97 per-file copies exist (one differs, `SymbolView: () => null`, and must stay); `expo-crypto` mocked in 53 files; ~44 suites hand-roll `{ request } as never` clients and bypass `FakeClient`'s wire-method check.
- Steps: delete the identical per-file `vi.mock('expo-symbols')` copies by codemod, keeping the differing one; migrate the named wire-method suites (hubModels, launchMcp, sessionDeletion, pinNavigation, hubUpdates, hosts, plugin count) to `FakeClient`; add a deferred-request mode only if two migrations need it.
- Tests: `make test-native`; test counts before and after equal.
- Docs: none.
- LOC: ~-700
- Risk: low. Gate: none. Deps: none.

### Theme G: tests and gates (SYNTHESIS T6)

### W1-49 Extend the copy-oracle ratchet to error-string assertions
- Findings: go-tests-agent-4, go-tests-rest-14, web-dev-and-tests-7 (ratchet half), build-gates-scripts-19 (tiering).
- Files: `internal/devtool/copyonly` (allowlist.go/tsv), `cmd/evener-hub/frontend/src/dev/copyonly`.
- Steps: add the `strings.Contains(err.Error(), ...)` pattern (785 in 190 files in agent; 1,341 in 374 files repo-wide) to the existing inventory as a ratchet that blocks new ones and allows the recorded set to shrink. Do not build a new tool.
- Tests: the ratchet's own tests for the new pattern.
- Docs: `docs/developing-evener/testing.md` ratchet section.
- LOC: ~+150 (plus a large allowlist data file)
- Risk: low. Gate: answered (DECISIONS.md, Q12 second tier: yes, with an allowlist of today's sites). Deps: none.

### W1-50 Convert phrase assertions to sentinels or behavior (per area)
- Findings: go-tests-agent-4, go-tests-rest-14, agent-internal-state-12, agent-misc-root-18, hub-sshconn-18, hub-internal-small-8, hub-root-rest-13, internal-misc-cmds-19, web-dev-and-tests-7, web-shell-widgets-16, web-stores-15, web-panes-a (about 150), native-rest-12, native-tests-legacy-mobile-10, appwire-ts-state-testing-14.
- Files: densest files first: agent `delegatestore`/`jobstore` (102 `Error(), ` assertions: `store_covtest_test.go:163`), `agent/failure_steering_test.go:20-27,362,491`, `session_tools_worktree_errors_test.go:337`, `job_watch_config_test.go:29`; sshconn (63 `strings.Contains(err.Error(), ...)`); hub `deploy_flags_test.go:488,497,561`; web `StatusBar.test.tsx:414`, `LivenessLine.test.tsx:134`, `NotificationCard.test.tsx:247`, `WarningItem.test.tsx:189`, `DockRegion.test.tsx:122,214`, `ConnectionBanner.test.tsx:143,461`; native `Composer.test.tsx:85`, `useSheet.test.ts:45`, `tasksRead.test.ts:28`, `BoardScreen.test.tsx:1033`.
- Steps: one PR per area (agent stores; agent/session; sshconn; hub root; web shell; web panes; native). Use `errors.Is/As` on sentinels (add a sentinel only where behavior depends on it), wire `Code`/`EvenerErrorInfo`, role/state/`data-testid`. Treat counts as upper bounds and triage test-supplied echo cases first.
- Tests: each converted test must still fail when the behavior it covers breaks (check one per file by removing the guarded line in a scratch worktree, CI only).
- Docs: none.
- LOC: ~-500 across the area PRs (each area is roughly -50 to -100)
- Risk: low. Gate: none. Deps: W1-49 helps but is not required.

### W1-51 Adopt the existing Go test kits
- Findings: go-tests-agent-1, -2, -3, -8, -9, -10, go-tests-rest-5, -6, -7 (first pass), agent-delegates-19 (helper half).
- Files: `agent/internal/agenttest` (`agenttest.go:44-60` `fakeAdapter.Complete` identical to `session_test.go:56-71`; `CommunicateResponse` at :212 identical to `communicate_test_helpers_test.go:10`; `ToolCallResponse` at :200 duplicates `tool_web_fetch_test.go:111`), `agent/internal/testkit` (adoption stalled: 922 `NewSession(` lines in 219 files), `fuzz/schemagen/source.go:74 NewByteSource` (21 local `next() byte` readers), `appwire/appwiretest` (used only by `optimistic_test.go`), `hubtestenv.go:35`, `cmd/evener/testmain_test.go:19` and `launchcheck/testmain_test.go:14` (list only `EVENER_API_TOKEN`).
- Steps: several small PRs: (a) swap the exact-duplicate builders for `agenttest`'s (mechanical, `go vet`); (b) replace the 21 byte readers with `NewByteSource` (corpus-compat risk: diff the first N bytes of generated programs against the old reader before swapping; keep the old reader where corpora would change); (c) testkit pilot on the three heaviest files plus the allowlist audit (go-tests-agent-3 verifier: do not claim -4,000 LOC before a pilot); (d) `waitFor`/`waitUntil` and scripted AppWire loop to `appwiretest`. Create no new packages (see W1-52 for the two the audit asks about).
- Tests: test count and pass set equal before/after; CI.
- Docs: `docs/developing-evener/testing.md` kit section.
- LOC: ~-1,500 across the series
- Risk: low to medium (fuzz corpus). Gate: none. Deps: none.

### W1-52 New shared Go test-support packages (only two)
- Findings: go-tests-rest-1 (131 `Complete(` implementations in 103 non-agent test files), go-tests-rest-9, go-tests-rest-7 (61 `exec.Command("git")` in 46 test files; only 16 set `GIT_CONFIG_GLOBAL`), go-tests-agent-9.
- Files: `llm/llmtest` (a scripted adapter; `agenttest` lives in `agent/internal` and is imported only from `agent/*`, `llm` is a separate module so `llm/llmtest` is viable), a `gittest` package (isolated git config, `newLinkedWorktree`, `runGit`), migrate copies in buildinfo, cmdutil, identifier, launchconfig, sshconn, plugins, `agent/internal/worktree/predicates_test.go:88` (one-line diff from `session_tools_worktree_create_test.go:82`).
- Steps: create each package only if copy counts stay above ten after W1-51 (SYNTHESIS conflict 8). Migrate by area.
- Tests: migrated tests keep their assertions.
- Docs: testing.md kit list.
- LOC: ~-1,200 (packages +300)
- Risk: low. Gate: answered (DECISIONS.md, Q13 orchestrator default: adopt the kits first, create a package only if more than ten copies remain). Deps: W1-51.

### W1-53 Web fixtures and connection-store reset
- Findings: web-dev-and-tests-3, web-dev-and-tests-4, web-dev-and-tests-5, web-dev-and-tests-6, appwire-ts-state-testing-9, web-stores-17 (helper half).
- Files: `connectFakeClient` re-declared in 89 test files; 456 `connectionStore.setState` calls with five reset spellings; 85 files declare thread fixtures (`changeVisionModel: true`), 36 declare `const CAPABILITIES: ThreadCapabilities`, 27 `testThread`, 27 `readResponse`; `wireThread` imported by 4 web files; `styles/cssBlock.ts` vs 58 files hand-rolling comment-strip; local `deferred` in 16 files and `appwire-client/typescript/src/testing/deferred.ts` (47 importers).
- Steps: add `resetConnectionStoreForTests()` in `stores/connection.ts` (matching other stores) and a shared `connectReadyFakeClient` in a `src/testing` module (no new widget, no new prop); make `readModuleCss` strip comments (unit test in `cssBlock.test.ts`) and migrate callers; swap local `deferred` only where the local copy is a subset of the shared API; export `MOBILE_QUERY`. Codemod in batches of 15-20 files per PR.
- Tests: `make test-web`; test counts equal.
- Docs: `docs/developing-evener/testing.md` section at ~:1635 points to `cssBlock`.
- LOC: ~-3,500 across the series
- Risk: low. Gate: none. Deps: none.

### W1-54 Delete coverage-wave test files
- Findings: hub-root-rest-11 (40 `cov_*_test.go`, 6,031 lines, 46 Fuzz funcs), hub-app-rest-15 (39 files, 5,928 lines, 790 discarded-result statements), go-tests-agent-5 (211 sprint-labelled files), agent-delegates-19, agent-jobs-18, agent-internal-tools-5, hub-hubcore-19, hub-sshconn-18, go-tests-rest-2, internal-misc-cmds-9, llm-core-providers-21, build-gates-scripts-19 (183 gap/cov-named files).
- Files: `cmd/evener-hub/cov_*_fuzz_test.go`, `agent/*covtest*_test.go` (17 in the delegates slice, 4,013 lines), `credentials/package_union_fuzz_test.go`, `*_covtest_test.go` elsewhere, `scripts/coverage/coverage-floors.txt`, `scripts/coverage/fuzzcov-ignore.txt`, `scripts/fuzz/fuzz-targets.txt` (46 Fuzz funcs in the hub `cov_*` files are registered by name; the static gap gate fails if the rows outlive the files), `testing-budget.json` rows.
- Steps: Q2 is answered. Per file: move assertions that test real behavior into the matching `app_*_test.go`, delete discarded-result blocks and `err != nil`-only tests, run the coverage before/after report in CI and attach it. Q2 answered: the floor is not a protected gate, so update or drop the floors rows in the same PR. Delete the fuzz-target and coverage-list rows together with the files. Smallest packages first; no line target (build-gates-scripts-19 verifier).
- Tests: coverage report attached per PR.
- Docs: `docs/developing-evener/testing.md` coverage section.
- LOC: ~-8,000 first pass (hub 6,000 plus delegates 2,000; the 38k repo-wide figure in SYNTHESIS is not claimed) here
- Risk: medium. Gate: answered (DECISIONS.md, Q2). Deps: before W1-50, so phrase-assertion conversion skips files this PR deletes.

### W1-55 Mechanical test renames and stale citations
- Findings: go-tests-agent-5 (cheap parts), go-tests-rest-3, go-tests-rest-4, hub-hostops-appsource-14, native-tests-legacy-mobile-5/12 (labels), agent-jobs-18 (comment half), hub-app-host-17 (round files).
- Files: 388 `(file.go lines N-M)` parentheticals in 50 agent test files; 14 round-named files (`host_manage_round12/15/16/17`, hostops `quarantine_round1-6`, sshconn `round8/10/11/12/13`; 4,020 lines); 50 Test/Fuzz names with `Round|Pass N`; `remote_hub_subscription_round{nine,ten,eleven,twelve}_test.go`; the 31 describes with tracker labels in `conversation.test.ts`.
- Steps: strip line-number parentheticals mechanically (script, committed only if Jesse wants it; otherwise run once); `git mv` round/coverage files to behavior names in a move-only commit; rename tests by AST rename. No merges of test files in this PR.
- Tests: test count identical before and after; `git diff --stat -M` shows moves.
- Docs: none.
- LOC: ~-600 (comments)
- Risk: low. Gate: none. Deps: none; run before any W2 test split.

### W1-56 Build-gate repairs (keep the tooling)
- Findings: build-gates-scripts-1, build-gates-scripts-3, -4, -5, -6, -7, -8, -10, -16, -2.
- Files: `testing-budget.json` (zero `"metric"` keys; `test-timing-budget.sh:485,553-556` downgrades the ratio FAIL to WARN; CI still runs the check at `ci.yml:433`, class ceilings can still fail), `make/testing.mk:233`, `makefiletargets_audit_test.go` (`makefileVariableFedDeletes` allowlist), and the tools that stay: `internal/devtool/*audit*`, `scenariosourcecite_audit_test.go`, `maketargetsdoc`, `internal/devtool/gatesurface`, the fuzz mutation toolkit, the sandbox/lazytranscript/appwirelegacy/doctorcomments/transcriptcomments prose audits.
- Steps: Q11 is answered (DECISIONS.md): delete none of the gate tooling. (a) Rebaseline the timing ratchet: regenerate every package budget from a CI-class measurement (not this workstation) and write the single top-level marker `"metric": "package-wall-seconds"` (`WALL_METRIC` in `test-timing-budget.sh`); package rows stay plain numbers. With the marker present the per-package ratios enforce again instead of downgrading to WARN. (b) Fix `make/testing.mk:233` bare `$(mktemp)` and the empty `makefileVariableFedDeletes` allowlist. (c) Keep `gatesurface`, `maketargetsdoc`, the fuzz mutation toolkit and the prose audits; fix only what is broken in them (build-gates-scripts-1, -3 to -8, -10, -16 become keep-and-fix findings). (d) Port the shell gate stack to Go only when touched (build-gates-scripts-2: `load-aware-workers.sh` is not replaced by GOMAXPROCS).
- Tests: the ratchet's own tests gain a case where a package over its ratio fails (not warns); every gate keeps its tests.
- Docs: `docs/developing-evener/README.md`, `make help` text if a target changes.
- LOC: ~+100 (budget rows and small fixes; nothing deleted)
- Risk: low to medium (a ratchet that enforces again can fail CI on noisy packages, so baseline from CI runs). Gate: answered (DECISIONS.md, Q11). Deps: none. Split into W1-56a (ratchet rebaseline) and W1-56b (mktemp and allowlist fixes).

### W1-57 Trim the testing guide
- Findings: x-docs-6 (testing.md is 1,973 lines of rolling changelog), x-docs-3 (remaining), x-docs-9 (history phrases only), x-docs-10 (stale count).
- Files: `docs/developing-evener/testing.md`, `docs/web-ui/README.md:40`, `docs-site/scripts/check.mjs` (backticked-path check, optional: x-docs-11).
- Steps: rewrite as present-tense rules; remove issue/date narrative; keep anchors cited elsewhere (grep first); do not split into three files.
- Tests: `make docs-check`.
- Docs: that is the change.
- LOC: ~-900
- Risk: low. Gate: none. Deps: after W1-49 and W1-53 (they edit the same sections).

### W1-58 Plan and spec corpus
- Findings: x-docs-1 (481 plans/402,756 lines, 441 specs/117,976 lines; 161 non-docs files cite `docs/superpowers`, 92 distinct paths).
- Files: `docs/superpowers/plans/**`, `docs/superpowers/specs/**`.
- Steps: Q19 is answered (DECISIONS.md): delete plans whose PR has merged to main (check with `gh pr list --search`, not by citation count) and that nobody has touched in the last 30 days. Keep all specs. Repoint any code cite to an evergreen doc in the same PR (exact `git grep` of each basename); drop front-matter noise. Going forward a plan is deleted in the PR that completes it.
- Tests: `make docs-check`.
- Docs: `docs/product/README.md` gains the one-line rule: a plan is deleted in the PR that completes it.
- LOC: 0 code (docs only: about -300,000 lines of markdown; not counted in the totals)
- Risk: low (history keeps it). Gate: answered (DECISIONS.md, Q19). Deps: none.

### Theme H: gated sweeps (compat, recovery, experiments)

Their gates (Q1, Q8, Q9, Q15) are answered in [DECISIONS.md](DECISIONS.md). Each is its own PR. The compat entries are shaped by Q1: migrations of persisted state stay, and only fallbacks in a dropped version direction go.

### W1-59 Write the version-skew policy into `docs/product/README.md` and classify the "older hub" fallbacks by direction
- Findings: x-compat-legacy-1 (46 "older hub" fallbacks; `appwire/types.go:30-35` documents that daemons outlive the hub that spawned them), T8.
- Files: `docs/product/README.md` (new "Compatibility" section), `docs/product/subsystems.md` link; the classification table lives in the PR description, one row per fallback.
- Steps: record the Q1 policy evergreen, with no dates: the hub tolerates any older daemon; daemon code may assume the hub is the same version or newer; phone code and local-hub code tolerate an older (remote) hub; on-disk state migrations stay. Then classify each of the 46 fallbacks by version direction: phone or local-hub code serving an older hub (keep, including the combined update/apply path), daemon code facing an older hub (delete), hub code serving an older phone app (delete), remote-side code serving a newer local hub (delete), and no production producer in any direction (T1 dead code, delete). New compat code names its removal condition in the PR description.
- Tests: `make docs-check`.
- Docs: that is the change.
- LOC: +40
- Risk: none. Gate: answered (DECISIONS.md, Q1). Deps: none; precedes W1-60..W1-64 and W1-40 part (c).

### W1-60 Drop never-shipped host-record spellings and simplify sidecar handling
- Findings: hub-app-host-6, hub-app-host-7 (bare `.stash` arm in `stashPathOwnedBy`, `manage.go:2842-2851`, and its test `pipeline_test.go:1525`; ask before touching `Retired*` fields at `records.go:43-66` or `remote-lease`), x-compat-legacy-6 (sidecar introduced `d66d970556` 2026-09-20, retired `3ffeccc893` 2026-09-26; no tag contains the introduction without the retirement), hub-hostops-appsource-9.
- Files: `cmd/evener-hub/app_host_manage.go:2804-2808` (".stash name never shipped"), `store.poison` (`:599-611,1625-1628`), `poison/poisoned/loadErr` and three guard sites, compensating-sidecar alias (`intent.go:166 compensationSidecarLegacy`), hostops retired fields (`custody.go`, `recordFile` OrphanBoundary/OrphanResolved/Attestation/PendingSpawns), `StateOrphanUnverified` and the appwire enum value.
- Steps: delete the never-shipped `.stash` spelling and the compensating alias (no producer in any direction, so T1 dead code; tag check first). Q1 says on-disk state migrations stay, so the retired record fields and sidecar handling that read persisted host records are simplification only: keep every read path and its behavior test. Replace write-poisoning with recovery per the product guide.
- Tests: existing store round-trip tests stay; delete only tests of the never-shipped spellings.
- Docs: `docs/product/subsystems.md` hub host rows.
- LOC: ~-300
- Risk: medium. Gate: answered (DECISIONS.md, Q1). Deps: W1-59, W1-29.

### W1-61 Simplify plugin-store migrations (keep every migration path)
- Findings: internal-misc-cmds-5, x-compat-legacy-13, internal-misc-cmds-10 (seams), internal-misc-cmds-15.
- Files: `internal/plugins/marketplace_migration.go` (1,282 lines, 439 comments), `metadata_migration.go` (343), `store_format.go` (94), tests (4,261 + 774 + 590 + 170), `locks.go:84-97` `lockStore` calling `migrateMarketplaceNames` and `ensureManagedContentMigrated`.
- Steps: Q1 answered (DECISIONS.md): state migrations stay. Keep the marketplace-name migration, and keep the managed Git-metadata migration (`metadata_migration.go`, `store_format.go`, added 2026-10-21, with its interrupted-operation recovery and audit state). Scope this entry to simplification that keeps every migration path working: take the three tree-copy implementations (`source.go:304`, `publication.go:325`, `resolve.go:412`) to one, and trim test seams that have no behavior test behind them.
- Tests: preservation behavior tests stay.
- Docs: `docs/product/subsystems.md` rows S17/S18.
- LOC: ~-300
- Risk: medium. Gate: answered (DECISIONS.md, Q1). Deps: W1-59.

### W1-62 Simplify the serf-era migration tools and the MEMORY.md migration (keep every migration path)
- Findings: internal-misc-cmds-6, x-compat-legacy-7, agent-misc-root-16, x-compat-legacy-5 (pre-union adapters; verify no producers).
- Files: `cmd/evener-migrate/*` (801 non-test lines, created 2026-08-18), `cmdutil/userdirs.go:34 checkLegacyDataDirs` / `ErrLegacySerfData`, install.sh notes, `agent/memory_migrate.go` (671 lines; call site `session_memory.go:201` swallows the error). `homeRootFiles` duplication in `evener-migrate` with the false "does not import cmdutil" comment.
- Steps: Q1 answered (DECISIONS.md): users skip versions, so state migrations stay. Keep the serf-era migration tools, the MEMORY.md migration and their tests. Allowed work: fix the false comment and move the 9-name `homeRootFiles` list into `envvars/userdirs` (about -25, ships now as W1-62a); drop non-Ctx wrappers in the memory migration and log its swallowed error at warning; share helpers between the two migrations. Delete a pre-union adapter (x-compat-legacy-5) only when verification shows no production producer, since that is dead code and not a migration. The launch refusal on legacy serf data is unchanged by this entry.
- Tests: every migration path keeps its behavior coverage; any removed duplicate helper names the test that still covers its path.
- Docs: install docs; subsystems row.
- LOC: ~-150
- Risk: low to medium. Gate: answered (DECISIONS.md, Q1). Deps: W1-59. The false-comment fix can ship now as a tiny PR (W1-62a).

### W1-63 Simplify legacy transcript projection and checkpoint formats (keep every format readable)
- Findings: internal-app-layer-1 (high), x-compat-legacy-12, agent-internal-state-10, x-compat-legacy-3 (rest), x-compat-legacy-9.
- Files: `internal/apptranscript/apptranscript.go:445` (`Format != TurnFormatIdentity` routes to `ProjectTurnParts`), `agent/transcript/placement.go:81` (stamps Format on every written entry, introduced `1268e1282e` 2026-09-26), `internal/transcriptindex/build.go` (`applyLegacy`:195, 31 "communicate" mentions), `flush_communicate.go` (102 lines), `agent/internal/contextmgr/checkpoint_format.go:299-321,669-677` (three legacy readers), `strategy_session_log.go:243`, `toolIntent` "purpose" fallbacks (`transcript_render.go:1746`, `doctor/transcript.go:324`, `apptranscript.go:217`).
- Steps: Q1 answered (DECISIONS.md): persisted state stays readable. Do not rewrite users' transcripts in place and do not delete the legacy projection or the three checkpoint readers. Allowed: simplification that keeps every migration path and its behavior coverage, for example normalizing legacy entries into identity-shaped entries in memory at the read boundary so one projection path remains (only with golden output identical for every legacy shape; bump index `projectionID`; update the transcript-read-model row), and sharing code between the legacy readers. Tool intent: add `schema.ToolIntent(args)` and call it from the three sites; the "purpose" fallback stays because it reads persisted data.
- Tests: golden transcripts from before 2026-09-26 and a legacy checkpoint of each format project identically before and after the change, and those goldens stay in the suite.
- Docs: `docs/product/subsystems.md` transcript-read-model row.
- LOC: ~-300
- Risk: high (user transcripts). Gate: answered (DECISIONS.md, Q1). Deps: W1-59.

### W1-64 Classify "older hub" fallbacks and young on-device formats by version direction
- Findings: x-compat-legacy-1, x-compat-legacy-2, native-session-board-8, native-rest-8, x-compat-legacy-14, appwire-ts-core-13 (`hubUpdate.ts:60-63,90-92,289+` deprecated `apply()`/`applying`/`applyError`, still called at `HubHome.tsx:69`: an intentional compat path).
- Files: web `hubUpdates.tsx:104`, native `HubHome.tsx:38` (both cast `check as {restartSupported?}`), `approvalRefs`/`tracksSeenThrough` in `attention.ts:66-70`, `boardMemory.ts`, `activityPoll.ts`, `boardData.ts`, `nativePreferenceDrafts.ts:248-320`, `draftDocument.ts:59-61,84`, `draftRepository.ts:205`, `screens.tsx:3153` (exclude: documented in `message-recovery.md`), web `workspace.ts:378,457 migrateLegacyLayout`, `sessionEntryCache.ts:327`, combined update/apply path.
- Steps: Q1 answered (DECISIONS.md); apply the direction table from W1-59 per area. Keep: every phone and local-hub fallback for an older (remote) hub, including the combined update/apply path (`apply()`/`applying`/`applyError`, the `restartSupported` casts), and every persisted client-state era (`nativePreferenceDrafts`, `draftDocument`, `draftRepository`, `boardMemory`, `migrateLegacyLayout`, `sessionEntryCache`); these may be simplified but keep every migration path and its behavior coverage. Delete only: daemon-side fallbacks for an older hub, hub-side fallbacks for an older phone app, remote-side fallbacks for a newer local hub, and fallbacks with no production producer. Never discard draft text.
- Tests: tests for deleted fallbacks go with them; tests for kept fallbacks and all draft preservation tests stay.
- Docs: `docs/product/message-recovery.md` unchanged for drafts.
- LOC: ~-500
- Risk: medium. Gate: answered (DECISIONS.md, Q1). Deps: W1-59. Split by area.

### W1-65 Recovery-over-lockdown behavior changes
- Findings: agent-subpackages-10, agent-delegates-17, x-compat-legacy-8, hub-app-rest-9 (policy half).
- Files: `agent/plugin/commands.go:97-108` (first unreadable or unparsable command file drops the whole plugin), `agents.go:46`, `agent/delegate_legacy_state.go:11 rejectLegacyDelegateState` (called `delegate_runtime.go:3236`; refuses startup) and `doctor/jobs.go:276 legacyDelegateFailures`, endpoint-fingerprint guard (`app_auth.go:402-405,1404,1486`; fail-closed default).
- Steps: Q15 is answered (DECISIONS.md): all three switch to recovery, TDD per item. (a) Red test: a plugin with one malformed command file loads its healthy commands, skips the bad file, and emits a diagnostic naming it. (b) Legacy delegate state is archived or ignored with a diagnostic and startup proceeds instead of refusing (`doctor/jobs.go legacyDelegateFailures` reports it). (c) When the endpoint-fingerprint key cannot be computed, discard the pending login or pairing flow and automatically restart a fresh flow against the current endpoint; never complete unchecked. Also dedupe the three verifiers into one `endpointGuard.Check` (done here, or taken from W1-32 phase 1 if it merged first).
- Tests: behavior tests including the transition back to useful operation (AGENTS.md): the plugin's healthy commands run, startup proceeds after legacy state is archived, the restarted flow completes against the current endpoint, and an unchecked completion is never possible.
- Docs: `docs/product/principles.md` or the owning guide; `friction.md` entries removed when fixed, without renumbering.
- LOC: ~+150
- Risk: medium. Gate: answered (DECISIONS.md, Q15). Deps: none (W1-32 phase 1 may land first).

### W1-66 Remove the four experimental context strategies
- Findings: agent-internal-state-2, agent-internal-state-1 (pipeline copies after deletion).
- Files: `agent/session_init.go:277-284` (accepts `obs-mask`, `checkpoint-pred`, `memory-crystals`, `recursive-distill`; `session_config.go:185` and `main.go:360` document only `compact|session-log|ooda`), the four strategy files and tests, `selectStrategy` cases, `aggressiveMaskObservations`, `predictiveChec...`, cov_s2 / fuzz-list references. Afterwards add `Manager.resetMeasurement()` and one `applyLayer` helper for the remaining strategies (agent-internal-state-1 plan).
- Steps: Q9 is answered (DECISIONS.md): delete all four strategies. Then the helper extraction, guarded by existing seqfuzz and compaction tests.
- Tests: compaction suites in CI.
- Docs: `docs/` contextmgr reference (`design/context.md`).
- LOC: ~-3,500
- Risk: low. Gate: answered (DECISIONS.md, Q9). Deps: none; precedes W2-04.

### W1-67 Delete `FollowUp` and its drain rung
- Findings: agent-session-core-6, agent-session-core-9.
- Files: `agent/queue.go:401-416 FollowUp`, `popFollowUp` (:1617), `runFollowUp` rung (`lifecycle.go:1022/1079`), pop at :1496, retirement check `session_retirement_admission.go:31`.
- Steps: Q8 is answered (DECISIONS.md): delete `Session.FollowUp`, `popFollowUp`, the `runFollowUp` drain rung and its retirement check. Red/green around the drain ladder; repoint the tests that used `FollowUp` at the real input paths (`ProcessPendingUserInput` or the client mutation path). Consolidating the three queue representations (`reflectDurableInputQueue` :665, `reflectDurableClientSteering` :1352, `daemonSourcedSteering` in `session_queue_persist.go:138`, `queues/<id>.json`) remains a design discussion with Jesse; do not start it here.
- Tests: drain-ladder tests; each repointed test names the real path it now drives.
- Docs: `docs/architecture.md` drain section.
- LOC: ~-600
- Risk: medium-high. Gate: answered (DECISIONS.md, Q8). Deps: W1-23.

### W1-68 One resolver for doc and image paths, with the artifacts-root fallback
- Findings: hub-root-rest-8 (remote image-by-path misses the artifacts root).
- Files: `doc_serve.go:57-110` (`resolveLocalDocTarget`), `doc_serve.go:423-487` (`sessionDocumentFromHub`), `image_serve.go:281-309` (`sessionImageByPath`), `output_images.go:360-398` (`readOutputImageFile`, `readOutputImageInRoot`), `image_proxy.go:80-90`, `doc_proxy.go:52-72`, `doc_serve.go:503-540` (`handleDocImage`).
- Steps: Q14 is answered (DECISIONS.md): remote image-by-path falls back to the artifacts root for absolute paths like local doc serving does. Red test first: a remote image request for an absolute path under the retained artifacts directory fails today and succeeds after. Then extract one shared resolver (cwd first, then the artifacts root, honoring the deletion fence and the contained-miss rule) that returns a typed result, and have `resolveLocalDocTarget`, `sessionDocumentFromHub` and `sessionImageByPath` map it to HTTP statuses or wire errors.
- Tests: resolver table (cwd hit, artifacts-root hit, deleted, contained miss, escape attempt) run through the doc and image routes, local and remote; assert codes and bytes, not text.
- Docs: `docs/product/subsystems.md` hub row if it names the doc and image routes.
- LOC: ~-100
- Risk: medium (path containment, security-adjacent). Gate: answered (DECISIONS.md, Q14). Deps: W0-07.

### W1-69 Fix the flaky tests seen on main (root cause first)
- Findings: not from the audit; observed on main CI during Wave 0.
- Files: 20 of the last 60 CI runs on main failed, all on tests that also pass on rerun (tally of 2026-10-25, from `c9ebba8a8f` to `e535fdcd2b`). By test:
  - browser guard `TestBackgroundTranscriptsEntrySliceBrowser` (`web-backgroundtranscriptguard`), 6 failures, times out waiting for a rail row "present and uncovered";
  - browser guard `TestAgentCascadeBrowser` (`web-cascadeguard`), 5;
  - Go `TestDelegateAttention_PrelaunchRecoveryDoesNotWaitForUnlaunchedRunner` (`agent`), 4;
  - web vitest (`web-unit`), 3, including "a pending approval suppresses the control and returns when it resolves" in `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`;
  - browser guard `TestSkillComposerBrowser` (`web-skillguard`), 2;
  - one each: `web-transcriptscrollguard`, `TestSystemdFallbackRequiresSelectedRootEvidence/retarget`, `TestHostRestartDisconnectedWithoutDeploymentDoesNotInstall`.
  - after the Wave 1 fix for the rail-row timeout, `TestBackgroundTranscriptsEntrySliceBrowser` still fails on main with "tab 1 phase 0 has missing or duplicate live output" (`0c813ad289`), which is a second root cause;
  - the Documentation workflow's `build` check (not required): the tutorial scenarios fail at `step=folder-confirm code=web-driver-failed` (`tutorial_config_test.go:357`, `tutorial_receipt_test.go:435`), seen on two test-only and docs-only PRs.
- Steps: find the root cause of each before touching it, with no retry loops, longer timeouts or skips as the fix. For each: pull the failing CI log, reproduce through the narrowest deterministic route (for the Go test, the unlaunched-runner ordering; for the vitest, the approval resolve/render ordering; for the browser guard, what the rail row waits on), write down the race or missing wait condition, then fix the production or test synchronization at that point. Prove the fix by a test that forces the bad ordering deterministically, red then green. One PR per root cause if they are unrelated.
- Tests: the deterministic forcing test per flake; CI reruns are the sample (no local stress loops).
- Docs: none; `docs/developing-evener/testing.md` only if a general waiting rule falls out.
- LOC: ~+50
- Risk: low to medium. Gate: none. Deps: none. Runs first in Wave 1: every flake costs each PR a rerun and tempts agents to merge past a red check.

## Wave 2: structural splits (design records approved)

Rules for every item here: follow the item's design record in [designs/](designs/) (`w2-NN.md`); Jesse granted full autonomy for Wave 2 and the orchestrator defaults are recorded in DECISIONS.md (Wave 2). An item starts once its listed Wave 0/1 dependencies have merged. Order inside each item is the SYNTHESIS T4 order: dead code first (Wave 0/1 already removed it), skeleton dedupe second, pure-move file splits third, leaf package extraction last and only for leaves. Split commits are pure moves: no logic edits, the function set and the symbol-to-file mapping are listed in the PR, `git diff --stat -M` and a before/after function-set diff prove it. Net LOC is about 0 for the moves; moved volume is given in parentheses. Keep `Session`, retirement and the `app_*` handlers in place (SYNTHESIS section 6). Several of these exist to make a later PR smaller, so they must land after their Wave 0/1 deletions to avoid moving dead code.

### W2-01 Split `agent/job_watch.go`
- Findings: agent-jobs-1 (high), x-error-handling-slop-2, x-error-handling-slop-1.
- Files: `agent/job_watch.go` (6,564 lines, 291 funcs, 259 commits, 11 in 30 days; eight concerns). About 460 lines at `:5709-6170` are Session methods acting on child sessions (`drainPendingWatchSends`, `driveChildrenWithUndeliveredAttention`:5753, `childStopGated`:5944); `session_jobtree_drain.go` (1,750 lines) and `subagents.go:1546 driveSubagentNotificationTurn` already own sibling logic.
- Steps: (1) pure-move commit splitting by concern (config/validation, timers, condition fire, send rail, teardown, attach scan, session glue); (2) separate commit moving the Session child-drive logic next to its siblings. (3) Only then the skeleton dedupe in W2-02.
- Questions for the discussion: the target file list; whether the Session child-drive code belongs with `session_jobtree_drain.go` or a new file; whether the `docs/architecture.md:330,389,423` anchors should move to symbol names in the same PR (recommended).
- Tests: no new tests (pure moves); the whole job/watch suite in CI is the proof; function-set diff attached.
- Docs: `docs/architecture.md` anchors, `docs/product/subsystems.md`, `friction.md` anchors.
- LOC: 0 (moves ~6,500)
- Risk: low per commit, merge-conflict heavy (259 commits of history: land fast, rebase others). Gate: design record. Deps: W0-02, W0-03, W0-12.

### W2-02 Watch skeleton dedupe: fire funnel, teardown transaction, configure
- Findings: agent-jobs-2 (high, verifier: only three sites share the breaker funnel), agent-jobs-3 (high), agent-jobs-4, agent-jobs-10, agent-jobs-11, agent-jobs-12 ("discuss first"), agent-jobs-16, agent-jobs-9.
- Files: `agent/job_watch.go` (`noteConditionFireLocked`: `onSessionEvent`:3298, `feedJobOutputWithProvenance`:3602, `fireAttachScan`:3714/3728; `clearWatch`:1639-1682, `autoClearWatchOverBudgetNotification`:2018-2037, `abandonRunningJobs` at `jobs.go:913-950`; `configureWatchWithHooks`:764-980, 217 lines, hooks used only by `job_watch_core_config_fuzz_test.go:229,250`; `validateWatchSendTarget`:1249 pass-through), `jobs.go:81-130,211-217` (ten maps/sets for send-rail delivery), `jobstore` JobRecord clone helpers (`cloneJobRecord` jobs.go:2358 shallow, `cloneActivityRecord` jobs_activity.go:1031, `cloneFoldJobs`).
- Steps: (1) `(*JobRecord).Clone()` in jobstore with a red test for depth, delete the two agent helpers (agent-jobs-9); (2) delete `validateWatchSendTarget`, collapse the two trim ifs; (3) red-first persist-failure tests for `clearWatch`, auto-clear and abandon (watch live again, delivery re-enabled), then one `rollbackWatchTeardown` helper; (4) one helper for the three condition-fire sites (breaker + send-vs-notify + `countWatchDelivery` under `jm.mu`), each site keeps its post-unlock tail; (5) split `configureWatchWithHooks` into `installWatch` and friends, updating `job_watch_timer_identity_test.go`'s AST guard; (6) send-rail state into a `watchSendRail` type guarded by `jm.mu` with no logic change, only if Jesse agrees; leave the two `deliverStableWatchSend` branches until per-branch concurrency tests exist.
- Questions for the discussion: is the send-rail type worth a new mechanism (the simplicity-over-mechanism memory); keep or drop the hooks in `configureWatchWithHooks`.
- Tests: persist-failure and watch-restore tests; preserve watch-restore semantics (R03) and activity read scope (SYNTHESIS section 6).
- Docs: `docs/job-control.md` if lifecycle prose changes.
- LOC: ~-700
- Risk: high (lock-protected state). Gate: design record. Deps: W2-01.

### W2-03 Extract Session-free leaf packages from `package agent`
- Findings: agent-misc-root-1, agent-misc-root-15 (second half), agent-subpackages-18, hub-root-rest-10 (counterpart in the hub).
- Files: `agent` (176 non-test files, 105,781 lines; 957 test files, 374,987 lines; 1,087 `Session` methods in 97 files); candidates in order: hostresources (`resourceCapsFromEnv`), memory pages (`memory_migrate.go` editors `setMemoryFrontmatterField`/`repairMemoryFrontmatter`/`memoryYAMLField` used by live `memory_save`, beside `agent/internal/frontmatter`), envsnapshot; later and contingent: transcriptview, artifactread.
- Steps: only steps 1-3 of the report as concrete PRs; steps 4-5 only if the exported surface shrinks first. Each move shrinks `agent` and removes a test-only export, never "refactor for its own sake".
- Questions for the discussion: which packages, and the rule that `Session` and retirement never move.
- Tests: moved with the code; package-level import-cycle check via `go list`.
- Docs: `docs/architecture.md` module map, `docs/product/subsystems.md`.
- LOC: 0 (moves ~3,000 per package)
- Risk: medium. Gate: design record. Deps: W0-04, W0-12, W2-01.

### W2-04 Split `contextmgr/context_manager.go` and unify journal rollback
- Findings: agent-internal-state-3, agent-internal-state-5 (scoped down), agent-internal-state-1 (after W1-66).
- Files: `agent/internal/contextmgr/context_manager.go` (2,838 lines, 104 funcs, test file 3,487; `summarizeFold` 2536-2711), `jobstore/store.go:779-806` and `delegatestore/store.go:235-262` (shared truncate+seek+sync rollback and unusable latch).
- Steps: pure moves by concern with formatter-only diffs; the rollback helper only for jobstore and delegatestore (leave sessionlog and transcript alone).
- Questions for the discussion: whether the shared rollback is worth a new package (one latch plus one function).
- Tests: seqfuzz and compaction suites; torn-tail tests for both stores.
- Docs: `design/context.md`.
- LOC: ~-100 (moves ~2,800)
- Risk: low to medium. Gate: design record. Deps: W1-66.

### W2-05 Split `delegate_runtime.go`
- Findings: agent-delegates-13, agent-delegates-9, agent-delegates-11, agent-delegates-14, agent-session-features-2.
- Files: `agent/delegate_runtime.go` (3,568 lines; 18 `delegateRuntime`, 9 `delegateTreeController` and 40 `Session` methods; process-global `delegateQuietWatchHubs` registry), `delegateRuntime.send` (:1610-1911, ~300 lines; inline-wait block at 1888-1905 and in `stableSendFailureOutcome`), `subagents.go:848-1355 prepareSubagentRunFromSelection` (~500 lines) vs `restoreIdle`, `session_jobtree_drain.go:1036-1519 drainJobTreeWith` (484 lines; `takeDrainWake` called 5 times; `if skipKick` guard 4 times).
- Steps: step 1 pure moves in one PR (update `friction.md` anchors and the `subsystems.md` S12 and `session-activity` links); step 2 (hub onto controller, injectable ticker registry) is design; differential test first for fresh-vs-restore child config (fresh prepare + freeze + restore compared field by field excluding live pointers); only if drift appears, fix as TDD bugs. For `drainJobTreeWith` keep wake-vs-verdict read order untouched, move first and simplify second; deleting `kickDriveTreeWith` means editing `session_jobtree_drain_seed100_more_test.go:81,88,114`.
- Questions for the discussion: file list; whether to remove the global ticker registry.
- Tests: function-set diff; differential config test.
- Docs: `docs/product/subsystems.md`, `friction.md`, `session-activity.md`.
- LOC: 0 (moves ~3,500)
- Risk: medium. Gate: design record. Deps: W1-22, W1-24.

### W2-06 Split session tool files and share one tool-argument decoder
- Findings: agent-session-tools-7, agent-session-tools-11, agent-session-tools-12, agent-session-tools-1, agent-session-tools-6, agent-session-tools-13, agent-session-tools-3 (rest).
- Files: `agent/session_tools_worktree.go` (3,798 lines; `worktreeRemove` 2387-2778 = 391 lines; `worktreePruneSweep1/2/3` at 3188/3555/3709; duplicate `"merge check failed: " + dErr.Error()`), `session_tools_jobs.go` (2,888 lines, 99 funcs; 110 "stable" prefixed identifiers; mirror DTO converters at :1798-1815), `session_tools.go` (1,822 lines: delegate create 97-250, vision side channel 295-675, tool-def cache 1422-1680, task/goal store accessors 1727-1790; `execTool` 773-1046, 273 lines), decoder helpers (`session_tools.go:1791,1799`, `find.go:280,294,309`, `shell.go:486,493`, `jobs.go:2244,2279,2308,2556,2607`).
- Steps: pure-move splits by lifecycle/tool (create/switch/remove/list/prune/git; per tool); `stableDelegate*` rename as a separate mechanical commit; `worktreeRemove` into guard helpers; sweeps renamed by purpose; one generic string-list helper replacing `stringArrayArg`/`jobStatusArrayArg`/`jobTypeArrayArg`, then one strict optional string/bool/whole-int set (only 7 inline `.(float64)` sites outside helpers); `execTool` pure extraction (`errorResult`, `runPreHooks`, `runPostHooks`; leave the lock-ordered emit/close block). Keep `spec §` references until the evergreen doc exists; defer mirror-DTO removal until the state JSON consumers are checked.
- Questions for the discussion: split boundaries; whether the `stable` rename is wanted.
- Tests: pure-move proof; decoder characterization (accepted/rejected inputs) before the consolidation.
- Docs: `docs/architecture.md` anchors.
- LOC: ~-400 (moves ~8,500)
- Risk: low to medium. Gate: design record. Deps: W0-02, W0-12.

### W2-07 Session lifecycle: turn loop, runtime release, lock contract
- Findings: agent-session-core-2 (high), agent-session-core-7, agent-session-core-8, agent-session-core-10, agent-session-core-12, agent-session-features-7, agent-session-features-8, agent-session-features-9.
- Files: `agent/session_lifecycle.go` (`processInputKindWithProvenance` 1118-1701, 584 lines, and `processOneInput` 1945-2607, 663 lines; `sessionEndEmitted=false` resets at :1173,1413,1579,1587,1665; emit-once `EventSessionEnd` blocks at :1352,:1687,:1718; `strings.Join(outputs)` returns at six sites), `releaseRuntimeOnce` 409-769 (361 lines, 16 retirement branches, a bare `{` at :420), `session.go:109-1125` (274 fields, 22 mutexes, `mu guards` list at `session.go:293-312` omits `restGeneration`, `restOwedGeneration`, `quietRestBeforeInpu...`), `state.go` (`settleTerminalState`:634, failure-boundary finishers :458/:472/:657), `session_config.go:318-798 testConfig` (91 fields), `session_notes_rpc.go` (1,294 lines, four files in one; canonicalizer 466-605), long functions (`stageCompactionEffects` 260, `publishFoldTransaction` 166, `prepareCompactedSkillReloads` 181, `admitCompactedSkillReloads` 158, `resumeWorktreeReentry` 202, `sessionActivityRead.summary` 146, `advanceJobs` 139, `prepareModelRequestWithError`).
- Steps: first PR is comment-only: correct the `mu guards` list and the lock-order notes. Then a no-op move commit and splitting `releaseRuntimeOnce` into `closeTerminal` and `releaseForRetirement` calling shared helpers in the same order (skip a generic `releaseStep` table: the verifier flags it as new mechanism); pull one `endSession`/`finishRun` helper for the session-end bookkeeping with the emit-once invariant tested; equivalence property test (replay a transcript through the live settle path and through restore, compare states) before touching awaiting-vs-idle derivation; peel only `restState` (own mutex, documented lock order); `testConfig` pure move to `session_testhooks.go` and delete hooks with zero users; `session_notes_rpc.go` pure file split, canonicalizer rewrite deferred; long functions one per PR with no reordering across lock phases.
- Questions for the discussion: which long functions are worth touching; whether `testing.Testing()` forks stay (the verifier says leave them unless Jesse wants them gone).
- Tests: the equivalence property test; emit-once test; existing lifecycle suites in CI.
- Docs: `docs/architecture.md` Session lock contract.
- LOC: ~-500 (moves ~4,000)
- Risk: high. Gate: design record. Deps: W1-16, W1-18, W1-19.

### W2-08 Split `execenv/local.go` and unify file-op dispatch
- Findings: agent-execenv-sandbox-1, -2, -3, -4, -5, -6, -13, -14.
- Files: `agent/execenv/local.go` (3,647 lines, 123 funcs, 84 `LocalExecutionEnvironment` methods, 31 struct fields), the 3-arm dispatch in `ReadFileAndBytesCtx` (:1591), `WriteFile` (:1742), `EditFileWithCtx` (:1804), `mutator.go:261-346`, grep/glob walk copies `local.go:2755-2870` vs `securepath_browse_fdops_unix.go:188-380`, list-dir walkers (`listDirectory`:2089, `listDirWalk` `listdir.go:237`, `walkDirFd`), `rg_capture.go` (393 lines; `buildRipgrepArgs`:2331 called only from `fuzz_fc2_grepargs_test.go`), 12 optional interfaces (`ReadFileAndBytes` test-only; `EditFileWith` only from `EditFile`), process launchers (`execPreparedCommandStdout`:2950-2985, `StreamCommand`:3064, `DetachCommand`:3182-3235), `gitignore.go:351-620` cost estimator (~300 lines, "best-effort, not a proof", issue #1971).
- Steps: denial-parity table test first, then `WriteFile` as a thin wrapper over a shared write helper, `fileTarget(tool, path, mode)` for the five dispatch sites, then the pure-move split. Step 1 on optional interfaces (delete wrapper-only `ReadFileAndBytes` and `EditFileWith`) needs no extra approval; folding Ctx/budgeted forms into `ExecutionEnvironment` does. The gitignore estimator needs Jesse (behavior).
- Questions for the discussion: interface shape; keep or replace the gitignore cost estimator.
- Tests: denial parity table across all dispatch arms; sandbox suites.
- Docs: `docs/product/subsystems.md` sandbox row.
- LOC: ~-600 (moves ~3,600)
- Risk: medium-high (sandbox). Gate: design record. Deps: W1-07.

### W2-09 Trim `explain.go`: narration comments and dead code only
- Findings: agent-internal-tools-1 (high), agent-internal-tools-2, agent-internal-tools-6.
- Files: `agent/internal/tool/repair/explain.go` (3,638 lines, 1,020 comment lines, 88 issue/review cites), `schemaOwnKeywordsMatch` (:2575, also used by `repair.go`'s null handling), `repair.go` `schemaAcceptsNull`/`branchAcceptsNull`.
- Steps: Q18 is answered (DECISIONS.md): leave the coaching text as is. The example-synthesis family (`candidateSatisfies`, `numericExample`, `snapMultiple`, `exampleValueFor`, lines 2379-3358) stays. Remove only narration comments (history, review rounds, issue ids) and code with no caller; behavior-level tests first so the model-facing output is pinned before and after.
- Tests: behavior tests asserting that the Example, when present, validates against the schema; no wording assertions; output identical before and after.
- Docs: tool-repair doc if it describes examples.
- LOC: ~-1,000
- Risk: low. Gate: answered (DECISIONS.md, Q18). Deps: none.

### W2-10 Hub and CLI composition roots
- Findings: hub-root-rest-1 (high), hub-root-rest-10, internal-misc-cmds-3 (high), hub-root-rest-14, internal-misc-cmds-4, hub-root-rest-7, x-error-handling-slop-1.
- Files: `cmd/evener-hub/main.go:253-1028 runMain` (775 lines; archive/favorite `SetOnChange` at :470-471 overwritten at :828-829; dead `pinSections` nil guard at :840; `NewPinSectionStore` never returns nil at `hubcore/pin_section.go:96`), `cmd/evener-hub` `main` package (56,197 non-test lines, 192,767 test, 463 test files; sole production importer `cmd/evener/main.go:20`), `cmd/evener/serve.go:401-2002 runServeWithDeps` (1,600 lines) and `run.go:114-516` (38+ overlapping flag names between `newRunFlagSet` in `main.go` and `serve.go`; help text drift at `main.go:360/363`), five subcommands re-implementing flag parsing (`fs.Visit` copies at `main.go:1205`, `attach.go:82`, `endpoint.go:42`, `login_url.go:36`, `restart.go:45`; `login_url.go:50-54` reads the token inline instead of `readAuthToken`), tool-fluency's copy of launch plumbing (`runnerApplyFastCheapModel` at `tools/tool-fluency/.../main.go:1357` is `applyFastCheapModel` at `serve.go:2183` with a literal message; `parseLiveSandboxNet` byte-identical to `parseSandboxNet`), launch-check subprocess handling (`spawn.go:1152-1189`).
- Steps: after the prerequisite deletions, split `runMain` into named construction phases keeping the `defer` order; flag sets shared by `run` and `serve` become one definition; the cheapest package seam is `hubconfig` (~934 lines, no `WebServer` dependency); move the tool-fluency launch helpers into `cmdutil` (and fold `PickFreshSessionID` into the shared `ProvisionSandbox`). Shutdown-order risk is real; refactor must keep defer order.
- Questions for the discussion: which seam first; whether to split the `main` package at all (hub-root-rest-10 verifier: not a standalone PR).
- Tests: a startup/shutdown order test (observe order through a recording fake at the real boundary); flag-parity test between `run` and `serve`.
- Docs: `docs/evener-hub.md`, `docs/product/subsystems.md`.
- LOC: ~-800
- Risk: high. Gate: design record. Deps: W0-07, W0-08, W1-32.

### W2-11 Hub relay closure, tree builder, host manager, sshconn manager splits
- Findings: hub-app-rest-2 (high), hub-hubcore-1 (high), hub-hubcore-12, hub-app-host-14, hub-sshconn-4, hub-app-rest-12, hub-app-rest-13, hub-app-host-5.
- Files: `cmd/evener-hub/app_relay.go:471-2299 newHubRelayFunctions` (~1,360 lines body; the 1,829 figure is its end line; shared state `relayMu`/`relayedThreads`/`pendingRelays`/`canonicalRelays`/`relayGenerations` at :491-495; ~29 closures; test-only `relayCommandCount`/`relayPublished` at :365-366, 2268-2286; six `observe*` globals at :369-378 referenced 79 times in tests), `hubcore/tree.go:921-1754 buildTreeAtWithProjects` (833 lines; 13 named closures each with one call site; overlays repeated at 1345, ~1612, ~1706-1721), `hubcore/roster.go:770-1148 refresh` (~378 lines; `ReadSpawnedThread` maps `delegate.Lifecycle` to `ThreadStatus` at :1570-1596), `app_host_manage.go` (3,854 lines, 120 funcs, 17 types), `sshconn/manager.go` (3,965 lines; seven name-keyed maps at :418-447), instance mutation lock preamble in `app_instances` (create 951, edit 1097-1431, Remove 1808-2199, `SetModelDisabled` 2619, `SetDefault` 2680), `app_force_stop.go:53-450 forceStopThread` (398 lines), `hubThreadFork` 1076-1304, 12 `manage.go` function fields back-filled from `manager` (hub-app-host-5: seven back-fill blocks at `manage.go:1469,1501,1522,1528,1534,1537,1550`).
- Steps: `newHubRelayFunctions` to a `hubRelay` struct with methods and named state, deleting the test-only members; `buildTreeAtWithProjects` into named phases (one call-site closures become functions); `refresh` with `classifyMissedProbe` and one `subagentStatesFromDelegates` plus characterization tests; file moves by subsystem; sshconn manager split after W1-34 (so less code moves), folding the three per-host maps into one recovery record only if the tests allow; `forceStopThread` and `hubThreadFork` into named phase functions guarded by existing tests (drop the `lockAliasGroup` extraction: the verifier refuted the "four copies" claim). Do not touch the crash-safe two-phase commit engine or its recovery paths.
- Questions for the discussion: the `hubRelay` state ownership; whether to delete the function-field seams in `manage.go` in favor of the `manager`.
- Tests: pure-move proofs; characterization tests per extracted phase.
- Docs: `docs/product/subsystems.md` hub rows.
- LOC: ~-600 (moves ~12,000)
- Risk: high. Gate: design record. Deps: W0-07..W0-09, W1-29, W1-30, W1-34.

### W2-12 Server and appserver runtime files
- Findings: appwire-server-12, appwire-server-13, appwire-server-7 (rest), internal-app-layer-4, internal-app-layer-2, internal-app-layer-7, x-go-ts-protocol-14, x-error-handling-slop-2.
- Files: `server/appwire_runtime.go` (3,074 lines; `handleAppThreadClear` 2013-2178, ~17 `s.mu.Unlock()` sites, nine exits), `internal/appserver/server.go` (2,327 lines; 14-hook seam; `captureSubscription` takes 8 params plus variadic; locks `mu`, `projectionMu`, `deliveryMu`, `hydrationMu`; seven context-key types at :1056-1059, :1205-1207; two unsubscribe entry points; V1/V2 resolver coexistence, local `server/server.go:506` sets V1, hub `app_rpc.go:1060` sets V2), `appwire/types.go` (5,898 lines, 365 types, 137 `Method*` consts, a generator input), 27 `Set*Func` setters.
- Steps: pure-move splits (the generated output must stay byte-identical for `types.go`); port the local resolver to `SubscriptionAdmissionResolution` and delete V1 and `safeResolveAdmission`; one unsubscribe target value under one key; add a comment at `server.go:1283` stating the closure runs under `projectionMu`. Do `handleAppThreadClear` last: nine-exit lock dance.
- Questions for the discussion: whether to split `types.go` at all (the generator reads it); `hook[T]` generic and the 50-method interface shrink (deferred by the verifier).
- Tests: generated-output freshness; appserver suites (10,406 test lines).
- Docs: protocol docs regenerate.
- LOC: ~-300 (moves ~11,000)
- Risk: medium-high. Gate: design record. Deps: W0-10, W1-20.

### W2-13 Hub state, config bags and registry parallel lists
- Findings: hub-root-rest-2, hub-root-rest-3, hub-internal-small-2, hub-internal-small-3, hub-hubcore-10, llm-registry-1, -2, -6, -7, -14.
- Files: `hub.toml` as config and machine database (reserved sections `host_records`..`mirror_commits`, `config.go:120,127,566-569,581-586`; `app_host_*` 14,242 non-test / 32,682 test lines), 26 `host_*` keys (23 undocumented; `docs/evener-hub.md:290-292` documents three), `launchconfig` (`merge.go` 31 `nonEmpty = true` blocks; `wire.go` FromWire/ToWire ~35 fields each; `args.go`, `runtime_defaults.go:106-190` 14-case switch), `hubcore/config.go` `WebConfig` (84 exported fields; 1,307 test literals; `RelayLifecycleHooks` 11 func fields), `llm/registry` (`resolve.go:746-988 resolveLayersModeLive` 243 lines with two bool flags; layer-order interleave at `resolve.go:856` and `:1415`; `write.go` 28 `setString`/`setBool` calls duplicating `schema.go` tags; `load.go:573 cloneCaps`; overlay rows `providers_overlay.toml:239-285`).
- Steps: Jesse decides single source of truth for host records (design record first, then a multi-PR effort with recovery tests); delete the 23 undocumented knobs only after Jesse confirms no deployed `hub.toml` tunes them; `launchconfig` completeness test first (every Layer field has a schema row and is covered by merge/ToArgs/FromWire/ToWire or an explicit exclusion), then generic `mergeScalar`/`mergePtr`; registry: cross-check test `modelDisabled == Resolve.Model.Disabled` over synthetic layer sets, then a shared extras-interleave helper; `write.go` struct-tag encoding behind an every-field round-trip test, abandon if output ordering or empty `api_key_env` changes.
- Questions for the discussion: Q20.
- Tests: as listed; round-trip/golden for `providers.toml` output.
- Docs: `docs/evener-hub.md`, product subsystems.
- LOC: ~-800
- Risk: high. Gate: Q20 answered (DECISIONS.md: machine records move to the `hostops` store; undocumented knobs fold into internal defaults); design record written. Deps: none.

### W2-14 Split `appwire-client` `reducer.ts` and the credentials closure
- Findings: appwire-ts-core-1, appwire-ts-state-testing-2, appwire-ts-state-testing-4, appwire-ts-state-testing-5, appwire-ts-core-7.
- Files: `appwire-client/typescript/src/reducer.ts` (5,168 lines; cache validators 1395-2020 and warning fold at 4630 do not belong), `state/credentials` closure (1,298 lines, 616 comment lines, closure 351-1297; `resetForTests` only used at `instances.test.ts:402,407,611,681`), four `*_REFETCH_DEBOUNCE_MS = 250` copies (`marketplaces.ts:91`, `plugins.ts:76`, `launchLayer.ts:59`, `authStatuses.ts:48`; private one at `instances.ts:317`), `export *` in `extensions/index.ts:7-13` vs the nav index rule, unused `createKeyedRevision`/`createListRevision`/`readRevisioned`/`writeRevisioned`/`attachLifecycle`.
- Steps: pure mechanical split of `reducer.ts` (validators first, warning fold second); delete `resetForTests`; one debounce helper; delete the unused revision helpers.
- Questions for the discussion: split boundaries; whether the package exports shrink.
- Tests: package unit tests; `make test-api-package`; web and native suites for consumers.
- Docs: package README.
- LOC: ~-500 (moves ~4,000)
- Risk: medium. Gate: design record. Deps: W1-13, W1-42.

### W2-15 Split `stores/threads.ts` and the transcript repository
- Findings: web-stores-1 (high), web-stores-16, web-stores-3 (step 2), web-stores-5, web-stores-6, web-stores-9.
- Files: `cmd/evener-hub/frontend/src/stores/threads.ts` (8,990 lines, ten subsystems, 86 module-level `let`/`Map`/`Set` singletons, `resetThreadsStoreForTests` at :8845 is a hand-maintained clear list; 68 commits since 2026-09-01; `liveControls.ts:65` says `threads.ts` cannot import it without a cycle; `tasksPanel.ts:5-9` documents a lazy reach-back), `transcriptRepository.ts` (3,108 lines; `SessionEntryCache` 1,805 lines), `refreshTrackedThread` (:6630, 8 positional params forwarded to a 9-param function), session-action boilerplate (:7973-7998, :8064-8081, six positional forwards at :7846-7850), `hostRouting.ts` 9-line "Compatibility entry point" (35 importers).
- Steps: one subsystem out at a time, leaf first, module-load wiring order preserved; replace positional parameters with an options object; reset registry only if Jesse wants a mechanism (YAGNI risk); `ForTests` exports move to a test-support module (step 1 of the `ForTests` work is W0-27).
- Questions for the discussion: first extraction (watched hydration after W1-46? tasks?); whether to keep `mutationDispatcher.ts` and `hostRouting.ts` shims.
- Tests: store suites (74,001 test lines vs 25,924 non-test) are the guard; module-load order test.
- Docs: `docs/product/subsystems.md` client store row.
- LOC: ~-500 (moves ~9,000)
- Risk: high. Gate: design record. Deps: W0-27, W1-46.

### W2-16 Web panes: Spawn, Composer, ProviderConnection, transcript scroll
- Findings: web-panes-b-1 (high), web-panes-b-2, web-panes-b-4, -5, -6, -7, -13, web-panes-a-2, web-panes-a-3 (high), web-panes-a-4, web-panes-a-5, web-panes-a-6, web-panes-a-8.
- Files: `panes/Spawn.tsx` (3,279 lines; `SpawnForm` :281-3259, 88 hooks; 11 `setBusyStartedAt(null)` teardowns; seven host-changed refs at :435, 578/1691, 603, 640-681, 1336, 1391, 1513/1555; slash-completion copy of `Composer.tsx:833-857` at :1014-1031; `splitModelId` checks at :2259, 2344, 2430), `Composer.tsx` (1,824 lines, 49 hook calls), `ProviderConnection.tsx` (2,131 lines; `SelectedConnection` from :474), `useTranscriptScroll.ts` (2,449 lines; duplicate export `captureTranscriptView` vs `transcriptViewRegistry.ts:134`), `ToolCallItem.tsx:461-468` Legacy/Provider split, four endpoint-changed constants (`ProviderConnection:58`, `InstanceSheet:111`, `instanceDialogs:64/73`, `CredentialsSection:96`).
- Steps: Spawn: extraction of pure `resolveSpawnBuiltinLaunch` in `spawnSlashMenu.ts`; shared `useSlashCompletion` beside `SlashCompletionMenu` (with a `commitOnPlainEnter` option); one `useHostEpoch(submittedSource)` replacing the seven refs, one ref per commit, guarded by the A-B-A switch tests; one `finishBusy()` for the 11 teardowns; only then split the component. Transcript scroll: pure moves first, rename element-level `captureElementView`, then the hook split; do not strip references to `docs/web-ui/parity/contracts-*.md` (they exist); drop only "this rewrite"/"w4 fix-round" provenance. ToolCallItem: always use `useTranscriptRenderContext()` and keep `thread` passed by props from the parent, add a context-change re-render test. Endpoint-changed: share only the claim/holds logic (`DestinationClaim` vs `draftIdentity`), keep per-surface copy local.
- Questions for the discussion: component boundaries; no new widget or widget prop without asking (AGENTS.md).
- Tests: existing slash and spawn tests; geometry via `make test-web-browser` where layout moves.
- Docs: `docs/developing-evener/design-system.md` only if widget usage changes.
- LOC: ~-1,000 (moves ~9,000)
- Risk: high. Gate: design record. Deps: W1-44, W1-45.

### W2-17 Rail and AppShell
- Findings: web-shell-widgets-1, -2, -3, -4, -5, -12, web-shell-widgets-6.
- Files: `shell/Rail.tsx` (adapter fork: non-normalized path in `loadedSection`:628-646, `projectsFor`:827-846, `projectFromSummary`:655-697 mode 'compatibility'; `NavigationRail` :1010-2130), `AppShell.tsx` (1,135 lines; live-demand effect :638-844 with round/PR citations at :605-827), `dockHostChunk.ts` (216 lines, private copy of `chunkRetry.ts`, 256), `CommandPalette.tsx:101-135 HELP_ROWS` vs the registry cheatsheet (`CheatsheetOverlay.tsx:1-12`), `needsYouActions.ts:168-170,407-410,430-433`.
- Steps: make `Rail.test.tsx`'s `resource()` seeding build normalized resources (helper at :168), then delete the non-normalized branches, `projectFromSummary`, the mode discriminator; extract live demand to `shell/liveSessionController.ts` with injected reads after porting the `AppShell.test.tsx` cases; collapse `dockHostChunk.ts` onto `chunkRetry.ts`; derive `HELP_ROWS` from `keybindingDefaults.ts`.
- Questions for the discussion: controller boundary and the injected reads.
- Tests: port AppShell cases to controller tests first; geometry guard.
- Docs: none beyond comments.
- LOC: ~-700 (moves ~3,500)
- Risk: high. Gate: design record. Deps: W1-38.

### W2-18 Fold `mobile/` into `mobile-native/src` and split the phone screens
- Findings: native-rest-3, native-tests-legacy-mobile-1, x-client-duplication-5, native-tests-legacy-mobile-3 (high), native-tests-legacy-mobile-4, native-rest-1 (high), native-session-board-1 (high), native-session-board-3, native-session-board-7.
- Files: `mobile/src` (12 files; no `package.json`; 145 `mobile-native` files reference it; `services/conversation.ts:40-41` and `state/conversation.ts:65` import `mobile-native/src/projectedRows`, a cycle), `mobile/src/state/conversation.ts` (3,721 lines; `createConversationStore` 596-3660, 40 inner functions, 26 `let` bindings; `rehydrate` 2206-2612; `send` :3092 and `queue` :3197 near-identical, `interrupt` :3285 differs), `mobile-native/src/screens.tsx` (`ConversationScreenView` :413-4031, 3,600 lines, 121 hook calls, 36 `useEffect`), `board/BoardScreen.tsx` (1,994 lines; `Board()` 173-1260; render-time ref writes at :311, 313, 315, 332, 458, 842), two `confirmShutDown` with different copy (`BoardScreen.tsx:1396`, `SessionInfoSheet.tsx:70`), 14 hand-rolled subscribe/listeners/revision stores (40 non-test files with listeners sets, 62 `subscribe` definitions).
- Steps: first W1-43 deletes the write-only activity store. Then lower-cost option first: move `projectedRows` and `transcriptPresentation` into `mobile/src` so dependencies run one way (3 import edits plus ~14 native consumers), or `git mv` `mobile/src` into `mobile-native/src`. Leaf components in `BoardScreen.tsx` (`HubButton`:1703, `Chips`:1750, `SummaryLine`:1826, `IdleFold`:1901, `Skeleton`:1947, `EmptyBoard`:1977) are already module-level, so step 1 is a pure move; pure row transforms in the conversation store (`seatTransientWarnings`:1160, `reanchorTransientWarnings`:1253, `reconcileCrossTurnReissues`:821, `splitClusterAtAnchors`:1125, `capAndTruncate`:1043) move out first; shared `runMutation` for `send` and `queue` only, guarded by the parameterized tests at `conversation.test.ts:1048`; one `createSubscribable()` helper for the 14 stores only if it is smaller than what it replaces.
- Questions for the discussion: fold or fix direction; whether `ConversationScreenView` becomes several screens or one screen with hooks.
- Tests: `make test-native`; pure-move proof.
- Docs: `mobile-native/README`, `docs/product/subsystems.md` phone rows.
- LOC: ~-800 (moves ~12,000)
- Risk: high. Gate: design record. Deps: W1-43, W1-42.

### W2-19 Split oversized test files
- Findings: go-tests-rest-12, go-tests-agent-11, web-dev-and-tests-8, web-panes-b-13, web-stores-17, native-tests-legacy-mobile-5, native-tests-legacy-mobile-12, native-session-board-14, hub-sshconn-18, go-tests-rest-13, go-tests-rest-15.
- Files: `cmd/evener-hub/app_rpc_test.go` (13,451 lines, 215 tests; helpers at :12642-12677), `agent/internal/execenv/glob_budget_test.go` (3,096 lines, 75 tests, 18 `TestLoadIgnoreSetRefuses*`), `Spawn.test.tsx` (11,489 lines, 282 tests, no `describe`), `AppShell.test.tsx` (8,914), `threads.sessionActions.test.ts` (10,661), `mobile/src/state/conversation.test.ts` (20,457 lines, 426 `it`, 91 `describe`, 31 with tracker labels), `ConversationScreen.send.test.tsx` (7,195), `BoardScreen.test.tsx` (5,871; 17 `vi.mock`), `PluginsPage.test.tsx` (3,743); 27 web test files over 2,000 lines (65 over 1,000, 165,270 lines); hub fixtures (`WebConfig{` 1,307 literals vs 285 `NewWebServer(`); sshconn fake runner parsing command text (`fakes_test.go requireProtocolUpgrade`, 435 `runFn` refs).
- Steps: pure moves of whole `describe` blocks or test groups into topical files, helpers into a sibling `_helpers_test` file; a test-only `fakeHost` argv-to-operation classifier in `fakes_test.go` and port sshconn tests in slices (add production exports only if a caller genuinely needs them); `newTestWebServer` defaults roots to `t.TempDir()` in one place.
- Questions for the discussion: priority order and whether splitting ahead of the source-file splits is worth the churn.
- Tests: test count and pass set equal before/after, listed in the PR.
- Docs: `docs/developing-evener/testing.md` file layout section.
- LOC: 0 (moves ~110,000)
- Risk: low (mechanical), huge diffs. Gate: design record. Deps: W1-55.

## Decisions that are Jesse's

Answers so far are recorded in [DECISIONS.md](DECISIONS.md).

Each is a product or policy call, not a refactor. Every item is answered, and each answer points to [DECISIONS.md](DECISIONS.md). The "Default" text on an answered item is the pre-answer assumption and no longer applies.

1. **Q1. Version-skew and state-migration policy.** Which hub, client and daemon versions must interoperate (daemons outlive the hub that spawned them per `appwire/types.go:30-35`), and how long on-disk formats are migrated. One written window turns about 46 "older hub" fallbacks, the plugin-store and serf migrations, the MEMORY.md migration, the legacy transcript projection and checkpoint formats, and young on-device formats into keep/delete answers. Unblocks W1-59..W1-64 and part of W1-40. Default: nothing is deleted. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q1): the hub tolerates any older daemon; phone and local-hub code tolerate an older (remote) hub; state migrations stay. Shapes W1-59..W1-64 and W1-40(c).
2. **Q2. Coverage floor as a gate.** Is `make coverage-floor` a required ratchet or an unenforced number? If it is not a gate, the coverage-wave files (about 38k lines repo-wide) and the 39-40 hub `cov_*` files can go after one before/after report. Unblocks W1-54. Default: leave them. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q2): the floor is not a protected gate; W1-54 is unblocked.
3. **Q3. Is `llm` a public SDK?** If it is not, the streaming `Generate` family and the multi-round tool loop (production callers pass `Tools: nil`) are dead. Unblocks W1-27. Default: keep. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q3): `llm` is a public SDK; keep both; W1-27 is dedupe only.
4. **Q4. Home for the shared durable-write helper and its fsync default.** `execsupport` is required by root, `agent` and `llm` but not `auth`; `cmd/evener-hub/internal/fsdurability` cannot be imported from `server`, `agent` or `auth`. Options: a package in `execsupport` (and add the dependency to `auth`), or in `envvars`, or a new tiny module. Also whether durability is always on with an explicit no-fsync variant for caches. Unblocks W1-01..W1-03. Default: `execsupport/fsatomic`, always-on, explicit no-fsync variant. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q4): `execsupport`, durable by default; plus G7: `afero` phased out.
5. **Q5. Fallback when HOME is unset.** Today it is `os.TempDir()` in some packages, `"."` in others. One value is needed for the shared state-root resolver (W1-04). Default: keep each caller's fallback until answered. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q5): a clear error, no fallback (W1-04).
6. **Q6. Web and phone stuck thresholds.** Web `describeLiveness` uses 20s quiet / 180s stuck; the package and phone use 10 minutes. Should they differ on purpose, and should "stuck" be suppressed while subagents run? Unblocks W1-39. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q6): one 10-minute rule, never stuck while delegates run (W1-39).
7. **Q7. Attention precedence for `approval_pending` with `restartRequired` or ended.** The hub reads it as awaiting, native reads it as restart needed. Unblocks W1-38 phase 2. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q7): restart-needed or ended wins over `approval_pending` (W1-38).
8. **Q8. Delete `FollowUp`, and consolidate the three queue representations?** The drain ladder depends on `FollowUp`; queue authority is design work. Unblocks W1-67. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q8): delete `FollowUp` and its rung; queue consolidation stays a design discussion (W1-67).
9. **Q9. The four experimental context strategies** (`obs-mask`, `checkpoint-pred`, `memory-crystals`, `recursive-distill`): delete? Unblocks W1-66. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q9): delete all four (W1-66).
10. **Q10. UI dead weight.** (a) the two mockup harnesses (about 2,750 lines) are final, (b) delete the four unused widgets, (c) delete the write-only phone `ActivityStore` after the rehydrate-trigger check, (d) keep or drop the ~10k-line demo hub fixture (`native-tests-legacy-mobile-13`). Unblocks W0-05, W1-43, W1-47. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q10): (a) delete, (b) delete, (c) delete after the rehydrate check, (d) keep the demo hub fixture.
11. **Q11. Gate tooling that exists but may not be worth its weight.** The timing ratchet (rebaseline with a `metric` key, or delete 664 lines of script and 1,231 of tests), the prose-pinning and Makefile-text audit tests (about 5.5k lines), the fuzz mutation toolkit (`ledger.json` and `buckets.json` are both `{}`), the generated Make-targets table renderer (3,642 lines), unused `internal/devtool/gatesurface`. Unblocks W1-56. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q11): delete none of the gate tooling; rebaseline the timing ratchet and fix what is broken (W1-56).
12. **Q12. Contributor rules wording, and a second copy-oracle tier.** Approve the five AGENTS.md rules in W0-01; decide whether the copy-oracle ratchet should also cover error strings (W1-49) or stay at "shows X" names. Answered ([DECISIONS.md](DECISIONS.md), Q12): the AGENTS.md wording is approved (W0-01), and the copy-oracle ratchet also covers error strings, with an allowlist of today's sites (W1-49).
13. **Q13. New test-support packages.** Create `llm/llmtest` and a `gittest` package only if copy counts stay above ten after adopting existing kits. Unblocks W1-52. Answered as an orchestrator default, not Jesse's call ([DECISIONS.md](DECISIONS.md), Q13): adopt the existing kits first; create a package only if more than ten copies remain (W1-52).
14. **Q14. Remote image-by-path and the artifacts root.** Local doc serving falls back to the artifacts root for absolute paths; remote `sessionImageByPath` does not. Should it? Affects hub-root-rest-8 (not scheduled until answered). Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q14): fall back to the artifacts root through one shared resolver (new W1-68).
15. **Q15. Fail-closed behaviors vs the recovery-first guide.** The endpoint-fingerprint guard default, one malformed plugin command file dropping the whole plugin, and legacy delegate state refusing startup. Unblocks W1-65. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q15): all three switch to recovery (W1-65).
16. **Q16. Ref grammar.** Is `:` allowed inside a session id? `hubapi.ParseRef` accepts it, `appwire.ParseRef` rejects it. Unblocks W1-36. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q16): reject `:` in session ids; one strict `ParseRef` (W1-36).
17. **Q17. Behavior-visible small fixes.** Report ambiguity for plugin slash commands (W0-18) and let `latest_intent` flow on the detailed-status path (W1-10). Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q17): report ambiguity with a sentinel error (W0-18); `latest_intent` flows on the detailed-status path (W1-10).
18. **Q18. Slimming `explain.go`.** It changes model-facing coaching text (examples validated against the schema or omitted). Unblocks W2-09. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q18): leave the coaching text; trim comments and dead code only (W2-09).
19. **Q19. Plan and spec corpus rule.** "Delete the plan in the PR that completes it", and approval to delete plans not cited outside `docs/superpowers`. Unblocks W1-58. Answered 2026-10-25 ([DECISIONS.md](DECISIONS.md), Q19): delete merged, untouched-for-30-days plans; keep specs; delete a plan in the PR that completes it (W1-58).
20. **Q20. `hub.toml` as machine database, and the 23 undocumented retention knobs.** One source of truth for host records is a design record first; the knobs can go if no deployed hub tunes them. Unblocks W2-13. Answered (DECISIONS.md).

## Facts re-checked while writing this plan

Read-only commands against `origin/main` at `a6c0b9c561`: `grep -c '"metric"' testing-budget.json` returns 0; `internal/bundled/bundled_test.go:9` is the only importer of `primeradiant.com/evener/frontmatter` and `testing-budget.json:96` lists it; `agent/jobs.go:991` is the only `createShell` definition; non-test `nolint:unused` lines are agent/jobs.go 6, session_tools_jobs.go 4, sandbox_delegate.go 2, job_watch.go 1, subagent_manager.go 1, internal/plugins/enabled.go 1 (15); `.golangci.yml` enables no `nolintlint` today; `go.work` modules are `.`, `agent`, `auth`, `envvars`, `execsupport`, `fuzz`, `identifier`, `invariant`, `llm`, and `execsupport/go.mod` requires only `golang.org/x/sync` (`agent` and `llm` require `execsupport`, `auth` does not); `hubapi/client.go` is at the repo root, not under `cmd/evener-hub/internal`; `waitHealthy` is in `cmd/evener-hub/internal/sshconn/version.go`; the mockup harness files exist under `cmd/evener-hub/frontend`; `docs/superpowers` has 481 plan files and 441 spec files. Everything else is taken from the verifier notes in `verified/*.json`, which carry their own spot-check results; counts the verifiers marked unverified (75 test-only functions, the 38k coverage-wave total, `~53` registry wording asserts) are labelled as such above.

Disagreements between verifiers that this plan resolved: (1) `hostreg.SameRegistration` (hub-internal-small-12 says include the epoch, hub-sshconn-6 says add a new function and leave `SameRegistration` alone because other callers carry no epoch; this plan follows hub-sshconn-6, and note hub-internal-small-12 itself found no latent fence hole, so SYNTHESIS section 2 overstates it as a defect); (2) `createShell` (the original plan converts 238 call sites to real shells, the verifier plan moves the helper to a `_test.go` file; this plan follows the verifier); (3) bearer-token empty semantics (SYNTHESIS lists it as a defect, the verifier calls the difference intentional per guard; this plan demotes it to an optional parse-sharing PR).
