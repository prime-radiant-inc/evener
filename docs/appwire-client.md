# Implementing an AppWire client

This guide describes client behavior using JSON and workflow rules. The
[generated reference](appwire-protocol.md) contains the method catalog and nested
wire types. The [TypeScript client library](../cmd/evener-hub/frontend/src/protocol/README.md)
provides the transport used by Evener's web and native clients, without a UI
framework dependency. Other languages can implement the same wire contract.

The current documentation is being expanded toward independent implementation.
Launch configuration, creation and a start/interrupt lifecycle are described
below. Full transcript reconciliation, mutation recovery, approvals, navigation,
providers, plugin management and upgrades still require dedicated chapters and
runnable fixtures. A method
being listed or callable is not evidence that its full workflow is documented.

## Connecting

Use a WebSocket URL ending in `/rpc`: `wss://hub.example/rpc`, or `ws://` for a
hub reached over an appropriate local connection. Native and Node clients can
supply the hub credential as an `Authorization: Bearer <token>` upgrade header.
The token is a hub credential, not a model-provider API key. Keep credentials,
connections, pending calls and cached session identities separate for each hub.
The browser's WebSocket API cannot supply arbitrary headers; browser deployment
must arrange the hub's browser authentication separately.

Send one JSON object per WebSocket text frame. Do not send batches or concatenate
objects in a frame. Correlate each response with its request ID; notifications
have no ID and may arrive while a request is outstanding. The examples omit
`jsonrpc`, matching the shipped client. Start with:

```json
{"id":1,"method":"initialize","params":{"protocolVersion":"evener-appwire-v4","clientInfo":{"name":"example-client","version":"1.0.0"},"capabilities":{"experimentalApi":false}}}
```

The successful result is an `InitializeResponse`. Validate the exact
`protocolVersion`, then send this notification before application requests:

```json
{"method":"initialized","params":{}}
```

Do not treat a successful WebSocket upgrade as a completed handshake. An
incompatible protocol is a terminal compatibility error, not a reason to retry
mutations. Inspect advertised capabilities and each thread's capabilities before
offering operations. Catalog entries marked `unimplemented` are reserved, not
usable features.

When `navigation` is present, its `version` describes the invalidation stream;
`readVersions` advertises supported navigation read representations. The current
hub sends `version: 1` and `readVersions: [2]`. These are separate versions:
send `representationVersion: 2` on navigation reads. `generationId` and
`sequence` identify the current invalidation stream; discard cached conditional
bases after a generation change. `readVersions` is optional in the wire type;
when absent, it does not advertise any read representation. Each advertised
version must be a positive integer. Optional `features.keybindingsSettings`
and `features.transcriptDisplaySettings` are booleans when present; a missing
or false capability does not authorize offering the corresponding operation.

The TypeScript library performs this handshake and correlates calls. Its default
request timeout is 30 seconds, overridable per call. It sends `ping` every 20 seconds
with a 10 second timeout while ready and reconnects with exponential backoff after
ordinary connection loss. It does not automatically repeat failed application
requests, restore thread subscriptions or rebuild application state.

An initial connection or handshake failure rejects `connect()` and closes that
client instance; it does not start automatic retries. To retry, create a new
client instance. Repeated calls to `connect()` on a live instance share its
initial handshake promise. After a previously ready connection is lost, observe
connection-state changes to follow reconnection rather than calling `connect()`
as a fresh readiness check.

On every new ready connection, restore subscriptions and read authoritative
snapshots. Install notification listeners before subscribing. Keep unsent drafts
separate from fetched state. Discard responses belonging to an old hub or an old
request context, even if their payload happens to contain the same session ID.
A snapshot restores current state; it is not a promise to replay every transient
event that occurred offline.

## Launch configuration

All paths below refer to the hub's filesystem. For a chosen existing directory,
fetch the schema, the editable layer and the resolved effective configuration:

