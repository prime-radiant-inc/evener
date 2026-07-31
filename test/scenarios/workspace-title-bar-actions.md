# workspace-title-bar-actions: interrupt, compact, shutdown end-to-end

**What this covers**: kata `gx92`. The session actions (interrupt, compact,
shutdown) hit `handleSessionAction` in `cmd/serf-hub/web_session.go:188`,
which forwards to the daemon via appwire (`TurnInterruptParams`,
`ThreadCompactStartParams`, `ThreadShutdownParams`). This scenario is the
server-side counterpart to `SessionActionsMenu.test.tsx`: did the daemon
actually stop / compact / exit? Without this, a regression in the daemon-side
wiring of these RPCs would not be caught.

**Surface**: see `docs/agentic-testing.md`, "The REST surface, and what is no
longer on it" — the verb table there is the single place these routes are
maintained. This card is **almost entirely browser-free**; only step 2b's
queue leg needs a client, because queue has no REST route at all. The old
`$HUB/s/$SID/<action>` form-POST shim is gone (`660376f78`) and now 404s
silently, leaving the daemon running — every action below uses
`$HUB/api/sessions/local:$SID/<action>`.

## Pre-state

- Hub running on an isolated `$HOME` and free port (never `9180`,
  Jesse's real one — see the Setup checklist in
  `docs/agentic-testing.md`) with `--serf` resolvable (sibling or
  PATH).
- OpenAI OAuth signed in (`./serf openai status` shows
  `source=oauth`).
- `$HOME/.serf/auth-token` readable (that isolated `$HOME`).
- `jq` not required — examples use `python3 -m json.tool` and
  inline `python3 -c`. Substitute freely.

## Steps

Set up shared state:

```bash
tmpdir=$(mktemp -d -t serf-e2e-titlebar-XXXXX)
TOKEN=$(cat "$HOME/.serf/auth-token")
HUB=http://127.0.0.1:$PORT
```

1. **[browser-free] Spawn**. Use `openai/gpt-5.4-mini` (cheap, fast). The
   initial prompt asks the agent to do a trivial reply so the session reaches
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

2. **[browser-free] Interrupt**. Send a slow turn that drives `exec_command`
   to run a long bash loop, then immediately try to interrupt:
   ```bash
   curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"call exec_command with command=\"bash -c '\''for i in 1 2 3 4 5 6 7 8 9 10; do echo step $i; sleep 2; done'\''\" then report"}' \
     "$HUB/api/sessions/local:$SID/send" &
   # wait until the session is actually active
   for i in $(seq 1 10); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     turn=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('active_turn_id',''))")
     [ "$state" = "active" ] && break
     sleep 1
   done
   echo "state=$state turn=$turn"
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d "{\"turn_id\":\"$turn\"}" "$HUB/api/sessions/local:$SID/interrupt"
   ```

