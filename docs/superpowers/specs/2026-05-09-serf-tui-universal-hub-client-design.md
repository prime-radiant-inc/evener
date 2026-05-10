# Serf TUI Universal Hub Client - Design Spec

Date: 2026-05-09
Status: Draft after two-agent critique
Linear: PRI-1542

UX note: `docs/superpowers/specs/2026-05-09-serf-tui-dashboard-ux-design.md` is authoritative for dashboard, project drill-down, session navigation, and transcript browse/fork behavior. Where that focused UX spec conflicts with this document's earlier generic dashboard/drill-in wording, use the focused UX spec.

## Summary

`serf-tui` becomes the terminal client for the same multi-session system that powers `serf-hub`. It stops being a single-session app that embeds or connects directly to one daemon. On startup it connects to a local hub, starts one if none is running, then presents a live dashboard grouped by project, with project drill-down for history. Users can drill into any session, read the full transcript, follow live activity, send input where supported, and use the same spawn, resume, fork, and control flows as the web hub.

This is intentionally a breaking design. There is no compatibility mode for the old embedded/direct single-session `serf-tui`. The hub is the control plane; the TUI is a hub client.

Two read-only critique passes were incorporated into this draft: one focused on hub/API architecture and one focused on TUI implementation and terminal UX. The main changes from those critiques are explicit custom-address auto-start behavior, durable transcript-follow event identity, resume-by-record instead of resume-by-ID, daemon-reported capabilities, clear returning a new ref, URL-safe refs, a narrower package extraction, and stronger SSE/parser/keyboard requirements.

## Goals

- Make `serf-tui` a universal terminal dashboard for all Serf sessions on the local host.
- Use the same hub system as the web UI for discovery, spawn, resume, fork, REST proxying, and SSE proxying.
- Start a local hub automatically when no local hub is running.
- Let users browse live sessions, past sessions, projects, forks, and subagents from one TUI.
- Let users drill into a session and see a complete transcript, not just the daemon's current SSE ring buffer.
- Support both interactive and non-interactive sessions through explicit capabilities instead of special-case UI paths.
- Keep the design ready for remote hosts by making session references source-aware from the start.

## Non-goals

- No backward-compatible embedded server mode in `serf-tui`.
- No direct daemon mode in `serf-tui`.
- No second local discovery implementation in `serf-tui`; it must not read rendezvous files or project state directly for normal operation.
- No remote host implementation in the first implementation pass.
- No terminal UI for editing system prompts, provider config, plugin code, or MCP definitions.
- No full-text transcript search in v1. Metadata search and in-open-session search are enough.

## Current Implementation Context

`serf-hub` already owns the right local control-plane primitives:

- A live roster watches `~/.serf/run/<pid>.json`, probes each daemon's `/status`, and maps `session_id` to daemon address.
- A past index reads saved session metadata across project state directories.
- A spawner starts and resumes `serf serve` subprocesses.
- REST and SSE proxies hide daemon addresses behind the hub origin.
- The web UI has session workspace routes, send, fork, replay, search, and spawn flows.

`serf-tui` currently owns single-session concerns:

- Startup either connects to one `--addr` daemon or starts an embedded in-process server.
- One Bubble Tea model stores one active session, one message list, one input, one SSE stream, and one direct daemon address.
- HTTP helpers call daemon endpoints directly.
- The SSE client reads `http://<daemon>/events` once and reports stream closure as an error.
- Transcript browsing and subagent inspection read local state directly.

The new design keeps the useful TUI rendering and event-reduction logic, but changes ownership boundaries:

- Hub owns process discovery, history discovery, routing, spawn, resume, fork, and remote readiness.
- TUI owns terminal layout, keyboard interaction, local cache, and session rendering.
- Daemons remain the runtime execution units behind hub.

## Design Principles

- One source of truth: hub is the only source of session lists, session metadata, and daemon routing.
- No HTML scraping: TUI uses JSON and SSE APIs, not web templates or htmx fragments.
- Durable transcript first: drill-in reconstructs from persisted transcript, then tails live events when the session is running.
- Capability-driven actions: UI enables controls from explicit server capabilities, not from inferred session type.
- Opaque references: TUI treats session references as opaque strings so remote hosts can be added without rewriting navigation.
- Small internal boundaries: split hub client, dashboard state, session state, event reduction, and rendering.

## User Experience

### Startup

Running `serf-tui` opens the dashboard. Startup flow:

1. Resolve hub address from `--hub-addr`, `SERF_HUB_ADDR`, hub config, or default `127.0.0.1:9180`.
2. Normalize the address into a base URL and, when local, a bind address. Accepted inputs are `host:port`, `http://host:port`, and `http://host:port/`.
3. Probe `GET /api/health`.
4. If the probe succeeds, connect.
5. If the probe fails and the normalized address is local, start `serf-hub` as a detached background process on the same resolved bind address.
6. Wait for health with a bounded timeout.
7. If startup fails, show a terminal error screen with the attempted command and hub log path.

Local address means `localhost`, `127.0.0.0/8`, or `::1` with an `http` scheme. `serf-tui` must not auto-start a hub for a non-loopback host. Remote hub startup belongs to the remote host's service manager, not the TUI.

The hub process should survive TUI exit. Starting it as a child tied to the TUI would make the "universal client" unstable because other TUI instances and the web UI would lose their control plane when one TUI exits.

Hub binary resolution:

- Prefer `--hub-bin` when provided.
- Else use a `serf-hub` binary next to the running `serf-tui` executable.
- Else fall back to `serf-hub` on `PATH`.

Hub startup command:

```sh
serf-hub --addr <resolved-local-bind-addr>
```

TUI redirects detached hub stdout/stderr to `~/.serf/logs/hub-<timestamp>.log`. If another process wins the startup race, the hub lock causes the loser to exit; TUI retries health and proceeds if the winning hub is healthy.

### Dashboard

The default screen is a live-only dashboard grouped by project. The focused dashboard UX spec defines the exact layout and keyboard behavior.

The dashboard may use one pane on narrow terminals or add a preview/details pane when there is enough space. It always keeps the same information architecture:

- Root dashboard: live sessions only, grouped under project headers.
- Project drill-down: live sessions first, then recent ended sessions for that project.
- Session workspace: chat-first view with transcript browse/fork focus.

Rows show:

- Status marker.
- Title.
- Project.
- Age or updated time.
- Short model label when space allows.
- Host label once remote hosts exist.

Rows also have stable row IDs distinct from session refs. Example row IDs: `project:<project-key>` and `project:<project-key>:<ref>`. The dashboard preserves selection by row ID when possible and falls back to session ref only when that row disappears.

Keyboard:

- `j`/`k` or arrows move selection.
- `enter` opens the selected session.
- `n` opens spawn.
- `/` opens search.
- `r` refreshes tree immediately.
- `q` exits.
- `?` opens help.

### Drill-in

Opening a session switches the right pane to the session view. A future layout may allow a persistent sidebar plus session view when terminal width allows; narrow terminals can use a full-screen session view.

Session view includes:

- Header: title, state, project, branch, model, host, turn count, context pressure.
- Conversation viewport using existing TUI message rendering concepts.
- Tool annotations and subagent references.
- Tasks/details overlay.
- Bottom input strip when `can_send` is true.
- Read-only reason when `can_send` is false.

Keyboard:

- `esc` enters transcript browse/fork focus or returns from browse focus to compose; it does not leave the session.
- `ctrl+o` returns to the dashboard.
- `enter` sends input when focused in input.
- `alt+enter` inserts newline.
- `ctrl+j` inserts newline as the reliable terminal fallback.
- `ctrl+c` interrupts only when `can_interrupt` is true and the session is processing; a second `ctrl+c` within a short window exits. When interrupt is unavailable, `ctrl+c` follows the visible quit confirmation path instead of silently doing nothing.
- `/compact`, `/model`, `/tasks`, `/details`, `/clear`, `/fork`, and `/theme` act through hub APIs.
- `tab` cycles focus only in dashboard, spawn, search, and details modes. Inside the conversation viewport it keeps the existing tool expand/collapse behavior so browsing tool output remains fast.

### Spawn

Spawn is a TUI form backed by hub's spawner:

- Required: none. An empty task may create a dormant session if hub supports it.
- Main field: task text.
- Fields: working directory, model, agent, reasoning effort.
- Advanced fields: max rounds, system prompt append, MCP config, plugin dirs, skills dirs, context strategy.

The first pass should implement only fields already supported by hub spawn. Additional old `serf-tui` flags should not be preserved just because they existed before; they need deliberate spawn API support.

### Resume

Resume is invisible:

- Selecting a past session opens it read-only with full transcript replay.
- Sending input to a past session calls hub, which resolves the past session record, starts `serf serve --resume <session_id> --dir <working_dir> --state-dir <state_dir>`, waits for rendezvous, and forwards the input.
- The session keeps the same public session ID.
- The tree transitions from ended to processing after the resumed daemon appears.

