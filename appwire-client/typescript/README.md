# @evener/appwire-client

The framework independent TypeScript client for Evener's AppWire protocol.
It has no runtime dependencies and exports the client, the connection seam
applications program against, the transport contract, generated protocol
types, wire errors with their session classifiers, the rejection classifier
and the user-facing message helpers every failure display goes through, the
pure question formatter, the ask_user question parser and the answered recap
it reads back out of a transcript, the live-question derivation an answering
dock renders from a thread, the batch reconciliation that keeps an in-flight
answer's questions frozen while late ones arrive, the ask-dock store that
reconciliation feeds (`createAskDockStore({ send })`, a framework-free store
each app points at its thread source with `followThreads`), the attachment count, size
and type limits every composer rejects a staged file against, the `[image N]`
marker splicing that anchors a staged image in the composer text and removes
it again, the translation that turns those markers into prose at send and
the composer input assembly that applies that translation and stages the
attached images beside the text, the thread view model and
its notification reducer, the activity tree parser, merge and disclosure
rules, the job log tail parser, the send/queue availability table, the
send/steer/queue/drain routing decisions a composer makes off it, the stable
delegate status rule, the delegate timing and model derivations both apps'
delegate details render from,
the slash invocation and catalog visibility rules the palette and composer
share, the command catalog itself as a framework-free store
(`createCommandCatalog(client)`, the hub-wide list re-read on a plugin change,
and `createSessionCommandCatalog(client, ref)`, one session's slash menu read
beside its diagnostics), the inline slash-completion token parser, menu merge, filter and
splice the composer's own menu is built from, the reasoning-effort labels
and picker ladders every effort chip and select share, the task-list
parser, aggregate sentence, status grouping and timestamp formatters the
tasks panel and native tasks sheet render from, and the tasks-panel store
both render out of (`createTasksPanelStore(listTasks)`: the triple plus a
coalescing `refresh` and a notification-following `watch`, over a
`TasksListRead` port so the web's reconnect-waiting read and native's direct
one both fit), the display formatters
both apps render counts, durations and clock times with, the text and
argument helpers a tool call's rendering is built from, the marketplace
source label both apps show beside a registered marketplace,
the short lowercase session-state gloss a session row's second line leads
with, the credential labels both apps describe a provider instance's active
credential source, shadowed layers and test outcome with, the path picker's
flat row builder with the path helpers both apps' path fields share, the
model catalog view helpers both apps' model pickers are built from (searchable
options, provider grouping, per-row metadata and the flat picker row list),
the launch-config engine's pure half both apps' launch settings are built on
(option grouping, layer filtering, the form state populate/collect pair, the
inherited entries a collection control ghosts in, and the add-a-path decision
every path list gates on) with its wire gateway - `createLaunchConfigStore(client)`,
a framework-free store over the schema/layer/resolve/trust/path-validate
methods with a per-instance schema cache - and the per-layer draft editor
(`LaunchSettings`) the native settings screen drives over it, the new-session form's pure trio both apps' spawn
surfaces share (the per-launch option filter and override collection with the
schema-wins model/effort precedence, the plugin selection state and its
launchOverrides merge, and the harness rules: which harnesses take evener
models and which take a plugin selection), the built-in slash invocation
matcher and argument lookup both composers run a draft through before sending
it, the transcript display configuration both apps resolve (local over hub
over shipped), encode for local storage and summarize a transcript's content
level and advanced toggles with, the transcript projector
(`projectThread(model, config)`) that turns a thread's turns and items into
the rows a transcript renders - content-level filtering, the critical/intent/
thinking/hidden decision per item, disclosure eligibility and anchors - over
the same config, framework-free and with no host global, the keybinding group both apps' shortcut settings are built on (the action
ids, the chord AST with its overlap predicate, the default binding map, the
display rows, the override primitives and the semantic override validation) -
its registry is a framework-free store factory, `createKeybindingsRegistry(parse)`
returning a `getState`/`setState`/`subscribe` triple each app wraps for its
own view layer, and its chord parsing goes through a `KeybindingParser` port
the host supplies (tinykeys' `parseKeybinding` in both apps), so the package
names neither a store library nor a parser; that triple is
`createFrameworkFreeStore`, the base every shared store here is built on; the
disclosure store both transcripts keep a row's open/closed choice in across a
remount (`createDisclosureStore()`, the triple plus store-bound actions, read
reactively through the `isDisclosureOpenIn` selector); the keybindings
overrides store both apps' shortcut settings run on
(`createKeybindingsStore({ client, registry?, characterKeyTriggers?, drafts? })`:
one hub's `evener/settings/keybindings` get/patch/changed posture, reconciled
into the host's registry as a delta when it has one, with a checkpointed draft
editor over an injected storage port for a host that edits offline; the host
drives the connection lifecycle through `setSupport`, `beginReadyGeneration`,
`endReadyGeneration` and `detachHub`); the ready-generation fence that store
fences every await on (`createReadyGenerationFence(isSupported)`: the
generation, read-serial and write-token bookkeeping behind `liveHub`,
`readStillMine` and `writeStillMine`, so a reply arriving after the generation
ended, support dropped or the hub was replaced lands nothing) - and the doc-pane
URL builders, which hang their hrefs off a base origin the host supplies (empty
for a same-origin web
page). The doc-pane data layer is published at the `./docContent` subpath as
well, where `readDocFile` takes the host's `DocPort` - that base origin paired
with a fetch: the package issues no request of its own and names neither an
origin nor a credentials policy. The hub overview store,
`createHubOverviewStore(client)`, is the same framework-free triple over a
`request`-only client port; it holds the fetch-once settings-overview read
both apps' hub settings render from.

The slash-completion module is ported from Beautiful UI's prompt-bar
completion affordance and ships its MIT attribution at
`LICENSES/beautiful-ui.txt`, inside the tarball.

## Published subpaths

Besides the root, `package.json` `exports` publishes these subpaths:

- `@evener/appwire-client/docContent` - the doc-pane data layer, where
  `readDocFile` takes the host's `DocPort`.
- `@evener/appwire-client/state/connection` - the connection state layer:
  `createConnectionStore()` is a framework-free store holding one host's wired
  `AppwireClientLike`, the `ConnectionState` mirror that follows it, and plain
  settable `serverInfo`/`features` fields the host writes from its own
  handshake read. `connect(client)` swaps the wired client with a reentrancy
  guard and a detach of the outgoing client's connection-state listener, so a
  replaced client never keeps a live subscription; `onConnectionNotification(store, handler)`
  follows whichever client the store holds across a swap. No handshake, no
  view binding. Resolves to `state/connection/index.ts`, a barrel.
- `@evener/appwire-client/state/navigation` - the navigation state layer the
  web app's navigation store is built on, adoptable by native if it ever
  gains one: the resource-key vocabulary and
  classifiers (`types`), the snapshot and delta codec (`codec`), the graph
  merge (`merge`), the deep-freeze helpers they share (`immutable`), the rule
  matching a hub invalidation target to a loaded resource and the revision it
  obliges it to reach (`invalidation`), and the revalidator that re-reads
  loaded resources on the hub's invalidations through injected request
  callbacks (`revalidator`), and the store itself (`store`):
  `createNavigationStore({ persistence })` is a framework-free store holding
  one hub connection's navigation state - capability and generation, the
  revalidator's loaded resources, the attention summary, the boot fan-out and
  the rail's expand state - over a `connect`/`request`/`onNotification`/`onReady`
  client port handed to `init(client)` and a `NavigationPersistence` port
  (`readExpansion`/`writeExpansion`) for expansion, with `projectNodeExpansionKey`
  naming a project's row, and the selectors the web app reads that state
  through (`selectors`): launch sources, section rows with their remaining
  count and next offset, pin-section summaries, the project catalog and a
  session summary found by ref, each pure over `NavigationStoreState` and
  adoptable by native if it ever gains a store. The subpath
  resolves to `state/navigation/index.ts`, a barrel that re-exports the eight
  modules whole.
- `@evener/appwire-client/state/extensions` - the extensions state layer both
  apps' plugin settings surfaces are built on: the marketplaces store
  (`createMarketplacesStore(client)`, a framework-free store over a
  `request`/`onNotification` client port holding a hub's marketplace list and
  one cached browse result per marketplace), the installed-plugins store
  (`createPluginsStore(client)`, the same port, holding a hub's installed
  plugins, their six mutations and a `pluginRevision` that moves as the hub
  announces a change), the global launch-layer store
  (`createLaunchLayerStore(client)`, the one `LaunchConfigLayer` the plugin and
  skill directory lists and the MCP server list are four fields of, read and
  written at cwd `/` and layer `global`), and the pieces they are built over:
  `createListRevision`, the fence on a list every response replaces whole, and
  `createStoreLifecycle`, the notification subscription, debounced refetch,
  `connectionChanged` recovery (a list a host has read is read again when the
  connection is ready again, because the hub's broadcast only reaches clients
  that were connected) and the `start`/`reset`/`dispose` trio a host drives
  from its screen. In every store fetches record their failure in state and
  mutations reject. The subpath resolves to `state/extensions/index.ts`, a
  barrel over the layer's modules.
- `@evener/appwire-client/state/credentials` - the credentials state layer:
  `createCredentialInstancesStore({ ownClientId })` is the framework-free
  store core (`instances`) each app's Providers & credentials store adapts:
  the instance listing and its writes, the API-key, credential-file, sign-out,
  sign-in, status (`authStatus`) and probe RPCs, and the `evener/auth/updated` refetch with its
  own-echo correlation, over a `request`/`onNotification` client port; with
  the stale-listing refusal and its `staleListingHeld` predicate, the
  `foreignListingChange` predicate both hosts gate a credential probe on, and
  `listingEstablished` in the state. Resolves to
  `state/credentials/index.ts`, a barrel.
- `@evener/appwire-client/state/mutation` - the mutation state layer: the
  durable record shapes both apps' outboxes store (`MutationIntent`,
  `MutationRecord`, `MutationOutboxRecord`, `MutationOptimisticRecord`,
  `MutationRecoveryRecord`), generic over the attachment type so a host's own
  attachment bytes (the web's `Blob`) never enter the package, and the client
  provenance a shared outbox's readers need. `createClientIdentity(storage,
  randomSource)` is a factory, not a module singleton - the package names no
  browser global, so a host builds one instance over its own
  `ClientIdentityStorage` port (the web passes a lazy `sessionStorage`
  adapter; a host with none gets a per-process identity) and gets back
  `{ ownClientId, isOwnMutationRecord }`, each memoized per instance.
  `isOwnMutationRecord` claims an unattributed record (written before the
  field existed) as well as this instance's own. `createSecureUUID(source)`
  is the strong identifier source both the record shapes' `clientMutationId`
  convention and a generated client identity use; both take their
  `SecureRandomSource` (`randomUUID?`/`getRandomValues?`, both optional) as a
  required parameter with no default - `globalThis.crypto` is named nowhere
  in the package, so a host with no global Web Crypto (React Native without a
  polyfill) passes its own source (the web's lazily-read, guarded `crypto`;
  `expo-crypto` for native) rather than the package assuming one exists.
  `createSecureUUID` documents its own fallback for a source with neither
  method: a non-cryptographic id, not UUID-shaped, rather than a throw. The
  layer also carries `MutationOutbox`, the discovery half of an outbox: a class
  that enqueues through a `MutationOutboxStorage` port (the 14 calls this layer
  and the dispatcher make; the web's IndexedDB adapter implements it and stays
  in the app), announces a commit to sibling clients, and re-scans when a host
  says a scan is worth doing. Every host-shaped capability is an option - the
  channel, the lifecycle and visibility targets, the timer - and none defaults
  to a browser global, so no DOM type lives here. It carries
  `MutationDispatcher` too: one attempt at a time per target ref over the same
  port, a receipt reconciled into storage before the next attempt, a refusal
  turned into a recovery record with the daemon's own reason, and an outcome
  nobody can vouch for left `blockedUnknown` rather than replayed - the rule a
  client must not break after a lost connection. Every reaction to an outcome (a
  blocked mutation, a clear's response, a shared note's authority) is a callback
  the app supplies. Its pure reconciliation (`reconcilePendingEntries`) turns
  those durable records plus a live `ThreadModel` into the `PendingTurnEntry`
  rows a composer's queue renders - identity-based, so an authoritative
  projection replaces the same outbox entry rather than duplicating it - and
  the pending-turns projection store built on that reconciliation:
  `createPendingTurnsStore({ threads, draft, identity })` is a framework-free
  store holding a host's own outbox/optimistic/recovery records plus the
  submission bookkeeping (`submittingRefs`, `submittedHere`) over a
  `PendingTurnsThreadsPort` (a ref's current `ThreadModel`), a
  `PendingTurnsDraftPort` (a ref's composer-draft revision, content and
  clear) and the host's own `ClientIdentity` (`isOwnMutationRecord`),
  generic over the attachment type like the records above. No storage
  adapter lives here either - just the shapes, the identity, the rules, the
  attempts, the reconciliation and the store built on them. Resolves to
  `state/mutation/index.ts`, a barrel.

A module is a root export when it is part of the client surface a consumer
takes to talk to a hub: the client, the wire types, the errors, and the pure
formatters and derivations a view renders wire data with. A module gets its own
subpath when it is a layer a consumer adopts as a whole or not at all - a data
layer that needs a host port (`docContent`), or a state layer the apps build a
store on (`state/navigation`). Later state layers go under `state/<name>/` with
an `index.ts` barrel, one `exports` entry, one qualification-manifest entry of
hand-written type uses and smoke calls, and one alias in each resolver (`tsconfig` `paths` in both apps, the Vite and vitest
configs, and Metro's `resolveRequest`, which probes `<subpath>/index.<ext>` for
a directory subpath). The runner derives each specifier's surface from its
entry, and `mobile-native/src/metroResolver.test.ts` reads the same `exports`
map, so a published subpath Metro cannot resolve fails there.

Build and qualify from this directory with `npm run qualification`. The runner
packs the package, installs that tarball into a temporary consumer, and then,
for every specifier the `exports` map publishes, checks ESM and CommonJS
TypeScript resolution and runs both runtime import forms against the names that
specifier promises. A subpath with no entry in the runner's qualification
manifest is not qualified, so the manifest and the `exports` map must name the
same specifiers. The runner also calls at least one export of every shipped
module on a trivial input, and checks the tarball contains each shipped module
and no source files or dependencies. It also runs the installed inspection and
discovery examples against a scripted local WebSocket server, verifying the
handshake, read-only requests, structured readback, and private output file.
The qualification command is run with the repository's configured Node 22
runtime.

Applications own credentials, caches, transcript storage, subscriptions, and
mutation reconciliation. Connection loss during a mutation leaves its outcome
uncertain; callers must follow the protocol mutation identity rules before
retrying. The examples use Node 22's platform WebSocket and therefore require
Node 22 or newer. For a hub requiring an Authorization header, provide an
authenticated `socketFactory` from the host application's WebSocket
implementation.


## Read-only discovery

The installed `examples/discovery.mjs` program runs one selected hub read. Set
`EVENER_RPC_URL` and `EVENER_CWD` for the shared example connection, then choose
an action with `EVENER_DISCOVERY_ACTION`:

| Action | Read | JSON parameters |
| --- | --- | --- |
| `paths` | Complete a filesystem path | `prefix` (required), `limit`, `includeFiles` |
| `projects` | Recent project directories | `limit` |
| `validatePath` | Validate a path | `path` (required), `kind` |
| `gitHead` | Current Git head | `cwd` (required) |
| `search` | Find live and past sessions | `query` |
| `harnesses` | Available harnesses | None |
| `settings` | Settings overview | None |

Pass a JSON object through `EVENER_DISCOVERY_PARAMS_FILE`; omitted parameters
use an empty object. For example, from a consumer with the package installed:

```sh
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_CWD=/path/to/project \
EVENER_DISCOVERY_ACTION=paths \
EVENER_DISCOVERY_PARAMS_FILE=/private/path/discovery-params.json \
EVENER_DISCOVERY_OUTPUT_FILE=/private/path/discovery-result.json \
node node_modules/@evener/appwire-client/examples/discovery.mjs
```

For that example, the parameters file could contain
`{"prefix":"/path/to","limit":10,"includeFiles":false}`. Paths and URLs above
are placeholders; choose an appropriate hub and local private directory.

Standard output contains only the action, outcome, and result count when
available. To retain the full structured response, set
`EVENER_DISCOVERY_OUTPUT_FILE` to an absolute, nonexistent file path. The example
creates it exclusively with mode `0600`; it refuses to overwrite an existing
file. On a failed read, it closes and leaves the reserved incomplete file in
place so a concurrent replacement can never be deleted through a pathname
race. If no output path is set, the full response is not written to disk.
Known response fields are validated and unknown fields are retained in the
readback.

The shared connection example uses the platform WebSocket. If your hub requires
an Authorization header, adapt `clientFromEnvironment` to the authenticated
`socketFactory` described above before using these examples.
