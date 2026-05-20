# web-steer-success-reconciles: optimistic-rendering happy path

Verifies the success side of the optimistic-rendering pattern (kata
`wymv`). When the user clicks "send as steer" during an active
turn, the renderer synchronously appends an `.optimistic-pending`
`details.steering` chip (via `SerfAppwirePending.register` —
`cmd/serf-hub/assets/pending.js`). When the authoritative
`serf/steering/injected` notification arrives over appwire, the
renderer's `deliverNotification` calls
`pendingRegistry.tryReconcile("turn/steer", { text })`. On match,
the pending chip is removed from the DOM and the conversation
reducer renders the real `details.steering` divider in its place.

The end-to-end steer is already covered by `web-steer-live-turn.md`
(kata `a08v` happy path). This scenario adds the
optimistic-rendering visual check: the pending placeholder is
visible before reconcile and gone after.

Driver: superpowers-chrome:browsing.

## Pre-state

- Hub running on `0.0.0.0:9180` with `--serf` resolvable.
- Anthropic OAuth or API key configured.
- `~/.serf/auth-token` readable.
- Chrome session authenticated against the hub.

## Steps

```bash
tmpdir=$(mktemp -d -t serf-e2e-steer-ok-XXXXX)
TOKEN=$(cat ~/.serf/auth-token)
HUB=http://localhost:9180
```

1. **Drop a pacing AGENTS.md** into the workspace so the turn
   stays `processing` long enough to click steer (same template as
   `web-steer-live-turn.md`):
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
   EOF
   ```

2. **Spawn** with `anthropic/claude-haiku-4-5-20251001` and a
   prompt long enough to stay in `processing` for ~60 s:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"Read AGENTS.md if it exists in your cwd. Then write a long 5-paragraph essay about software engineering. Follow the pacing rules in AGENTS.md exactly.\",\"model\":\"anthropic/claude-haiku-4-5-20251001\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['session_id'])")
   # Wait until state=active (turn actually started).
   for i in $(seq 1 30); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "active" ] && break
     sleep 1
   done
   ```

3. **Authenticate the browser** and open the workspace:
   ```
   navigate http://127.0.0.1:9180/auth?token=<TOKEN>&next=/s/<SID>
   await_element [data-steer-trigger]
   ```
   Confirm the button is enabled (live stream has hydrated the
   active state):
   ```javascript
   ({ disabled: document.querySelector("[data-steer-trigger]").disabled });
   // { disabled: false }
   ```

4. **Click steer and immediately assert the pending chip is
   present**:
   ```javascript
   const ta = document.querySelector("textarea.message-input");
   ta.focus(); ta.value = "Stop and write a haiku instead.";
   ta.dispatchEvent(new Event("input", { bubbles: true }));
   document.querySelector("[data-steer-trigger]").click();
   // Sync assertion — the chip is registered before request() awaits.
   document.querySelectorAll(".optimistic-pending").length;
   // 1
   ```

5. **Wait for reconcile** (the daemon ack + `serf/steering/injected`
   notification round-trip is typically 200-800 ms):
   ```
   await_element details.steering:not(.optimistic-pending)
   ```

## Expected

- **Step 4** — exactly one `.optimistic-pending` element exists
  immediately after the click. It is a `details.steering` chip
  (`chipForMethod("turn/steer", text)` in `pending.js:32`) with
  textContent `"↻ Stop and write a haiku instead."`.
- **Step 5** — within ~1 s the pending chip is removed
  (`pendingRegistry.tryReconcile` calls `removeEntry`) AND a real
  authoritative `details.steering` element is appended by the
  reducer. The page now has:
  ```javascript
  ({
    pending: document.querySelectorAll(".optimistic-pending").length, // 0
    steerCount: document.querySelectorAll("details.steering").length, // 1
    failed: document.querySelectorAll(".optimistic-failed").length,   // 0
  })
  ```
- Session continues to `active`, then settles to `idle` with
  the model honoring the steer (closing output is a haiku, not the
  5-paragraph essay).

Falsification:

- `.optimistic-pending` still present after `details.steering`
  appears — reconcile failed to match (text-normalize mismatch in
  `tryReconcile`).
- `.optimistic-failed` appears — the daemon rejected the steer
  despite `processing` state (regression).
- Two `details.steering` elements after reconcile — the renderer
  is appending the authoritative entry without removing the
  pending placeholder.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
rm -rf "$tmpdir"
```
