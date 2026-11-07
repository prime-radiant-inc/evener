# iPhone Duo support

The Evener iPhone app runs on both iPhone Duo displays. The outer display keeps
the single-pane Board. The inner display shows the Board beside the open
conversation.

## Layout boundary

The split is chosen from the window's point size, not the device name: a window
at least 600 points on both sides is an inner display. The Duo outer display is
466×678 points, the inner display is 669×951 points, and a regular iPhone in
landscape stays below the height boundary. The boundary is `isDuoInner` in
`src/duo/duoLayout.ts`.

The app declares all orientations in `app.config.js`; a portrait-only app cannot
fill the Duo's wider outer display. The existing safe-area and window-dimension
layout handles the rest of the adaptation.

## Wide layout

On the inner display the `Sessions` screen renders:

- the Board on the left, with an inline hub and search header, and
- a detail navigator on the right, with its own native header.

The right pane opens a conversation on the first Board tap and replaces the open
conversation on a later tap, so conversations do not stack. The right pane shows
a placeholder until a conversation is chosen. The Board keeps its search, scroll
and live state while the conversation is open, since it remains the focused
`Sessions` screen.

The root stack header is hidden for `Sessions` on the inner display; the Board's
inline header replaces it. The detail navigator owns the conversation's title,
back button and session menu.

## Reach and non-goals

Conversations opened from the Board use the split. Conversations opened from a
root route (a share, an alert, a restored root location) still open full screen;
moving those entry points into the split is a later change.

The app does not render on both displays at once. It does not lay out around the
hinge's reserved region in a partially folded pose; the fold is treated as a
normal window resize. iPad remains paused.

## Recovery and preservation

The split is view state. Folding or unfolding changes the layout but sends,
discards or duplicates no draft, queued message or reader position. The
conversation stores and the Board's controller are the existing ones; the wide
layout adds no new persistence.

## Verification

- `isDuoInner` unit tests cover the Duo outer, inner portrait and inner
  landscape sizes and a regular iPhone in both orientations.
- A Board test proves an embedded Board reports an opened conversation to the
  split instead of pushing a root route.
- `make test-native` covers the bundle, app tests, type checks and Biome.
- A Release build on the iPhone Duo simulator captures the outer display, the
  inner display with no conversation, and the inner display with a conversation.
  The same build rotates on the inner display. A draft written before folding
  remains after unfolding.
