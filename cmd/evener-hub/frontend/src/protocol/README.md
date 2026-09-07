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

## Structured question replies

`composeAskAnswers(items)` and the `AskAnswerItem`/`AskResolution` types expose
the answer format used by the web and native apps. Pass questions in posting
order across the complete pending batch. Headers containing brackets or line
breaks are encoded; labels, free text, leaning and notes retain the reply
format's escaping. Notes are available for every resolution.

```ts
import { composeAskAnswers, type AskAnswerItem } from "@evener/appwire-client";

const answers: readonly AskAnswerItem[] = [
  { header: "Delivery", resolution: { kind: "option", labels: ["Keep as draft"] }, note: "" },
];
const text = composeAskAnswers(answers);
```

This pure formatter does not validate selections, preselect recommendations,
send a message or confirm delivery. Validate labels, single/multiple selection
and fallback availability against the reviewed questions first. A `null`
resolution formats as a skip; do not use it to silently answer an unresolved
batch. Submit the resulting text through the current `turn/start` contract
with the expected session instance and a stable caller-authored mutation ID.
There is no separate question-answer RPC or atomic question-generation guard.

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
read-only recipe, **not full protocol coverage**. The recipes below cover selected
management and recovery paths. Additional creation cases, jobs,
continuous reconnect recovery, credentials and hub upgrades remain to be added.

Run `node node_modules/@evener/appwire-client/examples/coverage.mjs` to inspect
recipe coverage against the generated catalog. It lists every uncovered request
and notification, including reserved entries that require support classification.
The report measures recipe presence, not exhaustive branch or outcome coverage.

### Reading the task list

`tasks.mjs` reads `evener/tasks/list` once for `EVENER_REF`, or for the exact
`{ "ref": "local:..." }` object in `EVENER_TASKS_PARAMS_FILE`. It performs no
mutation or automatic retry. For example, with the connection variables above:

```sh
EVENER_REF=local:... node node_modules/@evener/appwire-client/examples/tasks.mjs
```

The CLI prints availability, task count and counts by status. An unavailable
list (`data: null`) has `available: false` and `taskCount: null`; an available
empty list has `available: true` and `taskCount: 0`. Programmatic consumers can
import `runTasks` from `examples/tasks-logic.mjs` to receive complete task rows
in `readback`, including descriptions, prompts, dependencies, notes, optional
timestamps and future fields. Rows retain server order. The recipe validates
required types and statuses, safe positive task IDs, unique IDs, and optional
field types; it preserves timestamp strings without interpreting dates. Invalid
or incomplete responses fail as a whole. The CLI never prints raw task rows or
transport error details. Task execution, mutation and job output require their
own workflows; reading a task with status `done` is only a state observation.

### Reviewing and changing a goal

`goals.mjs` reads the current goal by default. Set `EVENER_GOAL_PARAMS_FILE` to a
JSON file containing `{ "ref": "local:..." }`. An optional, unused absolute
`EVENER_GOAL_REVIEW_FILE` saves `ref`, `expectedInstanceId` and the complete
`reviewedGoal` (or explicit `null` when absent) privately with exclusive creation
and mode 0600. Standard output reports outcome, presence and the acknowledgment's
`started` boolean, without printing the objective.

```sh
EVENER_GOAL_PARAMS_FILE=/absolute/goal-read.json \
EVENER_GOAL_REVIEW_FILE=/absolute/goal-review.json \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
node node_modules/@evener/appwire-client/examples/goals.mjs
```

Author a decision using those three review fields. For `EVENER_GOAL_ACTION=set`,
also supply a nonempty `objective` string; the recipe preserves the authored
text. For `clear`, omit `objective`; the wire request sends an empty objective.
Both actions require `EVENER_GOAL_MUTATION=1` and a separately authored
`EVENER_GOAL_OWNED_HUB` matching `EVENER_RPC_URL`.

```sh
EVENER_GOAL_ACTION=set EVENER_GOAL_MUTATION=1 \
EVENER_GOAL_OWNED_HUB=ws://127.0.0.1:9180/rpc \
EVENER_GOAL_PARAMS_FILE=/absolute/goal-set.json \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
node node_modules/@evener/appwire-client/examples/goals.mjs
```

