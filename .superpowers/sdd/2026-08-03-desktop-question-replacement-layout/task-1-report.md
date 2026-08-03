# Task 1 implementation report

## Final fix wave report

### Findings addressed
- **Important 1 — AskDock remained top-aligned:** `.dock` had `flex: 1 1 auto`, so it consumed the composer region's free space and prevented the parent `.composer` `justify-content: flex-end` from bottom-anchoring the active question surface. Changed AskDock to `flex: 0 1 auto`; the filled parent remains responsible for available-space allocation and bottom alignment. Existing `min-height: 0`, `max-height: 100%`, and `overflow-y: auto` were preserved for tall question content.
- **Important 2 — no rendered geometry coverage:** Added the `composer-desktop-question-replacement` layoutguard case. It runs the real CSS in fresh headless Chrome and asserts geometry relationships rather than pixel snapshots: the short question dock has free slot space and its bottom equals the replacement slot bottom; a tall question exceeds its dock and remains internally scrollable; the dock does not overlap the status footer; the transcript retains usable height; and the outer document does not scroll.

### Files changed in the final fix
- `cmd/serf-hub/frontend/src/panes/session/composer/askDock/askdock.module.css`
  - Changed `.dock` from `flex: 1 1 auto` to `flex: 0 1 auto`.
- `cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.test.tsx`
  - Updated the focused CSS contract to assert the intentional shrink-only dock behavior.
- `cmd/serf-hub/frontend/scripts/layoutguard/cases/composer-desktop-question-replacement/{case.json,harness.html,assert.mjs}`
  - Added deterministic real-browser replacement-slot geometry coverage.

### Verification commands and outputs
- Focused Vitest:
  - `cd cmd/serf-hub/frontend && /opt/homebrew/bin/node ./node_modules/.bin/vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/composer/askDock/AskDock.test.tsx`
  - **PASS — 2 files, 114 tests.**
- Typecheck:
  - `cd cmd/serf-hub/frontend && /opt/homebrew/bin/node ./node_modules/.bin/tsc --noEmit --incremental false`
  - **PASS.**
- Desktop geometry contract:
  - `cd cmd/serf-hub/frontend && /opt/homebrew/bin/node scripts/layoutguard/run.mjs composer-desktop-question-replacement`
  - **PASS — desktop replacement slot fills, bottom-anchors, and internally scrolls.**
- Responsive geometry contracts:
  - `cd cmd/serf-hub/frontend && /opt/homebrew/bin/node scripts/layoutguard/run.mjs askdock-mobile-tall-crush`
  - **PASS — ask dock geometry holds at phone width.**
  - `cd cmd/serf-hub/frontend && /opt/homebrew/bin/node scripts/layoutguard/run.mjs session-ask-pane-allocation`
  - **PASS — pane allocates usable transcript and question scroll regions without outer scrolling.**
- `git diff --check`
  - **PASS.**

### Self-review
- The fix is pane-local flex layout only; no viewport-fixed positioning was introduced.
- Composer conditional rendering, question state, controls, card structure, submission behavior, and mobile viewport/overscroll rules were not changed.
- The geometry contract uses relationships and measured overflow behavior, not hardcoded rendered coordinates or screenshots, avoiding font/platform brittleness while catching the exact flex-growth regression.
- Existing unrelated workspace modifications/untracked files were left untouched.

### Commit
- Pending until the focused final-fix commit is created.

### Concerns
- `npx` is unavailable in this environment; commands used the checked-in local Node/Vitest/TypeScript binaries at `/opt/homebrew/bin/node`.
- The repository has unrelated pre-existing modifications and untracked files; only the final-fix files listed above should be staged.
