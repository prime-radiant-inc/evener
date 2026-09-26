# Multi-host evener — Component 06: Fleet view and session targeting

Status: component spec, pre-implementation. Decomposes
`2026-09-14-multi-host-evener-design.md` §4.6 and the decisions in §2
("Fleet view", "Session targeting", "Offline hosts"). It assumes Components
01–05 have landed (stream transport, attach bridge, host config, SSH
connection manager, remote hub source).

Owner surface: `cmd/evener-hub` (Go) and `cmd/evener-hub/frontend/src`
(TypeScript). One PR-sized change, plus optional follow-up splits noted below.

**Implementation status.** Both halves are on `main`: the Go half (06a) landed
with `55952d1dd3` — the `sourceOnline`/`appsource.OnlineSource` signal and the
truthful `Online` flag it drives (`cmd/evener-hub/web.go:67-96`;
`cmd/evener-hub/internal/appsource/online.go`) — and the frontend half (06b)
landed with `bdb29346ad`.

## Purpose

Make the hub's single navigation surface present every configured host as a
**source**: enumerate them, render each host's sessions from a live fan-out
merge, show an unreachable host as offline with its last-known sessions
marked offline/stale, and let a new session choose an explicit host (local by
default).

## Scope

1. **Source enumeration** — surface the `hubapi.Source{ID,Label,Kind,Online}`
   descriptor for every configured host in the navigation manifest, with a
   truthful `Online` flag (today the flag is hardcoded `true`).
2. **Host identity on rows** — guarantee `NavigationSessionSummary.HostID`
   carries the host/source ID for every row, and that the frontend can label a
   row with its host.
3. **Live fan-out and merge** — confirm and extend the existing per-source
   thread-list fan-out so the merged list is correct across hosts and search
   terms; no replicated index.
4. **Offline / stale** — a host that cannot be reached reports `Online:false`
  ; its last-known sessions render with the offline/stale marker and
  host-targeted actions are refused until reconnect.
5. **Session targeting** — a host picker in the new-session form, defaulting to
   local, threaded through `thread/start` to a specific source.

## Non-scope

- Transport, attach/reconnect, SSH, binary deploy, version match (Components
  01–05). This spec consumes a connectivity signal; it does not implement one.
- Per-host settings/admin pages and credential push (Component 07).
- Remote *tool execution* (`agent/execenv`); this is about where a session
  runs, not where its tools run.
- Rail grouping by host / per-host collapsible sections. The minimum here is
  the picker plus a host label on rows; grouping is an explicit follow-up (see
  Open questions).
- Auto-discovery of hosts; the host list is the configured `[[hosts]]` set.

## Contract / interfaces

### Read contract (navigation manifest)

`hubapi.NavigationManifest.Sources` (`hubapi/navigation.go`) is the fleet
enumeration. Element shape `hubapi.Source` (`hubapi/types.go`):

```
type Source struct {
    ID     string `json:"id"`
    Label  string `json:"label"`
    Kind   string `json:"kind"`
    Online bool   `json:"online"`
}
```

Rules this component must satisfy:

- One entry per configured host plus the local entry, **at all times**. The
  enumeration source is the registered-source set: component 05 registers one
  `RemoteHubSource` per configured `[[hosts]]` entry at hub startup, whether or
  not the host is attached, so `apiTreeSources()` sees every host and the
  manifest never loses an offline one. Do not derive the list from "sources
  that are currently connected": an unattached host is present-but-offline, not
  absent (component 03, §"Source registration hook").
  Components 03/04/05/06 agree on this and the agreement is normative here:
  **registry membership never changes** — component 04's attach/detach events
  and `Manager.Attached` carry connection state only and are consumed as
  `Online()` (component 04, §"Client handoff"; component 05, §"Registration and
  default-source selection"). Nothing in this component may treat a detach as
  removal.
- `ID` is the ref source ID (the host `name` from `[[hosts]]`, per design §5);
  it must be a valid navigation identity (`navigation_schema.go`).
- `Kind` distinguishes local from remote; current literal values are `"local"`
  (`web_api_tree.go`) and `"appwire"` (`web_api_tree.go`). Add a
  host-remote value only if the frontend needs to distinguish "attached host"
  from any future non-host appwire source; otherwise reuse `"appwire"`.
- `Online` must be `true` only while the source's connection state says the
  host is attached and healthy (component 05's optional online interface reads
  it from the SSH connection manager); `false` for a configured-but-down host.
  `Online` is **attachment** state, not reachability: component 04's manager is
  lazy (component 04, §"Channel lifecycle states"), so a configured and
  reachable host that has had no traffic yet has no channel and reports
  `false` — indistinguishable in the manifest from an unreachable host. Nothing
  in this component probes or attaches a host on manifest read; a host first
  reports `true` only after a request has forced component 04 to attach it.
  A source that reports no state defaults to online. **Shipped:**
  `sourceOnline` (`cmd/evener-hub/web.go:86-96`) type-asserts
  `appsource.OnlineSource` (`cmd/evener-hub/internal/appsource/online.go`) and
  defaults to true when the source does not implement it; `apiTreeSources` uses
  it for remote entries (`web_api_tree.go:946`), and the live-row computation
  gates on `appThreadTreeLive` plus `sourceOnline` as well
  (`web_api_tree.go:365`), so a down host's rows are not presented as live.
- `Label` is bounded by `maxNavigationLabelRunes` (`navigation_schema.go`);
  host labels are short, so truncation is a safety net, not a design point.
- The manifest `sources` array is capped at **64 entries including `local`**
  (`navigation_projection.go`: `len(inputs.Sources) > 64` is an error), and each
  element must have exactly `{id,label,kind,online}` (`navigation_schema.go`).
  With one source per configured host plus the local entry, that is at most
  **63 remote hosts**. An over-limit `[[hosts]]` list is **not** rejected:
  component 03 records the decision that the 64-source cap is withdrawn, so a
  config over the limit is the operator's own configuration (component 03,
  §Scope) and the navigation failure it causes is the accepted v1 risk rather
  than a validation error.
  The frontend mirror enforces the same element shape
  (`stores/navigation/codec.ts`, `stores/navigation/store.ts`). Do not add fields
  without changing both validators.

### Row contract

`hubapi.NavigationSessionSummary.HostID` (`hubapi/navigation.go`) is the
existing per-row host identity and is required by schema validation
(`navigation_schema.go`). It is already populated from the row ref in
`navigation_projection.go` (`HostID: ref.HostID`). This component must
ensure remote rows arrive with a ref whose `HostID` equals the host source ID —
which the remote-source normalization already does when it backfills
`thread.Source`/`thread.Evener.Ref` (`web_api_tree.go`).

### Write contract (session targeting)

The `thread/start` request carries an explicit `Source` field —
`appwire.ThreadStartParams.Source` (`appwire/types.go:1677-1680`), a bare source
ID, not a ref, and present in the generated bindings
(`appwire-client/typescript/types.gen.ts`) — added by this component rather than
overloading `Harness`. `hubThreadStart` gives it **sole authority
when set**, consulting the legacy `launchSourceID` harness fallback only when it
is empty (`app_threadlifecycle.go`:
`sourceID := strings.TrimSpace(params.Source); if sourceID == "" { sourceID =
launchSourceID(params) }`).

**The harness must not name a configured host.** `launchSourceID` treats any
non-empty, non-`evener` harness string as a source ID, so a harness value that
happens to equal a configured host name (say a harness literally named `m4`)
would silently retarget the spawn to that remote host — whether it arrives on
the fallback path (`Source` empty) or is forwarded alongside a set `Source`.
The required contract is therefore: a harness value that names a configured host
source — or any other registered non-local source — is refused (`InvalidParams`)
rather than routed or forwarded. The `launchSourceID` fallback itself is
**retained**, not retired outright: it is consulted only when `Source` is empty,
and for a harness value that does **not** name a configured/registered non-local
source (e.g. `"claude"`, or `"evener"`→`local`) it resolves exactly as today.
Deleting the fallback is explicitly *not* the contract — it would make a spawn
with a non-empty harness like `"claude"` and an empty `Source` fall through to
the local spawner in silence (`app_threadlifecycle.go:53-61`) instead of
resolving its backend, which is the routing regression this section exists to
prevent. **Implementation status:** the shipped `hubThreadStart`
(`app_threadlifecycle.go:66-74`) resolves the fallback unconditionally, so the
host-naming refusal is a requirement, not a present fact.

