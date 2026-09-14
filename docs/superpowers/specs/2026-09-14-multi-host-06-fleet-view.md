# Multi-host evener — Component 06: Fleet view and session targeting

Status: component spec, pre-implementation. Decomposes
`2026-09-14-multi-host-evener-design.md` §4.6 and the decisions in §2
("Fleet view", "Session targeting", "Offline hosts"). It assumes Components
01–05 have landed (stream transport, attach bridge, host config, SSH
connection manager, remote hub source).

Owner surface: `cmd/evener-hub` (Go) and `cmd/evener-hub/frontend/src`
(TypeScript). One PR-sized change, plus optional follow-up splits noted below.

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

`hubapi.NavigationManifest.Sources` (`hubapi/navigation.go:23-30`) is the fleet
enumeration. Element shape `hubapi.Source` (`hubapi/types.go:46-51`):

```
type Source struct {
    ID     string `json:"id"`
    Label  string `json:"label"`
    Kind   string `json:"kind"`
    Online bool   `json:"online"`
}
```

Rules this component must satisfy:

- One entry per configured host plus the local entry.
- `ID` is the ref source ID (the host `name` from `[[hosts]]`, per design §5);
  it must be a valid navigation identity (`navigation_schema.go:392-397`).
- `Kind` distinguishes local from remote; current literal values are `"local"`
  (`web_api_tree.go:710`) and `"appwire"` (`web_api_tree.go:723`). Add a
  host-remote value only if the frontend needs to distinguish "attached host"
  from any future non-host appwire source; otherwise reuse `"appwire"`.
- `Online` must be `true` only while the SSH connection manager reports the
  host attached and healthy; `false` for a configured-but-downtable host.
- `Label` is bounded by `maxNavigationLabelRunes` (`navigation_schema.go:394`);
  host labels are short, so truncation is a safety net, not a design point.
- The manifest `sources` array is capped at 64 and each element must have
  exactly `{id,label,kind,online}` (`navigation_schema.go:367-375`); the
  frontend mirror enforces the same (`stores/navigation/codec.ts:272-281`,
  `stores/navigation/store.ts:303-307`). Do not add fields without changing
  both validators.

### Row contract

`hubapi.NavigationSessionSummary.HostID` (`hubapi/navigation.go:169`) is the
existing per-row host identity and is required by schema validation
(`navigation_schema.go:411-413`). It is already populated from the row ref in
`navigation_projection.go:1093` (`HostID: ref.HostID`). This component must
ensure remote rows arrive with a ref whose `HostID` equals the host source ID —
which the remote-source normalization already does when it backfills
`thread.Source`/`thread.Evener.Ref` (`web_api_tree.go:491-499`).

### Write contract (session targeting)

There is no source/host field on the start request today:
`appwire.ThreadStartParams` (`appwire/types.go:1427-1437`) has no `ref`/`source`,
and the generated `ThreadStartParams` matches (`frontend/src/protocol/
types.gen.ts:1855-1865`). This component adds an explicit source selector to
`thread/start` (proposed: `ref` or `source`, see Open questions) rather than
overloading `Harness`.

## Implementation approach

### What already exists (this component is small because of it)

The multi-source plumbing is already built and tested; production registers
only `local`, so the fan-out currently degenerates to one source.

- **Source registry**: `appsource.Registry` with `Add`/`Source`/`All`/
  `SourceForRef` (`cmd/evener-hub/internal/appsource/registry.go:20-64`).
  `Source` interface is `ID()` plus thread/turn methods only
  (`appsource/source.go:15-44`) — there is **no connectivity method today**.
- **Production registration**: `newHubSourceRegistry`
  (`cmd/evener-hub/app_rpc.go:24-74`) adds exactly one source, `"local"`
  (`app_rpc.go:26`). Component 05 will register one source per host here.
- **Fan-out + merge**: `hubThreadListWithSourceTimeout`
  (`cmd/evener-hub/app_threadlist.go:20-138`) already runs every allowed source
  concurrently (4 workers, `app_threadlist.go:16-18`), sorts results back into
  source order, merges rows, folds local past-index entries, applies
  `SearchTerm`/`Statuses`/`SourceIDs` filtering post-merge
  (`app_threadlist.go:274-306`), and degrades a non-explicit source error to a
  skip — only a source named in `SourceIDs` turns an error into a response
  error (`app_threadlist.go:88-93,191-200`).
- **Background snapshot**: `refreshRemoteThreadSnapshot`
  (`web_api_tree.go:460-528`) walks every non-local source, backfills
  `thread.Source`/`thread.Evener.Ref`, records per-source completeness and
  conflict IDs, and stores into `hubcore.RemoteThreadCache`
  (`internal/hubcore/remotecache.go:14-116`). The refresher runs on a 30s
  ticker + poke (`main_background.go:87-114`), wired in
  `main.go:326-330,403,503`; cache changes invalidate navigation
  (`main.go:426`). Last-known-good per-source results are retained on transient
  errors (`web_api_tree.go:539-597`).
