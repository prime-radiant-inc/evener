# ask-two-clients: two clients race to answer the same pending question

**What this covers**: spec §8 row `ask-two-clients.md` — the "no first-answer-wins
machinery needed" claim in §5.2/§6.1: the daemon's `turn/start` reservation is atomic, so
when two clients answer the same awaiting session exactly one reply becomes a real turn,
the loser is rejected rather than silently retried into a second user message, and both
clients converge on the same answered recap.

**Surface**: see `docs/agentic-testing.md` — "The REST surface, and what is no longer on
it" and "Driving the web UI with superpowers-chrome:browsing". Three mechanism changes
this card is rebuilt around, because they invert what it used to say:

- **The reservation is a single `if`**: `AcceptClientMutationStart` rejects with
  `Conflict("turn is already active")` when `snapshot.ActiveTurnID != ""`, inside the same
  atomic execute that sets it (`agent/session_client_mutation.go:206-208`). The rejection
  is stamped `mutationOutcome: "notAccepted"`, `retryDisposition: "none"`
  (`agent/session_client_mutation_queue.go:184-185`) — "never auto-retried" is a wire
  field now, not a client convention.
- **The losing tab does NOT drop text into the composer.** `sendAskAnswers` /
  `dropComposedTextIntoComposer` are gone with the vanilla frontend (`660376f78`).
  `askDockStore.sendBatch` (`composer/askDock/askDockStore.ts:243-269`) awaits only the
  **durable outbox enqueue**, then removes the batch and records its keys in
  `excludedKeys` forever (`:113-138`) — so the dock clears in both tabs before either
  `turn/start` has been answered. The rejection surfaces later, through the durable
  recovery path (`stores/mutationDispatcher.ts:104-108` →
  `Composer.tsx:271-297` / `composer/queue/QueueStrip.tsx:345-393`), never as an inline
  composer drop. `sendBatch`'s own `catch` + toast (`AskDock.tsx:295-306`) is for a
  **local enqueue** failure only; a lost race does not reach it.
- **Two tabs of one Chrome profile do not race.** The outbox is one origin-scoped
  IndexedDB (`stores/mutationOutboxIndexedDB.ts:31`, `"serf-mutation-outbox"`), drained
  serially per ref, so the two intents are dispatched back to back rather than
  concurrently. The genuinely simultaneous race is two *separate* clients — which is why
  the exact assertions moved to the REST half below.

Steps 1-3 are **browser-free** and carry the exact race assertions. Steps 4-7 need Chrome
and assert what each tab converges to.

## Pre-state

- Hub, `$HUB`, `$TOKEN`, and the isolated environment exactly as `ask-web-answer.md`'s
  Pre-state — reuse that card's hub if it is still running, otherwise re-run its
  Pre-state first. The handoff is its run directory, not a port
  (`docs/agentic-testing.md`, "Handing this hub to a sibling card"):
  ```bash
  run=${SERF_E2E_RUN:?run ask-web-answer.md's Pre-state first, then export SERF_E2E_RUN="$run"}
  export HOME="$run/home"
  unset XDG_STATE_HOME
  PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" | grep -oE '[0-9]+$' | tail -1)
  HUB=http://127.0.0.1:$PORT
  TOKEN=$(cat "$HOME/.serf/auth-token")
  HUBPID=$(cat "$run/hub.pid")
  kill -0 "$HUBPID" 2>/dev/null || { echo "that hub is gone — re-run ask-web-answer.md's Pre-state" >&2; exit 1; }
  ```
- `jq` and a shell that can background two `curl`s.
- For the browser half: `superpowers-chrome:browsing` with multi-tab support
  (`new_tab`/`switch_tab`/`list_tabs`), and your own Chrome profile claimed
  (`set_profile <worktree-name>`) before the first `use_browser` call.

## Steps

