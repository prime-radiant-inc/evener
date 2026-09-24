# Component spec 07 — Remote administration (config proxy + credential push)

Parent: `2026-09-14-multi-host-evener-design.md` (§2 "Configuration", §4 item 7,
§5, §6).
Siblings: `2026-09-14-multi-host-02-attach-bridge.md`,
`2026-09-14-multi-host-03-host-config.md`. Depends on components 02, 03, 04, 05.
Spikes: `2026-09-14-multi-host-spikes-findings.md`.

## Purpose

Let the controller's Hub UI administer a **remote host's own configuration**
while that configuration stays **host-owned** (design §2). Two actions:

1. **Config proxy** — the controller's web edge forwards the browser's
   configuration RPCs to the remote host hub, which executes its own existing
   controllers and returns the result. The controller interprets nothing about
   providers, launch config, plugins, or credentials; it is a router.
2. **Credential push** — an explicit, per-host "copy credentials to this host"
   action that reads the *local* hub's `credentials.toml` and replays each
   provider-instance key through the *remote* hub's own
   host-side `evener/auth/apiKey/conditionalSet`, so the host classifies and
   writes its file atomically with the correct mode. Merge, do not clobber;
   report added/updated/skipped per instance.

The controller adds **no canonical credential or config store**: it reads the
existing local store and writes the remote one.

## Scope

- A hub-scoped proxy method on the controller that forwards one admin RPC to a
  named host's hub over the component-02/04/05 channel, plus host validation and
  offline refusal.
- Fan-out of the remote hub's config notifications
  (`evener/auth/updated`, `evener/launch/updated`,
  `evener/marketplace/updated`, `evener/plugin/updated`,
  `evener/settings/agentsDoc/changed`) to the controller's browser clients,
  tagged with the host, so remote settings panes refresh. Instance mutations
  broadcast **`evener/auth/updated`**, not an `evener/instance/updated`: there is
  no such AppWire method. `notifyInstanceUpdated` (`app_rpc.go`) emits
  `appwire.NotifyEvenerAuthUpdated` with an empty payload, so building the proxy
  around `evener/instance/updated` would silently forward nothing after an
  instance edit.
- A controller-side credential-push RPC that reads the local credentials file
  and writes the remote one through the remote hub's host-side
  `evener/auth/apiKey/conditionalSet`, returning a per-instance report.
- Frontend host scoping for the existing settings panes that call these RPCs:
  providers/credentials, launch config, plugins/marketplaces/skills, agents-doc
  (see "Contract" for the exact method families).
- The OpenAI Codex OAuth caveat: device-code login **on the host** is the
  reliable path; a direct token-file copy is best-effort and warned.
- A dedicated ref-translating dispatch for `evener/thread/forceStop`, so a
  UI force-stop on a `host:<thread>` reaches the owning host instead of being
  rejected by the controller's local-only check — separate from the generic
  proxy, see §"Dedicated ref-translating dispatch".
- The **host-routing origin guard**: every controller path that connects to a
  remote host — this proxy, the credential push, the
  `evener/thread/forceStop` non-local branch, component 06's
  `evener/host/attach`, and any future remote dispatch — refuses a
  remote-originated request **before dialing a remote host**, so the
  component-05 loop guard bounds admin routing too
  (§"Host-routing origin guard").

## Non-scope

- Opening the SSH channel, keepalive, reconnect, deploy, version match —
  component 04. The proxy consumes an already-attached host.
- The `RemoteHubSource`, ref translation, and thread/turn call mapping —
  component 05. This component reuses the same per-host client but does not
  define it.
- The `[[hosts]]` schema, validation, and registry — component 03.
- The fleet view, host picker in the new-session form, and offline/dormant
  session rendering — component 06.
- A new remote config format or a controller-side mirror of remote config.
  Configuration is host-owned (design §2); nothing is cached on the controller
  beyond the connection entry.
- Remote *tool execution* (`agent/execenv`) — design §Non-goals.

## Contract / interfaces

### Which existing host RPCs are proxied

The controller proxies the host hub's own hub-scoped handlers, unchanged. The
handler registrations are:

| Family | Handler source | Registration | Protocol rows |
| --- | --- | --- | --- |
| Provider instances | `cmd/evener-hub/app_instances.go` (`hubInstancesController`) | `app_rpc.go` | `appwire/protocol.go` |
| Launch config | `cmd/evener-hub/app_launch.go` (`hubLaunchController`) | `app_rpc.go` | `appwire/protocol.go` |
| Plugins / marketplaces | `cmd/evener-hub/app_plugins.go` (`hubPluginsController`) | `app_rpc.go` | `appwire/protocol.go` |
| Auth / credentials | `cmd/evener-hub/app_auth.go` (`hubAuthController`) | `app_rpc.go` | `appwire/protocol.go` |
| Agents doc | `cmd/evener-hub/app_rpc_agents_doc.go` (`registerAgentsDocHandlers`) | `app_rpc_agents_doc.go` (called from `app_rpc.go`) | `appwire/protocol.go` |

Concretely the proxied method names are (all `ScopeHub` in
`appwire/protocol.go` unless noted):

- `evener/instance/{list,create,edit,remove,setDefault}`
- `evener/launch/{resolve,schema,getLayer,setLayer,trustRepo}`
- `evener/marketplace/{list,add,remove,refresh,edit,browse}` and
  `evener/plugin/{list,install,upgrade,remove,enable,disable,setAutoUpgrade,preview,checkNow}`
- `evener/auth/{status,test,list,login/start,login/complete,logout,apiKey/set,apiKey/conditionalSet,apiKey/clear,credentialJson/set,device/start,device/poll}`
- `evener/settings/agentsDoc/{get,set}`
- **Host-dependent discovery** — the spawn form's remote-scoped calls
  (component 06, §"Frontend changes"): `evener/paths/complete`,
  `evener/path/validate`, `evener/dirs/create`, `evener/projects/recent`,
  `evener/harnesses/list`, `evener/spawn/slashCatalog`, `evener/git/head`,
  `evener/plugin/preview`, `evener/instance/list`, and `model/list`
  (`model/list` is `ScopeBoth`, `appwire/protocol.go`; the rest are `ScopeHub`;
  all forwarded to the host hub, which serves them against its own local
  environment). `evener/git/head` is the branch/location chip's read
  (`frontend/src/shell/gitLocation.ts`), `evener/plugin/preview` the
  plugin-preview panel (`panes/spawn/usePluginPreview.ts`), and
  `evener/instance/list` the provider-instance list (`stores/credentials.ts`).
  The remote settings panes call the
  filesystem helpers directly — `evener/path/validate`
  (`frontend/src/stores/launchConfig.ts:93`,
  `src/stores/extensions.ts:384`), `evener/dirs/create`
  (`src/stores/extensions.ts:388`), and `evener/paths/complete`
  (`src/stores/extensions.ts:393`) — and would otherwise fail closed with
  `appwire.InvalidParams`, breaking path validation, auto-completion, and
  directory creation in remote panes. **Implementation status:** landed — the
  07a allow-list (`remoteHostAdminMethods`, `cmd/evener-hub/app_host_admin.go`)
  carries this discovery set beside the five admin families, and the checked-in
  `cmd/evener-hub/host_request_methods.txt` is the spawn-form subset that the Go
  and TypeScript parity tests both read.
  This is the same discovery set component 06 enumerates; the two must be a
  single shared source of truth or covered by a scripted-host parity test that
  forwards each method through the envelope (component 06, §"Frontend changes").

