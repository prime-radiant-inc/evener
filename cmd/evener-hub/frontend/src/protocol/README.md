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
management and recovery paths, including activity-tree traversal. Additional
creation cases, continuous reconnect recovery and hub upgrades
remain to be added.

Run `node node_modules/@evener/appwire-client/examples/coverage.mjs` to inspect
recipe coverage against the generated catalog. It lists every uncovered request
and notification, including reserved entries that require support classification.
The report measures recipe presence, not exhaustive branch or outcome coverage.

### Discovering hub paths, projects, and settings

`discovery.mjs` runs one explicitly selected read. Set `EVENER_DISCOVERY_ACTION`
and optionally `EVENER_DISCOVERY_PARAMS_FILE` to a JSON object containing only
the RPC parameters below. Paths refer to the hub's filesystem.

| Action | Method | Parameters |
| --- | --- | --- |
| `paths` | `evener/paths/complete` | Optional `prefix`, `limit`, `includeFiles` |
| `projects` | `evener/projects/recent` | Optional `limit` |
| `validatePath` | `evener/path/validate` | `path`, optional `kind` |
| `gitHead` | `evener/git/head` | `cwd` |
| `search` | `evener/search` | Optional `query` |
| `harnesses` | `evener/harnesses/list` | None |
| `settings` | `evener/settings/overview` | None |

An omitted/empty completion prefix uses the hub's home-directory default.
A nonpositive path/project limit uses the server default. Empty path validation
returns a normal invalid-path result; it is not a transport error. An empty
Git head or search field remains empty. Settings sections and optional fields
retain their wire omission semantics. Known response fields are checked and
future fields are preserved. The recipe does not create directories, launch
sessions, execute harnesses or edit settings.

Import `runDiscovery(hub, { action, params })` from `examples/discovery-logic.mjs`
for complete `readback`. The CLI prints only action/outcome/counts. Set
`EVENER_DISCOVERY_OUTPUT_FILE` to a new absolute filename for private complete
output; it may contain paths, session titles and server diagnostics. The file
is reserved before connection with exclusive creation and mode `0600`.
Incomplete output is removed on failure only if the path still identifies the
reserved file. No request is retried automatically.

### Reviewing and managing a session

`session-management.mjs` defaults to `EVENER_SESSION_MANAGEMENT_ACTION=list`.
Put `{ "ref": "local:session-id" }` in `EVENER_SESSION_MANAGEMENT_PARAMS_FILE`.
The recipe reads `thread/read` without turns or subscription. Programmatic
`runSessionManagement` from `examples/session-management-logic.mjs` returns the
snapshot and a review containing the thread ID, instance ID and optional name.
Source-backed sessions without an instance remain readable but have no usable
mutation review. Omitted controls remain unavailable.

Set `EVENER_SESSION_MANAGEMENT_REVIEW_FILE` in list mode to save a new private
absolute file containing `ref`, `expectedInstanceId`, and `reviewed`. Output is
reserved before connection, uses exclusive `0600` creation, and shares the
ownership-aware failure cleanup described above. Review this state before use.

Mutations require `EVENER_SESSION_MANAGEMENT_MUTATION=1` and
`EVENER_SESSION_MANAGEMENT_OWNED_HUB` equal to `EVENER_RPC_URL`. Use the reviewed
file as the params file, adding `name` only for `rename`. Supported actions are
`rename`, `compact`, `clear`, and `shutdown`. The recipe captures authored input
before connecting, rereads the selected session, and checks the reviewed
identity/name and current action capability before dispatching once.

Rename, compact and shutdown have no atomic instance/review guard in their
wire parameters; the preflight does not prevent another client's subsequent
change. Rename and compact return an authoritative readback of the same
instance. Shutdown acknowledges an asynchronous stop and reports readback
unavailable, without claiming that the session has already stopped.

Clear sends a fresh client mutation ID and the reviewed expected instance.
Its validated replacement snapshot is the atomic readback: the qualified ref
stays the same, while the receipt must identify the returned replacement thread
and nonempty instance. Missing, malformed or unrelated receipts leave the
outcome uncertain and are not returned as validated readback. No action is
automatically replayed after failure; read current state and review it again.
CLI output contains metadata only, and uncertain outcomes exit with code 2.
An acknowledgment does not prove provider execution or attribute later state
to this client.

### Reading the command catalog

Run `commands.mjs` with the connection variables above to read
`evener/command/list` once. The hub-wide catalog combines enabled plugin commands
and Evener-wide user commands; it excludes project-scoped commands. Names are
unqualified: retain `pluginName` and `source` when distinguishing entries.

