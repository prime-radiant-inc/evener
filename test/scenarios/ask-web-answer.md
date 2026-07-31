# ask-web-answer: web UI answers an ask_user question and the reply reaches the model

**What this covers**: spec §8 row `ask-web-answer.md` (`docs/superpowers/specs/2026-07-03-ask-user-question-tool-design.md`)
— the round trip through the `awaiting`/needs-you scaffolding (§5.1, §5.4) and the web
answering layer (§6.1): a session ends its turn on a posted question, rests `awaiting`,
the web surfaces the model's real options, and the composed reply reaches the model as an
ordinary next user message.

**Surface**: see `docs/agentic-testing.md` — "The REST surface, and what is no longer on
it" for the endpoints, "Driving the web UI with superpowers-chrome:browsing" for the
`/auth` recipe and selector map. Two facts this card is built on, because they invert what
it used to say:

- The answering surface is the **composer's ask dock**
  (`cmd/serf-hub/frontend/src/panes/session/composer/askDock/`), not a form inside the
  transcript. The transcript's own `ask_user` row is deliberately read-only and says so
  (`panes/session/transcript/tools/askUser.tsx:16-21`, `:105`).
- Every `[data-ask-card]` / `[data-ask-option]` / `.ask-question-header` /
  `[data-ask-note-toggle]` / `[data-ask-send-btn]` / `.ask-settled-line` / `.agent-question`
  selector this card used to drive died with the vanilla frontend (`660376f78`) and has no
  same-named successor. The live hooks are `[data-ask-response-dock]` (`AskDock.tsx:309`)
  and `[data-ask-question]` / `[data-ask-key]` (`AskQuestionCard.tsx:233`); everything
  inside is addressed by accessible name.

Steps 1-2 and 7-8 are **browser-free** (REST + on-disk transcript) and are where the exact
assertions live. Steps 3-6 need Chrome and assert the rendered question and the answering
gesture.

## Pre-state

- Build fresh binaries and start an isolated hub, per the Setup checklist in
  `docs/agentic-testing.md`. Never a real hub, never a hardcoded port — one `mktemp` run
  directory names everything:
  ```bash
  run=$(mktemp -d -t serf-e2e-ask-web-XXXXXX)
  go build -o "$run/serf"     ./cmd/serf
  go build -o "$run/serf-hub" ./cmd/serf-hub
  ```
- Export credentials from the MAIN checkout (not this worktree):
  ```bash
  set -a; . /Users/jesse/prime-radiant/toil-suite/serf/.env; set +a
  ```
- **Isolate.** This card has no OAuth requirement (it authenticates via the exported
  `OPENAI_API_KEY` above, not stored OAuth state), so it gets the normal Setup-checklist
  treatment: a throwaway `$HOME` keeps auth-token, `credentials.toml`, and session history
  off the real `~/.serf` and `~/.local/state/serf` entirely.
  ```bash
  export HOME="$run/home"
  mkdir -p "$HOME"
  unset XDG_STATE_HOME
  ```
- Start the hub on a kernel-assigned port and read the port back from its own log line.
  **Never pass `--state-dir`/`SERF_STATE_DIR`** to the hub or any daemon in this scenario —
  both the hub and each spawned daemon must use the default state layout so the hub's
  roster and each daemon's `/status` agree (the isolated `$HOME` above still gives each its
  own default):
  ```bash
  "$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" &
  HUBPID=$!
  echo "$HUBPID" >"$run/hub.pid"
  for i in $(seq 1 50); do
    PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" 2>/dev/null | grep -oE '[0-9]+$') || true
    [ -n "$PORT" ] && break
    kill -0 "$HUBPID" 2>/dev/null || { echo "hub exited before listening:" >&2; cat "$run/hub.log" >&2; exit 1; }
    sleep 0.1
  done
  HUB=http://127.0.0.1:$PORT
  TOKEN=$(cat "$HOME/.serf/auth-token")
  curl -s -o /dev/null -w "%{http_code}\n" "$HUB/"   # → 401 means it answered
  export SERF_E2E_RUN="$run"   # how the sibling ask cards find this hub
  ```
- This card owns the hub the rest of the ask set reuses. `$SERF_E2E_RUN` is the
  whole handoff — `$HOME`, the port, the token and the pid all re-derive from files
  under it, so no sibling has to know a port number and two agents running the set
  concurrently never meet. See "Handing this hub to a sibling card" in
  `docs/agentic-testing.md`.
- The browser steps need a real SPA bundle: a checkout that has never run `make build-web`
  ships a one-line `frontend/dist/PLACEHOLDER` and serves no app. Build the frontend
  **before** the hub (rebuild-matrix item 3 in the runbook).
