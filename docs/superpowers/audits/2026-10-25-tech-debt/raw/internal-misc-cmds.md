# Tech-debt audit: internal-misc-cmds

Scope: internal/plugins, selfupdate, devtool, maketargetsdoc, hubendpoint, credentials, binresolve, httpguard, remoteinstall, e2ecap, fuzzroutes, bundled; cmd/evener, evener-dev, evener-doctor, evener-migrate, evener-transcript-v2-upgrade, llmcall, evener-tomlcheck, evener-internalcheck; auth, cmdutil, envvars, execsupport, identifier, rendezvous, frontmatter, invariant, buildinfo, tools/ (non-test).

Worktree: .worktrees/debt-audit at origin/main (a6c0b9c561). Read-only audit; no tests were run. Static reads, grep, wc, git log only.

## Overview and inventory

Non-test Go LOC / test Go LOC (wc -l):

| Package | Files | Prod LOC | Test LOC |
|---|---|---|---|
| internal/plugins | 35 | 8858 | 22862 |
| cmd/evener | 24 | 7349 | 28851 |
| cmd/evener-dev | 18 | 4476 | 8015 |
| tools/ | 38 | 4658 | 6587 |
| auth | 11 | 2140 | 5305 |
| internal/selfupdate | 6 | 1229 | 2439 |
| cmd/evener-doctor | 2 | 1158 | 3955 |
| execsupport | 9 | 1087 | 1489 |
| internal/maketargetsdoc | 6 | 928 | 2714 |
| internal/hubendpoint | 9 | 927 | 1227 |
| cmdutil | 10 | 883 | 2116 |
| cmd/evener-migrate | 3 | 801 | 1959 |
| identifier | 5 | 699 | 2012 |
| rendezvous | 5 | 482 | 1453 |
| envvars | 4 | 473 | 490 |
| cmd/llmcall | 1 | 432 | 951 |
| cmd/evener-tomlcheck | 1 | 421 | 583 |
| cmd/evener-transcript-v2-upgrade | 1 | 408 | 995 |
| internal/devtool | 13 | 1822 | 2425 |
| internal/credentials | 1 | 225 | 790 |
| cmd/evener-internalcheck | 1 | 210 | 609 |
| binresolve, httpguard, remoteinstall, e2ecap, fuzzroutes, bundled, frontmatter, invariant, buildinfo | 9 | ~390 | ~1500 |

About 213 non-test Go files and roughly 41k production lines in the slice, against about 97k test lines.

Biggest hotspots by size:
- cmd/evener/serve.go 2489 lines, one function (`runServeWithDeps`) of 1597 lines.
- internal/plugins/marketplaces.go 1670, marketplace_migration.go 1282, resolve.go 1146.
- cmd/evener-dev/agentshards.go 1353.
- tools/tool-fluency/cmd/evener-fluency/main.go 1876 (67 funcs).
- Functions over 200 lines: runServeWithDeps 1597, run 401, runShards 350, reconstructEntries 263, EditMarketplace 233 (marketplaces.go:821), runPluginLifecycle 211, installExtractedBinaries 208, resolveForLaunch 201.

Where the debt concentrates: (1) the same small primitive re-implemented per package (atomic file write, XDG state root); (2) cmd/evener serve vs run twin startup code; (3) migration and compat code for formats that predate the product's current layout; (4) coverage-driven test machinery (seams, "fuzz" targets that ignore their input); (5) one-off dev/recovery tooling that outgrew its need.

Product ownership notes honored in the fix plans: S20 installation and S21 diagnostics own selfupdate/remoteinstall/doctor; recovery-over-lockdown means migration removals must keep an automatic recovery path or get Jesse's explicit sign-off (flagged where relevant). Anything that removes backward compatibility needs Jesse's approval first, and the findings say so.

---

## Findings

### internal-misc-cmds-1: Seven hand-rolled "atomic write" implementations with different durability guarantees (severity: high, cross-cutting)

Locations:
- internal/plugins/atomic.go:32 `atomicWriteFile` (O_EXCL temp, fsync file, rename, best-effort dir fsync that ignores errors; 6 injectable seam vars at :18-25)
- internal/credentials/store.go:196 `(*Store).save` (afero.TempFile, fsync file, rename; NO directory fsync, NO chmod)
- auth/openai/storage.go:171 `WriteAuthFile` (CreateTemp, chmod 0600, fsync, rename, STRICT dir fsync with ENOSYS/ENOTSUP/EINVAL tolerance at :229 `authSyncUnsupported`)
- internal/hubendpoint/runtime.go:129 `Publish` + :196 `syncDir` (CreateTemp, chmod, fsync, rename, dir fsync with its own copy of the same errno tolerance)
- rendezvous/rendezvous.go:96 (fixed `target + ".tmp"` name, WriteFile + Chmod + Rename, no fsync at all)
- cmd/evener-dev/agentshards.go:750 `writeFileAtomic` (CreateTemp, chmod 0644, no fsync)
- cmd/evener-migrate/convert.go:242 (fixed `path + ".tmp"`, WriteFile, Rename, no fsync)
- also internal/selfupdate/update.go:820 `stageExecutable` / :859 `swapSymlink` (temp + rename variants)

Repo-wide the same grep finds more: llm/registry/refresh.go:48, agent/execenv/securepath_fdops_unix.go:916, cmd/evener-hub/internal/hubcore/state_file.go:35, launchconfig/io.go:53, hostops/store.go:1101 and quarantine.go:848, hubedge/auth_token.go:106, internal/transcriptindex/index.go:1294, agent/task/task_store.go:345, agent/schema/snapshot.go:641 and others (43 `O_EXCL` sites in non-test code, about 18 write-temp-then-rename routines).

Evidence: the comment at auth/openai/storage.go:196-199 itself says it is "the write-then-flush-the-parent step the hub's atomic-rename stores take" and credentials/store.go:190-195 documents leaking `credentials.toml.tmp-*` on crash as "the accepted cost". `credentials.toml` holds API keys yet is the one store here with no parent-directory fsync, while the less sensitive hubendpoint record gets one. rendezvous and evener-migrate reuse a fixed `.tmp` name, which is exactly the attacker-plantable name credentials/store.go:186-195 argues against. Errno-tolerance (`ENOSYS||ENOTSUP||EINVAL`) is copied in storage.go:229 and hubendpoint/runtime.go:200.