The recipe checks the session instance, current goal and goal capability before
one `goal/set` request. **This preflight is nonatomic:** that RPC has no instance
precondition, goal revision or mutation ID. Another writer can change the goal
after the check, and the hub can resume an exited session. The recipe cannot
provide server idempotency or prevent that race.

Programmatic callers can import `runGoals` from `examples/goals-logic.mjs` and
own the client lifetime. The function validates the boolean `started` response
and reads the same session again after either acknowledgment or failure. A lost
or malformed reply remains uncertain (CLI exit 2), even when the requested state
appears in readback; no action is automatically replayed. A readback error throws,
and dual failures remain ordered in an `AggregateError`. A goal can complete or
change before readback, so the returned goal is current observed state.
`started: false` is a valid acknowledgment, and neither boolean proves goal
completion. All outcomes retain `execution: "unverified"`. Actual autonomous
goal continuation and terminal-state evidence require a separate harness run.

### Reviewing and changing queued messages

`queue.mjs` lists a session queue by default. Use a parameters file containing
`{ "ref": "local:..." }`. An optional, unused absolute
`EVENER_QUEUE_REVIEW_FILE` saves the full queue text, entry IDs, revision and
session instance privately (mode 0600, exclusive creation). Standard output
contains outcome and depth only.

```sh
EVENER_QUEUE_PARAMS_FILE=/absolute/queue-read.json \
EVENER_QUEUE_REVIEW_FILE=/absolute/queue-review.json \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
node node_modules/@evener/appwire-client/examples/queue.mjs
```

For a mutation, author a fresh parameters file using the reviewed reference and
`expectedInstanceId`. Supply a stable, caller-authored `clientMutationId` for
that decision. Choose `EVENER_QUEUE_ACTION` and add its parameters:

| Action | Additional parameters | Effect |
| --- | --- | --- |
| `queue` | `input: [{ "type": "text", "text": "..." }]` | Accept text for later processing. This recipe supports text only; the typed API also supports images. |
| `cancel` | `index`, `expectedEntryId` | Remove the reviewed entry. |
| `promote` | `index`, `expectedEntryId` | Use that entry as steering, or resume a held queue. |
| `drain` | `expectedQueueRevision` | Combine the complete reviewed queue as one input. This recipe supplies no additional input. |

Mutations additionally require `EVENER_QUEUE_MUTATION=1` and a separately
authored `EVENER_QUEUE_OWNED_HUB` exactly matching `EVENER_RPC_URL`. The recipe
checks capabilities, the session instance, and the selected entry or complete
queue revision before dispatch. The server receives the instance and entry/
revision preconditions too. Resuming a held queue also releases remaining
waiting messages; review the full queue before promoting an entry.

```sh
EVENER_QUEUE_ACTION=cancel EVENER_QUEUE_MUTATION=1 \
EVENER_QUEUE_OWNED_HUB=ws://127.0.0.1:9180/rpc \
EVENER_QUEUE_PARAMS_FILE=/absolute/queue-cancel.json \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
node node_modules/@evener/appwire-client/examples/queue.mjs
```

Programmatic consumers can import `runQueue` from packaged
`examples/queue-logic.mjs` and own the client lifetime. It sends at most one
mutation, validates the operation-specific receipt, and reads the same session
again after either acknowledgment or failure. A lost or malformed reply returns
`outcome: "uncertain"` even if readback has changed; the CLI exits 2. A readback
failure throws (both failures are retained in an ordered `AggregateError`).
No response or readback triggers an automatic replay. All results report
`execution: "unverified"`: pending input can already have been consumed before
readback, and acknowledgment does not prove the model processed it. The contract
tests cover client behavior at a scripted transport boundary; real native
queue interactions require separate acceptance evidence.

### Reviewing and answering structured questions

The readonly default for `questions.mjs` requires a parameters file containing
`{"ref":"local:OWNED_SESSION"}`. It prints counts only. To save the complete
reviewed calls and question text, explicitly supply an absolute, unused output
path; that file is created exclusively with mode 0600.

```sh
EVENER_QUESTION_PARAMS_FILE=/absolute/path/to/read-questions.json \
EVENER_QUESTION_REVIEW_FILE=/absolute/path/to/question-review.json \
node node_modules/@evener/appwire-client/examples/questions.mjs
```

