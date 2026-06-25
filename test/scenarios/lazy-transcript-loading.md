# web-lazy-loading: large transcripts load windowed and page in on scroll-up

**What this covers**: lazy transcript loading via `thread/turns/list`
(commits `49445a1d` backend, `2d36b225` web). A large session must (1) cold-load
only the latest window of turns, not the whole transcript, and (2) page older
turns in on scroll-to-top, prepended above the live content with the scroll
position preserved (no jump). If `eventsFromThread`/`prependOlderTurns`, the
daemon/hub `thread/turns/list` handlers, or the `TurnLimit`/`OlderCursor` wiring
regress, this catches it.

## Pre-state

A freshly built `serf-hub` and a seeded large *past* session, both in an
isolated state dir (own `$HOME`/state/port — never touch a real hub):

1. `go build -o $E2E/serf-hub ./cmd/serf-hub`
2. Seed `$E2E/state/projects/bigproj/sessions/<id>.{meta.json,transcript.jsonl}`
   with ~60 user/assistant pairs (120 appwire turns). Use a throwaway `main`
   that calls `schema.SaveSessionMeta` + `transcript.NewWriter` /
   `tw.Append(schema.NewTurn(...))` — the same APIs the daemon writes with.
   Seed user text `"user message number NN"` so turns are countable.
3. Launch with a hub.toml whose `state_glob` points at `$E2E/state/projects/*`
   and a private `hub_state_root`/`run_dir`/`past_index_db`. Wait for
   `GET /api/health`.

## Steps

1. **Transport check** (proves the assembled hub→past path without a browser):
   dial `ws://127.0.0.1:<port>/rpc` with `Authorization: Bearer <auth-token>`,
   `initialize`, then `thread/read{ref, includeTurns, turnLimit:40}` and walk
   `thread/turns/list{ref, cursor, limit:30}` to the head.
2. **Browser cold load**: navigate to `/auth?token=…` then
   `/s/local:<id>`. Read `window.SerfRenderer.olderTurnsCursor` and count
   `user message number NN` matches in `.conversation`.
3. **Scroll-up paging**: record `conv.scrollHeight` (h0), set `conv.scrollTop=0`
   (fires the loader), await, then re-read cursor, the message count, the first
   message number, and `conv.scrollTop`.

## Expected

- Step 1: full read = 120 turns; windowed read = **exactly 40** turns with a
  non-empty `olderCursor`; the pages reconstruct all 120; the window is the
  latest turns. Falsify: windowed read returns 120 (no `TurnLimit`), or
  `olderCursor` empty, or sum ≠ 120.
- Step 2: ~**20** distinct user messages (numbers 40–59), **not 60**;
  `olderTurnsCursor === "80"`. Falsify: 60 messages rendered (cold load not
  windowed) or cursor empty.
- Step 3: cursor advances (`"80"→"50"`); the first message number drops
  (40→~25) — older turns **prepended above**; the last message (59) is
  unchanged (live content intact); and **`scrollTop ≈ scrollHeight−h0`** (the
  prepended height), i.e. the viewport stayed anchored. Falsify: `scrollTop`
  stays 0 (jumped to the new top) or the count/first-message don't change
  (paging didn't fire).

Recorded run: full=120, window=40/cursor=80, pages 30+30+20 to head; browser
cold=20 msgs (40–59); after scroll-up cursor=50, first=25, scrollTop=1418 ==
h1−h0 (3340−1922). All green.

## Cleanup

Kill the hub pid; remove the throwaway seeder/checker programs and the `$E2E`
scratch dir. Leave any real hub untouched.

## Sharp edges

- The `bigproj` project is collapsed in the sidebar (past sessions lazy-load
  their rows), so `.sb-row` is empty until expanded — navigate to `/s/<ref>`
  directly instead of clicking.
- "turns" are per-message (user *and* assistant), so a 40-turn window is ~20
  user messages. Count the right granularity.
- The cold load auto-scrolls to the bottom; scroll the `.conversation` element
  (not the window) to reach the top and trigger paging.
