# Task 1 implementation report

## Status

Complete. Implemented the pure lossless web shell-command formatter and its deterministic Vitest coverage.

## Commit(s)

- `7ce370e46` — `feat(web): add lossless shell command formatter`
- The report will be committed separately after this file is written.

## Files changed

- `cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.ts`
  - Added `ShellCommandLine` and `formatShellCommand`.
  - Scans left to right using raw source slices.
  - Protects operators inside quotes, comments, and nested parentheses/braces.
  - Preserves source newlines, horizontal whitespace, escapes, malformed input, and trailing operators.
- `cmd/serf-hub/frontend/src/widgets/shellcommand/shellCommand.test.ts`
  - Added the specified table-driven formatter cases and no-loss invariant.
  - Added explicit source-newline and operator-before-newline cases.

## Tests and checks

- `npm test -- --run src/widgets/shellcommand/shellCommand.test.ts` — PASS, 1 test file and 11 tests.
- `npx biome check src/widgets/shellcommand/shellCommand.ts src/widgets/shellcommand/shellCommand.test.ts` — PASS.
- `npm run typecheck` — PASS.
- `git diff --check` — PASS.
- The required RED run before implementation failed because `./shellCommand` did not exist, as expected.

## Self-review

- Confirmed the implementation is limited to Task 1 and has no later-task imports or unrelated edits.
- Confirmed operator text and adjacent horizontal whitespace remain in the preceding raw slice.
- Confirmed the no-loss assertion joins formatted text and compares it with the source with only newlines removed.
- Confirmed normal commit hooks remained enabled.

## Concerns

The brief's source-continuation fixture contains two runtime backslashes in `raw` but expects one runtime backslash in its first output line. Those expectations contradict the required no-loss invariant. The test keeps the raw value verbatim and expects both backslashes in the output, preserving the stated lossless contract.
