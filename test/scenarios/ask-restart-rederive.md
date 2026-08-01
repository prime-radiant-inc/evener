# ask-restart-rederive: daemon restart re-derives `awaiting` immediately, question still answerable

**What this covers**: spec §8 row `ask-restart-rederive.md` — the restore contract in §5.4
("Restore") and §6's pending definition: a daemon killed with an unanswered `ask_user`
question at the transcript tail must report `awaiting` on its **first** successful
`/status` read after restart (never an idle-until-next-turn window), and the question must
still render and be answerable after the restart. Mirrors
`cmd/serf/serve_ask_test.go#TestServeAsk_RestoreReportsAwaitingImmediately` at the
live, hub-fronted level, plus `reconnect-auto-resume.md`'s daemon-kill technique.

**Surface**: see `docs/agentic-testing.md` — "The REST surface, and what is no longer on
it" and "Driving the web UI with superpowers-chrome:browsing". The answering surface is the
composer's **ask dock** (`cmd/serf-hub/frontend/src/panes/session/composer/askDock/`); the
`[data-ask-card]` / `.ask-question-header` / `[data-ask-option]` / `[data-ask-send-btn]`
selectors this card used to drive died with the vanilla frontend (`660376f78`) and have no
same-named successors. See `ask-web-answer.md` for the current answering gesture in full.

Part A (steps 1-3) and step 5 are **browser-free** — Part A is the core daemon-level
guarantee and is the half worth running when Chrome is unavailable. Part B (step 4) needs
Chrome.

## Pre-state

- Hub, `$HUB`, `$TOKEN`, `$HUBPID` and the isolated `$HOME` **same as
  `ask-web-answer.md`** — reuse that card's hub if it is still running, otherwise re-run
  its Pre-state first. The handoff is its run directory, not a port
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
  The throwaway `$HOME` matters here specifically: this card reads and writes
  `~/.serf/run`, which under that isolation is `$run/home/.serf/run` and not any real
  hub's rendezvous directory.
- `jq`, `ps`, `kill` available.

## Steps

1. **(browser-free)** Spawn a session whose first turn asks a question, and wait for
   `awaiting`:
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
2. **(browser-free)** Find and kill the hub-spawned daemon backing this session.
   `rendezvous.DefaultDir()` is `$HOME/.serf/run` (`rendezvous/rendezvous.go:39-42`), one
   `<pid>.json` per live daemon (`rendezvous/rendezvous.go:1-5`):
   ```bash
   pid=""; rvfile=""
   for f in "$HOME"/.serf/run/*.json; do
     sid=$(jq -r '.session_id // empty' "$f" 2>/dev/null)
     if [ "$sid" = "$SID" ]; then pid=$(jq -r '.pid' "$f"); rvfile="$f"; break; fi
   done
   echo "killing pid=$pid (rendezvous $rvfile)"
   kill "$pid"
   sleep 1
   ps -p "$pid" >/dev/null 2>&1 && echo "STILL ALIVE (unexpected)" || echo "daemon dead"
   [ -f "$rvfile" ] && echo "STALE RENDEZVOUS FILE (unexpected)" || echo "rendezvous cleaned up"
   ```
3. **(browser-free) Part A — direct restart, isolated from the hub.** This checks the
   daemon-level guarantee with zero caching ambiguity — the same shape as
   `TestServeAsk_RestoreReportsAwaitingImmediately`, driven live. Bind a kernel-assigned
   port and give this daemon its own `--run-dir` so the hub never adopts it; both are real
   `serf serve` flags (`cmd/serf/serve.go:236,239,241,242`), and the daemon reports the
   address it actually bound in its own rendezvous entry (`rendezvous.Entry.Address`,
   `rendezvous/rendezvous.go#Address`). Do **not** pass `--state-dir`: Part A must read the
   same default state layout the hub wrote.
   ```bash
   partrun="$tmpdir/partA-run"; mkdir -p "$partrun"
   "$run/serf" serve --addr 127.0.0.1:0 --run-dir "$partrun" \
     --resume "$SID" --dir "$tmpdir" --model openai/gpt-5.5 &
   DAEMON2_PID=$!
   ADDR=""
   for i in $(seq 1 100); do
     ADDR=$(cat "$partrun"/*.json 2>/dev/null | jq -r '.address // empty' | head -1)
     [ -n "$ADDR" ] && break
     kill -0 "$DAEMON2_PID" 2>/dev/null || { echo "daemon exited before registering" >&2; break; }
     sleep 0.3
   done
   echo "partA addr=$ADDR"
   # THE assertion: the FIRST successful /status read, nothing before it.
   curl -s "http://$ADDR/status" | jq '{state}'
   curl -s -X POST "http://$ADDR/shutdown" >/dev/null
   wait "$DAEMON2_PID" 2>/dev/null
   ```
