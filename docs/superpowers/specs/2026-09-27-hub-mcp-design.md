# Evener Hub MCP — design

Date: 2026-09-27
Status: design written and implemented on branch `hub-mcp` in one
overnight pass, at Jesse's instruction ("build it out, test it by using
it"). Every decision below is open to veto; this document exists so the
review can happen after the fact against working code instead of before
it against prose.

## Problem

Evener sessions that act as a project manager — supervising other agent
sessions, splitting open-ended work across them, steering, reporting —
currently have no first-class way to see or act on the hub. The web UI and
TUI are human surfaces; an agent needs a tool surface. MCP is the natural
vehicle: evener sessions already consume MCP servers (stdio/http/sse via
`.mcp.json` and launch config), and the hub already speaks a typed RPC
protocol with a maintained TypeScript client (`@evener/appwire-client`).

This design adds **one MCP server, `hub-mcp`**, that exposes the hub's
session-management surface to an agent. Primary audience: the planned
"PM" session kind. Secondary: any MCP client pointed at a user's hub.

## Decisions

### D1. A Node program at `hub-mcp/`, consuming the SDK as a real consumer

The server is a TypeScript package at the repo root (`hub-mcp/`), built
with `tsc` to `dist/`, run as `node hub-mcp/dist/index.js`. It imports
`@evener/appwire-client` by name (repo rule; never a relative path),
resolved through a `file:../appwire-client/typescript` dependency — the same resolution
an installed consumer gets via the package's `exports` map. A preflight
script builds `../typescript/dist` when missing or stale, mirroring
`web-preflight.sh`'s role for the frontend and refusing the same
symlinked-`node_modules` hazard called out in AGENTS.md.

Rejected: living inside `appwire-client/typescript` (that package is a
published library; a server program is a consumer, not an export), and
hand-rolling the AppWire JSON-RPC instead of using the SDK (the SDK is
the deliverable's stated foundation and gives us the handshake,
reconnect, heartbeat, typed methods, and notification fan-out for free).

### D2. Official MCP TypeScript SDK over stdio

The server uses `@modelcontextprotocol/sdk` (McpServer + StdioServerTransport).
Evener's own client is the official Go SDK (`github.com/modelcontextprotocol/go-sdk`
v1.6.1, protocol rev 2025-11-25 negotiated downward), so SDK-to-SDK
interoperability is the vendor-supported path — handshake, version
negotiation, ping/pong keepalive, and error shapes are all handled by
code that is tested against each other.

Rejected: hand-rolled JSON-RPC over stdio. MCP looks small (initialize,
tools/list, tools/call) but the details that break real interop — version
negotiation, ping, cancellation, content block shapes — are exactly what
the SDK exists to get right, and the go-sdk client is chatty.

### D3. Tools only; no resources, no prompts (v1)

The consumer is an agent, not an IDE. Agents call tools; resources and
prompts add surface without adding capability for this audience. If a
human-facing MCP consumer appears later, resources can be added without
breaking tools.

### D4. One AppWire connection, Bearer-authenticated with `ws`

One `AppwireClient` per server process. Node's platform WebSocket cannot
set headers, so the authenticated `socketFactory` the SDK README calls for
is built on `ws` (`Authorization: Bearer <token>`), normalized to the
SDK's `WebSocketLike`. The connection is lazy: the MCP server completes
its own MCP `initialize` without the hub, so `tools/list` works even
while the hub is down; the first hub-touching tool call connects. This
keeps a session usable (and diagnosable) when the hub is temporarily
away, and errors carry the hub URL and connection state.

### D5. Configuration by environment, token never in config files

- `EVENER_HUB_RPC_URL` — hub `/rpc` WebSocket URL
  (default `ws://127.0.0.1:9180/rpc`).
- `EVENER_HUB_TOKEN` — explicit hub capability token. Wins if set.
- `EVENER_HUB_TOKEN_FILE` — token file (default `<state root>/auth-token`,
  where the state root is `EVENER_HUB_STATE_ROOT`, else
  `${XDG_STATE_HOME:-$HOME/.local/state}/evener`).

