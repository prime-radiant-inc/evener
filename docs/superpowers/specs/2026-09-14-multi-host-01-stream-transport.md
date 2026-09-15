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

- **Oversize frame → `errStreamFrameTooLarge`.** Both directions produce it:
  `Send` refuses a marshaled frame larger than `streamFrameLimit`, and `Recv`
  reports the capped scanner's `bufio.ErrTooLong` as the same sentinel. No
  buffer grows unbounded. Once this fires the scanner is done, so **every later
  `Recv` returns the same error** — the frame boundary is gone and the transport
  is unusable.
- **Invalid JSON on a line → the unmarshal error, and the transport does not
  poison itself.** The offending line has been consumed, so a later `Recv` would
  read the *following* line; nothing in the transport closes the stream. That
  is deliberate — see the caller contract below.
- **Underlying write error → propagated from `Send`.** The transport does not
  close on it either.
- `ctx` already done on entry → `ctx.Err()`, returned before any read/write.
- `Close` closes the underlying `io.ReadWriteCloser`; that is what unblocks a
  blocked `Recv`. `Close` is the caller's lever, not an error handler.

### Contract for callers: a framing error ends the channel

The transport does not decide when a connection is dead — the caller owns that.
The required behavior (component 05 states the same rule as "close the client,
fail pending requests, mark the source offline; never attempt to resynchronize a
corrupt stream"):

1. Any `Recv` error — `errStreamFrameTooLarge`, an unmarshal error, or `io.EOF`
   — means the stream can no longer be trusted. `Close()` the transport and fail
   everything in flight. Do not skip the bad line and keep reading; an
   oversize-frame error in particular is unrecoverable because the scanner
   returns the same error forever.
2. This is what the shipping caller does. `appwire.Client`'s read loop stops on
   the first `Recv` error, and component 04's link monitor (`linkMonitor`,
   `Channel.markLost` in `cmd/evener-hub/internal/sshconn`) turns that first
   read error into the channel's link-down edge, which closes the transport and
   starts the reconnect.
3. Cross-reference: `05-remote-hub-source.md` §"Error handling" assumes
   close-and-offline after a framing/JSON error. That assumption is a property
   of the *caller*, not of this transport; keep the two consistent by never
   resuming a stream that produced a frame error.

## Testing

- Round-trip between two `net.Pipe` ends (exists).
- `appwire.Client` over the stream transport against a manual responder (exists).
- Oversize-frame rejection and concurrent-`Send` non-interleaving (add).
- Frame-error semantics (add): after an oversize frame, a second `Recv` returns
  `errStreamFrameTooLarge` again (the scanner is exhausted); after an invalid
  JSON line, the next `Recv` can still read the next frame, and the test
  documents that closing is the caller's decision. `errors.Is` must match the
  sentinel in both the read and write paths.

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
