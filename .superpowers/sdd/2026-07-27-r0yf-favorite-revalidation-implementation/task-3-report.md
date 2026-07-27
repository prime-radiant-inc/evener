# Task 3 report: tighten explicit deletion and write-boundary contracts

Date: 2026-07-27
Branch: `wip/kata-favorite-cleanup-policy`
Task: kata r0yf, Task 3
Base: `12f548868`
Production/tests commit: `615a7a95d`

## Scope and outcome

The project deletion handler now treats every governed artifact operation as
part of the deletion gate. Session archive/favorite rows are deleted only
after flat artifacts, the per-session directory, and the final `.api.jsonl`
operation succeed. Directory-removal errors are reported as skipped. Project
archive/favorite rows are deleted only when no governed session was skipped.

Favorite-store deletion errors are collected after physical artifact removal,
reported as HTTP 500, and do not attempt an artifact rollback. The retained
favorite row was verified through an independent SQL connection. Notification
and attention-poke compensation is limited to requests that actually removed
a session's governed artifacts; fully skipped deletion is a no-op and emits
no tree notification.

No schema, endpoint, compatibility alias, cleanup job, read-time revalidation
mutation, or Task 1/2 behavior was changed.

## RED evidence

Tests were added before the production correction and run against the
unchanged handler:

```text
go test ./cmd/serf-hub -run 'TestProjectDelete' -count=1 -v
```

The command failed in the expected production paths:

```text
--- FAIL: TestProjectDeleteSkipsSessionThatBecomesLive
    archive decision (project, ...) = (false, false), want present=true value=true
--- FAIL: TestProjectDeleteDoesNotUnlinkSessionReservedAfterLivenessProbe
    archive decision (project, ...) = (false, false), want present=true value=true
--- FAIL: TestProjectDeleteSkipsOnRemoveFailure
    archive decision (project, ...) = (false, false), want present=true value=true
--- FAIL: TestProjectDeletePreservesDecisionsWhenSessionDirectoryRemovalFails
    directory failure must skip the session: {Deleted:[...] Skipped:[]}
--- FAIL: TestProjectDeletePreservesDecisionsWhenAPILogRemovalFails
    archive decision (session, ...) = (false, false), want present=true value=true
--- FAIL: TestProjectDeleteRetainsSkippedDecisionsAndRemovesOnlyDeletedDecisions
    archive decision (project, ...) = (false, false), want present=true value=true
--- FAIL: TestProjectDeleteReportsFavoriteStoreFailureAfterArtifactRemoval
    favorite store failure status=200 body={"deleted":[...],"skipped":[]}
--- FAIL: TestProjectDeleteBroadcastsTreeChangedExactlyOnceWhenNothingRemoved
    got notification "serf/tree/changed" before the sentinel; must not have broadcast "serf/tree/changed" here
```

These failures identify the original ordering bug, ignored directory error,
unconditional project-row deletion, swallowed store error, and unconditional
no-op notification.

## GREEN evidence

Focused project-delete and favorite endpoint tests:

```text
go test ./cmd/serf-hub -run 'Test(ProjectDelete|FavoriteEndpoint)' -count=1
ok  primeradiant.com/serf/cmd/serf-hub
```

All new failure-path tests were repeated five times:

```text
go test ./cmd/serf-hub -run 'TestProjectDelete(PreservesDecisionsWhenSessionDirectoryRemovalFails|PreservesDecisionsWhenAPILogRemovalFails|SkipsOnRemoveFailure|RetainsSkippedDecisionsAndRemovesOnlyDeletedDecisions|ReportsFavoriteStoreFailureAfterArtifactRemoval)' -count=5
ok  primeradiant.com/serf/cmd/serf-hub
```

The store-error/no-op/exact-one notification paths were also repeated five
times:

```text
go test ./cmd/serf-hub -run 'TestProjectDelete(ReportsFavoriteStoreFailureAfterArtifactRemoval|BroadcastsTreeChangedExactlyOnce|DoesNotBroadcastWhenNothingRemoved)' -count=5 -v
PASS
ok  primeradiant.com/serf/cmd/serf-hub
```

Required package and static checks:

```text
go test ./cmd/serf-hub -count=1
ok  primeradiant.com/serf/cmd/serf-hub

go test ./cmd/serf-hub/internal/hubcore -count=1
ok  primeradiant.com/serf/cmd/serf-hub/internal/hubcore

go vet ./cmd/serf-hub/...
exit 0, no output

golangci-lint run ./cmd/serf-hub/...
0 issues.

git diff --check
exit 0, no output
```

The full `cmd/serf-hub` test run completed in 26.411s. The final focused
failure-path run completed in 0.522s.

## Contract coverage

- Flat-file, per-session-directory, and final API-log removal failures retain
  matching session archive/favorite rows and canonical project rows.
- A partial project deletion removes only the exact decisions for the
  successfully removed session; skipped session, project, and unrelated
  session/project rows remain.
- Fully successful canonical deletion removes only target session/project
  rows; unrelated rows remain.
- Project ID/working-directory mismatch, entry-live refusal, mid-request
  liveness, and ownership-lock skips are asserted to mutate neither files nor
  decisions.
- A deterministic failing `FavoriteStore` filesystem reports the store error
  after all artifacts are gone, retains the original favorite session/project
  rows through an independent SQL read, and emits exactly one tree
  notification and one attention poke.
- Existing favorite endpoint tests continue to cover the ndr0 boundary:
  rejected nested/subagent/fork-active/synthetic targets do not write, while
  accepted top-level, capped-away, and orphan targets persist decisions. No
  ndr0 production or test behavior was changed by this task.
- Non-test `FavoriteStore.Set` remains only in the explicit favorite endpoint;
  non-test `FavoriteStore.Delete` remains only in the two explicit canonical
  deletion branches in `web_api_project_delete.go`. Store construction and
  change-hook wiring in `main.go` are not mutation call sites.

## Notification and error decisions

`PastIndex.Rebuild` remains the normal single notification source when it
observes an index delta. A direct notification is used only when physical
deletion succeeded but Rebuild did not fire its composed hook. Store errors
are reported after this mutation bookkeeping, so an HTTP failure does not
pretend that removed artifacts were restored and does not suppress the one
notification for the physical mutation. A fully skipped request neither
deletes project rows nor pokes attention or broadcasts.

The zero-session project case was explicitly considered. The current
canonical gate requires a matching tree project derived from session
metadata, so an empty project is rejected before the deletion loop. Per
Jesse's direction, this task does not broaden that gate or add a zero-session
deletion path.

No out-of-scope bug or reusable workflow issue was discovered that warranted a
new kata.

## Files and commit

Changed production/test files:

- `cmd/serf-hub/web_api_project_delete.go`
- `cmd/serf-hub/web_api_project_delete_test.go`

Production/tests commit: `615a7a95d` (`Tighten canonical project deletion cleanup`).

The report is intentionally committed separately because `.superpowers/` is
ignored.
