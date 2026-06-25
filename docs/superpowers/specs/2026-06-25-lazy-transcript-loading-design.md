# Lazy / paginated transcript loading (`thread/turns/list`)

**Date:** 2026-06-25
**Branch:** `bot/webui-desync-rootcause` (follow-on)
**Status:** design → (pending two decisions) → implementation

## Problem

A session transcript is materialized and rendered **whole** at every layer, so
opening a large session is slow (~1–3 s) and heavy (tens of MB):

- **Daemon** (`server/appwire_runtime.go handleAppThreadRead`): with
  `includeTurns`, builds the entire `Thread.Turns` from the notification buffer
  (`AppNotificationsAfter(0,…)`) or the whole transcript file.
- **Hub** (`cmd/serf-hub/app_threadread.go`): merges all past + live turns into
  one response.
- **Web** (`appwire.js eventsFromThread` → `renderer.js`): loops every turn ×
  every item into a flat event stream and appends each into the DOM, reflowing
  per item.

We need a **window**: render the latest turns on open, fetch older turns on
demand, keep the live stream for new turns.

## Design

The protocol shape already exists and is Codex-aligned: `ThreadTurnsListParams`
is cursor-based (`cursor`, `limit`, `itemsView`) with a `NextCursor` reply, and
Codex app-servers implement `thread/turns/list` natively. `ThreadItem` carries
`TranscriptEntryIndex` — a monotonic ordinal — which is the cursor key.

**Paging direction.** Chat-style: the latest turns load first, older pages load
backward. A cursor is an opaque token encoding the boundary (oldest loaded
turn's first `TranscriptEntryIndex`); `thread/turns/list` returns up to `limit`
turns *older than* the cursor plus a `nextCursor` (empty when the head is
reached).

### Daemon (`serf serve`)

1. **Bound the initial read.** `thread/read` gains an optional turn window: when
   set, return only the latest N turns and a cursor for older ones. The
   subscribe/relay/hydrate path is unchanged — it just hydrates the latest
   window instead of the whole transcript.
2. **Implement `thread/turns/list`.** Register `handleAppThreadTurnsList`:
   slice the same materialized turns (notification buffer / transcript file) to
   `limit` turns older than `cursor`, newest-first, returning `nextCursor`.
3. Flip `FeatureSet.ThreadTurnsList` to true.

### Hub (`serf-hub`)

1. Route `thread/turns/list` to the resolved source:
   - **local daemon / live:** the new daemon handler.
   - **past / not-loaded:** slice the transcript file (`pastEntryTurns`) by
     cursor instead of returning all.
   - **Codex:** proxy to the Codex app-server's native `thread/turns/list`
     (cursor/limit/itemsView pass through).
2. Pass the initial turn window through `thread/read` so the cold-load response
   is bounded; older turns come from `thread/turns/list`.

### Web client

1. **Cold load:** hydrate the latest window from `thread/read` (now bounded) and
   subscribe — unchanged except the window bound.
2. **Load earlier:** when the user nears the top, call a new
   `SerfAppwire.listTurns(ref, cursor, limit)`, convert via `eventsFromThread`'s
   per-turn logic, and **prepend** with scroll-anchor preservation (hold the
   pre-prepend scroll height so the viewport doesn't jump).
3. **Live:** new turns still arrive as notifications and append at the bottom —
   unchanged.

### Granularity (recommended default)

v1 pages **turns with their full items** (one level). It skips
`thread/turns/items/list` (two-level, per-turn item paging): that only matters
for a pathologically huge single turn, and Codex returns method-not-supported
for it, so there is no parity target. The `itemsView` param stays `full` in v1.

## Catalog / doc impact

Implementing these flips the two `ScopeUnimplemented` catalog entries to real
scopes (`thread/turns/list` → `both`; keep `thread/turns/items/list`
unimplemented). Update `appwire/protocol.go`, regenerate the protocol doc
(`make generate`), and replace `TestHubRPCDoesNotAdvertiseUnsupportedTurnLists`
with one asserting the capability **is** advertised once handlers exist. The
daemon/hub catalog cross-check tests then enforce the new wiring.

## Testing (TDD)

- **Daemon:** `thread/turns/list` cursor correctness — ordering (newest-first),
  page boundaries, `nextCursor` empties at the head, empty thread, single page.
- **Hub:** routing per source (live daemon, past file-slice, Codex proxy);
  bounded `thread/read` window.
- **Web (jstest):** `listTurns` prepends older turns in order; scroll position
  is preserved across a prepend; the live append path is unaffected.
- **Feature flag + catalog:** advertised true; catalog cross-checks pass.

## Decisions (locked)

1. **Load-earlier trigger:** auto-load on scroll-near-top. The renderer holds
   the pre-prepend scroll height and restores it after prepending so the
   viewport stays anchored on the turn the user was reading.
2. **v1 source scope:** all sources — serf live + past **and** Codex (proxy to
   the Codex app-server's native `thread/turns/list`). `ListTurns` becomes a
   Source-interface method; past/not-loaded sessions slice the transcript file.

## Out of scope (v1)

- `thread/turns/items/list` (per-turn item paging; no Codex target).
- DOM virtualization/recycling of off-screen rendered turns (separate perf axis;
  windowed *loading* is the ask here).
