# Multi-host evener — Component 06: Fleet view and session targeting

Status: component spec, pre-implementation. Decomposes
`2026-09-14-multi-host-evener-design.md` §4.6 and the decisions in §2
("Fleet view", "Session targeting", "Offline hosts"). It assumes Components
01–05 have landed (stream transport, attach bridge, host config, SSH
connection manager, remote hub source).

Owner surface: `cmd/evener-hub` (Go) and `cmd/evener-hub/frontend/src`
(TypeScript). One PR-sized change, plus optional follow-up splits noted below.

**Implementation status.** The Go half (06a) is implemented on
`multi-host-pr06a-fleet-view-go` and is **pending merge, not on `main`**: the
`sourceOnline`/`appsource.OnlineSource` interface and the truthful `Online` flag
it drives do not exist on `main`. The frontend half (06b) is on
`multi-host-pr06b-fleet-view-ui`.

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
  A source that reports no state defaults to online. **Implemented on
  `multi-host-pr06a-fleet-view-go`, pending merge (none of these symbols is on
  `main`):** `sourceOnline` (`cmd/evener-hub/web.go`) type-asserts
  `appsource.OnlineSource` (also new on that branch,
  `internal/appsource/online.go`) and defaults to true when the source does not
  implement it; `apiTreeSources` uses it for remote entries, and the live-row
  computation gates on `appThreadTreeLive` plus `sourceOnline` as well, so a
  down host's rows are not presented as live. (`apiTreeSources` and
  `appThreadTreeLive` themselves already exist on `main`; the `sourceOnline`
  wiring and gating are 06a's delta.)
- `Label` is bounded by `maxNavigationLabelRunes` (`navigation_schema.go`);
  host labels are short, so truncation is a safety net, not a design point.
- The manifest `sources` array is capped at **64 entries including `local`**
  (`navigation_projection.go`: `len(inputs.Sources) > 64` is an error), and each
  element must have exactly `{id,label,kind,online}` (`navigation_schema.go`).
  With one source per configured host plus the local entry, that is at most
  **63 remote hosts**, so component 03's `validateHostConfigs` rejects a larger
  `[[hosts]]` list (`ErrTooManyHosts`) rather than letting one over-limit config
  fail navigation for the entire hub (component 03, §"Host configuration").
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

