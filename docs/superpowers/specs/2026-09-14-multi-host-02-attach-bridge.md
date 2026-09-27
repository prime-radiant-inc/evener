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
     wildcard bind address is rewritten to loopback — an empty host, `0.0.0.0`,
     and `localhost` become `127.0.0.1:<port>`, and the IPv6 wildcard `::`
     becomes `[::1]:<port>` (`net.JoinHostPort("::1", port)`, so the literal is
     bracketed for the dial; a hub bound IPv6-only is not listening on IPv4, so
     forcing the family would fail the dial). A client running on the hub host
     always reaches the hub over loopback even when the hub advertises a
     wildcard; `localhost` is rewritten to the literal `127.0.0.1` so a poisoned
     resolver cannot aim the token-carrying dial off-host.
  2. reads the capability token from the host state root
     (`<stateRoot>/auth-token` — `hubedge.TokenFileName`; trimmed), taking the
     state root from the same `hub.toml` (or `--config path`) the hub itself
     read, never a re-derived path;
  3. dials `ws://<addr>/rpc` with `Authorization: Bearer <token>` **and the
     bridge marker `X-Evener-Bridge: 1`** (see §Contract, "Bridge marker") — the
     marker is what lets the hub edge tell a peer attach bridge from an ordinary
     local session, which presents the same token without it;
  4. proxies `Message`s in both directions between the WebSocket transport and a
     `StreamTransport` over stdin/stdout;
  5. exits when either side closes.
  6. refuses a **missing or invalid explicitly supplied `--config`**. The
     default-to-`DefaultConfig()` fallback (`LoadConfig` returns the defaults
     with a nil error when the file does not exist) applies **only** to the
     implicit default path. When `--config <path>` is passed and that file is
     missing or unparseable, the bridge exits nonzero with a diagnostic naming
     the path instead of silently reading defaults and deriving the wrong state
     root/token; the same rule applies to the hub's own `--config`
     (`cmd/evener-hub/main.go`), since bridge and hub must agree on the layout
     (component 03, §"Error handling").
- This command is a **client** of the running hub. It must not start a hub, take
  `hostlock`, or bind any port. Starting a stopped host hub for a first attach
  is component 04's **bootstrap** path (component 04 §5), run as its own remote
  command before the bridge is dialed — never something the bridge does; the
  bridge's "no hub at `<addr>`" failure is the signal that a bootstrap is
  needed, not a reason for the bridge to become a server.

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
- **The assembled URL is validated loopback-only before the token crosses it.**
  The bridge dials an unencrypted `ws://` URL carrying the hub's capability
  token, so the host in the resolved URL must be loopback — `127.0.0.1` or
  `[::1]` after the wildcard/`localhost` rewrite above — with no userinfo, no
  unexpected path or query, and a valid port. A config-supplied `addr` such as
  `example.com:9180`, a `localhost:user@host`-style spelling, an out-of-range
  port, or any non-`/rpc` path is refused with a named
  `errNonLoopbackAddr`-style error and a nonzero exit, never dialed.
- **The dial bypasses `HTTP_PROXY`.** The WebSocket dial must not use
  `http.DefaultClient` (which honors `HTTP_PROXY`/`HTTPS_PROXY`), or a proxy in
  the environment would receive the hub's capability token aimed at the loopback
  join; it uses a client with `Proxy: nil`.