**The host selector is controller-only; the harness is preserved.** The
`Source` field names a source in the controller's registry; the remote hub would
resolve the same string against its own registry (wrong target or `spawn source
is not available`). `RemoteHubSource.StartThread` therefore clears `Source`
before forwarding (component 05, §"Registration and default-source selection").
`Harness` is **not** a controller host selector to discard: it is the caller's
harness/backend selection, which the remote's own `launchSourceID` reads to
choose the backend, so it is forwarded verbatim (or explicitly translated);
clearing it — or overwriting it with `"evener"` — would silently start the
remote's default backend instead of the caller's choice. The safety that justified
clearing it is instead provided by the refusal above: a harness naming a
configured host source never reaches the remote. The chosen host is expressed by
which `RemoteHubSource` handled the start.

**The recipient re-checks the resolution for a remote-originated request.** The
refusal above runs against the *requesting* hub's own registry, so it cannot see
a host configured only on the receiving hub. A remote-originated `thread/start`
whose preserved `Harness` names one of the **recipient's** registered non-local
sources would therefore still resolve to that source in `launchSourceID` and be
routed onward, bypassing the component-05 loop guard. At the recipient,
`hubThreadStart` must therefore resolve only `local` for a request whose
routing-seam `origin` is non-empty: an effective non-local source (from a set
`Source` or the harness fallback) is refused (`InvalidParams`) and never routed.
The `launchSourceID` fallback above applies unchanged to local-originated
requests. See component 05, §"The receiving hub must reject a non-local
resolution for a remote-originated `thread/start`"; the code delta is a tracked
follow-up.

## Implementation approach

### What already exists (this component is small because of it)

The multi-source plumbing is already built and tested; production registers
only `local`, so the fan-out currently degenerates to one source.

- **Source registry**: `appsource.Registry` with `Add`/`Source`/`All`/
  `SourceForRef` (`cmd/evener-hub/internal/appsource/registry.go`).
  `Source` interface is `ID()` plus thread/turn methods only
  (`appsource/source.go`) — there is **no connectivity method today**.
- **Production registration**: `newHubSourceRegistry`
  (`cmd/evener-hub/app_rpc.go`) adds exactly one source, `"local"`
  (`app_rpc.go`). Component 05 registers one source per **configured** host
  here, at startup and independent of attachment, so the registry — and
  therefore this manifest — always carries the full `[[hosts]]` list.
- **Fan-out + merge**: `hubThreadListWithSourceTimeout`
  (`cmd/evener-hub/app_threadlist.go`) already runs every allowed source
  concurrently (4 workers, `app_threadlist.go`), sorts results back into
  source order, merges rows, folds local past-index entries, applies
  `SearchTerm`/`Statuses`/`SourceIDs` filtering post-merge
  (`app_threadlist.go`), and degrades a non-explicit source error to a
  skip — only a source named in `SourceIDs` turns an error into a response
  error (`app_threadlist.go`).
- **Background snapshot**: `refreshRemoteThreadSnapshot`
  (`web_api_tree.go`) walks the remote sources, backfills
  `thread.Source`/`thread.Evener.Ref`, records per-source completeness and
  conflict IDs, and stores into `hubcore.RemoteThreadCache`
  (`internal/hubcore/remotecache.go`). The refresher runs on a 30s
  ticker + poke (`main_background.go`), wired in
  `main.go`; cache changes invalidate navigation
  (`main.go`). Last-known-good per-source results are retained on transient
  errors (`web_api_tree.go`).
  **The walk must be non-dialing.** Component 05 resolves *every* source call
  through `sshManager.Ensure` (component 05, §"Method-coverage analysis"), so
  a walk over every configured source would open an SSH channel to each host
  within one 30s tick — eager attachment that contradicts component 04's lazy
  manager and the attachment-based `Online` above. The refresh therefore lists
  only sources that are **already attached**: it gates on the source's
  `Online()`/`Manager.Attached` state and skips an unattached host without
  calling into it. An unattached host contributes no fresh rows but keeps the
  last-known-good rows the cache already holds; a never-attached host has none,
  which is correct — nothing has asked for its sessions yet. The synchronous
  fallback walk (`remoteThreadFetch` when no cache is configured) obeys the same
  gate. The gate must not be a check followed by an `Ensure`-backed resolver: it
  must read through the **attached-only client lookup** (component 05,
  §"Method-coverage analysis"), so a host that disconnects between the check and
  the call is skipped rather than re-attached by a background read. That lookup
  is `sshconn.Manager.ClientIfAttached(name) (*appwire.Client, bool)`
  (component 04, §"Go surface"), installed on the source through
  `hubcore.WebConfig` (component 03, §"Implementation approach" item 4) as a
  `SetHostClientIfAttached` seam analogous to `SetHostOnline`/`SetHostFacts`
  (component 05, §"Registration and default-source selection").
  `refreshRemoteThreadSnapshot`
  (and its synchronous `remoteThreadFetch` fallback) resolves the client through
  that accessor, not the `Ensure`-backed `RemoteHubClientFunc`, and skips the
  host when it reports "not attached" without dialing.
  **Implementation status:** shipped. `refreshRemoteThreadSnapshot`
  (`web_api_tree.go`) skips a remote source whose attached-only lookup
  (`hubcore.WebConfig.RemoteHostClientIfAttached`, backed by
  `Manager.ClientIfAttached`) reports "not attached", without calling it, and
  carries the host's last-known-good rows forward. The source's own resolver is
  attached-only too, so the check and the request cannot disagree. The
  synchronous `remoteThreadFetch` fallback runs through the same function and
  obeys the same gate.
