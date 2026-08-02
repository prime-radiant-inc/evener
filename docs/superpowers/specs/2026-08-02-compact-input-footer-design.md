# Compact Input Footer Design

**Date:** 2026-08-02
**Status:** Approved

## Goal

Redesign the status area below the session composer as one tight line that never wraps or overflows its container. Keep the controls and live facts that help the user act now. Remove historical and accounting details from this surface.

## Scope

This change affects the session status row and the model switcher's compact presentation. It does not change protocol data, store actions, model selection behavior, reasoning-effort behavior, or the contents of the session actions menu.

## Content

The live row presents these items in order:

1. A visually unified model-and-effort cluster.
2. Context usage.
3. Elapsed time for the active turn, when space permits.
4. Queue depth when nonzero, with a compact form when needed.
5. The existing session actions menu, pinned to the far right.

Remove the failed-tool-call count and session cost from both live and ended rows.

Ended sessions use the same compact structure where the data remains meaningful. They do not render a separate settled-work or cost summary. The model remains interactive because a follow-up can resume the session. Reasoning effort remains available when the selected model supports it. Context remains visible when measured.

## Model and Effort Cluster

Model and reasoning effort should read as one unit, for example:

`gpt-5.6-sol · medium`

The cluster has one quiet visual treatment, tight internal spacing, and no duplicate boxes. Model and effort remain independent controls:

- Activating the model portion opens the existing model catalog.
- Activating the effort portion opens the existing native effort selector.
- Each control retains its own accessible name, focus target, keyboard behavior, disabled state, and error handling.

The implementation must not merge these settings into a new combined picker.

## Responsive Behavior

The row has a fixed single-line layout:

- `flex-wrap: nowrap`
- no horizontal scrolling
- no content extending beyond the row's bounds
- `min-width: 0` on shrinkable flex items
- the actions menu never shrinks or disappears

Use container queries tied to the available pane width rather than viewport queries. Apply these compression steps in order:

1. Hide elapsed time.
2. Change the queue label from `3 queued` to `Q3`; retain the full phrase as its accessible label and tooltip.
3. Replace the 64 px context meter with a percentage such as `42%`.
4. Ellipsize only the model label inside the model-and-effort cluster.

The effort value, context percentage, and actions menu remain visible at the narrowest supported pane width. The model label may shrink to the remaining space, but the cluster's model control must retain a usable hit target and expose the full model name through its accessible name and tooltip.

The row may use `overflow: hidden` as a final containment invariant, but deterministic compression rules must prevent ordinary clipping.

## Context Presentation

Wide panes retain the existing 64 px context meter. Narrow panes show an integer percentage derived from `contextPressure`.

Both forms expose the same semantic information:

- exact tokens used
- context-window size
- percentage used

The compact percentage receives the same accessible label and a tooltip with the exact counts. No context element renders when the context window is unknown or zero.

## Accessibility

The redesign preserves native and existing widget behavior:

- Model and effort remain separately focusable and operable by keyboard.
- Their visual grouping does not combine their accessible names.
- The model's full value remains available when its visible text is truncated.
- Compact queue text announces the full `N queued` phrase.
- Meter and percentage variants announce equivalent context information.
- Hidden responsive variants do not remain duplicated in the accessibility tree.
- Focus outlines remain visible within the row's clipped boundary.

## Implementation Surface

Primary files:

- `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/statusrow.module.css`
- `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.tsx`
- `cmd/serf-hub/frontend/src/panes/session/chrome/modelswitch.module.css`
- `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx`

Remove `FailureCount`, its glyph and cadence imports, and all status-row cost rendering. Keep the underlying `failedToolCalls` and `cost` data available elsewhere; this change removes only their footer presentation.

Prefer CSS container queries and paired presentation variants over JavaScript width measurement. Do not add resize listeners or store viewport state.

## Verification

Component tests must verify:

- failure count and cost never render in live or ended rows
- model and effort appear in one visual cluster while retaining separate controls
- the context meter and compact percentage carry equivalent accessible labels
- the compact queue form carries the full accessible phrase
- ended sessions retain applicable model, effort, and context controls without settled cost/work facts
- existing model and effort actions, errors, focus behavior, and keyboard access still work

Add or extend a layout-guard case that renders representative long values and checks a range of narrow pane widths. At every tested width:

- the status area has one line
- its scroll width does not exceed its client width
- the effort value, context representation, and actions menu remain visible
- compression occurs in the specified order
- the model label truncates rather than forcing overflow

Run the focused frontend tests and layout guard, then run the repository's standard web test target.

## Non-goals

- Changing model or reasoning-effort APIs
- Moving failure or cost data to a new surface
- Redesigning the model catalog or session actions menu
- Adding user-configurable footer fields
- Showing exact token counts directly in the compact row