Programmatic consumers can import `runCommands` and `summarizeCommands` from
`examples/commands-logic.mjs`. The reader validates descriptors and preserves
complete rows, order and future fields. The current Go response serializes an
empty catalog as `commands: null`; the recipe returns an empty `readback` array.
It also accepts an empty array. This means no commands, not an unavailable
catalog. Missing/malformed response fields reject the entire read. The CLI emits
only counts by source, never command descriptions or prompts. Reading a command
does not execute it, and failures do not trigger automatic retries.

### Reading and changing stored credentials

`credentials.mjs` uses the connection variables above, including `EVENER_CWD`
and `EVENER_TOKEN_FILE`, plus `EVENER_CREDENTIAL_ACTION`. The default action,
`list`, takes no parameters. `status` reads exactly `{ "provider": "my-instance" }`
from `EVENER_CREDENTIAL_PARAMS_FILE`. Import `runCredentials` from
`examples/credentials-logic.mjs` for complete status objects in `readback`;
the list action returns an array of status objects. An empty server list,
including `providers: null`, becomes an empty array. Invalid fields or duplicate
provider identities reject the entire read. Unknown status fields and optional
field omission are preserved.

Mutations require `EVENER_CREDENTIAL_MUTATION=1` and
`EVENER_CREDENTIAL_OWNED_HUB` equal to `EVENER_RPC_URL`. Put `provider` and the
complete status object from the reviewed read in `reviewed` in the private JSON
params file, plus `value` only for either setter. Keep this file private, for
example with mode `0600`; it contains credential data. The CLI reads the file
without placing its contents in command-line arguments.

| Action | Effect |
| --- | --- |
| `apiKey/set` | Store an authored key for an instance advertising `apiKey` auth. |
| `credentialJson/set` | Store Google credential JSON for an instance advertising `credentialJson` auth. The server validates its supported type and fields. |
| `apiKey/clear` | Clear only the stored file entry, including a stray key behind OAuth or ADC. |
| `logout` | Clear the layer selected by the server's logout contract. Non-Codex instances clear the stored file entry; Codex clears its OAuth record first, or a stray stored key when no OAuth record exists. |

The recipe captures parameters before connecting, validates support and auth mode,
and compares the entire current status with `reviewed` before dispatch. The
snapshot contains no secret fingerprint or instance-definition revision: it
cannot detect replacement with identical status, or prevent a concurrent change
between read and write. An environment or ADC credential can remain active after
clear/logout; `signedIn` does not prove an exact secret or provider connectivity.

One mutation is followed by one authoritative status read. A validated reply
means `acknowledged`; a lost or malformed reply remains `uncertain` even when
readback succeeds. Acknowledged writes whose readback fails retain that outcome
in `AcknowledgedReadbackError`; two failures retain both causes in an
`AggregateError`. Neither permits automatic replay. The result contains status
readback, not the logout reply's `removed` field. Provider execution remains
`unverified`. The CLI prints only action/outcome/execution and status booleans or
a provider count; it excludes provider names, account details, environment names,
keys, credential JSON and private error bodies. OAuth browser/device flows use
the recipe below; `evener/auth/test` remains a separate workflow.

### Device and browser OAuth

`oauth.mjs` performs one OAuth operation per invocation. Use the connection
variables above plus `EVENER_OAUTH_ACTION`, `EVENER_OAUTH_PARAMS_FILE`,
`EVENER_OAUTH_MUTATION=1` and `EVENER_OAUTH_OWNED_HUB` exactly equal to
`EVENER_RPC_URL`. Polling also requires mutation opt-in: an authorized poll
exchanges and stores credentials. Provider values identify authored instances.

| Action | Private JSON parameters | Result |
| --- | --- | --- |
| `device/start` | `{provider, reviewed}` | Device flow or explicit browser fallback. |
| `login/start` | `{provider, reviewed}` | Browser flow. |
| `device/poll` | `{provider, flow}` | Pending, expired, authorized or uncertain. |
| `login/complete` | `{provider, flow, redirectUrl}` | Authorized or uncertain. |

For starts, obtain the complete reviewed status with `runCredentials` from
`credentials-logic.mjs`. The recipe checks current OAuth support and exact
status equality before starting. This preflight is not an atomic revision check.
A returned flow records `rpcUrl`, `provider`, `kind`, `flowId` and `url`, plus
`userCode` and `intervalSeconds` for a device flow. Retain the full returned
object in `flow` for continuation. Different endpoints, providers or flow kinds
reject before connection; caller input is copied before the first await.
Programmatic callers must provide a hub configured for the owned endpoint.

