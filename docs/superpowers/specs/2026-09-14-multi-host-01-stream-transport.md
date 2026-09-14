# Component spec 01 — AppWire stream transport

Parent: `2026-09-14-multi-host-evener-design.md`. Spike-backed
(`2026-09-14-multi-host-spikes-findings.md`).

## Purpose

Carry AppWire `Message`s over an arbitrary byte stream so a controller can speak
to a remote hub over an SSH channel (a spawned `ssh` process's stdin/stdout)
instead of a WebSocket.

## Scope

- New `StreamTransport` in `appwire` implementing `appwire.Transport`
  (`Send`/`Recv`/`Close`) over an `io.ReadWriteCloser`.
- Newline-delimited JSON framing; a marshaled `Message` never contains a raw
  newline, so frames are unambiguous.
- A frame-size limit mirroring `appWireWebSocketReadLimit`.
- Serialized writes (mutex) so concurrent `Send`s do not interleave.

## Non-scope

- Keepalive/ping (the transport does not implement `Pinger`; the client keepalive
  skips it).
- Serving AppWire over a stream (that path is not needed: re-attach bridges to
  the hub's existing loopback `/rpc`).
- Compression or binary framing.

## Contract

```go
type StreamTransport struct{ /* unexported */ }
func NewStreamTransport(rw io.ReadWriteCloser) *StreamTransport
// Send: marshal Message -> append '\n' -> write (mutex-guarded)
// Recv: read through next '\n' -> unmarshal -> Message
// Close: close the underlying rw (unblocks a blocked Recv)
```

## Implementation

- `appwire/stream_transport.go` (spike exists; promote to a real file with
  package docs and the read-limit constant shared with the WebSocket transport).
- Cancellation: `Send`/`Recv` check `ctx.Err()` on entry; a blocked `Recv`
  unblocks when `Close` runs, which is how the client stops it.

## Data flow

```
appwire.Client.Send(ctx, msg)
  → StreamTransport.Send: json.Marshal(msg) + '\n'   (mutex-guarded write)
  → io.ReadWriteCloser (SSH channel / pipe)
  → peer StreamTransport.Recv: ReadBytes('\n') → json.Unmarshal → Message
  → peer appwire.Client.Recv dispatches the frame
```

Symmetric in the other direction; `Close` ends both.

## Error handling

- Oversize frame → error, do not grow the buffer unbounded.
- Invalid JSON on a line → error; the connection is considered broken.
- Underlying write error → propagated.

## Testing

- Round-trip between two `net.Pipe` ends (exists).
- `appwire.Client` over the stream transport against a manual responder (exists).
- Oversize-frame rejection and concurrent-`Send` non-interleaving (add).

## Acceptance criteria

- `go test ./appwire/ -run StreamTransport` passes.
- `StreamTransport` satisfies `appwire.Transport` and drives `appwire.Client`.
- No change to the WebSocket transport's behavior.

## PR size

Small: ~80–150 LOC plus tests.

## Open questions

- **Frame observer.** `WSTransport` records frames to a `FrameObserver` for
  `EVENER_RECORD_APPWIRE` corpus capture; `StreamTransport` does not. Decide
  whether stream frames should be recorded too (a shared codec/observer seam
  would cover both).
- **Shared limit.** The frame limit is currently the WebSocket backstop
  (`appWireWebSocketReadLimit`), reused under `streamFrameLimit`. Consider
  hoisting one shared frame limit into `transport.go` and sizing it to the
  largest legitimate AppWire message rather than the network backstop.
- **Shared framing core.** A `framedTransport` core plus thin WS/stream
  adapters would make the next transport additive instead of another full
  implementation.
