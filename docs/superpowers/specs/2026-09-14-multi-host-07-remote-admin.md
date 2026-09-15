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
   `evener/auth/apiKey/set`, so the host writes its file atomically with the
   correct mode. Merge, do not clobber; report added/updated/skipped per
   instance.

The controller adds **no canonical credential or config store**: it reads the
existing local store and writes the remote one.

## Scope

- A hub-scoped proxy method on the controller that forwards one admin RPC to a
  named host's hub over the component-02/04/05 channel, plus host validation and
  offline refusal.
- Fan-out of the remote hub's config notifications
  (`evener/auth/updated`, `evener/instance/updated`, `evener/launch/updated`,
  `evener/marketplace/updated`, `evener/plugin/updated`,
  `evener/settings/agentsDoc/changed`) to the controller's browser clients,
  tagged with the host, so remote settings panes refresh.
- A controller-side credential-push RPC that reads the local credentials file
  and writes the remote one through the remote hub's `evener/auth/apiKey/set`,
  returning a per-instance report.
- Frontend host scoping for the existing settings panes that call these RPCs:
  providers/credentials, launch config, plugins/marketplaces/skills, agents-doc
  (see "Contract" for the exact method families).
- The OpenAI Codex OAuth caveat: device-code login **on the host** is the
  reliable path; a direct token-file copy is best-effort and warned.

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
`appwire/protocol.go`):

- `evener/instance/{list,create,edit,remove,setDefault}`
- `evener/launch/{resolve,schema,getLayer,setLayer,trustRepo}`
- `evener/marketplace/{list,add,remove,refresh,edit,browse}` and
  `evener/plugin/{list,install,upgrade,remove,enable,disable,setAutoUpgrade,preview,checkNow}`
- `evener/auth/{status,test,list,login/start,login/complete,logout,apiKey/set,apiKey/clear,credentialJson/set,device/start,device/poll}`
- `evener/settings/agentsDoc/{get,set}`

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
3. allow-lists `Method` to the five families in the table above — the proxy must not
   become a generic hub-to-hub RPC tunnel (design §6 "secret handling"; the
   remote hub is a trusted peer but the browser is not);
4. calls the per-host `appwire.Client.Request(ctx, Method, Params, &out)`
   (`appwire/client.go`) and returns `out` as the browser response, passing
   wire errors through unchanged.

The per-host client is the one components 04/05 already maintain for
`RemoteHubSource` (created the way `spike/client/main.go` and
`appsource/local_daemon.go` create one: `appwire.NewClient(transport)`).
Component 05 must expose it (or an equivalent admin-call accessor); this spec
does not define that accessor.

**Alternative considered.** Add an optional `host` field to every admin params
struct, or open a per-host browser WebSocket (`/rpc?host=…`). A wrapper method is
one protocol row and one handler and leaves the ~40 param structs untouched; a
per-host socket would multiply the browser handshake. See "Open questions".

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
- `Store.Names()` (`store.go`) lists the file-layer entries; `Store.Get(name)`
  (`store.go`) returns one value. `Store.Set` writes atomically at mode `0600`
  (`store.go`) — but the push does **not** write the
  local store; it only reads it.

Write side (remote): for each local entry, call the remote hub's
`evener/auth/apiKey/set` with `{provider, value}`
(`appwire/types.go`) via the same `evener/host/request` channel. The
host's `hubAuthController.ApiKeySet` (`app_auth.go`) validates, calls
`c.setCredential` (= `creds.Set`), reloads the registry, and returns
`AuthStatusResponse`. The host therefore writes
its own `credentials.toml` atomically with the correct mode; the controller
never sees the file path or writes it.

**Merge / no-clobber policy.** Before writing, the pusher asks the remote
`evener/auth/status` for the instance (`app_auth.go`) and reads
`ActiveSource` and `HasStoredFile` from `AuthStatusResponse`
(`appwire/types.go`). Writes are permitted **only** when the
remote resolves the instance's credential from the file layer or has none;
every other source is skipped, because the pushed key either cannot be used or
would silently change which credential is in force. Remote resolution order is
`api_key` > `credential_headers` > `store` > `env:<VAR>` (`registry.credential`,
`llm/registry/instances.go`):

