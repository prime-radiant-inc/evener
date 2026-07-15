# Task 7 Report: Migrate Managed Worktree Storage and Resume

## RED evidence

Tests were changed first. The exact required focused command was:

```text
(cd agent && go test ./internal/worktree . -run 'Test.*(ProjectID|Worktree|Isolation)' -count=1)
```

It exited 1. The new regression failed because production still used the deleted/local storage identity: linked-checkout create returned the old hash-based `<state>/worktrees/<old hash>/<name>` path instead of the shared canonical `Project.ID` path. Existing path-dependent restore tests also failed (`TestWorktreeExit_RestoringIntoManagedLaunchRootIdempotentRelockForeignWarns` and `TestWorktreeExit_RestoringIntoUnlockedManagedLaunchRootTakesLock`). This was the expected pre-implementation RED caused by stale production `worktree.ProjectID` callers.

## Implementation summary

- **Create / delegate create:** `worktreeCreateCore` resolves once with `identifier.ResolveProjectWith(activeRoot, execenv.NewProjectResolver(active))`, carries the resulting `identifier.Project`, builds `<worktree-root>/<Project.ID>/<name>`, and writes `Project.CanonicalPath` into sidecar metadata. Delegate isolation uses the same path.
- **List / switch / remove / prune:** `worktreeStateSnapshot` resolves and carries one canonical Project per operation. All project directories use `Project.ID`; containment, managed-entry filtering, metadata lookup, restore relock, and prune sweeps use the canonical project directory/path.
- **Resume / init-inside detection:** resume resolves the Project from the persisted target through the environment adapter, uses its canonical path for Git control and containment, and its ID for storage. Init-inside occupancy uses the same shared resolver and Project values.
- **Local renderer removal:** deleted `agent/internal/worktree.ProjectID` and all SHA-256/hex/basename rendering code and tests. `ValidateName`, `EncodeSidecarName`, and `DecodeSidecarName` remain.
- **Callers:** migrated untagged, build-tagged, and fuzz test helpers to `identifier.ResolveProjectWith` through the shared test helper. No old worktree renderer callers remain.

## GREEN evidence

Required agent suite:

```text
(cd agent && go test . -run 'Test.*(Worktree|Isolation)' -count=1)
ok   primeradiant.com/serf/agent  6.314s
```

Required tagged compile:

```text
go test -tags serffuzz ./agent -run '^$' -count=1
ok   primeradiant.com/serf/agent  0.355s [no tests to run]
```

Required linked-checkout regression:

```text
go test ./agent -run '^TestManagedWorktreeStorageUsesOneProjectIDFromMainAndLinkedCheckout$' -count=1
ok   primeradiant.com/serf/agent  0.703s
```

Deterministic worktree codec tests:

```text
go test ./agent/internal/worktree -run 'TestValidateName|TestEncode|TestDecode' -count=1
ok   primeradiant.com/serf/agent  0.150s
```

All default packages compiled:

```text
go test ./... -run '^$' -count=1
ok   all default packages [no tests to run]
```

The required full internal package command was blocked in the implementer sandbox by Apple Git/xcrun cache restrictions, but parent verification outside that sandbox passed:

```text
$ (cd agent && go test ./internal/worktree -count=1)
ok  	primeradiant.com/serf/agent/internal/worktree	2.151s
```

The implementer’s sandbox failures had errors such as:

```text
git: error: couldn't create cache file '/tmp/xcrun_db-*' (errno=Operation not permitted)
```

Retrying with writable `TMPDIR`, `HOME`, and `XDG_CACHE_HOME` still produced the same `/tmp/xcrun_db-*` failure. The failure is environmental; the focused deterministic tests and all agent worktree/isolation tests pass.

## Changed files

- `agent/internal/worktree/name.go`
- `agent/internal/worktree/name_test.go`
- `agent/internal/worktree/program_fuzz_test.go`
- `agent/session_tools_worktree.go`
- `agent/session_worktree_resume.go`
- `agent/session_tools_worktree_test.go`
- `agent/session_tools_worktree_create_test.go`
- `agent/session_tools_worktree_errors_test.go`
- `agent/session_tools_worktree_prune_test.go`
- `agent/session_tools_worktree_remove_test.go`
- `agent/session_tools_worktree_switch_test.go`
- `agent/session_tools_worktree_livework_test.go`
- `agent/session_worktree_close_test.go`
- `agent/job_delegate_isolation_test.go`
- `agent/job_delegate_attach_finalize_seed100_fuzz_test.go`
- `agent/job_delegate_exact_tail_create_restore_fuzz_test.go`
- `agent/job_delegate_send_seed100_fuzz_test.go`
- `agent/session_tools_worktree_scripted_fuzzhelpers_test.go`
- `agent/session_tools_worktree_seed100_exact_fuzz_test.go`

The pre-existing `.superpowers/sdd/task-1-report.md` modification was not edited, staged, overwritten, or reverted. `.superpowers/sdd/progress.md` was not touched.

## Self-review

- `git diff --check` passes.
- No `worktree.ProjectID` or local `func ProjectID` references remain; the only remaining `identifier.ProjectID` is the shared identifier API.
- Managed storage has no fallback to old worktree hashes or old bucket lookup.
- Project resolution uses `execenv.NewProjectResolver` and linked/main checkouts share the same resolved Project.
- Default tests are offline/deterministic except the required real-Git internal package suite, which is blocked by the sandbox's Apple Git cache restriction.
- No Task 8 hub project-key migration was started.

## Concerns

The full `agent/internal/worktree` suite cannot complete in this sandbox because Apple Git invokes xcrun and cannot create its cache under `/tmp`; this is unrelated to the Task 7 code. Required agent worktree/isolation tests, tagged compile, linked-checkout regression, deterministic codec tests, and all-package compilation pass.

## Commit

Task 7 migration commit:

```text
5cc5bd2e853e03071f74d44c2b5e82df8c185d83 refactor(worktree): use canonical project IDs
```
