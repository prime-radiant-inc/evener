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
- A frame-size limit mirroring `appWireWebSocketReadLimit`
  (`defaultStreamFrameLimit`), with a per-transport override for tests.
- Serialized writes so concurrent `Send`s do not interleave.

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
func NewStreamTransportWithLimit(rw io.ReadWriteCloser, limit int) *StreamTransport // test seam
var ErrStreamFrameTooLarge = errors.New(...) // exported sentinel, both directions
var ErrStreamClosed = errors.New(...)        // exported sentinel, latched by Close
// Send: marshal Message -> append '\n' -> write (serialized; oversize -> ErrStreamFrameTooLarge)
// Recv: readLine (bufio.Reader.ReadSlice('\n') loop, bounded by limit) -> unmarshal -> Message
// Close: latch ErrStreamClosed, close the underlying rw (unblocks a blocked Recv)
```

The one framing mechanism is a `bufio.Reader` over the `io.ReadWriteCloser` and
a `ReadSlice('\n')` loop (`readLine` in `appwire/stream_transport.go`); the
reader's buffer stays modest and `readLine` enforces the transport's `limit`
exactly, so a maximum-size frame is accepted while anything larger fails without
buffering it all. `Recv` is that loop plus `unmarshalWSMessage`. There is **no**
`bufio.Scanner` and no `bufio.Reader.ReadBytes('\n')`/`ReadString` "read the
whole token then check" path anywhere in the transport — the three have
different buffering and different recoverability, and only one of them is the
shipped mechanism, so they are not interchangeable descriptions.

## Implementation

- `appwire/stream_transport.go` — shipped; the transport owns a `limit` int
  (`defaultStreamFrameLimit = appWireWebSocketReadLimit` for production,
  `NewStreamTransportWithLimit` for tests) and a `bufio.Reader`.
- `Recv`'s `readLine` is the cap mechanism: it grows the line a chunk at a time
  via `ReadSlice('\n')` and returns `ErrStreamFrameTooLarge` (poisoning the
  transport) as soon as the accumulated payload exceeds `limit`; `Send` returns
  the same exported sentinel when the marshaled frame is over `limit`. No
  `bufio.ErrTooLong` is involved — there is no `bufio.Scanner`.
- Cancellation: `Send`/`Recv` check `ctx.Err()` on entry; a blocked `Recv`
  unblocks when the canceled context's `AfterFunc` poisons the transport (closes
  the underlying stream), which is how the client stops it. Cancellation means
  teardown, not a retry: the transport is unusable afterwards. `Close` latches
  `ErrStreamClosed` and drains in-flight writes, so after it returns no write can
  still reach the stream and every later `Send`/`Recv` reports `ErrStreamClosed`
  even when the reader held prefetched frames.
- **`Close` ordering — close before wait, never wait-then-close.** `Close`
  latches the terminal cause first, then closes the underlying
  `io.ReadWriteCloser`, and only then waits for admitted writes. The order is
  what makes the drain terminate: closing the stream is what unblocks a `Write`
  that is blocked in the underlying stream, so a wait for in-flight writes can
  never hang on a write that is merely stalled. Synchronization is the same
  principle: admission is held under a read lock for the duration of
  `rw.Write`, and the drain takes the matching write lock **after** the close, so
  a waiting `Close` never deadlocks against a blocking `Write` that holds the
  lock. (The inverse — `Close` taking the write lock before closing — is exactly
  the deadlock this contract forbids.) The serialized-write lock is a buffered
  channel acquired with a `select` on `ctx.Done()`, not a plain mutex, so a
  `Send` queued behind a stalled write still obeys its own context.
  Blocked-write shutdown: a write already admitted when `Close` (or its context)
  lands is interrupted by the close and reports the recorded terminal cause; a
  write not yet admitted returns `ErrStreamClosed` (or the cancellation) without
  writing anything.
  **The admitted-write drain is `Close`-only; it is not a promise of every
  poison path.** `poison()`/cancellation close the underlying stream to
  interrupt a blocked `Write` but deliberately do **not** take the write lock or
  drain admitted writes — that close is exactly what unblocks the write it
  exists to interrupt, so a poison path guarantees only that later
  `Send`/`Recv` calls report the terminal cause, not that an in-flight write
  completes or is awaited. The earlier phrasing "`Close` (and every poison
  path) … waits for admitted writes" was wrong and is superseded: the
  wait-for-admitted-writes guarantee belongs to `Close` alone. **This is already
  the shipped implementation** —
  `appwire/stream_transport.go`'s `Close` latches `ErrStreamClosed`, calls
  `doClose()` (the underlying `rw.Close()`), then `drainWrites()`; `poison`
  deliberately does **not** take the write lock, because that close is what
  unblocks the write it exists to interrupt. The contract is stated here so a
  rewrite cannot regress to wait-before-close.

## Data flow

```
appwire.Client.Send(ctx, msg)
  → StreamTransport.Send: json.Marshal(msg) + '\n'   (serialized write)
  → io.ReadWriteCloser (SSH channel / pipe)
  → peer StreamTransport.Recv: readLine (ReadSlice('\n') loop) → unmarshal → Message
  → peer appwire.Client.Recv dispatches the frame
