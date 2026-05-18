# web-steer-live-turn: inject steering via the web UI mid-turn

**What this covers**: kata `a08v`. The workspace exposes two paths to
inject a steering message into the live model loop: the input-area
`steer` button (`renderer.js:1131`) and the `/steer` palette command
(`search.js:244`). Both POST to `/s/<id>/steer` (REST shim) or call
appwire `turn/steer` and rely on `activeTurnId` being populated by
the live stream. This scenario drives both paths against a real
model and verifies the daemon writes a `STEERING` transcript entry,
the conversation pane renders the steering divider
(`renderer.js:989` `appendSteeringMessage`), and the model's next
output is observably influenced. The jstest suite already covers
button-empty / palette-no-active-turn rejection
(`test-input-area.js`, `test-search-commands.js`); this is the
server-side end-to-end counterpart.

## Pre-state

- Hub running on `0.0.0.0:9180` with `--serf` resolvable.
- OpenAI OAuth signed in (`./serf openai status` shows
  `source=oauth`).
- `~/.serf/auth-token` readable.
- A Chrome session that can authenticate against the hub (visit
  `/auth?token=<auth-token>&next=/s/<sid>` once to set the cookie).

## Steps

Set up shared state:

```bash
tmpdir=$(mktemp -d -t serf-e2e-steer-XXXXX)
TOKEN=$(cat ~/.serf/auth-token)
HUB=http://localhost:9180
```

1. **Drop a pacing AGENTS.md into the workspace.** Without this the
   model finishes a 5-paragraph essay in ~10s, well before a human
   (or browser-driving agent) can open the palette and type a steer.
   This file forces several `exec_command bash -c 'sleep N'` calls
   per turn, stretching the active-turn window to ~60-120s:
   ```bash
   cat > "$tmpdir/AGENTS.md" <<'EOF'
   # Working agreement

   For every user request, follow these procedural rules EXACTLY:

   1. Pause between every action by calling exec_command with
      bash -c "sleep 8".
   2. After every model thought, call exec_command bash -c "sleep 8"
      BEFORE composing the next part of your reply.
   3. Insert these sleep pauses between EVERY paragraph and EVERY
      tool call. There must be at least 4 sleep calls per turn.
   4. Always think carefully and methodically; never rush.
   EOF
   ```

2. **Spawn** with `openai/gpt-5.4-mini`:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"Read AGENTS.md if it exists in your cwd. Then write a long 5-paragraph essay about software engineering practices, in slow careful detail.\",\"model\":\"openai/gpt-5.4-mini\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['session_id'])")
   echo "SID=$SID"
   ```

   This first turn may finish before you can steer it (the agent
   does not always read AGENTS.md on the very first prompt). Wait
   for `state=idle`, then send a SECOND turn that explicitly cites
   the pacing rules — that turn will reliably stay processing long
   enough to steer:
   ```bash
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 2
   done
   curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"Re-read AGENTS.md in your cwd (mandatory). Then write a long, careful 5-paragraph essay about software engineering practices. Follow the pacing rules in AGENTS.md exactly — insert exec_command sleep calls between every paragraph. This must take at least a minute."}' \
     "$HUB/s/$SID/send" &
   # wait until the turn is actually active
   for i in $(seq 1 30); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     turn=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('active_turn_id',''))")
     [ "$state" = "processing" ] && break
     sleep 1
   done
   echo "state=$state turn=$turn"
   ```

3. **Authenticate the browser** and load the workspace:
   ```
   /auth?token=<TOKEN>&next=/s/<SID>
   ```
   The auth endpoint sets the `serf-auth` cookie and redirects.
   Verify the live stream has caught up:
   ```javascript
   const conv = document.querySelector("[data-role='conversation']");
   ({ state: conv.dataset.state, activeTurnId: conv.dataset.activeTurnId });
   // { state: "processing", activeTurnId: "turn_<n>" }
   ```
   Confirm the input-area `steer` button is enabled:
   ```javascript
   const b = document.querySelector("[data-steer-trigger]");
   ({ disabled: b.disabled, html: b.outerHTML });
   // { disabled: false, html: "<button … data-steer-trigger=…>steer</button>" }
   ```

4. **PATH A — direct button click** (`renderer.js:1131`). Type the
   steer text into the input-area textarea, then click the steer
   button. This is the canonical UX surfaced in the workspace
   chrome:
   ```javascript
   const ta = document.querySelector("textarea.message-input");
   ta.focus(); ta.value = "Make it 1 paragraph instead of 5. Make it about Go testing specifically.";
   ta.dispatchEvent(new Event("input", { bubbles: true }));
   document.querySelector("[data-steer-trigger]").click();
   ```
   Then check:
   ```javascript
   ({
     taVal: document.querySelector("textarea.message-input").value,
     steerCount: document.querySelectorAll(".steering").length,
     steerText: document.querySelector(".steering")?.textContent,
   })
   // taVal: ""  (cleared)
   // steerCount: 1
   // steerText: "↻ steering injectedMake it 1 paragraph instead of 5. …"
   ```

5. **PATH B — ⌘K palette command** (`search.js:244`). Wait for
   another long-running turn (re-send the slow prompt to get a
   fresh `active_turn_id`), then dispatch Cmd/Ctrl+K, type
   `/steer`, Enter, then the steer text, Enter:
   ```javascript
   // open palette
   document.dispatchEvent(new KeyboardEvent("keydown", {
     key: "k", ctrlKey: true, bubbles: true, cancelable: true
   }));
   // dialog opens; type /steer, Enter (selects "Steer model" command,
   // enters args mode with pill "Steer model" + placeholder "steer text…")
   // type body text, Enter to submit
   ```
   After submit the dialog closes and (with `activeTurnId` still
   populated) a new `.steering` divider appears in the conversation
   pane.

6. **Wait for the turn to settle** to idle and inspect the
   transcript:
   ```bash
   for i in $(seq 1 90); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 2
   done
   TFILE=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   python3 - <<EOF
   import json
   for i, line in enumerate(open("$TFILE")):
       j = json.loads(line)
       t = j.get("turn", {})
       if t.get("kind") == "STEERING":
           for c in t.get("message", {}).get("content", []):
               if c.get("kind") == "text":
                   print(f"[{i}]", c.get("text", "")[:200])
   EOF
   ```

## Expected

- **Step 3 (browser hydration)**: live stream sets
  `data-state="processing"` and `data-active-turn-id="turn_<n>"` on
  the conversation pane; `[data-steer-trigger].disabled === false`.
  Falsification: button stays disabled while server shows
  `state=processing` (would mean the live stream isn't propagating
  `THREAD_STATUS_CHANGED` or `TURN_STARTED`).
- **Step 4 (PATH A)**: button click POSTs to `/s/<sid>/steer`
  (or `turn/steer` via appwire) with the textarea body. Hub returns
  `204 No Content`. The textarea clears (`renderer.js:1161`). A
  `<details class="steering">` is appended to the conversation pane
  with summary text `"↻ steering injected"` and body
  `"Make it 1 paragraph instead of 5. …"`. Falsification: textarea
  retains its text, no `.steering` element appears, OR an `error`
  banner is appended with title `Hub steer error`.
- **Step 5 (PATH B)**: palette closes after second Enter; same
  `.steering` divider appears with the palette-supplied text.
  Falsification: palette closes but no divider appears AND no
  error banner — implies the palette swallowed the steer (likely
  because `activeTurnId()` was empty at submit time, see Sharp
  edges). The system should at least surface the unavailable error.
- **Step 6 (transcript)**: the transcript contains a `kind=STEERING`
  entry whose `message.content[0].text` exactly equals the steer
  text from step 4 (and another from step 5 if both paths were
  exercised). The model's NEXT assistant message after the
  STEERING entry is observably influenced — for the PATH A text
  above, the assistant produces a single short paragraph about Go
  testing (not the 5-paragraph software-engineering essay it was
  originally asked for). Session state ends at `idle` with
  `live=true`; it does NOT terminate. `turn_count` advances by one
  for the user-input that started the turn (steering itself does
  not count as a turn). Falsification: no STEERING entry in the
  transcript, OR the model's next output is the original 5-paragraph
  essay unchanged, OR the session reaches `state=ended`/`closed`
  after the steer (the steer would have terminated the turn).

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
# Optional: drop the persisted artifacts.
# find ~/.local/state/serf/projects -name "$SID*" -delete
```

