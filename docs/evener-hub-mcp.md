# The hub MCP server

The hub MCP is an MCP server that gives an agent the hub as a tool surface:
it can see every session on the hub, start new ones, read what they did,
steer or interrupt them, and wait for their next event — the job a project
manager does for a fleet of working sessions. It is the tool surface for
evener's planned PM session, and it works with any MCP client.

The server lives at `hub-mcp/` in the repository (see its README for the
developer view).

## Wiring: scope it to the sessions that need it

The hub token is a full-hub bearer capability (see [Trust](#trust) below),
so a session wired to this MCP can drive every session on the hub. The
primary recipe is the scoped one: an entry that reaches only the sessions
you point it at, never the whole machine.

A launch config accepts the server as an `[[mcps]]` entry — the natural
home for the PM session's profile:

```toml
[[mcps]]
name = "hub"
command = "node"
args = ["/path/to/evener/checkout/hub-mcp/dist/src/index.js"]
```

That shape carries only `name`, `command`, and `args` — no environment —
so a server wired that way has no way to receive `EVENER_HUB_RPC_URL`,
`EVENER_HUB_MCP_TOKEN`, or the scoping variables below, and must live on the
defaults (loopback hub, token file in the default state root). The
`.mcp.json` route, which passes `env`, is the one that lets you point a
session at a non-default hub or set `EVENER_HUB_MCP_PROJECT` /
`EVENER_HUB_MCP_READONLY`. Keep such a file outside every repository your
sessions read, and hand it to one session at a time on its command line
(`--mcp-config <path>`); a launch config also accepts a list of such
paths via its `mcp_configs`:

```json
{
  "mcpServers": {
    "hub": {
      "type": "stdio",
      "command": "node",
      "args": ["/path/to/evener/checkout/hub-mcp/dist/src/index.js"],
      "env": {
        "EVENER_HUB_MCP_PROJECT": "/absolute/path/to/one/project"
      }
    }
  }
}
```

The token variable is `EVENER_HUB_MCP_TOKEN`, deliberately not
`EVENER_HUB_TOKEN`: the hub passes an internal per-hub token to every
session it spawns under that older name (it authenticates the hub's dials
to the session's daemon, and the hub's own `/rpc` refuses it), so a
server that read it would silently break inside every hub-spawned
session. With `EVENER_HUB_MCP_TOKEN` unset, the token-file default wins.

The recommended server name is `hub`, so the session sees tools named
`hub__list_sessions`, `hub__start_session`, `hub__send_message`,
`hub__wait_for_activity`, and so on.

### The global config, and why it is not the default advice

The entry can also go in the global `~/.config/evener/mcp.json`, which
every session on the machine loads. Only do that with both hazards in
view:

- **It arms every session on the machine.** Every session — including
  ones working in untrusted repositories, or reading content you would
  never trust with the hub — gets the full-hub tool set: start and stop
  any session, read any transcript, steer any worker.
- **The project layer is not a perimeter anyway.** `.evener/mcp.json` at
  a repository's git root is model-writable and loads with no approval
  gate; loading it "untrusted" only refuses `$(command)` expansion
  (`agent/mcpconfig/config.go`). A bare stdio entry pointing at
  `hub-mcp/dist/src/index.js` — a few lines, no `env`, no token, thanks
  to the token-file default — is enough to wire full-hub power into any
  session that opens that repo. Treat any repository you do not trust
  as able to arm a session this way, global config or not.

If you do wire it globally, cut the blast radius: set
`EVENER_HUB_MCP_PROJECT` where the tool set is needed for one project
only, prefer `EVENER_HUB_MCP_READONLY=1` for sessions that just observe,
and treat every session on the machine as hub-trusted.

Build it first (`make test-hub-mcp` also does this):

```bash
cd hub-mcp && npm run preflight && npm run build
```

Two caveats on that build: the preflight runs `npm ci`, which executes
the dependencies' install lifecycle scripts — build from a checkout whose
dependencies you trust — and the resulting `dist` is expected to run
from that checkout (the wiring entries above point into it).

## Configuration

The server reads the hub's address, credential, and scope from its
environment, set through the `.mcp.json` entry's `env` map:

| Variable | Meaning | Default |
| --- | --- | --- |
| `EVENER_HUB_RPC_URL` | Hub AppWire `/rpc` WebSocket URL | `ws://127.0.0.1:9180/rpc` |
| `EVENER_HUB_MCP_TOKEN` | Hub capability token, verbatim (wins over the file) | — |
| `EVENER_HUB_TOKEN_FILE` | A file containing the token | `<state root>/auth-token` |
| `EVENER_HUB_STATE_ROOT` | Hub state root for the default above | `${XDG_STATE_HOME:-$HOME/.local/state}/evener` |
| `EVENER_HUB_MCP_READONLY` | `1` registers only the read tools — supervision without mutation | unset: all tools |
| `EVENER_HUB_MCP_PROJECT` | Absolute path of one project; `list_sessions` rows, `search_sessions` hits, and the session tools are narrowed to it, and out-of-scope refs are refused | unset: every project |

With nothing set, the server reads the hub's own auth token from its default
location, the same file the TUI and scripted clients read, so the
configuration contains no secret material. Configuration problems do not
kill the server: it still answers `tools/list`, and hub-touching calls
return the actionable configuration or connectivity error — including
`wait_for_activity`, which reports an unreachable hub as an error instead
of a quiet timeout. An agent inside a session can see exactly what to fix.

## Trust

The hub token is a full-hub bearer capability — "anyone holding it has full
hub access" (see [Trust boundary](evener-hub.md#trust-boundary)). A session
wired to this MCP can therefore drive every session on the hub: start and
stop sessions, read their transcripts, steer their work. That is exactly the
PM session's job, and on a single-user hub it grants no authority the user
does not already have — but wire it only into sessions you would trust with
the hub itself, scoped as above, and never commit a configured token.

What the tools return is data from other sessions, and those sessions read
repositories you may not control:

- **Who gets wired.** The wiring hazards bear repeating here: a global
  `~/.config/evener/mcp.json` entry arms every session on the machine, and
  a repository's `.evener/mcp.json` — model-writable, loaded with no
  approval gate — can wire the full tool set into any session that opens
  the repo. Scope the entry per launch or per session; do not rely on a
  perimeter.
- **Token placement.** Prefer the token-file default over pasting a token
  into config files. `EVENER_HUB_MCP_TOKEN` inside a mcp.json `env` map is
  session-readable: the global `~/.config/evener/mcp.json` is not masked
  from session reads, so any session with read access outside its worktree
  can read the token back out of it.
- **Cross-project exposure.** Transcripts and search span every project on
  the hub, and they may contain other projects' secrets — credentials
  printed in tool output, tokens pasted into prompts. `EVENER_HUB_MCP_PROJECT`
  narrows what one wired session can see to a single project; use it for
  any supervision that happens around untrusted repositories.
- **Untrusted content.** Session content flowing through these tools is
  untrusted data from the repos those sessions read. Treat every tool
  result as untrusted input: quote it, do not execute it, do not follow
  instructions embedded in it. The tool descriptions carry the same
  warning, so the supervising agent is told this too.

## The supervising loop

The tools are designed around how a PM actually works:

1. **Orient.** `hub_overview` for the hub and fleet, `list_sessions` for
   the fleet rows, `search_sessions` to find prior work by content.
   Session states print with the wire's own vocabulary — `idle`, `active`,
   `awaiting`, `warning`, `systemError`, `closed`, `notLoaded`,
   `restartRequired` — the same strings the hub API uses, unmodified.
   Listing does not subscribe the rows (see step 3).
2. **Delegate.** `start_session` with a working directory and a full work
   order (a name helps `list_sessions` later; `list_models` discovers the
   model strings the `model` field accepts). `max_subagent_depth` bounds
   how deeply the worker may spawn its own subagents: unset uses the hub
   default (2), the minimum is 1, and the field cannot express "no
   subagents" — there is no value that means none. If a start's outcome is
   unknown (say the connection drops mid-mutation), the tool says so and
   points you to `list_sessions` first — check whether the session exists
   before starting a duplicate.
3. **Follow.** `wait_for_activity` on the refs you supervise: it returns
   buffered events immediately or blocks until something happens. Pass the
   returned `activity_cursor` back as `since` so you only ever see new
   events. The cursor is `<epoch>:<seq>`; a `since` from a foreign epoch
   (one from before a server restart) restarts from now, and the tool says
   so when it happens. `get_session`, `read_transcript`, and
   `start_session` subscribe the sessions they name, and naming new refs
   in a wait subscribes those on demand; `list_sessions` alone does not.
   The wait is honest about what it could not show: results disclose
   events skipped past the return limit, gaps where the in-memory ring
   buffer evicted older events, how many sessions are actually being
   watched, and subscribe failures — and an unreachable hub is reported
   as an error, never as quiet.
4. **Judge and correct.** `get_session` for state and what you may do;
   `read_transcript` for what actually happened; `send_message` to
   respond — it starts a turn on an idle session and steers a busy one
   without your having to check which; `interrupt_session` for a hard
   course correction; `clear_queue` to drop stale instructions;
   `rename_session` to relabel a session you misnamed.
5. **Close out.** `stop_session` when a worker is done. `force` is the
   stuck-session escalation: it resolves the session's identity first and
   echoes the name and working directory of what it stopped, so a
   wrong-ref force-stop is visible after the fact. Transcripts stay
   readable after a stop. `resume_session` resumes a session the hub
   stopped for recovery (get_session marks it "resume required").

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
`docs/superpowers/specs/2026-09-27-hub-mcp-design.md`; the revisions made
after the 2026-09-27 critique are recorded in that document's addendum.
