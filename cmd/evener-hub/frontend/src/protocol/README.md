# @evener/appwire-client

The framework independent TypeScript client for Evener's AppWire protocol. It
has no runtime dependencies and exports the client, the connection seam
applications program against, the transport contract, generated protocol
types, wire errors with their session classifiers, the rejection classifier
and the user-facing message helpers every failure display goes through, the
pure question formatter, the ask_user question parser and the answered recap
it reads back out of a transcript, the live-question derivation an answering
dock renders from a thread, the translation that turns a composer's
`[image N]` attachment markers into prose at send, the thread view model
and its notification reducer, the activity tree parser, merge and disclosure
rules, the job log tail parser, the send/queue availability table, the
send/steer/queue/drain routing decisions a composer makes off it, the stable
delegate status rule, the display formatters both apps render counts,
durations and clock times with, the text and argument helpers a tool call's
rendering is built from, and the doc-pane URL builders, which hang their hrefs
off a base origin the host supplies (empty for a same-origin web page). The
doc-pane data layer is published at the `./docContent` subpath as well, where
`readDocFile` takes the host's `DocPort` - that base origin paired with a
fetch: the package issues no request of its own and names neither an origin
nor a credentials policy.

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
