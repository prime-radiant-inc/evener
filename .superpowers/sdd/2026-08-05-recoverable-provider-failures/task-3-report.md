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

## Follow-up Formatting Verification

The frontend dependencies became available during the canonical gate run. The required formatter command was rerun exactly:

```text
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/ModelSwitch.test.tsx
```

- **Result:** exit 0; `Checked 1 file in 13ms. Fixed 1 file.`
- **Change:** formatting only. Biome collapsed the multiline `await waitFor` assertion at lines 172-176 into one line; no behavior, imports, fixtures, assertions, or production files changed.

Canonical gate:

```text
make test-web
```

- **Result:** exit 2.
- `PASS  web-typecheck`
- `FAIL  web-test`: Vitest completed **308 passed test files / 5551 passed tests**, but the subsequent `scripts/browserGuardProcess.test.mjs` Node tests both failed because the sandbox denies loopback listening: `listen EPERM: operation not permitted 127.0.0.1`. The second failure consequently received the listen EPERM error instead of the expected `chrome startup failed` error.
- `PASS  web-lint`
- The failure is environmental and unrelated to the formatting change; the touched `src/panes/session/chrome/ModelSwitch.test.tsx` suite passed (22 tests).

Post-format checks:

- `git diff --check`: exit 0.
- `git status --short` before commit: only `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx` and this report were modified.

## Formatting Self-review

- Diff is limited to Biome's line wrapping of the existing assertion.
- No behavior or test intent changed.
- No unrelated files were modified.
- The web test suite and web lint passed; the only gate failure is the sandbox's prohibited loopback socket operation.