Start actions also require `EVENER_OAUTH_OUTPUT_FILE`. The CLI reserves a new
file exclusively with mode `0600` before connecting, then writes the returned
flow there. Existing files are never overwritten. Keep params, flow and redirect
files private. The CLI prints only action/outcome and, after readback, `signedIn`.
It never prints URLs, codes, providers, account details, file contents or private
errors. Fallback and failed starts remove only the invocation's empty reservation.
A filesystem failure after the hub creates a flow reports `started` with
`challenge: write_unconfirmed`; an empty reservation is removed, while nonempty
output is retained for inspection. Do not assume a retained partial file is usable.

Open the saved URL manually. For device flows, call `device/poll` deliberately,
respecting the saved interval; use a five-second delay for a zero interval and
at least one second otherwise. There is no hidden poll loop. On explicit device
fallback, review status and deliberately call `login/start`. Open its URL and
paste the full returned redirect into the private completion params file.
Canceling means discarding the local handle and private files; the wire protocol
has no cancel method. It does not revoke a flow already stored on the hub.

Import `runOAuth` from `examples/oauth-logic.mjs` for structured results.
Authorized replies require matching provider and valid `supported`, `signedIn`
and `hasStoredOAuth` fields, then receive one fresh credential status readback.
The result preserves both `acknowledged` and `readback`: a concurrent logout can
make the latter signed out. Other active credential sources do not invalidate
stored OAuth acknowledgment. A thrown or malformed mutation reply performs one
status readback and remains `uncertain` even when credentials are configured.
A failed readback after valid authorization raises `OAuthReadbackError`, extending
`AcknowledgedReadbackError` and preserving `.acknowledged` and `.cause`; if both
mutation and readback fail, `AggregateError.errors` retains both errors.
Programmatic errors and complete results may contain private data; do not log them.

Pending and expired replies return directly. Expired also covers an unknown or
already consumed flow, so read current credentials before deliberately starting
again. No result authorizes automatic replay. CLI exit code `2` means uncertain;
other failures use `1`. None of these status checks proves provider execution.

### Reading and changing session settings

`session-settings.mjs` uses `EVENER_SESSION_SETTING` (`list`, `model`,
`reasoning-effort`, or `vision-model`) and reads JSON from
`EVENER_SESSION_SETTINGS_PARAMS_FILE`. A read takes exactly `{ "ref": "local:..." }`.
Import `runSessionSettings` from `examples/session-settings-logic.mjs` to obtain
the full `readback` and `settings` objects; the CLI prints only outcome, execution
status and the number of present settings.

Mutations additionally require `EVENER_SESSION_SETTINGS_MUTATION=1` and
`EVENER_SESSION_SETTINGS_OWNED_HUB` equal to `EVENER_RPC_URL`, using the existing
owned-hub restrictions. Pass `ref`, `expectedInstanceId`, and the exact `settings`
object from the reviewed read as `reviewed`. Then supply the selected fields:

| Setting | Authored fields | Meaning |
| --- | --- | --- |
| `model` | `modelProvider`, `model` | Provider may be empty for a model ref resolved by the server; model must be nonempty. Select from the current `model/list` catalog. |
| `reasoning-effort` | `reasoningEffort` | Empty resets to the session/model default; `none` disables reasoning. The daemon validates and clamps the value. |
| `vision-model` | `visionModel` | Empty uses the active session model, `off` disables the side-channel, otherwise supply a model or provider/model ref. |

`thread.modelProvider` is the complete current model value despite its field
name. Retain it exactly for review; do not split or reconstruct a fallback
ladder from it. Optional effort/vision fields remain absent when omitted, rather
than becoming a fabricated default. The setter accepts an authored model choice;
do not assume it edits one rung of a fallback ladder.

The recipe checks the current ref, instance, relevant capability and reviewed
settings before dispatch, then checks the same thread and instance on readback.
These setters have no atomic instance/revision/mutation-ID precondition, so the
review cannot prevent a change between read and write. A valid object response
means `acknowledged`; it does not prove the requested value remains current.
Readback can show clamping or another writer's value. A lost/malformed reply
means `uncertain`, even if readback succeeds. Acknowledged mutations with failed
readback retain that distinction through `AcknowledgedReadbackError`. Neither
case authorizes automatic replay. Actual model/provider execution is unverified.

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

