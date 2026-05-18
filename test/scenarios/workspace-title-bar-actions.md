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

Surfaces also fileable kata `k7t8` (interrupt is not wired in
production — `cancelFunc` never gets set in `cmd/serf/serve.go`).

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

3. **Wait for the turn to settle** (interrupt was a no-op — see
   Expected). Then **compact**:
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
- **Step 2 (interrupt)**: hub returns `503 interrupt is not available
  for this session` — `capabilities.interrupt=false` even mid-turn.
  The slow turn does NOT stop; state stays `processing` until the
  bash loop completes. **Falsification (expected today)**: this is a
  known bug — kata `k7t8`. If the interrupt later succeeds (state
  flips to `idle` or `error` within ~3s and the transcript shows an
  interrupt marker / partial output), update this scenario.
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
# If a daemon was left running (e.g. interrupt didn't stop it and
# you skipped the shutdown step), kill it:
# kill $PID
```

## Sharp edges

- **Interrupt is broken** (kata `k7t8`). `cmd/serf/serve.go` calls
  `SetCompactFunc`, `SetShutdownFunc`, etc., but never
  `SetCancelFunc`. The capability flag in `server/server.go` (line
  ~496) gates on `cancelFunc != nil`, so the hub correctly refuses
  with 503. The daemon's REST `/interrupt` handler returns 204 even
  with a nil `cancelFunc`, which is misleading — appwire-side
  (`appwire_runtime.go:344`) does the same. Until the daemon wires
  a cancellable turn context into `SetCancelFunc`, this surface is
  cosmetic only.
- The slow-turn prompt depends on the model actually choosing to run
  `exec_command` with a sleep loop. `gpt-5.4-mini` did so reliably in
  testing, but a model that shortcuts (responds with "I'll do that"
  via `communicate` without invoking the tool) will finish in
  seconds. If you see `state=processing` for less than ~5 seconds,
  inspect the transcript — the model may not have run the loop. The
  scenario does NOT depend on interrupt working (today), only on the
  hub correctly returning 503; for a future fix, choose a prompt
  that forces a multi-second tool call.
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
