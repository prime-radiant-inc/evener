# web-model-switch-mid-session: two web clients converge on a live model switch; a mid-turn pick is accepted and queued

**What this covers**: spec `2026-07-12-model-switching-design.md` Acceptance
criteria 1 (two clients converge without reload). The mid-turn and
queued-input behavior is now the opposite of what this card used to assert:
the daemon **accepts** a pick while a turn runs and applies it at the next
turn boundary, so the running turn finishes on the model it started with. A
pick on a **cold** (exited) session also works in one action: the client
resumes the session, reads the resumed daemon's authority, and sends once (see
Sharp edges). Exercises the status strip's model switcher
(`panes/session/chrome/ModelSwitch.tsx`), the `thread/model/set` RPC
(`appwire/types_protocol.go#MethodThreadModelSet`), and the model-control
snapshot the daemon publishes for that session.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. The
switcher is `[data-testid="model-switch-trigger"]` with its readout in
`[data-testid="model-switch-value"]`, and the picker rows are the shared ARIA
combobox's `role="option"` items (`widgets/modelCatalog/index.tsx`).

**Read Sharp edges before step 4.** Two of them contradict earlier versions
of this card: the trigger is not disabled mid-turn, and the wire now refuses
a request that carries no model-control authority.

## Pre-state

- Hub running on an isolated `$HOME` and a kernel-assigned port (see the
  Setup checklist in `docs/developing-evener/agentic-testing.md`). That checklist's `$run`
  directory is where this card writes its scratch files too — never a fixed
  `/tmp/…` name a second concurrent run would clobber (kata `k2rx`). The hub
  needs a real `openai` and a real `anthropic` instance configured, because the
  card picks four distinct `provider/model` targets: A (spawn), B (the first UI
  pick), C (mid-turn), and D (the drain-window pick that must supersede C).
  Distinct ids matter for D in particular: step 7 is only falsifiable when the
  surviving pick names a different model from the one it beat.
- `superpowers-chrome:browsing` with multi-tab support, and a real SPA bundle
  (`make build-web`).
- A session spawned on model A (e.g. `openai/gpt-5.5`), idle. Capture `SID`.
- For the browser-free steps, a socket at `ws://127.0.0.1:$PORT/rpc` with
  `Authorization: Bearer $TOKEN`, driven the way
  `docs/developing-evener/agentic-testing.md`'s "Driving AppWire directly"
  describes: `initialize` before anything else, and every frame is
  `{"id":N,"method":…,"params":{…}}` with **no `jsonrpc` field** (that field
  is rejected and the socket closes). The announced version is the one this
  hub accepts, `appwire.ProtocolVersion` in `appwire/types_protocol.go`; the
  handshake compares it exactly, so copy it from the checkout under test
  rather than from an older card:
  ```json
  {"id":1,"method":"initialize",
   "params":{"protocolVersion":"evener-appwire-v8",
             "clientInfo":{"name":"scenario","version":"0"},
             "capabilities":{"experimentalApi":false}}}
  ```
  Read the session's authority before any pick; step 4's request must echo
  it. Send, and take the reply whose `id` is `2` — notifications for other
  threads are interleaved with your own replies:
  ```json
  {"id":2,"method":"thread/read","params":{"ref":"local:<SID>","includeTurns":false}}
  ```
  From that reply's `result.thread.evener`:
  - `modelControl.authority` is `{instanceId, bootGeneration}` — what step 4's
    `expectedInstanceId` and `expectedBootGeneration` echo.
  - `capabilities.modelControlVersion` must be `1` for this card; a session on
    an older daemon answers without it and refuses the mutation, which is out
    of scope here.

## Steps

