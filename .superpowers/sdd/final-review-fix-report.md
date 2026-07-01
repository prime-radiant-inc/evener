# Final review fix report: shell/tool renderer branch

## Summary

Addressed the final whole-branch review findings for `wip/shell-tool-renderer`.

### Important 1: Tool metadata hover/focus-only on desktop

Changed `cmd/serf-hub/assets/style.css` so desktop `.tool-call .tool-meta` is readable by default:

- `.tool-call .tool-meta` now uses `opacity: 1` by default.
- Removed the desktop hover/focus-only reveal rules for `.tool-call:hover .tool-meta` and `.tool-call:focus-within .tool-meta`.
- Kept the existing mobile overflow protection that hides `.tool-call .tool-meta` inside the compact mobile `@media (max-width: 767px)` block.
- Updated the mobile CSS comment to describe this as compact mobile overflow behavior, not a hover-only metadata contract.

Updated tests:

- `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js` now asserts desktop `.tool-meta` is readable by default and does not depend on hover/focus reveal rules.
- `cmd/serf-hub/jstest/test-mobile-css.js` now documents/asserts the mobile hide as compact mobile overflow behavior.

### Important 2: Body variant contract for `write_file` and `web_fetch`

Changed `cmd/serf-hub/assets/renderer-tools.js`:

- `diffRenderer(friendly)` now creates a standardized wrapper:
  - `div.tool-body.<friendly>-body.tool-body--diff`
  - containing `pre.diff-body`
- This gives `write_file` a `div.tool-body.write-body.tool-body--diff` wrapper while preserving the existing `pre.diff-body` diff rendering path.
- `webFetchRenderer` now renders `div.tool-body.fetch-body.tool-body--preview`.

Updated tests in `cmd/serf-hub/jstest/test-tool-renderers.js`:

- Added deterministic `write_file` assertions for `.write-body`, `.tool-body`, `.tool-body--diff`, and nested `pre.diff-body` content.
- Added deterministic `web_fetch` assertions for `.fetch-body`, `.tool-body`, `.tool-body--preview`, and existing three-line preview behavior.

### Minor low-risk fixes included

- Added a Space-key assertion to the existing disclosure keyboard accessibility test. The test now verifies Enter expands and Space collapses.
- Added a shell alias smoke test covering both `exec_command` and `run_shell_command`, asserting they use the terminal renderer contract and command prompt output.

## Files changed intentionally

- `cmd/serf-hub/assets/style.css`
- `cmd/serf-hub/assets/renderer-tools.js`
- `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`
- `cmd/serf-hub/jstest/test-mobile-css.js`
- `cmd/serf-hub/jstest/test-tool-renderers.js`
- `.superpowers/sdd/final-review-fix-report.md`

## Files intentionally not staged

- `.superpowers/sdd/task-1-report.md` was already modified before this fix and was not staged.

## Verification

All required verification commands passed from `/home/jesse/git/prime-radiant/serf/.worktrees/shell-tool-renderer`.

1. `node cmd/serf-hub/jstest/test-tool-renderers.js`
   - Exit code: 0
   - Result: all scenario checks passed, including new `write_file`, `web_fetch`, shell alias, and Space-key checks.

2. `node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`
   - Exit code: 0
   - Output: `PASS: pane compact and full-border sidebar resize CSS contracts`

3. `node cmd/serf-hub/jstest/test-mobile-css.js`
   - Exit code: 0
   - Output: `PASS: mobile search palette CSS contract + layout guards`

4. `cd cmd/serf-hub && ./jstest/run-all.sh`
   - Exit code: 0
   - Output ended with: `jstest: all tests passed`

5. `go test ./cmd/serf-hub -count=1`
   - Exit code: 0
   - Output: `ok  	primeradiant.com/serf/cmd/serf-hub	4.695s`

## Commit

Created commit:

- `fix(hub): address tool renderer review findings`