The instance family has exactly those five handlers — the catalog has no
`evener/instance/setModelDisabled` and no `evener/instance/refreshModels`
(`protocol.go`), so the allow-list must not name them. Likewise the
plugin family is the nine above, including `evener/plugin/disable` and
`evener/plugin/setAutoUpgrade` (`protocol.go`), which the pane's UI can
reach and which were missing here.

Notes on the seams:

- `hubInstancesController` is "the only writer of providers.toml"
  (`app_instances.go`); reads go through `registry.ReadConfigFile`
  (`app_instances.go`, `llm/registry/write.go`) and writes through
  `registry.WriteConfigFile` (`app_instances.go`, `llm/registry/write.go`).
  This is exactly why the proxy forwards rather than reimplements: the host's
  registry owns the file.
- `hubLaunchController` refuses a launch-layer `env` key that looks like a
  credential (`app_launch.go`), so the proxy must surface that refusal
  verbatim; it must not launder it into a generic error.
- The auth controller already refuses a stored key under a Codex instance
  (`app_auth.go`) and under a gcp-adc instance —
  the credential push relies on these host-side refusals.

### Proxy method

The AppWire `Request` envelope carries only `ID`, `Method`, and `Params`
(`appwire/jsonrpc.go`), and hub-scoped methods carry no ref, so there is
**no existing host dimension** on an admin call. This component adds one. New
hub-scoped method, one protocol row in `appwire/protocol.go`:

```go
// evener/host/request — forwards one hub-scoped admin RPC to a named host.
type HostRequestParams struct {
    Host   string          `json:"host"`             // component-03 source ID
    Method string          `json:"method"`           // e.g. "evener/instance/list"
    Params json.RawMessage `json:"params,omitempty"` // the admin method's params
}
```

The handler:

1. resolves `Host` through `hostreg.Registry.Get` (component 03); unknown →
   `appwire.InvalidParams`;
2. refuses when the host is not attached (component 04/05 exposes attachment
   state) with `appwire.Unavailable`, matching the offline rule (design §2:
   "actions refused until reconnect");
3. allow-lists `Method` against an **exact set of method names**, not family
   *prefixes*. The set is the concrete enumeration above:
   `evener/instance/{list,create,edit,remove,setDefault}`,
   `evener/launch/{resolve,schema,getLayer,setLayer,trustRepo}`,
   `evener/marketplace/{list,add,remove,refresh,edit,browse}`,
   `evener/plugin/{list,install,upgrade,remove,enable,disable,setAutoUpgrade,preview,checkNow}`,
   `evener/auth/{status,test,list,login/start,login/complete,logout,apiKey/set,apiKey/conditionalSet,apiKey/clear,credentialJson/set,device/start,device/poll}`,
   `evener/settings/agentsDoc/{get,set}`, plus the host-dependent discovery set:
   `evener/paths/complete`, `evener/path/validate`, `evener/dirs/create`,
   `evener/projects/recent`, `evener/harnesses/list`,
   `evener/spawn/slashCatalog`, `evener/git/head`, and `model/list` — and
   nothing else. (`evener/plugin/preview` and `evener/instance/list` are also
   host-dependent discovery calls — the spawn pane's plugin-preview panel and
   its provider-instance list — and are already in the exact set through the
   plugin and instance families above; they are named here so component 06's
   discovery set and this allow-list enumerate the same names.) **The whole
   discovery set was the addition to the 07a allow-list** (now landed, per the
   implementation-status note above): `evener/plugin/preview` and
   `evener/instance/list` were already present through their families, so the
   genuinely new names were
   `evener/paths/complete`, `evener/path/validate`, `evener/dirs/create`,
   `evener/projects/recent`, `evener/harnesses/list`,
   `evener/spawn/slashCatalog`, `evener/git/head`, and `model/list`. Without
   them a wrapped discovery call fails closed with `appwire.InvalidParams`; an
   unwrapped `evener/git/head` in particular reads the controller's git
   repository for a remote path.
   A prefix match (`strings.HasPrefix(method, "evener/instance/")`) is **not**
   acceptable: it would auto-allow a future sensitive `ScopeHub` method the
   moment it is added to the catalog (a hypothetical
   `evener/instance/setModelDisabled` or `evener/instance/deleteAll`), turning
   the proxy into a generic tunnel by default. The allow-list must also **fail
   closed**: an unlisted method — including every method added to the catalog
   after this list was written — is `appwire.InvalidParams` and is never
   forwarded. The proxy must not become a generic hub-to-hub RPC tunnel
   (design §6 "secret handling"; the remote hub is a trusted peer but the
   browser is not);
4. calls the per-host `appwire.Client.Request(ctx, Method, Params, &out)`
   (`appwire/client.go`) and returns `out` as the browser response, passing
   wire errors through unchanged.

Before any of the above, the handler applies the shared host-routing origin
guard (§"Host-routing origin guard"): a **remote-originated** request
(`origin` non-empty) is refused typed with no dial, because `Host` always
names a remote host. A local-originated request proceeds as above.

The per-host client is the one components 04/05 already maintain for
`RemoteHubSource` (created the way `spike/client/main.go` and
`appsource/local_daemon.go` create one: `appwire.NewClient(transport)`).
Component 05 must expose it (or an equivalent admin-call accessor); this spec
does not define that accessor.

**Alternative considered.** Add an optional `host` field to every admin params
struct, or open a per-host browser WebSocket (`/rpc?host=…`). A wrapper method is
one protocol row and one handler and leaves the ~40 param structs untouched; a
per-host socket would multiply the browser handshake. See "Open questions".

### Dedicated ref-translating dispatch (`evener/thread/forceStop`)

`evener/thread/forceStop` (`MethodEvenerThreadForceStop`, `ScopeHub`) is the one
admin-family method that is neither forwarded through `evener/host/request` nor
served by the controller. The controller's `forceStopThread`
(`cmd/evener-hub/app_force_stop.go`) verifies and signals a **local daemon
process** through the hub's own `DaemonProcesses`/`ResumeLocks` ownership and
refuses any ref whose `SourceID` is not `"local"`
(`appwire.InvalidParams("force stop requires a local session ref")`). A raw
forward of a `host:<thread>` ref therefore fails on the host hub too, and a
forward of a bare `local:<thread>` ref would target the wrong machine's daemon
(component 05, §"Method-coverage analysis"). Component 05 explicitly defers
remote force-stop to this section; the earlier spec pointed there but defined no
handler, leaving the call rejected with "force stop requires a local session
ref".

Requirement: `forceStopThread` gains a non-local branch that

1. parses the ref and, when `SourceID` names a configured host, resolves it
   through `hostreg.Registry.Get` (component 03); unknown host →
   `appwire.InvalidParams`;
