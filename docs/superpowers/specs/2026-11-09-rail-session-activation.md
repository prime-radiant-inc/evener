# Session link activation

## Goal

A press on a session link places the session immediately. On the phone, the
same press dismisses the Sessions drawer without waiting for focus to change.

## Scope

- A top-level rail row places through the existing `openTopLevelSession` seam in
  the press's own commit, before the navigation-location read answers.
- Every other unopened session link (a nested rail row, a palette result, a
  notification, the phone Back path) opens its pane through `openPane`
  non-destructively. The route then reuses that pane beside the owner the
  location read names.
- The navigation-location read still runs after the press. It confirms ownership
  and reconciles placement; it no longer gates the first pane.
- The phone drawer closes from the row activation itself through the
  `navigationChrome` seam, including the row that is already focused.
- `replacePrimary` reuses an existing pane of the requested identity for the new
  primary instead of discarding and remounting it.

## Non-goals

- No location prefetch for every live row.
- No change to which panes a primary replacement keeps or clears.
- No new pane type, store, or second placement path.

## Recovery

If the location read fails, the session already placed by the press stays
visible. The existing terminal-location arm still reconciles the route.

## Test plan

- AppShell with a held location read: a top-level rail row is main in the press's
  own commit.
- AppShell with a held nested location read: a nested rail row's pane exists and
  is focused before the read answers, and keeps its pane id when the owner is
  confirmed.
- Rail rendered inside `MobilePanel`: pressing the already-focused row calls the
  drawer's close action.
- Workspace: `replacePrimary` reuses the matching pane id and still clears every
  other pane.
- Existing rail activation, route deferral, and mobile panel tests keep passing.