## Sharp edges

- **The first turn often races to idle before you can steer**. A
  5-paragraph essay against `gpt-5.4-mini` takes ~10-15s without
  pacing. The fix in step 1 is to drop an `AGENTS.md` with explicit
  sleep-between-paragraphs rules, then send a follow-up turn that
  cites those rules. This stretches each turn to 60-120s. If you
  see `state=idle` immediately after sending, the model didn't
  honour the pacing rules — try a stronger reminder or a slower
  model.
- **Button vs palette discoverability**. The input-area `steer`
  button is the canonical UX: it sits next to send/attach, becomes
  active automatically when there's an in-flight turn, and reuses
  whatever's already in the textarea. The palette command (`/steer`)
  works but requires three keystroke phases (Cmd+K → `/steer` Enter
  → body Enter). For typical interactive use prefer the button;
  the palette is useful for keyboard-only workflows or when the
  input area is offscreen.
- **`activeTurnId` is required for both paths**. The button reads
  `this.activeTurnId` (`renderer.js:1143`); the palette reads
  `activeTurnId()` from the conversation `data-active-turn-id`
  attribute (`search.js:171,247`). If the turn ends BETWEEN opening
  the palette and pressing Enter, the palette closes silently —
  there is currently NO visible error banner in that race
  (`showTurnActionUnavailable` shows a transient toast, but the
  dialog still closes). The button path always shows an inline
  banner with `title="Hub steer error"` because it sets
  `disabled=true` synchronously and falls into the catch branch.
- **`appendSteeringMessage` classifier suppresses some steers**
  (`renderer.js:989-1037`). Daemon-generated current-task nudges
  (`<TASK current_id="N" title="…">`) and the task-list reminder
  rendered as `<SYSTEM-REMINDER>You have a task_list tool…` are
  routed to `system-line` widgets or suppressed entirely. Use
  plain-prose steer text (no `<TASK>` / `<SYSTEM-REMINDER>` /
  task-list markers) to hit the default `details.steering`
  rendering. The transcript entry is unaffected by the classifier —
  it always lands as `kind=STEERING`.
- **The browser hydration window after a long-running navigate**.
  If you load `/s/<sid>` while the daemon is mid-turn, the
  rendered page reflects the server-side snapshot at request time
  AND the live stream catches up via SSE/appwire within a few
  hundred ms. If you check `data-active-turn-id` immediately after
  navigate it may be empty; wait for the next event or reload once
  the server reports `state=processing`.
- **STEERING entry vs `turn_count`**. `turn_count` (in
  `<sid>.meta.json` and `/api/sessions/local:<sid>`) counts
  committed user→assistant exchanges. A steering injection writes a
  transcript line but does NOT increment `turn_count`. Don't confuse
  transcript line counts with turn counts when verifying.
- **Steering does not terminate the active turn**. Unlike
  `/interrupt` (which cancels the in-flight context), `/steer`
  injects a system-reminder into the model's running loop. The
  current turn keeps going; the model sees the steer in its next
  round and adapts. The session stays `processing` until the model
  decides to call `communicate`.
