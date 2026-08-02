# Compact Input Footer Design

**Date:** 2026-08-02
**Status:** Approved

## Goal

Redesign the complete session footer below the composer as one tight line that never wraps or overflows its container. Keep the controls and live facts that help the user act now. Remove historical and accounting details from this surface.

## Scope

This change applies to `SessionChrome`, which composes the cadence indicator, status facts, goal control, detail-panel triggers, and session actions. `StatusRow` remains the facts sub-row. `SessionActionsMenu` remains in `SessionChrome`'s trailing control group.

The change does not alter protocol data, store actions, model selection behavior, reasoning-effort behavior, or session-action menu contents. Preserve the existing 640 px `SessionChrome` behavior that moves Details, Tasks, and Jobs into the actions menu. The compact-footer rules apply to the space left after that trailing group.

## Content

The live footer presents these items in order:

1. Cadence on layouts that currently show it.
2. A visually unified model-and-effort cluster.
3. Context usage.
4. Live cumulative work time while a turn is active, when space permits.
5. Queue depth when nonzero, with a compact form when needed.
6. The existing goal chip when space permits.
7. The existing detail-panel and session-action controls, pinned to the far right.

Remove the failed-tool-call count and session cost from live and ended states.

Ended sessions use the same compact facts where the data remains meaningful. They do not render a separate settled-work or cost summary. The model remains interactive because a follow-up can resume the session. Reasoning effort remains available when the selected model supports it. Context remains visible when measured.

The goal chip has lower visual priority than model, effort, and context. Hide it below the full-row threshold; do not alter its popover, dialog, or menu wiring. The existing session-actions menu remains available at every supported width.

## Model and Effort Cluster

Model and reasoning effort read as one unit, for example:

`gpt-5.6-sol · medium`

The cluster has one quiet visual treatment, tight internal spacing, and no duplicate boxes. Model and effort remain independent controls:

- Activating the model portion opens the existing model catalog.
- Activating the effort portion opens the existing native effort selector.
- Each control retains its own accessible name, focus target, keyboard behavior, disabled state, and error handling.

Do not merge these settings into a combined picker.

## Responsive Behavior

The complete footer has a fixed single-line layout:

- `flex-wrap: nowrap`
- no horizontal scrolling
- no content extending beyond the footer's bounds
- `min-width: 0` on shrinkable flex items
- the trailing controls never shrink or disappear

Set `container-type: inline-size` on `SessionChrome`'s `.body`, whose content box represents the width available to cadence, status facts, and the goal chip after the trailing controls reserve their space. Query the status presentation against that container.

Support panes down to 320 CSS pixels wide. Use these `.body` container thresholds:

1. **560 px and wider:** show the full row, including the 64 px context meter, cumulative work time, full queue label, and goal chip.
2. **Below 560 px:** hide cumulative work time and the goal chip.
3. **Below 480 px:** change the queue label from `3 queued` to `Q3`; retain the full phrase as its accessible label and tooltip.
4. **Below 400 px:** replace the context meter with an integer percentage such as `42%`.

At every width, let the model portion consume the remaining flexible space and ellipsize as needed. Give the model button at least 72 CSS pixels of inline space before truncation. If the available `.body` width cannot satisfy that preference, the model may shrink further to preserve effort and context. When the selected model supports reasoning, its effort value remains visible at the narrowest supported pane width. The compact context value also remains visible. The actions menu remains visible in the trailing group.

The footer may clip overflow as a final containment invariant, but deterministic compression must prevent ordinary clipping. Any control inside a clipping boundary must use an inset focus ring or reserve enough internal space to show its full ring.

## Context Presentation

Wide panes retain the existing 64 px context meter. Narrow panes show an integer percentage derived from `contextPressure`.

Both forms expose the same semantic information:

- exact tokens used
- context-window size
- percentage used

The compact percentage receives the same accessible label and a tooltip with exact counts. No context element renders when the context window is unknown or zero.

## Accessibility

The redesign preserves native and existing widget behavior:

- Model and effort remain separately focusable and keyboard-operable.
- Their visual grouping does not combine their accessible names.
- The model's full value remains available through its accessible name and tooltip when visible text truncates.
- Compact queue text announces the full `N queued` phrase.
- Meter and percentage variants announce equivalent context information.
- Hidden responsive variants do not remain duplicated in the accessibility tree.
- Focus indicators remain fully visible.

## Implementation Surface

Primary files:

- `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/sessionchrome.module.css`
- `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/statusrow.module.css`
- `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/modelswitch.module.css`
- the owning model-switch tests
- goal-control styles or tests only if required to hide the chip at the defined threshold

Remove `FailureCount`, its glyph and cadence imports, and all status-row cost rendering. Keep the underlying `failedToolCalls` and `cost` data available elsewhere; this change removes only their footer presentation.

Use CSS container queries and paired presentation variants. Keep the existing `SessionChrome` width observer that collapses Details, Tasks, and Jobs; do not add another resize listener or store viewport state.

## Verification

Extend each owning component's tests rather than duplicating interaction coverage in `StatusRow` tests. Verify:

- failure count and cost never render in live or ended states
- model and effort appear in one visual cluster while retaining separate controls
- the context meter and compact percentage carry equivalent accessible labels
- the compact queue form carries the full accessible phrase
- ended sessions retain applicable model, effort, and context controls without settled cost or work facts
- existing model and effort actions, errors, focus behavior, and keyboard access still work
- `SessionChrome` preserves its trailing-control collapse and composition behavior

Add a layout-guard case that renders long model and goal values, measured context, cumulative work time, a nonzero queue, cadence, and all trailing controls. Test pane widths of 320, 360, 400, 479, 480, 559, 560, and 900 CSS pixels. At every width:

- the complete footer occupies one line
- its scroll width does not exceed its client width
- effort remains visible when supported
- context remains visible
- the actions menu remains visible
- the expected threshold-specific variants are visible
- the model label truncates rather than forcing overflow
- focus indicators are not clipped

Run:

1. Focused tests for `StatusRow`, `ModelSwitch`, and `SessionChrome`.
2. `cd cmd/serf-hub/frontend && npm run layoutguard`.
3. Repository-root `make test-web`.

## Non-goals

- Changing model or reasoning-effort APIs
- Moving failure or cost data to a new surface
- Redesigning the model catalog or session actions menu
- Replacing the existing detail-trigger collapse observer
- Adding user-configurable footer fields
- Showing exact token counts directly in the compact row
