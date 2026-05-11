# Serf Codex-Shaped Backend Design

Date: 2026-05-11
Status: Draft

## Summary

Serf will replace its hub and daemon backend contract with a Codex-shaped app-server protocol. The public backend interface becomes JSON-RPC over long-lived transports, organized around `thread`, `turn`, `item`, and `notification` concepts. Serf keeps its own runtime, provider-neutral model support, plugins, agents, subagents, tasks, and fork semantics, but exposes them through the same app-server shape that Codex uses.

This is a breaking design. There is no compatibility mode for the current REST/SSE daemon protocol, `/api/sessions/*` hub API, `internal/hubapi` client, or direct TUI daemon mode. The implementation removes those surfaces after the replacement protocol is in place.

## Current Context

The current Serf stack has three different API layers:

- `serf serve` exposes REST endpoints such as `/status`, `/input`, `/steer`, `/interrupt`, `/compact`, `/clear`, `/model`, `/models`, `/tasks`, `/shutdown`, and `/events`.
- `serf-hub` discovers live daemons through rendezvous files, probes `/status`, reverse-proxies daemon actions, reconstructs replay streams from transcript JSONL, and exposes hub JSON under `/api/*`.
- `serf-tui` talks to `internal/hubapi` for dashboard and actions, then follows hub-proxied SSE streams for session rendering.

Codex's app-server has a cleaner app boundary:

- One JSON-RPC protocol over stdio, Unix socket, or websocket.
- Thread lifecycle requests: `thread/start`, `thread/resume`, `thread/fork`, `thread/list`, `thread/read`, `thread/turns/list`, `thread/turns/items/list`.
- Turn requests: `turn/start`, `turn/steer`, `turn/interrupt`.
- Structured server notifications: `thread/started`, `thread/status/changed`, `turn/started`, `turn/completed`, `item/started`, `item/completed`, assistant deltas, reasoning deltas, command/tool output deltas, plan updates, warnings, and account/model updates.
- Connection-scoped subscriptions and server requests for approvals or other client elicitations.

The Codex shape is better for Serf's hub and TUI than the current REST/SSE shape because it makes identity, turn state, streaming item updates, replay, active-turn steering, and subscriptions part of the protocol rather than behaviors inferred by clients.

## Goals

- Replace Serf's backend API with a Codex-shaped JSON-RPC app protocol.
- Use the same protocol between web UI, TUI, hub, and live session runtimes.
- Keep `serf-hub` as the host-level control plane for discovery, spawn, local source aggregation, and later remote sources.
- Keep `serf serve` as the local runtime process boundary, but replace its REST/SSE control plane with the app protocol.
- Make replay and live streaming use the same typed event vocabulary.
- Represent Serf sessions as app-server threads and Serf inputs as turns.
- Support active steering with explicit turn identity.
- Support Serf-specific features through explicit protocol extensions, not hidden side channels.
- Remove old REST/SSE and `internal/hubapi` code once the replacement is wired.

## Non-Goals

- No compatibility mode for REST/SSE clients.
- No adapter that fabricates Codex threads from old hub REST responses.
- No adapter that makes Codex app-server look like fake `serf serve` daemons.
- No reuse of Codex account/login storage. Serf remains provider-neutral and keeps its own auth/config decisions.
- No remote-host federation in the first implementation. The protocol must be source-aware, but v1 only needs local source support.
- No state migration from old Serf transcript formats in this design. A state migration can be designed separately if needed.

## Brainstorming Outcome

Three approaches were considered.

### Approach A: Codex-as-fake-daemon

The hub could make Codex app-server look like a daemon with `/status`, `/input`, and `/events`.

This is rejected. It preserves Serf's weaker API shape and hides Codex's useful turn and item identity. It also forces lossy conversions for active-turn steering, item completion, approval requests, and history paging.

### Approach B: Backend interface under current hub REST API

The hub could keep `/api/tree`, `/api/sessions/{ref}`, and SSE while using a Codex-shaped internal `Backend`.

This is useful only as an intermediate implementation tactic. It is rejected as the final architecture because web and TUI would still depend on REST/SSE semantics that the design is trying to remove.

### Approach C: Serf app protocol modeled on Codex

The hub and daemon both speak a Serf app protocol that follows Codex's JSON-RPC thread/turn/item/wire shape. The hub exposes this protocol to clients and multiplexes local daemon sources underneath it. Serf adds a small extension namespace for features Codex does not model directly.

