# reconnect-auto-resume: dead daemon transparently respawned on next user turn

**What this covers**: katas `e465` (commit `b46d0de`), `t65c`
(`d2b5102`), `ws5f` (`d02d386`), `xcas` (`aecf225`). When the daemon
backing a session has died but the session is still navigable, the
hub should silently spawn a fresh daemon via `hubThreadResume`
(`cmd/serf-hub/app_threadlifecycle.go:212`) and replay the user's turn. This
is the "server-side Layer 3" of the mggf design; the UI `Reconnect & retry`
button uses the SAME path.

**Surface**: see `docs/agentic-testing.md`, "The REST surface, and what is no
longer on it" and "Driving the web UI" — the selector map there is the single
place these hooks are maintained. This card is now runnable **end to end
without a browser**: the resume is triggered by two entry points that share
one implementation, and the REST one needs only `curl`. The old
`textarea[placeholder="message the agent…"]` selector is stale twice over
(the placeholder is `Message the agent…`, capital M) — the composer is
`[data-testid="composer-input-card"]` now.

## Pre-state

- Hub running with `-serf` set so it can spawn fresh daemons, on an isolated
  `$HOME` and a kernel-assigned port — see the Setup checklist in
  `docs/agentic-testing.md`. Every `~/.serf` path below resolves inside that
  isolated `$HOME`; a run against a real one would kill a real daemon.
- A session exists whose last completed turn ended in idle (i.e.
  not stuck mid-turn — the r6y9 case is different). Spawn one if
  needed: `curl ... /api/spawn` with a quick prompt.
- The daemon for that session must be killed (and its rendezvous
  file gone — happens automatically on clean shutdown).

## Steps

1. **[browser-free]** Spawn a fresh session with a one-turn prompt. Capture
   the `session_id` as `SID`. Wait for it to reach `state: idle` via
   `GET $HUB/api/sessions/local:$SID`.

2. **[browser-free] Find the daemon PID.** Each daemon writes
   `~/.serf/run/<pid>.json` on startup (`rendezvous.DefaultDir`,
   `rendezvous/rendezvous.go:39-49`; file naming at `:52-77`), so the
   filename *is* the pid:
   ```bash
   RFILE=$(grep -l "\"session_id\":\"$SID\"" "$HOME"/.serf/run/*.json)
   PID=$(basename "$RFILE" .json)
   echo "pid=$PID rfile=$RFILE"
   ```
   Cross-check with `ps aux | grep "serf serve.*$SID"` if the glob is
   ambiguous.

3. **[browser-free]** `kill "$PID"`. Confirm `$RFILE` is gone and the process
   is dead (`kill -0 "$PID"` fails).

4. **[browser-free] Confirm the session reads as past-only**, and that a
   follow-up is still offered:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | jq '{state, live, capabilities: {send: .capabilities.send}}'
   ```

5. **[browser-free] Send a cold follow-up turn over REST** — this is the
   resume trigger:
   ```bash
   curl -s -i -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d '{"text":"reply with just OK"}' \
     "$HUB/api/sessions/local:$SID/send"
   ```
   `handleSend` re-resolves the session and, finding no live daemon, spawns
   one through the same `resumeRequestFor` → `Spawner.Resume` path before
   starting the turn (`cmd/serf-hub/web_session.go:69-171`, kata `x3hp`).

6. **[browser, optional]** Repeat from a browser to cover the *other* entry
   point: navigate to `/auth?token=$TOKEN&next=/s/local:$SID`, type into the
   textarea inside `[data-testid="composer-input-card"]`
   (`aria-label="Message"`, placeholder `Message the agent…`,
   `panes/session/composer/Composer.tsx:782-783`) and click
   `[data-testid="composer-submit"]`. That routes to appwire `turn/start`,
   whose hub handler retries through the same `hubThreadResume`
   (`cmd/serf-hub/app_rpc.go:330` registers it; `:356,368` do the resume via
   the `resumeTurnStartThread` alias declared at `:58`). Note the ref form —
   a bare `/s/$SID` renders "Page not found" by design.

## Expected

- Step 5's POST returns `202 Accepted` (`web_session.go:170`) — the turn is
  started asynchronously, so the resume has already succeeded by the time the
  status line lands.
- Within ~10-15 seconds of step 5 (or 6):
  - The turn runs and completes: `state` cycles `active` → `idle` and
    `turn_count` increments.
  - No diagnostic (no "Reconnect & retry" needed — auto-resume was
    transparent).
- On the host:
  - A NEW daemon process exists with `serve ... --resume $SID` in its args
    (`cmd/serf-hub/spawn.go:276`).
  - A NEW rendezvous file at `$HOME/.serf/run/<new-pid>.json`, with a
    different pid than step 2's.
- In the browser (step 6 only): the new user turn and the assistant reply
  appear as `[data-testid="turn-block"]` entries without a reload, and no
  `[data-testid="turn-failure"]` row is rendered.
- Falsification: the send returns `502` with `daemon unreachable` /
  `resume failed` (`web_session.go:165`), or a diagnostic appears with text
  like "stream ended" or "rendezvous timeout" — the resume chain failed. Or
  no new daemon spawned at all.

## Cleanup

- `POST $HUB/api/sessions/local:$SID/shutdown` — this removes the respawned
  daemon's rendezvous file as part of its own shutdown hook. Otherwise the
  daemon keeps running until its idle timeout.
- Kill the hub by the PID you captured; remove the `$run` scratch dir.

## Sharp edges

- **Two entry points, one implementation.** `turn/start` over `/rpc` and
  `POST /api/sessions/local:<id>/send` both resume a dead daemon, and both
  land on `hubThreadResume`. Steps 5 and 6 are the same assertion from two
  sides — running only step 5 still covers the resume chain. There is no
  `/s/<id>/send` shim any more; that path 404s.
- This test exercises the LOCAL source resume path. For codex /
  managed-launch sessions, `t65c` + `ws5f` cover the equivalent path
  — write a `reconnect-codex-managed.md` scenario for that flow if
  you have a working codex setup.
- The `Reconnect & retry` button (kata `e465`) is surfaced only by a
  connection-class `turn.error`, which reaches the client from the
  daemon's own diagnostic or from a persisted `TURN_FAILURE` entry —
  never from the hub. So killing a daemon does **not** produce it: the
  daemon dies before it can record anything, and the hub's relay
  re-dials in silence (kata `3h02`). This scenario is the path that
  still works, and it needs no diagnostic — auto-resume fires from the
  server on a "cold" send to a dead daemon. A companion scenario for
  the button was retired for the same reason (kata `h0cc`); the button
  itself is covered by `TurnFailureEndCap.test.tsx`.
- If the daemon was hung-but-alive (not killed; deadlocked), `xcas`
  is the relevant kata — the dial timeout maps to `SessionUnavailable`
  and triggers the same resume. Worth a separate hung-daemon
  scenario.
- **A live daemon speaking a different AppWire protocol version wedges the
  resume, and says so.** `resumeFailureError`
  (`cmd/serf-hub/app_threadlifecycle.go:318-334`) re-reads the roster and
  names the blocking pid and protocol rather than returning a bare spawn
  failure. If step 5 fails, read the error text before assuming the resume
  chain regressed.
- The spawn of the resume daemon uses the SAME launch config the
  original session had, minus the model fields, which resume deliberately
  clears so a stale catalogued model can't block it (`spawn.go:289-293`).
  If the launch config has been deleted / invalidated since the original
  spawn, resume surfaces a `HubLaunchError`. That's correct behavior — the
  user can't recover without restoring config.
