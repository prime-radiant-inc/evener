# Component spec 05 — Remote hub source (`appsource.Source`)

Parent: `2026-09-14-multi-host-evener-design.md`. Depends on components 01
(stream transport), 02 (attach bridge), 03 (host config), 04 (SSH connection
manager). This is the highest-risk component; §Contract's method-coverage
analysis is the keystone deliverable.

**Citation convention.** Symbols (method constants, handler functions, files)
are authoritative and were verified on `multi-host-pr05a..d` and
`multi-host-pr06a-fleet-view-go`. Catalog and handler line numbers are omitted
deliberately: they shift as methods are added and the reviewer's base
(`origin/main`) is not the branch this spec describes. A `*.md:NNN` reference,
where it survives, is a hint, not pinning.

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
- Ref translation in both directions:
  - request params: `host:<thread>` / bare thread ID → `local:<thread>`;
  - responses and notifications: remote `local:<thread>` → `host:<thread>`,
    including `Thread.Evener.Ref`, `Thread.Evener.ParentRef`, `Thread.Source`,
    and nested `Thread` snapshots carried by `thread/started`
    (`appwire.NotifyThreadStarted`, `appwire/types.go`);
  - sub-thread (read-only alias) refs and parent refs.
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
- Native item-candidate paging (`appsource.ItemCandidateSource` /
  `ItemReadCandidateSource` / `CombinedItemReadSource`,
  `cmd/evener-hub/internal/appsource/source.go`). A source that does not
  implement these falls back to `sourceItemCandidateResultForRead` /
  `sourceItemCandidateResultForList`
  (`cmd/evener-hub/app_item_page_fit.go`), so v1 is correct without them.
  Adding them is a later optimization.
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
| `ReadThread` | `MethodThreadRead` | `ScopeBoth` | inline in `registerThreadHandlers` (`app_rpc.go`) | forward; translate refs |
| `ListTurns` | `MethodThreadTurnsList` | `ScopeBoth` | inline in `registerThreadHandlers` | forward |
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
| `ListJobs` | `MethodEvenerJobsList` | `ScopeBoth` | `hubJobsList` (`app_jobs.go`) | forward |
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
`MethodEvenerSpawnSlashCatalog`, `evener/spawn/slashCatalog`; `MethodModelList`,
`model/list`, `ScopeBoth`) take no ref, so naming the host is the **only** way
to scope them. They are forwarded as normal hub-scoped methods through the
host-scoped request envelope `evener/host/request` (component 07, §"Proxy
method"), which carries the selected host and method name — **not** through
`RemoteHubSource` (none is on the `Source` interface). This is the routing
component 06's spawn-form discovery calls use, and the exact set the
component-07 allow-list must carry (component 06, §"Frontend changes";
component 07, §"Proxy method"). The launch resolution the spawn form also needs
(`MethodEvenerLaunchResolve`, `evener/launch/resolve`, `ScopeHub`) is already
in the launch admin family (component 07, §"Proxy method"), so it is not
repeated here.

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
| effective launch config | `evener/launch/resolve` per root | `LaunchConfigResolved` (`appwire/types.go`) |
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
`RemoteHostHandshake func(host string) (appwire.InitializeResponse, bool)`
seam (backed by a `Manager.HandshakeIfAttached`, component 04, §"Go surface"),
installed as `SetHostHandshake` (§"Registration and default-source selection").
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
  `Manager.PreflightIfAttached` (component 04, §"Go surface"): the non-dialing,
  attached-only preflight accessor mirroring `ClientIfAttached` and
  `HandshakeIfAttached`. `Manager` stores live channels privately, so without
  this accessor `cmd/evener-hub/main.go` cannot construct `cfg.RemoteHostFacts`
  and `HostCapabilities.OS`/`Arch` stay permanently unpopulated (see §"Probe
  reaches the handshake facts…" above for the same shape on the handshake
  facts).
- `SetHostHandshake(cfg.RemoteHostHandshake)` — the attach handshake facts the
  capability probe needs (`ProtocolVersion`, `ServerInfo`, `SourceID`,
  `Features`), backed by `Manager.HandshakeIfAttached` (component 04, §"Go
  surface"). It returns the `InitializeResponse` for a host only while a live
  channel is installed, `(zero, false)` otherwise, and takes the manager-wide
  mutex, so it is safe from the `EventAttached` callback exactly like
  `ClientIfAttached`. Without this seam the probe cannot populate those four
  fields for a `RemoteHubSource` (see §"Capability probe").
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
configured host source be refused, or the harness-as-source fallback retired
(component 06, §"Write contract (session targeting)").

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
`StartThread` (`remote_hub_mutations.go`) rewrites `remote.Harness = "evener"`
and does not clear `Source` at all; forwarding the harness and clearing `Source`
is the 05c requirement, not a present fact.

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
  form is safe, and neither may call `Ensure`.

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
check and the request cannot disagree. **Implementation status:** both the gate
and the attached-only lookup are the implementing PR's requirement; the shipped
`refreshRemoteThreadSnapshot` (`web_api_tree.go`) iterates `s.sources.All()` with
no attachment gate, and `RemoteHubClientFunc` is wired to `Ensure`, so neither
exists yet.

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
may attach.

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
    (design §2 "Topology"; §Open questions item 3). It is a requirement of this
    component's routing seam, not a present fact.
  - **The origin signal is the connection a request arrived on, never
    `InitializeParams.ClientInfo`.** `ClientInfo` is caller-supplied and
    spoofable, and no origin/hop field exists today in the request context,
    `ThreadListParams`, or the `Source` interface, so the guard needs its own
    signal. The hub's `/rpc` edge already distinguishes the **host-bridge
    connection** — the one a peer hub's attach bridge opens with the host
    capability token (component 02) — from an ordinary local browser/TUI
    session. At accept/attach time the hub therefore marks the connection with
    its **role** (the credential it authenticated with: host capability token ⇒
    *remote-originated*; a local session ⇒ *local*), and the routing seam stamps
    that role into the request context, so every handler can read
    `origin` (empty for a local request, non-empty for a remote-originated one).
    The refusal is enforced at the **typed fan-out seam**, not by a check inside
    a handler: the multi-source fan-out (`hubThreadListWithSourceTimeout`,
    `app_threadlist.go`) — and any other path that routes a ref to more than one
    source — refuses to route a request whose `origin` is non-empty to any
    source other than the origin, returning the typed refusal instead. A
    remote-originated request is thus served from the origin's own local state
    only and can never be fanned to another remote source, which caps fan-out at
    depth 1 and terminates A→B→A. Coverage: a scenario test that injects a
    remote-originated `thread/list` (and each other fan-out path) and asserts it
    is never routed to a second remote source, with the typed refusal surfaced;
    and a local-originated request still fanned normally.
  - **Implementation status:** the shipped `remapRemoteSourceIDs`
    (`remote_hub_refs.go`, `multi-host-pr05a-remote-hub-source`) returns `nil`
    for an empty incoming filter — which `ListThreads` forwards as unfiltered —
    and returns an empty slice for an omitting filter. The `["local"]`-for-empty
    rule, the "must not reach the remote / must error" half, and the per-row drop
    are the implementing PR's requirements, not present facts.
- Outbound threads: set `Thread.Source = s.id`; rewrite `Thread.Evener.Ref`
  from `local:X` to `s.id + ":" + X`; rewrite `Thread.Evener.ParentRef` the same
  way (sub-thread aliases). Leave `Thread.Evener.InstanceID` untouched: it is an
  opaque precondition token round-tripped into `turn/start.expectedInstanceId`
  (`appwire/types.go`), so it must stay exactly what the remote hub
  minted.
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
    `Evener.Ref`, and `Evener.ParentRef`.
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
 → controller's relay/past-image enrichment runs as for any source
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
  routing-seam `origin` is non-empty — it arrived over a peer hub's
  capability-token bridge connection — is served from the origin's local state
  and is refused with a typed error if any fan-out path would route it to a
  second remote source. The refusal is deliberate and surfaced, never a silent
  serve-from-another-host; a local-originated request (`origin` empty) is
  unaffected.
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
   (`Source`/`Evener.Ref`/`Evener.ParentRef`). Add job/delegate coverage:
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
    request whose routing-seam `origin` names a remote source — is served from
    the origin's local state and is refused typed if any fan-out path tries to
    route it to a second remote source; a local-originated request (empty
    `origin`) fans out normally. Assert the origin is derived from the
    connection role, not from `InitializeParams.ClientInfo`.

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
  top-level `ref`, a nested `Thread`, and the nested `TranscriptRef` on
  `EvenerJobInfo`/`EvenerDelegateInfo` in `evener/job/*` and
  `evener/delegate/updated` — so no remote child reference reaches the
  controller as `local:<id>`.
- A non-explicit fleet-wide `thread/list` never attaches an unattached host
  (no `Ensure` on it); only an explicit `SourceIDs` naming the host does.
- The loop guard's origin signal comes from the connection role (host
  capability-token bridge connection), not `InitializeParams.ClientInfo`, and a
  remote-originated request is refused typed before any fan-out to another
  remote source.

## PR size estimate (LOC)

Land as a series, each independently reviewable:

- **05a — skeleton, ref translation, read path, registration.** ~300 LOC +
  ~250 test. `remote_hub_source.go`, `remote_hub_refs.go`,
  `newHubSourceRegistry` wiring. Read-only methods (`ID`, `ListThreads`,
  `ReadThread`, `ListTurns`, `ListModels`).
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
- Deferred, separate PR: native item-candidate paging and
  `RelaySessionSource` atomic handoff.

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
   caller-identity guard; §"Ref translation detail"), and the `["local"]` list
   remap keeps the list path from recursing, so a depth-2 chain cannot recurse
   even though it cannot be detected. The ref-grammar question (how, if ever, to
   represent a nested host's refs) stays open; the termination guarantee does
   not depend on its answer.
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