This is the chosen design.

## Architecture

```text
web UI / serf-tui
       |
       | JSON-RPC over websocket or Unix socket
       v
serf-hub app server
       |
       | JSON-RPC over local daemon websocket or Unix socket
       v
serf serve app runtimes
```

`serf-hub` remains the user-facing app server. It owns:

- Static web assets.
- Client JSON-RPC websocket endpoint.
- Source registry.
- Local daemon discovery through rendezvous.
- Spawning `serf serve` processes.
- Source-aware thread references.
- Thread list aggregation.
- Event subscription fanout.
- Remote-source ready routing, initially with only the local source.

`serf serve` becomes a per-runtime app-server source. It owns:

- One live Serf agent session.
- Thread state for that runtime.
- Turn execution.
- Serf event to app notification projection.
- Local app-wire endpoint bound to loopback or Unix socket.
- Runtime status and action capability reporting.

The web UI and TUI use the same client package. The browser uses websocket JSON-RPC. The TUI can use websocket at first and add Unix socket later if it improves local startup or security.

## Package Boundaries

### `internal/appwire`

Protocol definitions, JSON-RPC envelopes, typed requests, typed notifications, typed errors, refs, cursors, and client helpers. This package must not import `agent`, `server`, `cmd/serf-hub`, or UI packages.

### `internal/appserver`

Reusable request routing, connection lifecycle, subscriptions, request serialization scopes, and notification fanout. This package owns the server-side protocol plumbing but not Serf agent execution.

### `internal/appsource`

Interfaces for hub source drivers. The local daemon source and later remote sources implement these interfaces. This package bridges `internal/appserver` to concrete source clients.

### `cmd/serf-hub`

Process entrypoint, config, local source registry, spawn policy, static web server, and app-wire websocket endpoint. It should stop owning session behavior directly.

### `server`

Runtime-facing session server package. Its current REST/SSE server is replaced with app-wire handler construction and Serf agent event projection.

### `cmd/serf-tui`

Terminal UI. It should depend on `internal/appwire.Client`, not `internal/hubapi`, daemon addresses, or SSE parsing.

## Wire Protocol

Serf uses JSON-RPC 2.0 messages with Codex-style method names.

Request:

```json
{"jsonrpc":"2.0","id":1,"method":"thread/list","params":{"limit":50}}
```

Response:

```json
{"jsonrpc":"2.0","id":1,"result":{"data":[],"nextCursor":null}}
```

Notification:

```json
{"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"threadId":"th_123","turnId":"turn_456","itemId":"item_789","delta":"text"}}
```

Error:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"threadId is required","data":{"serfErrorInfo":"invalidParams"}}}
```

### Transport

Required in v1:

- `ws://127.0.0.1:<port>/rpc` for browser and TUI clients.
- `ws://127.0.0.1:<port>/source/<source-id>/rpc` is not public. Hub uses source drivers, not public per-source URLs.
- Daemon rendezvous files point the hub at a daemon app-wire endpoint.

Optional after v1:

- Unix domain socket for local TUI and hub-to-daemon connections.
- Stdio for tests and possible embedded integration.

Transport rules:

- Every connection must send `initialize` before non-initialize requests.
- Notifications are allowed only after initialization.
- The server may close uninitialized clients that send any method other than `initialize`.
- A connection may subscribe to multiple threads.
- Responses are correlated by request ID.
- Notifications are not correlated by request ID.
- Server requests use JSON-RPC requests sent from server to client and must be resolved by response or error.

### Initialize

Method: `initialize`

Params:

```json
{
  "clientInfo": {"name":"serf-tui","version":"0.1.0"},
  "capabilities": {
    "experimentalApi": false,
    "optOutNotificationMethods": []
  }
}
```

Response:

```json
{
  "serverInfo": {"name":"serf-hub","version":"0.1.0"},
  "protocolVersion": "serf-appwire-v1",
  "sourceId": "local",
  "features": {
    "threadList": true,
    "threadTurnsList": true,
    "turnStart": true,
    "turnSteer": true,
    "threadClear": true,
    "forkFromTurn": true,
    "tasks": true,
    "modelList": true,
    "directoryComplete": true
  }
}
```

## Identity Model

### Source ID

A source is a host or backend reachable through the hub. V1 has one source:

- `local`

The hub must keep source identity in all externally visible references so remote sources can be added without changing clients.

### Thread ID

