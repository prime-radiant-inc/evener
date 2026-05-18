# web-queue-then-drain-as-steer: queued messages collapse into a single STEERING

**What this covers**: kata `0bq1` (web). The "send as steer" button
(repurposed `[data-steer-trigger]`) AND the ⇧↵ keybind both call
`POST /s/<id>/drain-as-steer` (or appwire `turn/drainAsSteer`). The
daemon pops every entry from its per-session input queue, joins
them with blank lines, and injects the joined text as a single
STEERING entry on the active turn. The renderer mirrors that by
wiping `pendingQueue` and hiding the queue-preview chrome. Net
effect: the user can rapidly enqueue several thoughts during a
slow turn and then "ship them all at once" without waiting for the
turn to finish naturally.

If the queue is empty when the button is pressed, the action falls
back to the classic single-text steer (kata `a08v`) so no typed
text is lost — see `web-steer-live-turn.md` for that path.

## Pre-state

Same as `web-queue-then-completes.md`. Long-running first turn via
an AGENTS.md pacing nudge; hub on `0.0.0.0:9180`; OpenAI OAuth;
browser authenticated against the hub.

## Steps

```bash
tmpdir=$(mktemp -d -t serf-e2e-drain-XXXXX)
TOKEN=$(cat ~/.serf/auth-token)
HUB=http://localhost:9180
```

1. **Same pacing AGENTS.md** as in `web-queue-then-completes.md`
   step 1. Drop it into `$tmpdir`.

2. **Spawn** with `openai/gpt-5.4-mini` and start a slow turn:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"Read AGENTS.md if it exists in your cwd. Then write a long careful 5-paragraph essay about software engineering practices. Follow the pacing rules in AGENTS.md exactly — insert exec_command sleep calls between every paragraph.\",\"model\":\"openai/gpt-5.4-mini\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['session_id'])")
   # wait until the session is processing
   for i in $(seq 1 30); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "processing" ] && break
     sleep 1
   done
   echo "SID=$SID state=$state"
   ```

3. **Authenticate and load the workspace** at
   `/auth?token=<TOKEN>&next=/s/<SID>`.

4. **Queue two messages** by pressing ⌘↵ twice:
   ```javascript
   const ta = document.querySelector("textarea.message-input");
   async function queueOne(text) {
     ta.focus();
     ta.value = text;
     ta.dispatchEvent(new Event("input", { bubbles: true }));
     ta.dispatchEvent(new KeyboardEvent("keydown", {
       key: "Enter", ctrlKey: true, bubbles: true, cancelable: true,
     }));
     await new Promise(r => setTimeout(r, 300));
   }
   await queueOne("also: explain how Go's table-driven tests reduce duplication");
   await queueOne("and: avoid using mocks where a fake interface suffices");

   const preview = document.querySelector("[data-queue-preview]");
   const depth = preview.querySelector("[data-queue-depth]").textContent;
   ({ depth, rows: preview.querySelectorAll(".queue-preview-item").length });
   // { depth: "2", rows: 2 }
   ```

5. **Drain as steer via the button**. Type one more message into
   the composer — the drain handler should enqueue it first (so it
   joins the rest of the queue) then drain. This also confirms the
   "force-steer-empty-queue note" from the design: typed text is
   preserved even when the user clicks the steer button:
   ```javascript
   ta.focus();
   ta.value = "summary: prioritize clarity over cleverness in tests";
   ta.dispatchEvent(new Event("input", { bubbles: true }));
   const steer = document.querySelector("[data-steer-trigger]");
   steer.click();
   await new Promise(r => setTimeout(r, 500));

   const previewAfter = document.querySelector("[data-queue-preview]");
   ({
     previewHidden: previewAfter.hidden,
     depth: previewAfter.querySelector("[data-queue-depth]").textContent,
     taValue: ta.value,
     steerings: document.querySelectorAll(".steering").length,
   });
   // { previewHidden: true, depth: "0", taValue: "", steerings: >= 1 }
   ```

6. **Wait for the turn to settle** and inspect the transcript.
   The drain must produce ONE `STEERING` entry whose text contains
   all three queued lines, NOT three separate entries:
   ```bash
   for i in $(seq 1 120); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 2
   done
   TFILE=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   python3 - <<EOF
   import json
   steerings = []
   users = []
   for line in open("$TFILE"):
       j = json.loads(line)
       t = j.get("turn", {})
       if t.get("kind") == "STEERING":
           for c in t.get("message", {}).get("content", []):
               if c.get("kind") == "text":
                   steerings.append(c.get("text", ""))
       elif t.get("kind") == "USER":
           for c in t.get("message", {}).get("content", []):
               if c.get("kind") == "text":
                   users.append(c.get("text", "")[:120])
   print("USERS:", len(users))
   for u in users:
       print(" -", u)
   print("STEERINGS:", len(steerings))
   for s in steerings:
       print(" -", s[:300].replace("\\n", " ⏎ "))
   EOF
   ```

## Expected

- **Step 4 (queue×2)**: each ⌘↵ POSTs to `/queue` and the
  preview chrome shows two rows after the second submit, with
  `data-queue-depth="2"`. Falsification: a second ⌘↵ replaces
  the first row (would mean `pendingQueue` was clobbered instead
  of appended), or the second post hits `/send` (would mean the
  capability-queue branch didn't re-engage after the first queue).
- **Step 5 (drain)**: clicking the steer button with text in the
  textarea + a non-empty queue posts TWO HTTP requests in order:
  first an extra `/queue` carrying the textarea content, then
  `/drain-as-steer`. The preview is wiped (`previewHidden=true`,
  depth=0), the textarea is cleared, and a `.steering` element
  appears in the conversation pane. Falsification: only
  `/drain-as-steer` is posted and the textarea content is lost
  (the "don't lose typed text" sharp edge), or `/steer` is posted
  with just the textarea text and the queue is left intact (would
  mean the steer button's "queue empty" branch ran even though
  queue was non-empty).
- **Step 6 (transcript)**: exactly ONE additional `STEERING`
  entry whose text contains all three lines joined by blank lines
  (FIFO order: "also: explain…", "and: avoid…", "summary:
  prioritize…"). NO new `USER` turn appears for the queued
  messages — they did not become user turns because the drain
  collapsed them. The original prompt's `USER` entry is still the
  only one. The active turn's `assistant` reply that follows the
  STEERING reflects the steered guidance (the model's next message
  references the queued instructions). `turn_count` is unchanged
  by the drain (steering does not count as a turn). Falsification:
  multiple `STEERING` entries (would mean each queue entry was
  drained as its own steer), or a new `USER` entry appears for
  the queued text (would mean the daemon ran the queue as fresh
  turns instead of draining), or the assistant reply does NOT
  reference the queued guidance (the steer didn't reach the model
  before turn completion).

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```

