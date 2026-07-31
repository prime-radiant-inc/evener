# web-model-switch-mid-session: two web clients converge on a live model switch; mid-turn and queued-input switches stay rejected

**What this covers**: spec `2026-07-12-model-switching-design.md` Acceptance
criteria 1 (two clients converge without reload) and 4 (mid-turn switch
rejected, no state change), plus the Failure-modes row "Switch while
messages are queued" (rejected until the queue drains — the daemon's
`processing` flag covers the whole drain loop, so there is no reachable turn
boundary until it empties). Exercises the status strip's model switcher
(`panes/session/chrome/ModelSwitch.tsx`), the `thread/model/set` RPC
(`appwire/types.go:19`), and the `thread/model/changed` broadcast (`:94`).

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. The
`[data-model-trigger]` / `[data-model-display]` selectors and
`assets/model-switch.js` this card used to drive died with the vanilla
frontend (`660376f78`); there is no `SerfAppwire.setModel` to call. The
switcher is `[data-testid="model-switch-trigger"]` with its readout in
`[data-testid="model-switch-value"]` (`ModelSwitch.tsx:129,137`), and the
picker rows are the shared ARIA combobox's `role="option"` items
(`widgets/modelCatalog/index.tsx:251,291`), not `.chip-picker-model`.

**Read Sharp edges before step 4** — the trigger is no longer disabled
mid-turn, and this card previously asserted that it was.

## Pre-state

- Hub running on an isolated `$HOME` and a kernel-assigned port (see the
  Setup checklist in `docs/agentic-testing.md`), with a real `openai` and a
  real `anthropic` instance configured (or two distinct catalogued models on
  one instance — any two models with different `provider/model` ids work for
  the assertion).
- `superpowers-chrome:browsing` with multi-tab support, and a real SPA bundle
  (`make build-web`).
- A session spawned on model A (e.g. `openai/gpt-5.5`), idle. Capture `SID`.

## Steps

1. **[browser] Open two tabs on the same session.**
   `navigate $HUB/auth?token=$TOKEN&next=/s/local:$SID` (tab 1), `new_tab`
   the same URL (tab 2). Confirm both tabs' model readouts show model A:
   ```javascript
   ({
     port: location.port,                        // page-identity check, always
     path: location.pathname,                    // /s/local:<SID>
     model: document.querySelector('[data-testid="model-switch-value"]')?.textContent,
     disabled: document.querySelector('[data-testid="model-switch-trigger"]')?.disabled,
   })
   ```
   The readout is `provider/model` (`chrome/statusFormat.ts:55-57`'s
   `modelLabel`).

2. **[browser]** In tab 1, click `[data-testid="model-switch-trigger"]`, then
   click the `role="option"` row for model B. `handlePick` closes the picker
   optimistically and calls `threadsStore.getState().setModel(ref, provider,
   model)` (`ModelSwitch.tsx:101-112`).

3. **[browser] AC 1.** **Without reloading tab 2**, wait ~2s and re-run
   step 1's `eval` in tab 2.

4. **[browser-free] AC 4 — the mid-turn rejection, exactly.** Start a turn
   that runs for a few seconds (`POST /api/sessions/local:$SID/send`), wait
   until `state` is `active` **and** `active_turn_id` is non-empty, then
   attempt the switch over REST and capture status + body:
   ```bash
   curl -s -o /tmp/model-reject.json -w "%{http_code}\n" \
     -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"model":"anthropic/claude-haiku-4-5-20251001"}' \
     "$HUB/api/sessions/local:$SID/model"
   cat /tmp/model-reject.json
   # Re-read the session and confirm its model did NOT move.
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq .model
   ```
   `handleAPIModel` (`cmd/serf-hub/web_api.go:304-344`) forwards
   `ThreadModelSetParams` to the same source the browser's RPC reaches.

5. **[browser, optional] The same rejection through the UI.** Repeat step 4's
   timing, but drive tab 1's picker instead. Then read the toast region:
   ```javascript
   document.querySelector('section[aria-live="polite"][aria-label="Notifications"]')?.textContent
   ```

6. **[browser-free] Queued-input case.** While the turn from step 4 is still
   active, queue a second message (`turn/queue` over `/rpc`, or Send in the
   composer — see `docs/agentic-testing.md`; there is no REST queue verb).
   Wait for the first turn to finish, then re-issue step 4's `curl` *during*
   the drain window and capture the result the same way.

