# Component spec 05 — Remote hub source (`appsource.Source`)

Parent: `2026-09-14-multi-host-evener-design.md`. Depends on components 01
(stream transport), 02 (attach bridge), 03 (host config), 04 (SSH connection
manager). This is the highest-risk component; §Contract's method-coverage
analysis is the keystone deliverable.

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
    and nested `Thread` snapshots carried by `evener/thread/started`;
  - sub-thread (read-only alias) refs and parent refs.
- Subscription: `SubscribeThread` drives the remote hub's `thread/read`
  (`subscribe:true`) live feed over the single channel and fans notifications
  out per ref, reference-counting per remote thread and sending the remote
  hub's `thread/unsubscribe` when the last local subscriber leaves.
- Registration of one `RemoteHubSource` per configured host in the production
  registry (`cmd/evener-hub/app_rpc.go:24-74`), with the default source for an
  empty ref remaining `local` (`cmd/evener-hub/app_sources.go:31-38`).
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
  `cmd/evener-hub/internal/appsource/source.go:57-74`). A source that does not
  implement these falls back to `sourceItemCandidateResultForRead` /
  `sourceItemCandidateResultForList`
  (`cmd/evener-hub/app_item_page_fit.go:51-71`), so v1 is correct without them.
  Adding them is a later optimization.
- Atomic relay handoff (`appsource.RelaySessionSource`,
  `cmd/evener-hub/internal/appsource/source.go:639-659`). v1 uses the legacy
  read-then-subscribe path (`cmd/evener-hub/app_relay.go:1311-1322`); see §Data
  flow and §Open questions.
- Remote *tool execution* (`agent/execenv`): non-goal of the parent design.
- Attach-time cross-hub cycle detection: v1 learns no remote host list, so it
  cannot supply component 03's `AddWithUpstreams` upstream names. Explicitly
  deferred — see §Open questions item 4.

## Contract / interfaces

### The interface being implemented

`Source` is declared at `cmd/evener-hub/internal/appsource/source.go:15-46`:
`ID`, `ListThreads`, `ReadThread`, `ListTurns`, `StartThread`, `ResumeThread`,
`ForkThread`, `StartTurn`, `SteerTurn`, `ResolveSandboxEscalation`,
`InterruptTurn`, `QueueTurn`, `DrainAsSteer`, `PromoteQueuedAsSteer`,
`CancelQueued`, `CompactThread`, `ShutdownThread`, `SetThreadModel`,
`SetThreadReasoningEffort`, `SetThreadVisionModel`, `SetThreadName`, `GoalSet`,
`NotesHumanSet`, `UrlsRemove`, `ClearThread`, `ListModels`, `ListTasks`,
`ListJobs`, `JobOutput`, `SubscribeThread`.

The registry stores sources by `ID()` and resolves a ref's source by its
`SourceID` (`cmd/evener-hub/internal/appsource/registry.go:20-64`). A source's
`ID()` is therefore the host `name` from the host config entry (parent design
§5), and appears verbatim in controller refs and `hubapi.Ref.HostID`
(`hubapi/refs.go:11-30`).

Wire refs are `SourceID + ":" + ThreadID`, pattern
`^[A-Za-z0-9._~-]+$` (`appwire/refs.go:9-35`). The remote hub's own source ID is
`"local"`: `ServerConfig{SourceID: "local"}` (`cmd/evener-hub/app_rpc.go:235`)
and the registered local source is `NewLocalDaemonSourceWithEntries("local", …)`
(`cmd/evener-hub/app_rpc.go:26`). So the remote namespace is always `local:`.

### Method-coverage analysis (core deliverable)

`Source` was written against daemon-scoped calls: `LocalDaemonSource` dials each
daemon's WebSocket endpoint (`cmd/evener-hub/internal/appsource/local_daemon.go:686-724`)
and never talks to the hub's own router. The remote source instead attaches to a
**hub**, so the relevant question is which of its calls have a method on the
hub's AppWire router.