Impact: behavior drifts silently (crash durability, tmp-file leakage, permissions) depending on which copy a feature happened to copy; every new file store picks a variant at random; plugins carries 6 package-level seams only to fault-inject its copy.

Fix plan: add one `atomicfile` package in the lowest module that all callers can reach (envvars is the lowest-level module already hosting `userdirs`; alternatively a new tiny module next to execsupport). API: `Write(path string, data []byte, opts Options)` with `Options{Perm, Fs afero.Fs (optional), SyncDir bool=true}`, exclusive random temp name in the same directory, fsync file, rename, fsync parent with the single shared errno tolerance, temp cleanup on every error path. Migrate one caller per commit starting with credentials (add the missing dir fsync, covered by a behavior test that the file lands with 0600 and no temp is left), then auth/openai, hubendpoint, plugins, rendezvous, evener-dev, evener-migrate, then the out-of-slice stores. Delete the per-package seams and the fake-`atomicFile` test double in internal/plugins/coverage_atomic_git_fuzz_unix_test.go:19-30 once the helper has its own fault-injection tests. Tests: table test on the helper for permission, no-temp-left, overwrite, and crash-between-write-and-rename leaves the old content; no new tests per caller beyond keeping their existing behavior tests green. Docs: note the helper in docs/developing-evener conventions; no product docs change.

Size: about +120 for the helper and its tests, about -350 net across callers (the plugins/auth/credentials/hubendpoint copies are 40-70 lines each plus seams).

Risk: low-medium. Differences that matter must be preserved via Options: credentials temp-name policy, 0644 for the survey cache, afero injection for credentials/rendezvous tests.

---

### internal-misc-cmds-2: XDG state/config root resolution copied into about nine packages with drifting fallbacks (severity: high, cross-cutting)

Locations (all opened):
- rendezvous/rendezvous.go:42-62 `DefaultDir`; falls back to "." when the home dir fails; reads `XDG_STATE_HOME` raw
- auth/openai/storage.go:90-105 `DefaultStateDirWithStateHome`; uses `Trimmed()`; falls back to `os.TempDir()`
- cmdutil/statedir.go:25-60 `StateRootFromLookup` (the "canonical" one: raw value, falls back to ".")
- cmdutil/legacymigration.go:79,88 (re-reads XDG_CONFIG_HOME/XDG_STATE_HOME)
- cmd/evener-migrate/main.go:129-137 (third copy of both bases)
- agent/doctor/statedir.go:29 (falls back to TempDir), agent/runtime_dir.go:75 `xdgStateHome` (TempDir), appwire/frame_recorder.go:95 `recorderStateRoot` (relative `.local/state`), cmd/evener-hub/config.go:389 `DefaultStateGlob` ("."), cmd/evener-hub/spawn.go:828-840
- cmd/evener-doctor/main.go:120,148 help text restates the chain

Evidence: three different home-failure fallbacks ("." vs `os.TempDir()` vs relative path) and two different treatments of whitespace (`Trimmed()` vs raw). rendezvous/rendezvous.go:42-47 and appwire/frame_recorder.go:101-103 both carry a "duplicated here to keep this package free of cmd helper layer dependency" comment, but the dependency-free home already exists: envvars/userdirs/userdirs.go has `ConfigRoot(xdg, userHomeDir)` and `DefaultConfigRoot()` and no state-root counterpart. cmdutil/userdirs.go:24-29 `DefaultSkillsDir`/`DefaultPluginsRoot` also duplicate `userdirs.UserSkills`/`userdirs.PluginStore`.

Impact: the hub and daemon can disagree on where "the state root" is when the home dir is unset or XDG_STATE_HOME is whitespace; rendezvous (daemon discovery) is exactly where disagreement strands a session.

Fix plan: add `userdirs.StateRoot(xdgStateHome string, userHomeDir func() (string, error)) string` and `DefaultStateRoot()` beside `ConfigRoot`, decide one fallback policy (recommend: empty string on home failure, matching `ConfigRoot`, with callers choosing their last-resort), and make cmdutil.StateRootFromLookup, rendezvous.DefaultDir, auth/openai, evener-migrate, legacymigration and the out-of-slice copies call it. Delete the "duplicated here" comments. Replace `DefaultSkillsDir`/`DefaultPluginsRoot` with the userdirs functions. Tests: one table test in userdirs for the matrix {XDG set/unset/whitespace} x {home ok/fail}; existing per-package tests stay as behavior checks. Fixing the fallback is a behavior decision across packages: raise with Jesse (one question: which last-resort fallback).

Size: -150 across callers, +40 helper/tests.

Risk: medium for the fallback unification (behavior change when HOME is unset); none for the pure de-duplication if the policy keeps today's cmdutil result.

---

### internal-misc-cmds-3: `evener serve` is a 1597-line function and `evener run` re-implements the same startup in parallel (severity: high)

Locations:
- cmd/evener/serve.go:401-1998 `runServeWithDeps` (1597 lines; 44 flag declarations at :402-460; closures for retirement, shutdown, bridge, notes, etc.)
- cmd/evener/run.go:114-515 `run` (401 lines)
- cmd/evener/main.go:335-380 `newRunFlagSet` (flags for run)
- SessionConfig literals: run.go:~345-381 vs serve.go:~690-722

Evidence:
- 30 flag names are declared in both `newRunFlagSet` (main.go) and serve.go: agent, api-log, context-strategy, cpu-profile, dir, disable-memory, export-atif, export-atif-provider-handles, fast-cheap-model, max-concurrent-delegates, max-retained-terminal, max-rounds, max-subagent-depth, model, openai-responses-continuation, output-schema, provider-idle-timeout, reasoning-effort, result-tool-name, resume, resume-last, sandbox, sandbox-net, share-task-store, state-dir, system-prompt, system-prompt-as-user, trace, verbose, vision-model. Help text has drifted: run's `--agent` says "default (default), explorer, or another available agent name" while serve says "agent persona name (default: default)"; `--context-strategy` lists "compact|session-log|ooda" in run and nothing in serve.
- Both call the same ~15 startup steps in the same order (ParseProviderIdleTimeout, parseAPILog, resolveOpenAIResponsesContinuation, ResolveReasoningEffort, MaxRoundsToConfig, rejectPluginSelectionWithResume, configureSandbox, provisionSandbox, startupInterrupted(stepProvisioningSandbox) with `DisposeSessionScratch`, ResolveSessionMeta, applyFastCheapModel, applyVisionModel, buildInitialProfile). Each appears once in each file.
- The SessionConfig population including the four `if x >= 0 { cfg.MaxSubagentDepth = x }`-style conditionals (run.go:~388-400, serve.go:~724-736) and the sandbox provisioning/cleanup block (run.go:~410-425 vs serve.go:~738-756) are near line-for-line copies, with the serve copy's comment elaborated separately.
- The 1597-line function mixes flag parsing, resume resolution, session construction, a retirement state machine, shutdown budget logic and server wiring; its deps struct (`serveDeps`, serve.go:171) exists to make it testable, which is why there are about 40 serve_*_test.go files.