- **Tree ingestion**: `navigationSnapshotInputs` folds cached remote threads
  into the same `metas`/`live` inputs as local sessions
  (`web_api_tree.go:307-321`).
- **Manifest source list**: `apiTreeSources`
  (`web_api_tree.go:706-728`) is consumed by `navigation_service.go:1357` and
  projected via `navigationSources` into `NavigationManifest.Sources`
  (`navigation_projection.go:174`, `415-422`).

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
   (`web_api_tree.go:706-728`): replace `Online: true` at lines 711 and 724
   with the source's online state (local always true; remote from the new
   interface). This is the one hardcoded/local-only source list the design
   calls out.
3. **Dormant marking for offline-host rows**: `NavigationSessionSummary.Dormant`
   is projected from `node.Dormant` (`navigation_projection.go:1105`), and
   `Dormant` is set for local past sessions in `hubcore/tree.go:1151,1383,1479`.
   For an offline host, last-known remote rows are not live
   (`Live` is computed from the live inputs at `navigation_projection.go:1103`,
   `1137-1138`), but nothing currently sets `Dormant` for them. Set `Dormant`
   on folded remote rows whose source is offline, at the point remote threads
   are ingested (`web_api_tree.go:307-321`) or in the tree/projection seam —
   pick the seam that keys off source identity, not the row's own state.
4. **Session targeting in `thread/start`**:
   - `hubThreadStart` (`app_threadlifecycle.go:49-60`) currently selects a
     source via `launchSourceID(params)` (`app_threadlifecycle.go:310-319`),
     which treats the **`harness`** value as a source ID (returns `"local"` for
     `"evener"`, the harness string otherwise, `""` for empty). Add an explicit
     source ref field to `ThreadStartParams` and resolve it here, ahead of the
     harness fallback. Target the named source via `sources.Source(id)` and
     call `source.StartThread(ctx, params)` (the existing branch at
     `app_threadlifecycle.go:54-60`).
   - Note: the ref-default path the brief referenced is `sourceForThread`
     (`app_sources.go:15-39`), which defaults to source `"local"` when no ref
     is given (`app_sources.go:31-34`). That function is used by `turn/start`
     and the other ref-resolving RPCs (e.g. `app_rpc.go:691-708` resolves
     `resolveTurnStartSource`, aliased to `sourceForThread` at `app_rpc.go:77`),
     not by `thread/start`. `thread/start` is the path that must change to honor
     a host picker.
   - AppWire param naming, wire generation, and the request handlers that
     construct `ThreadStartParams` (`app_rpc.go` registration) must be updated
     together; regenerate the frontend `types.gen.ts` (this is the
     `make generate` path — see Non-scope note; the implementation PR runs it).

### Frontend changes (high level)

