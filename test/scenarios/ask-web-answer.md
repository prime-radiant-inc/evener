# ask-web-answer: web UI answers an ask_user question and the reply reaches the model

**What this covers**: spec §8 row `ask-web-answer.md` (`docs/superpowers/specs/2026-07-03-ask-user-question-tool-design.md`)
— the round-trip through the dormant `awaiting`/needs-you scaffolding (§5.1, §5.4) and the
web answering layer (§6.1): a session ends its turn on a posted question, rests `awaiting`,
the web form renders the model's real options, and the composed reply reaches the model as
an ordinary next user message.

## Pre-state

- Build fresh side-by-side binaries from this worktree:
  ```bash
  go build -o /tmp/serf-ask     ./cmd/serf
  go build -o /tmp/serf-hub-ask ./cmd/serf-hub
  ```
- Export credentials from the MAIN checkout (not this worktree):
  ```bash
  set -a; . /Users/jesse/prime-radiant/toil-suite/serf/.env; set +a
  ```
- Start the hub. **Never pass `--state-dir`/`SERF_STATE_DIR`** to the hub or any daemon in
  this scenario — both the hub and each spawned daemon must use the default state layout so
  the hub's roster and each daemon's `/status` agree:
  ```bash
  /tmp/serf-hub-ask -addr 127.0.0.1:9280 -serf /tmp/serf-ask &
  sleep 2
  TOKEN=$(cat ~/.serf/auth-token)
  HUB=http://127.0.0.1:9280
  curl -s -o /dev/null -w "%{http_code}\n" "$HUB/"   # → 401 means it answered
  ```
- `openai/gpt-5.5` usable (the default live model for this whole ask_user card set).
- `superpowers-chrome:browsing` available for the browser steps.

## Steps

1. Spawn a session whose first turn asks a question:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-ask-web-XXXXX)
   body=$(jq -n --arg wd "$tmpdir" '{
     prompt: "Before doing any other work, call the ask_user tool once. Ask exactly one question: header \"DB choice\", question \"Which database should the new ingest path use?\", with exactly two options: Postgres (recommended: true, detail \"matches prod; heavier local setup\") and SQLite (detail \"zero setup; diverges from prod\"). Do not do anything else first.",
     model: "openai/gpt-5.5",
     working_dir: $wd,
     harness: "serf",
     branch: "",
     access_mode: "full",
     agent: "default",
     launch_overrides: {}
   }')
   resp=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$body" "$HUB/api/spawn")
   SID=$(echo "$resp" | jq -r '.session_id')
   echo "SID=$SID"
   ```
2. Poll until the session rests `awaiting` (the asking round ends the turn at its boundary
   — spec §5.1):
   ```bash
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" = "awaiting" ] && break
     sleep 1
   done
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq '{state, capabilities}'
   ```
3. Authenticate a browser tab and open the session (use the literal token, not its path):
   ```
   navigate http://127.0.0.1:9280/auth?token=<TOKEN>&next=/s/<SID>
   await_element [data-ask-card]
   ```
4. Inspect the rendered question card:
   ```javascript
   (() => {
     const card = document.querySelector('[data-ask-card]');
     const q = card.querySelector('[data-ask-question]');
     const opts = [...q.querySelectorAll('[data-ask-option]')].map(o => ({
       label: o.dataset.optionLabel, recommended: o.classList.contains('recommended')
     }));
     return {
       hasAgentQuestionFrame: !!card.closest('.agent-question'),
       header: q.querySelector('.ask-question-header').textContent,
       opts,
     };
   })()
   ```
5. Deliberately pick **against** the model's recommendation — proves the reply carries the
   user's real choice, not an echo of the suggestion — and attach a note:
   ```javascript
   (() => {
     const q = document.querySelector('[data-ask-question]');
     const sqlite = [...q.querySelectorAll('[data-ask-option]')].find(o => o.dataset.optionLabel === 'SQLite');
     sqlite.querySelector('[data-ask-option-input]').click();
     q.querySelector('[data-ask-note-toggle]').click();
     const note = q.querySelector('[data-ask-note-field]');
     note.value = 'only for the prototype; revisit before prod';
     note.dispatchEvent(new Event('input', { bubbles: true }));
     return document.querySelector('[data-ask-answered-count]').textContent;
   })()
   ```
6. Send the answer:
   ```
   click [data-ask-send-btn]
   ```
7. Wait for the session to leave `awaiting`:
   ```bash
   for i in $(seq 1 90); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" != "awaiting" ] && break
     sleep 1
   done
   echo "final state=$state"
   ```
8. Confirm the round-trip on disk:
   ```bash
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:6
   ```

## Expected

- After step 2: `state` is `"awaiting"`; `capabilities.send` is `true`, `capabilities.queue`
  is `false` (an awaiting session is at-rest, not mid-turn).
- After step 4: `opts` contains both `Postgres` and `SQLite`; `Postgres.recommended` is
  `true` (if the model followed the prompt) and its row carries a `.ask-option-tag` reading
  `recommended`.
- After step 5: the answered-count reads `1 of 1 question answered`.
- After step 6: `[data-ask-card]` is replaced by a `.ask-settled-line` reading
  `◆ asked … — answered: …`.
- After step 8: the outline shows the `ask_user` tool result carrying the ack text
  `questions posted; answers arrive in the user's reply after your turn ends`, followed by a
  `USER_INPUT` turn whose text is exactly the §4.3 form:
  ```
  [answers]
  1. [DB choice] → "SQLite" — note: "only for the prototype; revisit before prod"
  ```
  and the assistant's next turn references **SQLite** (the actual choice), not Postgres (the
  recommendation) — proof the reply reached the model as real input, not a cached echo.
- Falsification: if the thread never shows `awaiting`, or the form's reply does not reach
  the model as the next user message, the feature is broken.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
pkill -f serf-hub-ask
rm -rf "$tmpdir" /tmp/serf-ask /tmp/serf-hub-ask
```

## Sharp edges

- The hub's roster refresh (which feeds `/api/search`, the sidebar, and the notification
  poller) ticks every 5s (`cmd/serf-hub/internal/hubcore/roster.go`); the direct
  `/api/sessions/local:<sid>` call used above talks to the live daemon and reflects
  `awaiting` sooner. Don't confuse the two cadences — see `ask-cross-session-notify.md` for
  the roster-level path.
- `recommended` ordering is a stable sort (recommended option first); if the model didn't
  mark a recommendation the step-4 tag check is simply moot, not a failure — the
  falsification only cares that the reply round-trips.
- Answering from any client is an ordinary `turn/start`; nothing about the daemon changes
  because the reply happened to come from a browser rather than a CLI `send`. See
  `ask-two-clients.md` for what happens when two clients race to answer the same question.
- **Step 7's poll checks for leaving `awaiting`, not for reaching `idle`.** Post-merge, an
  answered ask-then-settle session rests `awaiting` again (your-move), not `idle` — the same
  unified-rest-state behavior `status-vocabulary-roundtrip.md` pins. Polling for
  `state = "idle"` would never match (the session is already `awaiting` the instant this loop
  starts, from the pending question itself) and either hang for the full timeout or, worse,
  falsely appear to "work" via timeout exhaustion. `state != "awaiting"` catches the real
  transition (through `active` while the reply's turn runs) regardless of which settled value
  it lands on afterward — though on a fast round trip the 1s poll granularity can miss the
  brief `active` window entirely and exhaust the loop still reading `awaiting` (observed live:
  the reply settled back to `awaiting` between two consecutive polls). That is not itself a
  failure; step 8's outline read is the actual proof the round trip completed, independent of
  whether this loop happened to observe the transition.
