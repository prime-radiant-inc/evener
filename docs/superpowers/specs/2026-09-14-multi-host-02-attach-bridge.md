# Component spec 02 — Hub attach bridge (stdio ↔ loopback /rpc)

Parent: `2026-09-14-multi-host-evener-design.md`. Spike-backed
(`2026-09-14-multi-host-spikes-findings.md`).

## Purpose

A process that runs **on the remote host**, connects to that host's already
running hub at its loopback AppWire edge, and proxies AppWire messages between
that WebSocket and its own stdin/stdout — so the controller can speak to the hub
over an SSH channel with no additionally exposed port.

## Scope

- A new subcommand `evener hub attach --stdio` (a hub-binary verb, beside the
  other `evener hub` subcommands) that:
  1. resolves the hub's loopback address: `--addr host:port` when given,
     otherwise the host's own `hub.toml addr`, otherwise `127.0.0.1:9180`. A
     wildcard bind address (`0.0.0.0`, `::`, or an empty host) is rewritten to
     `127.0.0.1:<port>`, since a client running on the hub host always reaches
     the hub over loopback even when the hub advertises a wildcard;
  2. reads the capability token from the host state root
     (`<stateRoot>/auth-token` — `hubedge.TokenFileName`; trimmed), taking the
     state root from the same `hub.toml` (or `--config path`) the hub itself
     read, never a re-derived path;
  3. dials `ws://<addr>/rpc` with `Authorization: Bearer <token>`;
  4. proxies `Message`s in both directions between the WebSocket transport and a
     `StreamTransport` over stdin/stdout;
  5. exits when either side closes.
- This command is a **client** of the running hub. It must not start a hub, take
  `hostlock`, or bind any port.

The SSH channel argv carries **no token** and, by default, no address: component
04 runs `ssh <opts> <dest> <evener_path> hub attach --stdio`, and the bridge
derives the address and the token's state root from the host's own
configuration. `--addr` and `--config` exist for an operator (or a non-default
layout) to point the bridge explicitly, and component 04 passes them when the
`[[hosts]]` entry sets `config_path`/`addr` (components 03/04) — that is what
keeps a hub on a custom config or port attachable *and* restartable. The
capability token is never on the argv.

## Non-scope

- Starting or supervising the hub (that is the connection manager's job).
- Auth beyond passing the host's own capability token.

## Contract

- stdin/stdout: newline-delimited AppWire `Message` JSON only.
- **stdout carries framed AppWire exclusively**; every diagnostic goes to stderr
  (a stray stdout write corrupts the stream — this is a hard rule and a test).
- Exit 0 on clean channel close; nonzero with a stderr diagnostic on failure.

## Implementation

- **The subcommand lives in the hub package** (`cmd/evener-hub/attach.go`) and
  is dispatched by `runMain` in `cmd/evener-hub/main.go` before the normal hub
  flag parsing (`if len(args) > 0 && args[0] == "attach" { return
  runAttach(args[1:], stderr, deps) }`), so it inherits none of the hub startup
  path: no config dirs, no `hostlock`, no listener.
- **Both entrypoints already deliver real streams, which is the part the CLI
  layer must get right** (an earlier revision of this spec claimed "nothing new
  on the CLI side" and left it at that):
  - `cmd/evener-hub/main.go`: `Run(args, stdin, stdout, stderr)` fills a
    `mainDeps` seam whose `stdin`/`stdout` fields default to `os.Stdin` /
    `os.Stdout`, and `runAttach` reads `deps.stdin`/`deps.stdout` (falling back
    to the process fds when nil) before wrapping them in
    `appwire.NewStreamTransport(newStdioStream(stdin, stdout))`. PR 02 added
    exactly this seam plus the `attach` dispatch.
  - `cmd/evener/main.go`: the `hub` runner calls
    `hubcmd.Run(args, nil, nil, stderr)`. Nil is not a bug: `hub.Run` treats nil
    as "keep the process's own `os.Stdin`/`os.Stdout`", which is precisely what
    an ssh-spawned bridge needs, since ssh connects the child's fds 0/1 to the
    SSH channel. Forwarding the CLI's own `stdin`/`stdout` dependencies to
    `hubcmd.Run` (instead of `nil, nil`) is the one remaining CLI-side
    improvement, and it only matters for an in-process caller that injects
    streams; it is not required for the SSH path.
  - Nothing on the CLI may wrap, buffer, or redirect the hub command's stdout:
    the SSH child's stdout is the framed AppWire stream (component 04).
- Reuse: `appwire.DialWebSocketWithHeaders`, `appwire.NewStreamTransport`,
  `appwire.Transport` — the two-goroutine `proxyAppWire` pump (spike
  `spike/bridge/main.go`).
- Token: read from the state root, `strings.TrimSpace` (the file ends with a
  newline; untrimmed it is an invalid header value — spike finding).

## Data flow

```
controller hub
  appwire.Client over StreamTransport
    → ssh process stdin/stdout  ──────────────►  sshd on host
                                                   → bridge process
                                                       (stdout discipline: frames only)
                                                   → WebSocket ws://127.0.0.1:<hubaddr>/rpc
                                                   → hub AppWire router
```

The bridge is a bidirectional pump: each frame received on one transport is
sent on the other, in both directions.

## Error handling

- Dial failure → stderr message, nonzero exit.
- Either direction's error → close both transports, exit.
- No hub running → clear stderr message ("no hub at <addr>").

## Testing

- In-memory pipe pair + an in-process hub test server: assert framed messages
  round-trip and that stdout contains only frames.
- Assert the command never writes to stdout on the error path.
- Call `hub.Run` (the hub package's exported entrypoint) with injected
  `stdin`/`stdout` buffers and `args = {"attach", "--stdio"}`: assert the
  `attach` dispatch is reached and the injected streams — not the process fds —
  are the ones that carry frames. This is the regression test for the nil-stream
  failure mode.

## Acceptance criteria

- Against a running hub, `initialize` and `thread/list` succeed over the bridge's
  stdio (spike proved this end to end).
- No port is bound by the bridge; `hostlock` is untouched.
- `evener hub attach --stdio` and `evener-hub attach --stdio` both reach the
  bridge, and the bridge's stdin/stdout are the streams the caller supplied
  (`deps.stdin`/`deps.stdout`, defaulting to the process fds) — never nil.

## PR size

Small–medium: ~150–300 LOC plus tests.

## Open questions

- **Address discovery (resolved).** The bridge reads the host's own `hub.toml`
  (`--addr` overrides it; wildcard binds are rewritten to loopback). The
  controller never has to know the host's address to attach.
- **Separate binary (resolved).** `evener hub attach --stdio` in the hub binary;
  no `evener hub-bridge`.
