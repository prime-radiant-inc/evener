# reconnect-auto-resume: dead daemon transparently respawned on next user turn

**What this covers**: katas `e465` (commit `b46d0de`), `t65c`
(`d2b5102`), `ws5f` (`d02d386`), `xcas` (`aecf225`). When the daemon
backing a session has died but the session is still navigable, the
hub's `MethodTurnStart` should silently spawn a fresh daemon via
`hubThreadResume` and replay the user's turn. This is the
"server-side Layer 3" of the mggf design; the UI `Reconnect & retry`
button uses the SAME path.

## Pre-state

- Hub running with `--serf` set so it can spawn fresh daemons.
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
   ls /home/jesse/.serf/run/*.json | xargs -I{} cat {} | \
     python3 -c "import sys, json; \
       [print(d.get('pid'), d.get('session_id')) for d in [json.loads(l) for l in sys.stdin if l.strip().startswith('{')]]"
   ```
   Or grep ps: `ps aux | grep "serf serve.*<session_id>"`.
3. `kill <pid>`. Confirm `/home/jesse/.serf/run/<pid>.json` is gone
   and the process is dead.
4. Navigate to `/s/<session_id>` in the browser (or it should
   already be there).
5. Verify the status ribbon now shows `ended` (or similar — past-
   only projection). Send form should still be enabled.
6. Type a new message into the send textarea
   (`textarea[placeholder="message the agent…"]`) and click the
   submit button (or `⌘↵`).

## Expected

- Within ~10-15 seconds:
  - The new user turn appears in the conversation.
  - An assistant response follows.
  - No diagnostic (no "Reconnect & retry" needed — auto-resume was
    transparent).
- On the host:
  - A NEW daemon process exists with `--resume <session_id>` in its
    args.
  - A NEW rendezvous file at `/home/jesse/.serf/run/<new-pid>.json`.
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
- The `e465` UI button (`Reconnect & retry`) is only surfaced when
  a `source=hub` diagnostic exists. This scenario tests the path
  WITHOUT a diagnostic — the auto-resume fires from the server
  even on a "cold" send to a dead daemon. The button is the user-
  visible escape hatch for cases where the auto-resume itself
  failed (e.g. spawner not configured, launch config invalid).
- If the daemon was hung-but-alive (not killed; deadlocked), `xcas`
  is the relevant kata — the dial timeout maps to `SessionUnavailable`
  and triggers the same resume. Worth a separate hung-daemon
  scenario.
- The spawn of the resume daemon uses the SAME launch config the
  original session had. If the launch config has been deleted /
  invalidated since the original spawn, resume will surface a
  `HubLaunchError`. That's correct behavior — the user can't
  recover without restoring config.
