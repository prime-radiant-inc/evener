# Codex App-Server Protocol and Serf Extensions

Date: 2026-05-12
Status: Evidence capture for #70

## Summary

Codex app-server uses a JSON-RPC-like message envelope over multiple transports. The protocol is not websocket-only, and it is not strict JSON-RPC 2.0 because Codex intentionally omits the `"jsonrpc": "2.0"` field from wire messages.

The current Serf AppWire implementation follows the same broad `thread`, `turn`, `item`, and notification shape, but it is already a Serf protocol with explicit extensions: source refs, harness selection, Serf capabilities, tasks, directory completion, Serf-owned diagnostics, Serf provider/model refs, and Serf-specific notifications. Hub adoption should preserve those fields instead of reducing everything to a Codex-only projection.

## Evidence Snapshot

Codex source was read from the local inspo checkout at commit `07b695190f30a450e4921f71f77473e564395c59`. The linked worktree used for this branch does not contain an `inspo/` directory, so the source was inspected at `/Users/jesse/Documents/GitHub/prime-radiant-inc/serf/inspo/codex/codex-rs`. Source paths below are repo-relative to the Serf checkout that contains `inspo/`.

Primary Codex evidence:

- [`inspo/codex/codex-rs/app-server/README.md`](../../../inspo/codex/codex-rs/app-server/README.md)
- [`inspo/codex/codex-rs/app-server/src/main.rs`](../../../inspo/codex/codex-rs/app-server/src/main.rs)
- [`inspo/codex/codex-rs/app-server/src/lib.rs`](../../../inspo/codex/codex-rs/app-server/src/lib.rs)
- [`inspo/codex/codex-rs/app-server/src/message_processor.rs`](../../../inspo/codex/codex-rs/app-server/src/message_processor.rs)
- [`inspo/codex/codex-rs/app-server/src/request_processors/thread_processor.rs`](../../../inspo/codex/codex-rs/app-server/src/request_processors/thread_processor.rs)
- [`inspo/codex/codex-rs/app-server/src/request_processors/turn_processor.rs`](../../../inspo/codex/codex-rs/app-server/src/request_processors/turn_processor.rs)
- [`inspo/codex/codex-rs/app-server/src/request_processors/thread_lifecycle.rs`](../../../inspo/codex/codex-rs/app-server/src/request_processors/thread_lifecycle.rs)
- [`inspo/codex/codex-rs/app-server/src/bespoke_event_handling.rs`](../../../inspo/codex/codex-rs/app-server/src/bespoke_event_handling.rs)
- [`inspo/codex/codex-rs/app-server/src/error_code.rs`](../../../inspo/codex/codex-rs/app-server/src/error_code.rs)
- [`inspo/codex/codex-rs/app-server-transport/src/transport/mod.rs`](../../../inspo/codex/codex-rs/app-server-transport/src/transport/mod.rs)
- [`inspo/codex/codex-rs/app-server-transport/src/transport/stdio.rs`](../../../inspo/codex/codex-rs/app-server-transport/src/transport/stdio.rs)
- [`inspo/codex/codex-rs/app-server-transport/src/transport/websocket.rs`](../../../inspo/codex/codex-rs/app-server-transport/src/transport/websocket.rs)
- [`inspo/codex/codex-rs/app-server-transport/src/transport/unix_socket.rs`](../../../inspo/codex/codex-rs/app-server-transport/src/transport/unix_socket.rs)
- [`inspo/codex/codex-rs/app-server-transport/src/transport/auth.rs`](../../../inspo/codex/codex-rs/app-server-transport/src/transport/auth.rs)
- [`inspo/codex/codex-rs/app-server-protocol/src/jsonrpc_lite.rs`](../../../inspo/codex/codex-rs/app-server-protocol/src/jsonrpc_lite.rs)
- [`inspo/codex/codex-rs/app-server-protocol/src/protocol/common.rs`](../../../inspo/codex/codex-rs/app-server-protocol/src/protocol/common.rs)
- [`inspo/codex/codex-rs/app-server-protocol/src/protocol/v2/thread_data.rs`](../../../inspo/codex/codex-rs/app-server-protocol/src/protocol/v2/thread_data.rs)
- [`inspo/codex/codex-rs/app-server-protocol/src/protocol/v2/thread.rs`](../../../inspo/codex/codex-rs/app-server-protocol/src/protocol/v2/thread.rs)
- [`inspo/codex/codex-rs/app-server-protocol/src/protocol/v2/turn.rs`](../../../inspo/codex/codex-rs/app-server-protocol/src/protocol/v2/turn.rs)
- [`inspo/codex/codex-rs/app-server-protocol/src/protocol/v2/item.rs`](../../../inspo/codex/codex-rs/app-server-protocol/src/protocol/v2/item.rs)
- [`inspo/codex/codex-rs/app-server-protocol/src/protocol/event_mapping.rs`](../../../inspo/codex/codex-rs/app-server-protocol/src/protocol/event_mapping.rs)