```json
{"id":2,"method":"evener/launch/schema","params":{}}
{"id":3,"method":"evener/launch/getLayer","params":{"cwd":"/work/project","layer":"project"}}
{"id":4,"method":"evener/launch/resolve","params":{"cwd":"/work/project"}}
```

These are separate frames. `getLayer` returns a `LaunchConfigLayer` directly;
`resolve` returns `effective`, `layers`, `provenance`, optional `repo`, and
optional `diagnostics`. An empty `layers` object is possible when no layer
contributes settings. `effective` also includes applicable hub environment and
builtin defaults; it is **not** the object to write back as a saved layer.

### Schema-driven controls

Each `options` entry identifies a configurable field:

| Property | Client behavior |
| --- | --- |
| `wireField` | Key to read/write in JSON configuration, such as `maxRounds`. |
| `field` | Configuration/schema name, such as `max_rounds`; also used by provenance and diagnostics. |
| `kind` | Control/value family. See the families below. |
| `defaultableLayers` | Offer persistent editing only for listed layers. |
| `perLaunch` | Whether the field can be offered as a launch override. |
| `choices` | Use returned values; respect disabled entries and display their hints. |
| `pathKind` | Select the path validation mode; see the mapping below. |
| `driverSupport` | Driver-specific support information; do not invent a supported driver. |
| `builtinDefault*`, `envFallback` | Explain inheritance; do not persist a displayed fallback as an explicit override. |

Current kinds are `modelPicker`, `text`, `multilineText`, `integer`, `boolean`,
`select`, `radio`, `path`, `pathList`, `modelList`, `mcpServerList`, `envMap`, and
`pluginSelection`. Unknown future kinds should remain readable without guessing
an editor. `modelPicker` stores a qualified model reference. `envMap` is a JSON
object of string values; empty string is a valid environment value. MCP entries
are structured `{ "name": "tools", "command": "/usr/bin/example", "args": [] }`;
argument strings are separate array elements, not shell source.

### Layers, inheritance and saving

Precedence is global → trusted repository → project → per-launch. Environment
and builtin defaults fill fields left unset by those layers. Scalar values are
replaced by more specific values; strings left empty behave as unset. Boolean
false and integer zero are explicit values. Omit a field to restore inheritance
in that saved layer; do not replace false or zero with omission.

Collections have different contracts:

- `skillsDirs`, `pluginDirs` and `mcpConfigs` append across layers.
- `env` merges by key, with more specific values winning. An empty map does not
  erase inherited environment keys. Credential-like keys are refused in saved
  environment maps; use the provider-authentication API for provider secrets.
- `modelFallbacks` replaces the inherited list. Omitted means inherit; `[]`
  explicitly disables fallbacks. This distinction survives serialization.
- `enabledPlugins` replaces the inherited selection and is per-launch only;
  `setLayer` rejects it. Omitted and an explicit empty list are different.
- MCP servers contribute across layers; inspect diagnostics for duplicate names
  or rejected configuration rather than assuming every submitted entry applies.

`setLayer` replaces the **whole** named layer; it is not a field patch:

```json
{"id":5,"method":"evener/launch/setLayer","params":{"cwd":"/work/project","layer":"project","config":{"maxRounds":7,"env":{"BUILD_MODE":"debug"}}}}
```

Only `global` and `project` are writable. Preserve unrelated fields from the
layer snapshot while editing. The result is a resolved configuration, and a
successful mutation broadcasts `evener/launch/updated` with `cwd` and `layer`.
Refresh clean views on that notification; warn rather than overwrite dirty
local drafts. A global change can alter every project's inherited settings.

There is no revision/CAS parameter on `setLayer`. Re-read the layer before a
whole-layer write and reconcile intervening changes, but recognize that another
writer can still race between that read and the write. After a lost save reply,
read the layer back and reconcile; do not infer that the save failed. Saving can
succeed before the subsequent resolve operation returns an error.

### Paths

