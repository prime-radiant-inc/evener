# web-goal-set-and-complete: set a /goal from the web palette and watch it drive to completion

**What this covers**: the `/goal` objective engine on the web surface
(branch `goal-objective-engine`, commits `4a6221e9` set/clear palette +
`77237424` hardening). Exercises four things in one run:

- **goal/set via ⌘K palette** — the "Set session goal" command
  (`shell/palette/commands.ts:464-476`, id `goal`, capability-scoped) calls
  `threadsStore.getState().setGoal(ref, objective)`. The same call backs
  Session actions → **Set goal…** and the `GoalControl` dialog
  (`panes/session/chrome/GoalControl.tsx:153,165`).
- **A6 capability gate** — `goal/set` is pre-flight gated by the `Goal`
  thread capability inside `setGoalWithResume`
  (`cmd/serf-hub/app_session_resume.go#setGoalWithResume`, whose comment names `/par A6`);
  a serf session must advertise the appwire `Goal` capability or the call is
  rejected.
- **B6 compact continuation marker** — each autonomous continuation turn is
  projected as a `systemMessage` item described `Goal`
  (`internal/appprojector/appwire_projection.go:232,259-267`) whose text is
  the compact marker `Continuing toward: <objective>`
  (`agent/session_lifecycle.go:1386`), NOT the ~2.5KB rendered continuation
  prompt. The full prompt goes to the model as a steering turn, never to the
  UI.
- **status chip + terminal report** — a set goal renders a
  `Goal: <status>` chip whose popover
  (`[data-testid="goal-popover"]`, `GoalControl.tsx:200-205`) reads
  `<status> · <N> iterations`; on completion the chip flips to `complete`.

The Go layer covers the gate and persistence with unit tests
(`cmd/serf-hub/app_rpc_test.go:5873` `TestHubRPCGoalSetGatedByCapability`,
`agent/session_goal_*_test.go`); this is the live web counterpart that
proves the palette, the chip, and the marker actually render.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. The
`window.SerfAppwire.request("goal/set", …)` /
`window.SerfRenderer.sessionId` route this card used to drive died with the
vanilla frontend (`660376f78`), and its replacement is **not reachable from
`eval`**: `threadsStore` is a module import with nothing on `window`. So the
browser half must go through the real UI, and the exact assertions go to
`/rpc` and REST instead. `.status-item.goal .status-value` and
`.system-message` are likewise gone; see the steps for what replaced them.

## Pre-state

- Build fresh from the branch and run a hub (see `docs/agentic-testing.md`
  "Setup checklist"). The default `0.0.0.0:9180` may host an unrelated
  hub; bind the test hub to a kernel-assigned port and use it consistently.
  This card wants the real, signed-in OpenAI credentials (below), so it
  deliberately does NOT export an isolated `$HOME` — the same
  documented OAuth-footgun exception as the Setup checklist's
  `OPENAI_API_KEY=` recipe. That real `$HOME` means this hub shares
  Jesse's real `~/.serf/hub.lock`, auth-token, and credentials/providers
  files for the duration of the run — **it will fail to start at all**
  while Jesse's real hub already holds that flock, so check for that
  first rather than debugging a mysterious startup failure. What it does
  NOT have to share is session history: exporting `XDG_STATE_HOME` (a
  var independent of `$HOME` — see `cmd/serf-hub/config.go:89-99`'s
  `DefaultStateGlob`, which prefers it over `$HOME/.local/state`)
  relocates every session this hub spawns under a scratch dir instead of
  Jesse's real `~/.local/state/serf/projects`, with no effect on the
  credentials/token/lock paths above:
  ```bash
  run=$(mktemp -d -t serf-e2e-goal-XXXXXX)
  go build -o "$run/serf-hub" ./cmd/serf-hub
  go build -o "$run/serf" ./cmd/serf
  pgrep -f 'serf-hub.*:9180' >/dev/null && \
    { echo "Jesse's real hub is running on 9180 — this card cannot start until it stops (flock at ~/.serf/hub.lock)" >&2; exit 1; }
  export XDG_STATE_HOME="$run/state"
  mkdir -p "$XDG_STATE_HOME"
  "$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" &
  HUBPID=$!
  for i in $(seq 1 50); do
    PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" 2>/dev/null | grep -oE '[0-9]+$') || true
    [ -n "$PORT" ] && break
    kill -0 "$HUBPID" 2>/dev/null || { echo "hub exited before listening:" >&2; cat "$run/hub.log" >&2; exit 1; }
    sleep 0.1
  done
  [ -n "$PORT" ] || { echo "hub never logged a listening port" >&2; exit 1; }
  HUB=http://127.0.0.1:$PORT
  TOKEN=$(cat ~/.serf/auth-token)
  ```
