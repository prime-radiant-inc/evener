# ask-two-clients: two web clients race to answer the same pending question

**What this covers**: spec §8 row `ask-two-clients.md` — the "no first-answer-wins
machinery needed" claim in §5.2/§6.1: the daemon's `turn/start` reservation is atomic, so
when two clients answer the same awaiting session, exactly one reply becomes a real turn,
the loser's card converges to the same settled echo (once its live stream catches up), and
a losing submit is never silently retried into a second user message.

## Pre-state

- Hub + credentials as `ask-web-answer.md` (reuse if still running on `127.0.0.1:9280`).
- `superpowers-chrome:browsing` with multi-tab support (`new_tab`/`switch_tab`/`list_tabs`).

## Steps

1. Spawn a session with one question, two clearly distinct options:
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
2. Open **two** browser tabs on the same session:
   ```
   navigate http://127.0.0.1:9280/auth?token=<TOKEN>&next=/s/<SID>      # tab 1
   new_tab http://127.0.0.1:9280/auth?token=<TOKEN>&next=/s/<SID>       # tab 2
   ```
   Confirm both tabs show `[data-ask-card]` for the same question (`await_element` in each).
3. In **tab 1**, pick `now`. In **tab 2**, pick the *other* option, `wait` — deliberately
   different choices so the transcript unambiguously shows which reply won:
   ```javascript
   // tab 1
   document.querySelector('[data-ask-option][data-option-label="now"] [data-ask-option-input]').click();
   ```
   ```javascript
   // tab 2 (switch_tab first)
   document.querySelector('[data-ask-option][data-option-label="wait"] [data-ask-option-input]').click();
   ```
4. Fire both sends back-to-back, without waiting on either to settle first (maximizes the
   chance both `turn/start` calls are in flight together, mirroring the synchronous
   race-capture pattern in `docs/agentic-testing.md`). Both tabs share the identical URL, so
   use `list_tabs` to get each tab's index rather than a URL/title match:
   ```
   click [data-ask-send-btn]     # on tab 2 (currently active)
   list_tabs                     # note tab 1's index — both tabs share one URL
   switch_tab <tab 1's index from list_tabs>
   click [data-ask-send-btn]     # on tab 1
   ```
5. Wait ~3s for both tabs' live streams to settle, then inspect each tab and the transcript:
   ```javascript
   // each tab
   (() => {
     const card = document.querySelector('[data-ask-card]');
     const settled = document.querySelector('.ask-settled-line');
     const composerDraft = document.querySelector('form[data-input-form] .message-input')?.value || '';
     return { cardStillOpen: !!card, settledText: settled ? settled.textContent : null, composerDraft };
   })()
   ```
   ```bash
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:6
   ```

## Expected

- Step 3: each tab's `[data-ask-answered-count]` reads `1 of 1 question answered` with its
  own choice checked — the two tabs' in-progress answers are independent local state until
  submit.
- Step 5: exactly **one** new `USER_INPUT` turn appears in the transcript, containing either
  `→ "now"` or `→ "wait"` (never both, never a corrupted merge of the two).
- Step 5 (DOM): the **winning** tab shows `.ask-settled-line` echoing its own choice. The
  **losing** tab converges to the identical `.ask-settled-line` (echoing the *winner's*
  reply, once its live stream delivers the `USER_INPUT` broadcast — `resolvePendingAsk`
  fires "on every USER_INPUT... from this client or another," per spec §6.1) — its
  `[data-ask-card]` is gone either way. The losing tab's composer **may** still hold its own
  composed text as an inert draft (the Conflict-recovery drop, spec: "never auto-retried")
  but that text must **not** appear as a second `USER_INPUT` turn in the transcript.
- Falsification: if the second client's form does not collapse once the first client's
  reply starts the turn, or a losing/stale submit produces a second user message,
  multi-client handling is broken.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
pkill -f serf-hub-ask
rm -rf "$tmpdir" /tmp/serf-ask /tmp/serf-hub-ask
```

## Sharp edges

- **Which tab wins is nondeterministic** — the daemon's atomic reservation resolves the race
  server-side, and network/JS-scheduling jitter decides which `turn/start` call arrives
  first. Don't assert on *which* option won; assert on the invariants (exactly one turn,
  both tabs converge, no duplicate).
- **The two ways a "losing" tab can look right after the race** depend on ordering that this
  scenario does not control: if the Conflict error returns to the losing tab *before* the
  winning tab's `USER_INPUT` broadcast arrives, the losing tab's `sendAskAnswers` catch
  branch drops its composed text into the composer (`dropComposedTextIntoComposer`) while
  the card is still showing the (now-stale) form for a moment; if the broadcast arrives
  first, `resolvePendingAsk` has already nulled `pendingAsk` and the losing tab's own
  Conflict handling short-circuits (`if (this.pendingAsk !== pa) return;`) before it ever
  touches the composer. Either interleaving satisfies the falsification line — check the
  *converged* state after step 5's wait, not the instant right after the clicks.
- A `turn/start` Conflict is a normal, expected outcome of this scenario, not a bug — do not
  treat the `Hub steer error`-style banner path as a failure; that banner is only for
  *unexpected* send errors, and the conflict path here is explicitly routed to the composer
  drop instead, per `sendAskAnswers`'s dedicated `err.serfErrorInfo === "conflict"` branch.
- If both tabs appear to hang on the original form past ~5s, check `/tmp/serf-hub-ask` is
  still alive and that `SID`'s state actually left `awaiting` — a hung hub, not the ask
  feature, is the likelier cause.
