# Deterministic execenv Test Fixtures

## Problem

The `agent/execenv` package has two independent groups of deterministic-test
failures on macOS.

First, sandbox fixture constructors use `t.TempDir()` paths verbatim. The host
spells `TMPDIR` beneath `/var`, which is a symlink to `/private/var`. Read-only
and workspace-write file-tool reads outside writable roots deliberately use the
absolute no-symlink walker. The walker therefore refuses the fixture path at the
`/var` component before the test reaches the behavior it intends to exercise.
The resulting failures cover read, list, glob, grep, and file-existence tests,
but they all share this fixture-path cause.

Second, `TestCleanupDisposesOwnedTmpAfterChildGrace` uses a Go raw string for a
Bash TERM trap while also escaping its double quotes. Bash treats those escaped
quotes as literal path characters. The trap consequently checks a nonexistent
quoted directory name and never writes its sentinel, even though cleanup keeps
the owned temporary directory alive for the grace period.

## Evidence

- The host `TMPDIR` is beneath `/var/folders/...`; resolving it produces
  `/private/var/folders/...`, and `/var` is a symlink.
- Running the affected sandbox tests with the resolved `TMPDIR` passes five
  consecutive repetitions.
- The lifecycle test fails twenty consecutive repetitions with the existing
  trap.
- Executing the current escaped Bash directory expression misses `/tmp`; the
  same expression with ordinary shell quoting matches `/tmp`.

## Design

Keep the production sandbox and lifecycle behavior unchanged.

Add a test helper that creates a temporary directory and resolves its existing
path components with `filepath.EvalSymlinks`. Use that helper only in the shared
sandbox fixture constructors whose paths are intentionally passed through the
no-symlink enforcement layer. The fixtures will then exercise the declared
sandbox contracts without an ambient macOS system symlink becoming part of the
test case. Tests that intentionally construct symlinks remain unchanged.

Correct the lifecycle test's TERM trap by removing the unnecessary backslashes
before its double quotes. The raw Go string will then deliver normal quoted Bash
variables, so the existing sentinel proves that the owned temporary directory
was present when TERM was handled.

## Alternatives Rejected

- Following `/var` in production would weaken the sandbox's explicit
  no-symlink absolute-read guarantee to repair test setup.
- Setting a process-wide canonical `TMPDIR` in `TestMain` would alter unrelated
  tests and hide which fixtures require real paths.
- Increasing the test termination grace would not fix the malformed trap and
  would add wall-clock cost without making the assertion meaningful.

## Verification

The implementation must preserve the observed red baseline and then pass:

1. The focused sandbox and lifecycle regression set.
2. Repeated focused runs to catch fixture or process nondeterminism.
3. `go test ./agent/execenv -count=1`.
4. The repository's `make test` gate.
5. `git diff --check`.

No provider credentials, network calls, ambient model behavior, sleeps, or
weakened assertions are added.
