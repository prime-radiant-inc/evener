# Legacy Favorite Cleanup Policy — Design Spec

Date: 2026-07-26
Status: Draft for Jesse review
Scope: kata `r0yf`, informed by closed kata `ndr0`
Branch: `wip/kata-favorite-cleanup-policy`

## Decision

Do not automatically delete legacy favorite rows.

The favorite table stores only `(kind, id, favorited, decided_at)`. It does
not record the source snapshot, project identity, lineage evidence, whether a
row was visible when it was written, or whether a remote source was reachable.
Therefore, an ID missing from one navigation read is not evidence that the
user's decision is invalid. It may belong to an offline local session, an
unavailable remote, a capped-away row, transiently incomplete metadata, or a
session moving through a fork/orphan transition.

All stored decisions remain intact. At read/presentation time, the hub may
exclude a decision from the UI only when the current complete authoritative
navigation/lineage set positively contradicts it. A decision that cannot be
verified remains stored and is dormant for presentation; it can reappear
automatically when a later authoritative snapshot resolves it.

There is no migration, quarantine table, cleanup job, prefix-based sweep, or
background `DELETE`. New writes keep the `ndr0` validation boundary. Physical
favorite-row deletion is allowed only as part of an explicit, user-confirmed
session/project deletion, after canonical identity validation and successful
removal of the corresponding artifacts.

## Context and current boundaries

`kata r0yf` asks for a safe policy for rows left by the pre-`ndr0` endpoint.
`ndr0` now rejects session IDs that are synthetic cluster rows, nested
subagents, or nested fork rows before `FavoriteStore.Set`; it accepts real
top-level sessions, including orphan forks, and validates against the full
metadata/tree set so capped-away remote rows can still be written.

The current implementation establishes these boundaries:

- `cmd/serf-hub/internal/hubcore/favorite.go:13-136` owns the SQLite-backed
  `FavoriteStore`. The table is created in place with columns `kind`, `id`,
  `favorited`, and `decided_at`; `Favorites()` returns only `favorited=true`
  rows, while `Set(..., false, ...)` leaves a false decision row in storage.
- `cmd/serf-hub/web_api_favorite.go:14-96` handles POST `/api/favorite`.
  `topLevelFavoriteSessionID` uses the full navigation inputs and
  `hubcore.TopLevelSessionIDs`, rejects a `cluster:` request as part of the
  `ndr0` validation, and normalizes accepted local/ref aliases before writing.
- `cmd/serf-hub/internal/hubcore/tree.go:359-410` defines the shared lineage
  classification (`nestedSessionIDs` and `TopLevelSessionIDs`).
  `BuildTreeAtWithProjects` at `496-1036` builds from uncapped metadata and
  live entries, then `clusterRepeatedTitles` at `1117-1197` creates synthetic
  cluster nodes and the per-tier cap is applied at `851-856`.
- `cmd/serf-hub/web_api_tree.go:89-223` reads favorite decisions and projects
  the live, project, needs-you, and pinned tiers. The current pinned loop at
  `200-220` walks only the rendered `Current` and `Recent` slices, which are
  capped presentation slices; that cap must never become validity evidence.
- `cmd/serf-hub/web_api_tree.go:342-443` assembles local metadata, live
  entries, and remote threads. Remote list failures retain last-known-good
  data (`426-443`), so a cached read can render a tree but cannot prove that a
  missing remote row is gone now.
- `cmd/serf-hub/web_api_project_delete.go:32-205` is the existing explicit
  project deletion path. It validates the canonical project ID and working
  directory, holds session ownership while checking liveness, removes session
  artifacts, and scrubs session/project decision rows. Its decision-row calls
  must be ordered after every required artifact-removal result; in particular,
  a later API-log removal failure must leave the row for retry.
- `cmd/serf-hub/frontend/src/shell/rail/actions.ts:38-72`,
  `Rail.tsx:386-487`, and `Rail.tsx:673-694` show that favorite writes and
  project deletion are explicit UI actions, with project deletion confirmed in
  a dialog. `cmd/serf-hub/frontend/src/stores/tree.ts:257-424` retains the last
  successful tree on refresh failure.

## Goals

- Remove false UI effects from known legacy junk without destroying user data.
- Make validity depend on a complete, uncapped, lineage-aware snapshot rather
  than on absence from a rendered tree.
- Keep dormant decisions reversible without a second persistence structure.
- Preserve the `ndr0` write-time protection and existing canonical deletion
  safety checks.