A Serf app thread is the UI-openable conversation identity. For existing Serf concepts, this replaces public use of `session_id`.

Format:

- `th_<ulid>` for new Serf threads.
- Thread IDs must be opaque to clients.

### Session ID

`sessionId` remains as a grouping value for related forked or cleared threads. It is not the primary ref.

### Ref

Public refs are source-qualified:

```text
local:th_01HX...
```

Clients treat refs as opaque strings. The hub parses refs only in `internal/appwire`.

## Core Data Model

### Thread

Serf follows Codex's `Thread` shape with Serf-specific fields under `serf`.

```json
{
  "id": "th_01HX",
  "sessionId": "sess_01HX",
  "forkedFromId": null,
  "preview": "Implement the feature",
  "ephemeral": false,
  "modelProvider": "openai",
  "createdAt": 1778534400,
  "updatedAt": 1778534460,
  "status": {"type":"idle"},
  "path": null,
  "cwd": "/Users/jesse/project",
  "cliVersion": "0.1.0",
  "source": "serf",
  "threadSource": "user",
  "agentNickname": null,
  "agentRole": "default",
  "gitInfo": {"sha": null, "branch": "main", "originUrl": null},
  "name": "Implement the feature",
  "turns": [],
  "serf": {
    "ref": "local:th_01HX",
    "profile": "default",
    "contextPressure": 0.42,
    "capabilities": {
      "send": true,
      "steer": true,
      "interrupt": true,
      "compact": true,
      "clear": true,
      "forkFromTurn": true,
      "shutdown": true,
      "changeModel": true
    }
  }
}
```

### ThreadStatus

Serf uses Codex's tagged status shape:

- `{"type":"notLoaded"}`
- `{"type":"idle"}`
- `{"type":"systemError"}`
- `{"type":"active","activeFlags":[]}`

Serf maps current daemon states into this shape:

- `IDLE`, `idle`, `awaiting`, `awaiting_input` -> `idle`, with `waitingOnUserInput` when the agent is waiting on user reply.
- `PROCESSING`, `processing` -> `active`.
- `ERROR`, `error` -> `systemError`.
- Ended past threads read from storage use `notLoaded` unless resumed.

### Turn

Each user input starts a turn. Steering appends input to the active turn.

```json
{
  "id": "turn_01HX",
  "items": [],
  "itemsView": "notLoaded",
  "status": "inProgress",
  "error": null,
  "startedAt": 1778534460,
  "completedAt": null,
  "durationMs": null
}
```

### ThreadItem

Serf reuses Codex item kinds where they match:

- `userMessage`
- `agentMessage`
- `reasoning`
- `plan`
- `commandExecution`
- `fileChange`
- `mcpToolCall`
- `webSearch`

Serf adds these item kinds:

- `serfToolCall` for Serf-native tool calls that are not command, file change, MCP, or web search.
- `serfSubagent` for subagent lifecycle and status.
- `serfTaskList` for task snapshots.
- `serfHook` for plugin hook execution.
- `serfContextCompaction` for context compaction events.
- `serfPlugin` for plugin load status.
- `serfPrompt` for prompt load status.

Serf item kinds are serialized with `type` values prefixed by `serf`, for example `{"type":"serfTaskList", ...}`.

## Request Methods

### `thread/list`

Lists threads from the hub's sources. The web dashboard and TUI derive project grouping from this response rather than calling `/api/tree`.

Serf extends Codex params with:

```json
{
  "cursor": null,
  "limit": 50,
  "sortKey": "updated_at",
  "sortDirection": "desc",
  "modelProviders": [],
  "sourceKinds": ["serf"],
  "archived": false,
  "cwd": null,
  "searchTerm": "",
  "statuses": ["active","idle","notLoaded"],
  "sourceIds": ["local"],
  "includeSubagents": true
}
```

Response follows Codex:

```json
{"data":[],"nextCursor":null,"backwardsCursor":null}
```

### `thread/read`

Reads thread metadata and optionally turn summaries or full items.

Params:

```json
{"threadId":"th_01HX","includeTurns":true,"itemsView":"summary"}
```

The hub accepts refs in addition to thread IDs by adding `ref`:

```json
{"ref":"local:th_01HX","includeTurns":true,"itemsView":"summary"}
```

Exactly one of `threadId` or `ref` is required.

### `thread/turns/list`

Returns paginated turns for transcript browsing.

Params:

