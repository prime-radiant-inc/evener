# Repository Gate Cleanup Design

## Scope

Restore the repository's ordinary local gates without changing product behavior:

1. Make `make test` independent of the spelling of an existing `TMPDIR`.
2. Resolve the 39 current `make lint` findings with the smallest idiomatic edits.

The work does not add compatibility paths, new runtime configuration, or benchmark-specific behavior.

## Test-runner root cause

`scripts/gate/run-module-tests.sh` builds its owned log directory from
`"${TMPDIR:-/tmp}/evener-module-tests.XXXXXX"`. macOS normally exports `TMPDIR`
with a trailing slash, so the resulting string contains `//`. The filesystem
accepts that spelling, but Go and path helpers later return the normalized
single-separator spelling. Tests that correctly compare absolute paths then see
different strings for the same directory and fail.

The runner owns the path it derives, so it should remove the caller's one
optional trailing separator before appending its own. The filesystem location
does not change. A behavioral self-test will invoke the runner with a trailing
separator and require every child stream to receive a normalized owned temp
root. This exercises the real runner with fake external commands rather than
matching rendered shell source.

## Lint cleanup

The lint baseline is finite and already classified by the repository's
configured linters: 7 root-module findings, 30 agent-module findings, and 2
identifier-module findings. Apply only the transformations each finding calls
for: propagate writer errors where the function already returns an error,
delete the unused wrapper, and use the standard-library forms selected by the
configured Go toolchain. Preserve comments and behavior.

Static analysis is the red/green contract for these mechanical findings.
Focused package tests cover files whose executable code changes, followed by
the repository-wide gates.

## Verification

Run, in order:

1. `scripts/run-module-tests-selftest.sh`
2. `make lint`
3. `make test`
4. `make merge-approval-gate`

The final gate serially repeats lint, build, full deterministic tests, and all
development-tooling self-tests.
