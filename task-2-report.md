STATUS: DONE

Commit hash(es):
- `3d934f0fa7ad7eda28ef438a27e146076beefecd` — `feat(web): add session panel pane toggles`

Files changed:
- `cmd/serf-hub/frontend/src/shell/workspace.ts`
  - Added `WorkspaceStoreState.togglePane` with secondary placement, exact-params deduplication, close/focus behavior, and `{ paneId, opened }` results.
  - Added `isPaneOpen`, `requestPaneFocus`, `consumePaneFocus`, and `cancelPaneFocus` APIs.
  - Kept `sameParams` module-private and reset pending focus markers with test store reset.
- `cmd/serf-hub/frontend/src/shell/paneActions.ts`
  - Added the imperative `togglePane` seam.
- `cmd/serf-hub/frontend/src/shell/workspace.test.ts`
  - Added deterministic toggle, reference distinction, pending-focus consumption/cancellation, and ordinary-open marker tests.
- `cmd/serf-hub/frontend/src/shell/paneActions.test.ts`
  - Added fake-Dockview-backed toggle-close coverage.
- `task-2-report.md`
  - This implementation report.

Commands and outcomes:
- `cd cmd/serf-hub/frontend && npx biome check --write src/shell/workspace.ts src/shell/paneActions.ts src/shell/workspace.test.ts src/shell/paneActions.test.ts` — passed; Biome formatted 1 file.
- `cd cmd/serf-hub/frontend && npx vitest run src/shell/workspace.test.ts src/shell/paneActions.test.ts` — passed: 2 files, 62 tests.
- `cd cmd/serf-hub/frontend && npx tsc --noEmit` — passed.
- `git diff --check` — passed.
- `git commit -m "feat(web): add session panel pane toggles"` — passed; commit `3d934f0fa7ad7eda28ef438a27e146076beefecd`.

Concerns:
- The pre-existing untracked `task-1-report.md` and `task-1-review.md` were left untouched and excluded from the commit.