Primary Serf evidence:

- [`internal/appwire/types.go`](../../../internal/appwire/types.go)
- [`internal/appwire/jsonrpc.go`](../../../internal/appwire/jsonrpc.go)
- [`internal/appwire/errors.go`](../../../internal/appwire/errors.go)
- [`internal/appwire/ws_transport.go`](../../../internal/appwire/ws_transport.go)
- [`internal/appsource/source.go`](../../../internal/appsource/source.go)
- [`internal/appsource/codex_source.go`](../../../internal/appsource/codex_source.go)
- [`cmd/serf-hub/app_rpc.go`](../../../cmd/serf-hub/app_rpc.go)
- [`cmd/serf-hub/config.go`](../../../cmd/serf-hub/config.go)
- [`cmd/serf-hub/web.go`](../../../cmd/serf-hub/web.go)
- [`cmd/serf-hub/assets/appwire.js`](../../../cmd/serf-hub/assets/appwire.js)
- [`cmd/serf-tui/hub_model.go`](../../../cmd/serf-tui/hub_model.go)

## Claim Types

- Verified Codex: read from the current Codex source tree or its app-server README.
- Current Serf: read from current Serf implementation.
- Serf extension: deliberate Serf behavior or field that Codex does not expose natively.
- Open question: not proven by source yet, or requires a follow-up implementation decision.

## Transport and Connection Lifecycle

Verified Codex:

- Codex app-server transports are selected by `codex app-server --listen URL`.
- Supported listener values are `stdio://` (default), `ws://IP:PORT`, `unix://`, `unix://PATH`, and `off`.
- `stdio://` carries newline-delimited JSON messages on stdin/stdout.
- `ws://IP:PORT` carries one protocol message per websocket text frame. Codex marks this listener experimental and unsupported.
- `unix://` and `unix://PATH` accept websocket upgrade connections over a Unix socket. The default socket path is under `$CODEX_HOME/app-server-control/app-server-control.sock`.
- The websocket listener also serves `GET /readyz` and `GET /healthz`; both return `200 OK` when the listener is accepting, but requests with an `Origin` header are rejected with `403 Forbidden`.
- Websocket authentication is optional. When configured, Codex supports capability-token auth from an absolute token file or SHA-256 hash, and signed bearer tokens from an absolute shared-secret file. Clients present `Authorization: Bearer <token>` during the websocket handshake, before `initialize`.
- Transport ingress uses bounded queues. When request ingress is saturated, Codex rejects new requests with code `-32001` and message `Server overloaded; retry later.`
- Each connection must send one `initialize` request before other requests, then send an `initialized` notification. Requests before initialization return `Not initialized`; repeated initialization returns `Already initialized`.
- Notifications are only sent to initialized connections. Per-connection `initialize.params.capabilities.optOutNotificationMethods` suppresses exact notification method names; wildcards and prefixes are not used.
- Thread event delivery is subscription-scoped. `thread/start`, `thread/resume`, and `thread/fork` attach the connection to the target thread. `thread/unsubscribe` detaches it.
- Codex keeps an idle loaded thread around after the last subscriber leaves and unloads it after 30 minutes with no subscribers and no activity, then emits `thread/closed`.
- Closing a connection stops queued handlers for that connection, while already-started handlers are allowed to finish.

Current Serf:

- Serf's generic websocket transport is JSON text frames through `internal/appwire/ws_transport.go`.
- The current Hub Codex source adapter supports existing Codex instances only through configured websocket endpoints, optional inline bearer token, or optional bearer token file.
- Current Hub config accepts `[[codex_sources]]` entries with `id`, `endpoint`, `bearer_token`, and `bearer_token_file`.

Open questions:

- #57 should decide whether "existing Codex app-server instances" means websocket only for now, or whether Hub must also support `stdio://` and `unix://` Codex listeners.
- #58 should decide which launched Codex listener Hub owns first. Websocket gives Hub `/readyz`; stdio and Unix socket require different readiness checks.

## RPC Envelope and Errors

Verified Codex:

- The message envelope is JSON-RPC-like, not strict JSON-RPC 2.0. `jsonrpc_lite.rs` states that Codex neither sends nor expects a `"jsonrpc": "2.0"` field.
- A client request has `id`, `method`, optional `params`, and optional W3C `trace`.
- A notification has `method` and optional `params`.
- A response has `id` and `result`.
- An error response has `id` and `error`, where `error` contains `code`, `message`, and optional `data`.
- Request IDs may be strings or integers.
- Client-origin notifications are accepted by the transport but the current server-side processor only logs them, except for protocol-specific handling around initialization state.
- The server can send requests to the client for approvals, tool input, MCP elicitation, permissions, auth token refresh, and dynamic tool calls. Clients must respond with matching IDs.
- Codex request handlers are not globally serialized. Requests declare serialization scopes in `protocol/common.rs`; some scopes are global, some are shared reads, and many are thread- or process-scoped.
- Codex has no generic JSON-RPC cancellation method in the inspected protocol. `turn/interrupt` is the turn-level cancellation action.
- Standard JSON-RPC-style error codes include `-32600`, `-32601`, `-32602`, and `-32603`; Codex also uses `-32001` for overload.

Current Serf:

- Serf AppWire also omits `"jsonrpc"`, and `internal/appwire/jsonrpc.go` rejects any incoming message containing a `jsonrpc` field.
- Serf AppWire has Serf-specific structured error data: `data.serfErrorInfo`.
- Serf currently uses code `-32014` plus `serfErrorInfo: "actionUnavailable"` for unsupported actions.

Open questions:

- #68 should make action-unavailable diagnostics explicit at the Hub boundary instead of relying on method-specific fallback messages.
- If Hub forwards Codex error `data` to Web/TUI, it should preserve Codex details rather than replacing them with only Serf error data.

## Initialization and Capability Negotiation

Verified Codex:

- Codex `initialize` returns `userAgent`, `codexHome`, `platformFamily`, and `platformOs`.
- `initialize.params.capabilities.experimentalApi` opts the connection into experimental methods and fields.
- Experimental method and field gates are enforced at request dispatch. Without opt-in, Codex returns an invalid-request error explaining that the experimental API is required.
- `initialize.params.capabilities.optOutNotificationMethods` is an exact list of notifications to suppress.

Current Serf:

- Serf `initialize` response returns `serverInfo`, `protocolVersion`, `sourceId`, and a `features` object.
- Serf's current `Capabilities` struct has `experimentalApi` and `optOutNotificationMethods`, which overlaps enough for Hub's Codex adapter to initialize Codex with `experimentalApi: true`.

Serf extension:

- `protocolVersion: "serf-appwire-v1"`, `sourceId`, and `features` are Serf-owned protocol metadata, not Codex fields.
- Serf feature booleans currently cover thread list, turn list, turn start, steer, clear, shutdown, fork from turn, tasks, model list, and directory completion.

## Thread and Session Lifecycle

Verified Codex:

- Codex's core user-visible objects are Thread, Turn, and ThreadItem.
- `thread/start` creates a new thread, returns a `thread` object, emits `thread/started`, and subscribes the connection to thread events.
- `thread/resume` reopens an existing thread by `threadId` and subscribes the connection.
- `thread/fork` creates a new thread by copying stored history. If the source thread is mid-turn, Codex snapshots it as interrupted rather than inheriting an unmarked partial suffix.
- `thread/list` pages stored threads.
- `thread/read` reads a stored thread without resuming it. `includeTurns` asks Codex to populate `thread.turns`.
- `thread/turns/list` pages turns and can request summary/full/not-loaded item views.
- `thread/turns/items/list` exists in the protocol as an experimental shape, but the app-server currently returns a method-not-supported JSON-RPC error.
- Thread status is a tagged enum: `notLoaded`, `idle`, `systemError`, or `active` with `activeFlags`.
- Codex `Thread` includes `id`, `sessionId`, `forkedFromId`, `preview`, `ephemeral`, `modelProvider`, timestamps, status, path, cwd, CLI version, source, threadSource, optional agent metadata, optional name, and turns.
- `thread/start`, `thread/resume`, and `thread/fork` responses include turns only where the method is documented to include them; otherwise `turns` can be empty even for valid threads.