The token-file default means a session's `.mcp.json` entry needs no
secret material at all: the MCP reads the hub's own auth token from the
same place the TUI and scripted clients do. The hub token is a full-hub
bearer capability ("anyone holding it has full hub access", hub docs);
the MCP grants no authority beyond what its holder already has, but the
operator must understand a session wired this way can drive the whole
hub. That is precisely the PM session's job. The token is never logged
and never echoed in tool output or errors.

### D6. Twelve tools, organized around the PM loop

The surface is the PM's actual jobs: orient, inspect, delegate, follow,
correct, stop, report, find.

| Tool | AppWire methods | Purpose |
| --- | --- | --- |
| `hub_overview` | `initialize` info, `evener/daemon/list`, `evener/projects/recent` | One-call orientation: hub version/features, daemon fleet health, recent projects. |
| `list_sessions` | `thread/list` | The fleet at a glance: status, name/preview, project, model, updated, queue depth, task progress. Filters: `status[]`, `search`, `include_subagents`, `limit`. |
| `get_session` | `thread/read` (subscribe) | One session in depth: status + flags, model/effort, context pressure, cost, tasks, goal, notes, capabilities (what you may do to it now), diagnostics (delegates/jobs/watches). |
| `read_transcript` | `thread/turns/list`, `thread/turns/items/list` | What a session actually did, as readable text: user/assistant/steering messages, tool calls with truncated args/output, errors. `turns` (count from newest), `cursor` for paging older, `detail` outline/full. |
| `start_session` | `thread/start`, optional `evener/thread/name/set` | Delegate work: cwd, prompt, name, model, reasoning_effort, max_subagent_depth, non_interactive. Returns the new ref plus follow-up advice. |
| `send_message` | `thread/read` then `turn/start` \| `turn/steer` \| `turn/queue` | The state-machine-aware send. `mode` `auto` (default) reads fresh status and picks start/steer/queue; explicit modes available. Retries once on instance-id mismatch (session restarted mid-send). |
| `interrupt_session` | `turn/interrupt` | Stop the running turn, keep the session. |
| `stop_session` | `thread/shutdown`, `evener/thread/forceStop` when `force` | Graceful stop; `force:true` is the stuck-session escalation. |
| `clear_queue` | `thread/clear` | Drop a session's queued input. |
| `list_tasks` | `evener/tasks/list` | A session's task breakdown without reading its transcript. |
| `search_sessions` | `evener/search` | Find sessions by content, live and past. |
| `wait_for_activity` | (buffered notifications) | Block until matching hub events arrive or the timeout expires; the PM's "watch" primitive. See D7. |

Naming: MCP tool names are snake_case (spec-safe). The recommended
`.mcp.json` server name is `hub`, so a session sees `hub__list_sessions`,
`hub__send_message`, etc. (evener sanitizes the configured name into the
namespace: `servername__toolname`).

Deliberately excluded from v1 (YAGNI; each is a hub RPC that can wrap
later without breaking the surface): `thread/fork`, `thread/resume`,
`evener/archive/set`, `evener/session/delete`, `goal/set` on workers,
model/effort changes, credentials/host management, images. `rename` is
included only as the optional `name` on `start_session`.

### D7. `wait_for_activity`: push semantics through a pull tool

MCP tool calls are request/response, but a supervisor needs to know when
things happen. The server subscribes to threads (every tool that names a
ref issues `thread/read` with `subscribe: true`, matching the hub's
per-client subscription model) and keeps a bounded in-memory ring of
rendered events, each stamped with a monotonically increasing cursor.

`wait_for_activity(refs?, events?, since?, timeout_seconds?, limit?)`:
- `refs` defaults to all subscribed sessions; naming new refs subscribes
  them on demand.
- `events` filters by group — `turns`, `status`, `tasks`, `jobs`,
  `delegates`, `attention`, `errors` — default all. Token-delta
  notifications (`item/agentMessage/delta`, `item/toolOutput/delta`, …)
  are never buffered; only boundary events are (turn started/completed,
  item completed, thread status changed, closed, queue changed, name
  changed, model retry, warning, steering injected, task updated, goal
  updated, job started/finished, delegate updated, attention changed).