### Reading a job output window

`job-output.mjs` reads one `evener/jobs/output` page for `EVENER_REF` and
`EVENER_JOB_ID`. Alternatively, provide an `EVENER_JOB_OUTPUT_PARAMS_FILE`
containing exactly `ref`, `jobId`, and optional `maxBytes` and `beforeBytes`.
The reference must identify the session that owns the job.

```sh
EVENER_REF=local:... EVENER_JOB_ID=job_... node node_modules/@evener/appwire-client/examples/job-output.mjs
```

The server defaults to a 4096-byte tail. The recipe accepts `maxBytes` from
1 through 65536; `beforeBytes: 0` also reads the tail. A positive
`beforeBytes` is an exclusive lifetime byte offset. To read the preceding
window, use the returned `retainedStart` unchanged while `hasEarlier` is true.
Do not derive cursors from JavaScript string length, and stop if a page does
not advance. Output retention can remove older bytes during paging.

Programmatic consumers can import `runJobOutput` from
`examples/job-output-logic.mjs` to obtain `{ outcome: "read", readback }`.
The readback preserves log text and future fields. The exported
`parseJobLogTail` helper validates the same byte-window shape used by web and
native clients: nonnegative safe integer offsets, start no greater than total,
required boolean `truncated`, and optional boolean `hasEarlier` (absent means
false). Neither the helper nor recipe calculates byte spans from decoded text.
The recipe makes one read without retrying or merging pages. Its CLI prints
only byte bookkeeping and paging flags, never log text or transport errors.
An empty output is valid; missing jobs and failed reads remain errors.


### Reading activity and its continuation branches

`activity.mjs` reads `evener/jobs/list` using the shared `ActivityList` controller,
including advertised continuations. Supply the exact session ref and thread ID:

```sh
EVENER_REF=local:... EVENER_THREAD_ID=... node node_modules/@evener/appwire-client/examples/activity.mjs
```

Alternatively, set `EVENER_ACTIVITY_PARAMS_FILE` to a JSON file containing
`{ "ref": "local:...", "threadId": "...", "maxPages": 10 }`. The local page
budget defaults to ten and accepts safe integers from one to one hundred.
It is not sent to the hub. Parameters are validated and captured before connecting.
Each request sends only `ref` and, when continuing, the exact advertised
`continuation`. Tokens stay scoped to that ref and are never decoded or modified.

Import `runActivity` from `examples/activity-logic.mjs` for the parsed `readback`,
`remainingBranches`, `pagesRead` and `outcome`. `pagesRead` counts attempted page
requests, including a failed request. A completed traversal reports `read`;
`incomplete` preserves the tree when the budget is exhausted, a cursor repeats,
or a truncated branch has no continuation. A branch or request error reports
`failed` with any previously read tree. Root action unavailability and a missing
thread report `unavailable` and `ended`. The CLI prints only the outcome and
counts; callers of the helper receive potentially private tree and error data.
The helper disposes its controller; the caller owns closing the connection.

`ActivityList` is also exported for a live reader. Construct one per hub/session
lifetime with an already connected client, the exact ref and thread ID. Use
`start()` to subscribe to matching activity invalidations and perform the initial
read, `subscribe()`/`getSnapshot()` to observe state, `branches()`/`loadMore()` to
consume an advertised branch, and `refresh()` for a new root read. Refresh
replaces the paged root; continuation reads graft into the retained tree and
preserve other pending branches. Responses from another session or an older
revision leave the displayed tree intact and publish an error. Dispose on
navigation or client replacement; the controller does not connect or reconnect.
Treat snapshots as read-only. An optional retained tree must belong to the same
hub, ref and thread ID; the constructor checks the latter two, while the caller
must enforce hub ownership. The bounded recipe uses explicit reads and does not
subscribe to notifications or promise an atomic snapshot during concurrent updates.

`parseActivityTree`, `activityNodeID`, the activity types, and the continuation
merge helpers are shared with the web/native consumers. Invalid roots fail
parsing; invalid members are omitted and their branch is marked incomplete.
Numeric revisions and activity counters must be safely representable integers.

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
| `steer` | `input: [{ "type": "text", "text": "..." }]` | Send text to the current turn when steering is available. Waiting queue entries remain queued. |
| `cancel` | `index`, `expectedEntryId` | Remove the reviewed entry. |
| `promote` | `index`, `expectedEntryId` | Use that entry as steering, or resume a held queue. |
| `drain` | `expectedQueueRevision`, optional text `input` | Combine the complete reviewed queue and any appended composer input atomically. |