**2b. Interrupt with a queued message** (kata `0bq1` natural
composition). Re-send a slow turn, queue one user message, then interrupt.
The queued message must survive the interrupt — after the cancelled turn
settles back to idle, the daemon's outer ProcessInput loop pops the queue
head and runs it as a fresh user turn.

   **There is no REST route for queue.** `handleAPISession`'s verb list
   (`cmd/serf-hub/web_api_tree.go:1376-1416`) has no `queue` case, and the
   old `/s/<id>/queue` shim died with the vanilla frontend. `turn/queue`
   lives only on the AppWire WebSocket (`appwire/types.go:26`), so this leg
   needs one of:

   - **[browser-free]** dial `ws://127.0.0.1:$PORT/rpc` with
     `Authorization: Bearer $TOKEN`, `initialize`, then call
     `turn/queue{ref:"local:<SID>", ...}` directly; or
   - **[browser]** navigate to `/auth?token=$TOKEN&next=/s/local:$SID`, type
     into the textarea inside `[data-testid="composer-input-card"]` and click
     `[data-testid="composer-submit"]` — **Send** routes to `turn/queue`
     while a turn is running and `turn/start` otherwise
     (`panes/session/composer/submitRouting.ts:19-23`), one label with two
     timings. The queue then shows up as the heading
     `Queued messages (N)` (`composer/queue/QueueStrip.tsx:278`) and as
     `[data-testid="status-row-queue"]` reading `N queued` on the status
     strip (`chrome/StatusRow.tsx:355-359`).

   ```bash
   # Re-send a slow turn so we have a window to queue + interrupt.
   curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"call exec_command with command=\"bash -c '\''for i in 1 2 3 4 5 6 7 8 9 10; do echo step $i; sleep 2; done'\''\" then report"}' \
     "$HUB/api/sessions/local:$SID/send" &
   for i in $(seq 1 10); do
     d=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID")
     state=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     turn=$(echo "$d" | python3 -c "import json,sys; print(json.load(sys.stdin).get('active_turn_id',''))")
     [ "$state" = "active" ] && break
     sleep 1
   done

   # Capability check first — the REST detail still reports whether the
   # daemon WOULD accept a queue, which is enough for a gating assertion
   # even though there is no REST verb to exercise it.
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | python3 -c "import json,sys; d=json.load(sys.stdin); print('queue_cap=',d['capabilities'].get('queue'))"
   # queue_cap= True

   # Queue "after the loop, reply with the single word DONE" via turn/queue
   # (WebSocket or composer — see above), then interrupt the in-flight turn.
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d "{\"turn_id\":\"$turn\"}" "$HUB/api/sessions/local:$SID/interrupt" | head -3

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
   # Confirm the queued text appears in the transcript as a USER turn AFTER
   # the interrupted turn's cancellation marker. Use serf-doctor rather than
   # hand-parsing JSONL (see docs/agentic-testing.md, "Inspecting transcript
   # and meta on disk"):
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:20
   ```

   **Expected**: `queue_cap=True` mid-turn (kata `111a` capability
   gating). Interrupt cancels the in-flight turn (state cycles
   active → idle) but does NOT drop the queued message: the
   daemon's outer ProcessInput loop still pops the queue head on
   its next iteration and runs it as a fresh user turn.
   `turn_count` increments by exactly one (the queued message
   becomes a single new user turn, independent of the cancelled
   one). The outline shows the cancellation STEERING immediately
   followed by the queued USER entry. Falsification: the queue call
   is rejected (would mean the daemon dropped the Queue capability
   during the interrupt), `turn_count` does not advance (queued
   message lost), or the queued message appears as STEERING (would
   mean drainAsSteer ran when it shouldn't have).

3. **[browser-free] Wait for the turn to settle** (interrupt should land
   within a few seconds — see Expected). Then **compact**:
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
   TFILE=$(find $HOME/.local/state/serf/projects -name "$SID.transcript.jsonl")
   LINES_BEFORE=$(wc -l < "$TFILE")
   echo "before compact: turn_count=$TC_BEFORE lines=$LINES_BEFORE"
   # compact
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{}' "$HUB/api/sessions/local:$SID/compact" | head -3
   ```

4. **[browser-free] Follow-up turn** to verify compacted context survives.
   Ask about an earlier message; the agent should still know it:
   ```bash
   curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"in one short sentence: what was my first message to you?"}' \
     "$HUB/api/sessions/local:$SID/send"
   for i in $(seq 1 30); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 1
   done
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:5
   ```

5. **[browser-free] Shutdown**. Capture pre-state (daemon pid via rendezvous,
   meta file path), POST shutdown, watch the daemon exit:
   ```bash
   RFILE=$(grep -l "\"session_id\":\"$SID\"" $HOME/.serf/run/*.json)
   PID=$(basename "$RFILE" .json)
   META=$(find $HOME/.local/state/serf/projects -name "$SID.meta.json")
   echo "pid=$PID rfile=$RFILE meta=$META"
   ts_start=$(date +%s)
   curl -s -i -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
     -d '{}' "$HUB/api/sessions/local:$SID/shutdown" | head -3
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
  and the session flips from `active` back to `idle` (kata
  `0ax1`): the abort signal cancels the **turn**, not the session.
  The transcript records the partial tool output plus a system
  interrupt marker (a `STEERING` turn whose text contains
  `The user interrupted the previous turn`,
  `agent/session_lifecycle.go:658`). The turn is reported
  `status=interrupted` on `turn/completed` — the `TurnStatus` enum has no
  `canceled` value at all (`appwire/types.go:147-152`), so a controller
  grepping for that literal will never find it. The session remains alive —
  steps 3-5 below still work on the same SID. Send a follow-up `/send`
  immediately and it must complete normally. Falsification: state stays
  `active` for the full ~25-30s of the bash loop, or state flips to
  `closed`/`ended` (the old pre-`0ax1` semantic — would mean the
  ProcessInput abort path is still calling `s.Close()`), or
  `capabilities.interrupt=false` mid-turn, or the hub returns 503 on the
  interrupt POST. Verify the follow-up: `curl -s -X POST ...
  "$HUB/api/sessions/local:$SID/send" -d '{"text":"reply with just OK"}'`
  and confirm state cycles back to `idle` with a new assistant turn.
- **Step 3 (compact)**: hub returns `204 No Content`. The transcript grows by
  one entry of `turn.kind = "CHECKPOINT"` (`agent/schema/turn.go:28`) whose
  text starts with `[CONTEXT CHECKPOINT]`
  (`agent/internal/contextmgr/context_manager.go:927`) and includes the prior
  user messages and agent replies summarized. `turn_count` is unchanged. The
  POST is synchronous over the whole compaction, which is **two** layers, not
  one — see Sharp edges before assuming it should be instant.
- **Step 4 (follow-up after compact)**: the assistant's `communicate`
  reply references the first prompt (the literal `"reply with the
  word 'ready' and nothing else"`). Confirms the checkpoint
  preserved earlier-turn content.
- **Step 5 (shutdown)**: hub returns `204`; daemon process exits
  within ~3s; rendezvous file at `$HOME/.serf/run/<pid>.json` is gone;
  meta file at `$HOME/.local/state/serf/projects/.../<sid>.meta.json`
  persists; subsequent GET on `/api/sessions/local:<sid>` reports
  `state=ended` and `live=false`. Falsification: process still alive
  after 5s, rendezvous file still present, meta file deleted, or
  session reports `live=true`.

## Cleanup

```bash
rm -rf "$tmpdir"
# meta + transcript files under $HOME/.local/state/serf/projects linger
# (harmless). Optional:
find $HOME/.local/state/serf/projects -name "$SID*" -delete
# If a daemon was left running (e.g. you skipped the shutdown step),
# kill it:
# kill $PID
```

## Sharp edges

- **Interrupt wiring** (kata `k7t8`) — **confirmed still fixed; this step is
  a regression guard, not a repro.** `cmd/serf/serve.go` wraps each turn in a
  per-turn `context.WithCancel(ctx)` and registers the cancel via
  `srv.SetCancelFunc` for the duration of the turn (`:957`, `:965`, `:977`),
  clearing it on completion (`:986`). This means `capabilities.interrupt` is
  true only mid-turn. The REST `/interrupt` handler returns 503 if no cancel
  is registered (mirrors the appwire path's `Unavailable` semantics) — so a
  stale interrupt on an idle session surfaces an honest error rather than a
  silent 204.
- **Interrupt semantics** (kata `0ax1`, fixed). A successful
  interrupt cancels the in-flight turn but keeps the session
  alive. State transitions `active → idle` (NOT `closed`),
  the outer session loop in `cmd/serf/serve.go` remains ready for
  the next input, and the user can immediately follow up
  with another message. The transcript records a `STEERING` turn
  with a `<SYSTEM-REMINDER>` so the model sees on its next turn
  that the previous round was cut short. If you see state stay at
  `closed` after the interrupt, the abort path in
  `agent/session_lifecycle.go:517-527`'s `ProcessInputKind` regressed to its
  pre-`0ax1` behaviour of calling `s.Close()`.
- **Compact is not free, and its checkpoint text depends on the strategy.**
  `Session.Compact` (`agent/session_compaction.go:20-58`) runs
  `Manager.ForceCompact` (`agent/internal/contextmgr/context_manager.go:355`),
  which is Layer 1 (a deterministic checkpoint) **plus** Layer 2 (LLM
  summarization) whenever a client is available. Layer 2 short-circuits on a
  short history — `summarizeWithLLMSteered` returns the input unchanged when
  `len(history) <= PreserveRecentTurns` (`:1216-1223`) — which is why this
  card's two-turn session sees a fast 204 and why a longer one will not.
  Don't generalize the timing. The default strategy is `compact`
  (`agent/session_init.go:52-54`, selected for an empty `ContextStrategy`,
  which is what the hub spawns with) and it writes the bare
  `[CONTEXT CHECKPOINT]` header. `--context-strategy session-log` writes
  `[CONTEXT CHECKPOINT - SESSION LOG]` instead
  (`strategy_session_log.go:186`); grep for the shared prefix
  `[CONTEXT CHECKPOINT` if a run may use either.
- **Compact is refused while a question is pending.**
  `agent/session_compaction.go:34-36` returns
  `a question is pending; reply or clear first` — summarizing away the
  transcript tail the pending question lives in would compact out from under
  the user's reply. If step 3 fails, check for an unanswered `ask_user`
  before suspecting the RPC.
- The slow-turn prompt depends on the model actually choosing to run
  `exec_command` with a sleep loop. `gpt-5.4-mini` did so reliably in
  testing, but a model that shortcuts (responds with "I'll do that"
  via `communicate` without invoking the tool) will finish in
  seconds. If you see `state=active` for less than ~5 seconds,
  inspect the transcript — the model may not have run the loop and
  the interrupt may have raced the natural turn completion.
- `turn_count` semantics: it counts committed exchanges, not
  transcript entries. Compact adds one entry but no exchange, so
  `turn_count` is unchanged. After the follow-up send it ticks by
  one. Don't confuse line counts with turn counts.
- The session detail endpoint is `/api/sessions/local:<sid>`, NOT
  `/api/sessions/<sid>` (returns 404) and NOT `/s/<sid>` (returns
  the SPA shell). The `local:` prefix is required because the route parses a
  `hubapi.Ref` (`web_api_tree.go:1360-1374`).
- Multiple `$HOME/.serf/run/*.json` files can exist for different
  daemons. Use `grep -l "\"session_id\":\"$SID\""` to find the
  right one; do not pick the most recent.
- Shutdown is graceful (daemon ack'd 204 before exiting). The
  rendezvous file is removed by the daemon during its own shutdown
  hook, so its disappearance is the strongest signal that the
  daemon honoured the request. If the file lingers, the daemon
  crashed before cleanup or shutdown didn't actually fire.