```json
{"threadId":"th_01HX","cursor":null,"limit":50,"sortDirection":"asc","itemsView":"summary"}
```

### `thread/turns/items/list`

Returns paginated full items for a turn.

Params:

```json
{"threadId":"th_01HX","turnId":"turn_01HX","cursor":null,"limit":100,"sortDirection":"asc"}
```

### `thread/start`

Starts a new Serf thread. Serf follows Codex params and adds provider-neutral Serf fields under `serf`.

Params:

```json
{
  "model": "gpt-5.2",
  "modelProvider": "openai",
  "cwd": "/Users/jesse/project",
  "approvalPolicy": "on-request",
  "sandbox": "workspace-write",
  "developerInstructions": null,
  "threadSource": "user",
  "serf": {
    "agent": "default",
    "reasoningEffort": "high",
    "initialTask": "Implement the feature",
    "sourceId": "local"
  }
}
```

Response follows Codex `ThreadStartResponse` and includes the created thread.

The hub starts a local `serf serve` runtime when the source is local. The daemon creates the thread and returns metadata through app-wire. If `initialTask` is non-empty, the hub calls `turn/start` after the thread is created.

### `thread/resume`

Resumes a not-loaded thread and attaches a listener. Resume is by thread ID or ref. The hub resolves cwd and state location from source metadata, not from the caller's process cwd.

Params:

```json
{"ref":"local:th_01HX","excludeTurns":true}
```

### `thread/fork`

Serf extends Codex fork to support fork-from-turn editing.

Params:

```json
{
  "threadId": "th_01HX",
  "cwd": "/Users/jesse/project",
  "serf": {
    "sourceTurnId": "turn_01HY",
    "editedInput": "Revised user instruction",
    "label": "original branch"
  }
}
```

The `serf.sourceTurnId` and `serf.editedInput` fields are required for Serf's transcript edit/fork UI. Plain Codex-style fork without these fields is not exposed in Serf clients.

### `turn/start`

Starts a turn on an idle thread.

Params:

```json
{
  "threadId": "th_01HX",
  "input": [{"type":"text","text":"Continue"}],
  "cwd": "/Users/jesse/project",
  "model": "gpt-5.2",
  "effort": "high"
}
```

If the thread is not loaded, the hub resumes it first, subscribes the client, then starts the turn.

### `turn/steer`

Adds input to the currently active turn.

Params:

```json
{
  "threadId": "th_01HX",
  "expectedTurnId": "turn_01HY",
  "input": [{"type":"text","text":"Also check tests"}]
}
```

The daemon rejects the request if `expectedTurnId` does not match the active turn.

### `turn/interrupt`

Interrupts an active turn.

Params:

```json
{"threadId":"th_01HX","turnId":"turn_01HY"}
```

### `thread/compact/start`

Starts context compaction for a thread.

Params:

```json
{"threadId":"th_01HX"}
```

### `thread/clear`

Serf extension. Clears the current visible conversation by creating a new thread under the same session grouping and runtime configuration. Returns the new thread and ref.

Params:

```json
{"threadId":"th_01HX"}
```

Response:

```json
{"thread":{"id":"th_01HZ"},"ref":"local:th_01HZ"}
```

### `thread/model/set`

Serf extension. Changes the sticky model for future turns.

Params:

```json
{"threadId":"th_01HX","modelProvider":"openai","model":"gpt-5.2"}
```

### `serf/tasks/list`

Serf extension. Returns task state for a thread.

Params:

```json
{"threadId":"th_01HX"}
```

Response:

```json
{"tasks":[{"id":"task_1","title":"Run tests","status":"inProgress"}]}
```

### `serf/dirs/complete`

Serf extension. Directory completion for spawn UI.

Params:

```json
{"prefix":"/Users/jesse/Doc"}
```

Response:

```json
{"dirs":["/Users/jesse/Documents"]}
```

### `model/list`

Returns provider-neutral model options. Serf's response follows Codex's model list shape where possible and includes provider metadata.

## Notifications

Serf uses Codex notification names where possible:

- `thread/started`
- `thread/status/changed`
- `thread/name/updated`
- `turn/started`
- `turn/completed`
- `turn/diff/updated`
- `turn/plan/updated`
- `item/started`
- `item/completed`
- `item/agentMessage/delta`
- `item/reasoning/summaryTextDelta`
- `item/commandExecution/outputDelta`
- `item/mcpToolCall/progress`
- `warning`
- `error`
- `model/rerouted`

Serf adds:

- `serf/thread/contextPressure/updated`
- `serf/thread/model/updated`
- `serf/task/updated`
- `serf/subagent/started`
- `serf/subagent/completed`
- `serf/plugin/loaded`
- `serf/hook/started`
- `serf/hook/completed`
- `serf/communicate/requested`
- `serf/communicate/resolved`

Every notification that belongs to a thread includes:

- `sourceId`
- `threadId`
- `ref`
- `sequence`

Turn and item notifications also include:

- `turnId`
- `itemId` when item-scoped

`sequence` is a source-local monotonically increasing integer. It is used for reconnect and replay/live handoff. The hub must preserve source sequence identity and must not renumber daemon events without also preserving the original source sequence in `sourceSequence`.

## Event Projection

Serf agent events map into app notifications:

| Agent event | App projection |
| --- | --- |
| `SESSION_START` | `thread/started` and initial `thread/status/changed` |
| `USER_INPUT` | `item/started` + `item/completed` with `userMessage` |
| `ASSISTANT_TEXT_START` | `item/started` with `agentMessage` |
| `ASSISTANT_TEXT_DELTA` | `item/agentMessage/delta` |
| `ASSISTANT_TEXT_END` | `item/completed` with final `agentMessage` |
| `TOOL_CALL_START` | `item/started` with `serfToolCall` or a more specific item kind |
| `TOOL_CALL_OUTPUT_DELTA` | `item/serfToolCall/outputDelta` |
| `TOOL_CALL_END` | `item/completed` |
| `STEERING_INJECTED` | `item/started` + `item/completed` with `userMessage`, flagged as steering |
| `CONTEXT_COMPACTION` | `item/completed` with `serfContextCompaction` and `serf/thread/contextPressure/updated` |
| `SUBAGENT_START` | `serf/subagent/started` and `item/started` with `serfSubagent` |
| `SUBAGENT_END` | `serf/subagent/completed` and `item/completed` |
| `PLUGIN_LOADED` | `serf/plugin/loaded` |
| `HOOK_START` | `serf/hook/started` |
| `HOOK_END` | `serf/hook/completed` |
| `COMMUNICATE` | `serf/communicate/requested` when awaiting user input |
| `ERROR` | `error` and current turn failure when turn-scoped |
| `WARNING` | `warning` |
| `SESSION_END` | `turn/completed` if active, then `thread/status/changed` to `notLoaded` or `systemError` |

The projection layer is authoritative for UI rendering. Clients should not parse raw Serf transcript events.

## Subscriptions And Replay

Thread lifecycle:

1. Client calls `thread/read` or `thread/resume`.
2. Hub subscribes the connection to the thread.
3. Hub returns current metadata and any requested turns.
4. Live notifications stream on the same JSON-RPC connection.

Reconnect:

- Client reconnects and initializes.
- Client calls `thread/read` with `afterSequence`.
- Hub returns missed notifications or tells the client to reload turns if the gap is outside retention.
- Client resumes live notifications after the returned high-water mark.

The daemon should persist enough event identity to bridge replay/live handoff. A ring buffer alone is not sufficient for durable transcript-follow.

## Hub Source Routing

The hub stores source drivers behind this conceptual interface:

```go
type Source interface {
	ID() string
	ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error)
	ReadThread(context.Context, appwire.ThreadReadParams) (appwire.ThreadReadResponse, error)
	StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error)
	ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error)
	ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error)
	StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error)
	SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error)
	InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error)
	Subscribe(context.Context, appwire.Ref, appwire.Cursor) (<-chan appwire.ServerNotification, error)
}
```

The interface is shaped like app-wire requests, not like current hub sessions. It is an internal routing seam. The wire protocol remains the source of truth.

## Rendezvous Changes

Rendezvous files stop pointing at REST daemon addresses. They point at app-wire endpoints.

```json
{
  "pid": 12345,
  "protocol": "serf-appwire-v1",
  "endpoint": "ws://127.0.0.1:49152/rpc",
  "sourceId": "local",
  "threadId": "th_01HX",
  "sessionId": "sess_01HX",
  "workingDir": "/Users/jesse/project",
  "stateDir": "/Users/jesse/.local/state/serf/projects/abc",
  "agent": "default",
  "modelProvider": "openai",
  "model": "gpt-5.2",
  "startedAt": "2026-05-11T12:00:00Z",
  "spawnedBy": "serf-hub"
}
```