4. **(browser) Part B — hub-triggered respawn, and the dock still works.** Navigate to the
   (now daemonless) session. A passive page load does **not** respawn the daemon: the hub's
   `thread/read` falls back to the on-disk past transcript when no live source answers
   (`cmd/serf-hub/app_rpc.go:172-190`), so the question is reconstructed from the
   transcript rather than from any particular daemon instance. Only an actual `turn/start`
   — here, sending the answer — resumes.
   ```
   navigate $HUB/auth?token=<TOKEN>&next=/s/local:<SID>
   await_element [data-ask-response-dock]
   ```
   ```javascript
   (() => {
     const dock = document.querySelector('[data-ask-response-dock]');
     const q = dock.querySelector('[data-ask-question]');
     return {
       port: location.port,
       path: location.pathname,
       header: q.textContent.slice(0, 40),
       opts: [...q.querySelectorAll('input[type="radio"][aria-label]')]
               .map((el) => el.getAttribute('aria-label'))
               .filter((l) => l !== 'Something else…' && l !== 'let serf decide'),
     };
   })()
   ```
   Answer it — pick `alice`, then send (`AskDock.tsx:254-256`; the button carries no
   testid, so address it by its text):
   ```javascript
   document.querySelector('[data-ask-response-dock] input[aria-label="alice"]').click()
   ```
   ```javascript
   (() => {
     const dock = document.querySelector('[data-ask-response-dock]');
     const send = [...dock.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Send answers');
     send.click();
     return 'clicked';
   })()
   ```
5. **(browser-free)** Confirm a *new* daemon actually spawned — distinct from both the
   original and Part A's manual one — and that the reply landed:
   ```bash
   for i in $(seq 1 90); do
     st=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$st" != "ended" ] && [ "$st" != "notLoaded" ] && [ "$st" != "active" ] && break
     sleep 1
   done
   echo "final state=$st (killed pid was $pid)"
   for f in "$HOME"/.serf/run/*.json; do
     sid=$(jq -r '.session_id // empty' "$f" 2>/dev/null)
     [ "$sid" = "$SID" ] && jq -c '{pid, session_id}' "$f"
   done
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:6
   ```

## Expected

- **Step 2**: the original daemon PID is dead and its rendezvous file is gone (clean
  shutdown removes it; the hub reaps a stale one).
- **Step 3 (exact — this is the core guarantee)**: the very first successful
  `GET /status` against the manually restarted daemon reports `{"state":"awaiting"}` —
  never `"idle"`, and never a connection error once it has registered an address. Falsify:
  `idle` on the first read (restore lost the pending ask), or a read that only becomes
  `awaiting` after some later request nudges it.
- **Step 4**: `[data-ask-response-dock]` renders on cold attach to the daemonless session,
  with the header text containing `Rotation` and `opts` containing `alice` and `bob` — the
  dock is driven from the transcript projection (`liveAskQuestions`,
  `composer/askDock/deriveAskQuestions.ts:68-90`), not from a live daemon. Falsify: no dock
  at all on a session whose transcript tail is an unanswered `ask_user`, or a dock whose
  options do not match what the model actually posted.
- **Step 5**: a rendezvous file exists for `$SID` with a **different** `pid` than the one
  killed in step 2 — a genuinely new hub-spawned daemon, resumed by the answer's
  `turn/start`, not by the page load. The outline shows the composed reply
  (`[answers]` / `1. [Rotation] → "alice"`, `askDock/askCompose.ts:84-93`) followed by the
  assistant's next turn, and the transcript's `ask_user` row now recaps as
  `Asked: [Rotation] — answered: "alice"` (`panes/session/askShared.ts:118-150`). Falsify:
  no new rendezvous file (resume never fired), or the reply is missing from the outline.
- Falsification (whole card): if a restart with an unanswered ask loses the `awaiting`
  status or the answerable question, restore re-derivation is broken.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
kill "$DAEMON2_PID" 2>/dev/null   # in case Part A's /shutdown didn't take
rm -rf "$tmpdir"
```
Leave the hub up if `ask-web-answer.md`'s run still needs it; otherwise kill it by the PID
that card captured and remove its run directory. Kill Part A's daemon by the PID captured
in step 3 — never a `pkill -f serve` pattern, which would also kill a concurrent agent's
daemons.

## Sharp edges

- **Part A and Part B must not overlap in time.** Two `serve` processes touching the same
  session's transcript/meta concurrently is unsupported — confirm Part A's manual daemon
  has fully shut down (step 3's `/shutdown` + `wait`) before starting Part B. The separate
  `--run-dir` keeps the hub from *adopting* Part A's daemon; it does not make concurrent
  writes safe.
- **A passive page load does not trigger hub respawn.** Only an actual `turn/start` does.
  Don't be surprised if step 4's `navigate` alone leaves the session looking past-only in
  the hub's roster view — the dock renders anyway, from the transcript.
- **The hub's cached/roster-level view of a dead daemon uses its own past-only vocabulary**
  (`reconnect-auto-resume.md` observed `ended`, not a mirror of the last live state). This
  card deliberately does not assert on `/api/sessions/local:<SID>` during the dead window
  between steps 2 and 4; it asserts the **daemon-level** guarantee directly in Part A,
  where there is no such ambiguity. Step 5's poll is written to skip past the past-only
  words rather than to pin one.
- `--resume` is a flag on **both** `serf` (one-shot/resume) and `serf serve`; this card
  uses `serve --resume` because it needs a long-lived daemon with its own `/status`, not a
  one-shot exit-on-completion run.
- **`--addr 127.0.0.1:0` is only useful because the daemon publishes what it bound.** Read
  the port back from the rendezvous entry (step 3), never assume one — a hardcoded port
  collides with a concurrent agent's run and, worse, can silently answer from somebody
  else's daemon.