Mutations additionally require `EVENER_QUEUE_MUTATION=1` and a separately
authored `EVENER_QUEUE_OWNED_HUB` exactly matching `EVENER_RPC_URL`. The recipe
checks capabilities, the session instance, and the selected entry or complete
queue revision before dispatch. The server receives the instance and entry/
revision preconditions too. Resuming a held queue also releases remaining
waiting messages; review the full queue before promoting an entry.

Direct steering has no expected-active-turn field: the server applies it to
the active turn when it handles the request. Its receipt identifies that turn
and contains no queue entry IDs. A drain receipt identifies only the existing
queued entries; appended composer text does not acquire a synthetic queue ID.
An empty drain input array is allowed when the reviewed queue is nonempty.

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

### Session lineage and resume

`session-lineage.mjs` defaults to read-only transcript-target discovery. Set
`EVENER_REF` or provide `{ "ref": "local:owned-session" }` in
`EVENER_SESSION_LINEAGE_PARAMS_FILE`. Select
`EVENER_SESSION_LINEAGE_ACTION=transcripts|preview|review|resume|fork`.
`preview` accepts an optional integer `limit`; the hub defaults nonpositive
values to three and caps it at five. Empty previews contain `items: []`;
populated previews contain items with empty IDs, so these are
display previews, not stable identities for reader restoration.

Use `review` with `EVENER_SESSION_LINEAGE_OUTPUT_FILE` set to a new absolute
private file. It reads metadata without subscribing and returns a `review`
object containing `threadId` and any present `instanceId` and `name`.
Source-backed and ended threads may omit the instance and capabilities.
For an authored action, put that object into a parameter file as `reviewed`,
alongside `ref`, then set `EVENER_SESSION_LINEAGE_MUTATION=1` and
`EVENER_SESSION_LINEAGE_OWNED_HUB` equal to `EVENER_RPC_URL`.
An optional `expectedInstanceId` must equal the reviewed instance; it is a
client check and is not sent as an invented server precondition.

`resume` sends only the reviewed `ref`, starts or recovers that session, and
reads it again. The underlying API also supports a `sessionId` parameter;
this reviewed recipe uses `ref` to retain source identity throughout.
An ordinary local `fork` requires `sourceTurnId` and either `deferInput: true`
or nonblank `editedInput`. Despite its name, `sourceTurnId` is the positive
**transcript entry index**, optionally prefixed with `turn_`, taken from an
item's `transcriptEntryIndex`; do not pass a live turn ID. Turn-based forks
require the current `forkFromTurn` capability. Optional `label`, `modelProvider`
and `model` are forwarded; their effect remains source-defined. The local
handler creates the child from inherited configuration.

For a local tip-copy side thread, use `aside: true` and omit divergence,
replacement-input and label fields. Nonlocal sources can support whole-thread
forks with those fields omitted; the source decides availability. Preflight
review cannot atomically prevent concurrent changes for fork or resume.

A valid fork response must identify a different child. The recipe reads that
child and preserves `originalInput` for deliberate editing/submission. If the
reply is lost or malformed, it reads only the known parent, reports `uncertain`
and does not infer a child or repeat the fork. Acknowledged forks retain their
response even when child readback fails. Use the private output file to retain
the response and original input; stdout contains only outcome/count metadata.
Exit 2 means uncertainty or unavailable readback, including an acknowledged
fork whose child read failed. Exit 1 covers validation and other read failures;
the shared acknowledged-readback diagnostic below applies to resume.

### Connection and maintenance checks

`maintenance-checks.mjs` defaults to `ping` with no parameters. Select
`EVENER_MAINTENANCE_ACTION=auth/test` with a parameters file containing
`{ "provider": "owned-instance" }`, or `plugin/checkNow` with no parameters.
The file variable is `EVENER_MAINTENANCE_PARAMS_FILE`. Both selected checks
require `EVENER_MAINTENANCE_MUTATION=1` and
`EVENER_MAINTENANCE_OWNED_HUB` equal to `EVENER_RPC_URL`: credential testing can
probe the provider using effective credentials, and a plugin check can perform
configured plugin maintenance. Neither runs by default or retries on failure.