Current Serf:

- Serf Hub normalizes source identity into `ref` strings with `sourceID:threadID` in `Thread.Serf.Ref`.
- Serf thread status strings are `idle`, `processing`, `closed`, `ended`, and `error`.
- Current Codex adapter maps Codex `active` to Serf `processing`, `idle` to `idle`, `systemError` to `error`, and `notLoaded` to `ended`.
- Current Codex adapter retries `thread/read includeTurns=true` without turns when Codex reports that `includeTurns` is unavailable before the first user message.

Serf extension:

- `Thread.Serf` is a Serf-owned object with `ref`, optional `profile`, optional `contextPressure`, and per-thread action capabilities.
- `ThreadStartParams.harness` selects Serf vs configured Codex sources at the Hub launch boundary. Codex does not have a `harness` field.
- `serf/harnesses/list` returns Hub launch choices with `id`, `label`, and `kind`.

Open questions:

- Codex native `thread/fork` does not expose the Serf `sourceTurnId`, `editedInput`, or `label` semantics found in `ThreadForkParams`. The current Codex adapter drops those fields. #57/#66 should not mark fork-from-turn parity proven until this is resolved.
- Current Codex adapter sets `ForkFromTurn: true` for Codex threads, but the verified Codex protocol only proves whole-thread fork. This should be corrected or made capability-specific in #68.

## Turn Lifecycle and Actions

Verified Codex:

- `turn/start` accepts `threadId`, typed `input`, and optional per-turn overrides such as cwd, approval policy, sandbox policy, model, service tier, reasoning effort, summary, personality, output schema, experimental permissions, environments, and collaboration mode.
- `turn/start` returns an initial `turn` with `status: "inProgress"` and emits `turn/started` when the turn actually begins.
- Codex input variants are `text`, `image`, `localImage`, `skill`, and `mention`.
- `turn/steer` requires `threadId`, typed input, and `expectedTurnId`. It only applies to active regular turns; review and compaction turns reject steering.
- `turn/interrupt` requires `threadId` and `turnId`, returns `{}`, and the interrupted turn completes with status `interrupted`.
- Codex turn statuses are `completed`, `interrupted`, `failed`, and `inProgress`.

Current Serf:

- Serf `TurnStartParams` uses `ref`, `prompt`, and `items`; the Codex adapter converts those into Codex typed input. Serf `InputItem.url` preserves Codex-native image URLs/data URLs, and `InputItem.path` preserves Codex-native `localImage`, `skill`, and `mention` paths through the Hub.
- Serf `TurnSteerParams` uses `ref`, `turnId`, and text; the Codex adapter maps `turnId` to Codex `expectedTurnId`.
- Serf `TurnInterruptParams` allows an empty `turnId` in the type, but the Codex adapter requires `turnId` because Codex requires it.
- Current Codex turn statuses are normalized so `inProgress` becomes Serf `running`, `interrupted` becomes Serf `canceled`, and blank status becomes Serf `completed`.

Serf extension:

- Serf's prompt/items shape is the Hub/client contract. Codex's typed input is the adapter target, not the Serf client contract.
- Serf provider/model refs are separate from harness/source selection.

## Item Model

Verified Codex:

- Codex `ThreadItem` is tagged by `type`.
- Codex item variants include user messages, hook prompts, agent messages, plans, reasoning, command executions, file changes, MCP tool calls, dynamic tool calls, collaborative agent tool calls, web search, image view, image generation, review mode markers, and context compaction.
- Items carry stable IDs. Streaming deltas and lifecycle notifications identify the associated `threadId`, `turnId`, and `itemId`.
- Tool-like work is represented as item lifecycle, not as detached log lines. Command execution, MCP tool call, dynamic tool call, and collaborative agent tool call have item records and final statuses/results/errors.
- `item/started` and `item/completed` carry full item objects for the start/final states.
- Assistant text deltas are `item/agentMessage/delta`.
- Command output deltas are `item/commandExecution/outputDelta`.
- Deprecated file-change output deltas are no longer emitted by current Codex; file changes use patch update notifications.

