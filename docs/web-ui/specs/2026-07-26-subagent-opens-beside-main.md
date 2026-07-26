# Rail: a subagent opens beside the main pane, never as it

Date: 2026-07-26. Branch: `worktree-sidebar-archive-fold` (off
`worktree-webui-workspace-shell`).

## The bug

Placement is decided by *"is the main slot free?"* and nothing else
(`shell/workspace.ts`'s `openPane`: the first pane takes main, every
later one goes secondary). The store sees only a pane type and a
`{ref}`, so it cannot know a session is a subagent.

A subagent therefore takes the main slot whenever main happens to be
free. And `slot` is assigned once at open time, never re-derived, and
persisted in the saved layout — so once a subagent lands in main it
stays there across every reload, and its own parent opens to the *right*
of it. The workspace is permanently inverted.

Measured in a real browser against a hub with real session data:

| step | main (x=308) | secondary (x=866) |
| --- | --- | --- |
| fresh workspace | Welcome | — |
| click a subagent | **subagent** | — |
| then click its parent | subagent | **parent** |

Note what does NOT reproduce it: opening the parent first and then the
subagent already behaves correctly today (parent stays main, subagent
opens right, tabbed). The trigger is specifically a subagent reaching
main while main was free — after which the layout is stuck.

## A. A nested session can never take the main slot

`openPane` gains an optional slot preference in its `opts`, meaning
"place this in secondary, never main". Every other call site keeps the
existing default, so the "one placement rule in one place" property its
own doc comment describes survives — this adds one explicit opt-in
rather than moving policy out to callers.

Rail passes it using the same `isTopLevelSession` predicate that already
gates archive and pin (`RailRow.tsx`), so "nested" means one thing
across the whole rail: `subagent`, `fork`, and `cluster` are nested or
synthetic; everything else is a real top-level session.

## B. Clicking a nested session puts its top-level ancestor in main

`Rail.handleActivate`, for a nested node:

1. walk up to its top-level ancestor,
2. if the main slot is empty or holds welcome, open the ancestor there,
3. open the clicked node in secondary and focus it.

When main already holds something, it is left alone — only the subagent
opens. Opening the ancestor is scoped to the empty case precisely
because replacing whatever you were looking at would be worse than the
bug.

`railNodes` grows the ancestor lookup; the tree is already nested, so
this is a walk, not a new index.

This is what the htmx UI did (parity-m3-sidebar-tree.md §3: it opened
the whole ancestor chain from the first not-already-open ancestor down).

## C. Self-heal a layout with a nested session stuck in main

Fixing placement does not repair an existing hub: a subagent already
stamped `slot: "main"` in a saved layout stays there forever.

When an activated nested session's pane is currently in the main slot,
Rail closes and reopens it in secondary. One-time correction per stuck
pane, costing one transcript reload.

Chosen over bumping the layout storage key (`serf.workspace.layout.v2`),
which would fix it globally but discard every saved arrangement to
repair a state most layouts are not in.

## D. A cluster row toggles instead of opening a pane

Clicking a repeated-title cluster fold opens a pane for its synthetic
`cluster:<8 hex>` ref — an id that names no session (it is a SHA of
project + title, `hubcore/tree.go`) — which then sits in the tab strip
loading a transcript that will never arrive. Confirmed in a browser.

A cluster row is a disclosure, not a session. Activating one toggles it,
exactly as its own chevron does — the same thing a project row already
does in `handleActivate`.

## Out of scope

- Moving an already-open pane between slots as a general capability.
  `slot` stays assign-once; §C is a close-and-reopen, not a move.
- Restoring the htmx behavior of opening every intermediate ancestor for
  a deeply nested subagent. Only the top-level ancestor is opened; the
  chain between is reachable from the rail.

## Two gaps found while verifying, deliberately not fixed here

Both were found in a real browser after the change, and both are
reported rather than silently absorbed.

**A deep link straight to a subagent still lands it in main.**
`/s/<subagent-ref>` opens through `AppShell`'s route merge, not the
rail, so nothing on that path knows the ref is nested — the tree has not
been fetched when the route resolves. Fixing it properly means
re-placing a pane after the tree arrives, which is exactly the
"move an open pane between slots" capability listed as out of scope
above. It self-heals: the next rail click on that row applies §C.

**The same session can occupy two panes under two ref forms.**
`/s/{ref}` round-trips whatever string it is given. The rail opens the
fully-qualified wire ref (`local:<id>`), while a bare id (`<id>`) is the
htmx UI's canonical route form and is still what `notifications.js`
navigates to. `sameParams` compares the two as different panes, so
deep-linking a bare id and then clicking that session in the rail opens
it twice, side by side. Pre-existing and not specific to subagents — a
top-level session does the same — so it belongs with routing/ref
normalization rather than this change.

## Testing

Red-green TDD. Vitest:

- `workspace`: the slot preference places a pane in secondary even when
  main is free, and default placement is unchanged without it.
- `railNodes`: ancestor lookup for a direct child, a deeply nested
  child, and a top-level session (itself).
- `Rail`: activation with main empty, main holding the parent, main
  holding an unrelated session, a nested pane stuck in main, and a
  cluster row.

Then `tsc --noEmit`, `biome ci src`, vitest, `go test ./...`,
`make lint`, and a browser walk-through of those same states against an
isolated hub.
