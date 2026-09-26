# Component spec 05 — Remote hub source (`appsource.Source`)

Parent: `2026-09-14-multi-host-evener-design.md`. Depends on components 01
(stream transport), 02 (attach bridge), 03 (host config), 04 (SSH connection
manager). This is the highest-risk component; §Contract's method-coverage
analysis is the keystone deliverable.

**Citation convention.** Symbols (method constants, handler functions, files)
are authoritative and were verified on `multi-host-pr05a..d` and
`multi-host-pr06a-fleet-view-go`, both since landed on `origin/main`. Catalog
and handler line numbers are omitted deliberately because they shift as methods
are added; a `*.md:NNN` reference, where it survives, is a hint, not pinning.

## Purpose

Expose a remote `evener hub` as one more `appsource.Source` on the controller
hub, so every existing thread/turn RPC handler in `cmd/evener-hub` (which is
written once against the `Source` interface) works against a session running on
another host without touching those handlers.

The remote hub is a full hub, not a daemon. A controller attach is a client of
that hub's existing AppWire edge, reached through the add-bridge stdio channel
(components 01/02) over an SSH connection (04). `RemoteHubSource` maps each
`Source` call onto the matching **hub-scoped** AppWire method, and translates
refs between the controller's `host:<thread>` namespace and the remote hub's
`local:<thread>` namespace.

## Scope

- A new `appsource.Source` implementation, working name `RemoteHubSource`, in
  `cmd/evener-hub/internal/appsource/`.
- Mapping of all 30 `Source` methods onto wire methods that already exist on the
  remote hub's router (see the coverage table in §Contract).
- Ref translation in both directions, **recursively through every nested
  structure that carries a session ref**:
  - request params: `host:<thread>` / bare thread ID → `local:<thread>`;
  - responses and notifications: remote `local:<thread>` → `host:<thread>`,
    including `Thread.Evener.Ref`, `Thread.Evener.ParentRef`, `Thread.Source`,
    and nested `Thread` snapshots carried by `thread/started`
    (`appwire.NotifyThreadStarted`, `appwire/types.go`);
  - sub-thread (read-only alias) refs and parent refs;
  - the `Thread.Evener.Diagnostics` block on a thread snapshot
    (`EvenerDiagnostics.Jobs[].TranscriptRef`,
    `EvenerDiagnostics.Delegates[].TranscriptRef` —
    `EvenerJobInfo`/`EvenerDelegateInfo`, `appwire/types.go`) and the whole
    `ListJobs` result tree (`appwire.JobActivityTree`: `JobActivitySession.Ref`
    and its `Entries[]`; each `JobActivityEntry`'s `Job`/`Delegate` —
    `JobActivityJob.OwnerRef`/`.TranscriptRef` and
    `JobActivityDelegate.ChildRef`/`.Turns[].OwnerRef`/`.Turns[].TranscriptRef`,
    plus the **recursive** `JobActivityDelegate.Child *JobActivitySession`
    (walk `Tree.Root.Entries[]` → `Delegate.Child.Entries[]` → `Turns[]`) —
    `appwire/types.go`).
- Subscription: `SubscribeThread` drives the remote hub's `thread/read`
  (`subscribe:true`) live feed over the single channel and fans notifications
  out per ref, reference-counting per remote thread and sending the remote
  hub's `thread/unsubscribe` when the last local subscriber leaves.
- Registration of one `RemoteHubSource` per configured host in the production
  registry (`cmd/evener-hub/app_rpc.go`), with the default source for an
  empty ref remaining `local` (`cmd/evener-hub/app_sources.go`).
- Capability probe after attach, over the already-open channel.
- Error mapping to the hub's existing typed conditions (offline → session
  unavailable, version mismatch → typed protocol error).

## Non-scope

- Spawning `ssh`, keepalive, reconnect, deploy, version auto-match: component 04.
- The bridge process / stdio proxy: component 02.
- Stream framing: component 01.
- Host config schema and cycle rejection: component 03.
- Fleet fan-out, host picker UI, offline/dormant presentation: component 06.
- Per-host settings proxying and credential push: component 07.
- **Remote item paging is required, not deferred: the fallback cannot page.**
  `appsource.ItemCandidateSource` / `ItemReadCandidateSource`
  (`cmd/evener-hub/internal/appsource/source.go`) must be implemented by
  `RemoteHubSource`. The legacy fallback
  (`sourceItemCandidateResultForRead` / `sourceItemCandidateResultForList`,
  `cmd/evener-hub/app_item_page_fit.go`) produces a candidate window with a
  **zero** `Identity`, and the packer refuses to emit a continuation cursor it
  cannot re-encode (`legacy transcript item source cannot page without cursor
  identity`, `app_item_page_fit.go:229-243`) whenever the response carries a
  non-empty `OlderCursor`/`NextCursor`. A remote hub packs its own replies, so
  every multi-page remote read/list carries one. See §"Remote item paging
  requires a source-owned cursor identity".
  `CombinedItemReadSource` (`ReadThreadWithItemCandidates`) is **not**
  required: the packer consumes the already-materialized response through
  `ItemCandidatesFromRead`. This bullet supersedes the earlier
  "so v1 is correct without them / adding them is a later optimization" claim,
  which was wrong for a source whose replies carry a cursor.
- Atomic relay handoff (`appsource.RelaySessionSource`,
  `cmd/evener-hub/internal/appsource/source.go`). v1 uses the legacy
  read-then-subscribe path (`cmd/evener-hub/app_relay.go`); see §Data
  flow and §Open questions.
- Remote *tool execution* (`agent/execenv`): non-goal of the parent design.
- Attach-time cross-hub cycle detection: v1 learns no remote host list, so it
  cannot supply component 03's `AddWithUpstreams` upstream names. Explicitly
  deferred — see §Open questions item 4.

## Contract / interfaces

### The interface being implemented

`Source` is declared at `cmd/evener-hub/internal/appsource/source.go`:
`ID`, `ListThreads`, `ReadThread`, `ListTurns`, `StartThread`, `ResumeThread`,
`ForkThread`, `StartTurn`, `SteerTurn`, `ResolveSandboxEscalation`,
`InterruptTurn`, `QueueTurn`, `DrainAsSteer`, `PromoteQueuedAsSteer`,
`CancelQueued`, `CompactThread`, `ShutdownThread`, `SetThreadModel`,
`SetThreadReasoningEffort`, `SetThreadVisionModel`, `SetThreadName`, `GoalSet`,
`NotesHumanSet`, `UrlsRemove`, `ClearThread`, `ListModels`, `ListTasks`,
`ListJobs`, `JobOutput`, `SubscribeThread`.

The registry stores sources by `ID()` and resolves a ref's source by its
`SourceID` (`cmd/evener-hub/internal/appsource/registry.go`). A source's
`ID()` is therefore the host `name` from the host config entry (parent design
§5), and appears verbatim in controller refs and `hubapi.Ref.HostID`
(`hubapi/refs.go`).

Wire refs are `SourceID + ":" + ThreadID`, pattern
`^[A-Za-z0-9._~-]+$` (`appwire/refs.go`). The remote hub's own source ID is
`"local"`: `ServerConfig{SourceID: "local"}` (`cmd/evener-hub/app_rpc.go`)
and the registered local source is `NewLocalDaemonSourceWithEntries("local", …)`
(`cmd/evener-hub/app_rpc.go`). So the remote namespace is always `local:`.

### Method-coverage analysis (core deliverable)

`Source` was written against daemon-scoped calls: `LocalDaemonSource` dials each
daemon's WebSocket endpoint (`cmd/evener-hub/internal/appsource/local_daemon.go`)
and never talks to the hub's own router. The remote source instead attaches to a
**hub**, so the relevant question is which of its calls have a method on the
hub's AppWire router.

Method scopes are catalogued at `appwire/protocol.go` (`ScopeHub`,
`ScopeDaemon`, `ScopeBoth`, `ScopeConnection`, `ScopeUnimplemented`); the
`Methods` catalog is `appwire/protocol.go`. The hub router is
cross-checked to register every `ScopeHub` **and** `ScopeBoth` method
(`cmd/evener-hub/appwire_catalog_test.go`, `want :=
appwire.CatalogMethodNames(appwire.ScopeHub)`). Because a `ScopeBoth` method is
handled by the hub by proxying to its local source, **every method the `Source`
interface needs is already served by the remote hub's router.** No new
hub-scoped thread/turn RPC is required.

The table cites **method constants** (`appwire/types.go`) and **handler
symbols** (`cmd/evener-hub/`). Catalog line numbers are deliberately omitted:
the catalog shifts as methods are added, and the reviewer's base (`origin/main`)
is not the branch this spec was written against. Where a handler is registered
inline inside a registration function, the registration function is named.

| `Source` method | Wire method constant | Scope | Remote-hub handler | Disposition |
|---|---|---|---|---|
| `ID` | — | — | — | local; returns host name |
| `ListThreads` | `MethodThreadList` | `ScopeBoth` | `hubThreadList` (`app_threadlist.go`) | forward; **remap `SourceIDs`**, translate refs |
| `ReadThread` | `MethodThreadRead` | `ScopeBoth` | inline in `registerThreadHandlers` (`app_rpc.go`) | forward; translate refs, **including the nested `Thread.Evener.Diagnostics` job/delegate refs**, **rewrite image URLs** (§"Image URLs are host-scoped and must be rewritten through the controller"), and implement `ItemCandidatesFromRead` (§"Remote item paging requires a source-owned cursor identity") |
| `ListTurns` | `MethodThreadTurnsList` | `ScopeBoth` | inline in `registerThreadHandlers` | forward; **implement `ListItemCandidates`** — the controller cursor is decoded and translated to the remote hub's native cursor here (§"Remote item paging requires a source-owned cursor identity") |
| `StartThread` | `MethodThreadStart` | `ScopeHub` | `hubThreadStart` (`app_threadlifecycle.go`) | forward; **strip the controller-only `Source` field**; remote hub spawns on the host |
| `ResumeThread` | `MethodThreadResume` | `ScopeHub` | `hubThreadResume` (`app_threadlifecycle.go`) | forward; translate refs |
| `ForkThread` | `MethodThreadFork` | `ScopeHub` | `hubThreadFork` (`app_threadlifecycle.go`) | forward |
| `StartTurn` | `MethodTurnStart` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `SteerTurn` | `MethodTurnSteer` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `ResolveSandboxEscalation` | `MethodEvenerSandboxEscalationResolve` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `InterruptTurn` | `MethodTurnInterrupt` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `QueueTurn` | `MethodTurnQueue` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `DrainAsSteer` | `MethodTurnDrainAsSteer` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `PromoteQueuedAsSteer` | `MethodTurnPromoteQueuedAsSteer` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `CancelQueued` | `MethodTurnCancelQueued` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `CompactThread` | `MethodThreadCompactStart` | `ScopeBoth` | `compactThreadWithResume` (inline in `registerThreadHandlers`) | forward |
| `ShutdownThread` | `MethodThreadShutdown` | `ScopeBoth` | `shutdownThreadTolerateExited` (inline in `registerThreadHandlers`) | forward |
| `SetThreadModel` | `MethodThreadModelSet` | `ScopeBoth` | `setThreadModelWithResume` (inline in `registerThreadHandlers`) | forward |
| `SetThreadReasoningEffort` | `MethodThreadReasoningEffortSet` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
| `SetThreadVisionModel` | `MethodThreadVisionModelSet` | `ScopeBoth` | `setThreadVisionModelWithResume` (inline in `registerThreadHandlers`) | forward |
| `SetThreadName` | `MethodEvenerThreadNameSet` | `ScopeBoth` | `registerThreadNameSetHandler` (`app_rename.go`) | forward |
| `GoalSet` | `MethodGoalSet` | `ScopeBoth` | `setGoalWithResume` (inline in `registerThreadHandlers`) | forward |
| `NotesHumanSet` | `MethodNotesHumanSet` | `ScopeBoth` | `setNotesHumanWithResume` (inline in `registerThreadHandlers`) | forward; translate `Ref` |
| `UrlsRemove` | `MethodUrlsRemove` | `ScopeBoth` | `removeURLWithResume` (inline in `registerThreadHandlers`) | forward; translate `Ref` |
| `ClearThread` | `MethodThreadClear` | `ScopeBoth` | `clearThreadWithResume` (inline in `registerThreadHandlers`) | forward |
| `ListModels` | `MethodModelList` | `ScopeBoth` | `hubModelList` (`app_models.go`) | forward |
| `ListTasks` | `MethodEvenerTasksList` | `ScopeBoth` | `hubTasksList` (`app_tasks.go`) | forward |
| `ListJobs` | `MethodEvenerJobsList` | `ScopeBoth` | `hubJobsList` (`app_jobs.go`) | forward; **recursively translate the `JobActivityTree` session refs** |
| `JobOutput` | `MethodEvenerJobsOutput` | `ScopeBoth` | `hubJobsOutput` (`app_jobs.go`) | forward |
| `SubscribeThread` | `MethodThreadRead` with `Subscribe:true` + notifications | `ScopeBoth` | inline in `registerThreadHandlers` + relay | **compose** over the channel |

Methods the hub also serves that are *not* on `Source` but that component 06/07
will want split into two groups by how they are routed once a host is selected.
The `Method*` constants live in `appwire/types.go`; the catalog rows live in
`appwire/protocol.go`.

