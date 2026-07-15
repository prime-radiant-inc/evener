# Task 7 Fix Report — Canonical Project Lane Validation

## Findings

Independent review identified two gaps in the Task 7 migration:

1. Managed resume validated that a persisted target was registered in Git, but did not prove that the target was inside `<worktree-root>/<Project.ID>`. A registered non-Serf worktree could therefore be re-entered as managed.
2. `rollbackFreshDelegateWorktree` re-resolved the main repository from the active environment and derived sidecar metadata from `lanePath`, instead of using the Project already resolved during delegate creation.

## RED evidence

Focused command:

```text
go test ./agent -run 'TestResumeWorktreeReentry_Managed(RegisteredOutsideProject|SymlinkCanonicalizesBeforeContainment)|TestRollbackFreshDelegateWorktreeUsesCarriedProjectMetadataDir' -count=1
```

Exact initial failure before implementation:

```text
agent/session_tools_worktree_test.go:64:56: too many arguments in call to s.rollbackFreshDelegateWorktree
have (string, string, identifier.Project)
want (string, string)
FAIL    primeradiant.com/serf/agent [build failed]
```

The tests added before implementation were:

- `TestResumeWorktreeReentry_ManagedRegisteredOutsideProject_RestoresRootAndNotices`
- `TestResumeWorktreeReentry_ManagedSymlinkCanonicalizesBeforeContainment`
- `TestRollbackFreshDelegateWorktreeUsesCarriedProjectMetadataDir`

## Implementation

- `resumeWorktreeReentry`: for managed metadata, derives the expected project lane directory from the resolved `identifier.Project` and configured state/worktree root; canonicalizes both target and expected directory; requires strict safe containment; refuses and notices outside targets; uses the canonical target for registry, lock, and re-entry. Non-managed by-path resume remains unchanged.
- `worktreeCreateCoreResult` and `createDelegateWorktree`: carry the already-resolved `identifier.Project` through delegate creation.
- `rollbackFreshDelegateWorktree`: rejects an empty/invalid carried Project without selecting an alternate identity; uses `Project.CanonicalPath` for the control environment and `<worktree-root>/<Project.ID>/.meta` for sidecar cleanup; no longer calls `execenv.ResolveMainRepoRoot` or derives identity metadata from `lanePath`.
- Updated ordinary, fuzz, and `serffuzz` callers for the new result/signatures.

## GREEN evidence

Focused regressions:

```text
ok      primeradiant.com/serf/agent  0.867s
```

Required agent suite:

```text
(cd agent && go test . -run 'Test.*(Worktree|Isolation|Resume|Rollback)' -count=1)
ok      primeradiant.com/serf/agent  8.581s
```

Required tagged compile:

```text
go test -tags serffuzz ./agent -run '^$' -count=1
ok      primeradiant.com/serf/agent  0.437s [no tests to run]
```

Stale audits passed:

- No `worktree.ProjectID` references remain.
- No rollback `ResolveMainRepoRoot` call remains.
- `git diff --check` passed.

The required full internal suite was blocked in the fixer sandbox by Apple Git/xcrun cache permissions, but parent verification outside that sandbox passed:

```text
$ (cd agent && go test ./internal/worktree -count=1)
ok  	primeradiant.com/serf/agent/internal/worktree	2.907s
$ go test ./agent -run 'TestResumeWorktreeReentry_Managed(RegisteredOutsideProject|SymlinkCanonicalizesBeforeContainment)|TestRollbackFreshDelegateWorktreeUsesCarriedProjectMetadataDir' -count=1
ok  	primeradiant.com/serf/agent	0.837s
$ (cd agent && go test . -run 'Test.*(Worktree|Isolation|Resume|Rollback)' -count=1)
ok  	primeradiant.com/serf/agent	10.186s
$ go test -tags serffuzz ./agent -run '^$' -count=1
ok  	primeradiant.com/serf/agent	0.376s [no tests to run]
```

The fixer’s sandbox failure was environmental and included:

```text
git: error: couldn't create cache file '/tmp/xcrun_db-...' (errno=Operation not permitted)
FAIL    primeradiant.com/serf/agent/internal/worktree
```

## Changed files

- `agent/job_delegate.go`
- `agent/job_delegate_isolation_test.go`
- `agent/session_restore_close_status_program_fuzz_test.go`
- `agent/session_tools_worktree.go`
- `agent/session_tools_worktree_fault_fuzz_test.go`
- `agent/session_tools_worktree_seed100_exact_fuzz_test.go`
- `agent/session_tools_worktree_test.go`
- `agent/session_worktree_close_test.go`
- `agent/session_worktree_resume.go`
- `agent/session_worktree_resume_test.go`
- `.superpowers/sdd/task-7-fix-report.md`

The pre-existing `.superpowers/sdd/task-1-report.md` modification was preserved and not staged. `progress.md` was not changed.

## Self-review

- Exact-path staging only; no `git add -A`.
- No amend operation.
- Task 8 hub migration was not started.
- Containment uses canonicalized paths and avoids bare string-prefix matching.
- Invalid carried Project cleanup fails closed rather than selecting a fallback identity.
- Ordinary, fuzz, and build-tagged callers were audited.

## Concerns

The fixer sandbox could not run the full `agent/internal/worktree` suite because Apple Git could not create its xcrun cache under `/tmp`. Parent verification ran that suite successfully, so no test concern remains.

## Commit

Commit hash: f71203415389051d97316e78b19c06ac61b84db3.

## Final independent review verdict

The fresh re-review examined the complete Task 7 range from `b6048fb2f`
through verification-record commit `934c56926`.

```text
Spec compliance: ✅
Task quality: Approved
Critical findings: none
Important findings: none
Minor findings: none
```

The reviewer confirmed that managed resume now validates canonical Project.ID
lane containment, delegate rollback uses the carried Project without alternate
resolution, and all original Task 7 requirements remain satisfied.