## Sharp edges

- **The button changes label but stays the same DOM node**. The
  template's `[data-steer-trigger]` is the same element that used
  to read just "steer" — it now reads "send as steer ⇧↵". Tests
  that select by `data-steer-trigger` continue to work; tests
  that selected by visible text ("steer") need updating.
- **The button's semantics depend on the local queue mirror**, not
  on the daemon's depth. If the user pastes a queued message into
  the composer with the keyboard while the mirror is empty (e.g.
  after a page reload), the steer button will take the
  empty-queue branch and call `/steer` instead of `/drain`. This
  is acceptable for the single-user phase (Phase 2a) — a future
  pass that mirrors depth across clients would close this race.
- **The drain endpoint is gated on the daemon's actual queue
  depth, not the client mirror's**. If the daemon's queue is
  empty (e.g. the active turn just popped a message) and the
  client tries to drain, the daemon returns Conflict and the
  composer surfaces an error banner. The client mirror is wiped
  on success only; on failure it is left intact so the user can
  retry or queue more.
- **Joining is FIFO with blank-line separators**. The daemon
  joins queued messages in insertion order with `\n\n` between
  each, then wraps the whole thing in a single STEERING entry.
  Order matters when the model is asked to "do A then B" via
  queued messages — make sure the order you queue is the order
  you want the model to see them. The transcript's STEERING
  entry preserves that order verbatim.
- **⇧↵ in the composer is the keybind equivalent of the button**.
  The renderer's `bindKeyboard` intercepts Shift+Enter without
  Meta/Ctrl/Alt and clicks the steer button programmatically.
  The browser's default Shift+Enter behavior in a textarea is
  "insert newline"; this is suppressed only when the steer button
  is enabled (i.e. there is an active turn). If the session is
  idle, Shift+Enter inserts a newline as usual.