- Keep default tests deterministic and entirely local, following
  `docs/testing.md`.

## Non-goals

- Changing the favorite table schema or adding a migration.
- Adding a quarantine, tombstone, provenance, or cleanup-status table.
- Automatically deleting, rewriting, compacting, or aging out any favorite
  row.
- Treating an ID prefix such as `cluster:` as a reserved namespace.
- Recovering missing session artifacts or proving the historical reason a row
  was written.
- Changing the `ndr0` accepted/rejected target policy.
- Adding a user-facing “legacy cleanup” screen or a dormant-favorites bucket.
- Deleting project working directories. This policy concerns decision rows and
  the existing session/project artifact-deletion contracts.

## Considered approaches

| Approach | Decision | Reason |
| --- | --- | --- |
| Delete rows absent from the current tree, or matching a stale/synthetic-looking ID | Rejected | Absence is compatible with offline locals, unavailable remotes, metadata lag, pagination caps, and fork/orphan transitions. `cluster:` is not safely reserved, so a prefix rule can collide with a legitimate ID. |
| Add a migration or quarantine table and move suspicious rows there | Rejected | It changes the schema and still requires the same uncertain classification. It adds a second source of truth without improving evidence or reversibility. |
| Revalidate at read/presentation time against a complete authority snapshot | Recommended | It is side-effect-free, keeps the existing schema, hides only positive contradictions, and lets a dormant decision reappear when its source and lineage become observable again. |

## Exact terms

### Decision row

A persisted row keyed by the exact `(kind, id)` pair. `favorited=true` is an
active favorite for presentation; `favorited=false` is an explicit negative
decision. Both are stored decisions and both are covered by the no-delete
invariant. `decided_at` is historical write metadata, not validity evidence,
and must not be changed by revalidation.

### Canonical identity

The identity used by the authoritative navigation model and by destructive
actions, not a display label:

- A session is the canonical session/ref ID produced by the tree input path.
  Local legacy aliases may be compared only when they resolve one-to-one to
  that ID. The comparison must not rewrite the stored row.
- A project is `identifier.Project.ID`, validated against its canonical
  working directory. A basename, display name, `row_id`, or presentation
  bucket is not a project identity.
- A synthetic cluster ID is an output identity for a particular current tree
  node, not a reserved string namespace. Its kind must be learned from the
  current tree construction, never from its spelling.

If two stored keys, aliases, source refs, or current nodes can resolve to the
same identity in more than one way, the result is ambiguous and therefore
unverifiable. The implementation must not guess which row wins and must not
delete either row.

### Complete authoritative navigation/lineage set

The full pre-presentation input and classification set for the relevant
identity namespace. It is not `TreeProject.Current`, `Recent`, `Archived`, a
`/api/tree` page, or any other capped/rendered slice.

A snapshot is complete for a decision only when all of the following hold:

1. Local `PastIndex.AllMetas()` was read successfully, with the live roster
   used only as a supplement for sessions not yet present in the past index.
2. Every remote source that can own the decision's ref was listed
   successfully for this snapshot. A last-known-good result after a failed
   list is useful for rendering but is not current proof that an absent row is
   gone.
3. Source-qualified IDs and parent/fork refs were canonicalized without
   malformed, duplicate, or conflicting records.
4. Lineage classification is complete and unambiguous, using the same
   `nestedSessionIDs` policy as tree construction. The set includes all
   subagents and fork relationships needed to decide whether a session is an
   independently addressable top-level row.
5. Project membership, where needed, uses the canonical identity map from
   `ResolveProjectMap`; UI caps, tier paging, and cluster folding have not been
   mistaken for source completeness.

Completeness is a read-time property. No completeness bit is persisted.

### Valid/offline

The exact canonical decision key resolves in a complete snapshot to a real,
independently addressable top-level session or canonical project. “Offline”
means that no current live entry is required: a persisted local session that
has ended or is not running is still valid when its metadata and lineage are
authoritative. A remote row observed in a successful complete list is valid
even when it is not in the live tier.

Being outside the first page of a capped tier does not make a row offline or
invalid. If the only evidence is a stale remote cache or a missing local
metadata record, the row is not valid for this snapshot; it is unverifiable.

### Confirmed invalid now

A decision whose exact canonical identity is positively observed in a complete,
unambiguous snapshot, but whose current authoritative classification says it
cannot be independently pinned. Examples are:

- a session ID observed as a nested subagent;
- a fork-superseded parent observed nested under its active continuation; or
- a current synthetic cluster node, when there is no simultaneous real session
  identity collision for the same stored key.

This is a presentation classification only. It is not permission to delete or
rewrite the row. A row is never confirmed invalid merely because its ID is
absent, its source is unavailable, its metadata is transiently incomplete, or
its string resembles a synthetic ID.

### Unverifiable / dormant

A stored decision for which the current snapshot cannot establish either a
valid addressable target or a positive contradiction. This includes absent
offline locals, unavailable or failed remote lists, last-known-good-only data,
capped-away rows when the uncapped authority was not available, transient
metadata, malformed/incomplete lineage, fork/orphan transitions, and any
identity collision.

Dormant is not a stored state. For `favorited=true`, it means the row is
omitted from favorite flags and the pinned projection for this read. The row
remains untouched and is eligible to appear again when a later complete
snapshot classifies the same exact identity as valid.

## Data flow and boundaries

### Write path

1. The rail calls `setFavorite` with `{kind, id, favorited}`.
2. `handleAPIFavorite` retains the existing input checks and, for session
   writes, calls the `ndr0` validation path before `FavoriteStore.Set`.
   Accepted aliases are normalized to the canonical target before the write;
   rejected nested/subagent/fork-with-active-child and synthetic cluster
   targets perform no store write.
3. `FavoriteStore.Set` remains the only normal decision-write boundary. It
   updates the existing row and `decided_at`, including when the user turns a
   favorite off. The cleanup policy adds no write side effect.

### Read and presentation path

1. Build one navigation snapshot for the request from local past metadata,
   local live entries, canonical project resolution, and remote source data.
   The tree and favorite authority must use the same snapshot; a separate
   favorite read must not be paired with a different remote/tree generation.
2. Derive the complete, uncapped session/project identity and lineage index
   before tier caps and cluster presentation. Record source completeness and
   any ambiguity in memory only.
3. Read stored favorite decisions. A store read error is an error, not an
   empty decision set.
4. Revalidate each active decision against the identity index. Pass only
   valid decisions to tree projection. Omit confirmed-invalid and dormant
   decisions from presentation, without calling `Set`, `Delete`, or changing
   `decided_at`.
5. Project `favorite=true` only onto nodes whose exact canonical target is
   valid. The pinned list must use the complete valid top-level, unarchived
   candidate set rather than treating the per-tier `More*` cap as invalidity;
   any ordinary project-row pagination remains a presentation concern.
6. On a later tree refresh, repeat the same in-memory classification. No
   revalidation result survives a process restart except through the original
   decision row.

### Deletion path

Physical row deletion is a consequence of an explicit destructive action, not
of revalidation:

1. The user confirms session/project deletion in the UI. Cancel performs no
   request.
2. The server validates the canonical session or project identity. For a
   project, the supplied ID must resolve to the supplied canonical working
   directory, as `handleAPIProjectDelete` already requires. Display names and
   stale aliases are insufficient.
3. The server checks liveness/ownership and removes every required artifact.
   A decision row is deleted only after the artifact removal for that exact
   identity succeeds. A late artifact failure must leave the row intact.
4. A project-level favorite row is deleted only when the confirmed project
   deletion has successfully removed the complete artifact set governed by
   that endpoint. If any session is skipped, the project decision and the
   skipped session decisions remain.
5. The response reports skipped/failed identities. Revalidation is not used
   to infer missing deletion targets and never broadens the delete set.

## No-delete invariant

The following invariant is mandatory:

> No read, tree build, source refresh, startup path, background task, schema
> initialization, ndr0 validation, or revalidation pass may physically delete
> or rewrite a favorite row.

The only allowed `FavoriteStore.Delete` calls are in an explicit,
user-confirmed session/project deletion path after canonical identity
validation and successful artifact removal. `Set(..., false, ...)` is a
user decision update, not cleanup and not row deletion.

This invariant applies to false decisions and to rows classified as confirmed
invalid, dormant, malformed, stale, synthetic-looking, duplicate, or
ambiguous. There is no quarantine destination. SQLite remains the sole source
of persisted favorite decisions, and `decided_at` remains unchanged unless a
user submits a new decision.

## Revalidation behavior and UI contract

- Confirmed-invalid rows disappear from `favorites` and do not receive a
  favorite star. They remain in SQLite. No cleanup notification, success toast,
  or “deleted” language is shown.