- OpenAI usable (`OPENAI_API_KEY` in env or `~/.serf/credentials.toml`).
  Goal turns use a real model; `openai/gpt-5.4-mini` is enough.
- A Chrome session that can authenticate against the test hub, and a real SPA
  bundle (`make build-web` — a checkout that has never run it serves a
  one-line `frontend/dist/PLACEHOLDER` and no app).

## Steps

Set up a hermetic workdir and spawn a serf session. No AGENTS.md pacing
trick is needed — we *want* the goal to make progress quickly:

```bash
tmpdir=$(mktemp -d -t serf-e2e-goalwd-XXXXX)
resp=$(curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d "{\"prompt\":\"Say hello and stop.\",\"model\":\"openai/gpt-5.4-mini\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
  "$HUB/api/spawn")
SID=$(echo "$resp" | jq -r '.session_id')
# Wait for the spawn turn to settle to idle before setting a goal.
for i in $(seq 1 60); do
  state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
  [ "$state" = "idle" ] && break; sleep 1
done
```

1. **[browser-free] Confirm the session is live and idle** (the precondition
   for the `Goal` capability):
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq '{state, live}'
   ```
   **Expected:** `state` is `idle`. Note: the `Goal` capability lives on the
   **appwire** `ThreadCapabilities` (which the hub gate reads), NOT on the
   REST `/api/sessions` shape — `hubCapabilitiesFromAppwire`
   (`cmd/serf-hub/web_api_tree.go#hubCapabilitiesFromAppwire`) deliberately omits it, so
   `capabilities.goal` over REST is always absent. A6 is proven
   positively by step 2 (the `goal/set` call succeeds because the gate read
   `appCapabilities.Goal == true`) and negatively by the unit test
   `TestHubRPCGoalSetGatedByCapability`.

2. **[browser-free] Set the goal over the wire — the exact A6-positive
   proof.** Dial `ws://127.0.0.1:$PORT/rpc` with
   `Authorization: Bearer $TOKEN`, `initialize`, then call:
   ```json
   {"method":"goal/set","params":{"ref":"local:<SID>",
    "objective":"Create a file seed.txt containing the number 7. Then create double.txt containing that number doubled (14). Verify both files, then mark the goal complete."}}
   ```
   Param and response shapes are `appwire.GoalSetParams` /
   `GoalSetResponse` (`appwire/types.go:986-998`). **Expected:** no error
   (an `Unavailable` here means the gate refused). `started` may be either
   value — it reports only whether the loop began *immediately*; a goal set
   while a turn runs is still set and starts after it (`:991-995`).

3. **[browser] The same action through the real UI.** Authenticate and open
   the session:
   ```text
   navigate $HUB/auth?token=<TOKEN>&next=/s/local:<SID>
   await_element [data-testid="composer-input-card"]
   ```
   (Use the literal token from `~/.serf/auth-token`, not the path. Note the
   ref form — a bare `/s/<SID>` renders "Page not found" by design.) Then
   open the ⌘K palette, run **Set session goal**, and enter the same
   objective — or use Session actions → **Set goal…**, type into the
   `Objective` textarea, and press **Save** (`GoalControl.tsx:224-238`).
   There is no `window` handle to call `setGoal` through; drive the control.

