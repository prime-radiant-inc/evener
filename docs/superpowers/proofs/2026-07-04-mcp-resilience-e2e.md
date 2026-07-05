# MCP resilience: three-server end-to-end scenario card

**What this covers**: the whole MCP-resilience workstream (Tasks 1-19) as it
actually behaves in an assembled, running `serf serve` daemon driven through
the real web UI, the real TUI, and the daemon's own HTTP surface — not unit
tests. Exercises: non-fatal parallel startup with per-server outcomes (Tasks
2-4), the `SourceMCP` deferred-warning path flushed after `SESSION_START`
(Tasks 9-10), Channel-B errors routed through the error path so they render
red without demoting server health (Tasks 1, 13, Decision 3), the live
`Status`/`Error` carrier chain to wire/TUI (Tasks 13-15), the settings-pane
`mcpprobe` (Tasks 17-18), and lazy call-driven reconnect with zero-init
backoff and a rendered diagnostic (Task 8). Run against worktree HEAD
`9249a06c7e936837a79920629f3dcf0e46cecb8b`.

Per J3 (one server cannot be both startup-failed and rendering-red), the card
uses three servers:

- **A** — healthy stdio echo server (`agent/testdata/intgmcpserver`).
- **B** (`deadsvc`) — inline `deadsvc:$(command -v true)`: a real, on-PATH
  binary that exits immediately without speaking MCP, guaranteeing a
  connect-stage failure.
- **C** — a temporary, uncommitted stdio server (built only for this run, see
  below) whose one tool always returns `CallToolResult{IsError: true}`
  (Channel B), modeling a live upstream-erroring server.

## Pre-state

Build the three harness binaries from the worktree root:

```sh
go build -o /tmp/serf ./cmd/serf
go build -o /tmp/serf-hub ./cmd/serf-hub
go build -o /tmp/serf-tui ./cmd/serf-tui
go build -o /tmp/intgmcpserver ./agent/testdata/intgmcpserver
```

Server C has no repo fixture (the task's own file list adds only this proof
doc, a strong signal C should be a scratch artifact, not a permanent test
fixture). It was written to a temporary `agent/testdata/mcperrsvc/main.go`
(same testdata-build trick as `intgmcpserver`, needed only so `go build` could
resolve the `agent` module's pinned `github.com/modelcontextprotocol/go-sdk`
dependency), built to `/tmp/serf-mcp-e2e-errsvc`, then **deleted from the
worktree** — it was never `git add`ed. Its logic: one tool, `alwaysfails`,
unconditionally returns
`&mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "channelb-boom: upstream 400 (server C proof fixture)"}}}`.

### Live-model credential note (supersedes the plan text's `.env`/`oai-work` instructions)

`llm.NewFromAvailableProviders` (`llm/providers_config.go`) now tolerates
partial provider availability (`allowPartial=true`): a missing credential for
one configured instance is collected as a warning and that instance is
skipped, it does not abort client construction. No `.env` sourcing and no
`oai-work` instance were needed. Every model call in this run used
`--model openai/gpt-5.5`, routed through the `openai` instance in
`~/.serf/providers.toml` backed by the real ChatGPT/Codex OAuth token at
`~/.local/state/serf/auth/openai.json` — a genuine, non-mocked API path.

### Isolation recipe

Ran a real, isolated `serf serve` + `serf-hub` pair — never the main checkout,
never Jesse's real `~/.serf`. Both processes ran under a fake HOME
(`$SCRATCH/hubhome`) so the hub's `hostlock` (`~/.serf/hub.lock`, hard-coded to
`os.UserHomeDir()` with no config override) and default paths never touched
the real environment, plus non-default ports (`serve` on `127.0.0.1:29131`,
`hub` on `127.0.0.1:29180` — the conventional `9131`/`9180` were unavailable
in this environment: another agent's unrelated `serf-hub-sr`/`serf-sr` was
already running on `9573`; that process, and the browser tab pointed at it,
were never touched).

