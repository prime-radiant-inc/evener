# web-queue-then-completes: type during a turn, queued message runs after

**What this covers**: kata `111a` (web). While the session is
PROCESSING the workspace composer's send button is gated off and the
queue capability is on. Pressing Enter / ⌘↵ in the composer routes
the message through `POST /s/<id>/queue` (or appwire `turn/queue`)
instead of `turn/start`. The daemon enqueues the text and the
renderer mirrors it in the queue-preview chrome
(`renderer.js` `pendingQueue` + `renderQueuePreview`). When the
active turn completes the daemon's outer ProcessInput loop pops the
head of the queue and runs it as a fresh user turn — at which point
the `USER_INPUT` event arrives over the live stream and the
renderer's `popQueueMatch` removes the matching preview row.

This is the user-facing complement to the agent-level
TestSession_Enqueue_DrainsAfterTurnCompletes unit (kata 111a
backend) and to the TUI scenario (parallel subagent).

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
tmpdir=$(mktemp -d -t serf-e2e-queue-XXXXX)
TOKEN=$(cat ~/.serf/auth-token)
HUB=http://localhost:9180
```

1. **Drop a pacing AGENTS.md.** Same trick as `web-steer-live-turn`:
   the queue affordance is only observable while the session is
   processing, so the first turn has to be long enough for a human
   (or browser-driving agent) to type into the composer and press
   Enter:
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
   If the first turn races to idle (gpt-5.4-mini sometimes skips
   AGENTS.md on the very first prompt), wait for `idle` then send a
   follow-up prompt that cites the pacing rules explicitly — see
   `web-steer-live-turn.md` step 2 for the exact follow-up command.

3. **Authenticate the browser and load the workspace** at
   `/auth?token=<TOKEN>&next=/s/<SID>`. Wait for the live stream:
   ```javascript
   const conv = document.querySelector("#conversation");
   ({ state: conv.dataset.state, activeTurnId: conv.dataset.activeTurnId });
   // { state: "active", activeTurnId: "turn_<n>" }
   ```
   Verify the send button advertises queue capability and is
   disabled (because send is gated off mid-turn):
   ```javascript
   const send = document.querySelector(".send-btn");
   ({
     send: send.getAttribute("data-capability-send"),
     queue: send.getAttribute("data-capability-queue"),
     disabled: send.disabled,
   });
   // { send: "false", queue: "true", disabled: false }
   ```
   (When both `send=false` AND `queue=false` the button stays
   disabled, but with `queue=true` the form submission is rerouted
   to `/queue` and the button is enabled so the user can press
   Enter ⌘↵.)

4. **Type a queue message** into the composer and press ⌘↵:
   ```javascript
   const ta = document.querySelector("textarea.message-input");
   ta.focus();
   ta.value = "after this finishes, write a haiku about Go testing";
   ta.dispatchEvent(new Event("input", { bubbles: true }));
   ta.dispatchEvent(new KeyboardEvent("keydown", {
     key: "Enter", ctrlKey: true, bubbles: true, cancelable: true,
   }));
   // wait a beat for fetch to resolve
   await new Promise(r => setTimeout(r, 300));
   const preview = document.querySelector("[data-queue-preview]");
   const depth = preview.querySelector("[data-queue-depth]").textContent;
   const rows = preview.querySelectorAll(".queue-preview-item");
   ({
     hidden: preview.hidden,
     depth,
     rowText: rows[0] && rows[0].textContent,
     taValue: ta.value,
   });
   // { hidden: false, depth: "1",
   //   rowText: "#1after this finishes, write a haiku about Go testing",
   //   taValue: "" }
   ```
   Confirm the daemon side reports `queue_depth>=1`. There is no
   public REST surface for queue depth today, so confirm via the
   appwire `thread/read` capability bit — `caps.queue` should
   remain `true` (capability is gated on the session being mid-turn,
   not on the depth being zero):
   ```bash
   # Daemon-side confirmation: re-read the active thread. The
   # capability stays advertised; the daemon does NOT expose depth
   # over the public API today (see Sharp edges).
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | python3 -c "import json,sys; d=json.load(sys.stdin); print('state=',d['state'],'queue_cap=',d['capabilities'].get('queue'))"
   # state= active queue_cap= True
   ```

5. **Wait for the original turn to settle, then verify the queued
   message ran as a fresh user turn**:
   ```bash
   # The queued message is processed after the active turn completes.
   # State briefly returns to processing (the popped queue entry
   # becomes a new user turn) then back to idle.
   for i in $(seq 1 180); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     tc=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('turn_count'))")
     [ "$state" = "idle" ] && [ "$tc" -ge 2 ] && break
     sleep 2
   done
   echo "settled state=$state turn_count=$tc"
   ```
   Then back in the browser:
   ```javascript
   const userMessages = document.querySelectorAll(".user-message");
   const previewNow = document.querySelector("[data-queue-preview]");
   ({
     userCount: userMessages.length,
     lastUserText: userMessages[userMessages.length - 1].textContent,
     previewHidden: previewNow.hidden,
     previewDepth: previewNow.querySelector("[data-queue-depth]").textContent,
   });
   // { userCount: >= 2, lastUserText: "…haiku about Go testing…",
   //   previewHidden: true, previewDepth: "0" }
   ```

6. **Transcript check** — the queued message must land as `USER`
   on a fresh turn, not as `STEERING`:
   ```bash
   TFILE=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   python3 - <<EOF
   import json
   for i, line in enumerate(open("$TFILE")):
       j = json.loads(line)
       t = j.get("turn", {})
       if t.get("kind") == "USER":
           for c in t.get("message", {}).get("content", []):
               if c.get("kind") == "text":
                   print(f"[{i}] USER:", c.get("text", "")[:160])
   EOF
   ```

## Expected

- **Step 3 (workspace hydration)**: while the original turn is
  in flight, the workspace template renders the send button with
  `data-capability-send="false"` AND `data-capability-queue="true"`.
  The button is enabled (the queue path keeps Enter live) and the
  queue-preview container is hidden because the local mirror is
  empty. Falsification: send is disabled with both attributes
  `false`, or queue is `false` (would mean
  `Capabilities.Queue` is not threaded through
  `hubCapabilitiesFromAppwire`).
- **Step 4 (queue submit)**: ⌘↵ POSTs to `/s/<SID>/queue` (or
  appwire `turn/queue`); daemon returns 204. The preview container
  loses its `[hidden]` attribute and shows one row with the
  truncated message text and depth=1. The textarea is cleared.
  Falsification: ⌘↵ POSTs to `/send` (would mean the submit handler
  did not honor `data-capability-queue`), or the daemon returns 409
  (would mean the session was not actually processing), or the
  preview stays hidden (would mean `renderQueuePreview` was not
  called or `pendingQueue` not updated).
- **Step 5 (drain)**: when the active turn settles to idle the
  daemon pops the queue head and starts a fresh user turn — state
  briefly goes back to processing, then to idle, with `turn_count`
  incrementing by one. The renderer's `USER_INPUT` handler matches
  the queued text and removes the preview row, leaving the
  container hidden. Falsification: `turn_count` does not advance
  (queued message was dropped), the queue-preview row stays
  visible after the queued message completes (the popQueueMatch
  didn't fire), or `state` reports the new turn before the
  original one finishes (would mean the daemon's outer loop did
  not block on followup-queue exhaust).
- **Step 6 (transcript)**: the queued text appears as a
  `kind=USER` turn, NOT as a STEERING entry. The assistant's reply
  to that USER turn references the queued instruction (e.g. a
  haiku about Go testing). Falsification: the queued text only
  appears as STEERING (would mean drainAsSteer fired instead of
  the normal drain path), or the queued text never reaches the
  transcript (would mean the queue entry was lost on the daemon
  side).

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
# Optional:
# find ~/.local/state/serf/projects -name "$SID*" -delete
```