Method scopes are catalogued at `appwire/protocol.go:12-26` (`ScopeHub`,
`ScopeDaemon`, `ScopeBoth`, `ScopeConnection`, `ScopeUnimplemented`); the
`Methods` catalog is `appwire/protocol.go:112-213`. The hub router is
cross-checked to register every `ScopeHub` **and** `ScopeBoth` method
(`cmd/evener-hub/appwire_catalog_test.go:19-35`, `want :=
appwire.CatalogMethodNames(appwire.ScopeHub)`). Because a `ScopeBoth` method is
handled by the hub by proxying to its local source, **every method the `Source`
interface needs is already served by the remote hub's router.** No new
hub-scoped thread/turn RPC is required.

| `Source` method | Wire method | Scope (catalog line) | Remote-hub handler | Disposition |
|---|---|---|---|---|
| `ID` | — | — | — | local; returns host name |
| `ListThreads` | `thread/list` | Both (`protocol.go:115`) | `hubThreadList` (`app_rpc.go:393-395`) | forward; **remap `SourceIDs`**, translate refs |
| `ReadThread` | `thread/read` | Both (`protocol.go:116`) | `app_rpc.go:396-552` | forward; translate refs |
| `ListTurns` | `thread/turns/list` | Both (`protocol.go:118`) | `app_rpc.go:583-629` | forward; translate refs |
| `StartThread` | `thread/start` | Hub (`protocol.go:120`) | `app_rpc.go:662-677` | forward; remote hub spawns on the host |
| `ResumeThread` | `thread/resume` | Hub (`protocol.go:121`) | `app_rpc.go:678-687` | forward; translate refs |
| `ForkThread` | `thread/fork` | Hub (`protocol.go:122`) | `app_rpc.go:688-690` | forward |
| `StartTurn` | `turn/start` | Both (`protocol.go:131`) | `registerThreadHandlers` (`app_rpc.go:386-885`) | forward |
| `SteerTurn` | `turn/steer` | Both (`protocol.go:132`) | `registerThreadHandlers` | forward |
| `ResolveSandboxEscalation` | `evener/sandbox/escalation/resolve` | Both (`protocol.go:212`) | `registerThreadHandlers` | forward |
| `InterruptTurn` | `turn/interrupt` | Both (`protocol.go:133`) | `registerThreadHandlers` | forward |
| `QueueTurn` | `turn/queue` | Both (`protocol.go:134`) | `registerThreadHandlers` | forward |
| `DrainAsSteer` | `turn/drainAsSteer` | Both (`protocol.go:135`) | `registerThreadHandlers` | forward |
| `PromoteQueuedAsSteer` | `turn/promoteQueuedAsSteer` | Both (`protocol.go:136`) | `registerThreadHandlers` | forward |
| `CancelQueued` | `turn/cancelQueued` | Both (`protocol.go:137`) | `registerThreadHandlers` | forward |
| `CompactThread` | `thread/compact/start` | Both (`protocol.go:128`) | `registerThreadHandlers` | forward |
| `ShutdownThread` | `thread/shutdown` | Both (`protocol.go:130`) | `registerThreadHandlers` | forward |
| `SetThreadModel` | `thread/model/set` | Both (`protocol.go:124`) | `registerThreadHandlers` | forward |
| `SetThreadReasoningEffort` | `thread/reasoning-effort/set` | Both (`protocol.go:126`) | `registerThreadHandlers` | forward |
| `SetThreadVisionModel` | `thread/vision-model/set` | Both (`protocol.go:127`) | `registerThreadHandlers` | forward |
| `SetThreadName` | `evener/thread/name/set` | Both (`protocol.go:125`) | `registerThreadNameSetHandler` (`app_rpc.go:349`) | forward |
| `GoalSet` | `goal/set` | Both (`protocol.go:138`) | `registerThreadHandlers` | forward |
| `NotesHumanSet` | `notes/human/set` | Both (`protocol.go:139`) | `registerThreadHandlers` (`app_rpc.go:912-917`) | forward; translate `Ref` |
| `UrlsRemove` | `urls/remove` | Both (`protocol.go:140`) | `registerThreadHandlers` (`app_rpc.go:912-917`) | forward; translate `Ref` |
| `ClearThread` | `thread/clear` | Both (`protocol.go:123`) | `registerThreadHandlers` | forward |
| `ListModels` | `model/list` | Both (`protocol.go:180`) | `hubModelList` (`app_rpc.go:1156-1158`) | forward |
| `ListTasks` | `evener/tasks/list` | Both (`protocol.go:139`) | `hubTasksList` (`app_rpc.go:1159-1161`) | forward |
| `ListJobs` | `evener/jobs/list` | Both (`protocol.go:140`) | `hubJobsList` (`app_rpc.go:1162-1164`) | forward |
| `JobOutput` | `evener/jobs/output` | Both (`protocol.go:141`) | `hubJobsOutput` (`app_rpc.go:1165-1167`) | forward |
| `SubscribeThread` | `thread/read` (`subscribe:true`) + notifications | Both (`protocol.go:116`) | `app_rpc.go:396-552` + relay | **compose** over the channel |

