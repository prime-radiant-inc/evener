# Rail session activation

## Goal

A press on a session row in the sidebar places the session immediately. On the
phone, the same press dismisses the Sessions drawer without waiting for focus to
change.

## Scope

- Every session link that runs through `openSessionByRef` (the rail, palette,
  notifications and phone Back) opens an unopened session's pane through the
  existing `openPane` seam before the route is published. The route then
  settles the main slot through `openTopLevelSession` or reuses the pane beside
  a nested owner.
- The navigation-location read still runs after the press. It confirms ownership
  and reconciles a nested route; it no longer gates the first pane.
- The mobile drawer closes from the row activation itself. `MobilePanel` supplies
  its close function to the rail through one context; the rail calls it for a
  session row, including the row that is already focused.
- Nested placement still waits for the location read to name the owner; the
  immediate pane is reused beside it.

## Non-goals

- No location prefetch for every live row.
- No change to which panes a primary replacement keeps or clears; the immediate
  pane is reused when it is the requested identity.
- No new pane type, store, or second placement path.

## Recovery

If the location read fails, the session already placed by the press stays
visible. The existing terminal-location arm still reconciles the route.

## Test plan

- Rail rendered inside `MobilePanel` with a live row: pressing the row creates
  the session pane immediately, calls `onClose`, and dismisses the dialog.
- Pressing the row for the already-focused session still calls `onClose`.
- AppShell with a held location read: pressing a rail row shows the session pane
  before the read settles, then the route settles without replacing it.
- Existing rail activation, route deferral, and mobile panel tests keep passing.