The normal connection variables above still apply. Review the saved
`questions` and retain its `ref`, `expectedInstanceId` and complete
`reviewedCalls` without editing them. The answer parameters contain those
three fields, a stable caller-authored `clientMutationId`, and an aligned
`selections` array with exactly one explicit resolution and note per question:

```json
{
  "resolution": { "kind": "option", "labels": ["Keep as draft"] },
  "note": ""
}
```

This illustrates one selection, not a complete parameters file. Other
resolutions are `free` with a string `text`, `decide` with a string
`leaning`, `fallback` when the question supplies `if_unanswered`, and
`skip`. Notes are universal. Recommendations never fill missing SDK
answers. Multiple labels require `multi_select: true`.

```sh
EVENER_QUESTION_MUTATION=1 \
EVENER_QUESTION_OWNED_HUB=ws://127.0.0.1:9180/rpc \
EVENER_QUESTION_ACTION=answer \
EVENER_QUESTION_PARAMS_FILE=/absolute/path/to/question-answer.json \
node node_modules/@evener/appwire-client/examples/questions.mjs
```

The separately authored owned URL must equal `EVENER_RPC_URL`. Programmatic
consumers can import `runQuestions` from the packaged
`examples/questions-logic.mjs`; list mode returns the thread, reviewed
`calls` and flattened `questions` without writing a file.

The recipe follows opaque v4 item cursors back through the latest user input,
collecting every completed successful `ask_user` call in posting order. It
rejects malformed questions, overlapping pages, stale cursors and a changing
latest window. Stale or incomplete reviews require a new read and review.
Answer mode rereads and compares the complete batch, requires the same session
instance, server-confirmed pending questions and send capability, then issues
one `turn/start`. These question checks are nonatomic preflight; the server
has no expected-question-generation field.

Receipt decoding checks the caller mutation ID, thread/instance/turn identity,
disposition and pending projection state. A fresh metadata read follows both
acknowledgments and failures. An acknowledgment means accepted input, not
completed execution; `execution` remains `"unverified"`. Readback may already
show a different question. A lost or malformed acknowledgment stays uncertain
even if the previous question disappears: exit 2, no replay. Do not blindly
rerun the answer command. Other failures exit 1; the review file preserves
the original decision. If both mutation and readback fail, the programmatic
helper retains both causes in an ordered `AggregateError`.

`questions.contract.mjs` tests this request boundary without credentials,
providers or network access. It is included in external tarball qualification;
real provider completion and native question-sheet recovery need separate
acceptance evidence.


### Streaming, paging, and rejoin

`streaming-rejoin.mjs` is a read-only recipe for an existing session. It reads a
bounded fragment view, carries the server's older item cursor unchanged into
`thread/turns/list`, checks transcript keys and `{entry,item}` fragment
positions, unsubscribes, and replaces the subscription on rejoin. It verifies
the same immutable session ref and instance identity throughout and prints only
counts and notification names. It performs no mutation and never retries one.

```sh
EVENER_THREAD_REF=opaque-ref-from-owned-fixture \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/hub/auth-token \
EVENER_CWD=/path/on/hub \
node node_modules/@evener/appwire-client/examples/streaming-rejoin.mjs
```

The recipe requires an owned active session with an instance identity and reads at most 20 items per
request, following one older page when available. Entry zero is valid for the
system prelude. A stale cursor replaces the retained window with a subscribed
snapshot. During a bounded observation window (five seconds by default;
`EVENER_OBSERVE_SECONDS` accepts 0–60), matching item/turn/reset/resync events
trigger one authoritative snapshot read after the window. It then unsubscribes
and rejoins, verifying the same instance. Notifications for another ref are
ignored. Counts distinguish actual paging and stale recovery from unexercised
branches. This demonstrates bounded snapshot reconciliation, not an incremental
stream renderer or continuous reconnect loop. Use a longer transcript to
exercise paging and a concurrent owned writer to observe live events.
It does not establish complete streaming, notification, failure or native
release coverage.
Its deterministic contract checks can run without a hub:

```sh
node --test examples/streaming-rejoin.contract.mjs
```

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

### Session lifecycle with an interruptible scripted provider

`session-lifecycle.mjs` creates an empty session, subscribes, sends a unique
fixture, checks start/interrupt receipts and lifecycle pushes, verifies the
submitted input in an authoritative read, then unsubscribes. It leaves the
interrupted session for inspection. Use an isolated hub with an empty-task-capable
harness and a scripted provider that waits for interruption:

```sh
EVENER_EXAMPLE_CREATE_SESSION=1 \
EVENER_MODEL_PROVIDER=fake EVENER_MODEL=fake-test-model \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/isolated/project/on/hub \
node node_modules/@evener/appwire-client/examples/session-lifecycle.mjs
```

The provider/model are validated against the hub catalog before creation. The
script installs listeners before subscribing and handles lifecycle pushes that
precede mutation responses. On failure it attempts to stop only its own session
with its original instance identity, then unsubscribes. It never replays Start
or deletes history. If a creation response is lost, inspect the hub manually;
the script cannot identify the created session. No other writer should mutate
this fixture session during the run. Natural completion, transcript deltas,
approvals, queue control and reconnect recovery require separate recipes.

### Hub preferences and revision conflicts

`preferences.mjs` reads capability-supported keybindings and transcript display
settings. It prints revisions and load status without exposing the configuration.
The default invocation uses only GET requests.

An explicit write replaces the **complete** selected domain configuration from a
JSON file. For keybindings, supply `{ "version": 1, "rules": [...] }`, retaining
every rule you intend to keep, including unknown action names. For transcript
display, supply the generated `TranscriptDisplayConfig` shape with `version`,
`content` and `advanced`. Transcript writes target the mobile layout; desktop
preferences are unaffected. Read current values through the typed GET contract
before preparing the file.

Use a disposable hub whose endpoint you explicitly confirm:

```sh
EVENER_PREFERENCES_MUTATION=1 \
EVENER_PREFERENCES_OWNED_HUB=ws://127.0.0.1:9180/rpc \
EVENER_PREFERENCES_DOMAIN=transcript \
EVENER_PREFERENCES_CONFIG_FILE=/path/to/complete-mobile-config.json \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/isolated/project/on/hub \
node node_modules/@evener/appwire-client/examples/preferences.mjs
```

The recipe derives `expectedRevision` from a fresh read of the selected domain,
then issues one PATCH. A conflict or lost reply triggers one independent GET,
with no replay. Exit 0 means the read or acknowledged write completed; exit 2
means a conflict or uncertain write with readback; exit 1 means the operation
could not complete. Inspect the current settings and prepare a new deliberate
edit before another write. The script does not restore changes automatically.
Notifications, native editor persistence and concurrent editor reconciliation
require separate qualification.

Offline behavior checks run from the package:

```sh
node --test examples/preferences.contract.mjs
```

### Organization catalog and pin management

`organization.mjs` reads the v2 manifest and every pin catalog page, with at
most 40 sections per request. Assign and unpin also read the selected session's
current location. The default command only reads.

Write mode performs one explicitly selected assign, unpin, rename, or delete
operation. It requires an owned endpoint matching the connected URL, plus a
JSON target. For example, assign a fixture session to a named section:

```sh
EVENER_ORGANIZATION_MUTATION=1 \
EVENER_ORGANIZATION_OWNED_HUB=ws://127.0.0.1:9180/rpc \
EVENER_ORGANIZATION_ACTION=assign \
EVENER_ORGANIZATION_TARGET='{"sessionRef":"opaque-owned-ref","sectionName":"Focus"}' \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/path/on/test-hub \
node node_modules/@evener/appwire-client/examples/organization.mjs
```

Assign requires exactly one of `sectionId` or `sectionName`. Unpin takes
`sessionRef`; rename takes `sectionId` and `name`; delete takes `sectionId`.
Deleting a pinned section preserves its sessions.

The recipe keeps one generation across its reads and one catalog revision
across catalog pages. Revisions and ETags belong to individual resources:
manifest, catalog, and location versions can differ. Readback checks receipt
revision floors for the manifest and catalog it loaded. It does not treat a
pin-section or project revision as a location revision.

Exit 0 reports a successful read or an acknowledged operation with readback.
Exit 2 reports rejection, an unknown mutation outcome, or acknowledged but
failed readback. Exit 1 reports input, connection, or initial-read failure.
An unknown outcome is retained even if a later read succeeds; the recipe never
replays the mutation or claims that readback proves which client made a change.

Run `node --test examples/organization.contract.mjs` for deterministic boundary
tests. Package qualification also runs these checks outside the checkout.

### Plugin installation and management

