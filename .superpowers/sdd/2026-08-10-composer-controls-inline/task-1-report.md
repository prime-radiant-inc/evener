# Task 1 Implementation Report: Embedded SessionChrome Presentation

## Status

DONE

## Summary

Added an explicit `composer` presentation to `SessionChrome` while preserving the default standalone/footer presentation. The composer presentation renders exactly one inline cluster containing the existing `StatusRow` (model, native reasoning-effort control, and context meter) and the existing `SessionMenu` trigger. Footer-only cadence and goal controls are omitted from the composer cluster. Details, Tasks, and Activity panels remain mounted, and the existing callbacks/action adapters remain the single shared implementation for both presentations.

## Files changed

- `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
  - Added exported `SessionChromePlacement = "footer" | "composer"`.
  - Added optional `placement` prop, defaulting to `"footer"`.
  - Selected the root presentation/test hook from `placement`.
  - Rendered `StatusRow` directly in composer placement.
  - Preserved the existing footer body, cadence, and `GoalControl` path.
  - Kept the existing panel instances, refs, open callbacks, `SessionMenu`, and all action adapters in one shared JSX path.
- `cmd/serf-hub/frontend/src/panes/session/chrome/sessionchrome.module.css`
  - Added `.inline` as a non-wrapping, min-width-safe flex group and inline-size query container.
  - Reused `.right` and its inset `:focus-visible` treatment for the session-actions trigger.
  - Did not alter any `StatusRow` meter/percent container-query thresholds.
- `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`
  - Added a hydrated composer-placement contract test.
  - Asserted model, native reasoning-effort control, context, and `Session actions` are in the inline cluster and ordered.
  - Asserted composer placement creates exactly one status row and one menu trigger, and does not render standalone chrome, cadence, or goal controls.
  - Added an explicit default/footer preservation test.
  - Added deterministic CSS-source assertions for the inline flex/query-container contract.

## Design decisions

1. **Optional placement with footer default**
   - `placement?: "footer" | "composer"` keeps every existing caller on the standalone/footer presentation until Task 2 opts in.
2. **One control owner**
   - Both placements use the same `StatusRow`, panel refs, `openDetails`/`openTasks`/`openActivity` callbacks, panel components, `SessionMenu`, and action adapters.
   - Placement changes only the leading status content and root presentation class/test hook; no callback or menu action was duplicated.
3. **Native accessibility preserved**
   - Composer placement reuses `StatusRow` unchanged, including the native transparent `<select>` for reasoning effort and its label.
   - The shared `SessionMenu` keeps the exact `Session actions` accessible label.
4. **Container-query behavior preserved**
   - `.inline` owns `container-type: inline-size`, while the existing `StatusRow` remains shrinkable (`flex: 1 1 auto; min-width: 0`).
   - Existing 560/480/400px StatusRow thresholds and meter/percent rules were not modified.
5. **Self-review simplification**
   - The first green implementation stored the shared panel/menu subtree in a JSX variable. During self-review, it was simplified to one return tree with a conditional leading status section, reducing the final implementation diff while keeping one shared panel/menu path.

## TDD evidence and exact command results

All commands were run from the repository root unless the command starts with `cd cmd/serf-hub/frontend`.

### Dependency setup

The fresh worktree had no `node_modules`, so the first focused-test attempt could not load Vite (`ERR_MODULE_NOT_FOUND` for `@vitejs/plugin-react` and `vitest/config`). I installed the repository's locked dependencies:

```bash
cd cmd/serf-hub/frontend && npm ci
```

Result: exit 0; `added 126 packages, and audited 127 packages in 1s`. npm reported 4 existing lockfile vulnerabilities (2 moderate, 2 high); no dependency or lockfile was changed.

### RED

After adding the tests and before production implementation:

```bash
cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx
```

Result: exit 1; `1 failed` test file; `2 failed | 22 passed` tests. Expected failures:

- composer contract could not find `[data-testid="session-chrome-inline"]`;
- CSS contract could not find a `.inline` rule.

### Initial GREEN

```bash
cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx
```

Result: exit 0; `1 passed` test file; `24 passed (24)` tests.

### Required formatting

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/SessionChrome.tsx src/panes/session/chrome/sessionchrome.module.css src/panes/session/chrome/SessionChrome.test.tsx
```