```

Symmetric in the other direction; `Close` ends both.

## Error handling

- **Oversize frame → `ErrStreamFrameTooLarge`** (exported; match with
  `errors.Is`). Both directions produce it, with different recoverability:
  - **Read** (the framing cap, `readLine`): the line is grown a chunk at a time
    and the transport is **poisoned** the moment the payload exceeds `limit`.
    The rest of the oversize frame is still on the wire, so the stream is out of
    alignment; draining it would itself be unbounded work over
    attacker-influenced input. The transport closes the underlying stream and
    **every later `Recv` and `Send` returns the same error** — the frame boundary
    is gone and the transport is unusable.
  - **Write** (`Send`): a marshaled frame over `limit` is refused **before any
    bytes are written**, and this case does not poison the transport (nothing
    reached the wire), so a caller may retry with a smaller message.
- **Invalid JSON on a line → the unmarshal error, and the transport does not
  poison itself.** The offending line has been fully consumed, so a later `Recv`
  reads the *following* line; nothing in the transport closes the stream. That
  is deliberate — see the caller contract below. This non-poisoning is about
  **frame alignment** (the delimiter was found, so the next line starts where it
  should), not about the stream still being usable at the connection level: the
  caller contract below makes the error terminal for the channel regardless.
- **Torn frame → `io.ErrUnexpectedEOF`, poisoned.** A stream that ends mid-frame
  (a partial write on the peer) is not a short message: the delimiter is
  required, so `readLine` reports `io.ErrUnexpectedEOF` and poisons the
  transport. A clean end of stream between frames is `io.EOF`.
- **Underlying write error → propagated from `Send`, and it poisons the
  transport.** A write that lands only part of a frame
  (`io.ErrShortWrite`) has desynchronized the stream, so the transport is done;
  so is a zero-byte write failure (a broken pipe, a reset). Every *later* call
  returns the latched cause. The failing call itself may not: a partial write
  that races a cancellation latches `io.ErrShortWrite` — the desync is the
  durable cause, decided from the write before the context is consulted
  (`appwire/stream_transport.go:173-183`) — but that `Send` returns
  `context.Canceled`, because the in-flight call reports the cancellation it
  was racing before it consults the latch
  (`appwire/stream_transport.go:184-186`); the next call returns the latched
  `io.ErrShortWrite`. A zero-byte failure that coincides with cancellation
  latches and returns the same cancellation, so it does agree.
- `ctx` already done on entry → `ctx.Err()`, returned before any read/write.
- `Close` closes the underlying `io.ReadWriteCloser`; that is what unblocks a
  blocked `Recv`. `Close` is the caller's lever, not an error handler.

### Contract for callers: a framing error ends the channel

The transport does not decide when a connection is dead — the caller owns that.
The required behavior (component 05 states the same rule as "close the client,
fail pending requests, mark the source offline; never attempt to resynchronize a
corrupt stream"):

1. Any `Recv` error — `ErrStreamFrameTooLarge`, an unmarshal error, or `io.EOF`
   — means the stream can no longer be trusted. `Close()` the transport and fail
   everything in flight. Do not skip the bad line and keep reading; an
   oversize-frame error in particular is unrecoverable because the transport is
   poisoned and every later call returns the same error.
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
- Oversize-frame rejection and concurrent-`Send` non-interleaving.
- Frame-error semantics: after an oversize **read**, a second `Recv` returns
  `ErrStreamFrameTooLarge` again (the transport is poisoned) and a later `Send`
  does too; an oversize **write** returns the sentinel without poisoning, so a
  smaller message still goes out; after an invalid JSON line, the next `Recv`
  can still read the next frame (a property of the transport in isolation — the
  line was fully consumed, so alignment is intact — and **not** a licence for a
  caller to resume: the §"Contract for callers" above still makes that error
  terminal for the channel). `errors.Is` must match the exported sentinel on
  both paths. Add: a stream that ends mid-frame reports `io.ErrUnexpectedEOF`
  and stays poisoned; a clean end reports `io.EOF`; `Close` latches
  `ErrStreamClosed`, so a `Recv` after `Close` does not deliver a prefetched
  frame.
- `Close` returns while a write is blocked: with a writer stalled in the
  underlying `Write`, a concurrent `Close` closes the stream, unblocks the
  write, drains it, and returns — it must not deadlock waiting for a lock the
  blocked write holds. The blocked `Send` reports the recorded terminal cause,
  and a write that had not yet been admitted returns `ErrStreamClosed` without
  reaching the stream. A `Send` queued behind a stalled write is released by its
  own context cancellation. (Shipped coverage:
  `TestStreamTransportCloseWaitsForInProgressClose`,
  `TestStreamTransportCloseAdmitsNoFurtherWrites`,
  `TestStreamTransportQueuedSendHonorsCancel`, `appwire/stream_transport_test.go`.)

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
  (`appWireWebSocketReadLimit`), reused as `defaultStreamFrameLimit`. Consider
  hoisting one shared frame limit into `transport.go` and sizing it to the
  largest legitimate AppWire message rather than the network backstop.
- **Shared framing core.** A `framedTransport` core plus thin WS/stream
  adapters would make the next transport additive instead of another full
  implementation.