`plugin-management.mjs` connects and lists installed plugins by default. Select
one action explicitly to install, upgrade, remove, enable, disable, or set auto
upgrade on an owned fixture. A plugin is identified by its exact `plugin` and
`marketplace` pair. Installation requires that pair to be absent from the fresh
list; the other actions require it to be present. The marketplace must already
be configured on the hub.

```sh
EVENER_PLUGIN_MUTATION=1 \
EVENER_PLUGIN_OWNED_HUB=ws://127.0.0.1:9180/rpc \
EVENER_PLUGIN_ACTION=setAutoUpgrade \
EVENER_PLUGIN_TARGET='{"plugin":"fixture","marketplace":"owned"}' \
EVENER_PLUGIN_AUTO_UPGRADE=false \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/isolated/project/on/hub \
node node_modules/@evener/appwire-client/examples/plugin-management.mjs
```

The action names are `install`, `upgrade`, `remove`, `enable`, `disable`, and
`setAutoUpgrade`. Only `setAutoUpgrade` uses `EVENER_PLUGIN_AUTO_UPGRADE`, which
must be the JSON boolean `true` or `false`. Installation and upgrade may fetch
and write plugin contents; use a disposable marketplace and hub. The script
prints only the outcome and installed count. It never prints install paths.

### Provider instance configuration

`instances.mjs` lists provider instances and the registry write-refusal status
by default. Write mode selects one `create`, `edit`, `remove`, or `setDefault`
operation and reads its parameters from a JSON file:

```sh
EVENER_INSTANCE_MUTATION=1 \
EVENER_INSTANCE_OWNED_HUB=ws://127.0.0.1:9180/rpc \
EVENER_INSTANCE_ACTION=edit \
EVENER_INSTANCE_PARAMS_FILE=/path/to/instance-edit.json \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/isolated/project/on/hub \
node node_modules/@evener/appwire-client/examples/instances.mjs
```

Create requires `name` and `base`; optional fields are `baseUrl`, `protocol`,
`surface`, `vars`, `apiKeyEnv`, and `credentialHeader`. The last two configure
credential environment references, not a literal API key. Edit takes `name`
and any of `baseUrl`, `clearBaseUrl`, `protocol`, `surface`, or `vars`. Remove
and setDefault take only `name`. For example, `{ "name": "fixture",
"clearBaseUrl": true }` restores the inherited endpoint. Omitted edit fields
remain omitted: the example never fills them with displayed defaults. The hub
merges nonempty `vars` maps; `{}` does not clear stored variables. Empty strings
do not clear endpoint/protocol/surface overrides. `clearBaseUrl: true` takes
precedence over an accompanying `baseUrl`.

The recipe refuses writes when the fresh registry reports `writesRefused`.
Create requires the name to be absent; the other actions require it to be
present. The hub validates provider-specific constraints. No credentials are
stored and no OAuth flow starts through this recipe. Output contains only the
outcome, instance count and write-refusal flag.

### Marketplace catalogs and sources

`marketplaces.mjs` lists registered marketplaces by default. Select `browse` with
`EVENER_MARKETPLACE_PARAMS_FILE` containing `{ "name": "owned-fixture" }` to read
one catalog. The request uses the registry alias; the response's `name` is the
manifest's catalog name and may differ. Keep using the selected registry alias
for plugin installation and marketplace management. Write actions are
`add`, `refresh`, and `remove`; refresh/remove take the same name-only file.

```sh
EVENER_MARKETPLACE_MUTATION=1 \
EVENER_MARKETPLACE_OWNED_HUB=ws://127.0.0.1:9180/rpc \
EVENER_MARKETPLACE_ACTION=refresh \
EVENER_MARKETPLACE_PARAMS_FILE=/path/to/marketplace.json \
EVENER_RPC_URL=ws://127.0.0.1:9180/rpc \
EVENER_TOKEN_FILE=/path/to/test-hub/auth-token \
EVENER_CWD=/isolated/project/on/hub \
node node_modules/@evener/appwire-client/examples/marketplaces.mjs
```

Add takes `{ "name": "owned-fixture", "source": { "kind": "url",
"url": "/isolated/git/repository/on/hub" } }`. Source kinds are `github`
with `repo`, `url` with `url`, `directory` with `path`, and `git-subdir` with
`url` and `path`. Optional `ref` and `sha` are preserved exactly, including
empty strings; omitted fields stay omitted. Paths refer to the hub filesystem.
The hub applies source-specific rules and may fetch Git content.

