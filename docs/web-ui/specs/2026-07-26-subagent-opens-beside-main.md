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

## E. A deep link to a nested session corrects itself

`/s/<subagent-ref>` opens through `AppShell`'s route merge, which
resolves before any tree has been fetched — so nothing on that path can
tell the ref is nested, and it lands in main.

The rail owns the tree, so it corrects the placement as soon as the tree
arrives, using the same relocation a click performs (§B/§C). No click
needed, and a stale saved layout is repaired on load rather than on next
use.

The effect is keyed on both the tree and the main pane's ref, because
either can arrive second: the route may resolve before the first
`/api/tree` lands, or a pane may open while the tree is already loaded.
It settles on its own — after relocating, main holds the top-level
ancestor, for which `topLevelAncestorRef` returns the ref itself.

This turned up a bug in that lookup: it reported a CLUSTER row as a
member's ancestor. Cluster members are ordinary top-level sessions that
happen to share a title, and a cluster's ref is synthetic, so the effect
was "restoring" the very bogus pane §D removes. The lookup now descends
through cluster rows and treats their members as the top-level rows they
are.

## F. One canonical ref form

`hubapi.ParseRef` (hubapi/refs.go) has always required
`<hostID>:<sessionID>`; a bare session id is not a ref. The frontend
accepted one anyway, and that was a real bug rather than laxness: the
rail opens a session as `local:<id>` while a bare-id deep link opens it
as `<id>`, `sameParams` reads the two as different panes, and the same
session ends up open twice side by side. Observed in a browser.

- `urlToPane` now matches `/s/{ref}` and `/thread/{ref}` only for a
  qualified ref. A bare-id URL renders Not Found.
- `CommandPalette` drops its `item.result.ref || item.result.id`
  fallback. The hub's own `searchResult` doc comment
  (cmd/serf-hub/web_types.go) already states that "SPA clients open
  sessions only by qualified ref ... so the bare ID field alone cannot
  be used to open a hit", and `ref` ships without `omitempty` — the
  fallback contradicted the documented contract.

Old bare-id links (the htmx UI's canonical route form) are deliberately
not carried forward, at Jesse's direction: no back-compat.

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