The hub discovers local daemons from these files and opens app-wire clients to them. It does not probe `/status`.

## Web UI

The web UI becomes a JSON-RPC client. It keeps static HTML/CSS/JS serving, but all stateful interactions go through `/rpc`.

Required browser client pieces:

- Connection manager with initialize, reconnect, and request ID tracking.
- Store/reducer for threads, turns, items, and notifications.
- Dashboard grouping derived from `thread/list`.
- Session view rendered from `thread/read` plus live notifications.
- Spawn, send, steer, interrupt, compact, clear, fork, tasks, and model changes through app-wire methods.

HTMX can remain only for static page shell or settings panes that do not need live session state. Session state should not depend on HTML partials.

## TUI

`serf-tui` stops using `internal/hubapi` and stops parsing SSE. It uses `internal/appwire.Client`.

Startup:

- Resolve hub address as today.
- Auto-start local hub as today.
- Connect websocket to `/rpc`.
- Send `initialize`.
- Fetch `thread/list`.

Session view:

- Opens by ref.
- Calls `thread/read`.
- Receives notifications on the same connection.
- Sends idle input through `turn/start`.
- Sends active input through `turn/steer` with the active `turnId`.
- Interrupts through `turn/interrupt`.

## Error Model

JSON-RPC error codes:

- `-32700`: parse error
- `-32600`: invalid request
- `-32601`: method not found
- `-32602`: invalid params
- `-32603`: internal error
- `-32001`: server overloaded
- `-32010`: thread not found
- `-32011`: source not found
- `-32012`: thread not loaded
- `-32013`: active turn mismatch
- `-32014`: action unavailable
- `-32015`: provider unavailable

Error data includes:

```json
{
  "serfErrorInfo": "activeTurnMismatch",
  "sourceId": "local",
  "threadId": "th_01HX",
  "retryable": false
}
```

## Testing Strategy

Protocol tests:

- JSON-RPC request, response, notification, and error encoding.
- Unknown method rejection.
- Initialize gate.
- Request ID correlation.
- Server request resolution.
- Ref parsing and validation.

Projection tests:

- Each `agent.EventKind` maps to the expected notification or item mutation.
- Assistant deltas aggregate into a completed item.
- Tool start/output/end produces a stable item ID.
- Subagent and task events update both notifications and thread detail state.
- Sequence values are monotonic and survive replay/live handoff.

Daemon tests:

- `serf serve` writes app-wire rendezvous.
- Hub can connect to a daemon endpoint.
- `thread/read` returns current thread metadata.
- `turn/start` drives an agent input.
- `turn/steer` requires matching active turn ID.
- `turn/interrupt` cancels the active turn.

Hub tests:

- Source registry routes refs by source ID.
- `thread/list` aggregates local daemon and not-loaded state.
- Spawn starts a daemon and returns a source-qualified ref.
- Clear returns a new ref.
- Fork-from-turn creates the expected child thread.
- Reconnect returns missed notifications or a reload instruction.

Client tests:

- TUI renders dashboard from `thread/list`.
- TUI opens a session from `thread/read`.
- TUI applies item delta notifications.
- Web client reducer produces the same final message state from replay and live notifications.

End-to-end tests:

- Spawn from hub, send input, receive assistant deltas, interrupt, compact, clear.
- Resume an ended thread and send another turn.
- Fork from a prior turn with edited input.

## Rollout Plan

This is a breaking branch and should not be merged piecemeal into `main` until the new protocol is usable end to end.

Milestones:

1. Add app-wire protocol package and tests.
2. Add reusable app-server connection manager.
3. Project Serf agent events into app notifications.
4. Replace `serf serve` REST/SSE server with app-wire endpoint.
5. Update rendezvous and hub local source driver.
6. Replace hub `/api/*` state paths with `/rpc`.
7. Replace TUI `internal/hubapi` client and SSE parsing with app-wire client.
8. Replace browser session state with JSON-RPC websocket state.
9. Remove old REST/SSE and `internal/hubapi`.
10. Run full Go and JS verification.

## Settled Decisions For This Plan

- Local daemon app-wire uses websocket over loopback for v1. Unix sockets are not part of this replacement.
- Old rendezvous and hub state files are abandoned on this branch. A migration can be proposed separately if real data preservation becomes a product requirement, but this plan does not include one.
- The browser client uses hand-written TypeScript types that mirror `internal/appwire` for v1. Generated schemas can be added later after the protocol stops moving.