- Returns immediately with buffered events after `since` if any exist;
  otherwise blocks up to `timeout_seconds` (default 30, capped 120 — the
  call runs inside the calling session's turn, so it must return well
  before any plausible human patience and its context is cancellable by
  an interrupt).
- Every tool response that can change what a PM watches includes the
  current `activity_cursor`, and every wait result includes the new one.

This turns the naive "poll list_sessions in a loop" into
"wait once per event batch", which is both cheaper and the honest
representation of the hub's notification stream.

### D8. Mutations follow the hub's identity rules

Every mutating tool mints a `clientMutationId` (`crypto.randomUUID`) and
carries `expectedInstanceId` read from a fresh `thread/read` immediately
before the mutation — the CAS the protocol uses to catch a session that
restarted between read and write. On an instance-id mismatch the tool
re-reads and retries once; a second mismatch surfaces as a tool error
explaining the race. Outcome-uncertain connection drops during a
mutation are reported as such, never silently retried (the same rule the
SDK's README imposes on applications).

### D9. Text-first rendering, honest truncation

Every tool returns compact readable text (MCP text content). Rows are
one line where possible; long fields truncate with an explicit marker
(`… (+N chars)`); transcripts render as readable prose with tool calls
summarized (`shell: git status` rather than full argument JSON at outline
detail). Timestamps are UTC ISO-8601 with relative ages ("12m ago").
Refs are always full and canonical (`local:<SID>`) and every tool
accepts exactly the refs the other tools return, plus a bare `<SID>`
normalized to `local:<SID>`. Errors are actionable ("hub unreachable at
<url> (state: <s>): is it running?").

### D10. Testing: three layers, all hermetic

1. **Unit** (`node --test` over `dist`, no extra runner dependencies):
   rendering, the `send_message` state machine, the event buffer and
   wait semantics, config/token resolution — against a scripted fake of
   the client seam (the same port pattern the SDK's own stores use:
   `request`/`onNotification`).
2. **E2E against a real hub** (`hub-mcp/scripts/e2e/`): the scripted
   MCP client is a Node script speaking real MCP over stdio to the built
   server — initialize, tools/list, then a full PM workflow: overview →
   start (dormant and fakellm-driven sessions) → wait → read transcript
   → steer → interrupt → stop. The hub is the repo's own binary run
   HOME-isolated on a kernel-assigned port per
   docs/developing-evener/agentic-testing.md; sessions run against
   `test/e2e/fakellm` (scripted provider, no credentials, no network).
   This layer is "use it" in the strict sense: the exact bytes a PM
   session's daemon would send are what the test sends.
3. **A real session using it** (the wiring proof): a PM session is
   spawned with the MCP configured in `.mcp.json`, its model driven by a
   new `--script` mode of the standalone `fakellm` command (additive,
   per-round scripted answers), and the run asserts on durable state —
   the PM transcript records real MCP tool calls and results, and the
   hub shows the worker session the PM started through the MCP.

Default tests stay deterministic: no provider credentials, no network
beyond loopback, no wall-clock guessing (fakellm holds turns open; the
driver decides when rounds answer).

Gates: `make test-hub-mcp` (typecheck + unit, part of the tree like
`test-api-package`), `make test-hub-mcp-e2e` (builds Go binaries; run in
CI and pre-merge for this area). Biome covers `hub-mcp/` by explicit
paths in the new package's own lint script — the repo-wide `lint-biome`
scope (frontend `src/` + the SDK) is unchanged.

## Non-goals

- No new hub RPCs; if the hub surface lacks something the PM needs, that
  is a hub change, not an MCP shim.
- No remote-host management (the `source`/`evener/host/*` surface) in v1.
- No streaming/progress notifications to the MCP client (no long-running
  tools besides the bounded wait).
- The "PM session kind" itself (a launch profile/preset with the MCP
  preconfigured) is a separate, later change; this design ships the MCP
  plus the `.mcp.json` recipe any session can use today.

## Security notes

- The MCP process holds a full-hub bearer capability (see D5). Config
  via token file avoids pasting secrets into config files the model can
  read; the global `~/.config/evener/mcp.json` and CLI layers are the
  trusted config surfaces (mcpconfig's own trust split).
- The MCP never writes to disk, spawns nothing, and talks only to the
  hub URL it was given. All input comes from the MCP client (the
  session's daemon) and the hub.

## Post-critique revisions (2026-09-27)

A four-lens critique of the first-pass operator documentation found unsafe
wiring advice, an overclaim the code broke, and missing caveats. The
decisions above are the historical record; the revisions below supersede
them where they conflict. Code and documentation changed together.

- **Wiring.** The operator doc now makes the per-launch/per-session entry
  the primary recipe and moves the global `~/.config/evener/mcp.json`
  route behind an explicit warning naming both hazards: it hands the
  full-hub tool set to every session on the machine (including sessions
  working in untrusted repos), and the project layer (`.evener/mcp.json`)
  is model-writable, loads with no approval gate, and only refuses
  `$(command)` expansion — so a bare stdio entry pointing at the server's
  dist wires full-hub power into a session zero-config.
- **Tokens.** `EVENER_HUB_TOKEN` in a mcp.json `env` map is
  session-readable (the global config is not masked from session reads);
  the token-file default is now the stated recommendation for that reason.
- **Spawner-token collision** (found by the e2e gate, live). The hub
  injects its internal spawner token into every spawned session's env as
  `EVENER_HUB_TOKEN` (envvars/envvars.go: "per-hub bearer token passed
  to spawned evener serve daemons") — a bearer for the hub's dials to
  the daemon that the hub's own `/rpc` refuses (server/server.go
  authorizes only the auth-token file value). A wiring inside a
  hub-spawned session therefore sets `EVENER_HUB_TOKEN` to the empty
  string so the token-file default wins; the operator doc and the e2e
  harness both carry that recipe, and the gate asserts its mcp.json
  contains no token. The product-side fix — stop overloading the name,
  or accept the spawner token on `/rpc` — is follow-up work outside
  this server.
- **The honest wait (D7).** `wait_for_activity` no longer reports a quiet
  timeout when something is wrong. Waits disclose events skipped past the
  return limit, ring-buffer eviction gaps, how many sessions are actually
  watched, subscribe failures, and an unreachable hub (as an error). The
  cursor is `<epoch>:<seq>`; a `since` from a foreign epoch restarts from
  now and says so. `list_sessions` no longer subscribes the rows it
  lists; `get_session`, `read_transcript`, `start_session`, and named
  refs in a wait do.
- **Surface (D6).** Added `list_models` (discover model strings for
  `start_session`) and `rename_session`; `resume_session` may follow
  (wire support still being verified). Session states print with the
  wire's own vocabulary (`idle`, `active`, `awaiting`, `warning`,
  `systemError`, `closed`, `notLoaded`, `restartRequired`). Tool
  descriptions now carry an untrusted-content warning: session content is
  untrusted data from the repos those sessions read, and transcripts and
  search span every project on the hub and may contain other projects'
  secrets.
- **Scoping knobs.** `EVENER_HUB_MCP_READONLY=1` registers only the read
  tools; `EVENER_HUB_MCP_PROJECT=<absolute path>` narrows list/search rows
  and session tools to one project and refuses out-of-scope refs.
- **`max_subagent_depth`.** Unset uses the hub default (2); the minimum
  is 1; the field cannot express "no subagents."
- **Session-facing honesty.** `start_session` guidance now points to
  `list_sessions` first when a start's outcome is unknown;
  `stop_session` with `force` resolves identity first and echoes the
  name/cwd of what it stopped.
- **Build caveat.** Preflight's `npm ci` executes dependency lifecycle
  scripts at dev time, and the dist runs from a checkout; the operator
  doc says so.

The revised operator documentation is `docs/evener-hub-mcp.md`.