Methods the hub also serves that are *not* on `Source` but that component 06/07
will want: `thread/unsubscribe` (Both, `protocol.go:117`),
`evener/thread/force-stop` (Hub, `protocol.go:129`, `app_rpc.go:855`),
`evener/thread/transcripts/list` (Hub, `protocol.go:142`, `app_rpc.go:1168`),
`evener/subagent/preview` (Hub, `protocol.go:143`, `app_rpc.go:630`), and the
path/dir/project/git helpers (`protocol.go:144-148`). These are forwarded by the
hub as normal hub-scoped methods once a host is selected, not through
`RemoteHubSource`.

`thread/turnItemsList` is `ScopeUnimplemented` (`protocol.go:119`) and is not on
the `Source` interface; nothing maps to it.

**The one gap: OS/arch.** No AppWire method exposes host OS/arch. The closest
hub RPCs are `initialize` (`ServerInfo.Name/Version`, protocol version, features
— `appwire/types.go:223-229`) and `evener/settings/overview`
(`SettingsHubOverview.Version/Commit/BuildChannel/ListenAddr/RunDir`,
`appwire/types.go:3268-3308`); `evener/update/check` reports build/commit only
(`appwire/types.go:2066-2075`). The only "host facts" type in the tree is
`sandbox.HostFacts`, which is internal to the remote agent and never crosses
AppWire. So:

- **Option A (recommended):** the capability probe takes OS/arch from the
  component-04 SSH preflight (the same `uname`-style probe that decides which
  binary to push), and AppWire supplies the rest.
- **Option B:** add one new hub-scoped method, e.g. `evener/host/info`
  (`ScopeHub`), to `appwire/protocol.go:112-213` plus a handler in
  `registerMiscHandlers` (`app_rpc.go:1149-1209`), returning OS, arch, HOME/XDG,
  and working roots. Cost: one catalog entry + one handler + the cross-check
  test (`appwire_catalog_test.go`) updates itself; ~60 LOC. Defer unless the
  controller needs OS/arch without a live SSH probe.

Everything else maps cleanly; the real cost of this component is ref translation
and the subscription stream, not new RPC surface.

### Capability probe

After attach, `RemoteHubSource` runs a small sequence of calls on the same
channel and caches the result. Suggested exported type and interface (in
`appsource`, consumed by the fleet view in component 06):

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
| protocol / hub version / features | the attach handshake component 04 already performed (`InitializeResponse`, kept as `Client.Features()`) plus the preflight facts — **not** a second `initialize` | `InitializeResponse` (`appwire/types.go:227-233`) |
| OS/arch | component-04 preflight (no RPC — see gap above) | — |
| effective launch config | `evener/launch/resolve` per root, or `evener/launch/getLayer` `layer:"global"` | `LaunchConfigResolved` (`appwire/types.go:3014-3020`) |
| available models | `model/list` | `ModelListResponse` (`appwire/types.go:2206-2215`) |
| plugin inventory | `evener/plugin/list` (+ `evener/marketplace/list` if needed) | `PluginListResponse` (`appwire/types.go:3232-3234`) |
| credential/provider health | `evener/auth/list` (+ `evener/instance/list`) | `AuthListResponse` (`appwire/types.go:2674-2676`) |
| working roots | host config entry | — |