**Host-selection calls — forwarded through `evener/host/request`.** The
path/dir/project/git helpers registered by `registerMiscHandlers`
(`MethodEvenerPathsComplete`, `MethodEvenerDirsCreate`,
`MethodEvenerProjectsRecent`, `MethodEvenerPathValidate`,
`MethodEvenerGitHead`) plus the admin reads that resolve against the machine
running the handler — the plugin preview (`MethodEvenerPluginPreview`, wire
`evener/plugin/preview`, `app_plugins.go`), the provider-instance list
(`MethodEvenerInstanceList`, wire `evener/instance/list`, `app_instances.go`) —
and the remaining spawn-form discovery reads
(`MethodEvenerHarnessesList`, `evener/harnesses/list`;
`MethodEvenerSpawnSlashCatalog`, `evener/spawn/slashCatalog`) take no ref, so
naming the host is the **only** way to scope them. They are forwarded as normal
hub-scoped methods through the host-scoped request envelope
`evener/host/request` (component 07, §"Proxy method"), which carries the
selected host and method name — **not** through `RemoteHubSource` (none of
these five-plus calls is on the `Source` interface: `MethodEvenerPathsComplete`,
`MethodEvenerDirsCreate`, `MethodEvenerProjectsRecent`,
`MethodEvenerPathValidate`, `MethodEvenerGitHead`, `MethodEvenerPluginPreview`,
`MethodEvenerInstanceList`, `MethodEvenerHarnessesList`,
`MethodEvenerSpawnSlashCatalog`). This is the routing component 06's spawn-form
discovery calls use, and the exact set the component-07 allow-list must carry
(component 06, §"Frontend changes"; component 07, §"Proxy method").
`MethodModelList` (`model/list`, `ScopeBoth`) is **also in this host-selection
set** — it takes no ref, so the spawn form's host-scoped model discovery must
route it through `evener/host/request` too — but it is additionally reachable
as `Source.ListModels` on `RemoteHubSource` (table above), because a thread on
a known host must be able to list that host's models over the shared client
without a second routing seam. The two routings are not in conflict: the
allow-list entry serves host-selection callers with no ref, the source method
serves ref-owning callers (thread model selection). An implementer who reads
the allow-list as exhaustive for `model/list` would silently drop
`RemoteHubSource.ListModels`; component 06/07's single-shared-source-of-truth
parity requirement is written over the union, not over the allow-list alone.
The launch resolution the spawn form also needs (`MethodEvenerLaunchResolve`,
`evener/launch/resolve`, `ScopeHub`) is already in the launch admin family
(component 07, §"Proxy method"), so it is not repeated here.

**Ref-dispatched calls — served by the controller, never forwarded.** These
already reach the right host through the *ref*, so they must not be sent
through `evener/host/request`:

- `MethodEvenerThreadTranscriptsList` (`evener/thread/transcripts/list`) is
  `hubThreadTranscriptList` (`app_transcripts.go`), which resolves the owning
  source through the registry (`hubTranscriptRootForList`) and reads the remote
  thread through it.
- `MethodEvenerSubagentPreview` — the wire string is `evener/subagentPreview`,
  **not** `evener/subagent/preview` (`ScopeHub`, inline in
  `registerThreadHandlers`) — resolves `sourceForThreadWithDeletionFence` and
  calls `source.ReadThread`, the same ref dispatch.
- `MethodThreadUnsubscribe` (`ScopeBoth`) is likewise ref-carried.

A raw pass-through proxy would re-address an already-reachable call — and, with
a `host:`-prefixed ref, hit the same refusal the local dispatch avoids — so
these stay on the plain controller connection with their translated ref.