Current Serf:

- Serf `ThreadItem` is a smaller normalized shape with `type`, `id`, optional `turnId`, text, delta, images, tool name, call ID, arguments JSON, output, error, status, timestamps, and raw JSON.
- The Codex adapter maps Codex user messages to `user_message`, agent messages to `agent_message`, and command/MCP/dynamic/unknown items to `tool_call`.
- The Codex adapter preserves each raw Codex item in `ThreadItem.Raw`.
- The Web and TUI currently consume Serf-normalized `item/started`, `item/completed`, `item/agentMessage/delta`, and `item/toolOutput/delta`.

Open questions:

- The current Codex adapter listens for `commandExecution/outputDelta`, but the verified Codex notification method is `item/commandExecution/outputDelta`. #65 should add a red test and fix this mapping before claiming live provider/tool deltas work through Hub.
- #66 should group tool call starts, streaming output, and completed results by `itemId` or `callId`; the Codex source proves these are one logical transcript unit.

## Streaming, Replay, and Reconnect

Verified Codex:

- After a connection starts, resumes, or forks a thread, it receives subscribed thread lifecycle, turn lifecycle, and item notifications.
- Turn streaming follows the item lifecycle: `item/started`, zero or more item-specific deltas, `item/completed`, then `turn/completed`.
- Token usage streams separately through `thread/tokenUsage/updated`.
- Replaying final transcript state is done through `thread/read includeTurns` or `thread/turns/list`, not by replaying the live notification stream.
- `thread/turns/items/list` cannot be relied on yet because the app-server returns unsupported-method for it.
- `thread/realtime/*` notifications are explicitly ephemeral transport events and are not returned by read/resume/fork history.

Current Serf:

- Hub fans source notifications out to app-server clients after starting a relay with `SubscribeThread`.
- Web converts AppWire notifications to its SSE event vocabulary.
- TUI applies AppWire notifications directly into its hub session model.
- `cmd/serf-hub/assets/appwire.js` also contains a browser-side AppWire notification reducer.

Open questions:

- #65 must prove that live deltas update browser and TUI before refresh, and that `thread/read`/turn replay reconstructs the final transcript after refresh.
- #65 should cover both assistant text deltas and tool output deltas because they use different notification methods.

## Actions and Capabilities

Verified Codex:

- Codex has protocol actions for starting turns, steering active regular turns, interrupting turns, compacting a thread, archiving/unarchiving, setting thread names, rollback, shell commands, realtime start/stop, reviews, process management, and config/account/model operations.
- Codex does not expose a single per-thread action capability object equivalent to Serf `ThreadCapabilities`.
- `modelProvider/capabilities/read` is model-provider capability data, not a thread action capability contract.
- Unsupported or unavailable Codex methods are returned as JSON-RPC errors, such as `-32601` for unsupported `thread/turns/items/list`.

Current Serf:

- Serf per-thread capabilities are `send`, `steer`, `interrupt`, `compact`, `clear`, `forkFromTurn`, `shutdown`, and `changeModel`.
- Current Codex adapter returns action-unavailable for Serf-only actions: `thread/shutdown`, `thread/model/set`, `thread/clear`, and `serf/tasks/list`.
- Current Codex adapter sets only `send`, `compact`, and `forkFromTurn` on mapped Codex threads.

Serf extension:

- `Thread.Serf.Capabilities` is the client-visible action gate for Web and TUI.
- `serfErrorInfo: "actionUnavailable"` is the structured diagnostic clients should understand for disabled source actions.

Open questions:

- #68 should define how Hub derives capabilities for Codex threads across loaded/active/idle states. In particular, `steer` and `interrupt` should probably depend on active turn state and turn identity, not source-wide static booleans.
- #68 should make Web and TUI hide or disable unsupported Codex actions and show structured action-unavailable diagnostics when a request races capability state.

