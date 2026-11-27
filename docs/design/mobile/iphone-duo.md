# iPhone Duo support

The paired hub supplies the workspace on both iPhone Duo displays. The cover
shows one pane at a time. On the inner display the Board stays beside the
conversation when both fit at the selected text size. Collapsing the Board gives
the reader the full width. The draft and conversation stay open across folds
and rotations.

The system navigation bar places conversation actions below the status area
on the cover and in inner landscape. Back or Sessions leads when needed,
followed by Find and Overview. An expanded Board owns its own toggle. Inner
portrait keeps the actions above the reader. Attach, Send, and the composer
menu stay with the text field in every pose. When the system bar is on the
side, the Sessions drawer uses that bar for Close, Hubs, and Settings. The
list keeps its filter and New session button. Closing it restores the reader
actions, or the Sessions button on Welcome. A menu or Overview temporarily
hides the covered actions.

## Home Screen and Safari

A hub added to the Home Screen rotates on both displays. The installed workspace
subtracts the horizontal safe-area insets before choosing its layout. When iOS
leaves one side wide enough for touch controls, Back or Sessions, Find, and
Overview move to that edge below the system status area. With no available edge,
the actions stay above the reader. The Sessions drawer places Settings and Close
in the same free edge, and Welcome uses it to reopen Sessions. The New session
button stays with the Board, aligned with Send in the adjacent composer.

Safari supplies its own browser controls and viewport. Conversation actions stay
in the web header in a browser tab.

## On-device Board layout

In **Work on this iPhone**, the split follows the window's point size. A window
at least 600 points on both sides is an inner display. The Duo outer display is
466×678 points, the inner display is 669×951 points, and a regular iPhone in
landscape stays below the height boundary. The boundary is `isDuoInner` in
`src/duo/duoLayout.ts`.

The app supports landscape. Both Duo displays rotate, the inner display keeps
the split when it is wide enough, and a regular iPhone in landscape keeps the
single-pane Board and conversation. The Board's rows, chips and compose button,
the conversation, the agent and job screens, the reader and Hub pages, and the
alert banner stay inside the left/right safe areas.

### Wide layout

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

### On-device navigation

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
- A Release build captures the outer display, the wide split with no
  conversation, and the wide split with a conversation. A draft written before
  folding remains after unfolding.
