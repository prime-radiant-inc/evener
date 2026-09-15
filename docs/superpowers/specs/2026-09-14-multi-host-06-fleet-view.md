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
dormant, and let a new session choose an explicit host (local by default).

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
4. **Offline / dormant** — a host that cannot be reached reports `Online:false`
  ; its last-known sessions render as dormant and host-targeted actions are
   refused until reconnect.
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
rather than overloading `Harness`. `hubThreadStart` resolves it ahead of the
legacy `launchSourceID` harness fallback.

**The field is controller-only and must not reach the remote hub.**
`ThreadStartParams.Source` names a source in the controller's registry; the
remote hub would resolve the same string against its own registry (wrong target
or `spawn source is not available`). `RemoteHubSource.StartThread` clears it
before forwarding (component 05, §"Registration and default-source selection"),
so a host picker read on the wire never changes what the remote hub does: the
chosen host is expressed by which `RemoteHubSource` handled the start.

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
  (`web_api_tree.go`) walks every non-local source, backfills
  `thread.Source`/`thread.Evener.Ref`, records per-source completeness and
  conflict IDs, and stores into `hubcore.RemoteThreadCache`
  (`internal/hubcore/remotecache.go`). The refresher runs on a 30s
  ticker + poke (`main_background.go`), wired in
  `main.go`; cache changes invalidate navigation
  (`main.go`). Last-known-good per-source results are retained on transient
  errors (`web_api_tree.go`).
- **Tree ingestion**: `navigationSnapshotInputs` folds cached remote threads
  into the same `metas`/`live` inputs as local sessions
  (`web_api_tree.go`).
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
3. **Dormant marking for offline-host rows**: `NavigationSessionSummary.Dormant`
   is projected from `node.Dormant` (`navigation_projection.go`), and
   `Dormant` is set for local past sessions in `hubcore/tree.go`.
   For an offline host, last-known remote rows are not live
   (`Live` is computed from the live inputs at `navigation_projection.go`,
   `1137-1138`), but nothing currently sets `Dormant` for them. Set `Dormant`
   on folded remote rows whose source is offline, at the point remote threads
   are ingested (`web_api_tree.go`) or in the tree/projection seam —
   pick the seam that keys off source identity, not the row's own state.
4. **Session targeting in `thread/start`**:
   - `hubThreadStart` (`app_threadlifecycle.go`) currently selects a
     source via `launchSourceID(params)` (`app_threadlifecycle.go`),
     which treats the **`harness`** value as a source ID (returns `"local"` for
     `"evener"`, the harness string otherwise, `""` for empty). Add an explicit
     source ref field to `ThreadStartParams` and resolve it here, ahead of the
     harness fallback. Target the named source via `sources.Source(id)` and
     call `source.StartThread(ctx, params)` (the existing branch at
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
  `host_id` is not `"local"`, and a dormant/offline visual for dormant rows.
  Keep this to a label, not a tree re-layout.
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
                host_id → row badge; offline → dormant rows + refused actions
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
- **Start on an offline host**: the picker must disable offline hosts; if a
  request still arrives, `hubThreadStart`'s source branch returns
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
  - Dormant marking: an offline source's last-known rows project with
    `HostID` set and `Dormant:true`; an online source's rows do not.
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

1. With two hosts configured and both reachable, the navigation manifest lists
   three sources (`local` + two hosts) with `Online:true`, and each host's
   sessions appear in the merged list and tree with `host_id` equal to the host
   name; search matches sessions on any host.
2. With one host unreachable, the manifest lists it with `Online:false`, its
   last-known sessions remain visible and render dormant, and no fleet-wide
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

## PR size estimate (LOC)

Estimates only (no code written yet); roughly measured, not compiled.

| Area | Files | Est. LOC |
|---|---|---|
| Optional source-online interface + stub updates | `appsource/*` | 40–80 |
| `apiTreeSources` online/kind + dormant fold | `web_api_tree.go`, tests | 60–120 |
| `thread/start` source field (wire type, handler, generate) | `appwire/types.go`, `app_threadlifecycle.go`, generated TS | 60–120 |
| Frontend host picker + selector + start seam | `Spawn.tsx`, `startThread.ts`, `spawnDrafts.ts`, `selectors.ts` | 120–220 |
| Frontend row host badge + dormant/offline styling | `railNodes.ts`, `Rail.tsx`, CSS | 60–140 |
| Tests (Go + TS) | various | 200–350 |
| **Total** | | **~540–1030** |

This is larger than a single tight PR if done at once. A natural split:

- **06a** (Go-only, ~200–320 LOC): connectivity interface, truthful
  `Online`/`Kind`, dormant fold, `thread/start` source targeting + Go tests.
  Reviewable and landable with no UI change (picker absent → local default
  preserved).
- **06b** (frontend, ~340–700 LOC): host picker, row host badge, dormant/offline
  affordances, TS tests. Depends on 06a's generated type/source field.

## Open questions

- **Wire field shape for targeting (settled)**: `ThreadStartParams.Source` — a
  bare source ID, serialized as `source` — is the chosen shape; reusing the
  existing `ref` or overloading `Harness` were the rejected alternatives. The
  field is controller-only and is stripped before any remote forward
  (§"Write contract").
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
- **Dormant vs `Live` for offline rows**: confirm no path sets `Live:true` for
  an offline host's stale rows (`navigation_projection.go`
  compute `Live` from the live input set). If stale remote rows are pushed into
  `snapshot.live` (`web_api_tree.go`), the offline filter must run
  before or inside the projection, not after.
- **Unknown / partially verified**: I did not run the build or the frontend
  suite, so `ThreadStartParams` generation and rail CSS class names are
  unverified; the exact frontend module list may shift by one or two files.