1. **[browser] Open two tabs on the same session.**
   `navigate $HUB/auth/$TOKEN?next=/s/local:$SID` (tab 1), `new_tab`
   the same URL (tab 2). Confirm both tabs' model readouts show model A:
   ```javascript
   ({
     port: location.port,                        // page-identity check, always
     path: location.pathname,                    // /s/local:<SID>
     model: document.querySelector('[data-testid="model-switch-value"]')?.textContent,
     disabled: document.querySelector('[data-testid="model-switch-trigger"]')?.disabled,
   })
   ```
   The readout is `provider/model` (`chrome/statusFormat.ts`'s `modelLabel`).

2. **[browser]** In tab 1, click `[data-testid="model-switch-trigger"]`, then
   click the `role="option"` row for model B. `handlePick` closes the picker
   immediately and calls `threadsStore.getState().setModel(ref, provider,
   model)` (`ModelSwitch.tsx`'s `handlePick`).

3. **[browser] AC 1.** **Without reloading tab 2**, wait ~2s and re-run
   step 1's `eval` in tab 2.

4. **[browser-free] A mid-turn pick is accepted, not refused.** Start a turn
   that runs for a few seconds with AppWire `turn/start` and a unique
   `clientMutationId`, wait until `thread/read` reports
   `result.thread.status.type` as `active`, then send a pick for model C with
   the authority captured in Pre-state:
   ```json
   {"id":5,"method":"thread/model/set",
    "params":{"ref":"local:<SID>","modelProvider":"anthropic",
              "model":"claude-haiku-4-5-20251001",
              "clientMutationId":"<fresh-uuid>",
              "expectedInstanceId":"<auth.instanceId>",
              "expectedBootGeneration":"<auth.bootGeneration>"}}
   ```
   Capture the response. It is a success with `disposition: "accepted"`, and
   `modelControl.desired.lifecycle` names the pick's state (`pending` while
   the turn runs). The session's `active` model in that same snapshot is still
   model B: the running turn finishes on it.

5. **[browser, optional] The same pick through the UI.** Repeat step 4's
   timing, but drive tab 1's picker instead. Then read the toast region:
   ```javascript
   document.querySelector('section[aria-live="polite"][aria-label="Notifications"]')?.textContent
   ```
   A successful pick raises no error toast, and the readout shows the picked
   model with the turn still running.

6. **[browser-free] The queued-input case.** While the turn from step 4 is
   still active, queue a second message (`turn/queue` over `/rpc`, or Send in
   the composer — see `docs/developing-evener/agentic-testing.md`; there is no REST queue verb).
   Wait for the active turn to finish, then send a **different** target
   *during* the drain window — model D, not step 4's model C — with a fresh
   `clientMutationId`, and record that id as `$MUT_D`. It is accepted the same
   way, and `desired.clientMutationId` in its response is `$MUT_D` with a
   `requestSequence` above step 4's.

7. **[browser-free] The later pick wins.** Wait for the queue to fully drain to
   `idle`, then read the session again on the same socket:
   ```json
   {"id":9,"method":"thread/read","params":{"ref":"local:<SID>","includeTurns":false}}
   ```
   Take the reply whose `id` is `9`, and read from its `result.thread`:
   `modelProvider` and `evener.modelControl.active.model` (the model the daemon
   runs), and `evener.modelControl.desired` (the desired record, carrying
   `clientMutationId`, `requestSequence` and `lifecycle`).

   The active model is model D and the desired record is that pick's own
   `$MUT_D`, `applied`. Asserting the mutation id is what makes this step
   falsifiable: with one target used twice, an earlier pick left to apply
   produces the same final model, so the check passes while supersession is
   broken. Model D differs from model C for exactly that reason.

8. **[browser-free] A request with no authority is refused before admission.**
   Re-issue step 4's payload with `expectedBootGeneration` removed. The wire
   refuses it with `InvalidParams`, and a re-read shows no state change.

## Expected

- **Step 1**: both tabs read model A; `disabled` is `false`.
- **Step 2**: the picker closes immediately and tab 1's readout flips to the
  picked model.
- **Step 3 (AC 1)**: tab 2's readout has already updated to that model
  **without a reload** — driven by the daemon's model-control notification,
  which the shared reducer applies as a whole snapshot.
  Falsification: tab 2 needs a manual reload.
- **Step 4 (mid-turn)**: a successful response with `disposition: "accepted"`
  and a `desired` record whose lifecycle is `pending`, while `active` stays on
  model B. Falsification: a 409/conflict, or an `active` that moved mid-turn.
- **Step 5**: no error toast, and the readout shows the picked model while the
  turn runs. Falsification: a toast, or a readout that snaps back.
- **Step 6**: the drain-window pick for model D is accepted, and its response
  carries `$MUT_D`. Falsification: a conflict while queued input is still
  draining, or a response that echoes step 4's mutation id.
- **Step 7**: the daemon's `active` model is model D, and `desired` is
  `applied` under `$MUT_D`. Falsification: `active` on model C, or a `desired`
  record naming step 4's mutation id — either means the earlier pick applied and
  supersession is broken. A card that sends the same target twice cannot
  distinguish those from success.
- **Step 8**: `InvalidParams`, and no state change. This is the strict
  authority contract: a mutation without exact instance and boot generation
  never reaches the daemon.

## Cleanup

- Close both tabs.
- Shut the session down if it was spawned solely for this card, kill the hub
  by the PID you captured, and remove `$run`.

## Sharp edges

- **The trigger is NOT disabled mid-turn, and this card used to say it was.**
  The picker opens mid-turn, the pick is accepted, and the session queues it
  for the next turn boundary (`agent/session.go`'s `SetModel` queue path;
  `server/appwire_thread_settings.go`'s `handleAppThreadModelSet` has no
  processing gate). Asserting `disabled === true` mid-turn is a guaranteed
  false failure.
- **A mutation must carry authority.** `ThreadModelSetParams.Validate`
  requires `clientMutationId`, `expectedInstanceId`, and
  `expectedBootGeneration`; the daemon compares them against its own instance
  and boot generation and answers `authorityStale` when they do not match.
  Read them from `thread/read`'s `evener.modelControl.authority` immediately
  before the request, and never reuse them across a daemon restart.
- **`capabilities.changeModel` is true for a cold exited session too, and a
  pick there resumes it.** The hub advertises it for a session with no daemon
  (its cold read comes from `cmd/evener-hub/app_threadread.go`'s
  `pastThreadCapabilities`) and carries no model-control snapshot with it, so
  the client cannot send on the authority it holds. Each client therefore
  resumes first and sends once, fenced on the authority the read after the
  resume reports: the browser in `stores/threads.ts`'s `setModel`, the native
  app in `conversation.ts`'s `changeModel`. A pick on a session that was
  already exited therefore shows a resumed session and the picked model, not a
  refusal. Whether a pick is offered at all is one shared predicate,
  `modelPickCanSend` in `appwire-client/typescript/modelControl.ts`, which both
  clients and the browser's command palette call (`:103-115`): a `ready`
  session is offered, a cold (`ended`/
  `closed`/`notLoaded`) session's `unsupported` state is offered because the
  resume resolves it, and everything else is held. Two cases stay refused: a LIVE
  session whose daemon reports no model-control protocol has no resume that
  could authorize it, and `restartRequired` is a live daemon on an
  incompatible protocol whose `thread/resume` the hub refuses. A live-looking
  model chip on a finished session is the
  design. The hub serves model switching only through AppWire on `/rpc`; it
  registers no REST route for it, so a browser-free step cannot fall back to
  one. Drive those steps over `/rpc` and start from a live session: a
  `thread/model/set` on a session with no daemon has to resume it first, and
  that resume changes the very state the steps then assert on.
- The busy signal the daemon reads is its own `s.processing` — **not**
  `ActiveFlags` (Evener daemons never populate it). Don't assert on
  `ActiveFlags`.
- Widen the drain window (step 6) with a prompt that takes a few tool rounds;
  a fast model can drain before you land the call.
- Note the ref form throughout: `/s/local:$SID`. A bare `/s/$SID` renders
  "Page not found" client-side, by design.