Resume must not operate on a bare session ID. The hub spawner API should resume from a resolved session record or ref that includes `state_dir`, `working_dir`, model/profile context, and source host. Without that, arbitrary past sessions can resume against the hub process working directory or the wrong state directory.

### Fork

Forking mirrors the web model:

- User selects a prior user turn and invokes edit/fork.
- TUI asks for edited text and a label for the original branch.
- Hub creates the fork using the same session metadata model as web.
- TUI navigates to the new session reference and refreshes the tree.

The TUI should not implement transcript mutation. Fork creation is a hub/agent operation.

## Architecture

```text
                 terminal
                    |
                    v
             cmd/serf-tui
          dashboard + session UI
                    |
            JSON + SSE over HTTP
                    |
                    v
        serf-hub 127.0.0.1:9180
     roster + past index + spawn + proxy
                    |
        REST/SSE to live daemons
                    |
                    v
             serf serve daemons
```

Future remote architecture:

```text
cmd/serf-tui
   |
   v
local or remote hub endpoint
   |
   +-- local sessions
   +-- remote host A sessions
   +-- remote host B sessions
```

TUI connects to one hub endpoint. It does not discover remote hosts directly. Remote host federation, auth, and routing belong in hub.

## Hub API Additions

The current hub has web routes and a few JSON endpoints. The TUI needs a stable client API.

### Session Reference Encoding

The TUI treats `ref` as opaque, but the API still needs a safe transport grammar. A ref must be URL-safe path-segment text matching `[A-Za-z0-9._~:-]+` in v1. The local format is `local:<session_id>`. Future remote refs may add host-specific prefixes but must preserve that grammar unless the API moves refs into query parameters or JSON bodies.

Clients must build URLs through a typed helper that applies `url.PathEscape`. No TUI code should concatenate `/api/sessions/` with a raw ref string.

### `GET /api/health`

Purpose: startup probe and capability negotiation.

Response:

```json
{
  "version": "dev",
  "started_at": "2026-05-09T02:00:00Z",
  "hub_addr": "127.0.0.1:9180",
  "run_dir": "/Users/jesse/.serf/run",
  "state_glob": "/Users/jesse/.local/state/serf/projects/*",
  "capabilities": {
    "tree": true,
    "transcript_follow": true,
    "spawn_schema": true,
    "spawn": true,
    "fork": true,
    "remote_sources": false
  }
}
```

### `GET /api/tree`

Purpose: dashboard session tree.

Response:

```json
{
  "generated_at": "2026-05-09T02:00:00Z",
  "sources": [
    {
      "id": "local",
      "label": "this host",
      "kind": "local",
      "online": true
    }
  ],
  "live": [
    {
      "row_id": "live:local:01HX...",
      "ref": "local:01HX...",
      "host_id": "local",
      "session_id": "01HX...",
      "title": "Fix the failing tests",
      "project": "serf",
      "state": "processing",
      "kind": "session",
      "live": true,
      "updated_at": "2026-05-09T01:59:30Z",
      "age": "30s",
      "model": "gpt-5.2",
      "children": []
    }
  ],
  "projects": [
    {
      "key": "sha256:abc123",
      "name": "serf",
      "rollup_state": "processing",
      "sessions": [
        {
          "row_id": "project:sha256:abc123:local:01HX...",
          "ref": "local:01HX...",
          "host_id": "local",
          "session_id": "01HX...",
          "title": "Fix the failing tests",
          "project": "serf",
          "state": "processing",
          "kind": "session",
          "live": true,
          "updated_at": "2026-05-09T01:59:30Z",
          "age": "30s",
          "model": "gpt-5.2",
          "children": []
        }
      ]
    }
  ]
}
```

State values are normalized hub UI tokens:

- `awaiting`
- `processing`
- `warning`
- `idle`
- `ended`
- `unknown`

Hub must map daemon `AWAITING_INPUT` to `awaiting`. That state currently falls through normalization and should be fixed before the TUI depends on it.

The project section must include live roster entries even when no metadata has been written yet. A brand-new or dormant live daemon should not appear only in Live; hub can derive the project from rendezvous `working_dir` until past metadata exists.

### `GET /api/sessions/{ref}`

Purpose: selected-session metadata and action capabilities.

Response:

```json
{
  "ref": "local:01HX...",
  "host_id": "local",
  "session_id": "01HX...",
  "title": "Fix the failing tests",
  "state": "idle",
  "live": true,
  "project": "serf",
  "working_dir": "/Users/jesse/Documents/GitHub/prime-radiant-inc/serf",
  "branch": "serf-hub",
  "model": "gpt-5.2",
  "profile": "openai/gpt-5.2",
  "turn_count": 8,
  "context_pressure": 0.42,
  "parent_session_id": "",
  "divergence_turn": 0,
  "fork_label": "",
  "is_subagent": false,
  "capabilities": {
    "send": true,
    "steer": true,
    "interrupt": true,
    "compact": true,
    "clear": true,
    "fork": true,
    "resume": true,
    "shutdown": true,
    "change_model": true,
    "read_only_reason": ""
  },
  "streams": {
    "transcript_follow": "/api/sessions/local:01HX.../events?mode=transcript-follow",
    "live": "/api/sessions/local:01HX.../events?mode=live",
    "replay": "/api/sessions/local:01HX.../events?mode=replay"
  }
}
```

Capabilities must come from hub resolution and daemon status. The daemon `/status` response should add an action-capability object for live sessions. Hub then combines daemon capabilities with live/past/remote resolution. For example, hub can set `resume=true` for an ended local session found in the past index even when no live daemon exists, and can set `read_only_reason` for remote sources where the hub cannot write.

The TUI must not infer writeability from state alone.

### `GET /api/sessions/{ref}/events?mode=...`

Purpose: session event streaming for replay and live follow.

Modes:

- `replay`: emit persisted transcript as synthetic SSE, then `REPLAY_DONE`, then close.
- `live`: proxy daemon `/events` for live-only ring-buffer catch-up and future events.
- `transcript-follow`: emit persisted transcript from disk first, then attach to live daemon events if the session is live.

`transcript-follow` is the default TUI drill-in mode. It solves the current live-session gap where a late-opening client only receives whatever remains in the daemon's ring buffer. A long-running session's full transcript must come from disk.

The TUI API stream should keep the same SSE event names as daemon streams, but each JSON payload must include a reserved `_meta` object:

```json
{
  "_meta": {
    "source": "transcript",
    "transcript_seq": 42,
    "turn_index": 7,
    "runtime_event_id": "",
    "handoff": false
  },
  "text": "example payload field"
}
```

`source` is `transcript`, `live`, or `hub`. `transcript_seq` is a durable sequence derived from transcript order or byte offset. `runtime_event_id` is the daemon broadcaster ID when the event came from a live daemon. The SSE `id:` for hub API streams should use the same durable identity, for example `transcript:42` or `live:184`.

Replay conversion must emit canonical events that the TUI reducer can render:

- `USER_INPUT` with `text` creates a user message.
- Assistant text emits `ASSISTANT_TEXT_START`, one or more `ASSISTANT_TEXT_DELTA` events, then `ASSISTANT_TEXT_END`.
- The reducer may also tolerate the current replay shape where `ASSISTANT_TEXT_END` carries a full `text` field, but that is compatibility for existing hub replay internals, not the target contract.

Ordering rules for `transcript-follow`:

- Replay transcript entries through the last persisted entry.
- Emit a hub `TRANSCRIPT_HANDOFF` event with a `_meta` handoff marker and the transcript watermark.
- Subscribe to daemon events after the handoff point. Use daemon `Last-Event-ID` only for live ring-buffer catch-up; do not reuse transcript SSE IDs as daemon IDs.
- Deduplicate overlap using `_meta.turn_index`, `_meta.transcript_seq`, and `_meta.runtime_event_id`. If the hub cannot prove safe dedupe, it should prefer a small duplicated boundary over dropping user-visible content.

If the session is not live, `transcript-follow` is equivalent to `replay`.

### `POST /api/sessions/{ref}/send`

Purpose: send input and optional images.

Request:

```json
{
  "text": "continue",
  "images": [
    {
      "mime_type": "image/png",
      "data_base64": "..."
    }
  ]
}
```

Behavior:

- If the session is live, forward to daemon `/input`.
- If the live address is stale, force resume and retry once.
- If the session is ended and `can_resume` is true, resolve the past session record, resume with its `working_dir` and `state_dir`, then forward.
- Return daemon status codes where meaningful: 202 accepted, 409 busy, 400 invalid input.

### Session action endpoints

Purpose: avoid TUI constructing `/live/...` proxy paths directly.

Endpoints:

- `POST /api/sessions/{ref}/steer` accepts `{"text":"..."}` and returns `204` on success.
- `POST /api/sessions/{ref}/interrupt` accepts no body and returns `202` when the interrupt was requested.
- `POST /api/sessions/{ref}/compact` accepts no body and returns `204` on success.
- `POST /api/sessions/{ref}/clear` accepts no body and returns `{"ref":"local:<new>","host_id":"local","session_id":"<new>"}`.
- `POST /api/sessions/{ref}/shutdown` accepts no body and returns `202`.
- `POST /api/sessions/{ref}/model` accepts `{"model":"..."}` and returns the updated session detail.
- `GET /api/sessions/{ref}/models` returns the daemon/hub model list.
- `GET /api/sessions/{ref}/tasks` returns the task list JSON.
- `GET /api/sessions/{ref}/details` returns structured status, tools, MCP, skills, plugins, hooks, subagents, daemon PID/address, state paths, and source host.
- `POST /api/sessions/{ref}/fork` accepts `{"turn":3,"edited_message":"...","label":"original approach"}` and returns `{"ref":"local:<child>","host_id":"local","session_id":"<child>"}`.

`clear` is special because it changes the daemon's current session ID. The old ref becomes an ended/past session if its metadata exists. The TUI must navigate to the returned ref and refresh the tree immediately.

All JSON endpoints return `application/json` on success and `{"error":"..."}` on expected failures. Hub should preserve meaningful daemon status codes: `400` validation, `404` unknown ref, `409` busy or unsupported state, `502` stale/unreachable daemon after retry, `503` missing spawner or unavailable source.

The web UI may continue using `/s/<id>/...`; TUI should use `/api/...`.

### `GET /api/spawn-schema`

Purpose: tell TUI which spawn fields are actually supported.

Response:

```json
{
  "fields": [
    {"name": "task", "type": "text", "required": false},
    {"name": "working_dir", "type": "path", "required": false},
    {"name": "model", "type": "model", "required": false},
    {"name": "agent", "type": "string", "required": false},
    {"name": "reasoning_effort", "type": "enum", "values": ["low", "medium", "high"], "required": false}
  ]
}
```

Hub should reject unsupported spawn fields with `400` instead of ignoring them. This prevents the TUI from showing controls such as branch or access mode before hub actually implements them.

### `POST /api/spawn`

Purpose: create a new session from the TUI.

The response should return a session reference, not just a local session ID:

```json
{
  "ref": "local:01HX...",
  "host_id": "local",
  "session_id": "01HX..."
}
```

### `GET /api/search?q=...`

Purpose: dashboard search.

The current search response can be extended to include `ref`, `host_id`, `kind`, `updated_at`, and enough turn-match metadata for in-session results later.

## Shared Packages

Do not start with a broad hub refactor. The first extraction should be only the pieces the TUI must compile against or that need independent tests.

Recommended first package layout:

```text
internal/hubapi/
  types.go
  client.go
  refs.go
```

Recommended optional pure logic package:

```text
internal/hubmodel/
  state.go
  capabilities.go
  tree.go
```

Ownership:

- `internal/hubapi/types.go`: JSON DTOs shared by hub server and TUI client.
- `internal/hubapi/client.go`: typed HTTP client used by TUI.
- `internal/hubapi/refs.go`: ref parsing, validation, and path escaping.
- `internal/hubmodel/state.go`: daemon-to-UI state normalization.
- `internal/hubmodel/capabilities.go`: capability derivation from daemon status and hub source state.
- `internal/hubmodel/tree.go`: pure tree construction if the existing `cmd/serf-hub` tree code becomes hard to test in place.
- `cmd/serf-hub`: server binary, web templates, static assets, route wiring, roster, past index, and spawner until there is a concrete reason to move them.
- `cmd/serf-tui`: Bubble Tea UI and terminal-specific client behavior.

This keeps the TUI from importing a `cmd` package without forcing a large process/discovery refactor before the API is proven.

## TUI Internal Design

### Top-level model

`cmd/serf-tui` should replace the single-session `model` with an app model:

```go
type appModel struct {
    hub          *hub.Client
    health       hub.Health
    tree         hub.Tree
    selectedRef  hub.SessionRef
    mode         mode
    width        int
    height       int
    sessions     map[string]*sessionView
    dashboard    dashboardModel
    search       searchModel
    spawn        spawnModel
    err          error
}
```

Modes:

- `modeDashboard`
- `modeSession`
- `modeSearch`
- `modeSpawn`
- `modeDetails`
- `modeHelp`

### Session view