- **Tree ingestion**: `navigationSnapshotInputs` folds cached remote threads
  into the same `metas`/`live` inputs as local sessions
  (`web_api_tree.go`).
  **Project identity must be host-qualified.** The projector keys projects by
  working-directory path alone (`hubcore.ResolveProjectMap` →
  `projects[path]`, `hubcore/tree.go`) and resolves a path with
  `identifier.ResolveProject`, which uses `localResolver` against the
  **controller's** filesystem (`identifier/project.go`). Folding remote rows in
  unchanged therefore (a) collapses two hosts that share a path (say
  `/srv/app`) onto one project entry — `selectNavigationProjects`
  (`web_api_tree.go`) keeps the sort-first candidate and flags a spurious
  conflict, mixing the hosts' sessions — and (b) resolves a host path against
  the controller's disk, naming the wrong project or none, including for a host
  *past* session (a `metas`-only row, for which `resolveProjectMap` ignores any
  carried project and calls `ResolveProject` locally). Remote rows must
  therefore be grouped by **(source/host, project identity)**, using the
  identity the remote hub already reports on the row (`Thread.ProjectID` /
  `Thread.ProjectPath`, carried into `LiveEntry.Project` by
  `appThreadTreeEntries`), never by resolving the host path against the
  controller's filesystem. Every grouping/project key that can see a remote row
  carries the row's `ref.SourceID`, so identical paths on separate hosts stay
  separate projects. The group-id consumers (project favorites, archive keys)
  inherit the host qualification. This holds on **every** path that can see a
  remote row — thread listing, the background snapshot, tree ingestion, and
  metadata/archive/favorite handling — and none of them may pass a remote row
  through the controller-side `ResolveProject`.
  **Favorite and archive mutations must carry the owning host.** The host
  qualification above changes every project key, but the two mutation APIs are
  still host-unqualified, so a remote project action is rejected or hits the
  wrong project:

  - `evener/archive/set` (`MethodEvenerArchiveSet`, `ArchiveParams`) validates
    a project with `identifier.ResolveProject(params.WorkingDir)` on the
    **controller's** filesystem and requires an exact `project.ID` match
    (`app_archive.go`, `archiveSet`), then writes the controller's own
    `cfg.Archive` store. A remote project's `WorkingDir` does not exist on the
    controller (or resolves to the controller's own project at that path), so
    the call fails with `appwire.InvalidParams` ("project ID does not match
    workingDir") — or archives the controller's project, not the remote one.
    The **session** kind has the same defect: the archive action calls
    `setArchived("session", session.session_id, …)` (`Rail.tsx`, `actions.ts`)
    with a bare session ID and no source, and `archiveSet` writes
    `cfg.Archive.Set("session", params.ID, …)` keyed by that bare ID. A remote
    session's `session_id` is its bare thread ID, so a remote session archive is
    indistinguishable from a local one and collides with a local session of the
    same ID. `ArchiveParams.Source` must therefore be sent by the **session**
    action too — from the row ref's `SourceID` — and the session store key
    becomes `(source, id)`, exactly as for projects.
  - `evener/favorite/set` (`MethodEvenerFavoriteSet`, `FavoriteSetParams`)
    writes `cfg.Favorite` keyed by the bare `params.ID` with no host dimension
    (`app_favorite.go`), so two hosts' projects whose IDs collide share one
    favorite.

  **Requirement.** `ArchiveParams` and `FavoriteSetParams` gain an owning
  `Source` field (the row's `ref.SourceID` host, defaulting to `"local"`), and
  the frontend session/project archive actions send it from the ref; the
  controller keys both controller-side stores by `(source, id)`, so identical
  project IDs (or paths) on separate hosts are distinct entries — the same
  host qualification the navigation group key carries, so a favorite/archive
  flag projected onto a remote row is that row's own. For a **non-local**
  source, `archiveSet` must not resolve `WorkingDir` against the controller's
  filesystem at all: the project kind validates the ID against the identity the
  remote hub already reported for that row (`Thread.ProjectID` /
  `Thread.ProjectPath`, above) and the `WorkingDir` field is **optional**, used
  only as an optional cross-check against that reported identity (never against
  `identifier.ResolveProject`). The navigation receipt
  (`navigationChangeHint.Projects`) is keyed by `(source, id)` for the same
  reason, so a poke for one host's project does not refresh another's. Routing
  the mutation to the host instead would put the flag on the host hub's store,
  which the controller's folded navigation does not read; v1 keeps the stores
  controller-side and namespaces them, and does not pass a remote row through
  `ResolveProject`.
  **Implementation status:** shipped. Both param structs carry `Source`
  (`ArchiveParams`, `FavoriteSetParams`, `appwire/types.go`); both handlers
  normalize it, validate it against the configured hosts, and key the
  controller-side stores by `(source, kind, id)` (`app_archive.go`,
  `app_favorite.go`; `hubcore.NormalizeDecisionSource`).
  **The one source-qualified project identity must be the key on every
  navigation surface, not only inside the projection.** The grouping rule above
  is worthless if the identity is rebuilt or dropped one layer up: the browser
  asks for a project by a `projectKey` string, the navigation store caches by
  that string, and the invalidation poke names it. Two hosts whose projects
  share a project ID (or a path) then collide again at the first cache lookup
  even though the projection kept them apart — the browser is served, and
  invalidates, the wrong host's project. There must be **one** identity,
  formatted once and parsed once, carried end to end:

  - **Encoding (one pair of functions owns it).** The qualified project key is
    `"<sourceID>:<projectID>"` — the same `"<sourceID>:<id>"` form
    `appwire.Ref.String()` / `appwire.ParseRef` already define for session
    refs (`appwire/refs.go`), so a remote project on host `alpha` is
    `alpha:<projectID>` and a local one is `local:<projectID>`; both parts
    satisfy `appwire.ValidRefPart` and the project ID is a
    `identifier.Project.ID` (ASCII alphanumerics and `-`, `identifier/project.go`
    `projectID`), so the `:` separator is unambiguous. `"local"` is the
    canonical local-source token (the store migration above); an empty/absent
    source normalizes to it before keying and a bare (unqualified) key is
    accepted only as the local form. No layer may re-encode the pair
    differently (a `\x00` join, base64 of a struct, a path-derived id) and no
    layer may rebuild the key from the working directory.
  - **Wire types.** `NavigationProjectSummary.Key` and
    `NavigationProjectPage.Key` (the catalog and project rows,
    `hubapi/navigation.go`), `appwire.NavigationReadParams.ProjectKey` (the
    `project` / `project_page` reads), `hubapi.NavigationSessionLocation.ProjectKey`
    (the deep-link owner the session chrome renders,
    `panes/session/chrome/SessionChrome.tsx`), and
    `appwire.NavigationInvalidationTarget.ProjectKey` all carry that one string.
  - **Reads and server resource keys.** `navigationReadKeyWithFields`
    (`app_navigation.go`) parses the qualified key — refusing a malformed key or
    an unknown/non-configured source typed, before any lookup — into a
    `navigationResourceKey{Kind: navigationResourceProject…, ProjectKey}` whose
    `ProjectKey` stays the qualified string through `canonical()`,
    `navigationViewScope`, `navigationEntityKey`, and
    `navigationRootContainerKey` (`navigation_cache.go`). The projection
    lookups (`p.Project(key.ProjectKey)`, `p.ProjectPage`, both refusing an
    unknown key typed, `navigation_projection.go`) resolve exactly that host's
    project, and a qualified key never falls back to a controller-side
    `identifier.ResolveProject`.
  - **Invalidation targets and receipts.** The invalidation target identity
    (`navigation_service.go`'s target-key join) and the emitted
    `appwire.NavigationInvalidationTarget{Kind: appwire.NavigationTargetProject, ProjectKey}`
    carry the qualified key, and `navigationChangeHint.Projects`
    stays keyed by `(source, id)` for the same reason. A poke for one host's
    project must reach the *same* `ResourceKey` the browser registered
    (`targetBase`) and must not stale or refresh another host's entry.
  - **Frontend caches, routes, and selectors.** `ResourceKey`'s
    `{kind: "project"; projectKey}` and
    `{kind: "project_page"; projectKey; tier; …}`
    (`stores/navigation/types.ts`) hold the qualified string, and so do
    `keyID`, `navigationViewScope`, `navigationEntityKey`,
    `navigationRootContainerKey`, `targetBase`, the
    `selectProjectResource`/`selectProjectPage` selectors —
    which key `keyID({kind: "project"|"project_page", projectKey})` and must
    therefore see the qualified string (`stores/navigation/selectors.ts`) — and
    the deep-link/route parameter the
    rail, project browser, and session chrome pass. `canonicalResourceKey`
    (which today normalizes only a bare `location` ref to `local:`) must
    normalize a bare project key to its `local:` form the same way, or a
    pre-qualification key misses the qualified cache entry. The mobile client
    follows the same string in its `projectKey` route parameter, its reveal /
    readback target validation (`navigationPages.ts`,
    `navigationActionRepository.ts`, `navigationReveal.ts`) and its cache keys.

  **Requirement.** One source-qualified project identity, in the
  `"<sourceID>:<projectID>"` encoding above with `local` canonical, is the
  `projectKey`/`Key`/`ProjectKey` value on every surface listed — the project
  catalog rows, the `project`/`project_page` read parameters, the server
  resource key and its view/entity/container scopes, the session location's
  owning project, the invalidation target and the `(source, id)` receipt key,
  and the frontend `ResourceKey`, cache scope, selector, and route parameter.
  **A two-host same-key behavior test is mandatory:** two sources (one of them
  `local`) whose projects share a project ID and a working-directory path must
  produce two distinct catalog rows with two distinct qualified keys, resolve
  two distinct `project`/`project_page` reads (asking for one host's key never
  returns the other's sessions), stay in two distinct cache/view scopes and
  entity keys, take two distinct invalidation targets so a poke for one host
  leaves the other's cached entry settled, and a bare key still resolves to the
  local project. The reverse direction is asserted too: a qualified read for an
  unconfigured source is refused typed rather than degrading to local.
  **Implementation status:** none of this exists today. Project keys are bare
  `identifier.Project.ID` path-derived strings everywhere on the wire
  (`NavigationProjectSummary.Key`, `NavigationReadParams.ProjectKey`), on the
  server (`navigationResourceKey`, `navigationViewScope`,
  `navigation_projection.go`), and in the frontend (`ResourceKey`, `keyID`,
  `navigationViewScope`, `selectProjectResource`); `canonicalResourceKey`
  normalizes only refs. The qualification, the shared format/parse pair, and
  the two-host test are the implementing PR's requirement.
  **Migration of existing decisions — the uniqueness keys must be rebuilt, not
  just widened.** The controller-side favorite and archive stores are keyed by
  `(kind, id)` today, with no source column: `favorite` declares
  `PRIMARY KEY (kind, id)` and writes with `ON CONFLICT(kind, id)` and deletes
  `WHERE kind = ? AND id = ?` (`cmd/evener-hub/internal/hubcore/favorite.go:47,79-80,98`),
  and `archive` declares `PRIMARY KEY (kind, id)` and writes with
  `ON CONFLICT(kind, id)` (`archive.go:64-69,103`). Adding and backfilling a
  `source` column alone therefore **does not fix the collision**: the conflict
  target and the primary key stay `(kind, id)`, so a remote row with the same
  bare ID as a local one still matches the same key and overwrites it — the
  backfilled `source` is the only differing column and the key does not see it.
  On upgrade the migration must **rebuild each table with a composite primary
  key** `(source, kind, id)` — SQLite cannot add a column to, or otherwise
  alter, an existing primary key, so the migration creates the new table, copies
  every legacy row with `source = "local"`, drops the old table, and renames
  (`archive.go`'s `open`/schema setup is the seam) — and **every statement that
  names the key moves with it**:
    - the upsert conflict target (`ON CONFLICT(source, kind, id) DO UPDATE …`)
      in `ArchiveStore.Set` and `favoriteSet`;
    - lookups and deletes — `favorite`'s `DELETE … WHERE kind = ? AND id = ?`
      and the `archive` delete — become `(source, kind, id)`;
    - the readback: `SELECT kind, id, favorited` and the archive `Decisions`
      scan must select `source` too and key their maps by `(source, kind, id)`,
      so the tree and the navigation projection receive the owning source with
      the decision (a bare-keyed map would re-collide on read even with a
      composite table).
  **The migration is one shared, versioned transaction — not three independent
  rebuilds.** `favorite`, `archive`, and `session_pin` all live in the single
  `index.db` (`favorite.go`'s "It shares the same DB file"; each store runs its
  own `CREATE TABLE IF NOT EXISTS` in `open`), so create/copy/drop/rename per
  table across three uncoordinated `open` paths is not safe: a concurrent store
  initialization — a second opener, a concurrent process, or a crash mid-rebuild
  — can observe a missing or partially rebuilt table, and "a half-applied
  migration completes on the next start" is **not** guaranteed by per-table DDL.
  The migration must therefore be **centralized and applied once, before any
  store serves**: a single schema-versioned step (a `PRAGMA user_version` /
  schema-version record) takes `BEGIN IMMEDIATE`, rebuilds **all three** affected
  tables (and any other table whose primary key changes) inside that one
  transaction, advances the version only on commit, and commits before the
  stores are exposed. Concurrent initializers serialize on the write lock
  (`sqliteDSN` already sets `busy_timeout=5000` and WAL; `pin_section.go`'s
  `openWithImmediateTransaction` / `_txlock=immediate` is the existing seam), so
  a second opener either sees the committed new schema or waits — it never
  rebuilds ahead of the first or reads a half-rebuilt set. A crash before commit
  rolls the whole rebuild back and the next start re-runs it; a re-run after a
  committed migration is a no-op. The migration preserves the decision and its
  kind/id, so no existing local favorite or archive is lost and a pre-migration
  row keeps resolving to the local host. New rows are written with their owning
  source. **Implementation status:** `favorite` and `archive` are rebuilt to the
  composite key by `ensureDecisionSourceColumn` (`hubcore/archive.go:226-296`),
  per table inside each store's `open`; the single shared, versioned step above,
  and the `session_pin` rebuild below, remain the implementing PR's
  requirements.
  **The local source is canonicalized to `"local"` everywhere.** The migration
  writes legacy rows' source as `"local"`, so every read must agree on that
  token: an absent, empty, or bare (unqualified) source resolves to `"local"`,
  and no consumer may key an empty source and `"local"` as different. The
  controller-side consumers key by the bare id / `(kind, id)` today with no
  source at all, so without a canonicalization rule the backfilled `"local"`
  matches no lookup and local archive/favorite/pin state appears lost after
  upgrade. Requirement: define one canonical local-source constant (`"local"`)
  and use it in storage **and** at every lookup/projection boundary — the
  handlers, the store reads/deletes, the tree/projection maps, and the
  `SessionRef` resolution — normalizing an empty/absent source to `"local"`
  before keying, so a pre-migration bare-keyed read still resolves. A behavioral
  migration test must cover it: seed a bare `(kind, id)` / `session_id` row,
  migrate, and assert the local row is still readable and updatable under the
  canonical `"local"` key (not dropped, not shadowed by a remote row).
  **Session pin assignments must be source-qualified too.** Archive and favorite
  host qualification is not the only controller-side identity keyed by a bare
  session ID: pin sections and session-pin assignments are as well, with the same
  collision. The pin store keys its `session_pin` table by `session_id` alone and
  `Assign`/`CreateOrReuseAndAssign`/`Unpin`/`DeleteSession` take a bare
  `sessionID` (`cmd/evener-hub/internal/hubcore/pin_section.go`); the pin RPC
  params carry a `SessionRef` that is re-resolved to a bare tree-node ID and
  matched against **local** IDs (`SessionPinAssignParams`/`SessionPinUnpinParams`,
  `appwire/types.go`; `resolvePinSession`/`sessionRefMatchesID`/
  `resolveTopLevelSessionRef`, `app_pin_section.go`); and the navigation
  projection looks a session pin up by both the bare node id and the bare `ref`
  string (`PinSectionBySession`/`PinAssignments`, `navigation_projection.go`). A
  remote session's `session_id` is its bare thread ID, so a pin on `host:th_1`
  and a local `th_1` are indistinguishable: identical IDs on different hosts
  overwrite one another's assignment, and a remote pin is classified against the
  local tree. **Requirement:** pin storage, the RPC responses, and the
  navigation keys are keyed by `(source, id)` — the same `ref.SourceID` host
  qualification the archive/favorite stores carry — so two hosts' sessions with
  the same bare ID hold separate pins; the assign/unpin handlers resolve the
  requested `SessionRef` through the owning source (a `host:<id>` ref names that
  host's session, a bare/`local:` ref the local host) and reject an unknown
  source typed, and the projection matches a row's pin by its source-qualified
  key. `PinSection.ID`/name identity itself is controller-global and unchanged.
  Existing `session_pin` rows are migrated the same way, and here too the
  **rebuild is the substance**: `session_pin` declares
  `PRIMARY KEY (session_id)` and assigns with `ON CONFLICT(session_id)`
  (deletes `WHERE session_id = ?`, read back with
  `SELECT session_id, section_id, assigned_at`; `pin_section.go:101-102,531,577,665-667`),
  so a bare `source` column leaves the key, the conflict target, the delete, and
  the readback all bare — two hosts' identical session IDs still overwrite one
  another's assignment. The migration must rebuild `session_pin` with
  `PRIMARY KEY (source, session_id)`, copy the legacy rows as
  `source = "local"`, and update the assign/unpin upsert, the delete, the
  per-section count join (`COUNT(p.session_id)`), and the projection readback
  (`PinSectionBySession`/`PinAssignments`) to the `(source, session_id)` key, so
  no present local pin is lost and a pin on `host:th_1` and a local `th_1` stay
  distinct. **Implementation status:** the `session_pin` table, the pin
  handlers, and the projection maps are all bare-ID keyed today; the composite
  key, the table rebuild, and the source-aware resolver are the implementing
  PR's requirement.
  **Project summaries must carry the owning source, and destructive
  local-project actions must be gated by it.** The resolved model is still
  `identifier.Project` = `{ID, CanonicalPath}` (`identifier/project.go`) and
  carries no host dimension of its own, so the qualification rides beside it:
  the projected summary's `Sources` list reaches the frontend `RailProject`
  (`shell/rail/railNodes.ts`), and every consumer that acts on a project must
  use it. Remote rows are folded into the same project list, and the project
  context menu (`projectMenuItems`, `RailRow.tsx`) still renders the destructive
  items for every project, with the delete item wired to a **controller-local**
  call: `onDeleteProjectRequest` → `deleteProject(key, workingDir, sources)` →
  `evener/project/delete` (`Rail.tsx`, `actions.ts`). **Shipped:** both ends
  refuse a project that belongs to a host — `deleteProject` before issuing any
  request, and the `projectDelete` handler with a typed `InvalidParams`
  (`project_delete.go:128-132`) — so a remote row cannot delete the
  controller's own sessions; "New session" carries the project's source into the
  spawn form. Requirements:
  - every navigation project summary carries its owning source — the same
    `ref.SourceID` host qualification the group key and the archive/favorite
    stores use, projected onto the project entity (e.g. a `source`/`host_id`
    field beside `key`/`name`/`working_dir`), so the frontend reads it without
    re-deriving it from a member row, and the schema validators (Go and
    frontend) admit it;
  - `evener/project/delete` is **local-only**, and its params must gain the
    owning source. `ProjectDeleteParams` (`appwire/types.go`) today carries only
    `Key`/`WorkingDir`, so a direct delete request cannot distinguish a
    remote/unknown project from a local one before performing a destructive
    delete. It must add a `Source` field (the row's `ref.SourceID` host,
    defaulting to `"local"`); the frontend passes it from the row ref, and the
    handler refuses any non-local/unknown source **server-side** with a typed
    error before resolving `Key`/`WorkingDir` or deleting anything (never a
    controller-local delete, never a deletion on the wrong machine). For a
    non-local project the menu **hides** "Delete project…" (no remote deletion
    exists in v1);
  - archive/favorite are the **route-by-source** actions (above), passing the
    project's source with each call, and "New session" carries the project's
    source into the spawn form (the `ThreadStartParams.Source` / picker path)
    rather than opening the controller at a remote path.
  **Implementation status:** shipped for the delete gate and the frontend
  ownership it reads: `ProjectDeleteParams.Source` exists (`appwire/types.go`),
  the rail's `RailProject` carries `sources` (`railNodes.ts`), and
  `deleteProject` refuses a project that also belongs to a host before issuing
  any request (`actions.ts`). `identifier.Project` itself still carries no
  source field.
  **`annotateThreadProjects` must not overwrite a validated non-local
  identity.** The merged thread-list path (and the thread-read and
  lifecycle/start responses) runs `annotateThreadProjects`
  (`cmd/evener-hub/app_threadlist.go`) over every returned row, resolving a
  local row's `CWD` with `identifier.ResolveProject` — the controller's local,
  path-based resolver — and writing `ProjectID` / `ProjectPath`. **Shipped:** a
  row whose source is **not** `local` (its `ref.SourceID` names a configured
  host) is skipped, keeping the `ProjectID` / `ProjectPath` the remote hub
  already validated against the remote filesystem, because re-resolving that
  path with the controller's own resolver would clobber the remote identity with
  the controller's view of the same path (the wrong project, or none at all,
  when that path does not exist on the controller)
  (`app_threadlist.go:242-255`).
- **Manifest source list**: `apiTreeSources`
  (`web_api_tree.go`) is consumed by `navigation_service.go` and
  projected via `navigationSources` into `NavigationManifest.Sources`
  (`navigation_projection.go`, `415-422`).

### Go changes

1. **Connectivity signal** (`cmd/evener-hub/internal/appsource/`): add an
   optional interface, e.g. `Online() bool` (or `Status() SourceStatus`), that
   a source *may* implement; define the default (implemented = always online
   for local; remote source implements it from the connection manager). Do not
   widen the base `Source` interface unless every implementer is updated — the
   tests use many `Source` stubs (`appsource` coverage stubs, `scriptedAppSource`
   in `cmd/evener-hub/*_test.go`), so an optional interface avoids a broad
   churn PR.
2. **Truthful `Online` + `Kind` in `apiTreeSources`**
   (`web_api_tree.go`): replace both hardcoded `Online: true` literals it emits
   (the `"local"` entry and the `"appwire"` entry) with the source's online
   state (local always true; remote from the new interface). This is the one
   hardcoded/local-only source list the design calls out.
2b. **Invalidate navigation on attach/detach.** `apiTreeSources` reads `Online`
    from the source per request, so the *value* is truthful on the next read —
    but `NavigationManifest.Sources` is served from the navigation snapshot, so
    without an invalidation the flipped flag is invisible until the 30s
    background refresh, and the fleet view reports a down host as online (or
    vice versa) for up to half a minute. Register this poke on the hub's
    **single `sshconn.Options.OnEvent` fan-out** in `cmd/evener-hub/main.go`
    (component 04, §"Channel lifecycle states") — the same fan-out component 05's
    broker rebind is registered on, and never by assigning `Options.OnEvent` to
    one consumer's closure — so `EventAttached` and
    `EventDetached` invalidate navigation through the same
    `pokeAttention`/`navigation.Invalidate` path the roster/past changes use.
    The callback runs **synchronously under component 04's per-host lock and is
    non-reentrant** (component 04, §"Channel lifecycle states"): it must only record
    state or poke, and must never call `sshManager.Ensure` — that takes the same
    lock and deadlocks. **Implementation status:** shipped — `main.go` registers
    the poke on the manager's `OnEvent` fan-out, invalidating navigation and
    waking the remote-thread refresher (`cmd/evener-hub/main.go:468-505`), and
    the send is non-blocking, because the lock is held.
3. **Offline-host rows need a distinct "source unreachable" field, never
   `Dormant`.** `NavigationSessionSummary.Dormant` is projected from
   `node.Dormant` (`navigation_projection.go`), and `Dormant` means a session
   that **has never run**: "no model response and no accepted user input"
   (`hubcore/tree.go`, `dormantFor`), which the rail renders as "Not started"
   (`RailRow.tsx`, `saysNotStarted`). The earlier claim that `Dormant` is "set
   for local past sessions" is wrong — it is set for *never-run* sessions. For
   an offline host, last-known remote rows are not live (`Live` is computed
   from the live inputs at `navigation_projection.go`, `1137-1138`), but they
   genuinely **ran**; setting `Dormant` on them would label every quiet remote
   session "Not started". Requirements:
   - add a distinct projected field for "this row's source is unreachable"
     (e.g. `Offline`/`Stale` on `NavigationSessionSummary`, alongside
     `Dormant`), set on folded remote rows whose source is offline, at the
     ingestion (`web_api_tree.go`) or tree/projection seam — key it off source
     identity, not the row's own state;
   - leave `Dormant` meaning never-run: correct its description here and never
     set it for remote/offline rows;
   - the rail renders the offline/stale affordance from the new field; "Not
     started" stays true only of never-run sessions.
   **Implementation status:** `NavigationSessionSummary` has only `Dormant`
   today (`hubapi/navigation.go`); the new field and its rail rendering are the
   implementing PR's requirement.
4. **Session targeting in `thread/start`**:
   - `hubThreadStart` (`app_threadlifecycle.go`) currently selects a
     source via `launchSourceID(params)` (`app_threadlifecycle.go`),
     which treats the **`harness`** value as a source ID (returns `"local"` for
     `"evener"`, the harness string otherwise, `""` for empty). Add an explicit
     source ref field to `ThreadStartParams` and resolve it here with sole
     authority when set, consulting the harness fallback only when it is empty
     and refusing a harness value that names a configured host source (so a
     harness cannot silently retarget a spawn to a remote host). Target the
     named source via `sources.Source(id)` and call
     `source.StartThread(ctx, params)` (the existing branch at
     `app_threadlifecycle.go`).
   - Note: the ref-default path the brief referenced is `sourceForThread`
     (`app_sources.go`), which defaults to source `"local"` when no ref
     is given (`app_sources.go`). That function is used by `turn/start`
     and the other ref-resolving RPCs (e.g. `app_rpc.go` resolves
     `resolveTurnStartSource`, aliased to `sourceForThread` at `app_rpc.go`),
     not by `thread/start`. `thread/start` is the path that must change to honor
     a host picker.
   - AppWire param naming, wire generation, and the request handlers that
     construct `ThreadStartParams` (`app_rpc.go` registration) must be updated
     together; regenerate the frontend `types.gen.ts` (this is the
     `make generate` path — see Non-scope note; the implementation PR runs it).
5. **The Connect action needs a browser-reachable attach RPC — `Ensure` alone
   is not one.** The attachment affordance below must initiate component 04's
   `Ensure`, but `Ensure` is a Go method with no wire surface, the component-07
   proxy refuses a non-attached host by design, and the snapshot stays
   attached-only — so a configured-but-never-attached host would be dead UI
   despite the affordance. Define exactly one new **hub-scoped** method,
   `evener/host/attach` (one row in `appwire/protocol.go`, `ScopeHub`):
   - Params: `HostAttachParams{Host string}` — a component-03 source ID.
   - Handler: resolves `Host` through `hostreg.Registry.Get` (component 03);
     unknown → `appwire.InvalidParams`; else calls component 04's
     `sshManager.Ensure(ctx, host)` and returns the post-attach state
     (`serverName`/`version` from the attach handshake, plus the host ID) so the
     row can flip *attaching → online*.
   - Error mapping: the manager's typed errors pass through unchanged —
     unreachable/refused dial → `appwire.Unavailable`, deploy/version failures →
     component 04's typed `ErrDeploy`/`ErrProtocolIncompatible` — never a silent
     disabled row.
   - Idempotent while attached (`Ensure` is), so a repeated Connect is safe.
   - **Origin guard:** attaching dials a remote host, so the handler is gated by
     component 07's shared host-routing origin guard (§"Host-routing origin
     guard"): a **remote-originated** request (`origin` non-empty) is refused
     typed before `Ensure` is called, so a peer hub cannot make this hub attach a
     new host. A local-originated Connect is unaffected.
   - **Ownership:** component 04 owns `Ensure`; the handler lives with the other
     hub-scoped host methods in `registerMiscHandlers` (`app_rpc.go`). It is a
     plain hub-scoped method — **not** a forwarded `evener/host/request` admin
     call (component 07, §"Proxy method") and not added to that allow-list,
     because there is no host to forward to until the attach succeeds.
   - The handler is a local (non-bridge) browser request — its connection
     presents the capability token without `X-Evener-Bridge: 1`, so its
     loop-guard origin is empty (component 05 §"Ref translation detail") — and
     it is not itself a fan-out.
   The Connect control issues this call on the host row and renders its
   progress/error; it must not issue a proxy call, which refuses an unattached
   host. `appwire_catalog_test.go` picks the method up automatically; add a Go
   test asserting the handler dials exactly once through a stub manager and
   surfaces the typed error on failure.

### Frontend changes (high level)

- **New-session form**: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx`
  (form component `SpawnForm`, mounted at `/new`; see `Spawn.tsx` and
  the form body `1485-1882`). Add a host selector near the working-directory /
  settings cluster, local preselected. Persist the choice in the spawn draft
  (`panes/spawn/spawnDrafts.ts`) so it survives navigation like other fields.
- **Start seam**: `panes/spawn/startThread.ts` — `SpawnRequest`
  (`startThread.ts`) has no host field; add one and pass it into the
  `thread/start` params in `startThread` (`startThread.ts`), which is the
  canonical launch seam. `panes/spawn/schema.ts` may need the new field's
  default only if it participates in the effective launch-config preview.
- **Host-dependent discovery must use the selected host.** Routing only
  `thread/start` through the picker is not enough. The form's other calls —
  model discovery (`model/list`), harness discovery (`evener/harnesses/list`),
  path completion (`evener/paths/complete`), path validation
  (`evener/path/validate`), directory creation (`evener/dirs/create`), launch
  resolution/schema (`evener/launch/resolve`), recent projects
  (`evener/projects/recent`), the spawn slash catalog
  (`evener/spawn/slashCatalog`), the branch/location chip's git HEAD read
  (`evener/git/head`, `frontend/src/shell/gitLocation.ts`), the plugin-preview
  panel (`evener/plugin/preview`, `panes/spawn/usePluginPreview.ts`), and the
  provider-instance list the provider setup reads (`evener/instance/list`,
  `stores/credentials.ts`) — are host-dependent and must be issued against
  the selected host, not left controller-scoped, or a remote launch is
  validated against the wrong machine (a controller-local path accepted for a
  remote spawn, the controller's model/harness list, its git repository's
  branch, its plugin diagnostics and provider instances). A remote working
  directory's `evener/git/head` read against the controller's filesystem shows
  the wrong branch (or none), and the plugin/instance reads describe the wrong
  machine's configuration.
  **The routing mechanism is the host-scoped request envelope
  `evener/host/request`** (component 07, §"Proxy method"): each call is wrapped
  as `{host: <selected source ID>, method: "<method>", params: <the call's
  params>}` and the controller forwards it to that host's hub over its SSH
  channel, so the host's own handlers resolve the models, harnesses, paths,
  directories, launch config, and slash catalog against the host. The form must
  **not** issue these methods on the plain controller connection and rely on the
  handler being host-aware: the controller's handlers run against the
  controller's local environment. `RemoteHubSource` is not the path for these
  calls — of the set above only `model/list` is on the `Source` interface (as
  `ListModels`, for thread model selection), and that source method does not
  scope a spawn form's host-selection read (component 05, §"Method-coverage
  analysis") — the proxy envelope is. A remote working directory is therefore
  validated on the remote filesystem, not the controller's, and the `host` the
  form passes must be the same value it sends as `ThreadStartParams.Source`.
  Until the proxy and its allow-list land (component 07, §"Proxy method"), the
  picker must not present a remote host as fully usable.
  **Parity of the discovery set is enforced, not assumed.** This set is listed
  twice — here and in component 07's proxy allow-list (component 07,
  §"Host-dependent discovery") — with no coverage today, so drift silently
  validates a remote launch against the controller filesystem or fails closed
  with `InvalidParams`. The two must be a **single shared source of truth** (one
  exported list both this form's envelope routing and the 07 proxy allow-list
  consume), or, failing that, a scripted-host test must forward each discovery
  method through the `evener/host/request` envelope and assert the proxy accepts
  every method the form issues. Either way a method added to one list is a test
  failure until the other matches.
- **Source list source**: the manifest already carries `sources` and the
  navigation store already holds it (`stores/navigation/store.ts`,
  validated at `store.ts` and `stores/navigation/codec.ts`), but
  nothing renders or selects from it in production. Add a selector, e.g.
  `stores/navigation/selectors.ts`, exposing `manifest.data.sources`, and let
  the spawn form consume it. Generated `Source` type is
  `protocol/types.gen.ts`.
- **Host label on rows**: `NavigationSessionSummary.host_id` is generated
  (`types.gen.ts`) and populated server-side. The rail render seam is
  `shell/rail/Rail.tsx` / `shell/rail/railNodes.ts` (session presentation
  contract at `railNodes.ts`). Add a host badge/tooltip for rows whose
  `host_id` is not `"local"`, and an offline/stale visual for rows whose source
  is unreachable.
  Keep this to a label, not a tree re-layout.
- **Connecting a configured host**: a configured host that has never been
  attached has no user-facing way to become usable — `Online` is attachment
  state (above), the picker disables offline hosts, and host-scoped actions are
  refused until a channel exists — so a mere `[[hosts]]` entry is dead in the
  UI. The picker must therefore provide a **concrete, enabled attachment
  affordance** for every offline/never-attached host — an explicit
  "Connect"/"Attach" action on the host row, or selection of that host
  initiating attachment — never merely a disabled spawn row and never a control
  that cannot fire. This first host action must initiate attachment through the
  browser-reachable `evener/host/attach` RPC (§"Go changes" item 5, which calls
  component 04's `Ensure`) and surface its progress ("attaching") and failure
  states: an attach flips `Online` (via the attach event, §2b) and makes the
  host selectable, and a failure shows the manager's error (the offline refusal
  below), not a silently disabled control. Nothing else attaches a host on the
  user's behalf — in
  particular the background snapshot stays attached-only
  (§"Background snapshot"). Because the host's hub may not be running yet, this
  action is also the trigger for component 04's **first-attach bootstrap**
  (component 04 §5): `Ensure` starts the stopped hub, waits for its
  `/api/health` `version` to match the expected build, and only then attaches,
  so a clean or stopped host can reach `Online:true` from the one promised host
  action. A start that never becomes healthy is the failure the action
  surfaces.
- **Action gating**: a session row on an offline host must not offer host
  actions. The server-side `Rename` flag already excludes non-local rows
  (`web_api_tree.go`), so at minimum the read-only host rows
  are already non-renameable; confirm the composer/session actions honor the
  refusal error (see Error handling) rather than silently failing. **Remote
  session rows must not expose a Delete action that always fails:** the existing
  session-delete handler rejects a non-local session reference, so a remote row
  showing Delete presents an action that can never succeed. The rail must hide
  or disable session deletion for non-local rows, while the server-side rejection
  of a direct remote delete request is retained (defense in depth).

## Data flow

```
configured [[hosts]]  (Component 03)
        │
        ▼
SSH connection manager (Component 04) ── online/offline per host
        │
        ▼
appsource.Registry.Add(remoteHubSource per host)   app_rpc.go
        │
        ├── thread/list  → hubThreadListWithSourceTimeout  app_threadlist.go
        │                     (fan-out 4 workers, 3s/source, merge, filter)
        │
        └── background refresh (30s + poke)  main_background.go
                 │ refreshRemoteThreadSnapshot  web_api_tree.go
                 ▼
            hubcore.RemoteThreadCache  (last-known-good per source)
                 │
                 ▼
      navigationSnapshotInputs folds rows into metas/live  web_api_tree.go
                 │
                 ▼
      manifest.Sources ← apiTreeSources()   web_api_tree.go
      rows.HostID      ← ref.HostID         navigation_projection.go
                 │
                 ▼
      frontend: manifest.sources → host picker (spawn)
                host_id → row badge; offline → offline/stale rows + refused actions
```

Session start: `thread/start(source∈{local,host}, cwd, …)` →
`hubThreadStart` selects `sources.Source(source)` → `Source.StartThread` →
remote source maps to the host hub's hub-scoped RPC (Component 05).

## Error handling

- **Unreachable host, read path**: remain non-fatal. The refresh retains
  last-known-good rows (`web_api_tree.go`) and marks the source
  `Online:false`; the manifest still lists the host. `thread/list` skips a
  failed non-explicit source (`app_threadlist.go`), so one dead host does
  not fail a fleet-wide list. A request that explicitly names the dead host in
  `SourceIDs` should return the source error.
- **Unreachable host, action path**: host-targeted actions must be refused with
  a typed, actionable error — not a silent drop or a generic 500. Prefer
  `appwire.Unavailable(...)` ("host <name> is offline; reconnect to run this
  action") consistent with existing source-unavailable errors
  (`app_threadlifecycle.go`, `app_sources.go` error paths). Actions through
  `sourceForThread` (`app_sources.go`) already default to `local`; a
  remote ref on an offline host resolves to the registered-but-offline source,
  so the refusal belongs in the source/connection layer and must surface
  unchanged.
  **The refusal must be non-dialing.** A direct remote read or mutation
  resolves its client through the **attached-only** accessor
  `sshconn.Manager.ClientIfAttached(name) (*appwire.Client, bool)` (shipped by
  component 04a, `cmd/evener-hub/internal/sshconn/manager.go`) — never through
  the `Ensure`-backed `RemoteHubClientFunc` — so an offline host is refused with
  `appwire.SessionUnavailable(...)`/`appwire.Unavailable(...)` instead of being
  reconnected by the very action that was supposed to be refused (component 05,
  §"Every other remote call is non-dialing, not just the snapshot and the
  non-explicit list"). `Ensure` belongs only to the **explicit attach
  triggers** — this component's Connect action (`evener/host/attach`) and an
  explicit host named in `thread/list`'s `SourceIDs` — and to the first-attach
  bootstrap those drive. Nothing else attaches a host on the user's behalf.
- **Start on an offline host**: the picker must disable offline hosts *for a
  spawn* and offer the connect action instead (above) — a never-attached host is
  therefore reachable, not dead UI. If a start request still arrives,
  `hubThreadStart`'s source branch returns
  `Unavailable("spawn source is not available: <id>")`
  (`app_threadlifecycle.go`) — confirm and keep that contract, and resolve the
  source through the attached-only lookup so the refusal never dials the host
  back.
- **Connect action failure**: `evener/host/attach` returns the manager's typed
  error unchanged — `Unavailable` for an unreachable/refused host, component
  04's `ErrDeploy`/`ErrProtocolIncompatible` for a deploy/version failure — and
  the row renders *attaching → failed* with that error, never a silent no-op or
  a permanently disabled control.
- **Source removed / unknown ref**: `Registry.SourceForRef` returns
  `source not found: <id>` (`registry.go`); `sourceForThread` maps a
  parse failure to InvalidParams (`app_sources.go`). Reconnect/removal is
  owned by Components 03–05; this component only consumes the state.
- **Schema bounds**: keep `Online` a plain bool, `Kind`/`ID` valid identities,
  and `Label` bounded; both Go (`navigation_schema.go`) and frontend
  (`codec.ts`) validators reject malformed sources.

## Testing

- **Go unit**
  - `apiTreeSources` truthfulness: with a registered stub source whose
    `Online()` is false, the manifest reports `Online:false` and `local` stays
    true. Extend the existing coverage tests that already call
    `apiTreeSources` (`web_covtest_test.go`,
    `cov_session_tree_pass3_fuzz_test.go`).
  - Fan-out merge: a mid-list failing non-explicit source degrades to a skip;
    the same source named in `SourceIDs` returns the error
    (extend `app_threadlist.go` tests / `cov_threadlife_list_pass6_fuzz_test.go`
    which already exercises `SourceIDs`/`SearchTerm`).
  - Offline marking: an offline source's last-known rows project with `HostID`
    set and the new offline/stale field true, and with `Dormant` **unchanged**
    (a quiet remote session that ran is never labelled "Not started"); a
    never-run session still projects `Dormant:true` only via `dormantFor`; an
    online source's rows are not marked offline.
  - `thread/start` targeting: explicit source routes to that source's
    `StartThread`; missing source returns the existing unavailable error; empty
    source still defaults local. Extend `app_rpc_test.go` which already stubs
    `resolveTurnStartSource` (`app_rpc_test.go`).
  - Manifest schema: sources array still validates under
    `navigationManifestValuesValid` (`navigation_schema.go`).
  - Navigation invalidation on connectivity: a stub source whose `Online()`
    flips, driven by an `EventAttached`/`EventDetached` through the wired
    `OnEvent`, makes the next manifest read reflect the new flag **before** any
    background refresh tick. The callback does not re-enter `Ensure`.
  - Refresh stays lazy: a stub source whose `Online()` is false is **not**
    called by the background refresh (assert its `ListThreads` is never
    invoked), and a source that reports attached is; the check proves the walk
    never dials an unattached host.
  - Fleet-wide list stays lazy: a non-explicit `thread/list` (empty
    `SourceIDs`) never calls the `Ensure`-backed resolver for an unattached
    source (assert no `Ensure` and no `ListThreads` on it), while an explicit
    `SourceIDs` naming that host does attach it (extend the
    `app_threadlist.go` tests).
  - Connect RPC: `evener/host/attach` with a configured host calls a stub
    manager's `Ensure` exactly once and returns the post-attach state; an
    unknown host is `InvalidParams`; a failed attach surfaces the manager's
    typed error rather than an empty success.
  - Host-qualified projects: two remote sources whose threads share a working
    directory (same path, different hosts) produce two distinct project entries
    with distinct identities, neither resolved from a local path; a remote
    path that exists on the controller's filesystem does not adopt the
    controller's project identity.
  - Source-qualified navigation identity (the two-host same-key test): two
    sources — one of them `local` — whose projects share a project ID **and** a
    working-directory path produce two distinct catalog keys in the qualified
    `"<source>:<projectID>"` encoding, two distinct `project` and
    `project_page` reads (asking for one host's key never returns the other's
    sessions), two distinct server view/entity/container scopes and two
    distinct invalidation targets (a poke for one leaves the other's cached
    entry settled), and a bare (unqualified) key that still resolves to the
    local project; a qualified key naming an unconfigured source is refused
    typed, never degraded to local. Extend the navigation cache/service tests
    (`navigation_cache.go`, `navigation_service.go`) and drive the frontend
    half through the same fixture in the `stores/navigation`
    codec/store/revalidator tests (distinct `keyID`/`navigationViewScope`
    values, and a `targetBase` round trip that lands on the registered key).
  - Direct remote actions stay non-dialing: a `thread/read`, a mutation, or a
    subscription against an **unattached** source asserts `Ensure` is never
    called (and no dial happens) and returns the typed unavailable error; the
    same call against an attached source reaches the host.
  - Source-qualified session pins: two hosts' sessions with the **same bare
    thread ID** (`host:<id>` and `local:<id>`) hold separate pin assignments —
    assigning one does not move the other, unpinning one leaves the other, and
    the projection marks each row pinned by its own source. Assert the pin
    assignment/unpin RPC resolves a `host:<id>` `SessionRef` through the host and
    a bare/`local:` ref through the local source, and that an unknown source is
    refused typed; assert the migration backfills `source = "local"` on a
    pre-existing `session_pin` row without losing it, and that a bare/empty
    local lookup still resolves to the canonical `"local"` row after migration.
  - Source cap: no count test — the 64-source cap is withdrawn by decision
    (component 03, §Scope), so an over-limit manifest is a configuration the hub
    is expected to carry.
- **Frontend unit**: spawn form shows a host picker with local preselected and
  offline hosts disabled; picking a host sends the source field in the
  `thread/start` request (extend `panes/spawn` tests and
  `stores/navigation` codec/store tests). Assert `host_id` renders a badge for
  non-local rows. The Connect control on an offline host row issues
  `evener/host/attach` (assert the outgoing request) and renders attaching and
  the mapped error.
- **Live**: the design's environment-gated SSH test
  (`EVENER_SSH_E2E=1`, design §7) covers the end-to-end fleet view against a
  disposable host; never in default `make test`.
- Per the brief for this spec, no `make` or frontend build was run to produce
  this document; the implementation PR runs `make test` / `make vet` and the
  frontend unit suite.

## Acceptance criteria

1. With two hosts configured, both reachable, and each already **attached** (it
   has served a request, so component 04's lazy manager holds a live channel),
   the navigation manifest lists three sources (`local` + two hosts) with
   `Online:true`, and each host's sessions appear in the merged list and tree
   with `host_id` equal to the host name; search matches sessions on any host.
   A reachable host that has not yet been attached reports `Online:false` — the
   manifest read has no probe that attaches on demand — so `Online` means
   "attached", not "reachable", in every criterion here.
2. With one host unreachable, the manifest lists it with `Online:false`, its
   last-known sessions remain visible and render with the offline/stale marker
   (never `Dormant`), and no fleet-wide
   list fails because of it.
3. A host-targeted action against the offline host is refused with a typed
   `Unavailable` error naming the host; no action silently succeeds.
4. The new-session form offers a host picker, local by default; starting with a
   non-local host creates the session on that host and the new session's ref
   carries the host as its source.
5. `Source`/`NavigationSessionSummary` schema validation (Go and frontend)
   passes unchanged in shape, and `make test` is green.
6. Attaching or dropping a host flips that host's manifest `Online` flag on the
   next manifest read, without waiting for the 30s background refresh; the
   invalidation is driven by the `EventAttached`/`EventDetached` poke and the
   callback never calls `Ensure`.
7. **Withdrawn** (component 03, §Scope): no 64-source cap and no
   `ErrTooManyHosts`; an over-limit configuration loads, and the manifest's
   64-source limit is the accepted v1 risk.
8. Two hosts whose sessions share a working-directory path appear as two
   distinct projects (host-qualified identity), and that one qualification is
   carried as a single `"<source>:<projectID>"` string (with `local` canonical)
   through the project catalog row keys, the `project`/`project_page` read
   parameters and their server resource keys, the session location's owning
   project, the invalidation target, and the frontend `ResourceKey`, cache
   scope, and selector — so two hosts whose projects share a project ID *and* a
   path share no key, read, cache entry, or invalidation target on any of those
   surfaces, a bare key still resolves to the local project, and a key naming an
   unconfigured source is refused typed. No remote row's project is resolved
   against the controller's filesystem; the refresh attaches no host that was
   not already attached (assert no `Ensure`/`ListThreads` on an unattached
   source).
9. A configured-but-never-attached host is not dead UI: its first host action
   issues the browser-reachable `evener/host/attach` RPC (component 04's
   `Ensure`) and surfaces progress and failure mapped from its typed errors; on
   success the host reports `Online:true` and is selectable for a spawn. No
   other path attaches a host implicitly: the non-explicit fleet-wide
   `thread/list` (empty `SourceIDs`) runs only against already-attached sources
   and never calls the `Ensure`-backed resolver for an unattached host, so
   opening the app and listing the fleet attaches nothing (component 05, §"A
   background/snapshot caller must not force attachment"). For a host whose hub
   is stopped, the same action drives component 04's bootstrap (start + health
   wait) rather than failing at the first dial.
10. Every host-dependent discovery/validation call the spawn form makes (model,
    harness, path completion/validation, launch resolution, recent projects,
    slash catalog, git HEAD, plugin preview, provider-instance list) is issued
    against the selected host, so a remote launch is validated against the
    remote's filesystem and capabilities, not the controller's; the component-07
    allow-list enumerates exactly this set (plus its admin families).
11. Favorite and archive actions carry the owning source: two hosts' projects
    with the same ID (or path) hold separate favorite/archive keys, and a
    project archive on a non-local host succeeds without resolving
    `params.WorkingDir` against the controller's filesystem. A pre-existing
    local favorite/archive survives the `source = "local"` migration and
    resolves under the canonical `"local"` key (an empty/bare lookup
    normalizes to it), and the migration rebuilds all three shared tables in
    one versioned `BEGIN IMMEDIATE` transaction before the stores serve.
12. Session pin assignments carry the owning source: two hosts' sessions with the
    same bare thread ID hold separate pins, the assign/unpin handlers resolve the
    requested `SessionRef` through its source, and a pre-existing local pin
    survives the `source = "local"` migration — including a bare/empty local
    lookup, which normalizes to the canonical `"local"` key rather than missing
    the migrated row.
13. A direct remote read or mutation against an **unattached** host returns the
    typed unavailable error (`appwire.SessionUnavailable`/`Unavailable`, naming
    the host) and never dials it: the call resolves
    `Manager.ClientIfAttached` and the test asserts `Ensure` is called zero
    times and no transport is opened. The only paths that may attach are the
    explicit attach triggers — this component's Connect action and an explicit
    host in `thread/list`'s `SourceIDs` — plus the first-attach bootstrap they
    drive.

## PR size estimate (LOC)

Estimates only (no code written yet); roughly measured, not compiled.

| Area | Files | Est. LOC |
|---|---|---|
| Optional source-online interface + stub updates | `appsource/*` | 40–80 |
| `apiTreeSources` online/kind + offline/stale fold | `web_api_tree.go`, tests | 60–120 |
| `thread/start` source field (wire type, handler, generate) | `appwire/types.go`, `app_threadlifecycle.go`, generated TS | 60–120 |
| Frontend host picker + selector + start seam | `Spawn.tsx`, `startThread.ts`, `spawnDrafts.ts`, `selectors.ts` | 120–220 |
| Frontend row host badge + offline/stale styling | `railNodes.ts`, `Rail.tsx`, CSS | 60–140 |
| Tests (Go + TS) | various | 200–350 |
| **Total** | | **~540–1030** |

This is larger than a single tight PR if done at once. A natural split:

- **06a** (Go-only, ~200–320 LOC): connectivity interface, truthful
  `Online`/`Kind`, offline/stale fold, `thread/start` source targeting + Go tests.
  Reviewable and landable with no UI change (picker absent → local default
  preserved).
- **06b** (frontend, ~340–700 LOC): host picker, row host badge, offline/stale
  affordances, TS tests. Depends on 06a's generated type/source field.

## Open questions

- **Wire field shape for targeting (settled)**: `ThreadStartParams.Source` — a
  bare source ID, serialized as `source` — is the chosen shape; reusing the
  existing `ref` or overloading `Harness` were the rejected alternatives. Only
  `Source` is controller-only and is stripped before any remote forward;
  `Harness` is the caller's backend selection and is **forwarded verbatim** (or
  explicitly translated) — it is never stripped or overwritten (§"Write
  contract"; component 05, §"Registration and default-source selection").
- **`Kind` value for hosts**: reuse `"appwire"` or introduce a host-specific
  value? The frontend cannot currently tell an attached host from any other
  appwire source; if grouping/labels need that distinction, pick a value now to
  avoid a second schema change. (Both validators must change together.)
- **Rail grouping by host**: this spec deliberately stops at a picker plus a
  row badge. If users want per-host collapsible sections, that is a larger
  rail/tree reshape and should be its own spec/PR.
- **Online semantics**: what counts as online — a live SSH channel, a
  successful capability probe, or both? The exact signal is owned by Component
  04. v1's signal is attachment state (a current, non-closed channel —
  `sshconn.Manager.Attached`, installed on the source via `SetHostOnline` from
  `hubcore.WebConfig.RemoteHostOnline`), not the capability probe: a host whose
  probe is stale but whose channel is live is online. This spec only requires
  that `apiTreeSources` can read a boolean and that a transition invalidates
  navigation: `main.go` already does this on cache changes, and the required
  `EventAttached`/`EventDetached` poke (§Go changes item 2b) does it on a
  connection-state change, so the manifest flips without waiting for the 30s
  refresh.
- **Offline/stale vs `Live` for offline rows**: confirm no path sets `Live:true` for
  an offline host's stale rows (`navigation_projection.go`
  compute `Live` from the live input set). If stale remote rows are pushed into
  `snapshot.live` (`web_api_tree.go`), the offline filter must run
  before or inside the projection, not after.
- **Unknown / partially verified**: I did not run the build or the frontend
  suite, so `ThreadStartParams` generation and rail CSS class names are
  unverified; the exact frontend module list may shift by one or two files.
