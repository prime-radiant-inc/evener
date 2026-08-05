# Task 3 Report: Prove the web control re-enables and sends the switch

## Status

Implemented the capability-recovery rerender regression in `ModelSwitch.test.tsx`. The test starts with an active failed turn whose `changeModel` capability is false, rerenders with the idle snapshot and capability restored, verifies the control re-enables without reload, then selects `openai/gpt-5.5` and asserts the qualified `thread/model/set` parameters.

## Files

- Modified: `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx`
- Report: `.superpowers/sdd/2026-08-05-recoverable-provider-failures/task-3-report.md`

No production frontend or protocol files were modified.

## Commands and results

1. `cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/ModelSwitch.test.tsx`
   - **Result:** exit 1 before Biome ran.
   - **Reason:** `npx` attempted to resolve `biome` from `https://registry.npmjs.org/biome`; network access is unavailable (`ENOTFOUND`).

2. `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/ModelSwitch.test.tsx`
   - **Result:** exit 1 before Vitest ran.
   - **Reason:** `npx` attempted to resolve `vitest` from `https://registry.npmjs.org/vitest`; network access is unavailable (`ENOTFOUND`).

3. `git diff --check`
   - **Result:** exit 0.

The required commands were explicitly attempted in the required order: Biome first, then focused Vitest. Both gates remained unavailable because the frontend tools were not locally available and the sandbox has no network access.

## Commit

- Commit: `test: cover model switch recovery in web`
- Commit hash: `7b08190d8`
- Only `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx` was staged and committed.

## Self-review

- Uses the existing `FakeClient`, `modelListResponse`, `testModel`, and `CAPABILITIES` fixtures.
- Exercises `view.rerender` from failed active-turn capabilities to idle recovered capabilities.
- Verifies disabled-to-enabled transition and the exact qualified provider/model payload.
- Does not modify `ModelSwitch.tsx` or protocol production code.
- `activeTurnId: undefined` matches the existing `ThreadModel` fixture/type usage.

## Concerns

The focused test and required Biome command remain unverified due to unavailable local npm tooling and blocked registry access. No code-level concern was found; the diff passes `git diff --check`.
