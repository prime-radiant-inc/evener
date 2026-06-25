# Web UI keepalive + sidebar resilience

**Date:** 2026-06-25
**Branch:** `bot/webui-desync-rootcause`
**Status:** design → implementation

## Problem

Two field-reported symptoms in the serf-hub web UI:

1. **Interactive sessions silently desync** — the transcript stops receiving
   events. Manifests as the liveness line ("working · quiet …" → "no updates for
   Ns — may be stalled") or simply nothing updating until a manual force-refresh.

2. **Sidebar drops all live sessions for a little while** — live rows vanish,
   then reappear after several seconds.

## Root cause

### Desync: no WebSocket keepalive anywhere in the stack

The event path is two ping-less WS hops sharing one transport
(`appwire.WSTransport`):

```
browser ──WS /rpc──► hub (appserver) ──WS──► per-session daemon (appserver)
```

- `appwire/ws_transport.go` — `Send`/`Recv` are bare; no ping, no read deadline.
- `internal/appserver/websocket.go` — recv loop blocks on `Recv(r.Context())`;
  that context only cancels on a *clean* teardown. No ping ticker.
- `appwire/client.go` — the hub↔daemon read loop blocks on `Recv` forever; it
  ends only on a transport **error**.
- `cmd/serf-hub/assets/appwire.js` — reconnect is wired **only** to the WS
  `error`/`close` handlers; `request()` has no timeout.
- `cmd/serf-hub/assets/renderer.js` — the liveness UI is **cosmetic**; it never
  triggers a re-read or reconnect.

`coder/websocket` does not auto-ping. So when a TCP connection dies *silently*
(sleep/wake, Wi-Fi roam, VPN/NAT rebind, proxy idle-timeout with no close
frame), no `close`/`error` is delivered. The browser's `readyState` stays
`OPEN`, the Go read loops block forever, no frame arrives, and nothing recovers
until a full page reload rebuilds the WS and re-hydrates via
`readThread(subscribe=true)`.

### Sidebar: live rows pruned on a transient probe miss, render is a snapshot

- `navigationTreeInputs` → `Roster.List()` is an in-memory snapshot of the last
  `Roster.Refresh()`.
- `Roster.Refresh()` prunes a live entry after **two consecutive** `/status`
  probe failures, each with a **500 ms** timeout (`prober.go`, `main.go`). A busy
  daemon or a momentarily loaded host blows past 500 ms; two misses across two
  refresh cycles (fsnotify + 5 s tick) drop it for ~5–10 s.
- Independently, `remoteTreeThreads` **silently drops an entire source** on any
  `ListThreads` error (`web_api_tree.go`), emptying all of that source's rows
  for a render. The sidebar refreshes on nearly every notification, widening the
  window to catch a transient-empty render.

## Design

### 1. Transport keepalive (primary desync fix)

Detect a dead peer within ~30 s at every hop, surfacing it as a normal transport
error so the **existing** reconnect / re-hydrate machinery runs.

**Go server role (`internal/appserver/websocket.go`)** — covers hub-for-browser
and daemon-for-hub. Spawn a ping goroutine alongside the recv/send loops:
every `keepalivePingInterval` (15 s), `conn.Ping(ctx)` with a
`keepalivePongTimeout` (10 s) sub-context; on error, `cancel()` the connection
context (unblocking `Recv` and the send loop → clean teardown). The peer
(browser or Go client) auto-pongs while it is reading.

**Go client role (`appwire/client.go` + `ws_transport.go`)** — covers
hub↔daemon. Add `func (t *WSTransport) Ping(ctx) error`. Add an optional
`Pinger` interface (`Ping(context.Context) error`). In `Client.Start`, if the
transport is a `Pinger`, run the same ping loop; on error, `transport.Close()`
so the read loop's `Recv` errors out and `notifications` closes. The in-memory
test transport is not a `Pinger`, so tests are unaffected.

**Browser role (`appwire.js`)** — browsers cannot send WS ping frames from JS, so
use an application-level heartbeat. Add a cheap `ping` RPC handled directly in
`appserver` `HandleMessage` (returns `{}`, no router change, covers hub +
daemon). In `appwire.js`, when the socket is `OPEN`, every
`HEARTBEAT_INTERVAL_MS` (20 s) send `ping` with a `HEARTBEAT_TIMEOUT_MS` (10 s)
timeout. On timeout/reject, force `ws.close()` — which fires the existing close
handler → `markDisconnected` → renderer reconnect. This is what lets the browser
detect a silently-dead hub.

### 2. Browser liveness self-heal (belt-and-suspenders)

When the renderer's liveness model reaches **concern** (`renderer.js`
`refreshLiveness`), proactively re-hydrate the active thread via
`readThread(subscribe=true)` instead of only painting a warning. This recovers
the case where the browser↔hub socket is healthy (heartbeat passes) but the
hub↔daemon hop silently stalled, so no thread frames flow. Guard against
re-entrancy and only fire once per concern episode.

### 3. Sidebar last-known-good (sidebar fix)

`BuildTree` / the sidebar input assembly must not blank the live list on a
transient miss:

- When a remote source's `ListThreads` errors, retain its **last-known-good**
  threads for that render instead of dropping them (`web_api_tree.go`).
- Retention is per-source and replaced wholesale on the next successful list, so
  a genuinely-gone source still ages out once it returns an empty success.

This is the targeted fix Jesse chose (#3). The roster's 500 ms / two-strike
prune is left as-is for now; last-known-good retention at the tree layer covers
the user-visible symptom without changing liveness semantics.

## Constants (defaults; tunable)

| name | value | where |
|------|-------|-------|
| `keepalivePingInterval` | 15 s | appserver, appwire client |
| `keepalivePongTimeout` | 10 s | appserver, appwire client |
| `HEARTBEAT_INTERVAL_MS` | 20000 | appwire.js |
| `HEARTBEAT_TIMEOUT_MS` | 10000 | appwire.js |

## Testing (TDD)

- **appserver**: a connection whose peer never pongs is torn down within the
  ping/pong budget (use a controllable conn / short intervals). `ping` request
  returns an empty result.
- **appwire client**: `Pinger` transport that fails `Ping` closes the transport
  and the notifications channel.
- **appwire.js** (jstest): heartbeat sends `ping`; a hung `ping` closes the
  socket and triggers connection-lost.
- **renderer.js** (jstest): entering the concern band calls `readThread` once.
- **BuildTree / tree inputs**: a source that errors after a good list still
  contributes its last-known-good rows; an empty *successful* list clears them.

## Out of scope

- Roster prune-policy / probe-timeout retuning (concurrent probes, higher strike
  count). Tracked separately; #3 covers the symptom.
- Composer buffer-and-replay while disconnected (the banner still disables send).
