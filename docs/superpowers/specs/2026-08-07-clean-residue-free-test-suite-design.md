# Clean, Residue-Free Test Suite Design

## Goal

Make the canonical build and test cycle easy to read and safe to repeat:
successful test runs emit concise summaries, failed streams retain complete
diagnostics, and the cycle does not accumulate undeclared system residue.

## Output Contract

`make test` remains the canonical default test entry point. On success it emits
only one concise `PASS` summary for each Go module and frontend stream, including
the existing useful timings. The frontend's typecheck, Vitest, Node test, and
Biome output stays captured during a successful run rather than leaking debug
messages or stack traces into the parent gate.

On failure, the gate prints the concise stream verdicts first and then replays
the complete captured log for failed streams only. Genuine assertion details,
stack traces, compiler errors, and setup failures must remain visible. Output
must be controlled at the stream boundary; a final line filter is forbidden
because it could hide new diagnostics.

Each frontend test file has exactly one runner. Vitest must not collect files
owned by Node's built-in test runner, and Node must continue to run those files
explicitly. This prevents duplicate execution, runner-mismatch failures, and
the misleading stack traces they create.

## Residue Contract

The build and test cycle is `make build` followed by `make test`. Intended
repository build products and documented reusable tool caches are distinct from
per-run residue. The cycle may update those declared outputs and caches, but it
must not leave per-run scratch directories, temporary homes, browser profiles,
logs, sockets, listeners, child processes, services, or undeclared files.

Residue investigation uses an Apple container as a disposable whole-system
boundary. It records the complete writable filesystem plus process, listener,
and socket state before and after a cold cycle, then repeats the cycle from the
warm state. The cold diff identifies every output and cache the cycle creates;
the warm diff distinguishes reusable state from artifacts that grow once per
run. `TMPDIR` is only one observed path and is not treated as the cleanup
boundary.

The resulting regression gate must be deterministic and local. It should test
owned scratch cleanup directly with isolated filesystem roots and fake external
dependencies where necessary. Apple container setup and whole-image comparison
are diagnostic evidence, not a dependency of `make test` or CI.

## Existing Failures Found During Baseline

The initial baseline exposed three independent failures that must be resolved
before residue can be measured on a successful cycle:

- Vitest collects `scripts/layoutguard/cdp.test.mjs`, although that file uses
  `node:test` and is also executed explicitly by Node.
- `cmd/serf-hub/frontend_hash.go` uses `crypto/sha256` without an entry in the
  repository's closed-world identifier audit.
- `TestWaveCompletesDespiteBlockedLeakCheck` uses a wall-clock completion bound
  and flakes under load instead of awaiting the behavior it intends to prove.

Each fix must address its root cause with the smallest reasonable change. The
timing flake may not be repaired by widening its timeout.

## Verification

Changes follow red-green TDD. Runner tests assert structured verdict behavior
and real cleanup rather than matching large rendered logs. The frontend gate is
formatted with Biome before its canonical checks.

Completion requires:

- focused tests for every corrected failure and cleanup mechanism;
- `npx biome check --write` on touched frontend files;
- `make test-web` and, on this Chrome-capable host, `make test-web-browser`;
- a successful `make build` followed by `make test` with concise output;
- a successful deterministic dev-tooling gate;
- a clean worktree after intended committed changes; and
- cold and warm Apple-container state diffs with no unexplained per-run residue.

## Non-Goals

This work does not add live-provider tests, make Apple container a developer or
CI prerequisite, discard failure logs, suppress real failures, redesign the
test scheduler, or remove reusable caches solely to make a snapshot empty.