```sh
SCRATCH=<scratchpad>/mcp-e2e
mkdir -p "$SCRATCH"/{workdir,hubhome/.config/serf,hubhome/.local/state/serf/auth}

# Global MCP config the HUB's settings-pane probe reads (see "Sharp edges" —
# this is a *separate* discovery path from the live session's --mcp flags).
cat > "$SCRATCH/hubhome/.config/serf/mcp.json" <<'EOF'
{"mcpServers": {
  "A": {"command": "/tmp/intgmcpserver"},
  "deadsvc": {"command": "/usr/bin/true"},
  "C": {"command": "/tmp/serf-mcp-e2e-errsvc"}
}}
EOF

# Real, read-only credential reference — never copied, never mutated.
ln -sf ~/.local/state/serf/auth/openai.json \
  "$SCRATCH/hubhome/.local/state/serf/auth/openai.json"

XDG_STATE_HOME="$SCRATCH/hubhome/.local/state" /tmp/serf serve \
  --addr 127.0.0.1:29131 --model openai/gpt-5.5 \
  --dir "$SCRATCH/workdir" \
  --run-dir "$SCRATCH/hubhome/.serf/run" \
  --verbose \
  --mcp "A:/tmp/intgmcpserver" \
  --mcp "deadsvc:$(command -v true)" \
  --mcp "C:/tmp/serf-mcp-e2e-errsvc" &

HOME="$SCRATCH/hubhome" /tmp/serf-hub --addr 127.0.0.1:29180 --serf /tmp/serf &
```

`--state-dir` was deliberately **not** passed to `serve` — see "Sharp edges"
for why an explicit `--state-dir` silently breaks hub/session association,
and why `XDG_STATE_HOME` (not `SERF_STATE_DIR`) is the credential-scoped knob.

## Assertions, evidence, and verdicts

### Server A — healthy stdio echo server

| Assertion | Verdict |
|---|---|
| Session constructs | HOLDS |
| A's tool is callable | HOLDS |
| Settings probe `available` | HOLDS |
| `Servers()`/wire `connected` | HOLDS |

`GET /status` on the live daemon (the authoritative record — see Task 13/14)
immediately after startup:

```json
{"name": "A", "tools": ["A__echo"], "status": "connected"}
```

Drove a real turn through the daemon's own `/input` endpoint (`POST
{"text":"Call the A__echo tool with argument message=\"PROOF_A_12345\"..."}`)
and captured the `--verbose` NDJSON event stream:

```json
{"kind":"TOOL_CALL_START","data":{"tool_name":"A__echo","arguments_json":"{\"message\":\"PROOF_A_12345\",...}"}}
{"kind":"TOOL_CALL_OUTPUT_DELTA","data":{"tool_name":"A__echo","delta":"echo: PROOF_A_12345"}}
{"kind":"TOOL_CALL_END","data":{"tool_name":"A__echo","output":"echo: PROOF_A_12345"}}
```

Web UI (`http://127.0.0.1:29180/s/<session-id>`) rendered the turn in plain
text, no error marker: `echo: PROOF_A_12345`. TUI (`serf-tui --hub-addr
127.0.0.1:29180 ...`) rendered the same turn with a green `✓` and an `ok`
right-aligned label:

```
▍ ✓ A  echo PROOF_A_12345 User explicitly requested calling A__ech… ··· ok  <1ms
    echo: PROOF_A_12345
```

### Server B (`deadsvc`) — startup-failed

| Assertion | Verdict |
|---|---|
| Warning emitted after `SESSION_START` with `Source=mcp` | HOLDS |
| No B tool registered | HOLDS |
| B stays terminal (no reconnect trigger) | HOLDS |
| Settings probe `unreachable`/`missing` | **CORRECTED** — see below |

The startup warning arrived immediately, in the correct order relative to
`SESSION_START`, before any input was sent:

```json
{"kind":"SESSION_START","data":{"profile":"openai","model":"gpt-5.5",...}}
{"kind":"WARNING","data":{"message":"MCP server \"deadsvc\" failed to connect: calling \"initialize\": EOF","source":"mcp","title":"MCP server unavailable","hint":"An MCP server failed to connect, authenticate, or complete a tool call. ..."}}
```

`GET /status` confirms no tool was registered for B and its failure is
recorded:

```json
{"name": "deadsvc", "tools": [], "status": "failed", "error": "calling \"initialize\": EOF"}
```