## Model and Account Behavior

Verified Codex:

- Codex owns its account/config surface. The inspected app-server protocol includes account, auth, config, model, and model-provider methods.
- `model/list` returns Codex models.
- `modelProvider/capabilities/read` reads provider-level model capabilities.
- `initialize` returns Codex runtime metadata including `codexHome`.
- Codex launch has a `--session-source` argument defaulting to `vscode`, used for product restrictions and metadata.

Current Serf:

- Serf provider/model refs are represented as `ModelDescriptor{Provider, Model}` and thread start fields `modelProvider` plus `model`.
- Current Codex adapter maps Codex models into Serf model descriptors with `Provider` set to the Codex source ID.
- Recent Hub work routes harness/source selection separately from `model`; the protocol doc should preserve that boundary.

Serf extension:

- Serf-owned auth and provider config remain separate from Codex credentials. Codex account state must not be treated as Serf provider state.
- Harness/source selection is a Hub concern. `model` must remain a model choice, not a hidden Codex-vs-Serf selector.

Open questions:

- #64 should make TUI spawn model selection harness-aware, matching Web behavior, without overloading `model`.
- #57 should verify whether current `model/list` mapping preserves enough Codex provider metadata for Web/TUI source labels and future model capability display.

## Diagnostics

Verified Codex:

- Protocol errors are JSON-RPC error responses.
- Codex standard error codes include parse, invalid request, method not found, invalid params, internal error, and overload.
- Turn failures are also represented in final `Turn.error` with Codex error info and additional details.
- App-server logs are controlled by `RUST_LOG` and `LOG_FORMAT=json`.

Current Serf:

- Serf AppWire defines structured `serfErrorInfo` values: invalid params, method not found, provider unavailable, session unavailable, conflict, action unavailable, and internal.
- Hub Web maps AppWire unavailable errors to HTTP `503` in spawn paths.

Serf extension:

- Hub diagnostics must distinguish protocol errors, Codex account/config/capability failures, Hub source connection failures, Hub launch failures, Serf provider/config failures, and Serf runtime failures.

Open questions:

- #53/#68 should define the final diagnostic payload shape used by Hub, Web, and TUI.
- #58 should include structured launch diagnostics for binary missing, cwd invalid, auth token file invalid, readiness timeout, and early child exit.

## Process Launch Boundary

Verified Codex:

- Launching Codex app-server is outside the app-server wire protocol.
- Codex process options include `--listen`, `--session-source`, websocket auth flags, and debug-only test hooks.
- Websocket readiness can be probed through `/readyz`. The inspected README only documents `/readyz` and `/healthz` for the websocket listener.
- The app-server protocol creates/resumes/forks threads inside a running process; it does not supervise the process that hosts the protocol.

Current Serf:

- Hub currently registers configured existing Codex sources from `[[codex_sources]]`.
- Hub does not yet own Codex process lifecycle.

Serf extension:

- #58 belongs to Hub supervision: binary path, cwd, env, args, auth, timeout, ready probe, early-exit capture, source registration, and launch diagnostics.
- Launched Codex instances should register into the same source roster as configured Codex instances.

## Serf Extension Catalog

Current explicit Serf extensions:

- Protocol metadata: `protocolVersion`, `serverInfo`, `sourceId`, and `features`.
- Source identity: `Thread.Serf.Ref` as `sourceID:threadID`, and `Thread.Source` for the source label/id.
- Action gating: `Thread.Serf.Capabilities`.
- Harnesses: `serf/harnesses/list`, `ThreadStartParams.harness`, and harness descriptors with `id`, `label`, `kind`.
- Serf thread fields: `profile`, `contextPressure`, and capabilities.
- Serf lifecycle actions: `thread/clear`, `thread/shutdown`, `thread/model/set`.
- Serf support APIs: `serf/tasks/list` and `serf/dirs/complete`.
- Serf notifications: `item/toolOutput/delta`, `serf/thread/contextPressure/updated`, `serf/task/updated`, `serf/steering/injected`, `serf/subagent/started`, and `serf/subagent/completed`.
- Serf diagnostics: `data.serfErrorInfo`, including `actionUnavailable`.
- Serf fork/edit metadata: `sourceTurnId`, `editedInput`, and `label`.
- Serf provider/model refs: separate `modelProvider` and `model` fields, plus model descriptors with provider/model.