Set `EVENER_MAINTENANCE_OUTPUT_FILE` to a new absolute private file for full
results. Output is reserved before connecting and created with mode 0600;
existing files are refused. Missing readback produces a valid JSON outcome
record instead of a fabricated response. Stdout contains only outcome and
count metadata. Credential status is preserved in private readback; a
successful RPC does not imply successful authentication. Exit 2 means an
uncertain check and exit 1 means invalid input, unavailable ping or an output
failure. These contracts are exercised with deterministic SDK-boundary fakes;
they do not qualify live credentials or plugin downloads.

### Hub setup and pairing links

`hub-setup.mjs` requires `EVENER_HUB_SETUP_ACTION=dirs` or `pairing`,
`EVENER_HUB_SETUP_PARAMS_FILE`, and a new absolute `EVENER_HUB_SETUP_OUTPUT_FILE`.
The private output is reserved with mode 0600 before connecting. Directory
parameters are `{ "path": "/owned/workspace/new-directory" }`; the hub resolves
and validates the path, creates missing parents, and returns its canonical path
and whether it created the directory. This action requires
`EVENER_HUB_SETUP_MUTATION=1` and `EVENER_HUB_SETUP_OWNED_HUB` equal to
`EVENER_RPC_URL`.

Pairing parameters are `{ "origin": "https://your-hub.example" }`. The hub
validates its reachable origin and may use its configured mobile base URL.
This is the hub-side handoff generator, not an iPhone scanner: the returned
`/auth/<token>` link carries the hub credential. The recipe writes it only to
the requested private output and never opens it. Treat that file as a
credential. Stdout contains outcome metadata only. Neither action retries;
an absent or malformed reply produces an uncertain result and exit 2. Exit 1
covers input, connection and output failures. A directory acknowledgment is not
an independent filesystem check, and a generated pairing link is not proof
that a phone can reach the hub.

### Saved projects and sessions

`saved-items.mjs` requires an explicit `EVENER_SAVED_ITEMS_ACTION` of `favorite`,
`archive`, `projectDelete`, or `sessionDelete`. Supply
`EVENER_SAVED_ITEMS_PARAMS_FILE`, a new absolute `EVENER_SAVED_ITEMS_OUTPUT_FILE`,
`EVENER_SAVED_ITEMS_MUTATION=1`, and `EVENER_SAVED_ITEMS_OWNED_HUB` equal to
`EVENER_RPC_URL`. These are deliberate changes; no action runs by default.

Parameters follow the generated method contract and add a client-only
`reviewed` object identifying the exact target:

| Action | Wire fields | `reviewed` identity |
| --- | --- | --- |
| `favorite` | `kind: "project"`, `id`, `favorited` boolean | `kind`, `id` |
| `archive` project | `kind: "project"`, `id`, `workingDir`, `archived` boolean | `kind`, `id`, `workingDir` |
| `archive` session | `kind: "session"`, `id`, `archived` boolean | `kind`, `id` |
| `projectDelete` | `key`, `workingDir` | `key`, `workingDir` |
| `sessionDelete` | `ref` with local source | `ref` |

Deletion additionally requires `confirmTarget` equal to that same identity.
For example, a session deletion uses
`{ "ref": "local:owned-session-id", "reviewed": { "ref": "local:owned-session-id" }, "confirmTarget": { "ref": "local:owned-session-id" } }`.
Use the actual canonical IDs and paths from navigation or thread readback.
The hub validates project/path agreement and live-session deletion rules.
`reviewed` and `confirmTarget` are operator checkpoints; they are not sent to
the server and cannot atomically prevent concurrent changes.

The recipe preserves the navigation receipt and exact `deleted`/`skipped`
results in private JSON. An acknowledged deletion can still have skipped
targets; inspect those results before removing anything from your UI. There is
no automatic follow-up navigation refresh in this bounded recipe. Stdout
contains outcome/count metadata, and `execution` remains `unverified`. A lost
or malformed reply is uncertain, exits 2 and is never replayed. Exit 1 covers
invalid input, connection or output failure; inspect the current hub state
before deliberately issuing another action.

### Management mutation recovery

These management and approval recipes require an explicit mutation opt-in and a separately
supplied owned endpoint equal to `EVENER_RPC_URL`. Their logic helpers expect the
client returned by `connection.mjs` for that endpoint. Ownership confirmation is
an operator guard, not authentication or a check of an arbitrary client's URL.
Preflight reads are not atomic with writes; use fixtures without concurrent
writers. These methods have no mutation-ID or revision precondition in their
current contracts.

Management recipes with a separate readback step perform one fresh list or
thread read after one mutation attempt. A
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
