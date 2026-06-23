# Thread Pane Punch List Planning Doc

**Date:** 2026-06-23

**Branch:** `subagent-side-view-chrome`

## Goal

Refine the side-pane thread/composer experience after the compact thread-pane pass: make the composer layout cleaner, remove redundant side-pane header chrome, avoid iframe hotkey conflicts, and fix the side-pane splitter drag bug.

## Scope

### In scope

1. Broader composer cleanup.
2. Side-pane-only header removal.
3. Side-pane-only hotkey conflict avoidance.
4. Side-pane splitter drag bug fix.
5. Focused automated tests plus manual smoke verification.

### Out of scope

- Reintroducing `?pane=1` or any query-param pane mode.
- Replacing the `/thread/<ref>` standalone thread document architecture.
- Moving task status below the input box.
- Changing unrelated sidebar project/session navigation behavior.

## Requirements

### 1. Composer layout

- Keep task status/count where it is today.
- Move the model display out of the input box.
- Put the model display in the row below the input box, near the composer controls.
- Apply this as part of a broader composer cleanup, not only as a side-pane-specific tweak.
- Preserve model-change behavior where `Capabilities.ChangeModel` allows it.

### 2. Composer button copy and hotkey labels

- Rename the composer button text `send as steer` to `steer`.
- In side-pane iframe mode, remove visible hotkey labels from `steer` and `send` buttons to avoid implying shortcuts that conflict with the main session.
- Main workspace may keep visible hotkey hints unless implementation shows this creates inconsistency or complexity.

### 3. Side-pane header chrome

- In actual side-pane iframe mode (`body.pane-compact`), hide/remove the bold inner workspace title row.
- Preserve the title row for normal full `/s/<id>` workspaces.
- Preserve direct `/thread/<ref>` usability unless it is clearly redundant with browser/page chrome.

### 4. Side-pane hotkeys

- Disable textarea `⌘↵` / `Ctrl+Enter` send handling inside side-pane iframes.
- Disable textarea `⇧↵` steer handling inside side-pane iframes.
- Keep button clicks functional in side panes.
- Preserve main workspace keyboard shortcuts.

### 5. Splitter drag bug

Observed bug:

- After widening the side pane, the main pane cannot be made wider again.
- Once a drag starts making the main pane narrower, merely contacting the drag handle can keep the splitter stuck in “drag smaller” behavior.

Working hypothesis:

- The issue is in the splitter drag lifecycle, not in pane minimum width logic.
- The drag implementation likely continues applying width updates outside an active mouse-button drag, or re-enters drag state when the pointer contacts the handle.

Required behavior:

- Resizing only occurs during an active drag that began with a pointer/mouse down on the splitter.
- Resize stops reliably on pointer/mouse up or cancellation.
- Dragging left widens side panes / narrows main content.
- Dragging right narrows side panes / widens main content.
- Pane-count minimums may still clamp side panes, but they must not prevent legitimate shrink behavior above the true minimum.

## Implementation approach

### Preferred approach

Use existing boundaries:

- `body.thread-document` for the reusable thread document surface.
- `body.pane-compact` for actual iframe side-pane-only behavior.
- Existing composer data attributes (`data-steer-trigger`, `data-model-trigger`, `data-capability-*`, `data-input-form`) must remain intact.

### Likely files

- `cmd/serf-hub/templates/partials/workspace.html`
  - Composer layout and button text/hotkey markup.
- `cmd/serf-hub/assets/style.css`
  - Composer row styling and side-pane-only title/header hiding.
- `cmd/serf-hub/assets/renderer.js`
  - Disable keyboard submit/steer shortcuts in pane iframes.
- `cmd/serf-hub/assets/panes.js`
  - Fix splitter drag lifecycle.
- `cmd/serf-hub/web_test.go`
  - Template/route assertions for composer markup and side-pane chrome.
- `cmd/serf-hub/jstest/`
  - JS regression tests for composer structure, shortcut behavior, and splitter drag behavior.

## Testing plan

### Automated tests

Add or update focused tests for:

1. Composer markup:
   - Model appears below the textarea in the composer control row.
   - Task status remains above the input box.
   - `send as steer` no longer appears; `steer` does.

2. Side-pane compact mode:
   - `body.pane-compact` hides the inner workspace title row.
   - Pane buttons do not show hotkey labels.

3. Keyboard shortcuts:
   - Main workspace textarea still handles `⌘↵` / `Ctrl+Enter` and `⇧↵`.
   - Pane iframe mode ignores those shortcuts.

4. Splitter drag:
   - Width updates happen during an active drag.
   - Width stops changing after mouseup/pointerup.
   - Subsequent hover/contact with splitter does not resize.
   - Dragging right can shrink side panes / widen main pane down to the real clamp.

### Verification commands

Run at minimum:

```bash
git diff --check
go test ./cmd/serf-hub -count=1
cd cmd/serf-hub/jstest && NODE_PATH=${NODE_PATH:-/tmp/serf-jstest-jsdom/node_modules} ./run-all.sh
```

### Manual smoke

Use a local `serf-hub` server and verify:

- Full `/s/<id>` workspace still has normal title/header behavior.
- Side-pane `/thread/<id>` iframe hides the redundant inner title row.
- Composer shows task status above input, input, then model/buttons row.
- `steer` label is short.
- Pane buttons do not display conflicting hotkey badges.
- Splitter can resize in both directions and stops resizing after mouseup.

## Acceptance criteria

- The punch-list UI behavior is implemented as described.
- Splitter drag bug has a regression test and root-cause fix.
- Main workspace behavior is preserved except for intentional broader composer cleanup.
- Side-pane-only behavior is scoped to `body.pane-compact`.
- Verification and manual smoke pass.
- Changes are committed and pushed to `origin/subagent-side-view-chrome`.