4. **[browser-free] Watch the goal run.** The flattened goal fields ride on
   the REST detail (`hubapi.SessionDetail.GoalStatus` / `GoalIterations`,
   `hubapi/types.go:164-169`) — note they are **top-level**
   `goal_status`/`goal_iterations`, not a nested `goal` object:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | jq '{goal_status, goal_iterations, state}'
   ```
   **Expected:** at least one poll shows `goal_status: "active"` with
   `goal_iterations >= 1`.

5. **[browser] Read the chip and the continuation marker [B6].**
   ```javascript
   (() => {
     const chip = [...document.querySelectorAll("button")]
       .map((b) => b.textContent)
       .find((t) => t && t.startsWith("Goal: "));
     return {
       port: location.port,                        // page-identity check, always
       chip,                                       // "Goal: active" | "Goal: complete"
       goalNotices: [...document.querySelectorAll('[data-testid="system-notice-line"]')]
         .map((el) => el.textContent)
         .filter((t) => /continuing toward/i.test(t)),
     };
   })()
   ```
   Click the chip to open `[data-testid="goal-popover"]` and read its status
   line (`<status> · <N> iterations`).

   Cross-check against the daemon's authoritative record — the steering turns
   it actually sent the model:
   ```bash
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:40
   ```
   **Expected:** every entry in `goalNotices` is the short one-line marker
   `Continuing toward: Create a file seed.txt …`. It must **not** contain the
   continuation scaffolding — if the rendered text includes phrases like
   "You are continuing to work toward" or the `update_goal` tool instructions
   (hundreds/thousands of chars), B6 has regressed and the test fails.

6. **[browser-free] Wait for completion and read the terminal report.**
   ```bash
   for i in $(seq 1 120); do
     g=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
       | jq -r '.goal_status // ""')
     { [ "$g" = "complete" ] || [ "$g" = "blocked" ]; } && { echo "final=$g (i=$i)"; break; }
     sleep 2
   done
   cat "$tmpdir/seed.txt" "$tmpdir/double.txt" 2>/dev/null
   ```
   **Expected:** `final=complete`; `seed.txt` is `7` and `double.txt` is
   `14`. In the browser the chip reads `Goal: complete` and the transcript
   carries a final Goal system notice reporting why it stopped.
   Falsification: it ends `blocked` with files present and correct — the
   no-progress breaker mis-fired; or it never reaches a terminal state
   within ~4 min — the gate stopped issuing continuations.

## Cleanup

```bash
curl -s -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" -d '{}' \
  "$HUB/api/sessions/local:$SID/shutdown" >/dev/null 2>&1
kill "$HUBPID" 2>/dev/null
rm -rf "$tmpdir" "$run"          # $run holds the binaries, hub.log and XDG_STATE_HOME
```

Remove `$run` and `$tmpdir` by name, never a `/tmp/serf-e2e-*` glob — a
wildcard cleanup deletes every other concurrent scenario's workdir too.
Leave Jesse's real `~/.serf` and `~/.local/state/serf` untouched; the
`XDG_STATE_HOME` export above is what keeps this run's sessions out of them.

## Sharp edges

- **The web's goal setter is not callable from `eval`.** `threadsStore` is a
  module-scoped zustand store; nothing is published on `window`. An `eval`
  that tries `window.SerfAppwire.request("goal/set", …)` throws, and one that
  optional-chains it **fails open** — reporting "no goal set" for what looks
  exactly like a real regression. Set the goal through `/rpc` (step 2) for the
  exact assertion and through the palette/dialog (step 3) for the UI one.
- **`goal_status`/`goal_iterations` are flat, top-level REST fields.** There
  is no `goal` object on `/api/sessions/<ref>`; `hubapi` cannot depend on
  `appwire`, so `appwire.GoalState` (`appwire/types.go:356-359`) is
  flattened on the way out (`hubapi/types.go:164-169`). A poller reading
  `.goal.status` gets `null` forever and reports a false failure.
- **`GoalSetResponse.started === false` is not an error.** It is false when
  the objective was cleared, when a turn is already running (the gate picks
  the goal up after it), or when no immediate start was possible — the goal
  is set in every one of those cases (`appwire/types.go:991-995`).
- **The goal chip only exists once a goal is set.** `GoalControl` renders
  nothing at all with `model.goal` null (`GoalControl.tsx:193`) — the
  "Set goal…" entry point lives in the session actions menu and the palette,
  not as a permanent control on the strip. An absent chip before step 2/3 is
  correct.
- **Model nondeterminism.** A capable model may finish in a single turn
  (0 continuations) — then step 5 has no continuation message to inspect.
  The two-step ordered objective biases toward ≥1 continuation; if you get
  0, re-run or make the objective depend on reading back the first file.
- **Don't assert the exact iteration count.** `N` depends on the model.
  Assert `active` then `complete` and `N >= 1`, never `N == k`.
- **The goal end text's wording is owned by the projector**; grep for the
  status word, not an exact sentence.
- **A6 negative path** (goal/set rejected on a non-serf source, e.g.
  codex) is not reachable from a serf session here; it's covered by
  `TestHubRPCGoalSetGatedByCapability`. Step 2 verifies the positive side
  live.
- **Consecutive system notices group.** `SystemNoticeItem` folds a run of
  adjacent system items into a `<details data-testid="system-notice-group">`
  (`transcript/messages/SystemNoticeItem.tsx:290-294`). Each goal
  continuation opens its own turn, so they normally stay separate lines —
  but even when grouped, every member still renders its own
  `[data-testid="system-notice-line"]` inside the (possibly collapsed)
  `<details>`, so step 5's query holds either way.