`evener/paths/complete` accepts `{ "prefix": "/work/", "includeFiles": false,
"limit": 100 }` and returns `{ "data": ["/work/project"] }`. In directory-only
mode every entry is a directory. With `includeFiles:true`, directory entries have
a trailing `/`; files do not. Preserve returned spelling. The response has no
continuation cursor; narrow the prefix if the result reaches the requested cap.

`evener/path/validate` accepts `{ "path": "/work/project", "kind": "dir" }`
and returns `{ "path": "/work/project", "valid": true }` on success. Invalid
paths normally return a successful RPC result with `valid:false` and `error`;
they are not necessarily RPC errors. A transport failure is not valid-path proof.

Map schema `pathKind:"outputFile"` to validation `kind:"output-file"`.
`dir`, `file` and `command` use the same spelling in both APIs. Validation accepts
absolute paths and expands `~` on the hub. Command paths must exist and be
executable; this is not a shell PATH lookup. An output file may not exist yet,
but its parent must be an existing writable directory. Validation is a preflight,
not a reservation: the filesystem can change afterward.

### Repository trust

`resolve.repo` describes `.evener/launch.toml`, including its path, hash, trust
state and optional preview. The repository layer applies only when trusted.
States include `absent`, `untrusted`, `changed`, `rejected`, and `trusted`. Display
the returned preview and require a deliberate trust action for that exact hash:

```json
{"id":6,"method":"evener/launch/trustRepo","params":{"cwd":"/work/project","hash":"hash-returned-by-resolve"}}
```

A changed file returns error code `-32009` with `file changed since review`.
Re-resolve and obtain another review; never silently trust the replacement hash.
Successful trust returns a resolved configuration and broadcasts a launch update
for layer `repo`. Previously trusted hashes can remain trusted across branch
switches. A failed/lost trust reply requires re-reading the status before retrying.

## Creating a session

For local Evener, choose a directory and an actual model from `model/list`.
A missing explicit model is allowed only if launch configuration or the hub's
environment supplies one. Otherwise the hub rejects creation with
`model is required`; an optional wire field is not a guarantee of a usable default.

```json
{"id":7,"method":"thread/start","params":{"cwd":"/work/project","harness":"evener","modelProvider":"provider-from-catalog","model":"model-from-catalog","launchOverrides":{"maxRounds":7}}}
```

Omit `input` to create an empty session. To supply an opening prompt, add an
`input` array such as `[{"type":"text","text":"Inspect the project"}]`.
`ThreadStartResponse` contains `thread` and `turn`; use the returned identity,
including `thread.evener.ref`, rather than constructing it from a display title.
The server can normalize the directory, including its trailing slash.

Opening images are ordinary `input` items: `type: "image"`, `mediaType` (for
example `image/png`), `data` containing base64 bytes without a data-URL prefix,
and an optional `name`. They do not require a separate upload request. Put an
optional text item first, then images in attachment order. An image-only input
is valid; omit an empty text item. A local device file URI is not image data
and is not readable by a remote hub.

The shipped composers limit selection to eight images and eight MiB per source
file and re-encode native selections to PNG. Those are client staging rules,
not a promise that every harness/model accepts every image or encoded request
size. Retain the staged bytes and report server rejection. Local `[image N]`
editing markers are translated to `(attached image N: filename)` text at send;
neither the marker number nor local image ID is an `InputItem` field.

To verify stored user input independently, call `thread/read` with the returned
`ref` and `includeTurns: true`. User-message items expose their `text` and
`images`; image entries include media type, base64 data and optional name. A
successful start response alone does not prove that a live event consumer has
rendered those attachments.

Subscribed clients receive user input, including its `images`, in
`item/completed` notifications; an item need not have a preceding
`item/started` event. Upsert by item ID within the matching hub/thread.
Process the complete item payload, including image arrays, rather than only its
text. Tool items can carry `outputImages` references. Rendering an item as
separate text/activity and attachment rows is a client choice; those rows still
belong to one wire item and must be replaced or removed together.

