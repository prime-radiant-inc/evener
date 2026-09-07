# @evener/appwire-client

The framework-independent TypeScript client used by Evener's web and native
apps. The package has no runtime dependencies. Its compiled CommonJS distribution
supports Node require and named imports from ESM. It contains the existing client
implementation and generated method, result and notification types; it does
not copy or reimplement the app's transport.

This package is not yet published. Build a tarball from this checkout:

```sh
cd cmd/evener-hub/frontend/src/protocol
npm install
npm pack
```

Install the resulting tarball in a separate project. Node clients that need an
Authorization header can supply the `ws` package through `socketFactory`.
Browser clients use the platform WebSocket and the hub's browser authentication.
React Native clients can supply their platform's authenticated socket factory.

```ts
import { AppwireClient, WireError } from "@evener/appwire-client";

const hub = new AppwireClient({
  url: "wss://hub.example/rpc",
  clientInfo: { name: "my-client", version: "1.0.0" },
});
try {
  const hello = await hub.connect();
  const catalog = await hub.request("model/list", {});
  console.log(hello.protocolVersion, catalog.data.length);
} catch (error) {
  if (error instanceof WireError) console.error(error.code, error.message);
  else throw error;
} finally {
  hub.close();
}
```

`request(method, params, { timeoutMs })` accepts every generated method name and
returns its associated result type. A catalog entry marked unimplemented is not
a supported server operation. TypeScript types do not validate arbitrary JSON
at runtime; the handshake is validated, while method payloads are typed contracts.

`onNotification`, `onStateChange`, `onHandshakeResult` and `onReady` return
unsubscribe functions. Install notification handlers before subscribing to a
thread. `onReady` runs on subsequent successful reconnects as well; the caller
must restore subscriptions and reconcile authoritative state. The client does
not retain or replay application mutations and does not own transcript storage.

A request timeout or closed connection leaves a mutation's outcome uncertain.
Do not retry `thread/start` blindly. Retry-safe session mutations require their
specific mutation-ID contract; the client does not invent those IDs for callers.
Use one client and separate caches per hub. `close()` ends that connection and
rejects pending requests; create a new client for a different hub or credential.

## Runnable examples

Install the tarball and `ws` in a separate project, then run the packaged examples
(they share `connection.mjs`):

```sh
npm install /absolute/path/to/evener-appwire-client-0.1.0.tgz ws
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/hub/auth-token \
EVENER_CWD=/path/on/hub node node_modules/@evener/appwire-client/examples/inspect.mjs
```

The example reads handshake capabilities, model catalog, first session page,
launch schema and effective launch configuration. It prints counts and status,
not credentials or environment values. It performs no mutation. This is the
read-only recipe, **not full protocol coverage**. Fixtures for creation,
streaming, approvals, queue control, reconnect recovery, management, providers,
plugin management and upgrades remain to be added.

Run `node node_modules/@evener/appwire-client/examples/coverage.mjs` to inspect
recipe coverage against the generated catalog. It lists every uncovered request
and notification, including reserved entries that require support classification.
The report measures recipe presence, not exhaustive branch or outcome coverage.

The project-layer recipe exercises a reversible settings mutation, independent
readback, effective configuration, and the launch-update notification. Use an
isolated project directory on a test hub:

```sh
EVENER_EXAMPLE_WRITE_PROJECT=1 \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/isolated/project/on/hub \
node node_modules/@evener/appwire-client/examples/project-layer.mjs
```

It temporarily changes `maxRounds`, restores the original project layer, and
checks that global settings stayed unchanged. It refuses restoration if another
writer changed the project layer. AppWire has no atomic compare-and-set for this
operation: run this example without concurrent writers. An interrupted process
cannot restore settings; inspect the isolated project's layer before reusing it.
Operation and restoration failures are both reported.


### Repository trust with a disposable file

`repository-trust.mjs` requires a test hub on the **same filesystem** as the
example process. `EVENER_CWD` names an existing parent directory visible at the
same absolute path to both. The example creates its own temporary child project,
changes its file after review, verifies stale-hash rejection (`-32009`), reviews
the new revision, trusts it and verifies effective configuration independently.

```sh
EVENER_EXAMPLE_WRITE_REPO=1 \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/isolated/fixture/parent \
node node_modules/@evener/appwire-client/examples/repository-trust.mjs
```

The owned directory is removed on success or failure. Trust history remains in
the test hub's state; discard that isolated state when finished. A killed process
may leave its temporary child directory behind. This local fixture setup is not
an AppWire remote-file operation. Real clients must show the preview and obtain a
user's trust decision for the exact reviewed hash; they must never automatically
trust a newer revision because the earlier request was rejected.

### Plugin selection without creating a session

`plugins.mjs` exercises `evener/plugin/preview` with omitted selection, an explicit
empty list, one available plugin, and an unavailable name. It asserts selected
flags and structured selection errors. No plugins are installed or changed and
no session starts. If the directory has no available plugin, it explicitly
reports that the single-plugin case was not exercised.

```sh
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/hub/auth-token \
EVENER_CWD=/path/on/hub \
node node_modules/@evener/appwire-client/examples/plugins.mjs
```

Run against a stable catalog: concurrent installation/removal can legitimately
change the candidates between preview requests and fail the assertions.