Extract the current per-session state into `sessionView`:

```go
type sessionView struct {
    ref               hub.SessionRef
    detail            hub.SessionDetail
    messages          []chatMessage
    activeTools       map[string]int
    observedSubagents map[string]subagentUI
    input             textarea.Model
    viewport          viewport.Model
    lastEventID       string
    followTail        bool
    unreadCount       int
    pendingInput      string
    streamState       streamState
    replayComplete    bool
    connected         bool
    staleMetadata     bool
    reconnectAttempt  int
    lastError         error
    err               error
}
```

The current single-session renderer can move here with minimal conceptual change. It should not keep daemon address or state dir fields.

### Event reducer

Split event handling from Bubble Tea layout:

```text
cmd/serf-tui/
  event_reducer.go
  session_view.go
  dashboard_model.go
  hub_client_cmds.go
```

`event_reducer.go` consumes parsed SSE events and mutates a `sessionView` message state:

- Coalesce assistant text deltas.
- Track tool calls by call ID.
- Hide or transform `communicate` events consistently with web.
- Render task-list activity as system lines.
- Track subagent starts and ends.
- Update model, session ID, context tokens, turn count, and processing state.

The reducer should be testable without Bubble Tea.

The reducer must render both live daemon events and hub replay events. In particular, `USER_INPUT` with text creates a user message, and assistant replay text must render even if it arrives as `ASSISTANT_TEXT_END.text` from existing replay internals during migration.

### SSE parser

Replace the current scanner-style SSE client as part of the hub-client work. The new parser must:

- Accept fields with or without a single space after `:`.
- Concatenate repeated `data:` lines with newline separators.
- Preserve the last event ID.
- Emit a final buffered event even if the stream ends without a blank line.
- Check HTTP status before parsing.
- Treat `REPLAY_DONE` and normal replay EOF as completion, not an error.
- Surface live-stream EOF as reconnectable when the session is still live.

### Streaming manager

The TUI should stream only the open session by default. Dashboard state comes from `GET /api/tree` polling.

Rules:

- Opening a session starts `transcript-follow`.
- Switching sessions cancels the previous stream.
- Reopening a cached session may reuse cached messages and issue a live catch-up stream using `Last-Event-ID`.
- Stream errors mark the session view disconnected but do not crash the app.
- `REPLAY_DONE` closes replay streams without error.
- Live stream closure triggers reconnect with backoff while the session is still live.

### Dashboard refresh

Dashboard refreshes should be boring:

- Poll `GET /api/tree` every 2 seconds while dashboard is visible.
- Poll every 5 seconds while in a session.
- Refresh immediately after spawn, send, fork, clear, shutdown, or model change.
- Preserve selection by stable row ID first, then by `ref`.
- Preserve expanded/collapsed project state by project key.
- If selected session disappears, keep the session view open and show stale/disconnected status until the next successful resolution.

## Interactive and Non-interactive Sessions

Do not build two products. Model both as sessions with capabilities.

Examples:

- A live idle session may have `send=true`, `interrupt=false`, `resume=true`.
- A live processing session may have `send=false`, `steer=true`, `interrupt=true`.
- A completed non-interactive session may have `send=false`, `resume=true`.
- A read-only remote session may have `send=false`, `resume=false`, with `read_only_reason`.

The label "non-interactive" in agent runtime config is not enough to decide terminal behavior. Some sessions created by `serf serve` are technically non-interactive runtime sessions but still accept input through the server API. Hub capabilities are the UI contract.

## Remote Host Readiness

Remote support is not implemented in v1, but v1 must not paint itself into a local-only corner.

Required now:

- Every API object includes `host_id`.
- Every session row includes an opaque `ref`.
- TUI stores and navigates by `ref`, not bare `session_id`.
- UI has room for a short host label.
- Hub client accepts non-local `--hub-addr` but does not try to auto-start remote hubs.

Deferred:

- Remote host registration.
- Auth and token model.
- Tailscale identity integration.
- Cross-host search.
- Cross-host spawn placement.

## Runtime State Propagation

Hub-spawned daemons must receive the same runtime paths and auth-relevant environment the hub uses.

Rules:

- If hub supports configurable `run_dir`, `serf serve` must also support the same run dir through an explicit `--run-dir` flag or `SERF_RUN_DIR` env var.
- Hub passes its resolved run dir to every spawned or resumed daemon.
- Hub resume passes the resolved past session `state_dir` through `--state-dir`; it must not rely on ambient `SERF_STATE_DIR`.
- Hub spawn should pass configured state/auth environment intentionally, not incidentally through the process environment.
- Provider credentials and auth state used by `llm.NewFromEnv` must behave the same for hub-spawned daemons as for manually launched `serf serve`.

