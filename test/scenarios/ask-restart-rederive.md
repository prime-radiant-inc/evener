# ask-restart-rederive: daemon restart re-derives `awaiting` immediately, form still answerable

**What this covers**: spec §8 row `ask-restart-rederive.md` — the restore contract in §5.4
("Restore") and §6's pending definition: a daemon killed with an unanswered `ask_user`
question at the transcript tail must report `awaiting` on its **first** successful `/status`
read after restart (never an idle-until-next-turn window), and the question must still
render and be answerable after the restart. Mirrors `cmd/serf/serve_ask_test.go`'s
`TestServeAsk_RestoreReportsAwaitingImmediately` at the live, hub-fronted level, plus
`reconnect-auto-resume.md`'s daemon-kill technique.

## Pre-state

- Hub + credentials as `ask-web-answer.md` (reuse if still running on `127.0.0.1:9280`).
- `jq`, `ps`, `kill` available.

## Steps

1. Spawn a session whose first turn asks a question, and wait for `awaiting`:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-ask-restart-XXXXX)
   body=$(jq -n --arg wd "$tmpdir" '{
     prompt: "Before doing any other work, call the ask_user tool once. Ask exactly one question: header \"Rotation\", question \"Who should be on call this week?\", with exactly two options: alice (detail \"just back from PTO\") and bob (detail \"was on call last week too\"). Do not do anything else first.",
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
2. Find and kill the hub-spawned daemon backing this session (`rendezvous.DefaultDir()` is
   `~/.serf/run`, one `<pid>.json` per live daemon):
   ```bash
   pid=""
   for f in ~/.serf/run/*.json; do
     sid=$(jq -r '.session_id // empty' "$f" 2>/dev/null)
     if [ "$sid" = "$SID" ]; then pid=$(jq -r '.pid' "$f"); rvfile="$f"; break; fi
   done
   echo "killing pid=$pid (rendezvous $rvfile)"
   kill "$pid"
   sleep 1
   ps -p "$pid" >/dev/null 2>&1 && echo "STILL ALIVE (unexpected)" || echo "daemon dead"
   [ -f "$rvfile" ] && echo "STALE RENDEZVOUS FILE (unexpected)" || echo "rendezvous cleaned up"
   ```
3. **Part A — direct restart, isolated from the hub**, to check the daemon-level guarantee
   with zero caching ambiguity (this is the same shape as `TestServeAsk_RestoreReportsAwaitingImmediately`,
   just driven live rather than via the Go test harness):
   ```bash
   /tmp/serf-ask serve --addr 127.0.0.1:9282 --resume "$SID" --dir "$tmpdir" --model openai/gpt-5.5 &
   DAEMON2_PID=$!
   for i in $(seq 1 30); do
     curl -s -o /dev/null http://127.0.0.1:9282/status && break
     sleep 0.3
   done
   curl -s http://127.0.0.1:9282/status | jq '{state}'
   curl -s -X POST http://127.0.0.1:9282/shutdown >/dev/null
   wait "$DAEMON2_PID" 2>/dev/null
   ```
4. **Part B — hub-triggered respawn, and the form still works.** Navigate the browser to the
   (now dead-daemon) session; per `reconnect-auto-resume.md`, a passive page load does not
   itself trigger the hub's auto-resume — only an actual `turn/start` does, so re-derivation
   at cold-attach and answering are exercised together in one motion:
   ```
   navigate http://127.0.0.1:9280/auth?token=<TOKEN>&next=/s/<SID>
   await_element [data-ask-card]
   ```
   ```javascript
   (() => {
     const card = document.querySelector('[data-ask-card]');
     const q = card.querySelector('[data-ask-question]');
     return {
       header: q.querySelector('.ask-question-header').textContent,
       optionLabels: [...q.querySelectorAll('[data-ask-option]')].map(o => o.dataset.optionLabel),
     };
   })()
   ```
   Answer it:
   ```javascript
   document.querySelector('[data-ask-option][data-option-label="alice"] [data-ask-option-input]').click();
   ```
   ```
   click [data-ask-send-btn]
   ```
5. Confirm a *new* daemon actually spawned (distinct from both the original and the manual
   Part-A one) and the reply landed:
   ```bash
   for i in $(seq 1 60); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$st" = "idle" ] && break
     sleep 1
   done
   echo "final state=$st"
   for f in ~/.serf/run/*.json; do
     sid=$(jq -r '.session_id // empty' "$f" 2>/dev/null)
     [ "$sid" = "$SID" ] && jq -c '{pid, session_id}' "$f"
   done
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:6
   ```

## Expected

- Step 2: the original daemon PID is dead; its rendezvous file is gone (clean-shutdown
  cleanup, or reaped by the hub).
- Step 3: the very first successful `GET /status` against the manually-restarted daemon
  reports `{"state":"awaiting"}` — never `"idle"` and never a connection error once it binds.
  This is the core restore-re-derivation guarantee, checked with no hub layer in the way.
- Step 4: the ask card re-renders on cold attach to the now-daemonless session (`header`
  reads `Rotation`, `optionLabels` contains `alice` and `bob`) — the form is driven from the
  transcript, not from any particular daemon instance.
- Step 5: `state` reaches `idle`; a rendezvous file exists for `$SID` with a **different**
  `pid` than the one killed in step 2 (a genuinely new hub-spawned daemon); the outline shows
  the `[answers]` reply (`→ "alice"`) followed by the assistant's next turn.
- Falsification: if a restart with an unanswered ask loses the `awaiting` status or the
  answerable form, restore re-derivation is broken.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
pkill -f serf-hub-ask
pkill -f "serve --addr 127.0.0.1:9282" 2>/dev/null   # in case Part A's shutdown didn't take
rm -rf "$tmpdir" /tmp/serf-ask /tmp/serf-hub-ask
```

## Sharp edges

- **Part A and Part B must not overlap in time.** Two `serve` processes touching the same
  session's transcript/meta concurrently is unsupported — always confirm Part A's manual
  daemon has fully shut down (step 3's `/shutdown` + `wait`) before starting Part B.
- **A passive page load does not trigger hub respawn** (`reconnect-auto-resume.md`'s own
  finding): only an actual `turn/start` (here, clicking Send on the answer) does. Don't be
  surprised if step 4's `navigate` alone leaves the session looking dead/past-only in the
  hub's own roster view — the ask card itself still renders because it's derived from the
  transcript, independent of whether a live daemon backs it yet.
- **The hub's cached/roster-level view of a dead daemon uses its own past-only vocabulary**
  (`reconnect-auto-resume.md` observed `ended`, not a literal mirror of the last live
  state) — this scenario deliberately does not assert on the hub's `/api/sessions` reading
  during the dead window (between steps 2 and 4); it asserts the **daemon-level** guarantee
  directly in Part A, where there is no such ambiguity.
- `--resume` is a flag on **both** `serf` (one-shot/resume) and `serf serve`; this card uses
  `serve --resume` because it needs a long-lived daemon with its own `/status`, not a
  one-shot exit-on-completion run.