- Dormant rows have the same current presentation as an unrepresented target:
  no star and no pinned card. They are not reported as deleted or invalid.
  When the source returns, metadata becomes complete, or lineage resolves,
  the next tree refresh may show the star and pinned card again without a new
  user action.
- Valid/offline rows retain their favorite state even when ended, remote, or
  outside the first rendered tier page. They appear wherever the existing
  unarchived/pinned rules allow; archived rows remain out of the pinned tier.
- A cluster row never receives a favorite flag merely because a stored ID
  starts with `cluster:`. Only a current, collision-free node classification
  can make a legacy row confirmed invalid.
- The frontend adds no dormant-row UI. Its existing behavior of retaining the
  last successful tree on refresh failure is correct and should continue. A
  successful refresh is the point at which read-time revalidation is observed.
- A favorite-store read failure must not return a successful tree containing
  an empty favorite set, since that would visually turn favorites off. Return
  an error while leaving stored rows untouched; the frontend retains its last
  successful tree and surfaces its existing refresh error behavior. Authority
  incompleteness caused by offline sources is expected and does not itself
  fail the request; it only prevents invalidation in that namespace.

## Error handling and reversibility

- Failed local metadata reads, failed remote lists, unavailable sources, stale
  caches, malformed refs, duplicate IDs, and conflicting lineage all fail
  closed for classification: no row is invalidated from absence or ambiguity.
- A malformed legacy row is preserved and dormant unless a complete snapshot
  can positively resolve it to a valid canonical target. It is not repaired in
  place.
- A canonical identity mismatch on deletion returns a client error before
  artifact or decision mutation.
- A live/locked session or artifact-removal failure is reported as skipped;
  its decision row remains for a retriable explicit deletion.
- If all artifacts are removed but the subsequent SQLite row deletion fails,
  the row remains preserved and the operation reports the store failure. The
  physical deletion is not rolled back; the design does not pretend that
  deleted artifacts are recoverable. The retained row is harmless dormant
  state and may be cleared only by a later explicit deletion path that can
  validate the canonical target.
- Read-time revalidation is fully reversible: a later authoritative snapshot
  can restore presentation without changing SQLite or requiring a migration.

## Deterministic test plan

Tests must use fixed clocks, temporary filesystem roots, seeded metadata, and
scripted source responses. They must not use provider credentials or network
access; the boundary follows `docs/testing.md`.

### Pure authority/revalidation contracts

- A persisted ended/local top-level session is valid/offline and receives a
  favorite flag.
- A true row absent from a complete-looking presentation because it is beyond
  a 50-row tier cap is not classified invalid; its full-authority candidate is
  still eligible for the pinned projection.
- A failed remote list with last-known-good data leaves an absent legacy row
  dormant and intact; a later successful list containing the target makes it
  valid and visible again.
- Missing local metadata, transiently incomplete metadata, and an unavailable
  remote source produce dormant, not confirmed-invalid, results.
- A complete unambiguous subagent or fork-superseded parent row is omitted
  from presentation but remains in the underlying favorite store.
- An orphan fork is valid when it is independently top-level; a transition
  with incomplete/conflicting parent data is dormant until lineage is clear.
- A current synthetic cluster node can confirm only its exact legacy row when
  no real session identity collides with it. A legitimate session ID that
  resembles `cluster:` and any ambiguous collision are never rejected by a
  prefix rule.
- Canonical local/ref aliases resolve only when one-to-one. Conflicting alias
  matches preserve all rows and keep the presentation conservative.
- `favorited=false` rows remain in SQLite across every read/revalidation path
  and never enter the pinned projection.

### HTTP/tree contracts

- Extend `cmd/serf-hub/web_api_favorite_test.go` and
  `cmd/serf-hub/internal/hubcore/favorite_test.go` to assert that `ndr0`
  reject paths still perform no write and accepted capped-away/orphan targets
  still persist.
- Add tree endpoint cases in `cmd/serf-hub/web_api_tree_test.go` for dormant,
  confirmed-invalid, valid/offline, capped, and source-failure presentation.
  Assert structured fields (`favorite`, `favorites`, `more_*`, and node kind),
  not large rendered JSON strings.
- Add pure tree/lineage cases in
  `cmd/serf-hub/internal/hubcore/tree_test.go` or a focused sibling test file;
  use `BuildTreeAt` with a fixed clock and verify the shared top-level/lineage
  classification.
- Verify the favorite store row set before and after each read path, including
  rows that are confirmed invalid and rows with `favorited=false`.