Result: exit 0; `Checked 3 files in 28ms. Fixed 1 file.`

After the self-review simplification, the same command was rerun:

Result: exit 0; `Checked 3 files in 14ms. No fixes applied.`

### Final focused tests and typecheck

```bash
cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx && npm run typecheck
```

Final result after formatting and self-review: exit 0.

- Vitest: `1 passed` test file; `24 passed (24)` tests; duration `1.16s`.
- TypeScript: `tsc --noEmit --incremental false`; exit 0 with no diagnostics.

### Diff hygiene

```bash
git diff --check
```

Result: exit 0 with no output.

## Self-review

- Confirmed existing callers retain footer behavior because `placement` defaults to `"footer"`.
- Confirmed composer placement has a single `StatusRow` and a single `SessionMenu` trigger.
- Confirmed cadence and goal remain footer-only.
- Confirmed Details/Tasks/Activity panels remain mounted in both presentations.
- Confirmed all panel callbacks and every `SessionMenu` action adapter remain shared and unchanged in behavior.
- Confirmed the session-actions accessible name remains exactly `Session actions`.
- Confirmed the native reasoning-effort combobox remains present in composer placement.
- Confirmed no StatusRow container-query thresholds were changed.
- Confirmed only the three required implementation/test files were included in the implementation commit.

## Commits

- `21498ab91` — `refactor: embed session chrome controls`

This report is committed separately so it can contain the final implementation commit hash.

## Concerns

- No Task 1 implementation concerns.
- `npm ci` reported 4 existing dependency vulnerabilities (2 moderate, 2 high). They predate and are unrelated to this task; changing dependencies would be out of scope.
- Task 2 still needs to opt the composer caller into `placement="composer"`; the default remains footer intentionally.

## Fix Round 1: Preserve the StatusRow query-container boundary

### Review findings addressed

1. The initial composer topology made `.inline` both the row and the inline-size query container. Because `.inline` also contained the fixed, non-shrinking `.right` actions group, `StatusRow` container queries measured the total cluster width rather than the flex width available to status content.
2. The initial CSS-source test incorrectly required `container-type: inline-size` on `.inline`, codifying that shifted boundary.

### Root cause and fix

The footer topology already had the correct pattern: shrinkable status content inside `.body` (the query container), with `.right` as its fixed sibling. Composer placement had omitted the `.body` wrapper.

The fix is intentionally minimal:

- Wrapped the composer `StatusRow` in the existing `.body` class and added `data-testid="session-chrome-inline-status"` for deterministic topology coverage.
- Kept `.right` as the second direct child of `.inline`, outside the status query container.
- Removed `container-type` from `.inline`.
- Replaced the incorrect CSS expectation with assertions that `.inline` is not a query container and `.body` is.
- Added DOM assertions that the inline cluster is exactly `[status query container, fixed actions]`, that `StatusRow` is inside the first child, and that `Session actions` is only inside the second child.
- Did not change the existing 560/480/400px StatusRow thresholds.

### TDD evidence

After adding the regression assertions and before the production fix:

```bash
cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx
```

Result: exit 1; `1 failed` test file; `2 failed | 22 passed` tests. Expected failures:

- missing `[data-testid="session-chrome-inline-status"]` because the status-only wrapper did not exist;
- `.inline` still contained `container-type: inline-size`.

After wrapping `StatusRow` in `.body` and removing `container-type` from `.inline`, the same command returned exit 0 with `1 passed` test file and `24 passed (24)` tests.

### Final fix-round verification

```bash
cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/chrome/SessionChrome.tsx src/panes/session/chrome/sessionchrome.module.css src/panes/session/chrome/SessionChrome.test.tsx && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx && npm run typecheck
```

Result: exit 0.

- Biome: `Checked 3 files in 15ms. No fixes applied.`
- Vitest: `1 passed` test file; `24 passed (24)` tests; duration `1.16s`.
- TypeScript: `tsc --noEmit --incremental false`; no diagnostics.

### Fix-round self-review

- The nearest inline-size query container for composer `StatusRow` is now the shrinkable `.body` wrapper.
- The fixed `.right` actions group is a sibling, so its width is excluded from the queried content box.
- Footer topology and behavior are unchanged.
- Menu/panel ownership, callbacks, focus treatment, and native control accessibility are unchanged.
- No concerns remain from the two review findings.
