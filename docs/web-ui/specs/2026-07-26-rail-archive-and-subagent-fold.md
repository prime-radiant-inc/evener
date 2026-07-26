# Rail: subagent fold, action scoping, archived-sessions section, persistence, optimistic UI, overflow

Date: 2026-07-26. Branch: `worktree-sidebar-archive-fold` (off
`worktree-webui-workspace-shell`).

Seven fixes to the React rail's sidebar.

§§1-4 are regressions against the htmx UI it replaced. Three are
already-written contract: see
`docs/web-ui/parity/parity-m3-sidebar-tree.md` §3, §5, §8. The fourth
(persistence) is §10.1, and Jesse asked for it explicitly during this
design.

§§5-7 were first written up as out-of-scope notes on this spec and then
folded in at Jesse's direction.

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

## 5. Pinning is scoped like archiving

Established against a live hub rather than assumed:

- `POST /api/favorite` with a **subagent** id returns `{"ok":true}` and
  writes the decision, but the node never comes back `favorite:true` and
  never enters the Pinned tier — the tier is built only from a project's
  top-level Current+Recent sessions (`web_api_tree.go`). A menu item
  that silently does nothing.
- With a **cluster** id it is worse: the id is a synthetic SHA-derived
  `cluster:<hex>` naming no session at all, so it writes a decision row
  nothing will ever clean up. The star renders; the Pinned tier stays
  empty.
- **Rename** needs no change. The server already withholds the `rename`
  flag from every nested and synthetic node, and `RailRow` gates the
  item on it. Renaming a subagent or a cluster id both 404.

So `sessionMenuItems` gates the pin item on the same `isTopLevelSession`
archive uses, and the favorite **star** is gated on it too: the wire can
still carry `favorite:true` on such a row from a decision written before
this change, and a star whose menu offers no way to remove it is a dead
end.

## 6. Optimistic feedback

New `railPending.ts`: a list of in-flight ops and one pure function
projecting them onto the fetched tree. Archive hides the row, pin flips
the star, rename swaps the title — each applied before the POST and
dropped once the follow-up refresh settles, so a rejected mutation
restores exactly what was on screen.

Deliberately simpler than parity §15's keyed pending Map, per-op
`confirm()` predicates, mutation-vs-disappearance completion rules and
30-second eviction backstop. All of that existed because the htmx UI
resynced on a coalesced 2-second debounce that could not be awaited, so
an op had no defined moment at which it was safe to drop. `Rail` awaits
`treeStore.refresh()` directly (it always resolves, never rejects), so
every op has exactly one such moment.

Only the archiving direction hides anything. Unarchiving has no
optimistic form — which tier the row reappears in is a server-side
classification this layer cannot predict — matching parity §8 exactly.

## 7. Per-tier overflow

`more_current` / `more_recent` / `more_archived` ship on every project
and nothing read them, so sessions past the server's 50-per-tier cap
vanished with no indication. Each list now ends with a quiet `+N older`
note — the affordance `hubcore`'s own doc comment describes.

Each list counts only the tiers it renders: a project's inline list sums
Current+Recent (Archived is diverted out of it), the archived sub-branch
reports Archived, and a hydrated archived project reports all three. The
note is a leaf with no chevron and no menu: the rows it counts were never
sent to the client, so there is nothing to expand and nothing to act on.

## Out of scope

- Pagination for the capped rows. The overflow note reports them; it
  does not fetch them, which would need a new server endpoint.
- Optimistic UI for project deletion (confirm → POST → refetch, as
  before).

## Testing

Red-green TDD throughout. Vitest:

- `railNodes`: the current/inactive split, fold id derivation and
  per-parent independence, the cluster exemption, the
  `tier === "archived"` filter, archived sub-branch construction, and
  per-list overflow counts.
- `RailRow`: menu contents and star visibility for each `kind`, the fold
  row's label and singular/plural, the overflow row.
- `railExpansion`: round-trip, corrupt-JSON fallback, non-boolean
  rejection, cap eviction, storage-throws on read and on write.
- `railPending`: each op, coverage across every tier, non-mutation of
  the source tree.
- `Rail`: section order (archived last), collapsed by default, header
  count, expand state surviving a remount, and optimistic feedback
  asserted inside a deliberately held POST.

Then the full gates — `tsc --noEmit`, `biome ci src`, vitest,
`go test ./...`, `make lint` — and a live browser against a hub on an
isolated port, config, `HOME` and state dir, seeded with a copy of real
session metadata (plus, for the overflow case, 58 generated sessions
whose ids come from the repo's own `identifier.EncodeUUID` — the
indexer requires a real base62 UUIDv7 payload and silently drops
anything else).
