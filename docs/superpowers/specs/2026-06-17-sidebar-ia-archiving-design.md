# Sidebar IA v2: session tiering + archiving — design

**Date:** 2026-06-17
**Status:** approved for planning (Jesse)
**Area:** `cmd/serf-hub` web hub sidebar (navigation tree)

## Problem

The shipped sidebar tiers **projects** by recency (Active / Recent / Older) and lists a
project's sessions as one flat list. It also collects throwaway `serf-e2e-*` projects into a
"Test runs" bucket via a **name-prefix string match**. Two problems:

1. The recency axis is on the wrong level. We want projects always visible (ordered by
   recency), and the **sessions inside a project** tiered by activity — because a single
   project accumulates many sessions over time.
2. There is **no archiving**. The hub is expected to reach 1,000–10,000+ sessions. Without a
   way to move stale work out of the default view, the sidebar becomes unusable, and the
   tree is rebuilt by loading *all* session metadata every render.

The `serf-e2e-*` prefix bucket is a fragile naming-convention hack (`strings.HasPrefix`,
no real "disposable" signal) and is being removed in favor of the normal model + auto-archive.

## Goals

- Projects listed flat, **newest-first by most-recent session start**.
- Within a project, sessions split into activity tiers: **Current / Recent / Archived**.
- **Archiving** for both sessions and projects: manual action + auto-age at 2 weeks of
  inactivity, **reversible** (unarchive). Archived items move out of the default view.
- Keep the cross-project **Needs you** triage tier at the top.
- Remove the `serf-e2e-*` prefix bucket; those projects flow through the normal model and
  auto-archive like anything else.

## Non-goals (explicitly out of scope)

- **Pinning** (sessions or projects). Deferred; project-level pinning preferred when we do it.
- **Full DB-side pagination / lazy per-project session loading.** Called out below as a
  fast-follow once the *un-archived* set itself gets large; not built in this pass.
- A real `disposable`/`origin=test` flag for E2E runs. Deferred (YAGNI) unless E2E noise
  proves to matter after the prefix bucket is gone.

## Structure

```
┌─ Needs you (N)            cross-project: every AWAITING session, flat, at top
├─ Projects                 flat list, newest-first by most-recent session START
│   └─ project  [⋯ archive]
│        ├─ Current         a session with activity in the last 24h
│        ├─ Recent          last activity 24h–2wk ago, not archived
│        └─ Archived (N)    collapsed disclosure: >2wk inactive (auto) OR manually archived
└─ Archived projects (N)    collapsed at bottom: manually archived, or no non-archived sessions
```

## Tier definitions

A session's tier is computed from its **last activity** (`UpdatedAt`, the last turn) and its
archive state:

- **Current** — last activity ≤ 24h and not archived.
- **Recent** — last activity > 24h and ≤ 2 weeks and not archived.
- **Archived** — archived (see archive model). Auto-archive threshold is 2 weeks of
  inactivity, so any non-manually-decided session with last activity > 2 weeks is Archived.

Project placement:

- A project appears in the main **Projects** list iff it has ≥1 non-archived session
  (Current or Recent) **and** is not manually archived. Projects are ordered **newest-first
  by their most-recent session start** (`max(CreatedAt)` across the project's sessions).
- Otherwise the project appears in the collapsed **Archived projects** group at the bottom
  (manually archived, or every session archived / no recent activity).

## Archive model

Archive is **hub UI state**, never written into a session's own state directory.

**Effective-archived** is computed, with explicit user decisions overriding the auto rule:

- `autoArchived(entity)   = lastActivity > 2 weeks`
- `effectiveArchived(e)   = userDecision[e] if present, else autoArchived(e)`
- **Archive** action: `userDecision[e] = archived`.
- **Unarchive** action: `userDecision[e] = active` (sticky — an unarchived ancient item stays
  visible until the user archives it again; we do **not** re-auto-archive it in this pass).

This keeps the two reversibility edges sane: a manually-archived recent session stays
archived, and a manually-unarchived stale session stays visible. Each decision stores a
`decided_at` so a future refinement could expire unarchive decisions; not used now.

The same model applies at session granularity (keyed by session ID) and project granularity
(keyed by project name).

## Persistence

A hub-side archive store, in the existing **`index.db`** (already present at
`~/.serf/index.db`, queryable, survives restarts):

```
archive(
  kind        TEXT,        -- "session" | "project"
  id          TEXT,        -- session ID or project name
  archived    INTEGER,     -- user decision: 1 archived, 0 active
  decided_at  INTEGER,     -- unix seconds
  PRIMARY KEY (kind, id)
)
```

- Only **explicit user decisions** are persisted. Auto-archive is computed at tree-build time
  from `lastActivity`, so it needs no rows and adapts as time passes.
- `BuildTree` takes the archive decisions as an input (read once per build, like metas), and
  computes `effectiveArchived` per session/project.

## API

Two hub endpoints (mirroring existing hub API conventions, auth-gated like the rest):

- `POST /api/archive`   body `{ "kind": "session"|"project", "id": "...", "archived": true|false }`
  → upsert the decision, return ok. (One endpoint covers archive and unarchive via the flag.)

The sidebar exposes an archive/unarchive affordance on project headers and session rows
(e.g. a `⋯`/hover control), calls the endpoint, and refreshes the tree (the sidebar already
has an appwire-driven refresh path).

## Scale strategy

This pass keeps the current "load all metas, build tree in memory" path and filters/segregates
archived items in `BuildTree`. That is correct and gets us a usable sidebar at the expected
scale because the **default view only renders Current/Recent**, with Archived behind collapsed
disclosures.

**Fast-follow (not this pass):** when the *un-archived* working set itself grows large, push
recency-windowing + archive filtering into the `index.db` query so only Current/Recent metas
are loaded by default, and load a project's Archived sessions lazily on expand. Flagged so the
in-memory approach is a conscious interim choice.

## Testing

- **Go (TDD):** tier classification at the 24h / 2-week boundaries; project ordering by
  most-recent start; project placement (main list vs Archived projects) including the
  "all sessions archived" case; archive-store upsert/read; `effectiveArchived` with
  user-decision override (both directions) vs auto rule; removal of the `serf-e2e-*` bucket
  (e2e projects now classify by normal recency). Build from synthetic `SessionMeta` slices
  with controlled timestamps (inject `now`, as `tierFor`/`DateGroupsAt` already do).
- **Web (jstest):** sidebar renders Needs-you → Projects(Current/Recent/Archived) → Archived
  projects; Archived disclosures are collapsed by default and expand; archive/unarchive
  control calls the endpoint and the row moves tiers on refresh.
- **API:** handler test for `POST /api/archive` (upsert + auth).

## Migration / compatibility

This replaces the project-tier machinery (`TierActive/Recent/Older/Test`, `tierFor`,
`TierGroups`, the `serf-e2e-` constant and `isTestProject`). The `Needs you` tier and the
`clusterRepeatedTitles` / subagent-nesting logic are retained. No on-disk session format
changes; the only new persisted state is the `archive` table in `index.db`.
