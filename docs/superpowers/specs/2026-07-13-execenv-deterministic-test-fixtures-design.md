# Deterministic execenv Test Fixtures

## Problem

The default suite has three independent groups of deterministic-test failures
on macOS.

First, sandbox fixture constructors use `t.TempDir()` paths verbatim. The host
spells `TMPDIR` beneath `/var`, which is a symlink to `/private/var`. Read-only
and workspace-write file-tool reads outside writable roots deliberately use the
absolute no-symlink walker. The walker therefore refuses the fixture path at the
`/var` component before the test reaches the behavior it intends to exercise.
The resulting failures cover read, list, glob, grep, and file-existence tests,
but they all share this fixture-path cause.

Second, `TestCleanupDisposesOwnedTmpAfterChildGrace` ends its Bash script with
the external command `sleep 300`. Bash tail-executes that final command after
creating the readiness file, so the tracked PID becomes `sleep` and the shell's
TERM trap no longer exists. Cleanup signals the tracked process correctly, but
the fixture has discarded the observer that was supposed to write the sentinel.

Third, `TestRunProbeOfflineStates` gives a spawned fake Serf CLI a one-second
deadline even though timeout behavior is not the contract under test. The
repository gate runs modules concurrently, and under that load the otherwise
immediate fake process was killed at 1,001 ms and reported `blocked_infra`.

## Evidence

- The host `TMPDIR` is beneath `/var/folders/...`; resolving it produces
  `/private/var/folders/...`, and `/var` is a symlink.
- Running the affected sandbox tests with the resolved `TMPDIR` passes five
  consecutive repetitions.
- The lifecycle test fails twenty consecutive repetitions with the existing
  final command.
- After readiness, the tracked PID reports its command as `sleep`, and `Wait`
  reports signal termination without any trap output.
- Replacing the final command with `sleep 300 & wait` keeps the tracked command
  as `/bin/bash`; the trap observes the scratch directory and passes ten
  consecutive repetitions.
- `TestRunProbeOfflineStates` completes in 180–280 ms for ten isolated runs but
  hit its exact one-second deadline during the concurrent repository gate.
- The explicit timeout state is independently covered with an immediate-cancel
  configuration in `coverage_program_fuzz_test.go`.

## Design

Keep the production sandbox and lifecycle behavior unchanged.

Add a test helper that creates a temporary directory and resolves its existing
path components with `filepath.EvalSymlinks`. Use that helper only in the shared
sandbox fixture constructors whose paths are intentionally passed through the
no-symlink enforcement layer. The fixtures will then exercise the declared
sandbox contracts without an ambient macOS system symlink becoming part of the
test case. Tests that intentionally construct symlinks remain unchanged.

Keep Bash alive in the lifecycle fixture by backgrounding the sleeper and using
the shell's `wait` builtin. Bash remains the tracked process and retains its TERM
trap, while the background sleeper remains in the same process group. The
existing sentinel then proves that the owned temporary directory was present
when TERM was handled.

Give the offline fake CLI the repository's conventional five-second test guard.
This remains a bounded failure detector while removing a machine-load deadline
from a test whose assertions concern pass, skip, invalid-fixture, and
infrastructure-result classification rather than timeout behavior.

## Alternatives Rejected

- Following `/var` in production would weaken the sandbox's explicit
  no-symlink absolute-read guarantee to repair test setup.
- Setting a process-wide canonical `TMPDIR` in `TestMain` would alter unrelated
  tests and hide which fixtures require real paths.
- Increasing the test termination grace would not restore a trap discarded by
  tail-exec and would add wall-clock cost without making the assertion meaningful.
- Removing the offline probe deadline entirely would turn a broken child process
  into an unbounded test hang. Five seconds keeps the guard while matching other
  subprocess tests in the repository.

## Verification

The implementation must preserve the observed red baseline and then pass:

1. The focused sandbox and lifecycle regression set.
2. Repeated focused runs to catch fixture or process nondeterminism.
3. `go test ./agent/execenv -count=1`.
4. Repeated focused tool-fluency runs.
5. The repository's `make test` gate.
6. `git diff --check`.

No provider credentials, network calls, ambient model behavior, sleeps, or
weakened assertions are added.