**"No reconnect trigger" is a negative assertion, proved by absence**: B's
row above was captured at four separate checkpoints across the run (right
after startup, after A's first call, after C's error call, and after A's
kill+reconnect) and was **byte-for-byte identical every time** — `status:
"failed"`, same `error` string, `tools: []`. Since B contributed zero
callable tools, no code path could ever invoke it, so the reconnect logic
(which only fires from a live conn's exec closure) never has an opportunity
to run for B. The TUI's `/details` panel corroborates from the tool-inventory
side: the `Tools (26)` breakdown lists `MCP [A]: A__echo` and `MCP [C]:
C__alwaysfails` but no `MCP [deadsvc]` line at all.

**Corrected assertion — settings probe**: the card originally expected
`unreachable`/`missing`. Driving the actual settings pane
(`http://127.0.0.1:29180/settings/mcp`) showed all three servers, including
`deadsvc`, as `available`:

```
Discovered servers                                    3 entries
Reachability, as probed from the hub.

A — stdio available
C — stdio available
deadsvc — stdio available
```

This is **not a defect** — it is the exact, documented limit stated in
`agent/mcpprobe/mcpprobe.go`'s package doc: for stdio transports, "available"
means only that `exec.LookPath(cfg.Command)` succeeded; the probe never spawns
or connects to the process. `/usr/bin/true` genuinely exists on PATH — the
same property that made it a good choice for B's live-session connect-stage
failure (it must be a real, executable command, or the daemon couldn't even
`exec.Command` it) is exactly what defeats the stdio probe's command-presence
check. There is no way for a `deadsvc:$(command -v true)`-style stdio B to
read `missing` (the command exists) or `unreachable` (that status is only
reachable for http/sse transports); the task text's prediction was wrong, not
the shipped code. This is precisely the "daemon-env divergence" Task 17's doc
comment warns about: the live session's `Servers()`/wire status (`failed`,
real `Error` string) is the honest surface for B; the settings-pane probe is
explicitly a shallower, command-presence-only signal for stdio. Server C
exhibits the same divergence for the complementary reason (see below).

### Server C — connects, but every `tools/call` returns Channel-B `IsError:true`

| Assertion | Verdict |
|---|---|
| Tool result renders red in web UI | HOLDS |
| Tool result renders red (error) in TUI | HOLDS |
| `Servers()` shows C `connected` with `Error` populated | HOLDS |
| Status stays `connected` (Decision 3, no demotion) | HOLDS |

Drove a turn calling `C__alwaysfails`. The wire event carries the failure
through the **error** field, not `output` (Task 1's fix — Channel B reaches
the model as an error-typed `tool_result`):

```json
{"kind":"TOOL_CALL_END","data":{"tool_name":"C__alwaysfails","error":"[MCP Error] channelb-boom: upstream 400 (server C proof fixture)"}}
```

Web UI rendered a red **×** error marker on the tool-call line, followed by
the error body in the transcript:

```
✕  User explicitly requested calling C_alwaysfails with this exact message and returning the raw result/error.
[MCP Error] channelb-boom: upstream 400 (server C proof fixture)
```

TUI rendered the same call with a red `✕` marker and an `error` label (vs.
A's green `✓`/`ok`):

```
▍ ✕ C  alwaysfails PROOF_C_67890 User explicitly requested calling C__alw… ··· error  <1ms
    [MCP Error] channelb-boom: upstream 400 (server C proof fixture)
```

`GET /status` immediately after this call — status is **still `connected`**,
not demoted, with `Error` populated (Decision 3, J4):

```json
{"name": "C", "tools": ["C__alwaysfails"], "status": "connected", "error": "[MCP Error] channelb-boom: upstream 400 (server C proof fixture)"}
```

The TUI's `/details` panel (Task 15's exact per-server rendering) shows all
three servers on one screen — the clearest single artifact for the whole
Status/Error carrier chain:

```
MCP Servers (3):
    A (1 tools) — connected — last error: connection closed: calling "tools/call": client...
    deadsvc (0 tools) — failed — last error: calling "initialize": EOF
    C (1 tools) — connected — last error: [MCP Error] channelb-boom: upstream 400 (server...
```

(A's "last error" here is the transport-closed failure from the reconnect
sub-scenario below, correctly still showing — see "Sharp edges" on the
sticky `Error` field.)

### Reconnect — kill Server A's process, call again, expect immediate transparent recovery

| Assertion | Verdict |
|---|---|
| Reconnects immediately (zero-init backoff) | HOLDS |
| Retried call succeeds | HOLDS |
| Reconnect diagnostic line renders | HOLDS |

After A's first successful call, found and killed its child process:

```
$ ps aux | grep intgmcpserver
jesse  39451  ... /tmp/intgmcpserver
$ kill -9 39451
```

`GET /status` immediately after the kill still showed A as `connected` — the
Manager has no health poller, detection is purely call-driven/lazy, so
nothing changes until the next call is attempted:

```json
{"name": "A", "tools": ["A__echo"], "status": "connected"}
```

Issued the next call to `A__echo` (message `PROOF_A_RECONNECT_99`). The event
stream shows the reconnect warning firing **between** `TOOL_CALL_START` and
`TOOL_CALL_END` for the *same* call boundary — confirming Task 8's "retry
once, transparently, inside the exec closure" design; the caller only ever
sees one tool-call boundary with a successful outcome:

```json
{"kind":"TOOL_CALL_START","data":{"tool_name":"A__echo","arguments_json":"{\"message\":\"PROOF_A_RECONNECT_99\",...}"}}
{"kind":"WARNING","data":{"message":"MCP server \"A\" reconnected after a dropped connection","source":"mcp","title":"MCP server reconnected",...}}
{"kind":"TOOL_CALL_OUTPUT_DELTA","data":{"tool_name":"A__echo","delta":"echo: PROOF_A_RECONNECT_99"}}
{"kind":"TOOL_CALL_END","data":{"tool_name":"A__echo","output":"echo: PROOF_A_RECONNECT_99"}}
```

Total round time (`ROUND_TIMINGS`) was ~2.94s, essentially all LLM latency
(`llm_call_ns=2.92s`) — no observable added delay from the redial, consistent
with zero-init backoff on a server's first-ever failure. A fresh
`intgmcpserver` process (a new PID, `41845`, distinct from the killed
`39451`) was confirmed running afterward, proving the Manager actually
re-executed the command rather than reusing dead state.

The reconnect diagnostic **rendered live** in the TUI, inline in the
transcript right after the tool-call row (captured via a second, independent
kill+recall cycle while actively watching the TUI in real time — the TUI was
already attached, so this catches the same live event a still-open client
would see):

```
▍ ✓ A  echo LIVE_WATCH_1 User requested calling A__echo with this… ··· ok  <1ms
    echo: LIVE_WATCH_1

MCP server reconnected: MCP server "A" reconnected after a dropped connection
```

Final `GET /status` confirms recovery to `connected`:

```json
{"name": "A", "tools": ["A__echo"], "status": "connected", "error": "connection closed: calling \"tools/call\": client is closing: EOF"}
```

### Additional observation (not one of the card's assertions, not a fix made here)