Impact: every new CLI flag or startup invariant must be added twice with matching copy; the two paths have already drifted in help text and ordering comments. A 1600-line function is un-reviewable and RoboRev-resistant.

Fix plan (behavior-preserving, test-guarded by the existing serve_*/run_* suites):
1. Extract `type sessionLaunchFlags struct` plus `registerSessionLaunchFlags(fs)` in a new cmd/evener/launchflags.go owning the 30 shared flags, help text once. `newRunFlagSet` and serve both call it.
2. Extract `buildSessionConfig(flags, env, project, ...) (agent.SessionConfig, error)` covering the shared SessionConfig population, sandbox configure/provision, and the `>= 0` overrides.
3. Split `runServeWithDeps` into: `parseServeFlags`, `resolveServeSession` (resume decision), `startServeSession`, `serveLoop`, `shutdownServe`, each under ~200 lines; move retirement/shutdown closures into methods on a `serveRuntime` struct.
4. Delete the twin comments; update docs/environment-variables.md only if flag text changes (it should not).
Order: step 1 (pure flag move), then 2, then the split in 3 one phase at a time. Each step is mechanical and covered by existing tests.

Size: about -400 net lines from run/serve duplication; the split is about 1600 lines moved with no net change.

Risk: medium (large diff), low semantic risk if kept mechanical. Do not combine with behavior changes.

---

### internal-misc-cmds-4: tool-fluency re-implements cmd/evener launch plumbing, with a drifted copy of `applyFastCheapModel` (severity: medium)

Locations:
- tools/tool-fluency/cmd/evener-fluency/main.go:1349-1369 `runnerInitialProfile`, `runnerApplyFastCheapModel`
- cmd/evener/serve.go:2128-2200 `buildInitialProfile`, `resolveInitialProfile`, `applyFastCheapModel`, `errFastCheapProviderUnavailable`
- tools/.../main.go:1099-1170 `parseSandboxFlags`, `configureLiveSandbox`, `parseLiveSandboxNet`, `provisionLiveSandbox`, `provisionLiveSandboxWithHost`
- cmd/evener/sandbox.go:62-135, 165-175 `configureSandbox`, `provisionSandbox`, `provisionSandboxWithHost`, `parseSandboxNet`

Evidence: `runnerApplyFastCheapModel` is the same body as `applyFastCheapModel` except the error is built with a literal string instead of the `errFastCheapProviderUnavailable` sentinel (a test-visible difference: callers cannot `errors.Is` it). `parseLiveSandboxNet` and `parseSandboxNet` are byte-identical switch statements. The fluency code comments say "matching the production CLI (cmd/evener configureSandbox ...)" and "exactly as the production CLI does", i.e. the author knew these were copies and could not import package main. The provision copy has also lost the `agent.PickFreshSessionID` call that the production path performs first (sandbox.go:96-110 vs main.go:1162-1169).

Impact: the tool that validates tool fluency against the real runtime can diverge from the runtime's launch semantics without any test noticing.

Fix plan: move `ApplyFastCheapModel`, `ApplyVisionModel`, `ResolveInitialProfile`, `ParseSandboxNet`, `ConfigureSandbox` and `ProvisionSandbox(WithHost)` into cmdutil (it already owns `ResolveProfile` and `BuildResolveProfile`); cmd/evener and evener-fluency both call them. Delete the runner*/live* copies (about 90 lines). Tests move with the functions (the sentinel errors become cmdutil tests using errors.Is).

Size: about -120 net.

Risk: low. cmdutil already imports llm and provider; sandbox/agent imports need a check for a dependency cycle (cmdutil imports appwire and schema already).

---

### internal-misc-cmds-5: Plugin-store rename/metadata migrations for pre-current layouts run on every store lock (severity: medium, needs Jesse's decision)

Locations:
- internal/plugins/marketplace_migration.go (1282 lines; 439 of them comments; file header essay at :14-37)
- internal/plugins/metadata_migration.go (343 lines)
- internal/plugins/locks.go:79-95 `lockStore` calls `migrateMarketplaceNames()` and `ensureManagedContentMigrated(ctx)` on every acquisition
- internal/plugins/marketplaces.go:426-460 `migrationFailed`, `migrationFailedErr`
- tests: marketplace_migration_test.go 4261 lines, migration_preservation_test.go 774, metadata_migration_test.go 590, metadata_migration_alias_test.go 170