Reconcile snapshots with notifications received during the request. A newer
whole-item event must survive an older snapshot, including image replacement or
removal. Conversely, when accepting a newer snapshot's source item without
images, remove its obsolete attachment presentation. An overlapping older page
must not restore attachments from an older version of an already retained item.
These presentation/reconciliation responsibilities currently belong to the
application; the API transport library delivers typed payloads and does not
maintain the conversation projection.

For local Evener, explicit top-level `model`, `profile`, `reasoningEffort` and
`nonInteractive` take precedence over the corresponding `launchOverrides` fields.
`modelProvider` qualifies `model` when needed. Other harnesses are routed to their
source and may have different capabilities; consult `evener/harnesses/list` and model
metadata rather than assuming local Evener behavior for every harness.

A thread may be created even if a later operation or response delivery fails.
`thread/start` has no client mutation ID for deduplication. On timeout, connection
loss or an ambiguous error, retain the form and inspect the session list before
a user-controlled retry. Do not replay it automatically. Display a hub-provided
error message separately from the uncertainty explanation so configuration
failures remain actionable.

## Runnable coverage and maintenance

The packaged `inspect.mjs` recipe uses only the public library and `ws`. It has
been run from a clean tarball consumer against an authenticated isolated hub.
The inspection recipe covers initialize, model/list, thread/list, launch/schema
and launch/resolve. The opt-in `project-layer.mjs` recipe adds getLayer, setLayer
and the launch/updated notification. It writes a project override, checks the
layer and effective configuration, restores the original layer and checks that
the global layer stayed unchanged. See the package README for invocation and
concurrent-writer limitations. The repository-trust recipe adds trustRepo, stale-hash rejection and fresh
revision confirmation. These recipes cover eight catalog methods and one
notification; they do not establish whole-protocol or failure-path coverage.

The target is a fixture-backed cookbook covering every supported catalog method
and notification, including alternate outcomes. Each recipe must describe its
preconditions, request sequence, authoritative result, cleanup and recovery after
lost replies. Destructive and administrative scenarios need disposable hubs;
provider behavior needs explicit live opt-in. The native-app backlog and this
library remain in progress until that coverage exists.

When implementing another client flow, update its protocol semantics and runnable
recipe together. Generated types keep field names synchronized; they do not prove
requiredness, authorization, ordering, presence semantics or retry safety. Those
contracts need behavioral tests and documentation review.

The library exports `METHOD_NAMES` and `NOTIFICATION_NAMES` from the generated
catalog. Run its `examples/coverage.mjs` to list uncovered operations. That
inventory includes reserved methods; a complete suite must classify them and
verify appropriate rejection rather than advertise them as implemented features.

### Initial package verification

On 6 September 2026, a clean temporary Node project installed the locally packed
`@evener/appwire-client@0.1.0` and `ws@8.21.3`. The inspection recipe connected
directly to an authenticated isolated hub and reported protocol v3, two models,
eight sessions, 33 launch options and absent repository config. These counts
are fixture observations, not protocol constants. The package worked through
both ESM named imports and CommonJS require. An independent TypeScript consumer
compiled valid requests and rejected missing cwd, an unknown method and a string
where an integer was required, without skipping declaration checks.

The first ESM-source packaging candidate broke Metro's `.js`-to-TypeScript
resolution. The compiled package uses CommonJS while retaining the shared source
imports used by the apps; final iOS and Android Release bundles pass. This is
packaging evidence, not complete client recovery or scenario coverage. The
package has not been published to a registry.

The project-layer recipe also ran successfully from the independently installed
tarball against the isolated hub on 6 September 2026. Project write, notification,
readback, resolution, restoration and unchanged global settings were verified.


### Repository trust confirmation

