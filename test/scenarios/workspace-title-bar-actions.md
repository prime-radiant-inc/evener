# workspace-title-bar-actions: interrupt, compact, shutdown end-to-end

**What this covers**: kata `gx92`. The workspace title-bar buttons
(interrupt, compact, shutdown) hit `handleSessionAction` in
`cmd/serf-hub/web.go`, which forwards to the daemon via appwire
(`TurnInterruptParams`, `ThreadCompactStartParams`,
`ThreadShutdownParams`). The jstest suite (`test-actions.js`) confirms
the click handlers POST the right URLs. This scenario is the
server-side counterpart: did the daemon actually stop / compact /
exit? Without this, a regression in the daemon-side wiring of these
RPCs would not be caught.

## Pre-state

- Hub running on `0.0.0.0:9180` with `--serf` resolvable (sibling or
  PATH).
- OpenAI OAuth signed in (`./serf openai status` shows
  `source=oauth`).
- `~/.serf/auth-token` readable.
- `jq` not required — examples use `python3 -m json.tool` and
  inline `python3 -c`. Substitute freely.

## Steps

Set up shared state:

```bash
tmpdir=$(mktemp -d -t serf-e2e-titlebar-XXXXX)
TOKEN=$(cat ~/.serf/auth-token)
HUB=http://localhost:9180
```