- `openai/gpt-5.5` usable (the default live model for this whole ask_user card set).
- `superpowers-chrome:browsing` available, with your own Chrome profile claimed
  (`set_profile <worktree-name>`) before the first `use_browser` call.

## Steps

1. **(browser-free)** Spawn a session whose first turn asks a question:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-ask-web-wd-XXXXX)
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
   SID=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$body" "$HUB/api/spawn" | jq -r '.session_id')
   echo "SID=$SID"
   ```
2. **(browser-free)** Poll until the session rests `awaiting` (the asking round ends the
   turn at its boundary — spec §5.1), then read the capability gating:
   ```bash
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" = "awaiting" ] && break
     sleep 1
   done
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq '{state, capabilities}'
   ```
3. **(browser)** Authenticate a tab and open the session. Note the ref form — a bare
   `/s/<SID>` renders "Page not found" by design:
   ```
   navigate $HUB/auth?token=<TOKEN>&next=/s/local:<SID>
   await_element [data-ask-response-dock]
   ```
4. **(browser)** Inspect the rendered question. Options are plain radios labelled by the
   model's own option label (`AskQuestionCard.tsx:142-164`), so `aria-label` is the handle;
   the `recommended` tag is a sibling span inside the same `<label>` (`:162`):
   ```javascript
   (() => {
     const dock = document.querySelector('[data-ask-response-dock]');
     const q = dock.querySelector('[data-ask-question]');
     const opts = [...q.querySelectorAll('input[type="radio"][aria-label]')]
       .map((el) => el.getAttribute('aria-label'))
       .filter((l) => l !== 'Something else…' && l !== 'let serf decide');
     const tagged = [...q.querySelectorAll('input[type="radio"][aria-label]')]
       .filter((el) => el.closest('label')?.textContent.includes('recommended'))
       .map((el) => el.getAttribute('aria-label'));
     return {
       port: location.port,                       // page-identity check, always
       path: location.pathname,                   // /s/local:<SID>
       anchor: dock.querySelector('[role="status"]')?.textContent,
       askKey: q.getAttribute('data-ask-key'),    // "<callId>:<idx>"
       header: q.textContent.slice(0, 40),
       opts,
       tagged,
       count: [...dock.querySelectorAll('span')].map((s) => s.textContent)
                .find((t) => / of \d+ question/.test(t)),
       composerHidden: document.querySelector('[data-testid="composer-input-card"]')?.hasAttribute('hidden'),
     };
   })()
   ```
5. **(browser)** Deliberately pick **against** the model's recommendation — proves the
   reply carries the user's real choice, not an echo of the suggestion — and attach a note.
   Use real key events for the note: it is a React-controlled input, and a bare
   `note.value = "…"` does not reach the store (see Sharp edges).
   ```javascript
   document.querySelector('[data-ask-response-dock] input[aria-label="SQLite"]').click()
   ```
   ```
   click [data-ask-response-dock] input[placeholder="note (optional)"]
   type only for the prototype; revisit before prod
   ```
   Re-read the answered count (the footer's `aria-live` span, `AskDock.tsx:244-246`):
   ```javascript
   [...document.querySelectorAll('[data-ask-response-dock] span')]
     .map((s) => s.textContent).find((t) => / of \d+ question/.test(t))
   ```
6. **(browser)** Send. The dock's primary button has no testid; address it by its text
   (`AskDock.tsx:254-256`):
   ```javascript
   (() => {
     const dock = document.querySelector('[data-ask-response-dock]');
     const send = [...dock.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Send answers');
     if (!send) return 'NO SEND BUTTON — is the label "Next question"? (multi-question batch)';
     send.click();
     return 'clicked';
   })()
   ```
7. **(browser-free)** Wait for the session to leave `awaiting`:
   ```bash
   for i in $(seq 1 90); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" != "awaiting" ] && break
     sleep 1
   done
   echo "final state=$state"
   ```
8. **(browser-free)** Confirm the round trip on disk:
   ```bash
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:6
   ```

## Expected

- **Step 2 (exact)**: `state` is `"awaiting"`; `capabilities.send` is `true` and
  `capabilities.queue` is `false` — an awaiting session is at rest, not mid-turn
  (`server/appwire_runtime.go:1046,1057`: `Send: !active && !closed`,
  `Queue: … && active && !closed`). Falsify: `queue` true while `awaiting`, or the thread
  never reports `awaiting` at all.
- **Step 4**: `opts` contains both `Postgres` and `SQLite`; `tagged` contains `Postgres`
  if the model honored `recommended: true`; `anchor` reads `Answer the agent’s questions.`
  (curly apostrophe, `AskDock.tsx:311`); `count` reads `0 of 1 question answered`;
  `composerHidden` is `true` — the plain composer card is `hidden`/`inert` while a question
  is pending (`Composer.tsx:303,761` into `widgets/promptcard/index.tsx:58`). Falsify: no
  `[data-ask-response-dock]` on an `awaiting`-with-ask session, or the plain composer is
  still writable underneath it.
- **Step 5**: the count reads `1 of 1 question answered`. Falsify: it stays `0 of 1` —
  the click or the typed note never reached `askDockStore`.
- **Step 6**: the dock unmounts entirely (`[data-ask-response-dock]` gone — `AskDock.tsx:293`
  returns `null` with no batches), the plain composer card loses `hidden`, and the
  transcript's collapsed `ask_user` row grows its answered recap: the row
  `[data-testid="tool-call-item"][data-tool-name="ask_user"]` reads
  `Asked: [DB choice] — answered: "SQLite"` (`askUser.tsx:113-127` +
  `panes/session/askShared.ts:118-150`; the note is deliberately stripped from the recap,
  `askShared.ts:88-95`). Falsify: the dock is still up after a successful send, or the row
  still reads a bare `Asked: [DB choice]` long after the reply landed.
- **Step 8 (exact)**: the outline shows the `ask_user` tool result carrying the ack text
  `questions posted; answers arrive in the user's reply after your turn ends`
  (`agent/session_tools_ask.go:22`), followed by a user turn whose text is exactly the
  §4.3 form (`composeAskAnswers`, `askDock/askCompose.ts:84-93` — byte-exact,
  golden-string tested):
  ```
  [answers]
  1. [DB choice] → "SQLite" — note: "only for the prototype; revisit before prod"
  ```
  and the assistant's next turn references **SQLite** (the actual choice), not Postgres
  (the recommendation) — proof the reply reached the model as real input, not a cached
  echo.
- Falsification (whole card): if the thread never shows `awaiting`, or the dock's reply
  does not reach the model as the next user message, the feature is broken.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" \
  -d '{}' "$HUB/api/sessions/local:$SID/shutdown" >/dev/null
kill "$HUBPID" 2>/dev/null
rm -rf "$run" "$tmpdir"
```

The old `$HUB/s/$SID/shutdown` form-POST shim is gone (`660376f78`); it 404s silently and
leaves the daemon running, which then poisons the next run's state poll.

## Sharp edges

- **The note field is a React-controlled input.** `note.value = "…"` + a synthetic `input`
  event does not update `askDockStore` — React 19 owns the value setter. Use the browser
  tool's `type` action (real key events), or, if you must do it from `eval`, go through the
  native setter: `Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,"value").set.call(note, text)`
  followed by `dispatchEvent(new Event("input",{bubbles:true}))`. The same applies to the
  "Something else…" free-text and "let serf decide" leaning inputs. Radio/checkbox
  `.click()` is fine — a native click already fires the change React listens for.
- **The primary button is not always "Send answers".** In a multi-question batch the
  footer button relabels to `Next question` while an unanswered question remains
  (`AskDock.tsx:128-129,255`). This card asks for exactly one question, so it should read
  `Send answers` — if it reads `Next question`, the model posted more than one question and
  the card's own premise is off, not the UI.
- **Recommended-first is a render-time sort only** (`AskQuestionCard.tsx:65-74`); the
  parsed data is never reordered, so the transcript's read-only row keeps the model's own
  order. If the model didn't mark a recommendation, step 4's `tagged` check is moot, not a
  failure — the falsification only cares that the reply round-trips.
- **Step 7 polls for leaving `awaiting`, not for reaching `idle`.** An answered
  ask-then-settle session rests `awaiting` again (your-move), the same unified-rest-state
  behavior `status-vocabulary-roundtrip.md` pins; polling for `idle` would never match and
  would either hang for the full timeout or falsely appear to "work" via timeout
  exhaustion. On a fast round trip the 1s granularity can miss the `active` window
  entirely and exhaust the loop still reading `awaiting` — that is not a failure; step 8's
  outline read is the actual proof, independent of whether this loop observed the
  transition.
- The hub's roster refresh (which feeds `/api/tree` and the rail) ticks every 5s
  (`cmd/serf-hub/internal/hubcore/roster.go:451`); the direct `/api/sessions/local:<SID>`
  call talks to the live daemon and reflects `awaiting` sooner. Don't confuse the two
  cadences — see `ask-cross-session-notify.md` for the roster-level path.
- Answering is an ordinary `turn/start`; nothing about the daemon changes because the
  reply came from a browser. See `ask-two-clients.md` for two clients racing the same
  question.
