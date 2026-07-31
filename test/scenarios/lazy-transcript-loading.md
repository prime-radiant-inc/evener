# lazy-transcript-loading: large transcripts load windowed and page in on scroll-up

**What this covers**: lazy transcript loading via `thread/read{turnLimit}` +
`thread/turns/list` (commits `49445a1d` backend, `2d36b225` web). A large
session must (1) cold-load only the latest window of turns, not the whole
transcript, and (2) page older turns in as the reader approaches the top,
prepended above the live content without moving what they were reading. If
the daemon/hub `thread/turns/list` handlers, the `TurnLimit`/`OlderCursor`
wiring, or the web's `loadOlderTurns`/`prependOlderTurns` path regress, this
catches it.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. The old
`window.SerfRenderer.olderTurnsCursor` probe and `.conversation`/`.sb-row`
selectors this card used to drive died with the vanilla frontend
(`660376f78`); there is no renderer handle to read a cursor out of any more.

## Pre-state

A freshly built `serf-hub` and a seeded large *past* session, both in an
isolated state dir (own `$HOME`/state, kernel-assigned port — see the Setup
checklist in `docs/agentic-testing.md`; never touch a real hub):

1. `go build -o "$run/serf-hub" ./cmd/serf-hub`
2. Seed `$run/state/projects/bigproj/sessions/<id>.{meta.json,transcript.jsonl}`
   with ~60 user/assistant pairs (120 appwire turns). Use a throwaway `main`
   that calls `schema.SaveSessionMeta` + `transcript.NewWriter` /
   `tw.Append(schema.NewTurn(...))` — the same APIs the daemon writes with.
   Seed user text `"user message number NN"` so turns are countable.
3. Launch with a hub.toml whose `state_glob` points at `$run/state/projects/*`
   and a private `hub_state_root`/`run_dir`/`past_index_db`. Wait for
   `GET /api/health`.
4. For step 3 only: the browser needs a real SPA bundle. A checkout that has
   never run `make build-web` ships a one-line `frontend/dist/PLACEHOLDER`
   and serves no app — build the frontend first, then the hub (rebuild
   matrix item 3 in the runbook).

## Steps

1. **Transport check — the exact assertions, no browser needed.** Dial
   `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`,
   `initialize`, then:
   - `thread/read{ref:"local:<id>", includeTurns:true}` with no `turnLimit` —
     the unbounded read.
   - `thread/read{ref:"local:<id>", includeTurns:true, turnLimit:40}` — the
     window the web client actually asks for (`stores/threads.ts:550,593`).
   - walk `thread/turns/list{ref, cursor, limit:30, itemsView:"full"}` from
     the returned `olderCursor` back to the head, following `nextCursor`.
     30 is the web's own page size (`OLDER_TURNS_PAGE_SIZE`,
     `stores/threads.ts:621`; params at `:623-625`).

   Param and response field names are `appwire/types.go:768-801`:
   `ThreadReadParams.TurnLimit` → `ThreadReadResponse.OlderCursor` →
   `ThreadTurnsListParams.Cursor/Limit` → `ThreadTurnsListResponse.Data/NextCursor`.
   Send frames as `{"id":N,"method":…,"params":…}` with **no `jsonrpc`
   field** — see Sharp edges.

2. **Browser cold load**: navigate to `/auth?token=$TOKEN&next=/s/local:<id>`.
   Assert on the paging row rather than on a turn count (see Sharp edges —
   the transcript is virtualized, so a DOM count measures the viewport, not
   the model):
   ```javascript
   ({
     port: location.port,
     olderRow: !!document.querySelector('[data-testid="load-older-row"]'),
     rowLabel: document.querySelector('[data-testid="load-older-row"]')?.textContent,
     firstVisible: document.querySelector('[data-testid="user-message-item"]')?.textContent
                     ?.match(/user message number \d+/)?.[0],
   })
   ```

3. **Scroll-up paging**: scroll the transcript's own scroll container toward
   the top. Paging is automatic — `LoadOlderRow` observes a sentinel with a
   400px prefetch margin (`flow/LoadOlderRow.tsx:48,66-79`), so it fires
   *before* the row is fully on screen; there is no button to press and no
   `scrollTop = 0` to set. Note which message is under the reader's eye
   first, then re-read after the fetch settles: the lowest
   `user message number NN` still rendered, whether `load-older-row` is
   still present, and whether the noted message is still at the same offset.