7. **[browser-free]** Wait for the queue to fully drain to `idle`, then
   re-issue the identical `curl` from step 4 and confirm it now succeeds.

## Expected

- **Step 1**: both tabs read model A; `disabled` is `false` on an idle
  session with `capabilities.changeModel`.
- **Step 2**: the picker closes immediately and tab 1's readout flips to
  model B.
- **Step 3 (AC 1)**: tab 2's readout has already updated to model B **without
  a reload** — driven by the `thread/model/changed` notification, which the
  reducer applies wholesale (`protocol/reducer.ts:685-700`; note it
  *replaces* `reasoningEffortLevels`/`supportsReasoning` rather than patching
  them, so an empty ladder on the new model clears the old one's).
  Falsification: tab 2 needs a manual reload to see model B.
- **Step 4 (AC 4, exact)**: HTTP **409** with a JSON body whose `error` is
  the daemon's own `turn <reservedTurnID> is active`
  (`server/appwire_runtime.go:820-831`; `appwire.Conflict` →
  `CodeConflict` → 409 via `statusForWireError`, `web_api.go:110-125`). The
  re-read `model` is unchanged — no partial state. Falsification: 204, or a
  200 with the model mutated, or a 5xx that hides which layer refused.
- **Step 5**: the toast region contains `Couldn't change model:` followed by
  the same server detail (`ModelSwitch.tsx:110`,
  `protocol/errors.ts:63-67`). Falsification: the picker's click silently
  does nothing, or the model changes.
- **Step 6**: the switch during the drain window is **also** rejected with
  the same 409 family. The daemon re-arms `SetProcessing(true)` for each
  drained turn (`cmd/serf/serve.go:950-958`), and `ProcessInputKind` loops to
  run queued messages as further turns (`agent/session_lifecycle.go:517-524`),
  so `handleAppThreadModelSet`'s `processing || reservedTurnID != ""` guard
  (`server/appwire_runtime.go:820-831`) stays true across the whole drain.
  Falsification: it succeeds while a queued message is still draining.
- **Step 7**: once idle, the identical call that was rejected in steps 4 and 6
  returns 204 — confirms the rejection was state (turn-in-flight), not a
  permanent failure.

## Cleanup

- Close both tabs.
- `curl -s -X POST -H "Authorization: Bearer $TOKEN" -d '{}' \
  "$HUB/api/sessions/local:$SID/shutdown"` if the session was spawned solely
  for this card. Kill the hub by the PID you captured; remove `$run`.

## Sharp edges

- **The trigger is NOT disabled mid-turn any more, and this card used to say
  it was.** `disabled = !model.capabilities.changeModel` is the ONLY gate
  (`ModelSwitch.tsx:73`), and the comment above it (`:63-72`) explains why the
  client-side turn predicate was removed: it made the client guess at an
  answer only the daemon has, and it took the switch away from every cold
  session too. The picker opens mid-turn, the pick fires, and the daemon
  answers Conflict, which `handlePick` surfaces as a toast. So the AC-4
  assertion is "the switch is refused and nothing changed", never "the
  control is inert". Asserting `disabled === true` mid-turn is now a
  guaranteed false failure.
- **Because of that, the exact AC-4 assertion belongs at the REST/wire level,
  not in the DOM.** Step 4 is browser-free and gives a status code and a
  message; step 5's toast is the qualitative confirmation that the refusal
  reaches a human.
- **`capabilities.changeModel` is true for an ENDED session too.** The hub
  advertises Send and ChangeModel for a cold exited thread and resumes it
  behind `thread/model/set` (`cmd/serf-hub/app_model.go:11-26`'s
  `setThreadModelWithResume`). A live-looking model chip on a finished
  session is the design. Note that the *REST* route refuses a non-live
  session earlier, with `404 session not live` (`web_api.go:309-312`) — so
  the two paths legitimately differ here, and step 4's curl must run against
  a live session.
- The busy signal the daemon reads is its own `s.processing` plus
  `s.appReservedTurnID` (`server/appwire_runtime.go:820-824`) — **not**
  `ActiveFlags` (serf daemons never populate it; only the codex mapping
  does). Don't assert on `ActiveFlags`.
- The queued-input rejection (step 6) is easy to miss if the drain is fast
  on a quick model — use a prompt with a few tool rounds, or the AGENTS.md
  pacing trick in `docs/agentic-testing.md`, to widen the window enough to
  land the call inside it.
- Note the ref form throughout: `/s/local:$SID`. A bare `/s/$SID` renders
  "Page not found" client-side, by design.
