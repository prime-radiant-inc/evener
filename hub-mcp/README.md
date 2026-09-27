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
| `list_sessions` | The fleet at a glance: ref, state, name, project, model, last activity; filters by state or search term. |
| `get_session` | One session in depth, including what you may currently do to it (send, steer, queue, interrupt, stop…). |
| `read_transcript` | What a session did, as readable text, newest turns first, with cursors to page back. |
| `list_tasks` | A session's task list with statuses and dependencies. |
| `search_sessions` | Find sessions by content, live and past. |
| `start_session` | Delegate work: cwd + a work order (+ name, model, effort, subagent depth). |
| `send_message` | The state-machine-aware send: starts a turn when idle, steers mid-turn, queues on demand. |
| `interrupt_session` | Stop the running turn, keep the session. |
| `stop_session` | Graceful stop, with `force` escalation for a stuck session. |
| `clear_queue` | Drop queued-but-not-running input. |
| `wait_for_activity` | Block until the watched sessions produce events (turns, status, tasks, jobs, delegates, attention, errors) or a timeout passes; cursor-based so nothing is seen twice. |

Every session a tool touches is subscribed for activity automatically, so
`wait_for_activity` works on anything the agent has already seen.

## Configuration

The server reads its environment (set these in the `.mcp.json` entry's `env`):

| Variable | Meaning | Default |
| --- | --- | --- |
| `EVENER_HUB_RPC_URL` | Hub AppWire `/rpc` WebSocket URL | `ws://127.0.0.1:9180/rpc` |
| `EVENER_HUB_TOKEN` | Hub capability token (wins over the file) | — |
| `EVENER_HUB_TOKEN_FILE` | Token file to read | `<state root>/auth-token` |
| `EVENER_HUB_STATE_ROOT` | Hub state root (for the token-file default) | `${XDG_STATE_HOME:-$HOME/.local/state}/evener` |

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
can drive every session on the hub. That is the PM session's job — but see
`docs/evener-hub-mcp.md` before wiring it into untrusted sessions.

## Build and test

```sh
npm run preflight   # repair node_modules (from the lockfile) and build the SDK dist
npm run build       # tsc
npm test            # preflight + tsc + biome + node:test
```

The preflight refuses a symlinked `node_modules` rather than installing
through it (the hazard AGENTS.md calls out for every npm install in this
repo).

End to end (real isolated hub, fakellm-driven sessions, a raw JSON-RPC MCP
client playing the PM through the full workflow):

```sh
make test-hub-mcp-e2e
```

## Layout

- `src/config.ts` — environment and token resolution.
- `src/hub.ts` — the hub port seam; the lazily-connected AppWire adapter with
  Bearer-authenticated `ws` sockets.
- `src/events.ts` — notification classification, subscriptions, the event
  ring buffer, and the blocking wait.
- `src/sessions.ts`, `src/transcript.ts`, `src/render.ts` — the readable-text
  renderers.
- `src/tools.ts` — the twelve tools: descriptions, schemas, handlers.
- `src/index.ts` — MCP server assembly over stdio.
- `scripts/mcp-client.mjs` — a dependency-free raw MCP client for tests.
- `scripts/e2e/pm-workflow.mjs` — the end-to-end PM scenario.
