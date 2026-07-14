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

The implementation is focused on Task 8 and uses canonical IDs at destructive boundaries. The remaining verification concerns are environmental, not observed code failures: IPv6 loopback binding is prohibited by the sandbox, and Node `jsdom` dependencies are unavailable. The required tagged compile and deterministic focused migration checks pass.

Commit: `PENDING`