- **Bridge marker (cooperative, not a security boundary).** The bridge presents
  a marker on its `/rpc` dial — the request header `X-Evener-Bridge: 1` —
  alongside the same `Authorization: Bearer <token>` every other client uses.
  The hub's edge reads the marker together with the token and marks the
  connection *remote-originated*; a connection presenting the token **without**
  the marker is an ordinary local session. The marker is required because the
  credential cannot carry the role: the attach bridge, the local TUI, CLI
  scripts, and browser sessions all present the **same** host capability token
  (`cmd/evener-hub/web.go`, `cmd/evener-hub/internal/hubedge/auth_token.go`,
  `cmd/evener-tui/internal/hubstart/hub_start.go`), so the token alone cannot
  distinguish a peer bridge from a local client. **The header carries no secret
  and is client-asserted, so it is *not* verifiable.** Any holder of the shared
  capability token can set it or omit it; a malicious or modified peer bridge
  can therefore omit the header to be classified `local`. The explicit trust
  assumption for v1 is that peer hubs are **cooperative**: the marker makes an
  honest controller↔host cycle (A→B→A) terminate, and is a correctness aid, not
  a defense against a hostile peer. It is **not** a substitute for the token or
  a boundary that survives a compromised host. Binding the *remote-originated*
  role to a signal the receiving hub can **verify server-side** — a distinct
  bridge credential not shared with local clients, or the identity of the
  `ssh`-spawned transport instance — is the mechanism that would make the loop
  guard a real boundary; that binding is a tracked code follow-up, and until it
  lands the marker is documented as cooperative-only, never as "explicit and
  verifiable". Component 05, §"Ref translation detail", defines how the edge's
  role becomes the request-context `origin` and the loop-guard refusal that
  reads it. **Implementation status:** the header and the edge's role
  classification are shipped: the bridge sets `X-Evener-Bridge: 1` on its dial
  (`cmd/evener-hub/attach.go:145-151`), and the
  `/rpc` edge classifies the connection from the marker and stamps the origin
  into the request context (`cmd/evener-hub/host_routing_origin.go:20,47-48`;
  `cmd/evener-hub/web.go`). Binding the role to a server-verifiable signal is
  the part that remains a tracked code follow-up.

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
  - `cmd/evener/main.go`: the `hub` runner **forwards its own `stdin`/`stdout`
    dependencies** — `hubcmd.Run(args, stdin, stdout, stderr)`, with the
    dispatcher handing the CLI's streams straight through (PR 02 shipped this).
    Dropping them as `nil, nil` would be the defect the injected-stream test
    exists to catch: it silently discards a caller's streams, because `Run` can
    only fall back to `os.Stdin`/`os.Stdout` when a parameter is nil, and the
    bridge then carries frames over the process fds the test did not supply.
    On the real SSH path the forwarded streams *are* the process fds (ssh
    connects the child's fds 0/1 to the SSH channel), so forwarding is
    behaviour-preserving there and is the only honest reading for an in-process
    caller.
  - Nothing on the CLI may wrap, buffer, or redirect the hub command's stdout:
    the SSH child's stdout is the framed AppWire stream (component 04).
- Reuse: `appwire.DialWebSocketWithHeaders`, `appwire.NewStreamTransport`,
  `appwire.Transport` — the two-goroutine `proxyAppWire` pump (spike
  `spike/bridge/main.go`).
- Token: read from the state root, `strings.TrimSpace` (the file ends with a
  newline; untrimmed it is an invalid header value — spike finding).
- **Loopback validation and proxy bypass are shipped** (`cmd/evener-hub/attach.go`):
  `resolveHubURL` validates the assembled `addr` and returns the `ws://…/rpc`
  URL — refusing a non-loopback host, userinfo, a wrong path or query, and an
  out-of-range port with `errNonLoopbackAddr` — and `attachDialClient` is the
  `Proxy: nil` client handed to `appwire.DialWebSocketWithHeaders`. The Contract
  bullets above are what make those guards normative rather than incidental.

## Data flow

```
controller hub
  appwire.Client over StreamTransport
    → ssh process stdin/stdout  ──────────────►  sshd on host
                                                   → bridge process
                                                       (stdout discipline: frames only)
                                                   → WebSocket ws://127.0.0.1:<hubaddr>/rpc
                                                           (Authorization: Bearer <token>
                                                            + X-Evener-Bridge: 1)
                                                   → hub AppWire router
```

The bridge is a bidirectional pump: each frame received on one transport is
sent on the other, in both directions.

## Error handling

- Dial failure → stderr message, nonzero exit.
- Either direction's error → close both transports, exit.
- No hub running → clear stderr message ("no hub at <addr>").
- Non-loopback or malformed address → stderr message naming the address, nonzero
  exit, and the capability token is never dialed.
- Explicitly supplied `--config` missing or unparseable → stderr message naming
  the path, nonzero exit, with no fallback to defaults.

## Testing

- In-memory pipe pair + an in-process hub test server: assert framed messages
  round-trip and that stdout contains only frames.
- Assert the command never writes to stdout on the error path.
- Call `hub.Run` (the hub package's exported entrypoint) with injected
  `stdin`/`stdout` buffers and `args = {"attach", "--stdio"}`: assert the
  `attach` dispatch is reached and the injected streams — not the process fds —
  are the ones that carry frames. This is the regression test for the nil-stream
  failure mode.
- **Loopback guard (negative cases).** `resolveHubURL` refuses a non-loopback
  host (`example.com:9180`), a userinfo-in-port spelling, an out-of-range port,
  and a non-`/rpc` path with `errNonLoopbackAddr`; assert no `Authorization`
  header is assembled for a refused address. A configured `HTTP_PROXY` /
  `HTTPS_PROXY` is not consulted by the dial (`attachDialClient` has
  `Proxy: nil`), so the token cannot reach a proxy.
- **Bridge marker.** The dial presents `X-Evener-Bridge: 1` alongside the bearer
  token, and the hub edge records the connection as remote-originated only when
  both are present (component 05). Assert a token-only dial is classified local,
  and that a request whose `InitializeParams.ClientInfo` names a host is *not*
  treated as remote-originated (component 05, §"Ref translation detail").

## Acceptance criteria

- Against a running hub, `initialize` and `thread/list` succeed over the bridge's
  stdio (spike proved this end to end).
- No port is bound by the bridge; `hostlock` is untouched.
- `evener hub attach --stdio` and `evener-hub attach --stdio` both reach the
  bridge, and the bridge's stdin/stdout are the streams the caller supplied
  (`deps.stdin`/`deps.stdout`, defaulting to the process fds) — never nil.
- The bridge's `/rpc` dial carries `X-Evener-Bridge: 1` with the bearer token, so
  the hub edge can classify the connection remote-originated without deriving a
  role from the shared token.

## PR size

Small–medium: ~150–300 LOC plus tests.

## Open questions

- **Address discovery (resolved).** The bridge reads the host's own `hub.toml`
  (`--addr` overrides it; wildcard binds are rewritten to loopback). The
  controller never has to know the host's address to attach.
- **Separate binary (resolved).** `evener hub attach --stdio` in the hub binary;
  no `evener hub-bridge`.
