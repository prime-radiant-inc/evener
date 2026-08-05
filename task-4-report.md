
## Fix round 1

### Status
Addressed review findings C1, I1, I2, I3, and I4. Added the ref-keyed Activity summary store, mounted-body tracking, root-only summary publication, established-attempt/freshness gates, collapsed-trigger suppression, complete-count badge gating, one-fetch-at-a-time protection, background refresh, all-store eviction registration, stale toast generation guards, deferred/overlap tests, and continuation/reconciliation/remount coverage.

### Fix commit
`28e2ec90768ae7252dfcd4a77401b31df76ef066` — `fix(web): complete panel state retention review`

### Files changed
- `cmd/serf-hub/frontend/src/stores/activitySummary.ts`
- `cmd/serf-hub/frontend/src/stores/activitySummary.test.ts`
- `cmd/serf-hub/frontend/src/stores/activityPanel.test.ts`
- `cmd/serf-hub/frontend/src/stores/panelStoreEviction.test.ts`
- `cmd/serf-hub/frontend/src/stores/tasksPanel.test.ts`
- `cmd/serf-hub/frontend/src/stores/threads.ts`
- `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx`

### Commands and exact outcomes
- `cd cmd/serf-hub/frontend && npx biome check --write src/stores/activitySummary.ts src/panes/session/chrome/ActivityPanel.tsx src/panes/session/chrome/TasksPanel.tsx src/stores/activitySummary.test.ts src/stores/panelStoreEviction.test.ts src/stores/tasksPanel.test.ts src/stores/threads.ts`
  - Passed; no remaining Biome fixes.
- `cd cmd/serf-hub/frontend && npx tsc --noEmit --incremental false`
  - Passed with exit code 0.
- `cd cmd/serf-hub/frontend && npx vitest run src/stores/activitySummary.test.ts src/stores/activityPanel.test.ts src/stores/tasksPanel.test.ts src/stores/panelStoreEviction.test.ts src/panes/session/chrome/TasksPanel.test.tsx src/panes/session/chrome/ActivityPanel.test.tsx --reporter=dot --testTimeout=10000`
  - Passed: 6 test files, 67 tests.
- `cd cmd/serf-hub/frontend && npx vitest run src/stores src/panes/session/chrome/TasksPanel.test.tsx src/panes/session/chrome/ActivityPanel.test.tsx --reporter=dot --testTimeout=10000`
  - Passed: 18 test files, 486 tests.
- `git diff --check`
  - Passed.

### Concerns
- The fix-round report is being appended before committing so the final commit hash can be recorded exactly.
- Existing untracked Task 1–3 report/review files remain untouched.
- Browser geometry checks were not run; no browser guard was part of the requested review-fix commands.