1. **Spawn**. Use `openai/gpt-5.4-mini` (cheap, fast). The initial
   prompt asks the agent to do a trivial reply so the session reaches
   `idle` quickly:
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"reply with the word 'ready' and nothing else\",\"model\":\"openai/gpt-5.4-mini\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['session_id'])")
   # wait for idle
   for i in $(seq 1 30); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 1
   done
   ```

2. **Interrupt**. Send a slow turn that drives `exec_command` to run a
   long bash loop, then immediately try to interrupt:
   ```bash
   curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"call exec_command with command=\"bash\" and args=[\"-c\",\"for i in 1 2 3 4 5 6 7 8 9 10; do echo step $i; sleep 2; done\"] then report"}' \
     "$HUB/s/$SID/send" &
   # wait until the session is actually processing
   for i in $(seq 1 10); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     turn=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('active_turn_id',''))")
     [ "$state" = "processing" ] && break
     sleep 1
   done
   echo "state=$state turn=$turn"
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d "{\"turn_id\":\"$turn\"}" "$HUB/s/$SID/interrupt"
   ```

**2b. Interrupt with a queued message** (kata `0bq1` natural
composition). Re-send a slow turn, queue one user message
(via `POST /queue`), then interrupt. The queued message must
survive the interrupt — after the cancelled turn settles back
to idle, the daemon's outer ProcessInput loop pops the queue
head and runs it as a fresh user turn:

   ```bash
   # Re-send a slow turn so we have a window to queue + interrupt.
   curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"call exec_command with command=\"bash\" and args=[\"-c\",\"for i in 1 2 3 4 5 6 7 8 9 10; do echo step $i; sleep 2; done\"] then report"}' \
     "$HUB/s/$SID/send" &
   for i in $(seq 1 10); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     turn=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('active_turn_id',''))")
     [ "$state" = "processing" ] && break
     sleep 1
   done

   # Queue a follow-up. capability check first:
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | python3 -c "import json,sys; d=json.load(sys.stdin); print('queue_cap=',d['capabilities'].get('queue'))"
   # queue_cap= True
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"after the loop, reply with the single word DONE"}' "$HUB/s/$SID/queue" | head -3
   # HTTP 204

   # Interrupt the in-flight turn.
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d "{\"turn_id\":\"$turn\"}" "$HUB/s/$SID/interrupt" | head -3

   # The cancelled turn drops to idle; the queued message should
   # then run as a fresh user turn. Wait for the second turn to
   # complete (count incremented by 1 vs the pre-queue baseline).
   tc_baseline=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
                 | python3 -c "import json,sys; print(json.load(sys.stdin)['turn_count'])")
   for i in $(seq 1 60); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     tc=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('turn_count'))")
     [ "$state" = "idle" ] && [ "$tc" -gt "$tc_baseline" ] && break
     sleep 2
   done
   echo "post-queue settled state=$state turn_count=$tc baseline=$tc_baseline"
   # Confirm the queued text appears in the transcript as a USER turn
   # AFTER the interrupted turn's cancellation marker:
   TFILE=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   python3 - <<EOF
   import json
   kinds = []
   for line in open("$TFILE"):
       j = json.loads(line)
       t = j.get("turn", {})
       k = t.get("kind", "")
       if k in ("USER", "STEERING"):
           texts = []
           for c in t.get("message", {}).get("content", []):
               if c.get("kind") == "text":
                   texts.append(c.get("text", "")[:60])
           kinds.append((k, " | ".join(texts)))
   for k in kinds:
       print(k)
   EOF
   # Expect to see a STEERING containing "The user interrupted the
   # previous turn" followed by a USER entry containing "after the
   # loop, reply with the single word DONE".
   ```

   **Expected**: queue POST returns 204 (kata `111a` capability
   gating ensures `queue_cap=true` mid-turn). Interrupt cancels
   the in-flight turn (state cycles processing → idle) but does
   NOT drop the queued message: the daemon's outer ProcessInput
   loop still pops the queue head on its next iteration and runs
   it as a fresh user turn. `turn_count` increments by exactly
   one (the queued message becomes a single new user turn,
   independent of the cancelled one). The transcript shows the
   cancellation STEERING immediately followed by the queued
   USER entry. Falsification: queue POST returns 409 (would mean
   the daemon dropped the Queue capability during the interrupt),
   `turn_count` does not advance (queued message lost), or the
   queued message appears as STEERING (would mean drainAsSteer
   ran when it shouldn't have).

3. **Wait for the turn to settle** (interrupt should land within
   a few seconds — see Expected). Then **compact**:
   ```bash
   # wait until idle (the slow turn will finish on its own — ~25-30s)
   for i in $(seq 1 90); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 1
   done
   # record turn count and transcript size
   TC_BEFORE=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
                | python3 -c "import json,sys; print(json.load(sys.stdin)['turn_count'])")
   TFILE=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   LINES_BEFORE=$(wc -l < "$TFILE")
   echo "before compact: turn_count=$TC_BEFORE lines=$LINES_BEFORE"
   # compact
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{}' "$HUB/s/$SID/compact" | head -3
   ```

4. **Follow-up turn** to verify compacted context survives. Ask
   about an earlier message; the agent should still know it:
   ```bash
   curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"in one short sentence: what was my first message to you?"}' \
     "$HUB/s/$SID/send"
   for i in $(seq 1 30); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 1
   done
   tail -3 "$TFILE" | python3 -c "
   import json, sys
   for line in sys.stdin:
       j = json.loads(line)
       msg = j.get('turn', {}).get('message', {})
       for c in msg.get('content', []):
           if c.get('kind') == 'tool_call' and c.get('tool_call', {}).get('name') == 'communicate':
               print('REPLY:', c['tool_call']['arguments'].get('message'))
   "
   ```

5. **Shutdown**. Capture pre-state (daemon pid via rendezvous, meta
   file path), POST shutdown, watch the daemon exit:
   ```bash
   RFILE=$(grep -l "\"session_id\":\"$SID\"" ~/.serf/run/*.json)
   PID=$(basename "$RFILE" .json)
   META=$(find ~/.local/state/serf/projects -name "$SID.meta.json")
   echo "pid=$PID rfile=$RFILE meta=$META"
   ts_start=$(date +%s)
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{}' "$HUB/s/$SID/shutdown" | head -3
   for i in $(seq 1 10); do
     kill -0 "$PID" 2>/dev/null || { echo "process gone after $(($(date +%s) - ts_start))s"; break; }
     sleep 1
   done
   ls -la "$RFILE" 2>&1
   ls -la "$META"
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | python3 -c "import json,sys; d=json.load(sys.stdin); print('state=',d['state'],'live=',d['live'])"
   ```

## Expected

- **Step 1**: spawn returns `{ref, host_id, session_id}`; session
  reaches `state=idle` within ~10s; `turn_count=1`.
- **Step 2 (interrupt)**: while the bash loop is running,
  `capabilities.interrupt=true`. Hub returns `204 No Content` for
  the interrupt POST. Within ~3s the in-flight turn is cancelled
  and the session flips from `processing` back to `idle` (kata
  `0ax1`): the abort signal cancels the **turn**, not the session.
  The transcript records the partial tool output plus a system
  interrupt marker (a `STEERING` turn whose text contains
  `The user interrupted the previous turn`). The active turn is
  reported `status=canceled` on `turn/completed`. The session
  remains alive — steps 3-5 below still work on the same SID.
  Send a follow-up `/send` immediately and it must complete
  normally. Falsification: state stays `processing` for the full
  ~25-30s of the bash loop, or state flips to `closed`/`ended`
  (the old pre-`0ax1` semantic — would mean the ProcessInput abort
  path is still calling `s.Close()`), or `capabilities.interrupt=false`
  mid-turn, or the hub returns 503 on the interrupt POST. Verify
  the follow-up: `curl -s -X POST ... "$HUB/s/$SID/send" -d
  '{"text":"reply with just OK"}'` and confirm state cycles back
  to `idle` with a new assistant turn.
- **Step 3 (compact)**: hub returns `204 No Content` essentially
  instantly (compaction here is local re-projection of in-memory
  context; the daemon does not call the model). Transcript grows by
  exactly one entry of `turn.kind = "CHECKPOINT"` whose `message.text`
  contains `[CONTEXT CHECKPOINT]` and includes the prior user
  messages and agent replies summarized. `turn_count` is unchanged.
- **Step 4 (follow-up after compact)**: assistant's `communicate`
  reply references the first prompt (the literal `"reply with the
  word 'ready' and nothing else"`). Confirms the checkpoint
  preserved earlier-turn content.
- **Step 5 (shutdown)**: hub returns `204`; daemon process exits
  within ~3s; rendezvous file at `~/.serf/run/<pid>.json` is gone;
  meta file at `~/.local/state/serf/projects/.../<sid>.meta.json`
  persists; subsequent GET on `/api/sessions/local:<sid>` reports
  `state=ended` and `live=false`. Falsification: process still alive
  after 5s, rendezvous file still present, meta file deleted, or
  session reports `live=true`.

## Cleanup

```bash
rm -rf "$tmpdir"
# meta + transcript files under ~/.local/state/serf/projects linger
# (harmless). Optional:
find ~/.local/state/serf/projects -name "$SID*" -delete
# If a daemon was left running (e.g. you skipped the shutdown step),
# kill it:
# kill $PID
```

## Sharp edges

- **Interrupt wiring** (kata `k7t8`, fixed). `cmd/serf/serve.go`
  wraps each turn in a per-turn `context.WithCancel(ctx)` and
  registers the cancel via `srv.SetCancelFunc` for the duration of
  the turn (cleared on completion). This means `capabilities.interrupt`
  is true only mid-turn. The REST `/interrupt` handler returns 503
  if no cancel is registered (mirrors the appwire path's
  `Unavailable` semantics) — so a stale "interrupt" click on an
  idle session surfaces an honest error rather than a silent 204.
- **Interrupt semantics** (kata `0ax1`, fixed). A successful
  interrupt cancels the in-flight turn but keeps the session
  alive. State transitions `processing → idle` (NOT `closed`),
  the outer session loop in `cmd/serf/serve.go` remains ready for
  the next `/input` POST, and the user can immediately follow up
  with another message. The transcript records a `STEERING` turn
  with a `<SYSTEM-REMINDER>` so the model sees on its next turn
  that the previous round was cut short. If you see state stay at
  `closed` after the interrupt, the abort path in
  `agent/session.go` ProcessInput regressed to its pre-`0ax1`
  behaviour of calling `s.Close()`.
- The slow-turn prompt depends on the model actually choosing to run
  `exec_command` with a sleep loop. `gpt-5.4-mini` did so reliably in
  testing, but a model that shortcuts (responds with "I'll do that"
  via `communicate` without invoking the tool) will finish in
  seconds. If you see `state=processing` for less than ~5 seconds,
  inspect the transcript — the model may not have run the loop and
  the interrupt may have raced the natural turn completion.
- The compact endpoint is synchronous and very fast here (single
  digit ms) because our compact strategy is `session_log` which only
  emits a CHECKPOINT entry — no LLM round-trip. Other strategies
  (`recursive_distill`, `memory_crystals`) would take seconds and
  may briefly flip `state` to a compacting variant. Adjust the wait
  loop accordingly when testing those.
- `turn_count` semantics: it counts committed exchanges, not
  transcript entries. Compact adds one entry but no exchange, so
  `turn_count` is unchanged. After the follow-up send it ticks by
  one. Don't confuse line counts with turn counts.
- The session detail endpoint is `/api/sessions/local:<sid>`, NOT
  `/api/sessions/<sid>` (returns 404) and NOT `/s/<sid>` (returns
  HTML). The local: prefix is required because the route parses a
  `hubapi.Ref`.
- Multiple `~/.serf/run/*.json` files can exist for different
  daemons. Use `grep -l "\"session_id\":\"$SID\""` to find the
  right one; do not pick the most recent.
- Shutdown is graceful (daemon ack'd 204 before exiting). The
  rendezvous file is removed by the daemon during its own shutdown
  hook, so its disappearance is the strongest signal that the
  daemon honoured the request. If the file lingers, the daemon
  crashed before cleanup or shutdown didn't actually fire.
