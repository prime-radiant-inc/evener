# Task 2 Implementation Report: Inline Composer Controls

## Status

DONE

## Summary

Moved the existing shared `SessionChrome` control cluster into the composer's `PromptCard.leading` row immediately after the attachment paperclip. The inline cluster reuses Task 1's `placement="composer"` presentation, so the production order is attachment → model → reasoning effort → context meter → session actions. The standalone footer `SessionChrome` mount was removed, leaving exactly one status/control cluster. Existing ended-session gating remains authoritative: an unengaged ended follow-up card mounts neither the paperclip nor inline session chrome.

## Files changed

- `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx`
  - Imported `SessionChrome`.
  - Wrapped the existing attachment control and `<SessionChrome ref={ref} placement="composer" />` in one leading flex group.
  - Kept the whole leading group inside the existing `ended && !followUpEngaged ? undefined : ...` gate.
- `cmd/serf-hub/frontend/src/panes/session/composer/composer.module.css`
  - Added only `.leading`: a flex row with the existing control-row gap, `flex: 1 1 auto`, and `min-width: 0` so Task 1's inline cluster can consume and safely shrink within the space left by the PromptCard action group.
  - Did not duplicate or alter `StatusRow`, meter, menu, or container-query styles.
- `cmd/serf-hub/frontend/src/panes/session/Session.tsx`
  - Removed the standalone `SessionChrome` import and footer child.
  - Preserved the footer wrapper, measured composer flow, `LivenessLine`, and `PendingChips` placement.
  - Updated the layout comment to describe the new inline ownership.
- `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`
  - Extended the shared PromptCard test to assert the inline cluster is inside the card and follows the attachment control in document order.
  - Asserted the context control is present and exactly one `StatusRow` exists.
  - Added an explicit assertion that unengaged ended sessions do not mount inline chrome.
  - Kept the existing verb-order test focused on `composer-*` buttons now that model switching is also a button in the same card.
- `cmd/serf-hub/frontend/src/panes/session/Session.test.tsx`
  - Updated the Session boundary stub and placement assertion to require inline chrome inside the composer boundary and no standalone `session-chrome` in the footer.
  - Left all other Session behavior coverage unchanged.

## TDD evidence and exact command results

All commands were run from `cmd/serf-hub/frontend` unless otherwise noted.

### RED

The required focused command was run before production changes:

```bash
npx vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/Session.test.tsx
```

The first two authoring runs exposed test-harness issues rather than product behavior:

1. Exit 1; `2 failed | 136 passed`. Composer correctly failed because `session-chrome-inline` was absent, while the Session assertion queried `pane-footer` before hydration completed.
2. Exit 1; `2 failed | 136 passed`. Composer still had the expected placement failure; the Session test reached its assertion but used unsupported `toBeInTheDocument` matcher syntax in this repository.

After fixing only those test issues, the same command produced the valid red state:

- Exit 1.
- `2 failed | 136 passed (138)`.
- Composer failure: unable to find `[data-testid="session-chrome-inline"]`.
- Session failure: expected standalone `[data-testid="session-chrome"]` to be null, but it was still mounted.

### Initial GREEN and focused-test correction

After the production wiring, the required Step 4 command was run:

```bash
npx vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/Session.test.tsx src/panes/session/chrome/SessionChrome.test.tsx
```

Initial result: exit 1; `2 failed | 160 passed (162)`. Both failures were scoped test assumptions revealed by the intended new controls:

- The placement fixture lacked context usage/window values, so `status-row-context` correctly did not render.
- The existing Stop/Send/Steer order test collected every test-id button in the card and now also collected the intended `model-switch-trigger`.

The fixture was given real context values, and the verb-order test was narrowed to `composer-*` buttons. No production code was changed for these failures.

### Required formatting

```bash
npx biome check --write src/panes/session/composer/Composer.tsx src/panes/session/composer/composer.module.css src/panes/session/Session.tsx src/panes/session/composer/Composer.test.tsx src/panes/session/Session.test.tsx
```

Final result: exit 0; `Checked 5 files in 37ms. No fixes applied.`

### Final focused verification

```bash
npx vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/Session.test.tsx src/panes/session/chrome/SessionChrome.test.tsx
```

Final result: exit 0.

- Test files: `3 passed (3)`.
- Tests: `162 passed (162)`.
- Composer: 93 passed.
- Session: 45 passed.
- SessionChrome: 24 passed.
- Duration: 6.38s.

### TypeScript and diff hygiene

```bash
npm run typecheck
```

Result: exit 0; `tsc --noEmit --incremental false` produced no diagnostics.

From the repository root:

```bash
git diff --check
git diff --cached --check
```

Results: exit 0 with no output.

The staged-file audit listed exactly the five Task 2 implementation/test files from the brief before commit.

## Self-review

- Confirmed `SessionChrome` is mounted once, inside `PromptCard.leading`, after the attachment control.
- Confirmed the production DOM order follows attachment → model → effort → context meter → session actions.
- Confirmed Task 1's shrinkable `.body` query container and fixed `.right` sibling remain unchanged.
- Confirmed `.leading` owns available-space consumption and has `min-width: 0`; no status/menu styling was copied into composer CSS.
- Confirmed unengaged ended follow-up cards do not mount the leading group or inline chrome; focused/content-bearing ended cards still reveal the existing usable controls.
- Confirmed `Session.tsx` retains its footer wrapper, 76rem measured flow, liveness line, pending chips, and composer.
- Confirmed existing model, effort, menu action, attachment, routing, Stop/Send/Steer, and Session behavior tests remain in the focused passing set.
- Confirmed the implementation commit contains only the five required production/test files.

## Commits

- `1ccc76277` — `feat: move session controls into composer`

This report is committed separately so it can contain the final implementation commit hash.

## Concerns

- No Task 2 implementation concerns.