The implementation must add daemon run-dir support before exposing `run_dir` in the public hub/TUI contract. Until then, run dir remains default-only and should not be advertised by health or config APIs.

## CLI Contract

Breaking v1 `serf-tui` contract:

```sh
serf-tui
```

Starts or connects to local hub, then opens the dashboard.

Supported flags:

```text
--hub-addr string       hub address or URL, default 127.0.0.1:9180
--hub-bin string        serf-hub binary to auto-start for local hubs
--no-auto-start-hub     fail instead of starting a missing local hub
--log-file string       TUI log path
--debug                 show debug panel and verbose client errors
```

Removed from `serf-tui`:

- `--addr`
- `--resume`
- `--resume-last`
- `--list-sessions`
- embedded-session provider/model/runtime flags

Those concepts move to the hub dashboard and spawn/resume flows. If a later CLI convenience is needed, add explicit hub-backed commands rather than reviving embedded mode.

## Error Handling

Startup:

- Hub probe fails and address is local: auto-start hub.
- Auto-start command missing: show binary resolution failure and searched paths.
- Hub starts but health times out: show log path and last error.
- Hub lock race: retry health before surfacing failure.

Dashboard:

- Tree fetch fails: keep last tree and show stale banner.
- No live or past sessions: show spawn prompt.
- Hub capability mismatch: show minimum required version error.

Session:

- Replay transcript missing: show readable error, keep metadata visible.
- Live daemon unavailable: show ended/disconnected state and allow resume if supported.
- Send receives 409: keep text in input and show busy status.
- Send resumes but daemon does not appear: show resume failure and keep text.
- Stream reconnect fails: keep transcript visible and mark disconnected.

## Security

TUI API requests are local process-to-hub calls and do not carry browser `Origin`. Existing same-origin browser guards should not block them.

Remote support will require auth. Do not solve that in the TUI. The TUI should accept whatever authenticated hub client mechanism exists later, likely one of:

- Bearer token in hub config.
- Tailscale identity exposed through hub.
- Local-only unauthenticated mode plus explicit remote authenticated mode.

The TUI must never connect directly to remote daemons.

## Testing Strategy

Unit tests:

- Hub state normalization, including `AWAITING_INPUT`.
- Hub tree DTO generation from live roster plus past metadata.
- Live roster-only sessions appear in Projects before metadata exists.
- Session capability derivation for live, processing, ended, stale, and read-only sessions.
- Hub client URL construction and error handling.
- Ref parsing, validation, and `url.PathEscape` usage.
- Auto-start address normalization for default local, custom local, URL-form local, and remote addresses.
- SSE parser handling for multiline data, optional spaces, final unterminated events, non-200 responses, replay EOF, and `Last-Event-ID`.
- Event reducer behavior for actual hub replay events and live daemon events: user input, assistant deltas, assistant end-with-text, tool calls, `communicate`, task-list, subagents, warnings, and replay completion.
- Dashboard row selection, duplicate live/project rows, expanded projects, focus mode, `tab`, `ctrl+c`, scroll preservation, unread events, and read-only input.
- Guard that normal `serf-tui` code paths no longer call local state/discovery helpers such as `agent.RuntimeDir`, `agent.ListSessions`, `agent.ReadTranscript`, or rendezvous listing.

Integration tests:

- `httptest` hub API against fake roster, fake past index, and fake spawner.
- TUI model opens dashboard, refreshes tree, opens a session, processes replay events, and returns to dashboard.
- Send-to-ended-session calls resume then forwards input.
- Resume uses the past session working directory and state directory.
- Clear returns a new ref and TUI navigates to it.
- Hub auto-start resolver chooses explicit path, sibling binary, then PATH.
- Hub passes run dir, state dir, and auth-relevant environment to spawned and resumed daemons.

Manual verification:

- Build `serf-hub` and `serf-tui`.
- Kill all hubs, run `serf-tui`, verify it starts a detached hub and opens dashboard.
- Start two live sessions, verify both appear in Live and Projects.
- Open a long session after more than the daemon ring-buffer size worth of events, verify full transcript is shown.
- Open an ended session, send input, verify hub resumes it with the same session ID.
- Fork a prior turn and verify tree grouping matches web.
- Exit TUI, verify hub remains healthy.

## Implementation Phases