**`MethodEvenerThreadForceStop` is neither — it needs dedicated dispatch.** The
wire string is `evener/thread/forceStop`, **not** `evener/thread/force-stop`
(`ScopeHub`, registered in `registerThreadHandlers` as `forceStopThread`).
`forceStopThread` (`app_force_stop.go`) refuses any ref whose `SourceID` is not
`"local"` (`appwire.InvalidParams("force stop requires a local session ref")`),
because it verifies and signals a **local daemon process** through the hub's own
`DaemonProcesses`/`ResumeLocks` ownership. A raw forward of a `host:<thread>`
ref therefore fails on the host hub too, and a forward of a bare
`local:<thread>` ref would target the wrong machine's daemon. Remote force-stop
needs dedicated ref-translating dispatch: the controller resolves the ref to
the owning host, translates it into that host's `local:` namespace, and issues
the force-stop **on that host's hub** (the hub that owns the daemon process),
mapping the response back to the controller ref. That is a separate requirement
defined by component 07 (§"Dedicated ref-translating dispatch
(`evener/thread/forceStop`)"), not a member of the `evener/host/request`
allow-list, and it updates the controller's `forceStopThread`
(`app_force_stop.go`) rather than adding a bare `evener/host/request` entry.

`MethodThreadTurnItemsList` (wire string `thread/turns/items/list`) is
`ScopeUnimplemented` — served by no evener router — and is not on the `Source`
interface; nothing maps to it.

**The one gap: OS/arch.** No AppWire method exposes host OS/arch. The closest
hub RPCs are `initialize` (`ServerInfo.Name/Version`, protocol version, features
— `appwire/types.go`) and `evener/settings/overview`
(`SettingsHubOverview.Version/Commit/BuildChannel/ListenAddr/RunDir`,
`appwire/types.go`); `evener/update/check` reports build/commit only
(`appwire/types.go`). The only "host facts" type in the tree is
`sandbox.HostFacts`, which is internal to the remote agent and never crosses
AppWire. So:

- **Option A (recommended):** the capability probe takes OS/arch from the
  component-04 SSH preflight (the same `uname`-style probe that decides which
  binary to push), and AppWire supplies the rest.
- **Option B:** add one new hub-scoped method, e.g. `evener/host/info`
  (`ScopeHub`), to `appwire/protocol.go` plus a handler in
  `registerMiscHandlers` (`app_rpc.go`), returning OS, arch, HOME/XDG,
  and working roots. Cost: one catalog entry + one handler + the cross-check
  test (`appwire_catalog_test.go`) updates itself; ~60 LOC. Defer unless the
  controller needs OS/arch without a live SSH probe.

Everything else maps cleanly; the real cost of this component is ref translation
and the subscription stream, not new RPC surface.

### Capability probe

After attach, `RemoteHubSource` runs a small sequence of calls on the current
client and caches the result **against that client** (`remoteHubProbe{client,
caps}`), so a reconnect's new client re-probes automatically
(§"Reconnect handoff"). Exported type and interface (in `appsource`, consumed by
the fleet view in component 06):

```go
type HostCapabilities struct {
    ProtocolVersion string
    HubVersion      string
    HubSourceID     string // "local"
    Features        appwire.FeatureSet
    OS, Arch        string // component 04 preflight, not AppWire
    LaunchGlobal    appwire.LaunchConfigLayer
    // per-root effective config: `Roots[i]` -> `evener/launch/resolve` result
    // for that root (probe row below); empty when a root has no effective layer
    LaunchResolved  map[string]appwire.LaunchConfigResolved
    Models          appwire.ModelListResponse
    Plugins         appwire.PluginListResponse
    Auth            appwire.AuthListResponse
    Instances       appwire.InstanceListResponse
    Roots           []string // from host config entry (component 03)
}

type CapabilitySource interface {
    HostCapabilities(context.Context) (HostCapabilities, error)
}
```

Probe calls and their types:

| Probe field | Call | Type |
|---|---|---|
| protocol / hub version / features | the attach handshake component 04 already performed (`InitializeResponse`, captured by component 04 and exposed via `Channel.Handshake()`; `appwire.Client` has no `Features()` accessor) plus the preflight facts — **not** a second `initialize` | `InitializeResponse` (`appwire/types.go`) |
| OS/arch | component-04 preflight (no RPC — see gap above) | — |
| effective launch config | `evener/launch/resolve` per root, result keyed by that root path | `LaunchConfigResolved` (`appwire/types.go`), stored in `HostCapabilities.LaunchResolved[root]` |
| global launch layer | `evener/launch/getLayer` `layer:"global"` | `LaunchConfigLayer` (`appwire/types.go`) — this method returns `LaunchConfigLayer`, **not** `LaunchConfigResolved` (`appwire/protocol.go:179`), matching `HostCapabilities.LaunchGlobal` |
| available models | `model/list` | `ModelListResponse` (`appwire/types.go`) |
| plugin inventory | `evener/plugin/list` (+ `evener/marketplace/list` if needed) | `PluginListResponse` (`appwire/types.go`) |
| credential/provider health | `evener/auth/list` (+ `evener/instance/list`) | `AuthListResponse` (`appwire/types.go`) |
| working roots | host config entry | — |

`evener/auth/list` reports configured/signed-in state, not liveness. A live
credential test (`evener/auth/test`, `protocol.go`) hits the network per
provider; keep it out of the attach probe and expose it as an explicit refresh.

**The probe reaches the handshake facts through a source-level seam, not a
`*Channel`.** `Channel.Handshake()` exists on the component-04 `Channel`, but
`RemoteHubSource` holds only the seams installed from `hubcore.WebConfig`, so
component 04 must also expose the same facts for a host by name: a
`RemoteHostHandshake func(host string, client *appwire.Client)
(appwire.InitializeResponse, bool)` seam (backed by
`remoteHostHandshakeForChannel`, built on `Manager.ChannelIfAttached` with the
client-identity guard at the call site; component 04, §"Go surface"), installed
as `SetHostHandshake` (§"Registration and default-source selection"). It carries
the client the probe resolved and reports `false` when the installed channel is
a different generation, so the probe cannot pair one connection's wire reads
with another's handshake (the shipped seam is the same shape as
`RemoteHostFacts`' generation guard).
Without it the probe cannot populate `ProtocolVersion`, `HubVersion`
(`ServerInfo`), `HubSourceID`, or `Features` for a `RemoteHubSource` — none of
the existing `RemoteHostClient`/`RemoteHostFacts`/`RemoteHostOnline`/
`RemoteHostClientIfAttached` seams returns handshake data.

`initialize` is `ScopeConnection` and "must be the first request" on a
connection (`appwire/protocol.go`), so the probe cannot re-run it on the
already-initialized channel that component 04 handed over. Component 04 must
therefore **expose the handshake facts**: `Ensure` captures the
`InitializeResponse` and the `Channel` returns it from a handshake accessor
(component 04, `Channel.Handshake()`) carrying `ProtocolVersion`,
`ServerInfo` (hub name/version), `SourceID`, and `Features`. `appwire.Client`
keeps its own `Features` copy privately and exposes no accessor, so the probe
must not look for one on the client. Preflight supplies OS/arch and its own
protocol/version for the version-match decision; the probe reads
protocol/version/source/features from the `RemoteHostHandshake` seam above (the
`Channel.Handshake()` value, reached by host name), and makes the five AppWire
reads in the table above and nothing else.

### Registration and default-source selection

Production registers exactly one source today (`cmd/evener-hub/app_rpc.go`)
and the hub advertises `SourceID: "local"` (`app_rpc.go`). Component 05 adds
one `registry.Add(...)` per configured host inside `newHubSourceRegistry`
(`cmd/evener-hub/app_rpc.go`), constructed as
`appsource.NewRemoteHubSource(host.Name, host.Roots, cfg.RemoteHostClient)`,
each with `ID()` = host name. Registration is **eager and
attachment-independent**: every configured host gets its source at hub startup,
whether or not an SSH channel exists yet. Nothing is added to or removed from
the registry on attach/detach — lifecycle events carry connection state only
(component 04, §"Client handoff"). That is what keeps the fleet view's host
list equal to the configured `[[hosts]]` set (component 06). Order is
irrelevant; `Registry.All` sorts by ID (`registry.go`).

The source's optional seams are installed once at registration, all from
`hubcore.WebConfig` fields the hub already fills (`cmd/evener-hub/main.go`):

- `SetHostOnline(RemoteHostOnline)` where `RemoteHostOnline` is
  `sshManager.Attached` — so an unattached host reports itself offline. The
  interface is `appsource.OnlineSource` (`{ Online() bool }`,
  `cmd/evener-hub/internal/appsource/online.go`, component 06a), and
  `RemoteHubSource` implements it. `Online() == false` is a *state*, never an
  absent source.
- `SetHostFacts(cfg.RemoteHostFacts)` — the component-04 preflight facts the
  probe needs (`HostFacts`, `remote_hub_probe.go`), backed by
  `Manager.ChannelIfAttached` (component 04, §"Go surface").
  `cmd/evener-hub/main.go` reads one
  `Manager.ChannelIfAttached` value and takes the preflight from the same
  channel whose client is the probe's `client`, refusing with a typed
  `SessionUnavailable` when it is a different generation — the attached-only
  form of the old `ch.Client() != client` guard, so the probe never caches a
  snapshot assembled from two connections. `Manager` stores live channels
  privately, so without this accessor `cmd/evener-hub/main.go` cannot construct
  `cfg.RemoteHostFacts` and `HostCapabilities.OS`/`Arch` stay permanently
  unpopulated (see §"Probe reaches the handshake facts…" above for the same
  shape on the handshake facts).
- `SetHostHandshake(cfg.RemoteHostHandshake)` — the attach handshake facts the
  capability probe needs (`ProtocolVersion`, `ServerInfo`, `SourceID`,
  `Features`), backed by `remoteHostHandshakeForChannel` in
  `cmd/evener-hub/main.go` (component 04, §"Go surface"), which reads one
  `Manager.ChannelIfAttached` value and applies the generation guard at the call
  site (`ch.Client() == client`). It returns the `InitializeResponse` for a host
  only while a live channel is installed and only when that channel's client is
  the probe's `client`, `(zero, false)` otherwise; the lookup itself takes the
  manager-wide mutex, so it is safe from the `EventAttached` callback exactly
  like `ClientIfAttached`. `Manager.HandshakeIfAttached` exists but has no
  production caller — the guard is deliberately not inside an accessor. Without
  this seam the probe cannot populate those four fields for a
  `RemoteHubSource` (see §"Capability probe").
- `SetHostClientIfAttached(cfg.RemoteHostClientIfAttached)` where
  `RemoteHostClientIfAttached` is `sshManager.ClientIfAttached` (component 04,
  §"Go surface") — the **non-dialing, attached-only client lookup**. It returns
  the current `*appwire.Client` only while a live channel is installed and
  reports `false` otherwise, without spawning `ssh` or attaching. The
  notification broker's reconnect rebind (§"One notification consumer per
  client") and component 06's background snapshot read the client through this
  accessor instead of the `Ensure`-backed `RemoteHubClientFunc`, so neither can
  eagerly attach a dormant host or re-dial one that dropped.

Without a facts seam the probe leaves the preflight-owned fields zero-valued; it
never guesses them from the wire.

An empty ref must stay local: `sourceForThread` returns
`sources.Source("local")` for `ref == ""` (`cmd/evener-hub/app_sources.go`).
Remote sources are reached only by a non-empty `host:` ref (via
`Registry.SourceForRef`, `registry.go`).

Spawn is the one place where "which source" is not derived from a ref.
`hubThreadStart` already routes a non-local spawn to
`sources.Source(sourceID)` (`cmd/evener-hub/app_threadlifecycle.go`).
Component 06 adds an explicit wire field, `ThreadStartParams.Source` (a bare
source ID), and `hubThreadStart` resolves it ahead of the legacy
`launchSourceID(params)` harness fallback (`app_threadlifecycle.go`);
the field is on the wire type, not on `RemoteHubSource`.

**Precedence and the legacy `Harness` fallback.** `ThreadStartParams.Source` is
the sole authority when set; the legacy `launchSourceID(params.Harness)` fallback
is consulted only when `Source` is empty (`app_threadlifecycle.go`:
`sourceID := strings.TrimSpace(params.Source); if sourceID == "" { sourceID =
launchSourceID(params) }`). That fallback maps `"evener"` to `local` and any
other non-empty harness string to a source ID, so a harness value that happens
to equal a configured host name would silently retarget the spawn to that host.
Component 06's write contract therefore requires that a harness value naming a
configured host source — or any other registered non-local source — be refused
with `InvalidParams`. The `launchSourceID` fallback is **retained** for every
other harness value and is consulted only when `Source` is empty; it is not
retired outright (component 06, §"Write contract (session targeting)").

**The host selector is controller-only and is stripped at the remote
boundary; the harness is the backend selection and is preserved.**
`ThreadStartParams.Source` names a source in the *controller's* registry; the
remote hub would resolve the same string against *its own* registry and fail
("spawn source is not available: <name>") or target the wrong source, so
`RemoteHubSource.StartThread` clears `Source` before forwarding (component 05c's
`remote_hub_mutations.go`). `Harness` must **not** be cleared or overwritten: it
is the caller's harness/backend selection, not a controller host selector — the
remote's own `launchSourceID` reads it to choose the backend — so replacing it
(clearing it, or the shipped `remote.Harness = "evener"`) silently starts the
remote's default backend instead of the one the caller picked. The controller
therefore forwards `Harness` **verbatim** (or translates it to the remote's
equivalent), having already refused a harness value that names a configured host
source (component 06, §"Write contract (session targeting)") so the legacy
harness-as-source fallback can never retarget the call to another host. Where
the remote offers no source for that harness, it fails with its own "spawn
source is not available" — an explicit error, never a silent substitution.

```go
remote := params
// Source names a source in the controller's registry and would be misresolved
// against the remote's, so it is cleared. Harness is the caller's backend
// selection and is forwarded verbatim; a harness naming a controller host was
// already refused controller-side (component 06).
remote.Source = ""
```

The controller's chosen host is expressed purely by *which*
`RemoteHubSource` handled the call; the remote hub sees a plain `thread/start`
with its own default routing when the harness is empty. No other `Source` method
carries a host selector: every other method addresses an existing thread by
`Ref`, which is translated (above). **Implementation status:** the shipped 05c
`StartThread` (`remote_hub_mutations.go:101-119`) clears `Source` and neutralizes
`Harness` to `"evener"`; forwarding the caller's harness rather than
overwriting it is the requirement, not a present fact.

**The receiving hub must reject a non-local resolution for a remote-originated
`thread/start`.** The controller-side harness refusal is bounded by the
controller's *own* registry: it cannot see the hosts configured on the remote.
So a harness value preserved verbatim that happens to name a host **the remote**
has registered — a host C configured only on B — reaches B's `hubThreadStart`
with `Source` cleared, falls into `launchSourceID(params.Harness)`, and resolves
to source C; B then routes the spawn onward to C, which is exactly the fan-out
the loop guard forbids and which the controller could not have detected. The
recipient hub therefore enforces the loop guard at the spawn seam too: for a
request whose routing-seam `origin` is non-empty (remote-originated),
`hubThreadStart` resolves **only** to `local` — a remote-originated
`thread/start` whose effective source (from a set `Source`, or from the legacy
harness fallback) would be any non-local source is refused typed
(`InvalidParams`) and must never be routed. The `launchSourceID` fallback
remains for local-originated requests under component 06's contract ("Write
contract (session targeting)"). **Implementation status:** neither the
`origin`-aware refusal nor the harness restriction exists today; it is a
requirement, and the code delta is a tracked follow-up (component 05a/06).

`hubThreadResume` already routes a non-local ref to its source
(`app_threadlifecycle.go`), so resuming a remote session by
`host:<session>` works without further plumbing.

### Reconnect handoff (component 04 → 05)

A component-04 reconnect swaps the ssh child, the transport, and the
`appwire.Client`. The source must survive that without being re-registered, and
it does so by construction:

- **Nothing is cached.** `RemoteHubSource` stores the `RemoteHubClientFunc`
  resolver, never a `*appwire.Client` (`remote_hub_source.go`): `call` resolves
  it on every request. Component 04 serializes each host's reconnect under its
  per-host lock and installs the replacement channel only when it is attached,
  so a resolution returns the old live client, the new one, or an error — never
  a half-swapped pair.
  **The handoff is unaffected by the attached-only rule**
  (§"Every other remote call is non-dialing, not just the snapshot and the
  non-explicit list"): the accessor returns whatever channel component 04
  currently has installed, so a reconnected host's new client is picked up on
  the next call exactly as before. The only change is what happens when nothing
  is installed — a typed unavailable error, where the old wiring would have
  dialed (that dial is now the explicit attach triggers' job) — so an
  in-flight call during a reconnect window refuses rather than reattaching.
- **The capability cache is keyed to the client.** The probe result is stored as
  `remoteHubProbe{client, caps}` and reused only while `probe.client == client`
  (`remote_hub_probe.go`); a new client re-probes on the next
  `HostCapabilities` call with no explicit invalidation hook, and `probeMu`
  serializes concurrent probes.
- **Subscriptions end with their client and are re-established by the relay.**
  `SubscribeThread` binds to the client it resolved (`registerSubscriber(sub,
  client)`) and starts exactly one drain goroutine per client
  (`ensureDrainLocked` → `drainLoop`), which exits when that client's
  `Notifications()` closes. `pumpSubscription` then closes the subscription's
  `out` channel, which the controller relay treats as subscription end and
  re-subscribes through its recovery path (`app_relay.go`). Re-subscription runs
  the resolver again and lands on the new client. Routing state (`subs`) and the
  drain set (`drains`) are guarded by `subMu`.
- **No re-init, no replay.** The source never calls `Initialize` (component 04
  owns the handshake) and never replays an in-flight call: it fails with the
  transport error mapped to `SessionUnavailable` (`mapCallError`,
  `transportUnavailable`). Recovery is the *next* call.
- **Laziness is the known limitation.** Handoff is on demand, not event-driven:
  between the drop and the next call (or the relay's re-subscribe) the source can
  still hold a dead client, and nothing proactively re-probes or restores
  subscriptions at the moment of reconnect. Consumers must therefore read
  `SessionUnavailable` as "retry now", not as a permanent verdict, and
  `Online()` (via `Manager.Attached`) as the only up/down signal — it flips
  without notifying the source, so `apiTreeSources` reads it per request.

### One notification consumer per client — the broker (05/07 boundary)

`appwire.Client.Notifications()` is a **single** channel (`appwire/client.go`),
and the client tears the whole connection down on buffer overflow
(`ErrNotificationOverflow`), so only one goroutine may read it. Component 05 is
already that reader: `drainLoop` (`remote_hub_subscription.go`) consumes the
stream and routes each notification by remote thread ID. Component 07 needs the
same stream for its config-notification fan-out (`evener/auth/updated`,
`evener/launch/updated`, …, component 07, §"PR 07a"). If each opened its own
`<-client.Notifications()`, the two would race and silently drop each other's
notifications — the failure is notification loss, not a compile error.

Rule: there is exactly **one** drain goroutine per per-host client, and it is a
**broker that fans the stream out to every consumer**, not a thread-only router.
Component 05 owns the drain (it already keys it to the client:
`ensureDrainLocked`/`drains`, `remote_hub_subscription.go`) and must expose a
**source-level** registration point — a method such as
`source.RegisterNotificationConsumer(fn)`, **not**
`registerNotificationConsumer(client, fn)` — so component 07 subscribes instead
of reading the channel. Two lifecycle requirements follow, and both are the
implementing PR's contract:

- **Registration starts the drain.** The drain must not start only from
  `SubscribeThread`. Remote administration can register a consumer with no
  thread subscribers at all (a settings-only session), and in that normal case
  nothing reads `Client.Notifications()`, so config notifications are never
  delivered. Registering a consumer must itself ensure the drain for the current
  client is running (`ensureDrainLocked` on registration), and the broker must
  start it whenever it resolves a fresh client. The acceptance test is an
  admin-only consumer registered **before** any thread subscription that still
  receives a notification.
- **Registration is at the source/host level, so a reconnect rebinds it.** A
  per-client registration is dropped with its client on reconnect, and component
  07 has no relay loop to re-register the way thread subscriptions recover
  through the relay's EOF detection. Registration is therefore against the
  source, which holds the consumer list and attaches every registered consumer
  to each fresh client's drain (the same per-call client resolution that
  re-probes capabilities). A consumer is removed by unregistering from the
  source, or is told its client ended, exactly as `subs`/`drains` are today
  (§"Reconnect handoff"). **A reconnect must not lose the gap.** Per-call
  resolution is fine for a *request-driven* source, but a notification consumer
  must not wait for the next call: notifications the replacement client emits
  between its installation and that next call would be dropped, leaving the
  fleet/admin views stale until something else happens to resolve the client.
  Component 04 emits `EventAttached` when it installs the replacement channel
  (component 04, §"Client handoff"), so the source must start the fresh client's
  drain — and re-bind every registered consumer — **on that event**, not only on
  the next resolution. Where a drain cannot be started (no event wired), a
  consumer must reconcile its state explicitly after a reconnect rather than
  assume no notifications were missed.
  **The event carries connection state only, not the replacement client**, so
  the rebind reads the fresh client through the **non-dialing attached-only
  lookup** `ClientIfAttached(host)` (component 04, §"Go surface"; wired as
  `SetHostClientIfAttached`, §"Registration and default-source selection"), not
  through `Ensure`. A rebind that called `Ensure` would attach every configured
  host on every reconnect and violate the lazy manager; `ClientIfAttached`
  returns `(nil, false)` for a host that is not currently attached, so a rebind
  on a detach/attach edge never dials a dormant host. Once it has the fresh
  client, the source starts that client's drain and attaches every registered
  consumer to it.
  **The rebind is lock-safe inside the `EventAttached` callback.** `OnEvent`
  runs with component 04's per-host gate held; `ClientIfAttached` reads the
  installed channel under the **manager-wide mutex**, not that gate, so calling
  it synchronously from the callback cannot self-deadlock (component 04,
  §"Client handoff"). That is what lets the rebind run on the event — the
  gap-free requirement — instead of deferring to the next call. Carrying the
  replacement client in the event itself is the equivalent alternative; either
  form is safe, and neither may call `Ensure`. The rebind is registered on the
  hub's **single `Options.OnEvent` fan-out** (component 04, §"Channel lifecycle
  states"), beside component 06's navigation poke: a second consumer is added to
  that fan-out, never by reassigning `Options.OnEvent` to one consumer's
  closure, or the rebind silently stops running and the reconnect gap this
  section forbids is lost.

A consumer callback must not be able to stall thread subscriptions: the broker
hands each consumer its own buffered channel or dispatches on its own goroutine,
because a slow admin consumer must not block a `turn/completed`.

Shipped today, `drainLoop` is the only reader and there is no registration
point, so the broker seam is the implementing PR's requirement. Component 07
must **not** call `Client.Notifications()` directly; it consumes this broker.

## Implementation approach

Files (all under `cmd/evener-hub/internal/appsource/` unless noted):

- `remote_hub_source.go` — the `RemoteHubSource` type, method implementations,
  and the coverage/forwarding helper.
- `remote_hub_refs.go` — ref translation (`toRemoteRef`, `fromRemoteRefString`,
  `fromRemoteThread`, `remapRemoteSourceIDs`, `translateOut`) plus the
  notification rewrite (`translateNotification`, in `remote_hub_subscription.go`).
- `remote_hub_probe.go` — `HostCapabilities` and the probe.
- `remote_hub_subscription.go` — the single-client notification fan-out.
- `cmd/evener-hub/app_rpc.go` — register remote sources in
  `newHubSourceRegistry` (one added loop).
- `cmd/evener-hub/app_sources.go` — unchanged; its local default is already
  correct.

Reused seams:

- Transport: `appwire.Transport` (`appwire/transport.go`) and the
  component-01 `StreamTransport` over the SSH channel. Component 04 hands the
  source a dial function shaped like
  `appsource.appwireDialFunc` (`cmd/evener-hub/internal/appsource/transport.go`),
  or a long-lived `appwire.Transport`.
- Client: `appwire.NewClient(transport)` (`appwire/client.go`),
  `client.Start`, `client.Initialize` (`client.go`), `client.Request`
  (`client.go`), and the notification stream
  (`client.Notifications()`, `client.go`). One `Client` safely serves
  concurrent requests and the notification feed: `Send` is mutex-guarded
  (`client.go`) and responses are correlated by request ID
  (`client.go`).
- Reference implementation for call mapping and error shape:
  `LocalDaemonSource` (`local_daemon.go`), especially
  `withClientCallMapper` (`local_daemon.go`), the dial-error mapping
  (`local_daemon.go`), and the mutation-unknown mapping
  (`local_daemon.go`). `RemoteHubSource` should mirror these shapes so
  the hub's auto-resume and mutation-retry gates keep working, but the strings
  should say "remote hub unavailable: <host>" rather than "local daemon".

Key difference from `LocalDaemonSource`: `LocalDaemonSource` opens a fresh
WebSocket per call (`withClientCallMapper`) except for relay sessions; a remote
host's SSH channel is expensive, so the controller keeps **one** live channel
(and therefore one `appwire.Client`) per host and every call goes over it. What
the source itself holds is not that client but the *resolver*:
`RemoteHubClientFunc` (`func(ctx context.Context, host string)
(*appwire.Client, error)`, `remote_hub_source.go`), wired by the hub to
`sshManager.Ensure` + `ch.Client()`. Every call re-resolves through it (`call` →
`client`), so a reconnect's fresh client is picked up automatically — see
§"Reconnect handoff". That client also carries the notification stream
`SubscribeThread` needs, which is why subscription is a fan-out rather than a
second connection.

**A background/snapshot caller must not force attachment.** Because the
resolver *is* `Ensure`, merely listing an unattached source dials it. Component
06's 30s background snapshot (`refreshRemoteThreadSnapshot`) therefore gates on
the source's attachment state (`Online()`/`Manager.Attached`) and skips an
unattached host rather than calling `ListThreads` on it; only a request that
genuinely targets the host reaches `Ensure`. Without this gate the ticker would
eagerly attach every configured host, contradicting the lazy manager
(component 04, §"Channel lifecycle states") and the attachment-based `Online`
(component 06, §"Read contract"). **The gate must not be check-then-`Ensure`.**
A snapshot that reads `Online() == true` and then calls a resolver wired to
`Ensure` can still attach: if the host disconnects between the check and the
call, `Ensure` dials it again, so a "non-dialing" snapshot reconnects or attaches
a host nobody asked for. Component 04 must therefore also provide an
**attached-only client lookup** — a `Manager` accessor that returns the current
client only while a live channel is installed, and reports "not attached"
without dialing (e.g. `ClientIfAttached(host) (*appwire.Client, bool)`) — and
component 06's snapshot must use that, not the `Ensure`-backed resolver, so the
check and the request cannot disagree. **Implementation status:** shipped.
`sshconn.Manager.ClientIfAttached` exists (component 04a) and is installed on
the source through `hubcore.WebConfig.RemoteHostClientIfAttached` /
`SetHostClientIfAttached`; `refreshRemoteThreadSnapshot`
(`web_api_tree.go`), and its synchronous `remoteThreadFetch` fallback, gate on
it and skip an unattached host without a call, carrying the host's
last-known-good rows forward instead of blanking them. `RemoteHubClientFunc` is
no longer what the source resolves calls through — the source resolves every
call through the attached-only lookup (see the next section) — so the gate and
the request cannot disagree.

**The same gate applies to the primary `thread/list` fan-out, not only the
snapshot.** A **non-explicit** fleet-wide `thread/list` (empty `SourceIDs`) must
run only against sources that are **already attached**, using the attached-only
lookup rather than the `Ensure`-backed resolver: an unattached host is skipped
without a call, exactly as a non-explicit source error already degrades to a
skip (`app_threadlist.go`). Otherwise simply opening the hub and listing the
fleet would resolve every `RemoteHubSource` through `Ensure`, dialing and
attaching every configured host — eager attachment that contradicts the lazy
manager (component 04, §"Channel lifecycle states"), the attachment-based
`Online` (component 06, §"Read contract"), and fleet-view acceptance criterion 9
("no other path attaches a host implicitly"). A source **named explicitly** in
`SourceIDs` is a deliberate, host-targeted request and may attach that host —
that explicit list is one of the intended attach triggers (alongside component
06's Connect action), the opposite of the implicit empty-filter fan-out. So the
rule is: empty filter ⇒ attached-only, no dial; explicit host in `SourceIDs` ⇒
may attach. **Implementation status:** shipped. `hubThreadListWithSourceTimeout`
(`app_threadlist.go`) skips a remote source for an empty filter when the
attached-only lookup reports it not attached, and attaches an explicitly named
remote host (`RemoteHostClient`, the `Ensure`-backed dial) before calling the
  source — one of the two shipped attach triggers, alongside the component-06
  Connect action (`evener/host/attach`, the browser-reachable explicit attach
  handler in `app_host_attach.go`).

**Every other remote call is non-dialing, not just the snapshot and the
non-explicit list.** The gate above covers the background snapshot and the
fleet-wide list, but the same `Ensure`-backed `RemoteHubClientFunc` also sits
behind every *direct* call the source serves — a `thread/read`, a
`thread/turns/list` or item page, a subscription add, `turn/start` or any other
mutation, a `host/request` proxy, a capability/launch-resolve probe. Resolving
any of those through `Ensure` silently reconnects a host the user did not ask
to connect and turns a refusal into a dial: an action against an **offline**
host must fail with a typed unavailable error (component 06, §"Error handling"
and acceptance criterion 13), not quietly attach the host and then succeed. So
the source resolves the client for **every** call through the shipped
attached-only accessor
`sshconn.Manager.ClientIfAttached(name) (*appwire.Client, bool)`
(`cmd/evener-hub/internal/sshconn/manager.go`, shipped by component 04a — the
same seam component 06's snapshot gate uses), and when it reports "not
attached" the source returns a typed unavailable error —
`appwire.SessionUnavailable("remote hub unavailable: <host>")`, mirroring
`LocalDaemonSource`'s dial-error mapping (`local_daemon.go`) so the hub's
auto-resume and mutation-retry gates keep working — without dialing.
`Ensure` is reserved for the **explicit attach triggers**: component 06's
Connect action (`evener/host/attach`), an explicit host named in
`thread/list`'s `SourceIDs` (which attaches at the fan-out seam *before* it
calls the source, so the source's own resolver still never dials), and the
first-attach bootstrap those two drive (component 04 §5). In particular a spawn
on an offline host is refused, not attached: component 06 disables an offline
host for a spawn and offers the Connect action instead, so `thread/start` never
reaches `Ensure` either. **Implementation status:** `ClientIfAttached` exists on
the manager (04a) and is now the source's resolver for *all* calls:
`RemoteHubSource.resolveClient` (`remote_hub_source.go`) answers from
`SetHostClientIfAttached` when installed (production), returning
`appwire.SessionUnavailable("remote hub unavailable: <host>")` for a host with
no live channel, and never dials. The wired `RemoteHubClientFunc` is only the
fallback for tests that inject a client function without the attached-only seam.
The capability probe reads its handshake facts (`ProtocolVersion`, `ServerInfo`,
`SourceID`, `Features`) through `hubcore.WebConfig.RemoteHostHandshake` /
`SetHostHandshake` (backed by the `remoteHostHandshakeForChannel` closure over
`Manager.ChannelIfAttached`, with the client-identity guard at the call site)
and its preflight facts through `RemoteHostFacts` / `SetHostFacts` (backed by
the `remoteHostFactsForChannel` closure over `Manager.ChannelIfAttached`). The
generation guard (`ch.Client() == client`) lives in those closures, not inside
`Manager.PreflightIfAttached` / `Manager.HandshakeIfAttached`, which have no
production caller.

Subscription lifetime is the other difference. `RemoteHubSource` must
**reference-count subscriptions per remote thread ID** and issue the remote
hub's `thread/unsubscribe` (`appwire/types.go`, a `ScopeBoth` method,
`protocol.go`, params `ThreadUnsubscribeParams` `appwire/types.go`)
only when the **final** local subscriber for that thread leaves. Without the
count, a re-subscribe that replaces an existing subscription would unsubscribe
the thread out from under the replacement, and without the unsubscribe the
remote hub keeps a live relay for every thread the controller ever read for as
long as the channel lives. One `thread/unsubscribe {Ref:"local:<thread>"}` goes
out on the shared client when the count reaches zero; the per-client
notification drain goroutine keeps running — it is scoped to the client, not to
one subscription, and stopping it would break every other subscription on the
host.

Ref translation detail (`remote_hub_refs.go`):

- Inbound params: for every params type with `Ref` and/or `ThreadID`, parse the
  controller ref (if non-empty), require `SourceID == s.id`, and rewrite `Ref`
  to `appwire.Ref{SourceID: "local", ThreadID: ref.ThreadID}`. If `ThreadID` is
  set without a ref, pass it through unchanged. This mirrors
  `localEntryForRefMode`'s source check
  (`local_daemon.go`) — a foreign `SourceID` must be refused, not
  silently retargeted.
- `ListThreads` needs special handling: the remote `hubThreadList` filters by
  `params.SourceIDs` (`app_threadlist.go`) and compares against
  `thread.Source` (`app_threadlist.go`). The remap rule is **identity-preserving
  on this source, never "empty means unfiltered"**:
  - an incoming `SourceIDs` that contains `s.id` is forwarded as `["local"]`
    (any entry naming another controller host is dropped, because only `local`
    is representable on the remote);
  - an incoming `SourceIDs` that does **not** contain `s.id` must not reach the
    remote at all. The controller's per-source fan-out gate — `sourceAllowedForList`
    (`app_threadlist.go`), which returns false for a non-empty filter that omits
    the source — is the mechanism; `ListThreads` must not be called in that case,
    and if it is, it must error rather than forward an empty filter;
  - an **empty** incoming filter (an unfiltered fleet-wide list) is forwarded as
    `["local"]`, **not** as empty. Forwarding empty would make the remote
    `hubThreadList` fan out to *all* of its own allowed sources — including
    nested remote hosts it is itself a controller for — which is transitive
    fan-out and request amplification, and would return non-`local` refs the
    single-level translation cannot represent. `["local"]` asks the remote for
    exactly its own local threads. A non-empty filter that remaps to empty is
    likewise never a licence to forward unfiltered.
  - **Nested-row handling is explicit.** With `["local"]` the remote cannot
    return a nested row (its filter compares `thread.Source` to `local`), so the
    reject in `fromRemoteRefString` is not reachable on the list path. If a
    non-`local` ref nonetheless appears on a returned row (a remote misbehaving,
    or a future remote), the row is **dropped** with a logged warning rather than
    failing the whole call: one unrepresentable row must not blank the host's
    fleet view.
  - **The controller-side loop guard is the v1 bound on fan-out depth.** The
    `["local"]` remap keeps the list path from recursing, and the controller
    additionally refuses to fan a request out to any remote source when the
    request itself arrived over a remote source (a controller attached to this
    hub as a host): such a request is served from local state only, and an
    attempt to route it onward is refused typed. That caller-identity guard
    terminates an A→B→A chain even though the config alone cannot detect it
    (design §2 "Topology"; §Open questions item 3). It is shipped: the refusal
    is enforced at the routing seam's two shared guards
    (`appsource.guardRemoteDispatch`, `cmd/evener-hub/host_routing_origin.go`'s
    `guardRemoteHostDial`), so no handler carries its own origin check.
  - **The origin signal is an explicit bridge marker on the
    connection, never `InitializeParams.ClientInfo`.** `ClientInfo` is
    caller-supplied and spoofable, and no origin/hop field exists today in the
    request context, `ThreadListParams`, or the `Source` interface, so the guard
    needs its own signal. The signal **cannot be the credential**: the attach
    bridge, the local TUI, CLI scripts, and browser sessions all present the
    **same host capability token** (`cmd/evener-hub/web.go`,
    `cmd/evener-hub/internal/hubedge/auth_token.go`,
    `cmd/evener-tui/internal/hubstart/hub_start.go`,
    `cmd/evener-hub/attach.go`), so no role can be derived from it.
    Classifying every token-bearer *local* disables the guard entirely (the
    attach bridge is local too, allowing unbounded A→B→A recursion); classifying
    every token-bearer *remote-originated* blocks local clients from fanning out.
    The hub's `/rpc` edge therefore reads a dedicated marker presented only
    by `evener hub attach --stdio`: the request header `X-Evener-Bridge: 1`
    (component 02, §Contract "Bridge marker"), read **alongside** the
    bearer token at accept/attach time. A connection presenting both a valid
    token and the marker is marked *remote-originated*; a token-bearer without
    the marker is *local*. **The header is client-asserted and carries no
    secret, so it is not verifiable:** any token-holder can set or omit it, and
    the v1 trust assumption is that peer hubs are cooperative — the marker
    terminates an honest A→B→A cycle but is not a boundary against a hostile
    peer. Binding the role to a server-verifiable signal (a distinct bridge
    credential, or the identity of the `ssh`-spawned transport instance) is a
    tracked code follow-up; until then the marker is cooperative-only.
    At accept/attach time the hub therefore marks the connection with its
    **role** (bridge marker present ⇒ *remote-originated*; marker absent ⇒
    *local*), and the routing seam stamps that role into the request context, so
    every handler can read `origin` (empty for a local request, non-empty for a
    remote-originated one). **Implementation status:** shipped. `evener hub
    attach --stdio` presents the marker (`cmd/evener-hub/attach.go`), the `/rpc`
    edge classifies the role and stamps it into the request context
    (`cmd/evener-hub/web.go`), and the guard refuses remote-originated remote
    dispatch at its shared seams: the `Ensure`-backed dial
    (`guardRemoteHostDial`) and the remote-hub client resolution every
    `RemoteHubSource` call passes through (`appsource.guardRemoteDispatch`), so
    a request arriving over a bridge cannot ride an already-attached source
    either. The trust basis above is unchanged: the marker is cooperative-only
    until the role is bound to a server-verifiable signal.
    The refusal is enforced at the **typed fan-out seam**, not by a check inside
    a handler: the multi-source fan-out (`hubThreadListWithSourceTimeout`,
    `app_threadlist.go`) — and any other path that routes a ref to more than one
    source — refuses to route a request whose `origin` is non-empty to any
    source other than `local` (the recipient hub's own local daemons),
    returning the typed refusal instead. The same `origin` bound applies at the
    **spawn seam**: a remote-originated `thread/start` may resolve only to
    `local`, so a preserved harness that names one of the recipient's own
    configured host sources cannot fan the spawn out to that host
    (§"The receiving hub must reject a non-local resolution for a
    remote-originated `thread/start`"). A remote-originated request is thus
    served from the **recipient hub's own local state** only and can never be
    fanned to another remote source, which caps fan-out at
    depth 1 and terminates A→B→A. Coverage: a scenario test that injects a
    remote-originated `thread/list` (and each other fan-out path) and asserts it
    is never routed to a second remote source, with the typed refusal surfaced;
    and a local-originated request still fanned normally.
  - **Implementation status:** shipped. `remapRemoteSourceIDs`
    (`remote_hub_refs.go:56-67`) returns `["local"]` for an empty incoming
    filter; `ListThreads` (`remote_hub_source.go:491-512`) answers an explicit
    exclusion that names no other source with an empty response instead of
    widening it to an unfiltered list; and `translateOut`
    (`remote_hub_refs.go:498-516`) drops the one unrepresentable row while
    keeping the valid rows beside it.
- Outbound threads: set `Thread.Source = s.id`; rewrite `Thread.Evener.Ref`
  from `local:X` to `s.id + ":" + X`; rewrite `Thread.Evener.ParentRef` the same
  way (sub-thread aliases). Leave `Thread.Evener.InstanceID` untouched: it is an
  opaque precondition token round-tripped into `turn/start.expectedInstanceId`
  (`appwire/types.go`), so it must stay exactly what the remote hub
  minted.
- **Nested response refs are translated recursively, not only at the top
  level.** Several response shapes carry session refs below their top level
  (three today), and each must be walked with no field left as a remote
  `local:<id>`:
  - the `ListJobs` result (`evener/jobs/list`, `JobsListResponse.Data` =
    `appwire.JobActivityTree`, `appwire/types.go`) embeds a recursive session
    tree. Match the actual schema: the recursion carriers are
    `JobActivityDelegate.Child *JobActivitySession` (a delegate's child session)
    and `JobActivityDelegate.Turns []JobActivityJob` (the delegate's turn-jobs).
    `JobActivitySession` itself holds only `Ref` and `Entries []JobActivityEntry`
    — it has **no** `Child` — and `JobActivityJob` holds only `OwnerRef` and
    `TranscriptRef` — it has **no** `Child` and **no** `Turns`. Walk
    `Tree.Root.Entries[]`; for each `JobActivityEntry` the `Job` carries
    `OwnerRef`/`TranscriptRef`, and the `Delegate` carries `ChildRef`, the
    recursive `Child` (recurse into `Child.Entries[]`), and
    `Turns[].OwnerRef`/`Turns[].TranscriptRef` (`JobActivitySession`/
    `JobActivityEntry`/`JobActivityJob`/`JobActivityDelegate`,
    `appwire/types.go`). Every one of these is a session reference exactly like
    a top-level `ref`.
    **`JobsListResponse.Data` is typed `any` today, so the walk needs a decode
    step first.** The catalog defines `JobsListResponse.Data any`
    (`appwire/types.go`), not `appwire.JobActivityTree`; a typed recursive walk
    over a generic map would rewrite nothing in a real response, and the
    `evener/jobs/list` client decodes remote data into generic maps. The
    requirement is therefore: before walking nested refs, **recognize** an
    activity-tree payload and walk only a recognized tree — **preserving every
    other payload as the `any` value it arrived as** (an
    unrecognized/forward-compatible shape is not blanked, dropped, or replaced
    with a zero-value tree). "It unmarshalled without error" is not
    recognition: `{}` and any unrelated JSON object decode into a zero-value
    `appwire.JobActivityTree` with no error, so a decode-only test would
    silently rewrite payloads the source does not understand. Recognition is
    semantic and anchored on required fields **and their types**: an
    activity-tree payload is a JSON object carrying `revision` as a
    non-negative integer (a JSON number, never a string or object) and `root`
    as a JSON object carrying `sessionId` and `ref` as strings (empty strings
    allowed — the Go encoder emits all of them unconditionally; none of
    `JobActivityTree.Root`, `.Revision`, `JobActivitySession.SessionID`, or
    `.Ref` carries `omitempty`). An empty object, an unknown object, an object
    that carries `root` but not its required fields (`{"root":{}}`), and a
    payload whose required fields carry another type all fail that test, and
    every payload that fails it — or that passes it but still fails to decode
    as a tree — is preserved as the `any` value it arrived as, never rewritten.
    The retired flat array of `EvenerJobInfo` is recognized as its own shape
    and is **not** pass-through: it is the array form `evener/jobs/list`
    answered before the activity tree (docs/appwire-protocol.md,
    `evener/jobs/list`), its only ref-valued field is `transcriptRef` (a
    session ref for a delegate turn, the opaque `job:<id>` for a shell job),
    and that field is translated by the same declared-field policy as a tree
    node's. Leaving it untranslated would hand the controller a remote
    `local:<id>` — the wrong-machine failure this section exists to prevent.
    Recognition plus pass-through is
    therefore the only acceptable shape, and it keeps the wire field generic:
    making the wire field itself typed (`Data appwire.JobActivityTree`) breaks
    pass-through, because the stream client decodes the result with
    `json.Unmarshal` (`appwire.Client.Request`, `appwire/client.go`) and a
    legacy flat array fails that decode instead of arriving as the array shape
    the translation recognizes. What is
    not acceptable is a typed walk that silently no-ops because the runtime
    value is a `map[string]any`, or one that rewrites (and replaces) a payload
    it did not recognize. This
    must be tested **through the actual stream client**
    (`appwire.Client.Request` decoding a real JSON response), not only against
    a Go value that was already the typed struct, with an empty object, an
    object that carries `root` without its required fields (`{"root":{}}`), a
    legacy flat array, an unknown object, a minimal (zero-value) tree, and a
    forward-compatible tree carrying an extra field.
    **Implementation status:** the shipped translator (`translateActivityRefs`
    and its walk helpers, `remote_hub_refs.go`) applies the two-stage
    recognition boundary above **before** it walks. `activityTreeRecognized` is
    the cheap discriminator gate: it demands `revision` as a non-negative
    integer and `root` as an object carrying `sessionId` and `ref` as strings
    (empty strings allowed, because the wire types carry no `omitempty`).
    `activityTreeDecodable` then decodes the payload as a complete
    `appwire.JobActivityTree`; the decode is a gate and not a translation, so
    the walk keeps operating on the decoded map and any key the typed struct
    does not declare survives byte-for-byte. Every payload that fails either
    stage — an empty object, `{"root":{}}`, a tree whose required fields carry
    another type, an unrelated object, or a payload the discriminator gate
    accepts but the typed tree rejects (`entries` as an object rather than an
    array, a declared container of the wrong type, a `revision` that overflows
    the `uint64` field) — is returned as the `any` value it arrived as. Only a
    recognized and decodable tree is walked, and the walk itself stays
    structural: it follows the declared containers
    (`root`/`entries`/`job`/`delegate`/`child`/`turns`) and rewrites only the
    ref fields those nodes declare, preserving every other key byte-for-byte.
    The named pass-through cases are pinned by
    `TestRemoteHubJobsListPreservesUnrecognizedPayloads`, the payloads that pass
    the discriminator gate but fail the full decode by
    `TestRemoteHubJobsListPreservesUndecodableTreePayloads`, the recognized tree
    — every declared ref field, a zero `revision`, empty required strings, and a
    forward-compatible extra field — by
    `TestRemoteHubJobsListTranslatesRefsOfRecognizedTrees`, and the retired flat
    array's translation by `TestRemoteHubJobsListTranslatesLegacyFlatArrayRefs`
    with its unaddressable value classes (a bare id, a foreign ref, an empty
    value) in `TestRemoteHubJobsListPreservesUnaddressableLegacyTranscriptRefs`;
    all five run through the actual stream client, so the requirement is closed
    rather than an open code item.
  - the `Thread.Evener.Diagnostics` block (`EvenerDiagnostics`,
    `appwire/types.go`) on any thread snapshot (a `ReadThread`/`ListThreads`
    response or a `thread/started` notification): `Jobs[].TranscriptRef`
    (`EvenerJobInfo.TranscriptRef`) and `Delegates[].TranscriptRef`
    (`EvenerDelegateInfo.TranscriptRef`).
  - the `Thread.Evener.PendingEscalations[]` array
    (`Evener.PendingEscalations []SandboxEscalationRequested`, `appwire/types.go`)
    on any thread snapshot (a `ReadThread`/`ListThreads` response or a
    `thread/started` notification): each item's `Ref`
    (`SandboxEscalationRequested.Ref`) is the session ref the escalation card
    belongs to — a bare thread ID is not enough, and a pending card routed by a
    remote `local:<id>` resolves against the controller's own `local`, the wrong
    machine, where the session does not exist. Rewrite each `.Ref` from the
    remote `local:<id>` to the controller `host:<id>`; the item's `ThreadID` is
    a bare, unnamespaced ID and needs no change (matching the bare-`threadId`
    rule below).
  A nested `local:<id>` left untranslated makes the controller/TUI route the
  job, delegate, or child session to the controller's *own* `local` — the wrong
  machine, where the child session does not exist. `RemoteHubSource` must
  rewrite every such field from the remote `local:<id>` to the controller
  `host:<id>`, walking `JobActivityTree`, `EvenerDiagnostics`, and
  `Evener.PendingEscalations` to their leaves. This is the same rule the
  notification list below states, applied to the response carriers; a new nested
  ref field inherits it.
- Sub-thread aliases: the remote hub emits them as read-only threads with
  `Kind:"subagent"`, empty capabilities, `Ref:"local:<child>"`, and
  `ParentRef:"local:<owner>"` (`local_daemon.go`). Translating both
  refs is sufficient for the controller's list/read views; mutations against an
  alias are already refused on the remote side (aliases are excluded from
  mutation resolution, `local_daemon.go`).
- Notifications: translate **every session-reference field in every
  notification payload**, not only the top-level `ref`, and enumerate them
  rather than translating "the ones we noticed". The set is:
  - the top-level `ref` of every payload that carries one (the hub's own relay
    reads `ref` first, then `threadId`; `app_relay.go`). Bare `threadId` values
    are already unnamespaced and need no change.
  - a nested `Thread` snapshot (`NotifyThreadStarted`, wire string
    `"thread/started"`; see the notification catalog in `appwire/protocol.go`)
    gets the full outbound thread translation above applied to it — `Source`,
    `Evener.Ref`, `Evener.ParentRef`, and each `Evener.PendingEscalations[].Ref`.
  - the escalation notifications `evener/sandbox/escalation/requested`
    (`NotifyEvenerSandboxEscalationRequested`, payload `SandboxEscalationRequested`)
    and `evener/sandbox/escalation/resolved`
    (`NotifyEvenerSandboxEscalationResolved`, payload `SandboxEscalationResolved`)
    carry a top-level `Ref` beside a bare `ThreadID` identifying the session the
    escalation belongs to (`appwire/types.go`); both `.Ref` fields are session
    references and must be rewritten to `host:<id>`. The relay's own ref-first
    read (`app_relay.go`) routes on `ref`, so an untranslated `local:<id>` here
    both routes to the wrong session and exposes a `local:` ref to the browser.
  - the nested job/delegate transcript refs carried by `evener/job/started` and
    `evener/job/finished` (`EvenerJobParams.Job` =
    `EvenerJobInfo.TranscriptRef`, `appwire/types.go`) and by
    `evener/delegate/updated` (`EvenerDelegateParams.Delegate` =
    `EvenerDelegateInfo.TranscriptRef`, `appwire/types.go`). These are
    session references exactly like the others and must be rewritten from the
    remote `local:<id>` to the controller `host:<id>`.
  A nested `local:<id>` transcript ref left untranslated makes the
  controller/TUI route the job or delegate to *its own* `local` — the wrong
  machine, where the child session does not exist — so the job/delegate
  notifications are not an optional extra: every nested session-reference field
  is in scope, and a new payload that adds one inherits the rule.

**Remote item paging requires a source-owned cursor identity.** The hub's
item-page packer re-encodes the continuation cursor from the candidate result's
`Identity` (`packedOlderCursor`, `cmd/evener-hub/app_item_page_fit.go`); the
fallback for a source that does not implement the candidate interfaces supplies
a zero identity and returns `legacy transcript item source cannot page without
cursor identity` (`app_item_page_fit.go:229-243`) as soon as the response
carries a non-empty `OlderCursor`/`NextCursor`. A remote hub packs its own
replies, so a remote thread whose window stops short of the oldest item always
carries one: **without these interfaces every multi-page remote read or list
fails the request instead of returning its first page.** `RemoteHubSource` must
therefore implement:

- `ItemCandidatesFromRead` (`appsource.ItemReadCandidateSource`, `source.go`):
  convert the already-materialized remote read into an `ItemCandidateResult` —
  **no second transcript read** — whose `Candidates` are the response's items
  and whose `Identity` is a **controller-minted, source-scoped** cursor
  identity; retain the remote hub's opaque `OlderCursor` behind that identity
  (it is the remote hub's continuation token, never a browser value).
- `appsource.ItemCandidateSource` — `ListItemCandidates` decodes the browser's
  controller cursor against the retained identity (`appitempaging.DecodeCursor`),
  validates its boundary against the retained window, and translates it to the
  remote hub's own cursor for the forwarded `thread/turns/list`
  (`appitempaging.RebaseCursor`) — the item-mode list API the hub actually
  implements (`MethodThreadTurnsList`, `ScopeBoth`); the catalog's
  `MethodThreadTurnItemsList` (`thread/turns/items/list`) is
  `ScopeUnimplemented` and served by no evener router, so it is never a
  forwarding target (§"Method-coverage analysis") — so the cursor the browser
  round-trips is always re-encoded under the same identity and validated before
  any remote call. An unrecognized, stale, or identity-mismatched cursor returns
  `appwire.TranscriptItemCursorStale()`, never a bare remote error. The
  interface's `ReadItemCandidates` (the item-mode read entry) must be
  implemented too — it satisfies the interface arity and may delegate to the
  same conversion + retention path as `ItemCandidatesFromRead`.

The identity must rotate when the observed window is rewritten (an item
replaced, the transcript re-projected) so a stale continuation cannot splice
two different projections; the retention/rotation policy and the per-thread
serialization of paging are the implementing PR's.
`remote_hub_source_paging_test.go` (shipped with 05a) pins the shape — a
multi-page round trip through the real packer, plus stale/rotated-boundary
refusals.

**Image URLs are host-scoped and must be rewritten through the controller.**
A hub stamps image URLs into the thread snapshots it returns: the sha-addressed
replay form `/s/<session>/images/<sha>` (`sessionImageURL` /
`stampSessionImageURLs`, `cmd/evener-hub/output_images.go`; the bytes are
re-scanned from that hub's `sessions/<id>.transcript.jsonl`) and the
file-backed form `/doc/image?session=<session>&path=<rel>`
(`outputImagesForToolCall`, `output_images.go`; resolved against that hub's
session CWD). **Both routes are serving-hub-local by construction.** The
controller's handlers resolve the session id only against its *own* state —
`handleSessionImage` (`image_serve.go`) requires the id in the controller's
past index, and `handleDocImage`/`localSessionCWD` (`doc_serve.go`) refuses a
non-local route id and otherwise reads the controller's past/roster — so a URL
stamped by the *remote* hub either 404s or, when the controller has a local
session with the same id, resolves against the **wrong session** (the
file-backed form would read a controller-local file relative to that session's
CWD). The remote path must therefore:

- rewrite every image URL a remote hub stamped, through **one shared image
  visitor** applied at every seam that returns or relays a hub payload, not a
  pass over one field. The remote hub stamps two fields, so both are in scope:
  `ThreadItem.OutputImages[].URL` (tool-result thumbnails) and
  `ThreadItem.Images[].URL` (replayed user-input images, sha-addressed by
  `stampInputImageURLs`); `stampSessionImageURLs` walks both
  (`cmd/evener-hub/output_images.go`). Every structure that embeds a `Thread`,
  `Turn`, or `ThreadItem` is a carrier (`appwire/types.go`):
  - RPC responses: `ThreadReadResponse` (thread/read),
    `ThreadListResponse.Data` (thread/list), `ThreadTurnsListResponse.Data`
    (thread/turns/list), `ThreadStartResponse`/`ThreadResumeResponse`/
    `ThreadForkResponse`/`ThreadClearResponse` (thread/start|resume|fork|clear),
    `TurnStartResponse` (turn/start),
    `EvenerSubagentPreviewResponse.Items` (evener/subagent/preview), and
    `ThreadTurnItemsListResponse.Data` (thread/turns/items/list —
    `ScopeUnimplemented`, served by no evener router today, but the visitor
    covers the type);
  - notifications: `ThreadStartedParams.Thread` (thread/started),
    `TurnStartedParams.Turn` (turn/started), `TurnCompletedParams.Turn`
    (turn/completed), and `ItemLifecycleParams.Item` (item/started,
    item/completed).
  All of these are the same `Thread`/`Turn`/`ThreadItem` types, so the visitor
  is defined once over those types and applied at each seam rather than
  per-payload: a new carrier, or a new image field on an existing one, inherits
  the rule instead of being silently missed. An `Images[].URL` left
  remote-stamped reaches the browser unresolved exactly like an
  `OutputImages[].URL` one: it resolves against the controller's own state,
  404s, or reads the wrong machine. Each URL is rewritten to a host-qualified
  controller route that names `s.id` and the remote session/thread, so the
  browser never requests a remote-stamped URL from the controller;
- serve that route by fetching the bytes from the owning host over the attached
  channel. The host hub's HTTP endpoint is dialed over loopback by the bridge
  and is not reachable from the controller, so the controller must proxy the
  bytes, not redirect. The proxy resolves the **attached-only** client
  (`Manager.ClientIfAttached`, §"Every other remote call is non-dialing, not just
  the snapshot and the non-explicit list"), so an
  unattached or unknown host is refused typed (`appwire.SessionUnavailable`) and
  never falls back to a local read. The browser-facing route and the AppWire
  request it maps onto are specified in full here — the rewrite rule above is
  only usable if the URL it produces has an exact shape and behavior, and this
  is the one path by which a hub reads bytes out of another hub's filesystem:

  - **Route shape and encoding.** The rewritten URL keeps each stamped form's
    shape and **host-qualifies the route id** with the controller's own source
    id for the host (`s.id + ":" + <remote session id>`, the `appwire.Ref`
    form): `/s/<s.id>:<session>/images/<sha>` and
    `/doc/image?session=<s.id>:<session>&path=<rel>`. That route id is already
    the web layer's model of a remote session — `isLocalRouteID` is false for a
    `<sourceID>:<threadID>` id and `appRefFromRouteID` preserves it
    (`web.go`, `web_test.go`'s external-ref case). Encoding is the stamping
    functions' own: `url.PathEscape` for the route id in its path segment (the
    sha is fixed-shape lowercase hex and needs none), `url.QueryEscape` for
    `session` and `path` (`:` survives a path segment, so the id on the wire
    reads `alpha:02wM…`; the query form arrives as `alpha%3A02wM…`, decoded by
    `r.URL.Query()`) — exactly how
    `sessionImageURL` and `resolveOutputImageFile` write the local forms
    (`output_images.go`). The browser's existing item renderers consume these
    URLs unchanged, so no client-side URL shape is introduced.
  - **Registration, precedence, and HTTP behavior.** No new mux pattern: the
    two existing registrations carry it — `mux.HandleFunc("/s/", s.handleSession)`
    (whose `images/<sha>` branch calls `handleSessionImage`) and
    `mux.HandleFunc("/doc/image", s.handleDocImage)` (`web.go`). Each handler
    branches on the route id: a `local` (or bare, legacy local) id resolves
    exactly as today; a non-`local` id is the host-qualified form and is served
    by the proxy, and must never fall through to the local resolution, so a
    remote-stamped URL can never read controller-local state. Both routes are
    GET-only (`405` otherwise, matching `handleDocImage`'s existing check). A malformed sha on
    the sha branch is `400` exactly as `handleSessionImage` answers, and a
    missing `session`/`path` on the file-backed branch is `404` exactly as
    `handleDocImage` answers. A proxy outcome maps to HTTP as: host
    `InvalidParams` → `400` (the host's own containment/traversal refusal
    included — containment is resolved against the *owning* session's working
    directory, never the controller's; a refused path is a refusal, not a
    missing resource), host `ResourceNotFound` → `404`, and an
    unattached/unknown host or a transport failure → `503` with a short text
    body; a refusal never falls back to a local read.
  - **Authentication and cache.** The same access model as the local image
    routes: a same-origin browser GET on the controller's own listener with no
    extra credential (`<img>` carries no header), with the *host*-side read
    authenticated by the proxy's attached AppWire client. The same cache headers
    as the local answers: sha branch `Cache-Control: public, max-age=86400,
    immutable` with `ETag: "<sha>"` (content-addressed), file-backed branch
    `Cache-Control: private, max-age=60` with an `ETag` of the response's own
    `SHA` (`SessionImageResponse.SHA`). Neither branch revalidates
    conditionally beyond what `handleSessionImage`/`handleDocImage` do today.
  - **Mapping to the proxy request.** The route decodes back into exactly one
    AppWire call on the owning host's attached client: the route id's `s.id`
    selects the client (it is never a param field), `SessionID` is the remote
    session id with the `s.id` prefix stripped, and the sha branch sets `SHA`
    while the file-backed branch passes `Path` through as the session-relative
    path verbatim (`rel` is forwarded as decoded; the *host* cleans and
    contains it). Exactly one of `SHA`/`Path` is set by construction — the
    method's own `InvalidParams` precondition.

  - **Catalog entry.** Method `MethodEvenerSessionImage = "evener/session/image"`,
    scope `ScopeHub`, registered in `appwire/protocol.go`'s `Methods` with its
    params/result types so the host hub's router serves it and the catalog↔router
    cross-check covers it, plus the regenerated Go and TypeScript bindings. It
    is an AppWire method, **not** an HTTP route: it must not be added to the
    hub's loopback mux. **No `origin`-based refusal applies to it.** The proxy
    request arrives over the attach bridge, so it is remote-originated by
    construction (component 02, §Contract "Bridge marker"); refusing that role
    would reject exactly the request this path exists to make, and every remote
    image load would fail. The component-05 loop guard is still satisfied
    without a refusal here: its rule refuses a remote-originated request routed
    to **another remote source**, and this method has no source selector to fan
    out with — `SessionImageParams` carries only `SessionID`/`SHA`/`Path`, so it
    can only ever resolve against the recipient hub's own local state. If the
    params ever gain a host/source selector, the guard's real rule attaches to
    it: refuse resolution via another remote source, never a remote-originated
    caller as such. **Implementation status:** the method, the request-context
    `origin` role the guard reads, and this controller route are all tracked
    follow-ups (keystone, round-21/round-22 image items), so this bullet pins
    the contract, not shipped behavior.
  - **Params.** `SessionImageParams{SessionID string (json:"sessionId"); SHA string (json:"sha,omitempty"); Path string (json:"path,omitempty")}`.
    Exactly one selector must be set, matching the two stamped URL forms: `SHA`
    for the sha-addressed replay form (`/s/<session>/images/<sha>`), `Path` for
    the file-backed form (`/doc/image?session=<session>&path=<rel>`). Both set,
    or neither, is `InvalidParams`.
  - **Result.** `SessionImageResponse{MediaType string (json:"mediaType"); Size int64 (json:"size"); SHA string (json:"sha,omitempty"); Data []byte (json:"data")}`
    — `Data` is the raw bytes (base64 inside the JSON frame),
    `Size` is `len(Data)`, and `SHA` is the lowercase hex sha256 of `Data`
    (echoed for the sha-addressed form, and the value the controller uses for
    the file-backed form's `ETag`).
  - **Validation and bounds.** The host validates before it reads anything:
    `SHA` must match `^[0-9a-f]{64}$` (`imageShaRegexp`, `image_serve.go`).
    `Path` must be **session-relative** and must resolve inside the session's
    working directory (`fspaths.ResolveInRoot`, the same containment
    `handleDocImage`/`outputImagesForToolCall` use) — an absolute path, a
    non-regular file, or any escape is refused, so the method can never read an
    arbitrary host file. The served bytes are bounded by `outputImageMaxBytes`
    (8 MiB, `output_images.go`): an image over the bound is refused, never
    streamed, and the response stays far inside the transport's frame limit
    (`appWireWebSocketReadLimit`, 128 MiB). The bound does not come for free on
    the sha-addressed branch, which must enforce it itself: the file-backed
    branch's `readOutputImageFile` refuses an over-bound file at stat time, but
    the sha branch re-scans `sessions/<id>.transcript.jsonl` with
    `findImageInTranscript` (`image_serve.go`), which applies no size check to
    the bytes it matches and reads and decodes a record up to
    `transcriptJSONLMaxLineBytes` (128 MiB, `transcript_limits.go`, the same
    number as the transport frame limit) before the image is even located. The
    method must therefore enforce a hard decoded/encoded size limit **while
    scanning**: an over-bound record or image is rejected before its bytes are
    materialized, not after a full record decode, so the 8 MiB bound governs
    the read, not only the response. The media type is
    re-derived from the bytes with the `supportedOutputImageMedia` allow-list
    (`image/png`, `image/jpeg`, `image/gif`, `image/webp`, plus the RIFF/WEBP
    signature fallback) and is never the stored value: the sha-addressed form
    must not trust the transcript's `Image.MediaType`, and an image outside the
    allow-list is not servable.
  - **Errors.** Malformed or refused request fields — both/neither selector, a
    non-hex `SHA`, and an absolute or escaping `Path` (the containment/traversal
    refusal) — are `appwire.InvalidParams`, which the
    controller's route answers as a 400; an unknown session, a missing
    transcript, a sha or path that resolves to nothing, empty bytes, an
    unsupported media type, and an over-bound image are
    `appwire.ResourceNotFound`, which the controller's route answers as a 404 to
    the browser exactly as `handleSessionImage`/`handleDocImage` do locally.
    Transport and authorization failures surface from the channel unchanged. No
    refusal falls back to a local read, and none may return bytes belonging to a
    different session or host.
- keep the controller's file-backed enrichment pass off remote threads
  (`threadReadLocalImagePolicy` / `EnrichThreadFileBackedImages`, `app_rpc.go`),
  so the controller never probes controller-local paths named by remote data —
  the remote hub has already enriched its own replies; it is the *URLs* that
  must be translated.

A remote-stamped URL that reaches the local handlers is a wrong-machine read,
not merely a 404, because session ids are namespaced per host.

## Data flow

Attach and probe (component 04 drives, 05 owns the probe):

```
controller host config → SSH manager spawns `ssh host <bridge>`
  → bridge dials remote hub loopback /rpc with the host capability token
  → stdio becomes StreamTransport (component 01)
  → component 04: client = appwire.NewClient(transport); client.Initialize
  → RemoteHubSource resolves that client per call (RemoteHubClientFunc)
  → HostCapabilities probe over that client
    (the source itself was already registered at hub startup)
```

Read path (controller RPC → remote session):

```
browser: thread/read ref="host:S"
 → hub ThreadRead handler (app_rpc.go)
 → sourceForThread → Registry.SourceForRef("host:S") → RemoteHubSource
 → RemoteHubSource.ReadThread: ref "local:S" → client.Request(thread/read)
 → remote hub handler runs against its local daemon source
 → response Thread.Evener.Ref "local:S" → rewritten to "host:S"
 → the remote hub's image URLs (/s/<id>/images/<sha>, /doc/image?session=<id>…)
   are rewritten to the host-qualified controller route
   (§"Image URLs are host-scoped and must be rewritten through the controller")
 → controller's relay runs as for any source; the local file-backed image
   enrichment pass does not run for a remote source (the remote hub already
   enriched its own reply)
```

Subscription path:

```
browser subscribes → controller relay (app_relay.go)
 → RemoteHubSource.ReadThread (plain) then startRelay → SubscribeThread
 → RemoteHubSource issues thread/read {Ref:"local:S", Subscribe:true} on the
   shared client, discards the returned snapshot (already read)
 → remote hub attaches its own relay and pushes notifications on the channel
 → a single fan-out goroutine reads client.Notifications() and routes each
   translated notification by ref to the per-ref subscriber channel
 → controller relay broadcasts to subscribers under relayKey "host:S"
```

Teardown: when the controller relay's subscription for `host:S` ends, the
refcount for remote thread `S` drops. Only at zero does `RemoteHubSource` send
`thread/unsubscribe {Ref:"local:S"}` on the shared client and drop the routing
entry; a still-nonzero count leaves the remote relay attached.

The controller relay uses the non-atomic path because `RemoteHubSource` does not
implement `RelaySessionSource`: `prepareRelay` takes the
`source.ReadThread` + `startRelay` branch (`app_relay.go`) and
`startRelayForThread` takes the non-atomic branch (`app_relay.go`).
This matches the existing legacy path (`local_daemon.go`). See §Open
questions for the atomicity tradeoff.

## Error handling

- **Offline / transport failure.** Every call maps dial, EOF, reset, closed and
  timeout failures to `appwire.SessionUnavailable`, mirroring
  `localDaemonDialError` (`local_daemon.go`), so the hub refuses actions
  and the fleet shows the host offline (parent design §2). Message names the
  host.
- **Loop-guard refusal (remote-originated fan-out).** A request whose
  routing-seam `origin` is non-empty — it arrived over a peer hub's attach-bridge
  connection, identified by the `X-Evener-Bridge: 1` marker the bridge presents
  alongside the shared capability token (component 02) — is served from the
  **recipient hub's own local state** and is refused with a typed error if any
  fan-out path would route it to a second remote source. The refusal is deliberate and
  surfaced, never a silent serve-from-another-host. The `thread/start` spawn
  seam is one such path: a remote-originated spawn whose effective source —
  including one resolved from a preserved harness naming a host configured on
  the recipient — is non-local is refused typed and never routed onward (see
  §"The receiving hub must reject a non-local resolution for a
  remote-originated `thread/start`"). A local-originated request (`origin`
  empty, i.e. no bridge marker on the connection) is unaffected.
- **Version mismatch.** `client.Initialize` returns
  `appwire.ProtocolVersionMismatchError` (`appwire/client.go`) when the remote
  speaks a different `appwire.ProtocolVersion` (`appwire/types.go`). Component
  04 owns the response and it is **not** terminal by itself: a mismatch found at
  preflight (the host's `launch-check` refuses the protocol) enters the
  version-auto-match deploy + restart flow, and a mismatch found at `Initialize`
  triggers one restart of the already-correct on-disk build. Until that lands,
  the source is marked unavailable and does not retry in place; only a mismatch
  that survives the restart is terminal. This is the same rule component 04
  states in its §"Error handling" — the two specs must not diverge here.
- **Bridge stdout corruption.** A framing/JSON error from the component-01
  transport is a broken channel: close the client, fail pending requests, and
  mark the source offline. Never attempt to resynchronize a corrupt stream. This
  is the caller side of component 01's contract: a torn frame
  (`io.ErrUnexpectedEOF`), an oversize-frame read, and a partial/zero-byte write
  all **poison** the transport permanently, so any such `Recv` or `Send` error
  is terminal for the channel. A JSON *decode* error does not itself poison the
  transport (the offending line is fully consumed), but it is still a framing
  error the caller must not resume from: **the caller must `Close`** on the
  first `Recv` error and treat the channel as dead.
- **Mutation outcome unknown.** Remote mutations carry the flag-day mutation
  envelope (`appwire/protocol.go`). When a mutation's response is lost,
  map it the way `localDaemonMutationCallError` does
  (`local_daemon.go`): `ErrorMutationOutcomeUnknown`, preserved
  `ClientMutationID`, `RetryDispositionAutomatic`. Do not invent a different
  shape.
- **Foreign ref.** A params ref whose `SourceID` is neither `s.id` nor empty is
  `InvalidParams`, matching `localEntryForRefMode` (`local_daemon.go`).
- **Nested remote hosts.** If the remote hub is itself a controller and returns
  a thread whose ref is not `local:`, that ref cannot be represented in the
  controller's `SourceID:ThreadID` namespace without collision (ref grammar has
  no separator beyond the first colon, `appwire/refs.go`). v1 refuses such refs
  with a clear error rather than passing them through. On the **list path** this
  cannot arise when `ListThreads` is forwarded as `SourceIDs:["local"]` (above),
  which asks the remote only for its local threads; a stray non-`local` row is
  dropped, not fatal (see the `ListThreads` remap above). See §Open questions.
- **Secrets.** The host capability token lives in the component-04 SSH/bridge
  layer and is never logged by `RemoteHubSource`; use the hub log sink
  (`hubConnectionLogf`, `cmd/evener-hub/internal/appsource/transport.go`)
  and never include tokens in errors.

## Testing

Primary gate: `go test ./cmd/evener-hub/internal/appsource/ -run RemoteHubSource`.
All remote-hub tests run over an in-memory stream pair — no real SSH, no
network.

1. **Scripted remote hub.** A responder that reads AppWire requests from one end
   of a `net.Pipe`-backed `StreamTransport` and answers with canned
   `appwire.Message` results for the covered method names (the pattern proved by
   the spike's `TestStreamTransportBacksClient`, per
   `2026-09-14-multi-host-spikes-findings.md:16-18`). Assert:
   - each `Source` method sends the exact wire method from the coverage table;
   - inbound refs are `local:` on the wire, outbound refs are `host:`.
2. **Real in-process hub.** Higher fidelity: build a hub with
   `newHubAppServer(hubcore.WebConfig{...}, registry)` (`cmd/evener-hub/app_rpc.go`)
   whose registry contains one fake local source, connect a `RemoteHubSource`
   to it over a pipe pair, and round-trip `ListThreads`, `ReadThread`,
   `ListTurns`, a mutation, and a subscription. This exercises the real hub
   handlers and the catalog↔router guarantee rather than a mock's opinion.
3. **Ref round-trip.** Property test: for refs `host:X`, `host:local:X`
   confusion, sub-thread alias refs, and `ParentRef`, `fromRemote(toRemote(r))
   == r`.
4. **Notification translation — every nested ref.** Feed a remote
   `NotifyThreadStatusChanged` (`"thread/status/changed"`) with `ref:"local:S"`
   and a `NotifyThreadStarted` (`"thread/started"`) with a nested `Thread`;
   assert the subscriber sees `host:S` and a translated nested ref
   (`Source`/`Evener.Ref`/`Evener.ParentRef`). Add escalation coverage: a
   `thread/started` whose nested `Thread.Evener.PendingEscalations[]` carries
   `Ref:"local:<child>"`, and a `evener/sandbox/escalation/requested` /
   `evener/sandbox/escalation/resolved` payload with `Ref:"local:<child>"`,
   must both reach the subscriber as `host:<child>`; the payload's bare
   `ThreadID` is unchanged. Add job/delegate coverage:
   `evener/job/started` and `evener/job/finished` (`EvenerJobParams.Job.
   TranscriptRef:"local:<child>"`) and `evener/delegate/updated`
   (`EvenerDelegateParams.Delegate.TranscriptRef:"local:<child>"`) must reach
   the subscriber as `host:<child>`, not `local:<child>`.
5. **Subscription teardown.** With two local subscribers on one remote thread,
   drop one: assert no `thread/unsubscribe` is sent and the survivor keeps
   receiving. Drop the last: assert exactly one
   `thread/unsubscribe {Ref:"local:S"}` is sent on that client and the routing
   entry is gone. Assert a re-subscribe while one is live never unsubscribes the
   replacement.
6. **Error mapping.** Transport EOF/reset → `SessionUnavailable`; protocol
   mismatch → `ProtocolVersionMismatchError`; lost mutation response →
   `ErrorMutationOutcomeUnknown`.
7. **SourceIDs remap.** `ListThreads{SourceIDs:["host"]}` must reach the remote
   as `SourceIDs:["local"]` and return the host's threads. An **empty**
   fleet-wide `SourceIDs` must also reach the remote as `SourceIDs:["local"]`,
   never as an empty/unfiltered filter (assert the forwarded params), and a
   response carrying a non-`local` ref must drop that row rather than fail the
   call. A filter that omits `s.id` must not reach the remote at all (the gate),
   and a direct call must error rather than forward empty.
8. **Catalog guard.** A test asserting every wire method in the coverage table is
   in `appwire.CatalogMethodNames(appwire.ScopeHub)` (that helper includes
   `ScopeBoth`, `appwire/protocol.go`), so a future re-scope of a mapped
   method fails loudly.
9. **Live (gated).** A real SSH test against a disposable host behind
   `EVENER_SSH_E2E=1`, never in default `make test`.
10. **Notification broker.** With two consumers registered on one client (a
    thread subscription and a recording admin consumer), one emitted
    notification is delivered to both and neither loses it; the drain goroutine
    stays the only reader of `Client.Notifications()`. A consumer that stops
    reading does not block delivery to the other. An **admin-only** consumer
    registered before any thread subscription receives a notification (the drain
    starts on registration, not only on `SubscribeThread`), and after a client
    replacement (reconnect) the registered consumer receives notifications from
    the fresh client's drain without re-registering.
11. **Attachment-gated fan-out.** With an unattached `RemoteHubSource` whose
    resolver is a spy wired to `Ensure`, a non-explicit `thread/list` (empty
    `SourceIDs`) never calls `Ensure`/`ListThreads` on it (it is skipped) while
    an attached source is queried; an explicit `SourceIDs` naming the host is
    the only list path that reaches the resolver.
12. **Loop guard (origin signal).** A remote-originated `thread/list` — a
    request whose request context carries the remote-originated role stamped
    from the bridge marker — is served from the **recipient hub's own local
    state** and is refused typed if any fan-out path tries to
    route it to a second remote source; a local-originated request (empty
    `origin`) fans out normally. The spawn seam is asserted too: a
    remote-originated `thread/start` whose preserved harness names a host
    registered **on the recipient** (a host the requesting controller cannot
    see) is refused typed and never forwarded to that host, while a
    local-originated spawn with the same harness resolves normally. Assert the
    origin is derived from the explicit bridge marker (`X-Evener-Bridge: 1`)
    read alongside the bearer token, **not** from
    `InitializeParams.ClientInfo` and **not** from the token — a token-only
    local connection (the TUI, a CLI script, a browser) must classify `local`,
    and only a connection presenting the marker is *remote-originated*.
13. **Nested response refs.** A `ListJobs` response whose `JobActivityTree`
    carries `local:` refs at every level reaches the caller fully rewritten to
    `host:`. Assert **per ref-typed field**, not by scanning the serialized tree
    for a substring: `Root.Ref`; each `Root.Entries[]` entry's
    `Job.OwnerRef`/`Job.TranscriptRef`; each `Delegate.ChildRef`; the recursive
    `Delegate.Child.Ref` with its own `Child.Entries[]`; and each
    `Turns[].OwnerRef`/`Turns[].TranscriptRef`; each
    `Thread.Evener.PendingEscalations[].Ref` on a thread snapshot — walking
    `Tree.Root.Entries[]` → `Delegate.Child.Entries[]` → `Turns[]`. Each must
    equal the expected `host:<id>`. A `ReadThread`/`ListThreads` response whose
    `Thread.Evener.Diagnostics` carries `Jobs[].TranscriptRef` and
    `Delegates[].TranscriptRef` is asserted the same way. Do **not** assert by
    scanning for a `local:` substring: legitimate non-ref strings could contain
    it, and a substring scan does not pin which typed ref fields were rewritten.
    Feed the `ListJobs` response through the **actual stream client**
    (`appwire.Client.Request` over a `StreamTransport` answering with the real
    JSON `{"data":{…}}` envelope) so the `Data any` decode step is exercised;
    assert a recognized tree is walked, an unrecognized `Data` payload — an
    empty object, an object carrying `root` without its required fields, or an
    unrelated object — passes through untouched as the value it received, and a
    legacy flat array's session-valued `transcriptRef` is translated while its
    opaque `job:<id>` refs and bare ids are preserved.
14. **Remote item paging round trip.** Over the scripted (or in-process) remote
    hub, a `thread/read` whose remote reply carries `OlderCursor` returns a
    packed first page whose `OlderCursor` is the controller cursor — **not** the
    `legacy transcript item source cannot page without cursor identity` error —
    and following that cursor through `thread/turns/list` returns the next
    page (the forwarded method is the implemented `MethodThreadTurnsList`, never
    the unimplemented `thread/turns/items/list`). Assert the cursor forwarded on
    the wire is the **remote hub's own**
    (not the browser's controller-encoded one), that a garbage/unknown cursor
    returns `appwire.TranscriptItemCursorStale()`, and that a cursor minted
    before the retained window was rewritten (rotation) is refused rather than
    splicing two projections. With the fallback path this test fails with the
    identity error, so it is the regression guard.
15. **Remote image URL translation.** A remote payload carrying an
    `OutputImages[].URL` **and** one carrying an `Images[].URL` of each stamped
    form (`/s/<id>/images/<sha>`, `/doc/image?session=<id>&path=…`) reach the
    caller rewritten to the host-qualified controller route, on every carrier
    the shared visitor covers: a `ReadThread`/`ListThreads` snapshot and a
    `thread/started` notification, a `thread/turns/list` result, the
    `thread/start`/`thread/resume`/`thread/fork`/`thread/clear` and `turn/start`
    responses, an `evener/subagent/preview` item list, and the `turn/started`,
    `turn/completed`, `item/started`, and `item/completed` notifications.
    Assert the typed `URL` fields (a `local:`-style substring scan does not pin
    them), and assert **both** an input-image and an output-image field per
    carrier — an `OutputImages`-only walk silently drops every replayed
    user-input image. Assert the rewritten route's exact shape
    (`/s/<host>:<session>/images/<sha>`,
    `/doc/image?session=<host>:<session>&path=<rel>`), that a non-`local` route
    id is served by proxying the fetch to the owning host's client and never by
    the controller's local `handleSessionImage`/`handleDocImage` resolution (a
    colliding local session id must not be read), and that the controller's
    file-backed enrichment pass does not run for a remote source
    (`EnrichThreadFileBackedImages`/`threadReadLocalImagePolicy` gate), so no
    controller-local path is probed from remote data.

## Acceptance criteria

- `RemoteHubSource` satisfies `appsource.Source` (compile-time `var _ Source =
  (*RemoteHubSource)(nil)`).
- The coverage test passes and every `Source` method maps to a wire method the
  hub router registers (asserted via `CatalogMethodNames(ScopeHub)`).
- Against a scripted hub over an in-memory stream: `thread/list`,
  `thread/read`, `thread/turns/list`, one lifecycle mutation, and a live
  subscription all round-trip with refs translated in both directions.
- `ListThreads` with `SourceIDs` naming the host returns that host's threads,
  and an empty fleet-wide `SourceIDs` is forwarded to the remote as `["local"]`
  rather than unfiltered.
- `newHubSourceRegistry` registers one source per configured host and leaves the
  empty-ref default as `local` (`app_sources.go`).
- The capability probe returns protocol version, hub version, features, launch
  config, models, plugins, auth/instances, and roots (OS/arch via preflight or
  the optional `evener/host/info` method).
- Offline, version-mismatch, and lost-mutation conditions map to the existing
  typed errors.
- With `EVENER_SSH_E2E` unset, no test opens a socket or spawns `ssh`.
- Exactly one goroutine reads each client's `Notifications()`; the fan-out
  broker delivers a copy to every registered consumer (thread subscriptions and
  the component-07 admin fan-out), and a slow consumer cannot stall another.
- Notification translation rewrites **every** session-reference field — the
  top-level `ref` (including the `evener/sandbox/escalation/{requested,resolved}`
  payloads), a nested `Thread` (including each `Evener.PendingEscalations[].Ref`),
  and the nested `TranscriptRef` on `EvenerJobInfo`/`EvenerDelegateInfo` in
  `evener/job/*` and `evener/delegate/updated` — so no remote child reference
  reaches the controller as `local:<id>`.
- Response ref translation rewrites every session-reference field **recursively**
  through the `ListJobs` result (`appwire.JobActivityTree`: `Root.Ref`; each
  `Entries[]` entry's `Job.OwnerRef`/`TranscriptRef`; `Delegate.ChildRef` and
  the recursive `Delegate.Child.Ref` with its own `Entries[]`; and
  `Delegate.Turns[].OwnerRef`/`Turns[].TranscriptRef` — the recursion carriers
  are `JobActivityDelegate.Child *JobActivitySession` /
  `JobActivityDelegate.Turns []JobActivityJob`), through
  `Thread.Evener.Diagnostics`
  (`EvenerDiagnostics.Jobs[].TranscriptRef`/`Delegates[].TranscriptRef`), and
  through `Thread.Evener.PendingEscalations[].Ref`, so no nested remote
  `local:<id>` reaches the controller unrewritten. Because
  `JobsListResponse.Data` is `any` today — and stays generic, because typing it
  as `appwire.JobActivityTree` fails the stream client's decode for a legacy
  flat array — the translation **recognizes** an activity-tree payload by its
  required fields and their types (`revision` a non-negative integer, `root` an
  object carrying `sessionId` and `ref` as strings) and walks only a recognized
  tree; every payload that is not recognized — an empty object, an unknown
  object, an object carrying `root` without its required fields — passes
  through untouched as the value it received. The retired flat array is
  recognized separately and translated by its declared field: each element's
  session-valued `transcriptRef` moves into the controller namespace while its
  opaque `job:<id>` ref and bare ids are preserved, or the remote `local:<id>`
  would leak to the controller. "Decodes without error" is not recognition:
  `{}` and unrelated objects decode into a zero-value `appwire.JobActivityTree`.
  Verified through the actual stream client.
- A non-explicit fleet-wide `thread/list` never attaches an unattached host
  (no `Ensure` on it); only an explicit `SourceIDs` naming the host does.
- The loop guard's origin signal comes from the explicit, cooperative bridge marker
  (`X-Evener-Bridge: 1`) presented by `evener hub attach --stdio` and read
  alongside the bearer token — **not** from `InitializeParams.ClientInfo`, and
  **not** from the token (which every client shares, so it can carry no role) —
  and a remote-originated request is refused typed before any fan-out to another
  remote source, **including the `thread/start` spawn seam**: a remote-originated
  spawn whose effective source (set `Source`, or the legacy harness fallback) is
  non-local is refused and never routed, so a preserved harness naming one of
  the recipient's own configured hosts cannot bypass the guard.
- A multi-page remote read/list round-trips through the packer: the first page
  carries a controller cursor, the continuation forwards the remote hub's own
  cursor, and no remote path returns
  `legacy transcript item source cannot page without cursor identity`
  (`RemoteHubSource` satisfies `ItemReadCandidateSource`
  (`ItemCandidatesFromRead`) and `ItemCandidateSource`
  (`ReadItemCandidates`/`ListItemCandidates`) with a controller-minted identity
  and `RebaseCursor` translation).
- Image URLs stamped by the remote hub (`OutputImages[].URL` and
  `Images[].URL` alike) are rewritten to the host-qualified controller route on
  every carrier above — reads, lists, previews, and notification payloads; that
  route proxies the bytes from the owning host over the attached channel and
  never reaches the controller's local image resolution, which would read a
  local session with a colliding id.

## PR size estimate (LOC)

Land as a series, each independently reviewable:

- **05a — skeleton, ref translation, read path, registration.** ~300 LOC +
  ~250 test. `remote_hub_source.go`, `remote_hub_refs.go`,
  `newHubSourceRegistry` wiring. Read-only methods (`ID`, `ListThreads`,
  `ReadThread`, `ListTurns`, `ListModels`) plus the item-candidate paging pair
  (`ItemCandidatesFromRead`/`ListItemCandidates`) the packer requires.
- **05b — subscription fan-out.** ~200 LOC + ~200 test.
  `remote_hub_subscription.go`: shared-client fan-out, notification
  translation, `SubscribeThread`, and the per-thread reference count whose zero
  sends `thread/unsubscribe`.
- **05c — mutations + error mapping.** ~250 LOC + ~200 test. Turn and thread
  lifecycle methods, `SessionUnavailable` / mutation-unknown mapping.
- **05d — capability probe.** ~200 LOC + ~150 test. `remote_hub_probe.go`,
  `HostCapabilities`/`CapabilitySource`, preflight wiring.
- **05e (optional) — `evener/host/info`.** ~60 LOC + ~30 test, only if OS/arch
  must arrive over AppWire.
- Deferred, separate PR: `RelaySessionSource` atomic handoff (open question 1).
  Native item-candidate paging is **not** deferred — the packer cannot page
  without it (§"Remote item paging requires a source-owned cursor identity") —
  and it is 05a scope.

Total ≈ 950–1250 LOC plus tests. Component 05 is the largest of the seven;
splitting per the above keeps each PR reviewable.

## Open questions

1. **Subscription atomicity.** v1 uses the legacy read-then-subscribe path
   (`app_relay.go`), accepting a possible missed frame between the
   controller's read and the remote subscription. Implementing
   `RelaySessionSource` (`source.go`) would need the generic
   `relaySession` machinery (`relay_session.go`) to accept a *logical*
   connection over the shared client plus request-ID-scoped cut markers
   (`appwire.WithRequestIDObserver`, `client.go`) — real but larger
   work. Decide after 05b ships whether the gap is observable.
2. **OS/arch source.** Preflight (04) vs a new `evener/host/info` hub method.
   The method is clean and testable but adds wire surface; preflight keeps the
   wire stable but couples the probe to component 04.
3. **Nested hosts.** A remote hub that is itself a controller returns refs the
   controller namespace cannot represent (see §Error handling). Refuse, drop, or
   introduce a ref grammar change? Parent design allows a hub to be a host; v1
   refuses only what the config alone can see (duplicate names and invalid
   names; a self-edge is refused only through an explicit upstream list — see
   item 4), so a depth-2 chain is *not* prevented and no ref grammar exists for
   it.
   **v1 bounds this at runtime rather than by config:** the controller refuses
   to fan a remote-originated request out to another remote source (the
   caller-identity guard; §"Ref translation detail") — and the **recipient**
   hub enforces the same bound at the `thread/start` spawn seam, since the
   controller cannot see a host configured only on the recipient (see
   §"The receiving hub must reject a non-local resolution for a
   remote-originated `thread/start`") — while the `["local"]` list remap keeps
   the list path from recursing, so a depth-2 chain cannot recurse even though
   it cannot be detected. The ref-grammar question (how, if ever, to represent a
   nested host's refs) stays open; the termination guarantee does not depend on
   its answer.
4. **Attach-time cross-hub cycle detection (deferred).** Component 03 exposes
   `AddWithUpstreams(entry, upstreamNames)` so a cycle can be refused at the
   moment an upstream host list is learned, but v1 has no way to learn one: no
   AppWire method reports a hub's configured hosts, and the attach handshake
   returns server info, protocol version, source ID, and features only
   (`InitializeResponse`, `appwire/types.go`). So v1 refuses what the
   config alone can see (duplicate names and invalid names; a self-edge needs an
   explicit upstream list) and does **not** perform A→B→A attach-time detection.
   Closing this needs a new `ScopeHub` host-list
   method plus a call from the attach path into `hostreg.AddWithUpstreams`;
   component 03 records the same deferral. Until that RPC lands, the runtime
   loop guard above is what keeps v1 terminating — this item is about
   *config-aware detection*, not about the termination guarantee.
5. **Controller-side past/recovery fencing.** Non-local refs bypass the
   controller's past index, deletion fence, and recovery admission
   (`app_sources.go`, `app_sources.go`). Remote sessions
   therefore have no controller-side dormant view; confirm that the fleet view
   (06) is acceptable as the only source of last-known remote state.
6. **One client vs per-call clients (resolved).** The SSH manager keeps one live
   channel per host and the source resolves the current client per call through
   `RemoteHubClientFunc`; it never caches a client, so a reconnect that swaps
   the underlying client is picked up automatically. §"Reconnect handoff" is
   that contract. What remains open is only the *eagerness* of recovery: the
   probe and subscriptions re-establish on next use (or on the relay's
   re-subscribe) rather than at the moment of reconnect. Decide later whether a
   component-04 event should proactively re-probe.
7. **Capability caching / refresh.** Probe once at attach, or re-probe on a
   timer and on reconnect? Auth/model/plugin state changes on the host between
   probes.
