# Task 8 report: canonical hub project keys

## RED evidence

The required focused suite initially could not complete in this sandbox because `httptest` attempted IPv6 loopback binding and failed with:

```text
listen tcp6 [::1]:0: bind: operation not permitted
```

Before migration fixes, targeted tests also failed on old slug/path assumptions, nonexistent `/w/...` fixture paths, and legacy basename archive behavior. These were migrated to canonical `identifier.Project.ID` and resolvable canonical paths.

## Implementation summary

- **Ingestion/tree:** `hubcore` resolves each distinct live/past effective working directory once at the ingestion boundary, reuses carried live `identifier.Project` values, groups by canonical `Project.ID`, emits canonical `WorkingDir`, and keeps pathless sessions in presentation-only `no-project`.
- **Destructive APIs:** archive and project delete validate supplied IDs with `identifier.ValidateProjectID`, resolve the supplied path, require recomputed ID and canonical path agreement, and reject `no-project`. Delete no longer performs legacy basename cleanup.
- **TUI/spawn:** project rows use server-provided canonical keys only. `groupKey` is presentation-only for keyless grouping/folding/filtering; missing-key rows are non-actionable and never synthesize IDs from display names. Spawn prefill uses the canonical server project path.
- **Web UI:** project archive posts `{id: p.key, working_dir: p.working_dir}`; sidebar expansion state has no basename-key migration or co-basename copying.
- **Callers/tests:** migrated ordinary, fuzz, and `serffuzz` seam callers to canonical project-aware signatures and real resolvable path fixtures. Added main/linked-worktree, symlink, same-basename clone, canonical-key, mismatch, and `no-project` coverage.

## GREEN / verification evidence

Passed:

```text
go test ./cmd/serf-hub/internal/hubcore -run 'Test.*(Tree|Project|Archive|Delete)' -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore

go test -run 'TestHubModelSlashDashboardAndProjectNavigate|TestHubModelDashboardNewFromProjectRowUsesProjectDir|TestHubModelDashboardLaunchRowOpensUnscopedSpawn' ./cmd/serf-tui -count=1
ok   primeradiant.com/serf/cmd/serf-tui

go test -tags serffuzz ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore ./cmd/serf-tui -run '^$' -count=1
ok   all three packages; no tests to run

go test ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore ./cmd/serf-tui -run '^$' -count=1
ok   all three packages; no tests to run

git diff --check
pass
```

The exact required focused command was run. Hubcore passed; hub and TUI reached only unrelated sandbox IPv6 `httptest` panics in `TestHubRPCThreadStartPropagatesSpawnerStderrAsHubLaunchError` and `TestFetchHubTreeUsesAppWireThreadList`. The full hubcore package reached and passed all canonical migration scenarios; its fuzz scenario's HTTP fixture hit the same sandbox IPv6 limitation.

Sidebar JavaScript tests were attempted but could not run because the environment lacks the `jsdom` module (`Error: Cannot find module 'jsdom'`).

## Audits

- `ProjectSlug`, `projectSlug`, `hubProjectKey`, basename-8hex synthesis, and sidebar basename migration references were absent from `cmd/serf-hub` and `cmd/serf-tui` production/test sources after migration.
- Tagged/fuzz callers compile with `-tags serffuzz`.
- `.superpowers/sdd/task-1-report.md` and `.superpowers/sdd/progress.md` were not modified.
- No Task 9 clean-break reader work was started.

## Changed files

- `cmd/serf-hub/assets/sidebar.js`
- `cmd/serf-hub/cov_core_api_pass4_fuzz_test.go`
- `cmd/serf-hub/cov_exact_lifecycle_tree_fuzz_test.go`
- `cmd/serf-hub/cov_final_session_tree_fuzz_test.go`
- `cmd/serf-hub/cov_session_live_pass4_fuzz_test.go`
- `cmd/serf-hub/cov_session_residue_pass5_fuzz_test.go`
- `cmd/serf-hub/cov_session_tree_pass6_fuzz_test.go`
- `cmd/serf-hub/cov_web_tree_session_fuzz_test.go`
- `cmd/serf-hub/cov_workspace_mutations_pass6_fuzz_test.go`
- `cmd/serf-hub/internal/hubcore/coverage_edges_test.go`
- `cmd/serf-hub/internal/hubcore/roster.go`
- `cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go`
- `cmd/serf-hub/internal/hubcore/tree.go`
- `cmd/serf-hub/internal/hubcore/tree_test.go`
- `cmd/serf-hub/jstest/test-sidebar-migration.js`
- `cmd/serf-hub/web_api_archive.go`
- `cmd/serf-hub/web_api_archive_test.go`
- `cmd/serf-hub/web_api_favorite_test.go`
- `cmd/serf-hub/web_api_project_delete.go`
- `cmd/serf-hub/web_api_project_delete_test.go`
- `cmd/serf-hub/web_api_tree.go`
- `cmd/serf-hub/web_api_tree_test.go`
- `cmd/serf-tui/hub_controls_fuzz_test.go`
- `cmd/serf-tui/hub_dashboard.go`
- `cmd/serf-tui/hub_dashboard_view.go`
- `cmd/serf-tui/hub_keys.go`
- `cmd/serf-tui/hub_model.go`
- `cmd/serf-tui/hub_spawn.go`
- `cmd/serf-tui/hub_types.go`

## Self-review and concerns

The implementation is focused on Task 8 and uses canonical IDs at destructive boundaries. Parent verification exposed stale `/w/...` and basename fixtures; those are now real temporary directories resolved through `identifier.ResolveProject`, and the shared TUI mock provides explicit canonical server keys. The appwire Thread boundary now carries `ProjectID`/`ProjectPath`; TUI grouping uses those fields and leaves unresolved rows non-actionable.

