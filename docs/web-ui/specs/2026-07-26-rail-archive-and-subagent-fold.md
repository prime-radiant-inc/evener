# Rail: inactive-subagent fold, archive scoping, archived-sessions section, persisted expand state

Date: 2026-07-26. Branch: `worktree-sidebar-archive-fold` (off
`worktree-webui-workspace-shell`).

Four sidebar regressions the React rail carries against the htmx UI it
replaced. Three of them are already-written contract: see
`docs/web-ui/parity/parity-m3-sidebar-tree.md` §3, §5, §8. The fourth
(persistence) is §10.1, and Jesse asked for it explicitly during this
design.

## 1. Inactive subagents fold away

Contract: parity §3.

Today `railNodes.ts`'s `toSessionNode` maps every child inline at every
depth. There is no fold anywhere in the frontend — `grep` for "Inactive
subagent" hits only `hubcore/tree.go`.

A subagent whose normalized state is `active`, `awaiting`, `idle`,
`warning`, or `notLoaded` is CURRENT and renders inline, as today. A
subagent in `ended`, `closed`, or `errored` is INACTIVE and renders only
inside its parent's own `Inactive subagents (N)` disclosure, collapsed by
default.

`errored` folds away with the rest. Jesse decided this explicitly:
terminal is terminal, matching the old UI, even though the new rail
otherwise treats `failed` as one of its three signal states.

No minimum count — one inactive subagent still folds. (Parity §3 calls
this out specifically: the `clusterMin=3` threshold belongs to
repeated-title clustering, an unrelated mechanism, and
`design-system.md`'s "past ~3" language describes neither.)

Each parent gets its own fold, keyed off that parent's `row_id`.
Expanding one never affects another, at any nesting depth.

## 2. Archive only on top-level sessions and projects

Contract: parity §5 (menu mechanics); the scoping itself is Jesse's
call, not old-UI parity — the htmx menu offered archive on every session
row.

`RailRow.tsx`'s `sessionMenuItems` appends the archive item
unconditionally. It should appear only when `session.kind === "session"`.

`kind` is already on the wire and is exactly the right signal:
`hubcore/tree.go`'s `nodeKind` returns `subagent` for subagents and
`fork` for snapshotted fork originals — precisely the two nested kinds —
plus `cluster` for synthetic repeated-title fold rows.

Favorite and Rename are unchanged on those rows. Only archive is scoped.

## 3. "Archived sessions" section at the bottom

Contract: parity §2.3 (`skipArchived`), §8.

Two separate problems:

- The wire ships archived-tier sessions inside an active project's
  `sessions` array (`web_api_tree.go`'s `projectSessions` concatenates
  Current + Recent + Archived), and `projectNodes` renders all of them
  inline. An active project's header must never list its archived
  sessions.
- The existing `ArchivedSection` (`Rail.tsx`) holds only whole archived
  *projects*, and sits above "Test runs".

Target: `projectNodes` filters out `tier === "archived"`. The section
moves below "Test runs" — last block in the rail — renders as
`Archived sessions (N)`, stays collapsed by default, and holds:

- whole archived projects, lazy-hydrating on expand exactly as today
- for each active or test-run project with at least one archived
  session, a sub-branch under that project's name revealing just those
  sessions (they already ride the main snapshot; no fetch)

One deliberate divergence from parity §8: the archived-session divert
applies to test-run projects too, not only active ones. The htmx code
special-cased test runs out of it. One filter for every project beats
two paths, and the inconsistency is not worth carrying into the rewrite.

## 4. Expand state persists, row by row

Contract: parity §10.1.

The rail's expand state is `useState` only (`Rail.tsx`'s
`expandedOverrides`), so every disclosure re-collapses on reload. Every
disclosure — project rows, subagent folds, the archived section and its
sub-branches — must remember its own state across reloads, keyed by row
id.

New module `railExpansion.ts`: load/save of the override map, pure
functions over `localStorage`, no React.

Storage is one JSON blob under `serf.rail.expanded.v1`, following the
`serf.workspace.layout.v2` precedent (`DockHost.tsx`) rather than the
htmx UI's one-key-per-row `serf-hub.sidebar.expanded.<key>`. Same
behavior row by row; one read at boot instead of a key scan, and no
orphaned keys scattered across the namespace.

Reads and writes are best-effort try/catch, matching `prefs.ts` — a
browser that blocks storage degrades to in-memory state and never
throws. Corrupt JSON reads as an empty map, never a crash.

The map holds only ids the user explicitly toggled, but that still grows
without bound on a long-lived hub, so it is capped at 2000 entries,
dropping oldest-inserted first (`Map` preserves insertion order).

This also removes the separate `archivedOpen` `useState`: the section
becomes one more key in the same map, so the rail has one expand
mechanism instead of two.

## Out of scope

- Favorite/rename scoping on nested rows (only archive was asked for).
  Note that the server draws the Pinned tier from top-level Current +
  Recent sessions only (parity §2.2), so favoriting a subagent likely
  does nothing visible — a latent bug, separate from this work.
- Optimistic UI for archive. `Rail.tsx` refetches on success; the htmx
  UI's `__drop` optimistic hide (parity §8) is not being restored here.
- The per-tier 50-row cap's missing "+N older" affordance (parity §1).

## Testing

Red-green TDD throughout. Vitest:

- `railNodes`: the current/inactive split, fold id derivation and
  per-parent independence, the `tier === "archived"` filter, archived
  sub-branch construction.
- `RailRow`: menu contents for each `kind` — archive present on
  `session`, absent on `subagent`/`fork`/`cluster`.
- `railExpansion`: round-trip, corrupt-JSON fallback, cap eviction,
  storage-throws-on-read, storage-throws-on-write.
- `Rail`: section order (archived last), collapsed by default, count in
  the header.

Then the full gates — `tsc --noEmit`, `biome ci src`, vitest,
`go test ./...`, `make lint` — and a live look in a real browser.