### Explicit deletion contracts

- Extend `cmd/serf-hub/web_api_project_delete_test.go` so every artifact
  removal failure, including a late API-log failure, preserves the relevant
  session and project decision rows.
- Verify successful canonical deletion removes only the rows for artifacts
  actually removed; a partially skipped project retains its project favorite.
- Verify ID/working-directory mismatch and live/locked targets perform no
  decision deletion.
- Keep the existing frontend contracts in
  `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx`,
  `RailRow.test.tsx`, and `stores/tree.test.ts`: cancel sends no delete, a
  skipped session is retained, a deleted session is reconciled, and refresh
  failure retains the last successful tree.

## Short implementation plan outline

1. **Add a pure in-memory authority/revalidation seam.** Keep
   `cmd/serf-hub/internal/hubcore/favorite.go` as a persistence-only store.
   Add the smallest focused hubcore helper (or same-package section) that
   consumes complete raw navigation inputs, reuses `nestedSessionIDs`/
   `TopLevelSessionIDs`, records source completeness, resolves exact canonical
   identities, and returns presentation decisions without any store mutation.
   Do not alter the SQLite schema or write a quarantine table.
2. **Make tree reads use one snapshot.** Update
   `cmd/serf-hub/web_api_tree.go` so tree construction and favorite
   revalidation share the same navigation generation. Build the authority
   before clustering/capping, exclude only confirmed-invalid/dormant rows from
   the projection, and ensure the pinned candidate source is not the capped
   tier slices. Propagate favorite-store read errors instead of treating them
   as an empty map.
3. **Preserve the ndr0 write boundary.** Keep
   `cmd/serf-hub/web_api_favorite.go` and its existing validation contracts
   responsible for rejecting new non-addressable session targets before
   `FavoriteStore.Set`; add only the tests needed to prevent regression.
4. **Tighten explicit deletion ordering.** In
   `cmd/serf-hub/web_api_project_delete.go`, centralize or adjust the existing
   canonical deletion gate so every required artifact operation succeeds
   before the matching `FavoriteStore.Delete`. Preserve project rows on
   partial deletion and retain rows when the store delete itself fails. A
   future explicit session-delete endpoint must use this same gate.
5. **Verify the UI contract.** Keep the frontend free of a dormant cleanup
   surface. Update only the REST/tree tests and any minimal projection wiring
   required for valid capped favorites, then run deterministic focused Go and
   frontend tests described above plus `git diff --check`.

## Exact references reviewed

- `AGENTS.md`
- `docs/testing.md`
- kata records `r0yf` and `ndr0` via `kata show`
- `cmd/serf-hub/internal/hubcore/favorite.go`
- `cmd/serf-hub/internal/hubcore/favorite_test.go`
- `cmd/serf-hub/internal/hubcore/tree.go`
- `cmd/serf-hub/internal/hubcore/tree_test.go`
- `cmd/serf-hub/internal/hubcore/remotecache.go`
- `cmd/serf-hub/web_api_favorite.go`
- `cmd/serf-hub/web_api_favorite_test.go`
- `cmd/serf-hub/web_api_tree.go`
- `cmd/serf-hub/web_api_tree_test.go`
- `cmd/serf-hub/web_api_project_delete.go`
- `cmd/serf-hub/web_api_project_delete_test.go`
- `hubapi/types.go`
- `cmd/serf-hub/frontend/src/stores/tree.ts`
- `cmd/serf-hub/frontend/src/stores/tree.test.ts`
- `cmd/serf-hub/frontend/src/shell/rail/actions.ts`
- `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx`
- `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx`
- `cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx`
- `cmd/serf-hub/frontend/src/shell/rail/RailRow.test.tsx`

## Self-review before implementation

- Each implementation step is concrete; schema migrations, quarantine
  structures, and production/test changes are not part of this design commit.
- The distinction between absence, a positive contradiction, and a valid
  offline target is explicit; UI caps and stale caches cannot act as
  invalidity evidence.
- Cluster IDs, local/ref aliases, duplicate rows, and source/lineage
  collisions are handled conservatively without assuming reserved prefixes.
- Revalidation is side-effect-free and reversible; physical deletion is
  explicitly gated, destructive, and retriable only through a confirmed
  canonical deletion path.
- The scope is one implementation plan: persistence, hub read projection,
  deletion ordering, and their deterministic tests. No unrelated cleanup or
  feature work is included.