Rules for adoption:

- Keep Serf extensions explicit in structs and docs.
- Preserve raw Codex item data where Serf's normalized item shape is smaller than Codex's item model.
- Do not claim Codex-native support for a Serf extension unless Codex source proves it.
- Do not hide harness/source identity inside `model`.

## Compatibility Matrix

| Surface | Codex native | Serf native | Hub normalized today | Gap or open question |
| --- | --- | --- | --- | --- |
| Envelope | JSON-RPC-like without `jsonrpc` | Same broad shape, rejects `jsonrpc` | Shared AppWire message structs | Preserve Codex `error.data` when bridging |
| Transports | stdio, websocket, Unix socket, off | Websocket transport exists | Codex source adapter uses websocket only | #57/#58 decide stdio/Unix support |
| Initialize response | `userAgent`, `codexHome`, platform fields | `serverInfo`, `protocolVersion`, `sourceId`, `features` | Adapter ignores missing Serf init fields for Codex | Keep response differences documented |
| Thread identity | `threadId` | `ref` plus source ID | `sourceID:threadID` refs | Web/TUI must surface source labels |
| Thread list/read | `thread/list`, `thread/read`, `thread/turns/list` | Same names plus Serf fields | Adapter maps Codex threads to Serf threads | `thread/turns/items/list` unsupported in Codex |
| Thread start | `thread/start` inside running process | `thread/start` with harness/prompt/items | Hub routes harness to source | Launching a Codex process is #58, not `thread/start` |
| Fork | Whole-thread `thread/fork` | fork from turn/edit metadata | Adapter drops Serf fork metadata | #57/#66 resolve fork-from-turn claims |
| Turn start | typed `input` | `prompt` plus `items` | Adapter converts to Codex input | Cover all item types used by clients |
| Steer | `turn/steer` with `expectedTurnId` | `turnId` plus text | Adapter maps fields | #68 gate on active turn state |
| Interrupt | `turn/interrupt` requires turn ID | type allows empty turn ID | Adapter requires turn ID | UI must not offer without turn ID |
| Compact | `thread/compact/start` | same method | Adapter calls Codex | Capability should reflect source/thread state |
| Clear/shutdown/change model | Not Codex thread actions in current adapter | Serf actions | Adapter returns action unavailable | #68 structured unavailable diagnostics |
| Models | `model/list`, provider capabilities | provider/model descriptors | Adapter uses source ID as provider | #57/#64 avoid lossy source/model mapping |
| Tasks | Not Codex | `serf/tasks/list` | Adapter unavailable | Web/TUI hide for Codex |
| Assistant deltas | `item/agentMessage/delta` | same method | Web/TUI consume it | #65 live and replay proof |
| Command output deltas | `item/commandExecution/outputDelta` | `item/toolOutput/delta` | Adapter currently checks wrong Codex method name | #65 red test and fix |
| Tool transcript grouping | item lifecycle by `itemId` | normalized `tool_call` items | Web/TUI partially reduce starts/deltas/completions | #66 fixture coverage |
| Diagnostics | JSON-RPC errors and turn errors | `serfErrorInfo` | Mixed current mapping | #53/#68/#58 structured diagnostics |
| Process launch | CLI/runtime concern | Hub supervision concern | Not implemented for Codex | #58 owns lifecycle and registration |

## Follow-Up Katas

- #57: Existing Codex source support should be validated against the verified Codex envelope and websocket handshake. Do not infer Unix/stdio support from Codex source unless Hub implements it.
- #58: Launch work is process supervision, not a protocol method. Implement binary/cwd/env/args/auth/timeout/ready/early-exit diagnostics and register launched instances as sources.
- #64: TUI model selection must remain harness-aware. Codex source/model selection is not a Serf provider/model picker.
- #65: Add a failing test for Codex `item/commandExecution/outputDelta` and prove browser/TUI live updates plus final replay.
- #66: Use item IDs/call IDs to render tool calls and results as one transcript unit in Web and TUI.
- #68: Define Hub capability derivation and structured action-unavailable behavior for Codex and Serf sources.