### Phase 1: Hub Client API

- Extract shared hub DTOs, ref helpers, typed client, and pure state/capability helpers into `internal/hubapi` and optionally `internal/hubmodel`.
- Add `GET /api/health`.
- Add `GET /api/tree`.
- Add `GET /api/sessions/{ref}`.
- Add `GET /api/spawn-schema`.
- Add JSON session action endpoints.
- Add session refs with `host_id=local`.
- Add daemon `/status` action capabilities and hub capability derivation.
- Fix `AWAITING_INPUT` normalization.
- Ensure live roster-only sessions appear in project groups.
- Fix run-dir propagation or remove configurable run-dir from the public contract.
- Add tests for DTOs, tree, and capabilities.

### Phase 2: Durable Session Streams

- Add `GET /api/sessions/{ref}/events`.
- Implement `replay`, `live`, and `transcript-follow` modes.
- Add `_meta` durable event identity on hub API streams.
- Reuse existing replay conversion for persisted transcripts.
- Make replay emit canonical assistant delta events, while tolerating existing end-with-text replay payloads in the TUI reducer.
- Attach live SSE after transcript replay when session is live.
- Add dedupe safeguards at the transcript/live handoff.
- Add stream tests with fake replay and fake live event sources.

### Phase 3: TUI Shell and Dashboard

- Replace old `serf-tui` startup with hub probe and auto-start.
- Add typed hub client.
- Replace the SSE parser.
- Build dashboard model and tree rendering.
- Add polling refresh and selection preservation.
- Add search overlay for live and past metadata.
- Remove embedded server startup and direct daemon address paths.

### Phase 4: Session Drill-in

- Extract per-session state into `sessionView`.
- Move event handling into a testable reducer.
- Render session view from transcript-follow stream.
- Add tasks/details overlays.
- Add reconnect and `Last-Event-ID` support.

### Phase 5: Write Actions

- Implement send, steer, interrupt, compact, clear, model switch, shutdown, fork, and spawn through hub APIs.
- Gate every action on capabilities.
- Resume by resolved session record, not bare session ID.
- Navigate to the returned ref after clear and fork.
- Keep unsent text on action failure.
- Refresh tree after mutating actions.

### Phase 6: Hardening

- Add manual verification scripts or documented commands.
- Add TUI help.
- Add debug panel.
- Remove dead old flags and embedded-only helpers.
- Update README and command docs.

## Alternatives Considered

### Alternative A: Hub-first TUI client

Recommended. TUI speaks JSON/SSE to hub. Hub owns discovery and routing.

Pros:

- Aligns with web UI.
- Enables remote hosts later.
- Avoids duplicating rendezvous and past-index logic.
- Centralizes resume/fork/spawn behavior.

Cons:

- Requires hub JSON API work before most TUI work.
- Requires auto-start lifecycle handling.

### Alternative B: TUI reads local rendezvous and state directly

Rejected. It might be faster for local-only dashboard display, but it creates a second hub inside TUI.

Problems:

- Duplicates live discovery, stale daemon handling, past indexing, and spawn/resume.
- Makes remote support a rewrite.
- Risks behavioral drift from web hub.

### Alternative C: TUI consumes web HTML routes

Rejected. Terminal clients need structured data, not htmx fragments.

Problems:

- Fragile selectors and template coupling.
- Hard to test.
- Forces terminal state to follow browser layout decisions.

## Open Questions

- Should dashboard tree refresh use polling only in v1, or should hub expose a tree-events stream?
- Which old `serf-tui` spawn flags deserve first-class hub spawn fields? The default should be to omit them until someone needs them.

## Acceptance Criteria

- `serf-tui` with no running hub starts a detached local hub and opens a dashboard.
- `serf-tui --hub-addr 127.0.0.1:<custom>` starts the hub on that custom local address, and remote hub addresses are never auto-started.
- `serf-tui` has no embedded server path and no direct daemon `--addr` mode.
- Dashboard lists live and past sessions from hub.
- Drill-in shows full persisted transcript and follows live events for active sessions.
- Drill-in handles actual replay events and live daemon events without losing user or assistant messages.
- Sending to an ended resumable session resumes through hub with the session's recorded working directory and state directory, then preserves session identity.
- Clearing a session returns a new ref and TUI navigates to it.
- Actions are enabled or disabled from capabilities returned by hub.
- TUI stores and navigates by opaque session ref, not bare local session ID.
- Remote-host support can be added by extending hub sources without replacing TUI navigation.