`evener/auth/list` reports configured/signed-in state, not liveness. A live
credential test (`evener/auth/test`, `protocol.go:165`) hits the network per
provider; keep it out of the attach probe and expose it as an explicit refresh.

`initialize` is `ScopeConnection` and "must be the first request" on a
connection (`appwire/protocol.go:113`), so the probe cannot re-run it on the
already-initialized channel that component 04 handed over. Component 04 supplies
those three fields through the facts seam (preflight `protocol`/`version`, and
the feature set captured from the attach handshake); the probe makes the five
AppWire reads in the table above and nothing else.

### Registration and default-source selection

Production registers exactly one source today (`cmd/evener-hub/app_rpc.go:24-74`)
and the hub advertises `SourceID: "local"` (`app_rpc.go:235`). Component 05 adds
one `registry.Add(NewRemoteHubSource(...))` per configured host in
`newHubSourceRegistry`, each with `ID()` = host name. Registration is
**eager and attachment-independent**: every configured host gets its source at
hub startup, whether or not an SSH channel exists yet. An unattached host's
source reports itself offline (`OnlineSource`, component 06a's `online.go`) and
fails calls with `SessionUnavailable`; nothing is added or removed from the
registry on attach/detach. That is what keeps the fleet view's host list equal
to the configured `[[hosts]]` set (component 06). Order is irrelevant;
`Registry.All` sorts by ID (`registry.go:39-52`).

An empty ref must stay local: `sourceForThread` returns
`sources.Source("local")` for `ref == ""` (`cmd/evener-hub/app_sources.go:31-38`).
Remote sources are reached only by a non-empty `host:` ref (via
`Registry.SourceForRef`, `registry.go:54-64`).

Spawn is the one place where "which source" is not derived from a ref.
`hubThreadStart` already routes a non-local spawn to
`sources.Source(sourceID)` (`cmd/evener-hub/app_threadlifecycle.go:53-60`).
Component 06 adds an explicit wire field, `ThreadStartParams.Source` (a bare
source ID), and `hubThreadStart` resolves it ahead of the legacy
`launchSourceID(params)` harness fallback (`app_threadlifecycle.go:323-332`);
the field is on the wire type, not on `RemoteHubSource`.

**The host selector is controller-only and is stripped at the remote
boundary.** `ThreadStartParams.Source` names a source in the *controller's*
registry; the remote hub would resolve the same string against *its own*
registry and fail ("spawn source is not available: <name>") or target the wrong
source. `RemoteHubSource.StartThread` therefore clears it before forwarding
(component 05c's `remote_hub_mutations.go`):

```go
remote := params
remote.Source = "" // controller-only; the remote hub's own default (local) applies
```

The controller's chosen host is expressed purely by *which*
`RemoteHubSource` handled the call; the remote hub sees a plain `thread/start`
with its own default routing. No other `Source` method carries a host selector:
every other method addresses an existing thread by `Ref`, which is translated
(above).

`hubThreadResume` already routes a non-local ref to its source
(`app_threadlifecycle.go:337-339`), so resuming a remote session by
`host:<session>` works without further plumbing.

## Implementation approach

Files (all under `cmd/evener-hub/internal/appsource/` unless noted):

- `remote_hub_source.go` — the `RemoteHubSource` type, method implementations,
  and the coverage/forwarding helper.
- `remote_hub_refs.go` — ref translation (`toRemoteParams`, `fromRemoteThread`,
  `fromRemoteNotification`).
- `remote_hub_probe.go` — `HostCapabilities` and the probe.
- `remote_hub_subscription.go` — the single-client notification fan-out.
- `cmd/evener-hub/app_rpc.go` — register remote sources in
  `newHubSourceRegistry` (one added loop).
- `cmd/evener-hub/app_sources.go` — unchanged; its local default is already
  correct.

Reused seams:

- Transport: `appwire.Transport` (`appwire/transport.go:5-9`) and the
  component-01 `StreamTransport` over the SSH channel. Component 04 hands the
  source a dial function shaped like
  `appsource.appwireDialFunc` (`cmd/evener-hub/internal/appsource/transport.go:12-16`),
  or a long-lived `appwire.Transport`.
- Client: `appwire.NewClient(transport)` (`appwire/client.go:53-62`),
  `client.Start`, `client.Initialize` (`client.go:347-368`), `client.Request`
  (`client.go:287-289`), and the notification stream
  (`client.Notifications()`, `client.go:217-219`). One `Client` safely serves
  concurrent requests and the notification feed: `Send` is mutex-guarded
  (`client.go:243-249`) and responses are correlated by request ID
  (`client.go:232-285`).
- Reference implementation for call mapping and error shape:
  `LocalDaemonSource` (`local_daemon.go:246-724`), especially
  `withClientCallMapper` (`local_daemon.go:686-724`), the dial-error mapping
  (`local_daemon.go:731-782`), and the mutation-unknown mapping
  (`local_daemon.go:808-828`). `RemoteHubSource` should mirror these shapes so
  the hub's auto-resume and mutation-retry gates keep working, but the strings
  should say "remote hub unavailable: <host>" rather than "local daemon".

Key difference from `LocalDaemonSource`: `LocalDaemonSource` opens a fresh
WebSocket per call (`withClientCallMapper`) except for relay sessions; a remote
host's SSH channel is expensive, so `RemoteHubSource` should hold **one**
long-lived `appwire.Client` per host and issue every call over it. That client
also carries the notification stream that `SubscribeThread` needs, which is why
subscription is a fan-out rather than a second connection.

Subscription lifetime is the other difference. `RemoteHubSource` must
**reference-count subscriptions per remote thread ID** and issue the remote
hub's `thread/unsubscribe` (`appwire/types.go:37`, a `ScopeBoth` method,
`protocol.go:117`, params `ThreadUnsubscribeParams` `appwire/types.go:1483-1486`)
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
  (`local_daemon.go:899-924`) — a foreign `SourceID` must be refused, not
  silently retargeted.
- `ListThreads` needs special handling: the remote `hubThreadList` filters by
  `params.SourceIDs` (`app_threadlist.go:34-39,191-196`) and compares against
  `thread.Source` (`app_threadlist.go:288-290`). Remap `SourceIDs` entries equal
  to `s.id` to `"local"`; if the list ends up empty it means "no filter", which
  matches the controller's intent when only this host was requested.
- Outbound threads: set `Thread.Source = s.id`; rewrite `Thread.Evener.Ref`
  from `local:X` to `s.id + ":" + X`; rewrite `Thread.Evener.ParentRef` the same
  way (sub-thread aliases). Leave `Thread.Evener.InstanceID` untouched: it is an
  opaque precondition token round-tripped into `turn/start.expectedInstanceId`
  (`appwire/types.go:1490-1496`), so it must stay exactly what the remote hub
  minted.
- Sub-thread aliases: the remote hub emits them as read-only threads with
  `Kind:"subagent"`, empty capabilities, `Ref:"local:<child>"`, and
  `ParentRef:"local:<owner>"` (`local_daemon.go:1011-1018`). Translating both
  refs is sufficient for the controller's list/read views; mutations against an
  alias are already refused on the remote side (aliases are excluded from
  mutation resolution, `local_daemon.go:911-922`).
- Notifications: rewrite the `ref` field of every notification payload that
  carries one (the hub's own relay reads `ref` first, then `threadId`;
  `app_relay.go:314-342`). Bare `threadId` values are already unnamespaced and
  need no change. Notifications that embed a `Thread` (e.g. `thread/started`,
  `appwire/protocol.go:269`) must get the same thread translation applied to the
  nested snapshot.

## Data flow

Attach and probe (component 04 drives, 05 owns the probe):

```
controller host config → SSH manager spawns `ssh host <bridge>`
  → bridge dials remote hub loopback /rpc with the host capability token
  → stdio becomes StreamTransport (component 01)
  → RemoteHubSource.client = appwire.NewClient(transport); client.Initialize
  → HostCapabilities probe over that client
    (the source itself was already registered at hub startup)
```

Read path (controller RPC → remote session):

```
browser: thread/read ref="host:S"
 → hub ThreadRead handler (app_rpc.go:396)
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
`source.ReadThread` + `startRelay` branch (`app_relay.go:1311-1322`) and
`startRelayForThread` takes the non-atomic branch (`app_relay.go:1794-1798`).
This matches the existing legacy path (`local_daemon.go:605-655`). See §Open
questions for the atomicity tradeoff.

## Error handling

- **Offline / transport failure.** Every call maps dial, EOF, reset, closed and
  timeout failures to `appwire.SessionUnavailable`, mirroring
  `localDaemonDialError` (`local_daemon.go:731-782`), so the hub refuses actions
  and the fleet shows the host offline (parent design §2). Message names the
  host.
- **Version mismatch.** `client.Initialize` returns
  `appwire.ProtocolVersionMismatchError` (`appwire/client.go:333-360`) when the
  remote speaks a different `appwire.ProtocolVersion` (`appwire/types.go:25`).
  Surface it typed; component 04's version auto-match owns the restart. Until it
  restarts, mark the source unavailable rather than retrying in place.
- **Bridge stdout corruption.** A framing/JSON error from the component-01
  transport is a broken channel: close the client, fail pending requests, and
  mark the source offline. Never attempt to resynchronize a corrupt stream.
- **Mutation outcome unknown.** Remote mutations carry the flag-day mutation
  envelope (`appwire/protocol.go:215-257`). When a mutation's response is lost,
  map it the way `localDaemonMutationCallError` does
  (`local_daemon.go:808-828`): `ErrorMutationOutcomeUnknown`, preserved
  `ClientMutationID`, `RetryDispositionAutomatic`. Do not invent a different
  shape.
- **Foreign ref.** A params ref whose `SourceID` is neither `s.id` nor empty is
  `InvalidParams`, matching `localEntryForRefMode` (`local_daemon.go:899-908`).
- **Nested remote hosts.** If the remote hub is itself a controller and returns
  a thread whose ref is not `local:`, that ref cannot be represented in the
  controller's `SourceID:ThreadID` namespace without collision (ref grammar has
  no separator beyond the first colon, `appwire/refs.go:9-35`). v1 should refuse
  such refs with a clear error rather than pass them through. See §Open
  questions.
- **Secrets.** The host capability token lives in the component-04 SSH/bridge
  layer and is never logged by `RemoteHubSource`; use the hub log sink
  (`hubConnectionLogf`, `cmd/evener-hub/internal/appsource/transport.go:24-31`)
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
   `newHubAppServer(hubcore.WebConfig{...}, registry)` (`cmd/evener-hub/app_rpc.go:212-370`)
   whose registry contains one fake local source, connect a `RemoteHubSource`
   to it over a pipe pair, and round-trip `ListThreads`, `ReadThread`,
   `ListTurns`, a mutation, and a subscription. This exercises the real hub
   handlers and the catalog↔router guarantee rather than a mock's opinion.
3. **Ref round-trip.** Property test: for refs `host:X`, `host:local:X`
   confusion, sub-thread alias refs, and `ParentRef`, `fromRemote(toRemote(r))
   == r`.
4. **Notification translation.** Feed a remote `thread/status/changed` with
   `ref:"local:S"` and a `thread/started` with a nested `Thread`; assert the
   subscriber sees `host:S` and a translated nested ref.
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
   as `SourceIDs:["local"]` and return the host's threads.
8. **Catalog guard.** A test asserting every wire method in the coverage table is
   in `appwire.CatalogMethodNames(appwire.ScopeHub)` (that helper includes
   `ScopeBoth`, `appwire/protocol.go:49-61`), so a future re-scope of a mapped
   method fails loudly.
9. **Live (gated).** A real SSH test against a disposable host behind
   `EVENER_SSH_E2E=1`, never in default `make test`.

## Acceptance criteria

- `RemoteHubSource` satisfies `appsource.Source` (compile-time `var _ Source =
  (*RemoteHubSource)(nil)`).
- The coverage test passes and every `Source` method maps to a wire method the
  hub router registers (asserted via `CatalogMethodNames(ScopeHub)`).
- Against a scripted hub over an in-memory stream: `thread/list`,
  `thread/read`, `thread/turns/list`, one lifecycle mutation, and a live
  subscription all round-trip with refs translated in both directions.
- `ListThreads` with `SourceIDs` naming the host returns that host's threads.
- `newHubSourceRegistry` registers one source per configured host and leaves the
  empty-ref default as `local` (`app_sources.go:31-38`).
- The capability probe returns protocol version, hub version, features, launch
  config, models, plugins, auth/instances, and roots (OS/arch via preflight or
  the optional `evener/host/info` method).
- Offline, version-mismatch, and lost-mutation conditions map to the existing
  typed errors.
- With `EVENER_SSH_E2E` unset, no test opens a socket or spawns `ssh`.

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
   (`app_relay.go:1311-1322`), accepting a possible missed frame between the
   controller's read and the remote subscription. Implementing
   `RelaySessionSource` (`source.go:639-659`) would need the generic
   `relaySession` machinery (`relay_session.go:11-134`) to accept a *logical*
   connection over the shared client plus request-ID-scoped cut markers
   (`appwire.WithRequestIDObserver`, `client.go:156-173`) — real but larger
   work. Decide after 05b ships whether the gap is observable.
2. **OS/arch source.** Preflight (04) vs a new `evener/host/info` hub method.
   The method is clean and testable but adds wire surface; preflight keeps the
   wire stable but couples the probe to component 04.
3. **Nested hosts.** A remote hub that is itself a controller returns refs the
   controller namespace cannot represent (see §Error handling). Refuse, drop, or
   introduce a ref grammar change? Parent design allows a hub to be a host;
   cycles are refused, but depth-2 chains are not addressed.
4. **Attach-time cross-hub cycle detection (deferred).** Component 03 exposes
   `AddWithUpstreams(entry, upstreamNames)` so a cycle can be refused at the
   moment an upstream host list is learned, but v1 has no way to learn one: no
   AppWire method reports a hub's configured hosts, and the attach handshake
   returns server info, protocol version, source ID, and features only
   (`InitializeResponse`, `appwire/types.go:227-233`). So v1 refuses what the
   config alone can see (duplicate names, self-edges) and does **not** perform
   A→B→A attach-time detection. Closing this needs a new `ScopeHub` host-list
   method plus a call from the attach path into `hostreg.AddWithUpstreams`;
   component 03 records the same deferral.
5. **Controller-side past/recovery fencing.** Non-local refs bypass the
   controller's past index, deletion fence, and recovery admission
   (`app_sources.go:163-200`, `app_sources.go:283-292`). Remote sessions
   therefore have no controller-side dormant view; confirm that the fleet view
   (06) is acceptable as the only source of last-known remote state.
6. **One client vs per-call clients.** A long-lived client amortizes SSH setup
   but must survive reconnects (component 04) and re-`initialize`; confirm the
   reconnect contract the SSH manager exposes to sources.
7. **Capability caching / refresh.** Probe once at attach, or re-probe on a
   timer and on reconnect? Auth/model/plugin state changes on the host between
   probes.
