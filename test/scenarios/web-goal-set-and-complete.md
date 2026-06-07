# web-goal-set-and-complete: set a /goal from the web palette and watch it drive to completion

**What this covers**: the `/goal` objective engine on the web surface
(branch `goal-objective-engine`, commits `4a6221e9` set/clear palette +
`77237424` hardening). Exercises four things in one run:

- **goal/set via ⌘K palette** — the "Set session goal" command
  (`search.js`, id `goal`) calls `window.SerfAppwire.request("goal/set", …)`.
- **A6 capability gate** — `goal/set` is pre-flight gated by the `Goal`
  thread capability (hub `app_rpc.go` + `app_compact.go`); a serf session
  must advertise `capabilities.goal === true` or the call is rejected.
- **B6 compact continuation marker** — each autonomous continuation turn
  renders as a `SYSTEM_MESSAGE` titled **Goal** whose text is the compact
  marker (`Continuing toward: <objective>`), NOT the ~2.5KB rendered
  continuation prompt. The full prompt goes to the model, never the UI.
- **status pill + terminal report** — the input strip shows
  `goal <status> · <N> turns` (`templates/partials/input_strip.html`);
  on completion a `systemAnnouncement("goal", "Goal", …)` reports why it
  stopped and the pill flips to `complete`.

The Go layer covers the gate and persistence with unit tests
(`cmd/serf-hub/app_rpc_test.go` `TestHubRPCGoalSetGatedByCapability`,
`agent/session_goal_*_test.go`); this is the live web counterpart that
proves the palette, the pill, and the marker actually render.

## Pre-state

- Build fresh from the branch and run a hub (see `docs/agentic-testing.md`
  "Setup checklist"). The default `0.0.0.0:9180` may host an unrelated
  hub; bind the test hub to a free port and use it consistently:
  ```bash
  go build -o /tmp/serf-hub-test ./cmd/serf-hub
  go build -o /tmp/serf-test ./cmd/serf
  /tmp/serf-hub-test -addr 127.0.0.1:9185 -serf /tmp/serf-test &
  sleep 2
  TOKEN=$(cat ~/.serf/auth-token); HUB=http://127.0.0.1:9185
  ```
- OpenAI usable (`OPENAI_API_KEY` in env or `~/.serf/credentials.toml`).
  Goal turns use a real model; `openai/gpt-5.4-mini` is enough.
- A Chrome session that can authenticate against the test hub.

## Steps

Set up a hermetic workdir and spawn a serf session. No AGENTS.md pacing
trick is needed — we *want* the goal to make progress quickly:

```bash
tmpdir=$(mktemp -d -t serf-e2e-goal-XXXXX)
resp=$(curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "{\"prompt\":\"Say hello and stop.\",\"model\":\"openai/gpt-5.4-mini\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
  "$HUB/api/spawn")
SID=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['session_id'])")
# Wait for the spawn turn to settle to idle before setting a goal.
for i in $(seq 1 60); do
  state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
    | python3 -c "import json,sys; print(json.load(sys.stdin).get('state',''))" 2>/dev/null)
  [ "$state" = "idle" ] && break; sleep 1
done
```

