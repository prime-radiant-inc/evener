# Web UI View Modes Design

**Date:** 2026-08-04
**Status:** Approved

## Goal

Add two focused view modes to the Serf web UI while preserving the existing display as the default full view. A compact selector at the top of the viewport lets users switch among:

- **Everything:** the current UI, unchanged, including tool calls and results.
- **Conversation:** user and agent messages only. Each group of hidden tool calls appears as centered italic text, such as `3 tool calls`.
- **Intent:** user and agent messages plus the rationale associated with each tool call. The raw tool call, arguments, and results remain hidden.

## Selector

Place a compact three-option segmented radio selector in the viewport header. Use the labels `Everything`, `Conversation`, and `Intent`, in that order. Mark the active mode with the existing selected-control styling. Keep the selector visible while scrolling if the current viewport header is persistent; otherwise place it in the existing header without changing header behavior.

The selector must be keyboard accessible and expose its selected state through the existing radio-control accessibility pattern. Changing the selection must update the rendered message list without reloading the session.

## Rendering modes

### Everything

Reuse the current message and tool rendering path without visual or behavioral changes. This mode is the compatibility baseline and should remain the initial mode whenever a session opens.

### Conversation

Render user and agent messages in their existing order and styles. Omit tool call cards, arguments, intermediate output, and tool results. For each contiguous group of omitted tool calls, render one centered divider with italic text using the group count. Use singular/plural text correctly, such as `1 tool call` and `3 tool calls`.

The divider is informational only and is not interactive.

### Intent

Render user and agent messages in their existing order. Replace each hidden tool call with its associated rationale, preserving chronological order relative to surrounding messages. Do not render the tool name, arguments, result, or other raw tool-call content.

Use the existing rationale/thinking visual language if one exists. Otherwise use subdued, readable text that is visually subordinate to user and agent messages. Do not imply that the rationale is a complete or authoritative explanation beyond the data available in the session.

If a tool call has no rationale, omit the rationale block rather than inventing text. If a rationale exists without a corresponding visible message, preserve its position in the session sequence.

## Scroll behavior

Treat the top visible content as the stable anchor when switching modes. Before changing mode, capture the content identity and its offset from the viewport top. After rendering the new mode, restore that same content identity to the same top-of-viewport position when it exists in the new representation. Do not reset the reader to the session end or to the beginning during a mode switch.

When opening a session, always scroll to the end of the session after the initial content has rendered. This must happen even when the session contains interstitial events or the persisted session state points to an earlier position. Do not open a session at an interstitial point.

If the exact anchor is hidden in the selected mode, use the nearest surviving surrounding user or agent message as the anchor. If no anchor survives, preserve the normalized scroll proportion as a fallback.

## Data flow and boundaries

Keep filtering and grouping separate from presentation:

1. The session event/transcript data remains the source of truth.
2. A view-model transformation derives visible entries for the selected mode.
3. The existing message components render user and agent entries.
4. A mode-specific renderer renders either tool-count dividers or rationale entries.
5. The viewport controller owns anchor capture and restoration, plus the initial scroll-to-end behavior.

Do not mutate transcript data when changing modes. Switching modes should be local UI state and should not trigger network writes or session changes.

## Error handling

Unknown or missing mode values fall back to `Everything`. Missing tool metadata must not prevent user or agent messages from rendering. A missing rationale produces no Intent entry. Empty tool groups produce no Conversation divider.

If anchor restoration cannot find a matching entry after a mode change, use the nearest available message or normalized scroll fallback without throwing or interrupting rendering.

## Testing

Add deterministic tests covering:

- The selector exposes exactly the three approved labels and selects `Everything` initially.
- Everything renders the existing tool UI.
- Conversation hides tool details and renders correctly pluralized tool-count dividers.
- Intent hides raw tool details and preserves rationale ordering.
- Missing rationale and missing tool metadata do not crash rendering.
- Switching modes preserves the anchored content and viewport offset when possible.
- Opening a session scrolls to the end rather than an interstitial point.
- Keyboard interaction and selected-state accessibility behavior follow the existing control pattern.

Use scripted or fixture session data. Do not introduce provider credentials, network access, timing dependence, or current model behavior into default tests.

## Scope exclusions

This change does not redesign the existing Everything mode, add new transcript data, expose tool details inside Conversation or Intent, or add user-configurable persistence of the selected mode across sessions.
