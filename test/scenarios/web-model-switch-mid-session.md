# web-model-switch-mid-session: two web clients converge on a live model switch; mid-turn and queued-input switches stay rejected

**What this covers**: spec `2026-07-12-model-switching-design.md` Acceptance
criteria 1 (two clients converge without reload) and 4 (mid-turn switch
rejected, no state change), plus the Failure-modes row "Switch while
messages are queued" (rejected until the queue drains — no reachable
boundary while `processing` covers the drain loop,
`agent/session_lifecycle.go:432-438`). Exercises the header model chip
(`data-model-trigger`, `cmd/serf-hub/assets/model-switch.js`), the
`thread/model/set` RPC, and the `thread/model/changed` broadcast.

## Pre-state

- Hub running with a real `openai` and a real `anthropic` instance
  configured (or two distinct catalogued models on one instance — any two
  models with different `provider/model` ids work for the assertion).
- `superpowers-chrome:browsing` with multi-tab support.
- A session spawned on model A (e.g. `openai/gpt-5.5`), idle.

## Steps

1. Open two browser tabs on the same session:
   `navigate http://127.0.0.1:9280/auth?token=<TOKEN>&next=/s/<SID>` (tab 1),
   `new_tab` the same URL (tab 2). Confirm both tabs' `[data-model-trigger]`
   read model A.
2. In tab 1, click `[data-model-trigger]`, pick model B from the picker
   (`.model-switch-picker .chip-picker-model`). Confirm the picker sends
   `thread/model/set`.
3. **Without reloading tab 2**, wait ~2s and read tab 2's
   `[data-model-trigger] [data-model-display]` text/`data-full-model`.
4. Start a turn on the session (any prompt that runs a few seconds). While
   `Status.Type == "active"` / `ActiveTurnID` is set, click
   `[data-model-trigger]` in tab 1: confirm the trigger is `disabled` and
   the picker does not open (`openPicker` returns early on `isBusy()`).
   Separately, call `thread/model/set` directly over the wire (or via
   `SerfAppwire.setModel`) while the turn is active and capture the error.
5. **Queued-input case.** While the turn from step 4 is still active, type
   and send a second message so it queues (composer queue-mode). Wait for
   the first turn to finish but confirm the daemon immediately starts
   draining the queued message (`processing` stays true across the drain).
   During that drain window, call `thread/model/set` again and capture the
   result.
6. Wait for the queue to fully drain to idle, then retry `thread/model/set`
   with model B and confirm it now succeeds.

## Expected

- Step 2: `thread/model/set` returns success; tab 1's chip updates to model
  B immediately from the RPC response / local apply.
- Step 3 (AC 1): tab 2's chip has already updated to model B **without a
  reload** — driven by the `thread/model/changed` notification
  (`applyModelChanged` in `model-switch.js`), not a `thread/read`.
- Step 4 (AC 4): the trigger is disabled while active; falsification if the
  picker opens. The direct `thread/model/set` call returns a structured
  AppWire error naming the active turn id (server: `"turn " + reservedTurnID
  + " is active"` or `"session is processing"`), and the session's model is
  unchanged (re-read `thread/read`, model still A pre-step-2 semantics — no
  partial state).
- Step 5: `thread/model/set` during the drain window is **also** rejected
  (same error family) — falsification if it succeeds while a queued message
  is still draining, since the daemon's `processing` flag covers the whole
  drain loop and there is no reachable turn boundary until it empties.
- Step 6: once idle, the identical `thread/model/set` call that was
  rejected in steps 4-5 now succeeds — confirms the rejection was state
  (turn-active), not a permanent failure.
- Falsification: tab 2 requires a manual reload to see model B; the picker
  is clickable while a turn is active; a switch during either the active
  turn or the queue-drain window mutates the session's model or profile.

## Cleanup

- Close both tabs.
- `curl -s -X POST ... "$HUB/s/$SID/shutdown"` if the session was spawned
  solely for this card.

## Sharp edges

- The busy signal `model-switch.js` tracks is `Status.Type == "active" &&
  ActiveTurnID != ""` — **not** `ActiveFlags` (serf daemons never populate
  it; only the codex mapping does). Don't assert on `ActiveFlags`.
- The queued-input rejection (step 5) is easy to miss if the drain is fast
  on a quick model — use a prompt with a few tool rounds or add a short
  `sleep`-style tool call to widen the window enough to land the RPC inside
  it.