1. **Confirm the session is live and idle** (the precondition for the
   `Goal` capability):
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | python3 -c "import json,sys; print('state =', json.load(sys.stdin).get('state'))"
   ```
   **Expected:** `state = idle`. Note: the `Goal` capability lives on the
   **appwire** `ThreadCapabilities` (which the hub gate reads), NOT on the
   REST `/api/sessions` shape — `hubCapabilitiesFromAppwire` deliberately
   omits it, so `capabilities.goal` over REST is always absent. A6 is proven
   positively by step 3 (the `goal/set` call succeeds because the gate read
   `appCapabilities.Goal == true`) and negatively by the unit test
   `TestHubRPCGoalSetGatedByCapability`.

2. **Authenticate the browser and open the session:**
   ```text
   navigate http://127.0.0.1:9185/auth?token=<TOKEN>&next=/s/<SID>
   await_element #composer, .workspace, [data-session]
   ```
   (Use the literal token from `~/.serf/auth-token`, not the path.)

3. **Open the palette and set a goal.** Press ⌘K (or `eval` the palette
   directly), choose **Set session goal**, and enter a two-step objective
   that forces at least one continuation turn:
   > `Create a file seed.txt containing the number 7. Then create double.txt containing that number doubled (14). Verify both files, then mark the goal complete.`

   Driving it directly through the appwire client is the robust path:
   ```javascript
   await window.SerfAppwire.request("goal/set", {
     ref: window.SerfAppwire.refForSession(window.SerfRenderer.sessionId),
     objective: "Create a file seed.txt containing the number 7. Then create double.txt containing that number doubled (14). Verify both files, then mark the goal complete."
   })
   ```
   **Expected:** the promise resolves (no `Unavailable` error). The agent
   begins working autonomously. This resolution IS the live A6-positive
   proof: the hub gate (`ensureThreadActionAvailable(…, "goal")`) forwards
   `goal/set` to the source only when the appwire `Goal` capability is set.

4. **Observe the status pill while the goal runs.** Poll the session, or
   read the input strip in the DOM:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | python3 -c "import json,sys; d=json.load(sys.stdin); g=d.get('goal') or {}; print('pill:', g.get('status'), g.get('iterations'),'turns | state', d.get('state'))"
   ```
   **Expected:** at least one poll shows `pill: active <N> turns` with
   `N >= 1`. In the DOM, `.status-item.goal .status-value` reads
   `active · <N> turns`.

5. **Verify the continuation marker is compact [B6].** Read the rendered
   transcript for the "Goal" system messages:
   ```javascript
   Array.from(document.querySelectorAll(".system-message, [data-system-message]"))
     .map(el => el.textContent.trim())
     .filter(t => /goal/i.test(t))
   ```
   Or inspect the daemon's authoritative record — the steering turns it
   actually sent the model:
   ```bash
   TS=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   grep -c '"kind":"STEERING"' "$TS"
   ```
   **Expected:** the web "Goal" continuation message text is the short
   marker `Continuing toward: Create a file seed.txt …` (one line). It must
   **not** contain the continuation scaffolding — if the rendered text
   includes phrases like "You are continuing to work toward" or the
   `update_goal` tool instructions (hundreds/thousands of chars), B6 has
   regressed and the test fails.

6. **Wait for completion and read the terminal report.**
   ```bash
   for i in $(seq 1 120); do
     g=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
       | python3 -c "import json,sys; print((json.load(sys.stdin).get('goal') or {}).get('status',''))" 2>/dev/null)
     [ "$g" = "complete" ] || [ "$g" = "blocked" ] && { echo "final=$g (i=$i)"; break; }
     sleep 2
   done
   cat "$tmpdir/seed.txt" "$tmpdir/double.txt" 2>/dev/null
   ```
   **Expected:** `final=complete`; `seed.txt` is `7` and `double.txt` is
   `14`. The web transcript shows a final **Goal** system announcement
   reporting completion, and the pill reads `complete`. Falsification: if
   it ends `blocked` with files present and correct, the no-progress
   breaker mis-fired; if it never reaches a terminal state within ~4 min,
   the gate stopped issuing continuations.

## Cleanup

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID/shutdown" >/dev/null 2>&1
rm -rf /tmp/serf-e2e-goal-*
pkill -f serf-hub-test   # only if you started the test hub
```

## Sharp edges

- **Model nondeterminism.** A capable model may finish in a single turn
  (0 continuations) — then step 5 has no continuation message to inspect.
  The two-step ordered objective biases toward ≥1 continuation; if you get
  0, re-run or make the objective depend on reading back the first file.
- **Don't assert the exact iteration count.** `N` depends on the model.
  Assert `active` then `complete` and `N >= 1`, never `N == k`.
- **`goalEndText` formatting** ("complete" vs a reason string) is owned by
  the projector; grep for the status word, not an exact sentence.
- **A6 negative path** (goal/set rejected on a non-serf source, e.g.
  codex) is not reachable from a serf session here; it's covered by
  `TestHubRPCGoalSetGatedByCapability`. Step 1 verifies the positive side
  (capability present) live.