The reconnect `WARNING`'s `hint` field is the same generic, failure-flavored
sentence used for the startup-failure warning ("An MCP server failed to
connect, authenticate, or complete a tool call. Check the command is on PATH
...") even though the `Title` the caller set is "MCP server reconnected" — a
recovery, not a failure. The classifier evidently derives `Hint` from
`Source` alone (`defaultForSource(SourceMCP)`) regardless of the caller's
Title/severity, so a positive notification inherits failure-oriented
phrasing. This is cosmetic (the notification is still correctly labeled,
correctly sourced, and correctly timed) and out of Task 20's scope to fix
(it would mean changing `agent/internal/diagnostic`'s classifier, not
proving the MCP-resilience behavior) — noted here only so it isn't lost.

## Cleanup

`serf serve` was killed first (SIGTERM); this closed stdin to its stdio MCP
children and both `intgmcpserver` and the Server C fixture exited on their
own — confirmed via `ps` immediately after, no orphaned processes. `serf-hub`
and `serf-tui` were killed next. The temporary `agent/testdata/mcperrsvc/`
directory was deleted (never staged). The unrelated, pre-existing
`serf-hub-sr`/`serf-sr` process on port 9573 (another agent's concurrent,
unrelated work) and its browser tab were never touched. All scratch state
lived under a scratchpad tmpdir; nothing was written to `~/.serf`,
`~/.config/serf`, `~/.local/state/serf`, or any other real-environment path
(the credential symlink is the sole read, never a write, of real-environment
state). `git status` on the worktree is clean before and after.

## Sharp edges

- **`--state-dir` vs `XDG_STATE_HOME` vs `SERF_STATE_DIR` are three different
  knobs.** `serf serve --state-dir X` only sets the *session's own*
  persistence path (meta/transcript) and the API-log path; it does **not**
  affect where `providers.toml`/`credentials.toml` are read from
  (`cmdutil.DefaultStateRoot()` reads the `SERF_STATE_DIR` **env var**
  directly, independent of the `--state-dir` flag), nor where the OpenAI OAuth
  token is read from (`auth/openai.DefaultStateDirWithStateHome` reads the
  `XDG_STATE_HOME` **env var**, and only when no `EnvOption` overrides it —
  `serve.go` never passes a `WithStateHome`, so `--state-dir` never reaches
  it either). First attempt at this card passed `--state-dir` explicitly and
  the session ran fine, but the hub's sidebar showed "No sessions yet"
  forever: the hub's past-index glob (`$XDG_STATE_HOME-or-HOME/.local/state/
  serf/projects/*`) never matched the custom `--state-dir` path, so the live
  roster entry had no past-index row to join against. Fix: don't pass
  `--state-dir` at all; instead set `XDG_STATE_HOME` for the `serve` process
  to the same root the hub's fake `HOME` implies, so `serve`'s **default**
  project-state dir (`cmdutil.DefaultProjectStateDir`) lands exactly where the
  hub's glob looks. Symlink the real credential file into that new
  `XDG_STATE_HOME` location once this is done.
- **The browser used for this session is a shared, persistent Chrome
  instance** — an unrelated concurrent agent process was already running its
  own `serf-hub-sr`/`serf-sr` in a tab (port 9573) before this task started.
  Tab identity was not stable across tool calls: several `navigate`/`extract`/
  `screenshot` calls failed with "No target with given id found" or silently
  landed on the *other* task's tab/session. Recovery: always open a dedicated
  `new_tab`, re-verify via `list_tabs` immediately before any action that
  isn't `navigate`, and always route through `/auth?token=...&next=<path>`
  rather than relying on cookie persistence across separate navigations
  (bare `/` reloads sometimes came back "Unauthorized" even though a curl
  request with the same cookie jar succeeded every time — the auth/cookie
  mechanism itself is fine, verified independently via curl; something about
  the shared browser's tab/cookie state specifically was unreliable).
  Deliberately never touched the other task's tab or process.
- **Reconnect/startup warnings render live, not retroactively.** Loading the
  web session page or attaching the TUI *after* the SESSION_START warning or
  the reconnect warning had already fired showed a clean transcript with no
  visible diagnostic line for either — the first attempt at capturing the
  reconnect line this way came up empty and looked like a possible defect.
  It is not: watching an *already-attached* TUI through a second, independent
  kill+recall cycle showed the "MCP server reconnected: ..." line render
  inline, live, immediately after the tool-call row. Diagnostics of this kind
  are confirmed delivered on the authoritative event stream regardless (every
  `WARNING` event in this proof came straight from `--verbose` NDJSON); a
  reviewer wanting to see the *rendering* specifically must be watching (or
  have the transcript loaded) at the moment the event fires, not reload
  afterward.
- **The `Error` field is sticky, not cleared by a later success.** After A's
  successful reconnect+retry, `Servers()`/`/status` continued reporting A's
  *prior* transport-closed error string even though the very next call
  succeeded. This matches Task 13's design ("last failure of any kind") —
  `Error` is a last-failure-with-age field for the health surface, not a
  live-call-outcome field, and a reviewer should not read a non-empty `Error`
  on a `connected` server as "currently broken" without also checking
  `Status`.
- **`serf-hub`'s host lock is unconditionally keyed on real `$HOME`**
  (`cmd/serf-hub/main.go`'s `hostlock.AcquireLock(filepath.Join(home,
  ".serf", "hub.lock"))`, `home` from `os.UserHomeDir()`), with no
  `hub.toml`/flag override. Isolating a second hub instance on one host
  requires overriding the `HOME` env var for that process, not just passing
  `--config`/`--addr`.
- Ports `9131`/`9180` (the tool defaults) were unavailable in this
  environment (occupied by the unrelated `-sr` pair); this card used
  `29131`/`29180`. Pick free ports and confirm with `lsof` before binding.

## Verdict

All four sub-scenarios' load-bearing assertions held under live,
non-mocked, real-API execution, cross-checked across the daemon's own
`/status` endpoint, the `--verbose` NDJSON event stream, the real web UI, and
the real TUI. The one assertion that did not hold as originally written (the
settings-probe status for Server B) was traced to the probe's own documented,
intentional command-presence-only semantics for stdio transports — a
corrected expectation, not a code defect — and Server C's settings-probe
row exhibits the analogous, equally-intentional divergence for the
complementary reason (initialize-depth vs per-call depth). No regression or
unexpected behavior was found in the shipped Tasks 1-19 implementation.