2. refuses an unattached host with `appwire.Unavailable` (the same offline rule
   as the proxy, §"Error handling");
3. translates the ref to `appwire.Ref{SourceID: "local", ThreadID: ref.ThreadID}`
   and issues `evener/thread/forceStop` **on that host's hub** through the
   per-host `appwire.Client` (components 04/05's shared client) — **not** through
   `evener/host/request`, which disclaims ref translation and must not carry it;
4. maps the remote response/error back and returns it (a remote
   `InvalidParams`/`Unavailable` surfaces unchanged, naming the host).

The **local** branch (the shipped `SourceID == "local"` path) is unchanged. A UI
force-stop on `host:<thread>` therefore terminates the wedged daemon on its own
host instead of being rejected. `evener/thread/forceStop` is **not** added to
the `evener/host/request` allow-list.

**Implementation status:** the shipped `forceStopThread` (`app_force_stop.go`)
rejects every non-local ref, and component 07 does not yet add this branch, so a
remote force-stop is rejected today. The branch is the implementing PR's
requirement.

**Origin guard.** Before step 3 dials the host, the non-local branch applies the
shared host-routing origin guard (§"Host-routing origin guard"): a
remote-originated `forceStopThread` request is refused typed and no call is
issued, so a peer hub cannot make this hub signal another host's daemon. The
**local** branch is unaffected — a remote-originated request naming a `local:`
session is served from this hub's own local state, exactly as component 05's
loop guard specifies.

### Host-routing origin guard (all remote dispatch)

Component 05 (§"Ref translation detail"; §Error handling "Loop-guard refusal")
bounds fan-out at depth 1 by refusing, at the typed fan-out seam, to route a
request whose request-context `origin` is non-empty to any source other than
`local`. Stated for the thread/turn fan-out paths, that guard must also cover
**every controller path that connects to a remote host**, because a consumer hub
that is itself attached *as a host* (design §2 "Topology", the A→B→A case) can
otherwise use the admin surface to make its controller contact a third host: a
request that arrives at hub B over the attach bridge (`origin` non-empty) and
names host C in `evener/host/request` — or triggers the credential push, a
remote `forceStop`, or a host attach — would make B dial C, bypassing the
depth-1 cap.

Requirement: the guard is enforced at **one shared host-routing seam** that
every remote dispatch passes through — the per-host client accessor (component
05's exposed admin accessor) or a single `routeToHost(ctx, hostID, …)` helper
beside it — **not** by a per-handler check that a new remote path can forget.
The seam refuses, with a typed `appwire.InvalidParams` naming the origin, any
call that would contact a remote host when the request's `origin` is non-empty.
Concretely:

- `evener/host/request` (§"Proxy method") — `Host` always names a configured
  remote host, so every remote-originated call is refused; the method is never
  served from another host and never falls back to local execution.
- `evener/host/pushCredentials` (§"Credential push method") — refused; it is a
  remote dispatch, so a remote-originated push cannot copy credentials onto a
  host the caller's own controller reached.
- the `evener/thread/forceStop` non-local branch (§"Dedicated ref-translating
  dispatch") — refused; it forwards to the owning host's client.
- component 06's `evener/host/attach` (which calls `sshManager.Ensure`) —
  refused; a remote-originated request may not make this hub attach a new host.
- the `evener/host/notification` fan-out (§"Notification envelope") does **not**
  contact a remote host — it re-emits a host's own notification to this hub's
  local browser clients — so it is unaffected: the guard gates *remote
  dispatch*, not local re-emission. A future path that re-emits to another
  remote source is gated by the same seam.
- **Any future remote dispatch path passes through the seam.** A new
  controller-side method that dials, feeds, or forwards to a remote host must
  call the shared seam, so the guard cannot be omitted by construction.

A **local** request (`origin` empty — an ordinary browser, TUI, or CLI session
of this hub) is unaffected: the proxy, credential push, force-stop, and attach
all proceed as specified above. This mirrors component 05's rule that a
remote-originated request is served from the **recipient hub's own local state
only**; the admin surface has no local-state variant of a remote-host action, so
the correct disposition for a remote-originated remote-dispatch request is the
typed refusal.

**Implementation status:** landed — the origin travels in the request context,
stamped once at the hub's `/rpc` edge from the attach bridge marker
(`X-Evener-Bridge`) by `cmd/evener-hub/host_routing_origin.go`; the value lives
in `appsource.WithHostRoutingOrigin`/`HostRoutingOrigin` because the guard's
shared dispatch seam reads it too. Both halves are in place: the dial half
(`guardRemoteHostDial`, `cmd/evener-hub/host_routing_origin.go`) and the
dispatch half (`appsource.guardRemoteDispatch`,
`cmd/evener-hub/internal/appsource/host_routing_origin.go`). They emit the
same typed refusal shape - `appwire.InvalidParams` naming the origin and the
action it may not take - but not from one shared function: `refuseRemoteOrigin`
is unexported in package `hub` and formats a per-action phrase, while
`guardRemoteDispatch` builds its own message in package `appsource`. Changing
one does not change the other.

**Coverage:** a scenario test that injects a remote-originated
`evener/host/request` — and the credential push, the non-local force-stop, and
`evener/host/attach` — and asserts each is refused typed **before any remote
dial** (a per-host client spy records no request), while a local-originated call
proceeds; plus an assertion that every remote-dispatch entry point routes
through the shared seam.

### Notification envelope

The controller re-emits each config notification it owns to its browser clients
tagged with the source host, through a wrapper method symmetric with
`evener/host/request`:

```go
// evener/host/notification — re-emits one host-owned config notification.
type HostNotificationParams struct {
    Host   string          `json:"host"`   // component-03 source ID
    Method string          `json:"method"` // the original notification method
    Params json.RawMessage `json:"params,omitempty"`
}
```

- `Method` is one of the config notifications named in §Scope
  (`evener/auth/updated`, `evener/launch/updated`,
  `evener/marketplace/updated`, `evener/plugin/updated`,
  `evener/settings/agentsDoc/changed`); the fan-out must not wrap any other
  notification. `notifyInstanceUpdated`'s reuse of `evener/auth/updated` carries
  through unchanged as the wrapped `Method` (§Scope).
- `Host` is always present and is the only way a store tells two hosts apart.
- **Compatibility.** Local notifications keep their existing method and params
  (the `notifyAuthUpdated`/`notifyLaunchUpdated`/… `BroadcastAll` path is
  unchanged); the wrapper is used **only** for remote hosts. A store that is not
  host-scoped therefore never sees remote traffic and cannot misapply it, and an
  existing handler for a bare `evener/auth/updated` keeps firing for the local
  host only. The host-scoped stores added in 07b subscribe to
  `evener/host/notification` and apply a payload only when `Host` equals the
  currently selected host.
- **Catalog registration.** `evener/host/notification` must be added to the
  AppWire notification catalog (`appwire/protocol.go`, `Notifications`) with
  `HostNotificationParams` as its params type, and the generated Go and
  TypeScript bindings regenerated (`make generate`), so typed clients can
  subscribe to it. Without the catalog entry neither the Go client nor the
  generated TS types can name or type this notification — it is the same
  registration every other hub-originated notification carries.
  **Implementation status:** landed — `evener/host/notification` is in the
  AppWire notification catalog (`appwire/protocol.go`) with
  `HostNotificationParams` (`appwire/types.go`), and the hub-side fan-out emits
  it (`cmd/evener-hub/app_host_admin.go`) for the host-owned config
  notifications: auth/updated, launch/updated, marketplace/updated,
  plugin/updated, and settings/agentsDoc/changed. The client-side unwrapping
  into host-scoped stores is 07b.

### Credential push method

New hub-scoped method on the controller:

```go
// evener/host/pushCredentials — copies local provider-instance keys to a host.
type HostPushCredentialsParams struct {
    Host string `json:"host"`
}
type HostPushCredentialsResponse struct {
    Host    string                    `json:"host"`
    Results []HostCredentialPushResult `json:"results"`
}
type HostCredentialPushResult struct {
    Instance string `json:"instance"`
    Action   string `json:"action"` // "added" | "updated" | "skipped" | "failed"
    Reason   string `json:"reason,omitempty"`
}
```

Read side (local):

- `credentials.LoadStore(cmdutil.CredentialsPath())` (`internal/credentials/store.go`,
  `cmdutil/registry.go`), or the hub's already-loaded `cfg.CredsStore`
  (`cmd/evener-hub/main.go`, `internal/hubcore/config.go`).

The push is a remote dispatch, so it is gated by the shared host-routing origin
guard (§"Host-routing origin guard"): a remote-originated
`evener/host/pushCredentials` is refused typed before any host is dialed.
- `Store.Names()` (`store.go`) lists the file-layer entries; `Store.Get(name)`
  (`store.go`) returns one value. `Store.Set` writes atomically at mode `0600`
  (`store.go`) — but the push does **not** write the
  local store; it only reads it.

**The instance→provider join rule (required, unambiguous).** The local store is
keyed by **instance name**, and the wire APIs are keyed by `Provider`; the join
is **identity on that name**, not a lookup through `Base`/`ProviderID`.
Concretely:

- The unit of the push is the local credentials-store entry (`Store.Names()`);
  its key **is** the instance name. It is sent verbatim as the wire `Provider`
  value **only to the two provider-keyed auth methods** — `evener/auth/status`
  and `evener/auth/apiKey/conditionalSet`. `evener/instance/list` accepts
  `EmptyParams` and has no provider/instance parameter, so the pusher calls it
  **once** with `{}` and joins the returned entries against the store keys
  (`InstanceEntry.Name` for the explicit-instance match,
  `AvailableProviders[].ID` for the implicit-provider fallback — both below);
  the instance name is never passed to `instance/list`.
- The remote resolves `Provider` the way `hubAuthController.Status` does
  (`app_auth.go`): first `registry.Instance(name)` (an explicit instance), then
  the implicit-provider fallback `registry.Provider(name)` when that provider is
  `Implicit`. A name that is neither is "not present on the host".
- The remote `evener/instance/list` is the authority for "present": an explicit
  instance is matched by `InstanceEntry.Name`; the implicit-provider fallback is
  matched by `AvailableProviders[].ID`. The push **never** keys on
  `InstanceEntry.ProviderID` or `Base`: several instances can share a `Base`
  (and thus a `ProviderID`), so keying by provider would collapse them and write
  one instance's credential into another's slot.
- When a local store key has **no** remote counterpart (neither a `Name` nor an
  implicit provider `ID`), the result is **skipped** with reason "no matching
  instance on the host" — the table row below. The local key is not pushed under
  a guessed provider.

**Implementation status:** landed on the controller side and the wire -
`cmd/evener-hub/app_host_credentials.go` implements this join over
`evener/auth/status` and `evener/auth/apiKey/conditionalSet`. The pane action
that starts a push from the remote-credentials sheet is its own follow-up change
(the frontend bullet in this component's plan below), so this note records what
the controller and the wire contract do, not the whole of 07c. The shipped wire
types (`AuthStatusParams.Provider`,
`AuthApiKeySetParams.Provider`, `InstanceEntry.Name/Base/ProviderID`,
`appwire/types.go`) still do not disambiguate instance from provider, and they
do not need to: the join is resolved once, locally, against `instance/list`.
Two refinements the implementation carries beyond the rule above: the join's
*lookup* folds case (the local store lowercases its keys, the host spells its
instances as authored) while the `Provider` sent is the host's own spelling of
the matched entry, and exactly one kind of entry is skipped without dialing at
all - one whose stored value is not an API key (the store also holds the Google
credential JSON `evener/auth/credentialJson/set` writes, which must never be
copied to another host as a key). That skip is about the KIND of value, not about
a scheme's capability: every other matched entry goes through `evener/auth/status`
and `evener/auth/apiKey/conditionalSet`, and the host's own locked classification
decides - so an instance whose scheme cannot consume a key comes back as the
host's typed `skipped`, never judged from the instance-list snapshot.

Write side (remote): for each local entry (whose key is the instance name), call
the remote hub's **`evener/auth/apiKey/conditionalSet`** with
`{provider: <name>, value, expectedSource, expectedRevision}` — `provider` is
that same instance name and `value` is `Store.Get(name)` (`appwire/types.go`)
via the same `evener/host/request` channel. The host's conditional-set handler
(`app_auth.go`) validates under `credentialWrite`, re-resolves `ActiveSource`
and the config revision, classifies, and — only when the write is permitted —
calls `c.setCredential` (= `creds.Set`), reloads the registry, and returns
`ApiKeyConditionalSetResponse`. The host therefore writes its own
`credentials.toml` atomically with the correct mode; the controller never sees
the file path or writes it. A bare `evener/auth/apiKey/set` is the old
unconditional path and is **not** what the pusher calls; the classification and
the write are one locked host-side operation (see "The no-clobber guarantee is
atomic" immediately below), and the two-call "read `evener/auth/status`,
classify, then set" form is **not** the contract — the read may still precede
the write to capture `ConfigRevision` for `ExpectedRevision` (below); what must
not happen is classifying from it.

**Merge / no-clobber policy.** The pusher may read the remote
`evener/auth/status` for an instance (`app_auth.go`, `AuthStatusResponse`,
`appwire/types.go`) to render the report **and to capture the instance's
`ConfigRevision` for `ExpectedRevision`** (§"Where `ExpectedRevision` comes
from"); it does **not** gate the write on that read. The permit/skip decision
is made by the host inside the one
locked conditional set (below), which re-resolves the source itself. A write is
permitted **only** when the host resolves the instance's credential from the
file layer or has none; every other source is skipped, because the pushed key
either cannot be used or would silently change which credential is in force.
Remote resolution order is
`api_key` > `credential_headers` > `store` > `env:<VAR>` (`registry.credential`,
`llm/registry/instances.go`):

The classification the **host** applies — re-resolved inside the one locked
conditional set, not read by the client before a separate write:

| Condition on the host (re-resolved under the credential write lock, top to bottom) | Action |
| --- | --- |
| instance is Codex-OAuth or gcp-adc style (`oauth`/`adc` or the scheme's transport) | **skipped** — the host's `apiKey/set` would refuse it (`app_auth.go`); classify host-side so the report is a skip, not an error |
| instance not present in the remote `evener/instance/list` | **skipped** — no matching instance on the host |
| `ActiveSource == "api_key"` or `"credential_headers"` | **skipped** — the instance resolves from `providers.toml`, which *outranks* the file layer, so a pushed key would be shadowed and change nothing |
| `ActiveSource == "env:<VAR>"` | **skipped** — the host operator's environment supplies a working credential; a file-layer write outranks `env:` and would silently replace it |
| `ActiveSource == "store"` (implies `HasStoredFile == true`) | **updated** — write; the host overwrites its own file-layer key |
| `ActiveSource == "none"` **and the instance's auth scheme is key-capable** | **added** — write; the instance has no credential today and can consume an API key |
| `ActiveSource == "none"` **and the instance uses `AuthNone`** | **skipped** — the host's `apiKey/set` refuses a key for an auth-none instance (`app_auth.go`: "authenticates without a credential"), so a pushed key would be one nothing reads; classify host-side so the report is a skip, not an error |
| the conditional set returns a wire error | **failed**, with the wire error text |

`"none"` is therefore **not** writable unconditionally: it is writable only
when the instance's scheme can actually consume an API key. `AuthNone`
(and an auth-none transport generally) deliberately reads no credential, so its
`apiKey/set` rejects the write (`app_auth.go`); permitting the write for it would
either fail on the host or store a credential nothing sends. Codex-OAuth and
gcp-adc instances are already skipped by the rows above for the same reason.

"Merge" means the host's other file-layer entries are never deleted — only
`conditionalSet` is used, never `apiKey/clear`, and no whole-file replace exists.
"Don't clobber" means no write at all unless the remote resolves from the file
layer or has no credential: a working `api_key`, `credential_headers`, `oauth`,
`adc`, or `env:<VAR>` credential is never shadowed by a pushed key. The policy
must be applied **atomically on the host** — not from a separate
`evener/auth/status` read — so it is a guarantee rather than a check-time
snapshot; see the atomic contract immediately below. The controller makes no
pre-wire decision about a scheme's capability at all: its one pre-wire decision is
about the KIND of value (an API key is sent, a credential document is not), and
everything else the host's locked conditional set decides.

**The no-clobber guarantee is atomic — the check moves host-side.** The
two-call form (the pusher reads `evener/auth/status`, classifies, then writes
`evener/auth/apiKey/set`) is check-then-act across two independent proxy RPCs
and is **not** sufficient: the host's `ApiKeySet` takes the credential write
lock (`credentialWrite`, `app_auth.go:453` and `app_auth.go:396`) but does
**not** re-resolve `ActiveSource` under it, so a credential whose source changes
between the two calls — an operator exporting `env:<VAR>`, a `providers.toml`
edit adding an `api_key`/`credential_headers` entry, a concurrent push to the
same host — is never seen, and the file-layer write then shadows the credential
that appeared after the check. The required contract is therefore a **host-side
conditional/CAS set**: the host re-resolves the instance's credential source
under the same credential write lock it writes with, and **refuses** the write
when the source is no longer the file layer (or when an expected configuration
revision the set validated has changed). Check and write happen inside that one
locked operation, so no concurrent change can slip between them and "don't
clobber" becomes a guarantee rather than best-effort at check time. Concretely
the controller must call a new host-side method
`evener/auth/apiKey/conditionalSet` — not `evener/auth/apiKey/set` — with
`ApiKeyConditionalSetParams{Provider, Value, ExpectedSource, ExpectedRevision}`
(`appwire/types.go`). Under `credentialWrite` the host re-resolves
`ActiveSource`/`HasStoredFile` and the instance's configuration revision; it
writes only if the source is still the file layer (or the instance still has
none) and any `ExpectedRevision` the controller observed is unchanged, and
otherwise refuses without writing. Its response,
`ApiKeyConditionalSetResponse{Action, Reason, Status}` (`Action` ∈
`added`|`updated`|`skipped`; `Reason` a human-readable string; `Status` the
post-write `AuthStatusResponse`), is the surface the push report reads: a
`skipped` classification is delivered as a successful typed response, distinct
from a wire error (`failed`). The `evener/auth/status` read that **supplies
`ExpectedRevision` happens before the `conditionalSet`** (§"Where
`ExpectedRevision` comes from"); any status read *after* the write renders the
report only — use the response's `Status`, or a second read explicitly labelled
report-only — and never gates or fences the write.

**Where `ExpectedRevision` comes from (required, defined source).** The
`ExpectedRevision` the controller sends is not invented: the read-only
responses the pusher already makes expose the instance's configuration
revision. `AuthStatusResponse` and `InstanceEntry` (`appwire/types.go`) each
carry a `ConfigRevision string` — the same value the host re-resolves under
`credentialWrite` (a digest/counter over the instance's effective credential
configuration, stable while that configuration is unchanged). Per instance the
pusher's order is **normative**, and it is the order the flow diagram shows:

1. read `evener/auth/status` (and, for the implicit-provider fallback, the
   joined `evener/instance/list` entry) and capture `ConfigRevision`;
2. issue `evener/auth/apiKey/conditionalSet` carrying that captured value as
   `ExpectedRevision`;
3. render the report — from the `Status` the `conditionalSet` response already
   carries (`ApiKeyConditionalSetResponse.Status` is the post-write
   `AuthStatusResponse`), or, only when post-write state beyond that is
   genuinely needed, from a **second, explicitly report-only**
   `evener/auth/status` read.

Steps 1 and 2 must not be reversed: a status read taken after the write can
only echo the post-write revision (or, if no pre-write read happened, the zero
value), which silently removes the revision fence — exactly the check-then-act
hazard this section exists to close. The captured value populates a request
parameter only; it does not re-introduce a client-side gate, because the host
compares the echoed value to its own under the lock and refuses on any change.
A controller that has observed no revision (a first push, or a host response
that omits the field) sends the zero value, which the host interprets as "no
revision fence" — the source fence (`ExpectedSource`) still applies. A
revision the controller did not observe is never fabricated.

**Implementation status:** landed on the controller side and the wire -
`evener/auth/apiKey/conditionalSet` is in the AppWire catalog with
`ApiKeyConditionalSetParams`/`ApiKeyConditionalSetResponse`, and
`ConfigRevision` is exposed on both `AuthStatusResponse` and `InstanceEntry`
(`appwire/types.go`), populated from the host's effective
credential-configuration revision. `ExpectedRevision` is sourced from that
field, and the controller's push calls the conditional set rather than the racy
status-then-`apiKey/set` pair. The pane action that drives a push from the
remote-credentials sheet is a separate follow-up change (the frontend bullet
below), so this note records the host-side contract, not the whole of 07c.

**Honest limitation.** `AuthStatusResponse` never returns the stored key, so the
pusher cannot tell "same value" from "different value". `updated` is therefore
emitted whenever a remote file-layer key already exists, even when the value is
identical. See "Open questions".

### OpenAI OAuth caveat

The Codex OAuth record is `auth/<instance>.json` under the hub state root
(`auth/openai/storage.go`, `AuthFilePath`), and the curated instance is
`openai-codex` (`cmd/evener/openai_login.go`). The state root the hub uses
is `cmdutil.StateRootFromLookup` (`cmd/evener-hub/openai_state_dir.go`,
`hook` at `cmd/evener-hub/app_rpc.go`). `SaveAuth` writes it atomically
at mode `0600` (`auth/openai/storage.go`).

The record can be refresh/device-bound: an access/refresh token minted for the
controller's machine may not refresh on the host. Therefore:

- **Primary path:** sign in **on the host** through the remote hub's own
  `evener/auth/device/start` / `evener/auth/device/poll`
  (`app_auth.go`, registration `app_rpc.go`). Device-code is chosen
  headless by `EVENER_LOGIN_HEADLESS=1` (`envvars/envvars.go`, read at
  `cmd/evener/openai_login.go`), which is what a controller-driven,
  non-interactive SSH session needs. The host writes its own record via
  `SaveAuth`; nothing is copied.
- **Best-effort copy:** replaying the local record onto the host is offered only
  with an explicit warning that it may be rejected or expire immediately, and
  that the user should re-run the host device login if it fails. It is not part
  of the first push PR (see PR plan).

The credential push covers the **API-key file layer** only. OAuth/ADC
credentials are never pushed by "copy credentials"; the UI routes them to the
host login flow.

## Implementation approach (files/packages, cited seams)

Decompose component 07 into four landable PRs.

### PR 07a — proxy method + host validation + notification fan-out

- Add the protocol row and typed params in `appwire/protocol.go` and
  `appwire/types.go` beside the existing `ScopeHub` rows (`protocol.go`).
- New controller `hubHostAdminController` in a new
  `cmd/evener-hub/app_host_admin.go`, registered in
  `newHubAppServerWithNavigationAndTrace` beside
  `registerAuthHandlers`/`registerInstanceHandlers`/… (`app_rpc.go`).
- The controller takes the component-03 `hostreg.Registry` and the component-05
  per-host client accessor. It allow-lists methods against the **exact method
  set** enumerated under "Proxy method" (fail closed; no prefix matching); the
  allow-list is the security boundary.
- Add the dedicated `evener/thread/forceStop` ref-translating branch to
  `forceStopThread` (`cmd/evener-hub/app_force_stop.go`) as
  §"Dedicated ref-translating dispatch" specifies; it is **not** part of the
  `evener/host/request` allow-list.
- Notification fan-out: **subscribe to component 05's source-level notification
  broker**, never to `Client.Notifications()` directly (component 05, §"One
  notification consumer per client — the broker"). `Notifications()` is a single
  channel that component 05's `drainLoop` already reads; a second reader would
  race it and each would silently drop notifications the other needs. The broker
  hands this component each notification (or a copy on its own buffered
  channel), and the admin fan-out re-emits the config notifications it owns to
  the controller's browser clients through the `evener/host/notification`
  envelope (§"Notification envelope"). Registration is against the component-05
  `RemoteHubSource`, not a specific `appwire.Client`, so a reconnect's fresh
  client drain rebinds the consumer automatically; an admin-only consumer
  registered with no thread subscribers receives notifications because
  registration itself starts the drain. The existing broadcast
  helpers (`notifyAuthUpdated`, `notifyLaunchUpdated`,
  `notifyMarketplaceUpdated`, `notifyPluginUpdated`) are the model for the
  controller-side notification shape; `notifyInstanceUpdated` is *not* a
  distinct wire method — it broadcasts `evener/auth/updated` (see §Scope).

### PR 07b — frontend host scoping for the settings panes

- The settings stores already call the admin RPCs:
  `cmd/evener-hub/frontend/src/stores/credentials.ts`
  (`evener/instance/*`, `evener/auth/apiKey/set`, …),
  `src/stores/launchConfig.ts` (`evener/launch/*`),
  `src/stores/extensions.ts` (`evener/marketplace/*`, `evener/plugin/*`).
- Introduce a host-context in the settings route (the settings nav is
  `src/panes/settings/sections.ts`; the panes are under
  `src/panes/settings/sections/`: `credentials/`, `launch-evener`,
  `marketplacesPlugins`, `skills`) and wrap these calls through
  `evener/host/request` when a remote host is selected, `local` (direct call)
  otherwise.
- The browser client is `src/protocol/client.ts`; a wrapped request still uses
  the existing `request(method, params)` surface, so the change is at the store
  boundary, not the transport. The same stores subscribe to
  `evener/host/notification` (§"Notification envelope") and refresh only when
  the notification's `Host` matches the selected host.

**Implementation status:** landed — the selected host lives in the settings
route rather than in a per-pane selector: `useSettingsHost`
(`cmd/evener-hub/frontend/src/panes/settings/settingsHost.ts`) reads the
route's `host` parameter (`HOST_QUERY_PARAM`,
`cmd/evener-hub/frontend/src/shell/routing.ts`), and the store mirrors the
route in both directions — an app-built navigation sets the selection to the
host its target names and drops it when the target is not a settings route, at
the moment the history changes and before any pane renders
(`syncSettingsHostToRoute`, registered with `navigate` in the same module) —
so the pane and the address bar never disagree. Every host-scoped pane reaches
the selection through the one shared frame `HostScopedSurface`
(`cmd/evener-hub/frontend/src/panes/settings/sections/hostScopedSurface.tsx`),
which is also what refuses an unknown or unattached host honestly: it says so
only once the registry reports `ready`, and until then renders that host's own
state instead of this hub's. A **remote** host's reads AND writes for each
family go through `evener/host/request` (`hostRequest`,
`cmd/evener-hub/frontend/src/stores/hostRouting.ts`) via one store instance
per host — `launchConfigStoreForHost`
(`cmd/evener-hub/frontend/src/stores/launchConfig.ts`),
`extensionsInstanceForHost`
(`cmd/evener-hub/frontend/src/stores/extensions.ts`) and
`agentsDocStoreForHost` (`cmd/evener-hub/frontend/src/stores/agentsDoc.ts`) —
rather than one store with a host-routing port, because the launch option
schema cache is server-global and a shared instance would serve one host's
schema for another. A `local` selection keeps today's direct path
byte-for-byte. The stores act on an `evener/host/notification` frame only when
its `params.host` matches the selected host.


### PR 07c — credential push

- New file `cmd/evener-hub/app_host_credentials.go` with the push controller and
  the classification table above.
- Read the local store through `cfg.CredsStore` or
  `credentials.LoadStore(cmdutil.CredentialsPath())`; enumerate with `Names()`
  and `Get()` (`internal/credentials/store.go`).
- Query the remote `evener/instance/list` once with `{}` through the proxy
  channel (it takes `EmptyParams`; the join against the local store keys is on
  the returned entries) and `evener/auth/status` per matched key **before the
  write**, to capture the instance's `ConfigRevision` (the `expectedRevision`
  source above), then **write through the new host-side
  `evener/auth/apiKey/conditionalSet`** with
  `{provider, value, expectedSource, expectedRevision}` — never the
  unconditional `evener/auth/apiKey/set`, which is not atomic with the
  no-clobber check. The host performs the classification and the write inside
  one `credentialWrite`-locked operation and returns
  `ApiKeyConditionalSetResponse{Action, Reason, Status}`. Every local store key
  is used verbatim as the wire `Provider` (the instance→provider join rule
  above); a key with no remote `Name`/implicit-`ID` counterpart is skipped. The
  report is rendered from that response's `Status` (or a second, explicitly
  report-only status read), not from the pre-write capture read.
- Add the push action to the remote-credentials pane in the frontend
  (`src/panes/settings/sections/credentials/`) - its own follow-up change, not
  part of the controller-side work the status notes above record as landed.
- No new on-disk state on the controller.

### PR 07d — host device-login affordance + best-effort OAuth copy warning

- Frontend: for a Codex instance on a remote host, offer "Sign in on host",
  which drives `evener/auth/device/start`/`device/poll` through the proxy and
  shows the verification URL/code; document `EVENER_LOGIN_HEADLESS=1`
  (`envvars/envvars.go`).
- Optional best-effort token copy, clearly warned, gated behind the same
  explicit action.

## Data flow

Admin proxy (read or mutate):

```
browser                 controller hub                        host hub (remote)
  |  evener/host/request  |                                       |
  | {host:"m4",method:    |  hostreg.Get("m4")  (03)              |
  |  "evener/launch/      |  attached? (04/05)                    |
  |   setLayer",params} --|-- allow-list method                   |
  |                       |  appwire.Client.Request(method,params)|  hubLaunchController.SetLayer
  |                       |-------------------------------------->|  (app_launch.go, app_rpc.go)
  |   response            |<--------------------------------------|  resolved config / wire error
  |<----------------------|                                       |
                          |  notification evener/launch/updated   |
                          |<--------------------------------------|
                          |  re-broadcast tagged host="m4"        |
                          |-------------------------------------->| browser refetches
```

Credential push:

```
local credentials.toml                           host hub (remote)
  credentials.LoadStore(CredentialsPath())          |
  Names()/Get()  (store.go)               |
        |                                           |
        |  evener/auth/status -------------------->| hubAuthController.Status (app_auth.go)
        |    captures ConfigRevision                |
        |    [+ the evener/instance/list entry      |
        |     for the implicit-provider fallback]   |
        |<-- ActiveSource, HasStoredFile, Revision -|
        |                                           |
        |  evener/auth/apiKey/conditionalSet ------>| conditional-set handler (app_auth.go)
        |    {provider, value,                      |   under credentialWrite:
        |     expectedSource, expectedRevision}     |   re-resolve ActiveSource/
        |                                           |   revision -> classify ->
        |                                           |   creds.Set (store.go) when allowed
        |                                           |   atomic save 0600 (store.go)
        |<-- ApiKeyConditionalSetResponse ----------|   Action/Reason/Status
        |    {action, reason, status}               |   (skipped is a typed response, not an error)
        |                                           |
  per-instance report -> browser (from the conditionalSet response's Status;
  a second auth/status read is report-only, never a fence)
```

Secrets flow controller→host over the AppWire channel only; the browser never
receives a key, and the controller writes nothing.

## Error handling

- Unknown host → `appwire.InvalidParams("unknown host %q")` from the proxy.
- Host not attached → `appwire.Unavailable`, matching design §2 offline rule.
  A proxy call to an offline host never falls back to local execution.
- Disallowed method → `appwire.InvalidParams`; the allow-list is checked before
  any forwarding.
- Remote dispatch from a remote-originated request → `appwire.InvalidParams`,
  refused before any dial by the shared host-routing origin guard
  (§"Host-routing origin guard") — the proxy, credential push, non-local
  force-stop, and host attach all share the one refusal.
- Remote wire errors are returned verbatim (`appwire.WireError` passthrough),
  including the launch credential-env refusal (`app_launch.go`) and the
  auth Codex/gcp-adc refusals (`app_auth.go`).
- Push: a failure for one instance does not abort the run; it is recorded as
  `failed` for that instance and the loop continues. A failure to read the local
  store fails the whole call before any remote write.
- A host whose `providers.toml` cannot be parsed keeps its own refusal
  (`app_instances.go`); the proxy surfaces it, it does not repair it.
- Channel loss mid-call follows component 04's reconnect policy; the in-flight
  call returns the client error.

## Testing

- **Proxy unit tests** (no SSH): drive `hubHostAdminController` against a
  scripted remote hub over an in-memory stream pair (the component-05 harness):
  method allow-list, unknown host, offline host, error passthrough, and that the
  returned result is the remote's.
- **Allow-list boundary test (fail closed).** Every method in the exact set is
  allowed; a `ScopeHub` method the list does not name — including one added to
  the catalog after the list was written — is denied with `appwire.InvalidParams`
  and never reaches the remote (assert no request is forwarded). Enumerate the
  allow-list against `appwire.CatalogMethodNames(appwire.ScopeHub)` so a catalog
  addition forces a deliberate allow/deny decision rather than a silent
  auto-allow.
- **Host-routing origin guard.** A request whose request context carries a
  non-empty `origin` (remote-originated) is refused typed for
  `evener/host/request`, `evener/host/pushCredentials`, the non-local
  `evener/thread/forceStop` branch, and `evener/host/attach` — assert the
  per-host client spy records **no** request and no `sshManager.Ensure` call;
  a local-originated request (empty `origin`) proceeds normally. Assert every
  remote-dispatch entry point routes through the shared seam.
- **Remote force-stop test.** A `host:<thread>` force-stop resolves the host,
  translates the ref to `local:<thread>`, issues `evener/thread/forceStop` on the
  host's client (assert the forwarded ref and that it does **not** go through
  `evener/host/request`), and returns the host's result; a local ref keeps the
  shipped `forceStopThread` path; an unknown/unattached host is refused typed.
- **Notification fan-out:** a scripted remote emits `evener/auth/updated`;
  assert one controller-side broadcast tagged with the host.
- **Push table test:** a fake remote that records `apiKey/conditionalSet` calls
  and reports scripted `status`/`instance/list`; assert the
  added/updated/skipped/failed matrix (a `skipped` arrives as a successful typed
  response, not a wire error), that `apiKey/clear` is never called, that
  **every matched store key whose value is an API key is routed through
  `apiKey/conditionalSet`** — the controller's only preflight skip is about the
  KIND of value (a credential document, never a key, is not sent), so a scheme's
  capability is never judged controller-side — and that an
  `api_key`/`credential_headers`/`env:`-resolving or OAuth/ADC instance is
  driven through the same call and asserted as a **typed `skipped` response**
  (scripted by the fake host, matching the classification table above) rather
  than skipped controller-side, and that remote-only instances survive (fake
  host store compared before/after).
- **Push ordering test (revision fence).** Against the fake remote, assert the
  per-instance call sequence: the `evener/auth/status` read (or the joined
  `evener/instance/list` entry for the implicit-provider fallback) that carries
  `ConfigRevision` is issued **before** `apiKey/conditionalSet`, and the value
  sent as `expectedRevision` equals what that read returned; assert a
  `conditionalSet` is never sent with a zero/missing revision when the scripted
  status response supplied one, and that the report is rendered from the
  `conditionalSet` response's `Status` (any post-write status read is
  report-only and cannot precede the write).
- **Secret hygiene:** assert no key value appears in the push response, in the
  controller log output, or in a rendered error, reusing the secret-marked
  registry (`envvars/envvars.go`, `Secret`) and `redactEnvSecrets`
  (`cmd/evener-hub/spawn.go`).
- **Local read test:** `credentials.LoadStore`/`Names`/`Get` against a temp
  store, including an absent file (empty store, `store.go`).
- **Frontend:** store tests asserting that a remote host wraps each admin call
  through `evener/host/request` and `local` does not; settings routing tests for
  the host context.
- **Live E2E** (gated, never in default `make test`): `EVENER_SSH_E2E=1` plus an
  explicit host, reusing the component-04/05 disposable-host setup. No live SSH
  in the default suite.

## Acceptance criteria

1. `evener/host/request` forwards each method in the five families to the named
   host's hub and returns the remote result; local execution never happens for a
   remote host request.
2. An unknown or unattached host is refused with a typed wire error; the
   allow-list matches method names exactly, rejects any method outside the
   enumerated set, and fails closed on a method added to the catalog after the
   list was written; every method the
   settings panes call — including `evener/plugin/disable`,
   `evener/plugin/setAutoUpgrade`, and `evener/settings/agentsDoc/{get,set}` —
   is inside it, the host-dependent discovery set (component 06) — including
   `evener/git/head`, `evener/plugin/preview`, and `evener/instance/list` — is
   inside it, and no instance method outside the five listed exists to call.
3. Remote config mutations reach the host's own store: an `evener/instance/create`
   proxied to `m4` lands in `m4`'s `providers.toml`, not the controller's
   (`app_instances.go`).
4. Remote config notifications refresh the browser's panes without a manual
   reload (tagged with the host).
5. Credential push reads the local store and writes the host's store only via
   the atomic host-side `evener/auth/apiKey/conditionalSet` (never the
   unconditional `evener/auth/apiKey/set`), so the no-clobber classification
   and the write happen in one `credentialWrite`-locked operation; the host
   file is written atomically at mode `0600`
   (host-side `store.go`). An instance whose remote credential resolves
   from `api_key`, `credential_headers`, `oauth`, `adc`, or `env:<VAR>` receives
   no write at all; only `store`, and `none` **for a key-capable scheme**, are
   writable. An `AuthNone` instance (`ActiveSource == "none"` with an auth-none
   transport) is **skipped**, never written: the host-side set refuses
   the key, and the `skipped`/`failed` distinction is carried by
   `ApiKeyConditionalSetResponse.Action`/`.Reason`. The revision fence is
   delivered, not merely declared: per instance the `ConfigRevision` capture
   read precedes the `conditionalSet` and its value is echoed as
   `expectedRevision`; a post-write status read is report-only and never
   substitutes for the pre-write capture, and the zero value is sent only when
   the read itself observed no revision.
6. The push report lists every local entry with `added`/`updated`/`skipped`/
   `failed` and a reason for skips; remote-only entries are preserved.
7. No key value appears in the push response, controller logs, or errors; the
   controller writes no new credential/config file.
8. Codex OAuth is handled by host device login (`EVENER_LOGIN_HEADLESS=1`); any
   token-file copy is warned and does not claim success on a refresh failure.
9. `go test ./cmd/evener-hub/... ./internal/credentials/...` and the frontend
   store tests pass with no live SSH.
10. A UI force-stop on a `host:<thread>` ref reaches the owning host: the
    controller translates the ref and issues `evener/thread/forceStop` on that
    host's hub (never `evener/host/request`), a `local:` ref keeps the shipped
    path, and an unknown/unattached host is refused typed — no call is rejected
    with "force stop requires a local session ref".
11. A remote-originated request (`origin` non-empty) can reach **no** remote
    host through any controller dispatch: `evener/host/request`, the credential
    push, the non-local `evener/thread/forceStop`, and `evener/host/attach` are
    each refused typed before any dial, at the one shared host-routing seam; a
    local-originated request is unaffected.

## PR size estimate (LOC)

- 07a proxy method + allow-list + registration + fan-out: ~200–320 LOC + tests
  (~250–350).
- 07b frontend host scoping: ~250–450 LOC + tests (~250–400).
- 07c credential push (controller + classification + UI action): ~250–400 LOC +
  tests (~300–450).
- 07d host device-login affordance + warned copy: ~120–220 LOC + tests
  (~150–250).

Total ≈ **1,800–2,800 LOC** across four reviewable PRs; each is independently
landable once components 02–06 expose the channel and per-host client. 07a is the
only one with a new RPC surface and should land first.

## Open questions

- **Routing convention.** A wrapper method (`evener/host/request`) vs an optional
  `host` field on every admin params struct vs a per-host browser socket
  (`/rpc?host=…`). The wrapper is smallest and centralizes the allow-list; a
  per-host socket would make fan-out and the host context implicit but multiply
  the browser handshake. Decide in 07a review.
- **Per-host client ownership.** Component 05 owns the `appwire.Client` for
  thread calls; does it expose it for admin calls, or does 07 own a second
  client per host? A shared client is cheaper but couples the two components'
  lifecycles; a second client doubles the SSH channels. This spec assumes a
  shared, exposed client.
- **Notification tagging (resolved).** Host-tagged notifications use the
  `evener/host/notification` wrapper `{host, method, params}`
  (§"Notification envelope"), not a new field on each config notification's
  params. Local notifications are unchanged; only remote re-emissions are
  wrapped, so a store that does not subscribe to the wrapper sees only local
  traffic.
- **Value identity.** `AuthStatusResponse` never returns the key, so `updated`
  is reported even for an identical value. Is that acceptable, or should the
  host-side `apiKey/conditionalSet` return a value-hash (in
  `ApiKeyConditionalSetResponse`) so the report can say `unchanged`? The host is
  the only side that could hash.
- **Instance reconciliation.** The host's `providers.toml` is host-owned and may
  not define the same instances as the controller. This spec skips a local entry
  with no remote instance; the alternative (push and let it fail, or create the
  instance) is a product call.
- **OAuth copy.** Whether the best-effort token copy ships at all, given it may
  be device-bound, or whether 07d stops at the device-login affordance.
- **Offline queueing.** Design §2 refuses actions while offline. Whether a push
  should be queued for reconnect or strictly refused is unstated; this spec
  refuses.
- **Hub-to-hub authorization.** The proxy relies on the remote hub trusting the
  bridge's capability token (spike findings: token trimmed before the bearer
  header). Whether remote admin RPCs need an additional authorization check
  beyond the attach token is not fixed here.