| Condition on the remote (evaluated top to bottom) | Action |
| --- | --- |
| instance is Codex-OAuth or gcp-adc style (`oauth`/`adc` or the scheme's transport) | **skipped** — the host's `apiKey/set` would refuse it (`app_auth.go`); classify locally so the report is a skip, not an error |
| instance not present in the remote `evener/instance/list` | **skipped** — no matching instance on the host |
| `ActiveSource == "api_key"` or `"credential_headers"` | **skipped** — the instance resolves from `providers.toml`, which *outranks* the file layer, so a pushed key would be shadowed and change nothing |
| `ActiveSource == "env:<VAR>"` | **skipped** — the host operator's environment supplies a working credential; a file-layer write outranks `env:` and would silently replace it |
| `ActiveSource == "store"` (implies `HasStoredFile == true`) | **updated** — write; the host overwrites its own file-layer key |
| `ActiveSource == "none"` | **added** — write; the instance has no credential today |
| remote `apiKey/set` returns an error | **failed**, with the wire error text |

"Merge" means the host's other file-layer entries are never deleted — only
`apiKey/set` is used, never `apiKey/clear`, and no whole-file replace exists.
"Don't clobber" means no write at all unless the remote resolves from the file
layer or has no credential: a working `api_key`, `credential_headers`, `oauth`,
`adc`, or `env:<VAR>` credential is never shadowed by a pushed key.

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
  per-host client accessor. It allow-lists methods against the families in the
  table above; the allow-list is the security boundary.
- Notification fan-out: the per-host `appwire.Client` delivers the remote hub's
  notifications on `Client.Notifications()` (`appwire/client.go`); re-emit
  them to the controller's browser clients tagged with the host. The existing
  broadcast helpers (`notifyAuthUpdated`, `notifyLaunchUpdated`,
  `notifyMarketplaceUpdated`, `notifyPluginUpdated`, `notifyInstanceUpdated`)
  are the model for the controller-side notification shape.

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
  boundary, not the transport.

### PR 07c — credential push

- New file `cmd/evener-hub/app_host_credentials.go` with the push controller and
  the classification table above.
- Read the local store through `cfg.CredsStore` or
  `credentials.LoadStore(cmdutil.CredentialsPath())`; enumerate with `Names()`
  and `Get()` (`internal/credentials/store.go`).
- Query the remote `evener/auth/status` and `evener/instance/list` through the
  proxy channel; write through `evener/auth/apiKey/set`.
- Add the push action to the remote-credentials pane in the frontend
  (`src/panes/settings/sections/credentials/`).
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
        |  evener/auth/status (per instance) ------>| hubAuthController.Status (app_auth.go)
        |<-- ActiveSource, HasStoredFile -----------|
        |                                           |
        |  classify (added/updated/skipped)         |
        |                                           |
        |  evener/auth/apiKey/set ----------------->| ApiKeySet (app_auth.go)
        |    {provider, value}                      |   creds.Set  (store.go)
        |<-- AuthStatusResponse / wire error -------|   atomic save 0600 (store.go)
        |                                           |
  per-instance report -> browser
```

Secrets flow controller→host over the AppWire channel only; the browser never
receives a key, and the controller writes nothing.

## Error handling

- Unknown host → `appwire.InvalidParams("unknown host %q")` from the proxy.
- Host not attached → `appwire.Unavailable`, matching design §2 offline rule.
  A proxy call to an offline host never falls back to local execution.
- Disallowed method → `appwire.InvalidParams`; the allow-list is checked before
  any forwarding.
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
- **Notification fan-out:** a scripted remote emits `evener/auth/updated`;
  assert one controller-side broadcast tagged with the host.
- **Push table test:** a fake remote that records `apiKey/set` calls and reports
  scripted `status`/`instance/list`; assert the added/updated/skipped/failed
  matrix, that `apiKey/clear` is never called, that no `apiKey/set` is issued
  for an `api_key`/`credential_headers`/`env:`-resolving or OAuth/ADC instance,
  and that remote-only instances survive (fake host store compared
  before/after).
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
   allow-list rejects any method outside the five families; every method the
   settings panes call — including `evener/plugin/disable`,
   `evener/plugin/setAutoUpgrade`, and `evener/settings/agentsDoc/{get,set}` —
   is inside it, and no instance method outside the five listed exists to call.
3. Remote config mutations reach the host's own store: an `evener/instance/create`
   proxied to `m4` lands in `m4`'s `providers.toml`, not the controller's
   (`app_instances.go`).
4. Remote config notifications refresh the browser's panes without a manual
   reload (tagged with the host).
5. Credential push reads the local store and writes the host's store only via
   `evener/auth/apiKey/set`; the host file is written atomically at mode `0600`
   (host-side `store.go`). An instance whose remote credential resolves
   from `api_key`, `credential_headers`, `oauth`, `adc`, or `env:<VAR>` receives
   no write at all; only `store` and `none` are writable.
6. The push report lists every local entry with `added`/`updated`/`skipped`/
   `failed` and a reason for skips; remote-only entries are preserved.
7. No key value appears in the push response, controller logs, or errors; the
   controller writes no new credential/config file.
8. Codex OAuth is handled by host device login (`EVENER_LOGIN_HEADLESS=1`); any
   token-file copy is warned and does not claim success on a refresh failure.
9. `go test ./cmd/evener-hub/... ./internal/credentials/...` and the frontend
   store tests pass with no live SSH.

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
- **Notification tagging.** The exact wire shape for a host-tagged
  `evener/auth/updated` (new field vs a distinct method name) is not fixed here;
  the frontend stores must be able to ignore or apply it per selected host.
- **Value identity.** `AuthStatusResponse` never returns the key, so `updated`
  is reported even for an identical value. Is that acceptable, or should the
  host's `apiKey/set` gain a compare-and-set / value-hash so the report can say
  `unchanged`? The host is the only side that could hash.
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