Evidence: the migration handles marketplaces recorded "by an older evener" under a name containing '@' (marketplace_migration.go:14-18), with interrupted-rename marker recovery. git log shows the whole feature landed 2026-09-12 (#1065, #1180) and was extended 2026-09-17 (#1825); the product's first evener-named release is v0.2.0 (selfupdate/update.go:36-40 comment). 1625 prod lines plus 5795 test lines (7420 incl. prod) exist solely so "no operation has to ask which evener wrote the name" (marketplace_migration.go:21-23). `lockStore` makes every mutating plugin operation first read the marketplaces file and run the migration check, so a corrupt marketplaces file now fails even `plugin disable` or `gc` (locks.go:70-76 says exactly that). `loadMigratedMarketplaces` (marketplace_migration.go:56-70) adds a lock acquisition to read-only listings when a marker exists.

Impact: the largest single block of conditional complexity in the plugins package; 33% of the file is comment narrative about edge cases of a layout that only a few installs can have; it is the main reason EditMarketplace is 233 lines and the package has about 75 seam vars.

Fix plan: ask Jesse whether stores written before a chosen cut-over date still exist anywhere. If not (or if "validate on load and tell the user" is acceptable): replace the rename machinery with a validity check in `loadMarketplaces` that surfaces refused names through evener-doctor and the existing recovery banner, then delete marketplace_migration.go, metadata_migration.go, `lockStore`'s migration calls, and their tests. This honors recovery-over-lockdown only if the doctor path gives an automatic or one-command repair, so keep a minimal `plugin doctor --fix` or fail with a clear remediation. This is backward-compat removal and must not proceed without Jesse's explicit approval. Docs: update docs/product/subsystems.md plugin-store row and the plugins package doc.

Size: about -1600 prod, -5800 test if fully removed.

Risk: medium. Deleting a migration strands any remaining old-layout store; mitigated by a dated cut-over and the doctor path.

---

### internal-misc-cmds-6: Serf-era and pre-registry migration tools shipped as permanent commands (severity: medium, needs Jesse's decision)

Locations:
- cmd/evener-migrate/main.go (399 lines), rewrite.go (143), convert.go (259; pre-registry providers.toml to schema 2)
- cmdutil/legacymigration.go (131) wired into every product binary via `EnsureUserConfigDirs`/`CheckLegacyDataDirs` (cmdutil/userdirs.go:33-44)
- cmd/evener-transcript-v2-upgrade/main.go (408; header says "legacy transcript v1")
- cmd/evener-doctor/reconstruct.go (593; rebuilds a transcript from an AgentsView sqlite archive; opens with `modernc.org/sqlite`)
- tests: 1959 + 995 + 1112 lines

Evidence: main.go:5-20 documents three source generations (`~/.serf`, interim `~/.evener`, final XDG). `ErrLegacySerfData` (legacymigration.go) blocks launches while legacy dirs hold data. convert.go and `ErrOldSchema` exist for a providers.toml layout that predates registry v2. Also a stale justification: main.go:88-91 says `homeRootFiles` "is duplicated here rather than shared because evener-migrate is a deliberately dependency-light standalone binary that does not import cmdutil", but the package is `package migrate`, is run via `evener migrate` (cmd/evener/main.go:530 `migratecmd.Run`), and already imports envvars; the claim is false and the two 9-entry lists (`homeRootFiles` in cmdutil/legacymigration.go:19 and `[]homeRootFile` in migrate/main.go:92) must be hand-synchronized.

Impact: about 1930 production lines of compat code plus 4200 test lines in the startup path of every binary; the startup guard can refuse to launch for a rename that happened in 2026.

Fix plan: (a) now, uncontroversial: have evener-migrate import the list from cmdutil (or move it to `envvars/userdirs`) and delete the duplicate and the false comment (-25 lines). (b) With Jesse's approval, set a sunset: remove `ErrLegacySerfData` startup refusal first, then evener-migrate, convert.go, the transcript v1 upgrader, and treat reconstruct as an ad-hoc recovery script outside the shipped binary (it needs a one-off AgentsView DB). Each removal is its own PR with the docs row (docs/product/subsystems.md S20/S21, UPGRADING.md) edited in the same change.

Size: (a) -25; (b) about -1930 prod, -4200 test.

Risk: (a) low; (b) breaks upgrade path for users on old layouts, needs approval.

---

### internal-misc-cmds-7: Dead `internal/devtool/gatesurface` Go package; the shell library is the live definition (severity: low)

Locations: internal/devtool/gatesurface/gatesurface.go (26 lines), drift_test.go (101), gatesurface_test.go (38), root gatesurface_test.go (56).

Evidence: `grep -rn gatesurface` across .go/.mk/Makefile finds no importer outside the package's own tests. The package says it is "the single definition of which tests the deterministic non-fuzz gate runs" and "a Go port of scripts/lib/gate-surface-lib.sh", but scripts/coverage/coverage-floor.sh, coverage-gaps.sh, scripts/gate/run-module-tests.sh and test-timing-budget.sh all still source the shell lib (grep of `GATE_TEST_RUN`/`GATE_FUZZ_TEST_SKIP`). The only consumer is `drift_test.go`, which asserts the Go constants equal the shell constants: tests guarding a copy nobody reads.

Impact: a second "single source of truth" that is not read by anything; any change to the gate surface must touch both or the drift test fails.

Fix plan: delete the package and its tests (about -220 lines). If a Go consumer is wanted later, make evener-dev read the shell lib's values rather than the reverse. Docs: remove the mention in docs/developing-evener/coverage.md if it names the Go package.

Size: -220. Risk: none (no importers).

---

### internal-misc-cmds-8: Root `frontmatter` module is a test-only duplicate of `agent/internal/frontmatter` (severity: low)

Locations: frontmatter/frontmatter.go (61 lines, Split at :18, Parse at :44), frontmatter/*_test.go (303 lines), agent/internal/frontmatter/frontmatter.go (86 lines).

Evidence: frontmatter.go:16-19 says "It matches agent/internal/frontmatter, which this module cannot import". `grep -rl 'evener/frontmatter"'` returns exactly one file: internal/bundled/bundled_test.go. Every production consumer (agent/skill, agent/plugin, agent/memory_*, agent/prompt_data.go) imports agent/internal/frontmatter (14 files). So the root package exists to serve one test.

Impact: two parsers for the same format with identical delimiter and BOM handling that must be kept in lockstep; the test-only one has 303 lines of tests.

Fix plan: delete frontmatter/. In internal/bundled/bundled_test.go replace `frontmatter.Parse` with a 10-line local split using yaml.v3 (the test needs only the Meta map) or move the bundled-asset assertions into the agent package, where `TestBundledAgentToolListsNameRegisteredTools` already lives (bundled_test.go:140-ish comment says so). Remove the go.work/go.mod mention if frontmatter is its own module path.

Size: about -360, +15. Risk: none.

---

### internal-misc-cmds-9: Coverage-chasing "fuzz" targets that ignore their input, and hand-listed `FuzzPackageUnion` replays that have drifted (severity: medium)

Locations:
- 10 package_union_fuzz_test.go files, in this slice: internal/plugins (96 `t.Run` entries), selfupdate (22), credentials (8), binresolve (14), bundled (1)
- 6 `coverage_*_fuzz_test.go` in internal/plugins (357+313+266+256+251+193 lines) and 7 in cmd/evener (serve/seed/run/main/openai_auth/runserve_exact/serve_residual, about 1100 lines), cmdutil/coverage_program_fuzz_test.go (172), tools/.../coverage_program_fuzz_test.go

Evidence: every one is `f.Fuzz(func(t *testing.T, _ uint8) {` (22 hits), i.e. the fuzz input is discarded, so it is not fuzzing; it replays deterministic cases under the `evenerfuzz` build tag to bump the fuzz-lane coverage union. credentials/package_union_fuzz_test.go:11-12 says exactly "replays every deterministic package scenario under fuzz coverage". The replay lists are hand-written and have drifted: plugins lists 96 of 517 `func Test*`, selfupdate 22 of 85, credentials 8 of 16, bundled 1 of 4 (counted with grep). The coverage_* files use fake `coverageAtomicFile`/`coverageLockFile` types (coverage_atomic_git_fuzz_unix_test.go:19-30) and assert only `err == nil/!= nil` after injected failures.

Impact: about 3500 lines in-slice whose purpose is to move a coverage number; they test mocked behavior (violates the testing rules), they are build-tagged out of `make test`, and they rot as the Test list changes.

Fix plan: confirm with Jesse that the fuzz-union coverage metric is still wanted. If so, generate the union list (`go test -list` in the fuzz script) instead of hand-listing; if not, delete the `package_union` and `coverage_*` files and rely on the default suite. Real fuzz targets with meaningful inputs (parse_fuzz, source_fuzz) stay. Update docs/developing-evener/fuzzing.md and scripts/fuzz/fuzz-targets.txt in the same change.

Size: -3500 test lines in-slice (about -8000 repo-wide if the pattern is dropped). Risk: low for the product; changes the coverage-ratchet number, so the floor must be re-blessed.

---

### internal-misc-cmds-10: internal/plugins carries about 75 package-level function seams used only for fault injection (severity: medium)

Locations: atomic.go:18-25 (6), manifest_fallback.go:14-19 (4+), locks.go:19-24 (6), doctor.go:21-24 (8), git.go:18-19, gc.go:16-17, install.go:20-22, marketplaces.go:18-31 (15), source.go:18-26 (13), publication.go:180, resolve.go:405-412, paths.go:16, updates.go (2), metadata_migration.go (4). A regex over the package finds 75 `name = os.X|filepath.X|io.X|time.X|func(` assignments in non-test files.

Evidence: e.g. source.go declares `sourceStat`, `sourceWalk`, `sourceRel`, `sourceOpen`, `sourceMkdirAll`, `sourceOpenFile`, `sourceCopy`, `sourceRemoveAll`, `sourceClose` as variables so `coverage_source_manifest_fuzz_test.go` can force `copyTree` error branches (copyTree at source.go:304-345 is built from them). `grep -c` shows tests swapping them (9 hits in coverage_atomic_git_fuzz_unix_test.go alone). Tests of this shape assert "an injected error becomes an error", i.e. they test the mock.

Impact: production code reads worse (every OS call is an indirection) and mutates globals under tests (hence "Not parallel" comments elsewhere); the injection tests do not prove recovery or preservation.

Fix plan: drop seams that only serve error-branch coverage; keep the two or three that guard real recovery contracts (lock timing, rename failure rollback in store_changed). Test real failure modes with real filesystem conditions (read-only dir, missing parent, symlink loops) or `afero.Fs` injected through the `Manager`, which is how credentials and rendezvous already do it. Do this together with finding 1 and 9 (atomic.go seams vanish with the shared helper).

Size: about -250 prod lines of indirection, -1500 test lines. Risk: low-medium; do package by package, keeping behavior tests.

---

### internal-misc-cmds-11: `selfupdate.installExtractedBinaries` is a 208-line multi-binary transaction for a one-element list (severity: medium)

Locations: internal/selfupdate/update.go:41 `var installBinaries = []string{"evener"}`; :590-798 `installExtractedBinaries`; :803 `digestsUnderLock`; :520, :582, :635, :804 loops over `installBinaries`; :162 in `Upgrade`.

Evidence: the comment at :36-40 states only `evener` is installed ("evener-dev ... no longer rides in the archive"). The function still builds a `staged` struct slice, per-binary snapshot logic for regular-file entrypoints (`linkIsRegular`, `linkFile`), a `restore` closure that loops all staged entries, and returns `map[string]string` digests keyed by binary. Test files iterate `installBinaries` 8 times (cov_rhub_selfupdate_test.go:166,170,314,318,334,346,399; install_lock_test.go:60; installdirs_custom_test.go:138).

Impact: a nested transaction (stage, snapshot, commit, restore, abort) designed for N binaries that is only ever run for one; hard to review, and it is the update path (S20) where mistakes brick installs.

Fix plan: collapse to a single-binary `installBinary(ctx, extractDir, shareBinDir, binDir) (digest string, err error)`: stage the temp, commit by rename, swap symlink, and one `rollback` that restores the previous bytes and entrypoint. Keep the install lock, stage-then-commit ordering, and restore-on-digest-failure (existing tests at cov_rhub_selfupdate_test.go cover these and must remain green). Drop `installBinaries` and the digest map type. Also factor the temp-then-rename in `restore` into the shared atomic helper of finding 1 where it fits (modes differ: 0755).

Size: about -90 prod lines. Risk: medium (update path); keep behavior tests untouched and add one for the restore-on-failure sequence before refactoring.

---

### internal-misc-cmds-12: Hand-copied `agent.DetailedStatus` to `server.DetailedStatus` mapping (severity: medium)

Locations: cmd/evener/serve.go:2232-2345 `agentToServerDetailedStatus` (114 lines) plus :2347-2415 clone helpers `cloneServeBool/Int64/Usage/Worktree/Watches`; types agent/status.go:124 `DelegateStatusInfo` vs server/server.go:105 `DelegateStatusInfo`; :2228 `evenerUsageFromLLM`.

Evidence: the delegate mapping at serve.go:2293-2316 lists ~45 fields by hand (`Task: delegate.Task, Description: ..., ResolvedProfileID: ...`); the same-named type exists in both packages. `evenerUsageFromLLM` (:2228) is a one-line wrapper over `appwire.EvenerUsageFromLLM`. Adding a status field requires touching agent type, server type, this mapper, and its tests; a missed field drops silently (the compiler cannot flag it).

Impact: drift risk on a client-visible status surface, and about 190 lines of copy code in the serve entry file.

Fix plan: make server consume `agent`-owned DTOs through appwire types (the shared type already exists for the wire) or define the status structs once in appwire and have both agent and server use them; then delete the mapper and clone helpers. Interim step if the type unification is too big: a reflective field-parity unit test over both structs so a missing field fails CI, and inline `evenerUsageFromLLM`. Docs: update docs/product/session-activity.md if it describes the mapping.

Size: about -190 once unified. Risk: medium (wire types; guarded by existing serve_test.go:210-1153 mapping tests).

---

### internal-misc-cmds-13: `runPluginLifecycle` is a 211-line switch with five copies of the `<plugin>@<marketplace>` boilerplate and a single-implementation 16-method interface (severity: medium)

Locations: cmd/evener/plugincmd.go:406-616 `runPluginLifecycle`, :19-36 `pluginManager` interface, :38-42 `pluginLaunchResolver`, :315 `splitPluginRef`; cmd/evener/plugincmd_coverage_fuzz_test.go:16-80 `pluginManagerReplay`.

Evidence: cases install/remove/enable/disable/upgrade/auto-upgrade each repeat: new FlagSet, SetOutput, Parse, `if fs.NArg() < 1 { usage }`, `splitPluginRef(fs.Arg(0))`, manager call, Fprintf. Five identical 8-line prefixes. `pluginManager` has exactly one production implementation (`plugins.NewManager`, plugincmd.go:42) and the only fake (`pluginManagerReplay`) lives in an `evenerfuzz`-tagged coverage test. The `pluginLaunchResolver` type assertion at :424-430 (`m.(pluginLaunchResolver)` then "does not support effective listing") guards against a case that cannot occur in production. `splitPluginRef` duplicates `plugins.splitKey` (internal/plugins/install.go:448) with slightly different no-`@` behavior.

Impact: boilerplate hides the per-verb behavior; the interface exists only for a coverage fake.

Fix plan: replace the switch with a verb table `map[string]func(m *plugins.Manager, ...)` where a shared helper `parseRefArg(fs, args, usage)` does the FlagSet and ref parse; export one `plugins.ParseRef` and delete `splitPluginRef`; drop `pluginManager`/`pluginLaunchResolver` and let tests use a real `plugins.Manager` rooted at `t.TempDir()` (the plugins package already supports that with `NewManager(root)`). Fakes in coverage tests are deleted with finding 9.

Size: about -150 prod, -150 test. Risk: low; CLI behavior pinned by plugincmd_test.go (950 lines).

---

### internal-misc-cmds-14: `cmd/evener-dev` carries Python-repr emulation and a 530-line text scraper for `go test -v` failures (severity: medium)

Locations:
- cmd/evener-dev/covstmt.go:289-376 `surrogateEscapeRunes`, `containsRunes`, `pyRepr` (88 lines) used at :161-163, :185, :197
- cmd/evener-dev/agentshards.go:846-1346 `replaySurveyFailures`, `expandSurveyFailure` (162 lines), `surveyFallbackOwnerStates`, `surveyFallbackLaterFailureClaim`, `surveyPhaseParts`, `surveyFrameworkLine`, and friends (about 500 lines)
- cmd/evener-dev/agentshards.go:314-663 `runShards` (350 lines)

Evidence: covstmt.go:289-296 says it decodes argv "the way Python's os.fsdecode does" so `--in` patterns match "the Python this report replaced"; pyRepr renders strings exactly as Python's repr(). The retired Python script no longer exists (covstmt.go header: "instead of a drifted Python duplicate"), so parity with it is not a requirement: `strings.Contains` and `%q` do the job. In agentshards.go the failure-excerpt logic scrapes human `-v` output heuristically (owner states, phase lines, framework lines), although `timingparse.go` already consumes `go test -json` in the same tool. 1353 lines in one file with `runShards` at 350.

Impact: about 600 lines of dev tooling whose edge cases (mismatched owners, deferred lines, fallback claims) have 3037 lines of tests, all to reformat a CI summary excerpt.

Fix plan: replace `pyRepr`, `surrogateEscapeRunes`, `containsRunes` with `strings.Contains(file, pattern)` and `%q`; delete the matching tests (TestPyReprMatchesPython and relatives). For the survey excerpt, switch to `go test -json` events (`Action:"output"` grouped per `Test`) which gives owner attribution without scraping, then delete the scraper. Split runShards into plan/run/report. Docs: docs/developing-evener/README.md only if output format changes.

Size: about -90 (python parity) and about -450 (scraper) prod; larger on the test side.

Risk: low for pyRepr (dev-only output); medium for scraper replacement (changes CI summary shape; Jesse should confirm he cares about the excerpt format).

---

### internal-misc-cmds-15: Plugin store has three tree-copy implementations and a scattered `.git` policy (severity: medium)

Locations: internal/plugins/source.go:304 `copyTree` (os.Walk, inline open/copy/close), publication.go:325 `copyTreeExact` (+ :208 `copyEntryExact`, :229 `copyRegularFile`), resolve.go:412 `copyBundledPayload = os.CopyFS`; `.git` handling in publication.go:22 `isGitName`, :59 `symlinkReachesGit`, :85 `targetTouchesGit`, source.go:191 `rejectGitPathComponent`, :198 `selectedDirectoryIsGitAdministration`, :238 `looksLikeGitAdministration`, :358 `refuseGitSymlink`, :378 `hasGitComponent`, :385 `removeGitMetadata`, metadata_migration.go `collectGitEntries`, `unsafeGitEntry`, `preservesManagedNamespace`.

Evidence: `copyTree` re-implements the open/MkdirAll/OpenFile/Copy/Close sequence that `copyRegularFile` (publication.go:229-244) already encapsulates; its eight `source*` seam vars exist only for it. The `.git` rules live across 4 files and the comment at source.go:300-302 refers to an issue number (#4768) for why. Two functions for ".git admin directory" (`looksLikeGitAdministration` and `selectedDirectoryIsGitAdministration`) and three for "path reaches git" exist.

Impact: any change to what counts as Git metadata needs edits in several functions; the copy variants differ in overwrite (`O_TRUNC` vs `O_EXCL`) and symlink (dereference vs preserve) semantics without a single statement of why.

Fix plan: put all content-audit and copy routines in one file `content.go` with one `copyTree(src, dst, opts)` taking `Options{DereferenceLinks, SkipGit, Exclusive}`, built on `copyRegularFile`; collect the `.git` predicates into a small `gitmeta.go`. Keep the behaviors via the existing tests (managed_publication_test.go, source_selection_test.go, marketplaces_path_scrub_test.go). Combine with finding 10 to remove the `source*` seams.

Size: about -120 prod lines. Risk: medium (supply-chain-sensitive audit code); sequence after tests for each current semantic are named.

---

### internal-misc-cmds-16: `cmdutil` pass-through wrappers and near-twin resolvers (severity: low)

Locations: cmdutil/cmdutil.go:240 `ParseAllowedDecisions` calling :242 `parseAllowedDecisions`; :64 `ResolveModelRef` vs :83 `ResolveResumeModelRef`; cmdutil/load_client.go:31 `ResolveProfile` and :37 `BuildResolveProfile`; cmdutil/userdirs.go:33 `CheckLegacyDataDirs` calling `checkLegacyDataDirs`; cmdutil/userdirs.go:20-28 vs envvars/userdirs `UserSkills`/`PluginStore`.

Evidence: `ParseAllowedDecisions(raw)` is `return parseAllowedDecisions(raw)` (no added behavior; tests call the lower-case one at cmdutil_test.go:180-200). `ResolveModelRef` and `ResolveResumeModelRef` are identical except for the ordering of the env-model and persisted-resume checks; the two copies of the "no model" error and resume-normalization (`ToLower(TrimSpace(...))`) are duplicated. `ParseAllowedDecisions(envvars.EVENERAllowedDecisions.Getenv())` appears in serve.go:2138 and fluency main.go:1353 as a repeated 2-liner.

Impact: small; each is a place where a future fix lands in one twin only.

Fix plan: delete the lower-case wrappers (export once), fold the two resolvers into `ResolveModelRef(..., precedence)` or a shared `resumeModel(...)` helper, and replace `DefaultSkillsDir`/`DefaultPluginsRoot` with the userdirs functions. Pure refactor; existing tests cover.

Size: about -45. Risk: none.

---

### internal-misc-cmds-17: Hand-rolled subcommand dispatch boilerplate (severity: low)

Locations: cmd/evener/main.go:495-579 `dispatchCLICommand`, `cliCommandRunners`, `dispatchCLICommandWith`; cmd/evener-dev/main.go:14-45 (a map-based dispatcher with the same purpose).

Evidence: 11 runner fields in `cliCommandRunners`, an 11-case `switch` returning `(true, "evener <name>", err)` each, and three near-identical adapters (hub, doctor, migrate at :508-527) converting `int` exit code to `subcommandExitError`. evener-dev already uses a `map[string]func(...) int`.

Impact: adding a subcommand edits four places (field, constructor literal, switch case, usage text).

Fix plan: one table `[]cliCommand{name, run}` plus a `fromExitCode(func(...) int)` adapter; `dispatchCLICommandWith` becomes a map lookup. Keep the injection of runners used by main tests. Docs: none.

Size: about -60. Risk: low.

---

### internal-misc-cmds-18: History-narrating and issue-numbered comments, and essay-length comments (severity: low)

Locations (examples): cmd/evener-dev/agentshards.go:~265 ("the inline duplication it used to carry is retired"), internal/selfupdate/update.go:36-40 ("no longer rides in the archive ... have aged out"), internal/remoteinstall/remoteinstall.go:20-24 ("used to download install.sh from the mutable main branch") and :49-51 ("the pin the hub earned in round twelve"), cmd/evener-dev/*.go (56 `#NNN`/"issue #NNN" references across the slice, e.g. agentshards.go:94,496,565-569, timingparse.go:19-54, timingrender.go:7-10,75, webchecks.go:35,56), internal/plugins/marketplaces.go:1073,1608, source.go:303, cmd/evener/serve.go:611,1081,1365,1759,1783, resume_guard.go:27 ("a contradiction Jesse refused the operation over (#4442)", :80 "roborev, PR #4589").

Evidence counts over the slice's non-test Go: "legacy" 71 hits, "no longer" 40, "used to" 16, "retired" 8, PR/issue numbers 56. Comment density: marketplaces.go 580/1670 lines, marketplace_migration.go 439/1282, resolve.go 417/1146.

Impact: violates the naming-and-comments rule (history belongs in commit messages); review feedback and rollout narration will rot.

Fix plan: strip history and tracker references from comments in a mechanical pass, keeping the invariant being protected. Do it file by file when those files are already being touched (findings 3, 5, 14), not as a standalone sweep, to avoid churn.

Size: about -400 comment lines. Risk: none.

---

### internal-misc-cmds-19: Tests assert on human-readable output (severity: medium, test-debt)

Locations: cmd/evener tests: `strings.Contains(<output|err|got|stdout>, "<literal>")` appears 158 times (run_test.go 33, providers_test.go 28, main_test.go 20, install_test.go 9); internal/plugins 30 (autoupgrade_test.go:242, locks_test.go:80, resolve_test.go:253, marketplaces_path_scrub_test.go:49,121,348,397); tools/ 32; cmd/evener-dev 31; internal/devtool 24; evener-doctor 12; cmdutil 11; evener-migrate 10 (regex count over *_test.go).

Evidence: providers_test.go:245 `!strings.Contains(out, "LIVE") || !strings.Contains(out, "ok (1 models)")`; :265 `"openai-chat: ok"`, `"openai-responses: inconclusive"`; :293 `"openai-chat: error"`. docs/developing-evener/testing.md:62-90 ("Copy Is Not a Test Oracle") forbids hardcoded-phrase assertions on human-readable strings and requires sentinels or structured fields. Some hits are legitimate (names the test planted, e.g. "acme"), many are copy.

Impact: wording changes break tests, and a green test proves little about behavior.

Fix plan: triage per file. For CLI output that is a contract, assert through a structured form (`--json` flags already exist for plugin list/gc/doctor; add a `--json` for providers test output or return a typed result). For errors, use `errors.Is` sentinels (several packages already export them). Start with providers_test.go and run_test.go. Update only tests; no product docs.

Size: about -300 assertion lines replaced by about 200 structured assertions. Risk: low; follow the TDD rule by first making the sentinel/struct available.

---

### internal-misc-cmds-20: `install.sh` is a byte-for-byte checked-in copy to satisfy `go:embed` (severity: low)

Locations: install.sh (180 lines) at repo root and internal/remoteinstall/install.sh (180 lines), guarded by `TestEmbeddedScriptMatchesReviewedInstallers` and sshconn's `TestRound11EmbeddedInstallerMatchesTheReviewedScript` (remoteinstall.go:25-31); install tests also at root (install_test.go, install_fuzz_test.go).

Evidence: `//go:embed` cannot reach `..`, so the repo keeps two copies and two tests to keep them equal. The doc comment narrates history ("the hub's fallback used to download install.sh").

Fix plan: make internal/remoteinstall/install.sh the only copy and publish the root `install.sh` by a `make` copy step (or the other way, a root-level Go file embedding it) so there is a single source and the drift tests vanish. Needs a decision on which path the documented quickstart URL serves; check .github release workflow before moving.

Size: -180 duplicated lines, -2 tests. Risk: low-medium (installer URL and release workflow).

---

### internal-misc-cmds-21: Small duplicates and dead weight in the leaf packages (severity: low)

Locations and evidence:
- agent/session_tools_shell.go:422 `shellSingleQuote` duplicates execsupport/shellquote/shellquote.go:124 (`"'" + strings.ReplaceAll(s, "'", `'\''`) + "'"`), and agent already depends on execsupport (agent/go.mod:21). Replace with `shellquote.Literal`.
- internal/fuzzroutes/fuzzroutes.go:30-46: `"/api/health"` appears twice (slots 13 and 15), and "/api/search" is documented "former REST route", kept only "for corpus-index stability"; the package comment claims to replace two copies yet carries compat for the harvested corpus. Replace the index-by-position design with a name-keyed lookup, or accept and document; ask Jesse before changing the corpus format.
- internal/e2ecap/e2ecap_test.go: `TestRequireLoopbackBind` just calls `RequireLoopbackBind(t)` (14 lines): it passes or skips by environment and asserts nothing; delete it.
- execsupport/procgroup_unix.go:36-42 `KillGroupAfterReap(pid)` is `Kill(pid)`; the non-unix twin is a no-op. It documents a distinct intent so keep it, but note the unix wrapper adds nothing beyond a name.
- internal/maketargetsdoc/parse.go:228-246 `ruleShape` mirrors makefiletargets_audit_test.go:61 `ruleLineName` "deliberately ... that copy is unavoidable, because the generator is package main". Moving the parser to a non-main subpackage removes the copy and the claim that it is unavoidable.
- internal/httpguard has one importer (server/server.go:476-586) and 130 lines of code; fine, but the host/origin allow-list builder (`NewSameOriginPolicy`, lines 21-47) re-derives localhost aliases that cmd/evener-hub/internal/hubedge also checks (hubedge/auth_token.go:136 does its own constant-time compare); there are 4 `ConstantTimeCompare` copies in the repo (auth/openai/manual.go:47, hubedge/auth_token.go:136, hostops/token.go:479, httpguard.go:79).

Fix plan: execute each as a tiny independent change; none needs design discussion except the fuzzroutes corpus index.

Size: about -80 total. Risk: none to low.

---

### internal-misc-cmds-22: Hand-rolled TOML line scanner in `evener-tomlcheck` (severity: low)

Locations: cmd/evener-tomlcheck/main.go:187-421 (`splitWords`, `skipTOMLQuoted`, `splitTOMLKeyPath`, `tomlTableHeaderPath`, `checkTOMLFile`).

Evidence: about 230 lines to find keys by regex per line, track multi-line strings by hand (`inMultilineString`, `multilineDelim`), and strip quoted keys. Inline tables and multi-line arrays are matched only at the start of a line (`tomlKeyLineRe.FindStringSubmatch(line)` at :381), so `x = { fooBar = 1 }` is not checked; the repo already depends on a TOML decoder (credentials/store.go uses `toml.NewEncoder`).

Impact: a naming linter that silently misses nested keys while 583 test lines exist for the scanner.

Fix plan: parse with the TOML library's key metadata (`MetaData.Keys()`) for the key set and keep the line-number lookup by searching for the key's first occurrence, or accept the limitation and document it. Low priority.

Size: about -150. Risk: low.

---

## Recommended fix sequence

1. Delete dead code first (no decisions needed): finding 7 (gatesurface), finding 8 (root frontmatter), the easy items in 21 (shellSingleQuote, e2ecap vacuous tests), and finding 16 wrappers. About -700 lines, no behavior change.
2. Finding 1 (shared atomic write helper): helper plus credentials first (the one real durability gap), then the remaining six callers, removing plugins seams as each caller moves. Pair with finding 11 collapse (selfupdate) since it reuses the helper.
3. Finding 2 (state-root helper in envvars/userdirs): needs Jesse's answer on the home-failure fallback; then migrate rendezvous, auth/openai, cmdutil, evener-migrate; finish with the out-of-slice copies in agent/appwire/hub.
4. Finding 4 (move ApplyFastCheapModel/sandbox helpers into cmdutil), then finding 3 (flag registration and SessionConfig builder shared between run and serve, then split `runServeWithDeps`). Finding 4 is the prerequisite that shrinks serve.go before it is split.
5. Findings 13 and 17 (plugin CLI table, dispatch table), together with 19 (replace string assertions with sentinels/JSON) since they touch the same tests.
6. Ask Jesse about the sunset for compat code, then act: findings 5 and 6 (plugin-store migrations, serf-era migrate tools, transcript v1 upgrader, reconstruct). Do 6(a) (shared homeRootFiles) now.
7. Finding 9 and 10 once Jesse confirms the fuzz-union coverage metric's fate; then finding 15 (plugins tree copy and `.git` policy) on the slimmer package.
8. Finding 12 (agent/server status DTO unification) as its own wire-adjacent change with the appwire owners.
9. Dev tooling cleanup: findings 14 and 22, then finding 20 (install.sh single copy), and comment hygiene (finding 18) opportunistically in files already open.
