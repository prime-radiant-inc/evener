# The hub MCP server

The hub MCP is an MCP server that gives an agent the hub as a tool surface:
it can see every session on the hub, start new ones, read what they did,
steer or interrupt them, and wait for their next event — the job a project
manager does for a fleet of working sessions. It is the tool surface for
evener's planned PM session, and it works with any MCP client.

The server lives at `hub-mcp/` in the repository (see its README for the
developer view) and is offered to a session like any other MCP server, via
`.mcp.json`:

```json
{
  "mcpServers": {
    "hub": {
      "type": "stdio",
      "command": "node",
      "args": ["/path/to/evener/checkout/hub-mcp/dist/src/index.js"]
    }
  }
}
```

Put the entry in the global `~/.config/evener/mcp.json` for every session on
the machine, or in a launch config's `mcps` for selected sessions. The
recommended server name is `hub`, so the session sees tools named
`hub__list_sessions`, `hub__start_session`, `hub__send_message`,
`hub__wait_for_activity`, and so on.

Build it first (`make test-hub-mcp` also does this):

```bash
cd hub-mcp && npm run preflight && npm run build
```

## Configuration

The server reads the hub's address and credential from its environment, set
through the same `.mcp.json` entry's `env` map:

| Variable | Meaning | Default |
| --- | --- | --- |
| `EVENER_HUB_RPC_URL` | Hub AppWire `/rpc` WebSocket URL | `ws://127.0.0.1:9180/rpc` |
| `EVENER_HUB_TOKEN` | Hub capability token, verbatim | — |
| `EVENER_HUB_TOKEN_FILE` | A file containing the token | `<state root>/auth-token` |
| `EVENER_HUB_STATE_ROOT` | Hub state root for the default above | `${XDG_STATE_HOME:-$HOME/.local/state}/evener` |

With nothing set, the server reads the hub's own auth token from its default
location, the same file the TUI and scripted clients read, so the
configuration contains no secret material. Configuration problems do not
kill the server: it still answers `tools/list`, and every hub-touching call
returns the actionable configuration error, so an agent inside a session can
see exactly what to fix.

## Trust

The hub token is a full-hub bearer capability — "anyone holding it has full
hub access" (see [Trust boundary](evener-hub.md#trust-boundary)). A session
wired to this MCP can therefore drive every session on the hub: start and
stop sessions, read their transcripts, steer their work. That is exactly the
PM session's job, and on a single-user hub it grants no authority the user
does not already have — but wire it only into sessions you would trust with
the hub itself. Prefer the token-file default over pasting the token into
config files, and never commit a configured token.

## The supervising loop

The tools are designed around how a PM actually works:

1. **Orient.** `hub_overview` for the hub and fleet, `list_sessions` for
   the fleet rows, `search_sessions` to find prior work by content.
2. **Delegate.** `start_session` with a working directory and a full work
   order (a name helps `list_sessions` later). The returned ref is the
   handle for everything else.
3. **Follow.** `wait_for_activity` on the refs you supervise: it returns
   buffered events immediately or blocks until something happens. Pass the
   returned `activity_cursor` back as `since` so you only ever see new
   events. There is no polling loop to write.
4. **Judge and correct.** `get_session` for state and what you may do;
   `read_transcript` for what actually happened; `send_message` to
   respond — it starts a turn on an idle session and steers a busy one
   without your having to check which; `interrupt_session` for a hard
   course correction; `clear_queue` to drop stale instructions.
5. **Close out.** `stop_session` when a worker is done (`force` only for a
   stuck one). Transcripts stay readable after a stop.

Every tool accepts the refs the other tools return, plus a bare session id.

## Testing

- `make test-hub-mcp` — typecheck, lint, and unit tests (scripted hub port,
  no hub process needed).
- `make test-hub-mcp-e2e` — builds an isolated hub plus the fakellm scripted
  provider, then plays the whole supervising loop above through the MCP with
  a raw JSON-RPC client. No provider credentials, no network, no fixed
  ports.

The design decisions, including why the server uses the official MCP
TypeScript SDK and why resources and prompts are out of scope for v1, are in
`docs/superpowers/specs/2026-09-27-hub-mcp-design.md`.