There is no source/host field on the start request today:
`appwire.ThreadStartParams` (`appwire/types.go`) has no source field,
and the generated `ThreadStartParams` matches
(`appwire-client/typescript/types.gen.ts`). This component adds an
explicit `source` field to `thread/start` — a bare source ID, not a ref —
rather than overloading `Harness`. `hubThreadStart` gives it **sole authority
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
source is refused (`InvalidParams`) rather than routed or forwarded; the
harness-as-source fallback is likewise refused/retired outright. **Implementation
status:** the shipped 06a `hubThreadStart` resolves the fallback unconditionally
(`multi-host-pr06a-fleet-view-go`, **pending merge, not on `main`**), so this
refusal is a requirement, not a present fact.

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
  **Implementation status:** the shipped `refreshRemoteThreadSnapshot`
  (`web_api_tree.go`) iterates `s.sources.All()` with no attachment gate and its
  resolver is wired to `Ensure`; both the gate and the attached-only lookup are
  the implementing PR's requirement, not a present fact.
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
  **Implementation status:** both handlers (`app_archive.go`,
  `app_favorite.go`) and both param structs (`appwire/types.go`) are the
  unqualified shape today; the source field, store namespacing, and the
  non-local validation rule are the implementing PR's requirement, not a
  present fact.
  **Project summaries must carry the owning source, and destructive
  local-project actions must be gated by it.** The project the rail renders has
  no host dimension today: the resolved model is `identifier.Project` =
  `{ID, CanonicalPath}` (`identifier/project.go`), the projected entity's
  key/name/`working_dir` reach the frontend `RailProject`
  (`shell/rail/railNodes.ts`), and none of them names the source. Remote rows
  are folded into the same project list, and the project context menu
  (`projectMenuItems`, `RailRow.tsx`) unconditionally renders the destructive
  items for every project, with the delete item wired to a **controller-local**
  call: `onDeleteProjectRequest` → `deleteProject(key, workingDir)` →
  `evener/project/delete` (`Rail.tsx`, `actions.ts`). A remote project row can
  therefore trigger a delete of the controller's own sessions/project state (or
  a colliding local project), and "New session" navigates the controller to the
  remote path. Requirements:
  - every navigation project summary carries its owning source — the same
    `ref.SourceID` host qualification the group key and the archive/favorite
    stores use, projected onto the project entity (e.g. a `source`/`host_id`
    field beside `key`/`name`/`working_dir`), so the frontend reads it without
    re-deriving it from a member row, and the schema validators (Go and
    frontend) admit it;
  - `evener/project/delete` is **local-only**: for a non-local project the menu
    **hides** "Delete project…" (no remote deletion exists in v1), and a
    direct call naming a non-local/unknown source is refused typed (never a
    controller-local delete, never a deletion on the wrong machine);
  - archive/favorite are the **route-by-source** actions (above), passing the
    project's source with each call, and "New session" carries the project's
    source into the spawn form (the `ThreadStartParams.Source` / picker path)
    rather than opening the controller at a remote path.
  **Implementation status:** none of this exists today — `identifier.Project`
  and `RailProject` have no source field, `projectMenuItems` renders the delete
  item unconditionally (`RailRow.tsx`), and `deleteProject` sends a bare
  key/working-dir to the local `evener/project/delete` (`actions.ts`). This is a
  tracked code follow-up.
  **`annotateThreadProjects` must not overwrite a validated non-local
  identity.** The merged thread-list path (and the thread-read and
  lifecycle/start responses) runs `annotateThreadProjects`
  (`cmd/evener-hub/app_threadlist.go`) over every returned row. Today it
  resolves each row's `CWD` with `identifier.ResolveProject` — the controller's
  local, path-based resolver — and unconditionally writes `ProjectID` /
  `ProjectPath`, so a remote row's project identity is clobbered by the
  controller's view of the same path (the wrong project, or none at all, when
  that path does not exist on the controller). The requirement: a row whose
  source is **not** `local` (its `ref.SourceID` names a configured host) keeps
  the `ProjectID` / `ProjectPath` the remote hub already validated against the
  remote filesystem; `annotateThreadProjects` skips non-local rows instead of
  re-resolving them, and any local resolution cache it keeps is keyed per source
  so a remote path can never seed the local project for that same path.
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
    vice versa) for up to half a minute. Wire
    `sshconn.Options.OnEvent` in `cmd/evener-hub/main.go` so `EventAttached` and
    `EventDetached` invalidate navigation through the same
    `pokeAttention`/`navigation.Invalidate` path the roster/past changes use.
    The callback runs **synchronously under component 04's per-host lock and is
    non-reentrant** (component 04, §"Channel lifecycle states"): it must only record
    state or poke, and must never call `sshManager.Ensure` — that takes the same
    lock and deadlocks. **Implementation status:** 06a wires no `OnEvent`, so
    today the flag flips only on the next tick; this poke is the implementing
    PR's requirement. The poke must be non-blocking (the existing buffered
    channel send), because the lock is held.
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
  calls — none is on the `Source` interface (component 05, §"Method-coverage
  analysis") — the proxy envelope is. A remote working directory is therefore
  validated on the remote filesystem, not the controller's, and the `host` the
  form passes must be the same value it sends as `ThreadStartParams.Source`.
  Until the proxy and its allow-list land (component 07, §"Proxy method"), the
  picker must not present a remote host as fully usable.
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
  UI. The first host action (selecting the host in the picker, or an explicit
  "Connect"/"Attach" affordance on the host) must initiate attachment through
  component 04's `Ensure` and surface its progress and failure: an attach flips
  `Online` (via the attach event, §2b) and makes the host selectable, and a
  failure shows the manager's error (the offline refusal below), not a silently
  disabled control. Nothing else attaches a host on the user's behalf — in
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
  refusal error (see Error handling) rather than silently failing.

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
- **Start on an offline host**: the picker must disable offline hosts *for a
  spawn* and offer the connect action instead (above) — a never-attached host is
  therefore reachable, not dead UI. If a start request still arrives,
  `hubThreadStart`'s source branch returns
  `Unavailable("spawn source is not available: <id>")`
  (`app_threadlifecycle.go`) — confirm and keep that contract.
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
  - Host-qualified projects: two remote sources whose threads share a working
    directory (same path, different hosts) produce two distinct project entries
    with distinct identities, neither resolved from a local path; a remote
    path that exists on the controller's filesystem does not adopt the
    controller's project identity.
  - Source cap: a manifest built from 63 hosts + `local` validates; 64 hosts +
    `local` (65 entries) is the over-limit case component 03's
    `ErrTooManyHosts` prevents from being configured.
- **Frontend unit**: spawn form shows a host picker with local preselected and
  offline hosts disabled; picking a host sends the source field in the
  `thread/start` request (extend `panes/spawn` tests and
  `stores/navigation` codec/store tests). Assert `host_id` renders a badge for
  non-local rows.
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
7. At most 63 remote hosts can be configured: a 64th is refused at config load
   (`ErrTooManyHosts`), so the manifest's 64-source cap (including `local`) can
   never be exceeded by configuration.
8. Two hosts whose sessions share a working-directory path appear as two
   distinct projects (host-qualified identity), and no remote row's project is
   resolved against the controller's filesystem; the refresh attaches no host
   that was not already attached (assert no `Ensure`/`ListThreads` on an
   unattached source).
9. A configured-but-never-attached host is not dead UI: its first host action
   initiates attachment (component 04's `Ensure`) and surfaces progress and
   failure; on success the host reports `Online:true` and is selectable for a
   spawn, and no other path attaches a host implicitly. For a host whose hub is
   stopped, the same action drives component 04's bootstrap (start + health
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
    `params.WorkingDir` against the controller's filesystem.

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