1. **(browser-free)** Spawn a session with one question and two clearly distinct options:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-ask-twoclients-XXXXX)
   body=$(jq -n --arg wd "$tmpdir" '{
     prompt: "Before doing any other work, call the ask_user tool once. Ask exactly one question: header \"Deploy\", question \"Deploy now or wait for review?\", with exactly two options: now (detail \"ship immediately\") and wait (detail \"hold for a second pair of eyes\"). Do not do anything else first.",
     model: "openai/gpt-5.5", working_dir: $wd, harness: "serf", branch: "", access_mode: "full", agent: "default", launch_overrides: {}
   }')
   SID=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$body" "$HUB/api/spawn" | jq -r '.session_id')
   for i in $(seq 1 60); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$st" = "awaiting" ] && break
     sleep 1
   done
   echo "SID=$SID state=$st"
   ```
2. **(browser-free)** Fire **two genuinely concurrent** answers as two separate clients,
   with deliberately different choices so the transcript shows unambiguously which one won.
   Both bodies are the exact `[answers]` form the dock composes (`askCompose.ts:84-93`):
   ```bash
   out=$(mktemp -d -t serf-e2e-ask-race-XXXXX)
   answer() { jq -n --arg l "$1" '{text: ("[answers]\n1. [Deploy] → \"" + $l + "\"")}'; }
   curl -s -o "$out/a.body" -w '%{http_code}' -X POST \
     -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d "$(answer now)"  "$HUB/api/sessions/local:$SID/send" > "$out/a.code" &
   curl -s -o "$out/b.body" -w '%{http_code}' -X POST \
     -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d "$(answer wait)" "$HUB/api/sessions/local:$SID/send" > "$out/b.code" &
   wait
   echo "A=$(cat "$out/a.code") $(cat "$out/a.body")"
   echo "B=$(cat "$out/b.code") $(cat "$out/b.body")"
   ```
3. **(browser-free)** Let the winning turn start, then finish, then read the durable record:
   ```bash
   state() { curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""'; }
   for i in $(seq 1 15); do [ "$(state)" = "active" ] && break; sleep 1; done   # turn claimed
   for i in $(seq 1 90); do [ "$(state)" = "active" ] || break; sleep 1; done   # turn done
   sleep 2   # let the transcript tail flush
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:8
   ```
4. **(browser)** Now repeat the gesture from two tabs, to check what each *client* shows.
   Spawn a second asking session the same way as step 1 (call it `SID2`), wait for
   `awaiting`, then:
   ```
   navigate $HUB/auth?token=<TOKEN>&next=/s/local:<SID2>     # tab 1
   new_tab  $HUB/auth?token=<TOKEN>&next=/s/local:<SID2>     # tab 2
   ```
   Confirm both tabs render `[data-ask-response-dock]` for the same question
   (`await_element` in each).
5. **(browser)** In **tab 1** pick `now`; in **tab 2** pick the other option, `wait`:
   ```javascript
   // run in each tab, with its own label
   document.querySelector('[data-ask-response-dock] input[aria-label="now"]').click()
   ```
6. **(browser)** Fire both sends back to back without waiting on either. Both tabs share
   one URL, so use `list_tabs` for indices rather than a URL/title match:
   ```javascript
   // active tab
   (() => {
     const dock = document.querySelector('[data-ask-response-dock]');
     [...dock.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Send answers').click();
     return 'clicked';
   })()
   ```
   ```
   list_tabs
   switch_tab <the other tab's index>
   ```
   then run the same `eval` there.
7. **(browser)** Wait ~5s for both tabs' live streams to settle, then read each tab:
   ```javascript
   ({
     port: location.port,
     dockStillOpen: !!document.querySelector('[data-ask-response-dock]'),
     composerHidden: document.querySelector('[data-testid="composer-input-card"]')?.hasAttribute('hidden'),
     recap: [...document.querySelectorAll('[data-testid="tool-call-item"][data-tool-name="ask_user"]')]
              .map((el) => el.textContent.match(/Asked: \[[^\]]*\][^\n]*/)?.[0]),
     composerDraft: document.querySelector('[data-testid="composer-input-card"] textarea[aria-label="Message"]')?.value,
     queueRows: [...document.querySelectorAll('li')].map((li) => li.textContent)
                  .filter((t) => /Delivery uncertain|Destination deleted|\[answers\]/.test(t)),
     toast: document.querySelector('section[aria-label="Notifications"]')?.textContent,
   })
   ```
   ```bash
   go run ./cmd/serf-doctor transcript "$SID2" --format outline --range last:8
   ```

## Expected

- **Step 2 (exact, the whole point)**: one request returns **202**; the other returns
  **409** with body `{"error":"turn is already active","code":-32013,"serf_error_info":"conflict"}`
  (`statusForWireError`, `cmd/serf-hub/web_api.go#statusForWireError`; `hubapi.ErrorResponse` tags at
  `hubapi/types.go#ErrorResponse`), **or** — if the capability gate saw the flip first — **503**
  with `serf_error_info: "actionUnavailable"` (`ensureThreadActionAvailable(…, "send")`
  ahead of `StartTurn`, `cmd/serf-hub/web_session.go:137-139`). Either loser shape is
  correct; two 202s is not. Falsify: both requests return 202, or the loser returns 502
  ("daemon unreachable") — a conflict must not be mistaken for an unavailable session and
  retried through the resume path (`shouldResumeAfterTurnStartError` →
  `isSessionUnavailableError`, `cmd/serf-hub/app_compact.go:12-29`, deliberately excludes
  `CodeConflict`).
