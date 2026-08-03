# Task 1 implementation report

## Summary
Implemented the desktop question replacement layout contract for the session composer pane by:
- keeping the composer region in the pane flex chain with `flex: 1 1 auto` and `min-height: 0`;
- bottom-aligning the composer/replacement slot with pane-local flex layout, not viewport-fixed positioning;
- adding focused regression coverage for the pending-question replacement state and the replacement-slot CSS contract.

## Files changed

### `cmd/serf-hub/frontend/src/panes/session/composer/composer.module.css`
- Added `justify-content: flex-end` to `.composer` so the active composer / AskDock replacement slot sits at the bottom of the filled pane region.
- Preserved the existing `flex: 1 1 auto` and `min-height: 0` contract.

### `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Added imports for reading the local CSS source contract in the focused test.
- Added a regression test that mounts the pending-ask state and verifies:
  - the AskDock replacement surface renders (`Answer the agent’s questions.`);
  - the pending question content is visible (`Ship now?`);
  - the normal message textbox is not rendered while ask-pending is active.
- Added a CSS-source contract test asserting the composer region still declares `flex: 1 1 auto`, `min-height: 0`, and `justify-content: flex-end`.

## Tests and commands

### Read-only inspection
- `sed -n '1,180p' docs/testing.md`
- `grep -n -C 5 "AskDock\|askPending\|composer" cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.test.tsx`
- Reviewed:
  - `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx`
  - `cmd/serf-hub/frontend/src/panes/session/composer/composer.module.css`
  - `cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.tsx`
  - `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`
  - `cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.test.tsx`

### Verification
- Initial attempt: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/composer/askDock/AskDock.test.tsx`
  - failed because `npx` was not available in the environment.
- Successful focused test run:
  - `/opt/homebrew/bin/node ./node_modules/.bin/vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/composer/askDock/AskDock.test.tsx`
  - Result: 2 test files passed, 114 tests passed.
- `git diff --check`
  - passed.

## Self-review
- Change scope stayed minimal: only the composer layout CSS and focused composer/AskDock regression tests changed.
- The implementation follows the approved spec by using pane-local flex layout only; it does not introduce fixed positioning or alter question state/control logic.
- The new test coverage is at the DOM/state boundary, with CSS source checks kept local and focused.

## Commit
- Pending at the time of writing this report; the final commit hash will be recorded after `git commit`.

## Concerns
- The environment did not provide `npx`, so verification had to use the local Node binary at `/opt/homebrew/bin/node` with the repo’s checked-in Vitest binary.
- The CSS-source assertion proves the contract in the stylesheet, but it does not measure rendered box geometry; that remains covered indirectly by the existing frontend layout behavior and the AskDock contract tests.
