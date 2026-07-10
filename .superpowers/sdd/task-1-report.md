# Task 1: Deterministic Fork Failure Tests

## Scope

Repair the two pre-existing deterministic agent baseline failures reported by
`make test`:

- `TestW3Init_ForkSession_OpenError`
- `TestW3Init_ForkSession_NewWriterError`

No commit was created.

## Root Cause

Both tests attempted to induce filesystem errors through Unix permission bits:

- The open-error test set the transcript file to mode `000`.
- The new-writer-error test set the sessions directory to mode `0500`.

The test process runs as UID 0 in this environment. Root can read the mode-000
file and create a child file in the mode-0500 directory, so neither intended
fault occurred. The successful open then advanced to `LoadSessionMeta`; because
the open-error test had not created parent metadata, it failed with a
missing-meta error instead. The writer path completed successfully and returned
`nil`.

This was an ambient-machine-state test design error, not a production fork
behavior bug.

## Design

`ForkSession` now delegates to an unexported `forkSessionFS` helper supplied
with an `afero.Fs`. The public path passes `afero.NewOsFs()`, preserving normal
production behavior. The helper sends transcript reads/writes and session-meta
reads/writes through that same filesystem.

The existing private filesystem implementations were exposed as narrow public
wrappers:

- `transcript.NewWriterWithFS`
- `schema.SaveSessionMetaWithFS`
- `schema.LoadSessionMetaWithFS`

The repaired tests use real Serf persistence below a fake external filesystem
boundary:

- `fault.FS` injects the parent transcript open failure.
- `afero.NewReadOnlyFs` permits loading a seeded parent but rejects child
  transcript creation.

This avoids global hooks, permission assumptions, and mocks of Serf internals.

## TDD Evidence

### Initial Reproduction

Command:

```sh
go test ./agent -run '^(TestW3Init_ForkSession_OpenError|TestW3Init_ForkSession_NewWriterError)$' -count=1 -v
```

Result: failed deterministically.

- `TestW3Init_ForkSession_OpenError`: received `load parent session meta: ... no
  such file or directory`, not `open parent transcript`.
- `TestW3Init_ForkSession_NewWriterError`: received `err = <nil>`.

### Red

The tests were rewritten first to require the real filesystem seam. Before the
seam existed, the same focused command failed to build with:

```text
undefined: forkSessionFS
undefined: schema.SaveSessionMetaWithFS
```

### Green

After implementing the filesystem seam, the focused command passed both tests:

```sh
go test ./agent -run '^(TestW3Init_ForkSession_OpenError|TestW3Init_ForkSession_NewWriterError)$' -count=1 -v
```

Result:

```text
PASS
ok   primeradiant.com/serf/agent  0.016s
```

## Files Changed

- `agent/fork.go`
  - Added `forkSessionFS` and routed the public `ForkSession` through an OS
    filesystem instance.
  - Routed transcript and metadata persistence through the supplied filesystem.
- `agent/cov_w3init_fork_test.go`
  - Replaced chmod-based fault simulation with deterministic faulting/read-only
    `afero` filesystems.
- `agent/schema/snapshot.go`
  - Added thin `SaveSessionMetaWithFS` and `LoadSessionMetaWithFS` wrappers over
    existing filesystem-injecting implementations.
- `agent/transcript/transcript.go`
  - Added thin `NewWriterWithFS` wrapper over the existing
    filesystem-injecting writer implementation.

## Verification

All commands ran from the coverage worktree unless noted otherwise.

```sh
go test ./agent -run '^(TestW3Init_ForkSession_OpenError|TestW3Init_ForkSession_NewWriterError)$' -count=1 -v
```

Passed both repaired tests.

```sh
cd agent && go test ./... -count=1
```

Passed all agent-module packages (exit 0).

```sh
go test ./agent -run '^(TestForkSession_|TestS2Cov_ForkSession_|TestW3Init_ForkSession_)' -count=1 -v
```

Passed the complete fork-focused suite, including existing happy paths and all
fault arms.

```sh
git diff --check -- agent/fork.go agent/cov_w3init_fork_test.go agent/schema/snapshot.go agent/transcript/transcript.go
```

Passed with no whitespace errors.

```sh
make test
```

Passed with exit code 0:

```text
PASS  .        12.48s
PASS  agent    7.39s
PASS  llm      2.03s
PASS  auth     1.11s
PASS  envvars  0.18s
PASS  invariant 0.16s
```

## Self-Review

- The fault injection is per-call and carries no mutable global state, so the
  existing `t.Parallel()` tests remain race-safe.
- The tests exercise the real scanner, header parsing, metadata load, and
  transcript writer paths. Only the external filesystem behavior is controlled.
- Production still uses the OS filesystem. The new public wrappers only expose
  existing implementations and do not alter serialization, paths, or error
  wrapping.
- No known remaining concern for this repair. The deliberately expanded
  filesystem seam is also useful for deterministic persistence fuzzing.

## Review Fix: Child Transcript Create Fixture

### Root Cause

`TestW3Init_ForkSession_NewWriterError` wrapped the seeded in-memory filesystem
in `afero.NewReadOnlyFs`. `transcript.NewWriterWithFS` calls `MkdirAll` before
`Create`, so the fixture deterministically failed at `create transcript dir`
rather than reaching the intended child transcript `Create` boundary. Its old
error-text-only assertion accepted that wrong failure.

### TDD Evidence

The test assertion was strengthened first to require that the returned error
wrap `fault.ErrInjected`, while retaining the read-only fixture. The focused
test then failed for the intended diagnostic reason:

```sh
go test ./agent -run '^TestW3Init_ForkSession_NewWriterError$' -count=1 -v
```

```text
cov_w3init_fork_test.go:108: err = create child transcript: create transcript dir: operation not permitted, want wrapped injected child transcript create error
FAIL
```

The fixture was then replaced with a test-only Afero wrapper that delegates all
operations except `Create` for a sibling `.transcript.jsonl` path other than
the seeded parent. It returns an `os.PathError` wrapping `fault.ErrInjected`.
That permits the parent transcript/meta reads and writer `MkdirAll`, while
failing precisely the generated child transcript creation.

```sh
go test ./agent -run '^TestW3Init_ForkSession_NewWriterError$' -count=1 -v
```

```text
PASS
ok   primeradiant.com/serf/agent  0.016s
```

### Files

- `agent/cov_w3init_fork_test.go`: replaced only the broad read-only fixture,
  and asserted the wrapped injected cause.
- `.superpowers/sdd/task-1-report.md`: appended this review-fix record.

No production files changed.

### Verification

```sh
go test ./agent -run '^(TestW3Init_ForkSession_OpenError|TestW3Init_ForkSession_NewWriterError)$' -count=1 -v
```

Result: both tests passed (`ok primeradiant.com/serf/agent 0.015s`).

```sh
go test ./agent -run '^(TestForkSession_|TestS2Cov_ForkSession_|TestW3Init_ForkSession_)' -count=1 -v
```

Result: the complete fork family passed (`ok primeradiant.com/serf/agent 0.151s`).

```sh
git diff --check -- agent/cov_w3init_fork_test.go
```

Result: passed with no whitespace errors.

### Concerns

None.
