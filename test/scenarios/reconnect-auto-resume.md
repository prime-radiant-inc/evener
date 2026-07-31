# reconnect-auto-resume: dead daemon transparently respawned on next user turn

**What this covers**: katas `e465` (commit `b46d0de`), `t65c`
(`d2b5102`), `ws5f` (`d02d386`), `xcas` (`aecf225`). When the daemon
backing a session has died but the session is still navigable, the
hub's `MethodTurnStart` should silently spawn a fresh daemon via
`hubThreadResume` and replay the user's turn. This is the
"server-side Layer 3" of the mggf design; the UI `Reconnect & retry`
button uses the SAME path.

## Pre-state

- Hub running with `--serf` set so it can spawn fresh daemons, started
  under an isolated `$HOME` on a free port per the Setup checklist in
  `docs/agentic-testing.md`. Every `$HOME/.serf/run` path below is that
  isolated home's rendezvous dir (`rendezvous.DefaultDir()`,
  `rendezvous/rendezvous.go:40-49`), never Jesse's real one — this card
  kills daemons by PID, so pointing it at the real run dir would kill
  his live sessions.
- A session exists whose last completed turn ended in idle (i.e.
  not stuck-processing — the r6y9 case is different). Spawn one if
  needed: `curl ... /api/spawn` with a quick prompt.
- The daemon for that session must be killed (and its rendezvous
  file gone — happens automatically on clean shutdown).

## Steps

1. Spawn a fresh session with a one-turn prompt. Capture the
   `session_id`. Wait for it to reach idle.
2. Find the daemon PID:
   ```bash
   ls "$HOME"/.serf/run/*.json | xargs -I{} cat {} | \
     python3 -c "import sys, json; \
       [print(d.get('pid'), d.get('session_id')) for d in [json.loads(l) for l in sys.stdin if l.strip().startswith('{')]]"
   ```
   Or grep ps: `ps aux | grep "serf serve.*<session_id>"`.
3. `kill <pid>`. Confirm `$HOME/.serf/run/<pid>.json` is gone
   and the process is dead.
4. Navigate to `/s/<session_id>` in the browser (or it should
   already be there).
5. Verify the status ribbon now shows `ended` (or similar — past-
   only projection). Send form should still be enabled.
6. Type a new message into the composer's textarea — find it via
   `[data-testid="composer-input-card"]`
   (`panes/session/composer/Composer.tsx:760`), not by placeholder text:
   the placeholder is `Message the agent…` on a live session but
   `Send a follow-up…` once the session reads as ended (`:782`), which
   is exactly the state step 5 just confirmed. Then click the submit
   button (`[data-testid="composer-submit"]`) or press `⌘↵`.

## Expected

- Within ~10-15 seconds:
  - The new user turn appears in the conversation.
  - An assistant response follows.
  - No diagnostic (no "Reconnect & retry" needed — auto-resume was
    transparent).
- On the host:
  - A NEW daemon process exists with `--resume <session_id>` in its
    args.
  - A NEW rendezvous file at `$HOME/.serf/run/<new-pid>.json`.
- Falsification: a diagnostic appears with text like "stream ended"
  or "rendezvous timeout" — the resume chain failed. Or no new
  daemon spawned.

## Cleanup

- The respawned daemon will keep running until next idle timeout
  or `kill`. Safe to leave; or `kill <new-pid>` if you want a
  clean ps.

## Sharp edges

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
- The spawn of the resume daemon uses the SAME launch config the
  original session had. If the launch config has been deleted /
  invalidated since the original spawn, resume will surface a
  `HubLaunchError`. That's correct behavior — the user can't
  recover without restoring config.