- **New-session form**: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx`
  (form component `SpawnForm`, mounted at `/new`; see `Spawn.tsx:313-327` and
  the form body `1485-1882`). Add a host selector near the working-directory /
  settings cluster, local preselected. Persist the choice in the spawn draft
  (`panes/spawn/spawnDrafts.ts`) so it survives navigation like other fields.
- **Start seam**: `panes/spawn/startThread.ts` — `SpawnRequest`
  (`startThread.ts:12-27`) has no host field; add one and pass it into the
  `thread/start` params in `startThread` (`startThread.ts:45-55`), which is the
  canonical launch seam. `panes/spawn/schema.ts` may need the new field's
  default only if it participates in the effective launch-config preview.
- **Source list source**: the manifest already carries `sources` and the
  navigation store already holds it (`stores/navigation/store.ts:140-141`,
  validated at `store.ts:303-307` and `stores/navigation/codec.ts:272-281`), but
  nothing renders or selects from it in production. Add a selector, e.g.
  `stores/navigation/selectors.ts`, exposing `manifest.data.sources`, and let
  the spawn form consume it. Generated `Source` type is
  `protocol/types.gen.ts:1580-1585`.
- **Host label on rows**: `NavigationSessionSummary.host_id` is generated
  (`types.gen.ts:1209`) and populated server-side. The rail render seam is
  `shell/rail/Rail.tsx` / `shell/rail/railNodes.ts` (session presentation
  contract at `railNodes.ts:13-45`). Add a host badge/tooltip for rows whose
  `host_id` is not `"local"`, and a dormant/offline visual for dormant rows.
  Keep this to a label, not a tree re-layout.
- **Action gating**: a session row on an offline host must not offer host
  actions. The server-side `Rename` flag already excludes non-local rows
  (`web_api_tree.go:220,244,1092-1093`), so at minimum the read-only host rows
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
appsource.Registry.Add(remoteHubSource per host)   app_rpc.go:24-74
        │
        ├── thread/list  → hubThreadListWithSourceTimeout  app_threadlist.go:20-138
        │                     (fan-out 4 workers, 3s/source, merge, filter)
        │
        └── background refresh (30s + poke)  main_background.go:87-114
                 │ refreshRemoteThreadSnapshot  web_api_tree.go:460-528
                 ▼
            hubcore.RemoteThreadCache  (last-known-good per source)
                 │
                 ▼
      navigationSnapshotInputs folds rows into metas/live  web_api_tree.go:307-321
                 │
                 ▼
      manifest.Sources ← apiTreeSources()   web_api_tree.go:706-728
      rows.HostID      ← ref.HostID         navigation_projection.go:1093
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
  last-known-good rows (`web_api_tree.go:539-597`) and marks the source
  `Online:false`; the manifest still lists the host. `thread/list` skips a
  failed non-explicit source (`app_threadlist.go:88-93`), so one dead host does
  not fail a fleet-wide list. A request that explicitly names the dead host in
  `SourceIDs` should return the source error.
- **Unreachable host, action path**: host-targeted actions must be refused with
  a typed, actionable error — not a silent drop or a generic 500. Prefer
  `appwire.Unavailable(...)` ("host <name> is offline; reconnect to run this
  action") consistent with existing source-unavailable errors
  (`app_threadlifecycle.go:57`, `app_sources.go` error paths). Actions through
  `sourceForThread` (`app_sources.go:31-34`) already default to `local`; a
  remote ref on an offline host resolves to the registered-but-offline source,
  so the refusal belongs in the source/connection layer and must surface
  unchanged.
- **Start on an offline host**: the picker must disable offline hosts; if a
  request still arrives, `hubThreadStart`'s source branch returns
  `Unavailable("spawn source is not available: <id>")`
  (`app_threadlifecycle.go:54-58`) — confirm and keep that contract.
- **Source removed / unknown ref**: `Registry.SourceForRef` returns
  `source not found: <id>` (`registry.go:54-63`); `sourceForThread` maps a
  parse failure to InvalidParams (`app_sources.go:24-27`). Reconnect/removal is
  owned by Components 03–05; this component only consumes the state.
- **Schema bounds**: keep `Online` a plain bool, `Kind`/`ID` valid identities,
  and `Label` bounded; both Go (`navigation_schema.go:392-397`) and frontend
  (`codec.ts:272-281`) validators reject malformed sources.

## Testing

- **Go unit**
  - `apiTreeSources` truthfulness: with a registered stub source whose
    `Online()` is false, the manifest reports `Online:false` and `local` stays
    true. Extend the existing coverage tests that already call
    `apiTreeSources` (`web_covtest_test.go:451-457`,
    `cov_session_tree_pass3_fuzz_test.go:115`).
  - Fan-out merge: a mid-list failing non-explicit source degrades to a skip;
    the same source named in `SourceIDs` returns the error
    (extend `app_threadlist.go` tests / `cov_threadlife_list_pass6_fuzz_test.go`
    which already exercises `SourceIDs`/`SearchTerm`).
  - Dormant marking: an offline source's last-known rows project with
    `HostID` set and `Dormant:true`; an online source's rows do not.
  - `thread/start` targeting: explicit source routes to that source's
    `StartThread`; missing source returns the existing unavailable error; empty
    source still defaults local. Extend `app_rpc_test.go` which already stubs
    `resolveTurnStartSource` (`app_rpc_test.go:10052-10090`).
  - Manifest schema: sources array still validates under
    `navigationManifestValuesValid` (`navigation_schema.go:392-397`).
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

- **Wire field shape for targeting**: add `source`/`host` (a bare source ID) to
  `ThreadStartParams`, or reuse the existing `ref` on related params (e.g.
  `ThreadReadParams.Ref`)? A bare `source` reads best for "run this new session
  on host X" and avoids implying an existing thread. Needs a decision before
  touching generated code.
- **`Kind` value for hosts**: reuse `"appwire"` or introduce a host-specific
  value? The frontend cannot currently tell an attached host from any other
  appwire source; if grouping/labels need that distinction, pick a value now to
  avoid a second schema change. (Both validators must change together.)
- **Rail grouping by host**: this spec deliberately stops at a picker plus a
  row badge. If users want per-host collapsible sections, that is a larger
  rail/tree reshape and should be its own spec/PR.
- **Online semantics**: what counts as online — a live SSH channel, a
  successful capability probe, or both? The exact signal is owned by Component
  04; this spec only requires that `apiTreeSources` can read a boolean and that
  a transition invalidates navigation (`main.go:426` already does this on cache
  changes; a connection-state change must also poke).
- **Dormant vs `Live` for offline rows**: confirm no path sets `Live:true` for
  an offline host's stale rows (`navigation_projection.go:1103,1137-1138`
  compute `Live` from the live input set). If stale remote rows are pushed into
  `snapshot.live` (`web_api_tree.go:318-320`), the offline filter must run
  before or inside the projection, not after.
- **Unknown / partially verified**: I did not run the build or the frontend
  suite, so `ThreadStartParams` generation and rail CSS class names are
  unverified; the exact frontend module list may shift by one or two files.