A client should retain the preview and hash together while the user reviews the
file. Send that hash to `evener/launch/trustRepo`; do not substitute a newer hash
from a background refresh. After the request (including an uncertain reply),
resolve again. Confirm only if the current repository hash matches the reviewed
hash and its trust state is `trusted`. If it changed, show the current status and
require a new review. Do not automatically approve that new hash. Trust metadata
remembers previously approved hashes; deleting the file does not revoke that
history. The native flow has isolated-hub evidence for this sequence; the packaged
`repository-trust.mjs` recipe reproduces stale-hash rejection and fresh review
against an isolated hub sharing the fixture filesystem. See the package README
for prerequisites and cleanup limitations.

On 6 September 2026, the repository-trust recipe ran from the independently
installed tarball against the authenticated isolated hub. It verified rejection
of the stale hash, approval of the new revision and effective maxRounds=12, then
removed its owned temporary directory. Trust metadata remains in isolated state.


### Per-session overrides versus saved layers

Send per-session choices in `thread/start.launchOverrides`. Do not call
`evener/launch/setLayer` for these choices. Use `evener/launch/resolve` with the
same cwd and launchOverrides to preview the resulting configuration; never copy
its entire effective result back into the draft. Omit launchOverrides when no
fields are selected. Preserve explicit false, zero and empty values according to
each field's merge semantics.

The web and native creation flows give advanced model/reasoning fields precedence
over their compact selectors. Because top-level thread/start model/reasoning
fields win on the server, clients must hoist the advanced values into those
fields too. An advanced qualified model (provider/model) replaces both the old
model and its separate modelProvider selection. Use the perLaunch schema flag and
driver support when advertising advanced fields. Plugin selection is a separate
control in both flows. Native scalar launch-only behavior has both-platform
manual evidence; a complete session-creation SDK recipe remains outstanding.


## Selecting plugins for a new session

For an Evener-kind harness, call `evener/plugin/preview` with the chosen hub
`cwd` and the same `launchOverrides` intended for `thread/start`. It resolves
plugin candidates from installed plugins and configured directories; it does
not install plugins or change persistent enablement.

`PluginPreviewResponse.plugins` contains `name`, `source`, optional version,
description, marketplace/path, component counts and `selected`. Treat names as
opaque identifiers. With no `enabledPlugins` field, use the server's selected
flags. The first toggle creates an explicit allow-list from those defaults.
`enabledPlugins: []` selects none; restoring defaults removes the field entirely.
An explicit list is authoritative, including names currently unavailable.
Never silently discard a missing name during refresh.

```json
{"id":20,"method":"evener/plugin/preview","params":{"cwd":"/work/project"}}
{"id":21,"method":"evener/plugin/preview","params":{"cwd":"/work/project","launchOverrides":{"enabledPlugins":[]}}}
{"id":22,"method":"evener/plugin/preview","params":{"cwd":"/work/project","launchOverrides":{"enabledPlugins":["plugin-name-from-preview"]}}}
```

Display `diagnostics` separately from `selectionErrors`. Each selection error
identifies `name` and `reason`; require correction before launching that explicit
selection. Keep unavailable selected names visible with a removal action. A
failed preview must not silently reset the choice. Existing selected names can
still be removed while additions wait for a valid preview. Preview again when
cwd, overrides, plugin catalog or launch settings change; subscribe to
`evener/plugin/updated` and `evener/launch/updated`. Discard results from an old
hub or selection context.

Before creating with an explicit list, the native client previews the exact
selection again. Failure at this read stage has not requested a session. Once
`thread/start` is dispatched, the ordinary uncertain-creation rules apply.
Preview is not a reservation: plugins can change before Start, and server
validation remains authoritative. Pass the list unchanged in
`thread/start.launchOverrides.enabledPlugins`; do not save it with `setLayer`.
Clear/omit this field for harnesses that do not support Evener plugin selection.

After creation, `thread/read` exposes loaded plugin information in
`thread.evener.diagnostics.plugins`. This is an independent runtime readback,
not a promise that every plugin component was exercised. The packaged
`plugins.mjs` example verifies preview/default/none/explicit/missing-name behavior
without creating a session. Full plugin installation and execution recipes remain
outstanding.

## Session lifecycle and turn receipts