## Sharp edges

- **Queue depth is not surfaced on the wire today**. The daemon's
  `Session.QueueDepth()` / `QueuePreview()` are reachable from
  `cmd/serf/serve.go` but the appwire `ThreadCapabilities.Queue`
  is a boolean only — it advertises support, not depth. The
  renderer keeps a client-side mirror (`pendingQueue`) that is
  authoritative for the local browser. If a second client queues
  to the same session the mirror in browser A will drift; for the
  single-user web UX in this phase that is acceptable. A future
  pass can expose depth/preview via `SerfThread` or a new
  notification.
- **The queue path bypasses image attachments**. The daemon's
  `Enqueue` is text-only today. The composer surfaces a clear
  error banner ("queue does not support images yet…") when the
  user tries to queue with pending images so the images aren't
  silently dropped. If you need to send images mid-turn, drain
  the queue and either steer or wait for idle.
- **The send button stays enabled in queue mode**. The
  template's `disabled` clause only fires when BOTH
  `Capabilities.Send` AND `Capabilities.Queue` are false. When
  send is off but queue is on the button is the active queue
  trigger; the `data-capability-queue` attribute disambiguates
  the submit handler's branch. Visual treatment is identical to
  the normal send button on purpose — the user sees "Enter to
  send" semantics, the daemon handles the difference.
- **Queued vs steered messages render very differently**. A
  queued message becomes a fresh user turn (its own `USER`
  transcript entry, its own assistant reply). A drained-as-steer
  message becomes a single `STEERING` entry inside the active
  turn (no new user transcript line, the active turn keeps going).
  When checking the transcript, look at `turn.kind` to
  disambiguate. See `web-queue-then-drain-as-steer.md` for the
  drain path.
- **Race: queue and turn-completion can interleave**. If you
  press ⌘↵ a fraction of a second after the active turn
  completes, the daemon may return Conflict ("no active turn to
  queue against"). In that case the safest UX is for the user
  to retry — the textarea is preserved on a failed queue (the
  renderer surfaces an error banner rather than clearing). For
  testing, prefer a long-running first turn (via AGENTS.md
  pacing) so you have a wide window.