## Parent-verification review fixes

### Exact RED evidence

The parent-required focused command reproduced these stale-fixture failures before the fixes:

```text
TestArchiveEndpointProjectKind: status=400 invalid project ID
TestArchiveUnarchiveSkipsRedundantLegacyDeleteForBasenameID: status=400 invalid project ID
TestProjectDeleteRemovesFilesAndScrubs: resolve project: lstat /w: no such file or directory
TestProjectDeleteRefusesWhenLive: got 400 (expected 409)
TestProjectDeleteSkipsOnRemoveFailure: resolve project: lstat /w: no such file or directory
TestArchiveDecisionsFlowIntoTree: alpha grouped as no-project
TestAPITreeProjectServedFromTree: status=404 project not found
TestAPITree_ArchivedProjectsAreStubs: archived project list empty
TestWeb_APITreeGroupsLiveOnlySessionsByProject: serf projects=0; rows grouped under no-project
TestHubDashboardSpawnWaitsForSlowHubSpawn: fixture path/key was not an actionable canonical server row
```

The same required suite also reaches unrelated existing HTTP tests that panic in this sandbox because `httptest` cannot bind IPv6 loopback:

```text
listen tcp6 [::1]:0: bind: operation not permitted
```

### Review-fix implementation and GREEN evidence

- Migrated archive, delete, and tree web fixtures to real temporary directories and `identifier.ResolveProject`, asserting canonical IDs and paths.
- Added canonical `ProjectID`/`ProjectPath` to appwire thread responses at hub list/read/start/resume boundaries; TUI consumes only those server fields and preserves keyless presentation-only groups.
- Migrated the shared tmux fixture and slow-spawn fixture to explicit canonical server keys and canonical paths.
- Deterministic migrated tests pass:

```text
go test ./cmd/serf-hub/internal/hubcore -run 'Test.*(Tree|Project|Archive|Delete|Spawn)' -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore

go test ./cmd/serf-hub -run '^(TestArchive|TestProjectDelete|TestOrphan|TestTreeResponse|TestAPITree_|TestWeb_APITreeGroupsLiveOnlySessionsByProject|TestWeb_APITreeReturnsRefsAndNormalizesAwaiting|TestWeb_APITreeSkipsLiveEntriesUntilSessionIDKnown|TestSpawnAndResumeRequestsCarryCanonicalProjectSeparatelyFromWorkingDir|TestResolveStateDirForProjectUsesCarriedProjectWithoutResolvingWorkingDir)$' -count=1
ok   primeradiant.com/serf/cmd/serf-hub
```

- Tagged compile passed: `go test -tags serffuzz ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore ./cmd/serf-tui -run '^$' -count=1` (all three packages, no tests to run). Ordinary compile passed for the same three packages. `git diff --check` passed.
- Parent reran the exact focused command outside the delegate sandbox after the fixes:

```text
go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub ./cmd/serf-tui -run 'Test.*(Tree|Project|Archive|Delete|Spawn|Dashboard)' -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore  0.486s
ok   primeradiant.com/serf/cmd/serf-hub                   3.013s
ok   primeradiant.com/serf/cmd/serf-tui                   2.264s
```

- The documented sidebar setup was attempted with `npm init -y && npm install jsdom`; installation failed offline with `npm ERR! code ENOTFOUND` for `registry.npmjs.org`, so `node test-sidebar-migration.js` could not run.

Implementation commit: `606eb6ead2190d0f5c92446e0e9b2b82b65555a6`
Fix commit: `b0096985f`

## Parent follow-up: TUI E2E fixture fixes

Parent verification initially reduced the failures to three TUI E2Es. The shared fixture had two stale assumptions:

- Its literal `/tmp/serf-tui-e2e/serf` expectation did not account for Darwin canonicalizing `/tmp` to `/private/tmp`. The fixture now canonicalizes the original short `/tmp` root with `filepath.EvalSymlinks` before joining the suffix. This preserves Linux behavior and avoids the UI truncation caused by switching to the much longer ambient Darwin `TMPDIR`.
- Dashboard and Codex spawn tests used `Tab` while focused on the directory field. That invokes directory completion; subsequent prompt text was appended to the path. Both tests now use the form's explicit `Enter` transition from directory to prompt.

Each reproduced TUI failure passed in a focused rerun, and the exact three-package command above then passed in full. The parent tagged compile also passed for all three packages. `git diff --check` and the forbidden-key audit pass.

## Independent review fix: remote appwire project identity

The first independent review found one Important defect: remote appwire threads carried `ProjectID`/`ProjectPath`, but web-tree ingestion discarded those fields and attempted to resolve only the remote `CWD` on the local filesystem. A TDD regression reproduced the empty carried identity when the remote CWD was not locally resolvable. `appThreadTreeEntries` now validates the supplied project ID and preserves the pair in `LiveEntry.Project`, allowing `ResolveProjectMap` to reuse the remote hub's canonical identity.

Fresh verification after this fix:

```text
go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub ./cmd/serf-tui -run 'Test.*(Tree|Project|Archive|Delete|Spawn|Dashboard)' -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore  0.416s
ok   primeradiant.com/serf/cmd/serf-hub                   2.497s
ok   primeradiant.com/serf/cmd/serf-tui                   2.153s

go test -tags serffuzz ./cmd/serf-hub ./cmd/serf-hub/internal/hubcore ./cmd/serf-tui -run '^$' -count=1
ok   all three packages; no tests to run

git diff --check
pass
```

Review-fix commit: `de042d413`

## Final independent re-review

A fresh independent review of `2eb33dcae..de042d413` found no Critical, Important, or Minor issues.

- Spec compliance: ✅
- Task quality: Approved
