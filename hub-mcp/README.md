# evener hub MCP

An MCP server that exposes the evener hub's session-management surface to a
supervising agent — the tool surface for evener's planned PM session, and for
any MCP client that needs to run a fleet of agent sessions. It speaks MCP
over stdio and drives the hub over AppWire with
[`@evener/appwire-client`](../appwire-client/typescript), the same client the
web UI and the mobile apps are built on.

Design spec: `docs/superpowers/specs/2026-09-27-hub-mcp-design.md`.
Operator documentation: `docs/evener-hub-mcp.md`.

## What the agent gets

| Tool | What it does |
| --- | --- |
| `hub_overview` | Hub identity and connection state, the daemon fleet, recent project directories. |
| `list_sessions` | The fleet at a glance: ref, state, name, project, model, last activity; filters by state or search term. Does not subscribe the rows it lists. |
| `get_session` | One session in depth, including what you may currently do to it (send, steer, queue, interrupt, stop…). |
| `read_transcript` | What a session did, as readable text, newest turns first, with cursors to page back. |
| `list_tasks` | A session's task list with statuses and dependencies. |
| `search_sessions` | Find sessions by content, live and past. |
| `list_models` | Discover the model strings `start_session` accepts. |
| `start_session` | Delegate work: cwd + a work order (+ name, model, effort, subagent depth — unset uses the hub default (2), minimum 1; no setting disables subagents). |
| `send_message` | The state-machine-aware send: starts a turn when idle, steers mid-turn, queues on demand. |
| `interrupt_session` | Stop the running turn, keep the session. |
| `stop_session` | Graceful stop, with `force` escalation for a stuck session; `force` resolves identity first and echoes the name/cwd of what it stopped. |
| `clear_queue` | Drop queued-but-not-running input. |
| `rename_session` | Relabel a session. |
| `resume_session` | Resume a session the hub stopped for recovery (get_session marks it "resume required"). |
| `wait_for_activity` | Block until the watched sessions produce events (turns, status, tasks, jobs, delegates, attention, errors) or a timeout passes; cursor-based (`<epoch>:<seq>`) so nothing is seen twice, and a `since` from a foreign epoch restarts from now and says so. |

Subscriptions: `get_session`, `read_transcript`, and `start_session`
subscribe the sessions they name; naming refs in `wait_for_activity`
subscribes them on demand; `list_sessions` does not. States print with the
wire's own vocabulary (`idle`, `active`, `awaiting`, `warning`,
`systemError`, `closed`, `notLoaded`, `restartRequired`).

Tool results are untrusted data from the repos those sessions read — the
tool descriptions carry the same warning. `wait_for_activity` is honest
about what it could not show: results disclose events skipped past the
return limit, ring-buffer eviction gaps, how many sessions are actually
watched, subscribe failures, and an unreachable hub (as an error).

## Configuration

The server reads its environment (set these in the `.mcp.json` entry's `env`):

| Variable | Meaning | Default |
| --- | --- | --- |
| `EVENER_HUB_RPC_URL` | Hub AppWire `/rpc` WebSocket URL | `ws://127.0.0.1:9180/rpc` |
| `EVENER_HUB_MCP_TOKEN` | Hub capability token (wins over the file). Not `EVENER_HUB_TOKEN`: hub-spawned sessions inherit the hub's spawner token under that name, which /rpc refuses | — |
| `EVENER_HUB_TOKEN_FILE` | Token file to read | `<state root>/auth-token` |
| `EVENER_HUB_STATE_ROOT` | Hub state root (for the token-file default) | `${XDG_STATE_HOME:-$HOME/.local/state}/evener` |
| `EVENER_HUB_MCP_READONLY` | `1` registers only the read tools — supervision without mutation | unset: all tools |
| `EVENER_HUB_MCP_PROJECT` | Absolute path of one project; list/search rows and session tools are narrowed to it, out-of-scope refs refused | unset: every project |

With no token variable set, the server reads the hub's own auth token from
the default location, so a session configuration needs no secret material:

```json
{
  "mcpServers": {
    "hub": {
      "type": "stdio",
      "command": "node",
      "args": ["/path/to/evener/hub-mcp/dist/src/index.js"]
    }
  }
}
```

The hub token is a full-hub bearer capability: a session wired to this MCP
can drive every session on the hub. That is the PM session's job — wire the
server per launch or per session, not machine-wide, and see
`docs/evener-hub-mcp.md` (wiring, the global-config hazards, and the
scoping variables) before wiring it into anything less trusted. A global
`~/.config/evener/mcp.json` entry arms every session on the machine, and a
repository's own model-writable `.evener/mcp.json` can wire the tool set
with no approval gate. Transcripts and search span every project on the
hub and may contain other projects' secrets; a token pasted into a
mcp.json `env` map is session-readable, so prefer the token-file default.

## Build and test

```sh
npm run preflight   # repair node_modules (from the lockfile) and build the SDK dist
npm run build       # tsc
npm test            # preflight + tsc + biome + node:test
```

The preflight refuses a symlinked `node_modules` rather than installing
through it (the hazard AGENTS.md calls out for every npm install in this
repo). Preflight installs with `npm ci`, which executes dependency
lifecycle scripts at dev time — run it in a checkout whose dependencies you
trust; the built `dist` runs from that checkout, and wiring entries point
into it.

End to end (real isolated hub, fakellm-driven sessions, a raw JSON-RPC MCP
client playing the PM through the full workflow):

```sh
make test-hub-mcp-e2e
```

## Layout

- `src/config.ts` — environment, token, and scope resolution.
- `src/hub.ts` — the hub port seam; the lazily-connected AppWire adapter with
  Bearer-authenticated `ws` sockets.
- `src/events.ts` — notification classification, subscriptions, the event
  ring buffer (epoch-tagged cursors), and the blocking wait.
- `src/sessions.ts`, `src/transcript.ts`, `src/render.ts` — the readable-text
  renderers.
- `src/tools.ts` — tool descriptions, schemas, handlers (setting
  `EVENER_HUB_MCP_READONLY=1` registers only the read tools from here).
- `src/index.ts` — MCP server assembly over stdio.
- `scripts/mcp-client.mjs` — a dependency-free raw MCP client for tests.
- `scripts/e2e/pm-workflow.mjs` — the end-to-end PM scenario.