- **Step 3 (exact)**: exactly **one** new user turn in the outline, containing either
  `→ "now"` or `→ "wait"` — never both, never a merge of the two, and never a second
  `[answers]` turn appearing later when the first turn ends (the loser was rejected, not
  parked).
- **Step 7 (both tabs)**: `dockStillOpen` is `false` and `composerHidden` is `false` in
  **both** tabs — a successful durable enqueue removes the batch in the sending tab
  (`askDockStore.ts:258`), and the winner's `[answers]` reply lands as a plain
  `userMessage`, which pushes the question behind `liveAskQuestions`' last-user-message
  boundary in the other (`askDock/deriveAskQuestions.ts:60-90`). `recap` in both tabs
  reads the identical `Asked: [Deploy] — answered: "<winner's choice>"` — the losing tab
  echoes the *winner's* answer, because the recap is computed from the shared transcript
  (`panes/session/askShared.ts:118-150`), not from local state.
- **Step 7 (the loser's own text)**: the rejected intent lands in the durable recovery
  surface — either restored into that tab's composer as an editable draft (`composerDraft`
  starts with `[answers]`; `Composer.tsx:271-297` only does this when the composer is
  empty and the projection refresh for this ref has landed) or listed as a recovery row
  with an **Edit message** action (`queueRows`; `QueueStrip.tsx:378-392`). Either is
  correct — both are inert, and neither ever becomes a second user turn. Falsify: the
  loser's text reappears as a second `[answers]` user turn in the outline, or vanishes
  with no recovery surface at all.

Falsification (whole card): if the second client's dock does not clear once the first
client's reply starts the turn, or a losing submit produces a second user message,
multi-client handling is broken.

## Cleanup

```bash
for sid in $SID $SID2; do
  curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
    -d '{}' "$HUB/api/sessions/local:$sid/shutdown" >/dev/null 2>&1
done
rm -rf "$tmpdir" "$out"
```
Leave the hub up if `ask-web-answer.md`'s run still needs it; otherwise kill it by the PID
that card captured and remove its run directory. The old `$HUB/s/$SID/shutdown` shim is
gone and 404s silently.

## Sharp edges

- **Which client wins is nondeterministic** — the daemon's atomic reservation resolves it
  server-side and scheduling jitter decides the order. Never assert on *which* option won;
  assert the invariants (one 202, one rejection, exactly one turn, both tabs converge).
- **Steps 4-7 are not a true race, and that is fine.** Two tabs in one Chrome profile share
  one IndexedDB outbox drained serially per ref, so tab 2's intent is dispatched after
  tab 1's response — it still conflicts (the daemon sets `ActiveTurnID` inside the same
  atomic execute that accepts, so the window is not raceable), but it is a *sequenced*
  conflict, not a simultaneous one. Step 2 is where the concurrency assertion lives. If
  you want two genuinely independent browser clients, they need two Chrome **profiles**,
  not two tabs.
- **The loser's rejection is not a toast.** `AskDock`'s toast path fires only when the
  local durable enqueue itself rejects (`askDockStore.ts:260-268`, message
  `Couldn't send answers: …`). A `turn/start` conflict happens after the enqueue resolved,
  so a toast here means something else failed — read it, don't ignore it, but it is not
  this scenario's expected loser signal.
- **A dock that stays up in the losing tab after ~5s is a real failure**, but check the
  cause before filing: `excludedKeys` makes the sending tab's own removal permanent
  (`askDockStore.ts:44-47`), while the non-sending tab depends on the winner's
  `userMessage` actually reaching it over the socket. If neither tab's dock cleared, the
  socket did not hydrate — check `$run/hub.log` and confirm `location.port` matches your
  hub before suspecting the ask feature.
- If both tabs appear to hang on the original question past ~5s, check the hub is still
  alive and that the session's state actually left `awaiting` — a hung hub, not the ask
  feature, is the likelier cause.