An explicitly named add requires that name to be absent from a fresh list;
refresh/remove require it to be present. Add also accepts an omitted name,
which lets the hub derive it from the manifest. The hub upserts by name, so a
derived name can replace an existing registration; the recipe cannot preflight
that name before loading the source. Use an explicit unused name and an owned
disposable source. Preflight does not prevent concurrent changes. Output contains
only the outcome and marketplace or plugin count.

### Sandbox approval decisions

`approvals.mjs` defaults to a read-only, non-subscribing `thread/read`. Supply
`EVENER_APPROVAL_PARAMS_FILE` with `{ "ref": "local:owned-session" }`. The
`runApprovals` helper returns the full readback so the caller can review the
pending escalation privately; the CLI prints only its count.

To resolve a reviewed approval, select `EVENER_APPROVAL_ACTION=resolve`, set
`EVENER_APPROVAL_MUTATION=1` and separately confirm the endpoint with
`EVENER_APPROVAL_OWNED_HUB`. The parameters file must contain `ref`,
`expectedInstanceId`, the complete reviewed `escalation` object, and an explicit
boolean `approve`. Use the same connection environment as the marketplace
example. Do not reconstruct the card from its ID alone. The helper connects,
reads the current instance and pending cards, and refuses a changed or missing
card before sending a decision. It captures the authored parameters before
awaiting connection; `false` means deny and is never defaulted to approval.

The resolver receives only `ref`, `escalationId`, and `approve`. Its contract
has no expected-instance or mutation-ID field, so this client preflight cannot
atomically prevent replacement between reading and deciding. Readback must
still belong to the reviewed instance. The result always reports
`execution: "unverified"`: an acknowledgment or disappearance of a pending card
does not prove that a tool resumed. Unknown/already-resolved cards can conflict;
the recipe never resubmits them. Question answers use a separate turn contract
and are outside this example.

### Management mutation recovery

These management and approval recipes require an explicit mutation opt-in and a separately
supplied owned endpoint equal to `EVENER_RPC_URL`. Their logic helpers expect the
client returned by `connection.mjs` for that endpoint. Ownership confirmation is
an operator guard, not authentication or a check of an arbitrary client's URL.
Preflight reads are not atomic with writes; use fixtures without concurrent
writers. These methods have no mutation-ID or revision precondition in their
current contracts.

After one mutation attempt the recipe performs one fresh list or thread read. A
valid acknowledgment followed by readback reports `acknowledged`. A rejected or
lost reply reports `uncertain` even if readback shows the desired state. The
recipes never replay mutations, restore configuration, or attribute a concurrent
writer's change to themselves. A disconnected client can make the immediate
readback fail; this example does not wait through a reconnect loop.

Exit 0 means a read or acknowledged operation with readback; exit 2 means an
uncertain operation with readback; exit 1 means invalid input or an incomplete
operation, including failed readback after an acknowledgment. A valid acknowledgment
followed by a failed read throws `AcknowledgedReadbackError`, exported from
`examples/management-recovery.mjs`. Its `outcome` remains `"acknowledged"`,
`execution` is `"unverified"`, `method` identifies the operation, and `cause`
preserves the read failure, including an undefined or null thrown value.
The CLI emits `{"outcome":"acknowledged","execution":"unverified","readback":"unavailable"}`
to stderr and exits 1. This distinguishes an accepted change from an operation
whose acknowledgment is unknown without claiming that current state was read.
Both failures remain ordered in `AggregateError` when mutation and readback fail.
CLI diagnostics omit potentially private causes. After exit 1 or 2, run the default
read-only command to inspect current
state before preparing a deliberate next edit; do not rerun the write command
as a recovery shortcut.

Deterministic contract checks exercise every action and recovery outcome at the
SDK request boundary, including malformed lists, lost replies and both-error
cases. `make test-api-package` also runs them from the packed, installed artifact:

```sh
node --test examples/plugin-management.contract.mjs examples/instances.contract.mjs
node --test examples/marketplaces.contract.mjs examples/approvals.contract.mjs
```

These checks do not establish actual plugin download/installation, provider
connectivity, notification reconciliation, continuous reconnect recovery, or
native release acceptance. Those require separate owned-hub qualification.