## Expected

- **Step 1 (transport, exact)**: full read = 120 turns; windowed read =
  **exactly 40** turns with a non-empty `olderCursor`; the `thread/turns/list`
  pages reconstruct all 120 with no duplicates and no gaps; the window is the
  *latest* 40. Falsify: the windowed read returns 120 (TurnLimit ignored), or
  `olderCursor` is empty despite truncation, or the pages don't sum to 120.
- **Step 2 (cold load)**: `[data-testid="load-older-row"]` is present, reading
  `Older turns` — it renders only while `model.olderCursor` is set
  (`panes/session/Session.tsx:195-198`), so its presence *is* the assertion
  that the cold load was windowed. The first rendered user message is well
  into the session (message ~40 of 60), not message 0. Falsify: no
  `load-older-row` on a 120-turn session (the cold load pulled everything),
  or message 0 is already rendered.
- **Step 3 (paging)**: the lowest rendered message number drops; the reader's
  noted message stays at the same screen offset (older turns went in *above*
  it, and the prepend anchor correction compensated —
  `flow/useTranscriptScroll.ts:21-24`, arithmetic pinned by
  `useTranscriptScroll.test.ts`); repeating until the head is reached makes
  `load-older-row` disappear entirely. Falsify: the view jumps to the new top
  (anchor correction lost), the message numbers never drop (paging never
  fired), or `load-older-row` persists after the oldest turn is rendered.
- **Failure path** (optional, worth one look if step 3 misbehaves): a failed
  page renders `role="alert"` with the error text plus a
  `[data-testid="load-older-retry"]` button inside the same row, and the
  automatic observer stops re-firing until Retry is pressed
  (`LoadOlderRow.tsx:57-62,86-93`). Falsify: a failed page shows nothing, or
  the observer hammers the failing endpoint in a loop.

Recorded run (2026-07, pre-rewrite, transport half only): full=120,
window=40/cursor=80, pages 30+30+20 to head. The browser half's recorded
numbers (`olderTurnsCursor === "80"`, 20 messages in `.conversation`) came
from the deleted renderer and are not reproducible; they are dropped rather
than restated.

## Cleanup

Kill the hub by the PID you captured; remove the throwaway seeder/checker
programs and the `$run` scratch dir. Leave any real hub untouched.

## Sharp edges

- **The transcript is virtualized.** `VirtualList`
  (`@tanstack/react-virtual`, `panes/session/Session.tsx:211-215`) renders
  only the turns near the viewport, so
  `document.querySelectorAll('[data-testid="user-message-item"]').length` is
  a measure of the *window on screen*, not of how much history is loaded.
  Counting it and comparing against 20 or 60 — the way this card used to
  count `.conversation` children — produces a number that changes with the
  browser window size. Assert on `load-older-row`'s presence and on which
  message numbers are reachable instead.
- **The paging cursor is not readable from the page.** There is no
  `window.SerfRenderer` and nothing else global; `model.olderCursor` lives in
  the zustand store. Check the cursor values at the transport level (step 1)
  and use the row's presence/absence as the browser-side proxy.
- **AppWire frames carry no `jsonrpc` field.** Sending the JSON-RPC 2.0
  envelope every other tool defaults to gets the frame rejected outright
  (`"jsonrpc field is not part of AppWire"`,
  `appwire/jsonrpc.go:164-166`) and the server closes the socket — which
  looks like a connection problem, not a malformed request. Frames are
  `{"id":N,"method":"…","params":{…}}`.
- Past sessions lazy-load their rail rows, so a collapsed `bigproj` project
  shows no session row until expanded — navigate to `/s/local:<id>` directly
  instead of clicking. Note the ref form: a bare `/s/<id>` renders
  "Page not found" by design.
- "Turns" are per-message (user *and* assistant), so a 40-turn window is ~20
  user messages. Count the right granularity when reading step 1's output.
- Paging fires from an `IntersectionObserver`, which needs a real layout —
  it does nothing in a headless/zero-size viewport. If step 3 never fires,
  check the window size before suspecting the loader.