A session reference (`thread.evener.ref`, for example `local:abc`) and a thread ID
(`thread.id`, for example `abc`) are distinct. Pass the former as `ref`; do not
put a qualified reference into `threadId`. Read and subscribe with:

```json
{"id":10,"method":"thread/read","params":{"ref":"local:abc","includeTurns":true,"subscribe":true}}
```

Install notification listeners first. `turn/started` carries `threadId`, `ref`
and `turn`; `turn/completed` additionally carries `turnId`. These pushes can
arrive before the response to the request that caused them. Match both session
identity and turn ID, and buffer lifecycle information until that response is
correlated. A connection's subscription does not survive reconnect; restore it
and reconcile a fresh snapshot after the next successful handshake.

Read `thread.evener.instanceId` and `thread.evener.capabilities` from the current
snapshot. Send input only when `capabilities.send` permits it. The instance ID
protects against applying a mutation to a replacement runtime for the same
session. Each logical mutation has its own client-generated ID:

```json
{"id":11,"method":"turn/start","params":{"ref":"local:abc","expectedInstanceId":"instance-from-read","clientMutationId":"unique-send-id","input":[{"type":"text","text":"Inspect this project"}]}}
```

The result contains `turn` and `receipt`. Validate the receipt's mutation ID,
thread ID, optional instance ID and turn ID before associating it with pending
input. `disposition` is `applied` or `replayed`; `turn/start` has
`projectionState: "pending"`. Acceptance does not mean the turn is complete or
that the snapshot already contains every resulting item. Observe lifecycle and
item notifications, and reconcile authoritative state. An independently
implementable transcript reducer also needs the item-event and pagination
contracts; the lifecycle example alone does not implement that reducer.

To stop the active turn, use a fresh mutation ID and the same current instance
identity, after checking `capabilities.interrupt`:

```json
{"id":12,"method":"turn/interrupt","params":{"ref":"local:abc","expectedInstanceId":"instance-from-read","clientMutationId":"unique-stop-id"}}
```

The interrupt response contains a receipt with `projectionState: "reflected"`.
The lifecycle recipe checks the corresponding `turn/completed` event, then reads
`thread/read` again with `includeTurns:true, subscribe:false` to verify the
interrupted turn and its submitted user item. A read with `subscribe:false` is
an ordinary read; it does not remove an existing subscription. Explicitly leave
it when done:

```json
{"id":13,"method":"thread/unsubscribe","params":{"ref":"local:abc"}}
```

A timeout or disconnection is not proof that a mutation failed. Keep its input
and mutation identity until reconciliation resolves the outcome. Do not invent
a new ID and resend uncertain input. Session creation (`thread/start`) has no
client mutation ID; a lost creation reply requires inspecting the session list
rather than automatically issuing another creation.

### Runnable start/interrupt/readback recipe

The packaged `examples/session-lifecycle.mjs` creates a fresh empty session,
subscribes, submits a unique text fixture, validates receipts and lifecycle
notifications, interrupts the turn, independently reads its input and terminal
state, and unsubscribes. It leaves the idle session available for inspection.
Use an empty-task-capable harness and a scripted provider that holds its turn
open until interrupted; a fast naturally completed turn cannot prove Stop.
The selected provider/model must appear in `model/list` for the chosen directory.

```sh
EVENER_EXAMPLE_CREATE_SESSION=1 \
EVENER_MODEL_PROVIDER=fake EVENER_MODEL=fake-test-model \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/isolated/project/on/hub \
node node_modules/@evener/appwire-client/examples/session-lifecycle.mjs
```

Run without concurrent writers to that newly created session. On failure, cleanup
attempts to interrupt only that session and only if its instance still matches;
it never repeats Start or deletes history. Cleanup failures are reported along
with the original error. A killed process cannot run cleanup. If the creation
reply is lost, the example cannot know the ref to clean up; inspect the isolated
hub before repeating it. This recipe does not cover text deltas, tool execution,
approvals, queueing, disconnect recovery or natural turn completion.
