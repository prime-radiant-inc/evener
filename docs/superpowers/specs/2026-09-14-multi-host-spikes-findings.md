# Multi-host spikes — findings (2026-09-14)

Throwaway feasibility spikes for the multi-host evener design. Branch
`remote-hosts-spike`, base `fa654338c`. Not for merge.

## What was proven

### Spike A — AppWire over a byte stream (worked)
`appwire/stream_transport.go` adds `StreamTransport`, an `appwire.Transport`
over any `io.ReadWriteCloser`, framed as newline-delimited JSON. A marshaled
`Message` never contains a raw newline (JSON string escaping), so framing is
unambiguous. It does not implement `Pinger`, so the client keepalive skips it;
cancellation is via `Close`, which unblocks a blocked `Recv`.

Tests (`appwire/stream_transport_test.go`) pass:
- `TestStreamTransportRoundTrip` — request/response between two `net.Pipe` ends.
- `TestStreamTransportBacksClient` — `appwire.Client` drives a manual responder
  over the stream transport, including `initialize`.

### Spike C — AppWire over SSH stdio, end to end (worked)
`spike/bridge` runs on the remote host: it dials the hub's loopback
`ws://127.0.0.1:9180/rpc` with the capability token, then proxies AppWire
messages between that WebSocket and its own stdin/stdout. `spike/client` spawns
`ssh <host> <bridge>`, wraps the SSH process's stdin/stdout in
`StreamTransport`, and drives it with `appwire.Client`.

Result against m4 (`jesses-macbook-pro-2-1`, m4.local):
```
initialize over SSH stdio ok: protocol=evener-appwire-v5 source=local
thread/list over SSH stdio ok: 100 threads
```
So a controller can speak AppWire to a remote hub over an SSH channel with no
HTTP port exposed beyond the host's loopback.

### Spike B — serve AppWire over a raw stream (not needed)
The chosen re-attach design bridges stdio to the hub's existing loopback `/rpc`,
so the hub does not need a stream serve path. Cancelled; `ServeTransport`
extraction is deferred unless a future mode serves stdio directly.

## Operational findings

- The capability token file (`~/.local/state/evener/auth-token`) ends with a
  newline; it must be trimmed before use as an `Authorization: Bearer` header
  value or the WebSocket handshake fails with "invalid header field value".
- Cross-compiled `darwin/arm64` Go binaries run on Apple Silicon without a
  manual signing step (the Go linker adds an ad-hoc signature); `scp` + `chmod +x`
  sufficed.
- Non-interactive SSH has no `XDG_*` vars, so evener resolves to
  `~/.config/evener` and `~/.local/state/evener` — config discovery works.
- m4 already ran an `evener hub` bound to `*:9180`; its hub lock
  (`host.lock`/`hub.lock`) is held, confirming one-hub-per-machine in practice
  and that re-attach must be a client, never a second hub.

## Cleanup needed

- `spike/bridge`, `spike/client`, and `appwire/stream_transport*.go` are spikes.
- `/tmp/evener-spike-bridge` was copied to m4 (`jesses-macbook-pro-2-1`) and must
  be removed.
