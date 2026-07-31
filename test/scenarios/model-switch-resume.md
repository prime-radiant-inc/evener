# model-switch-resume: a switch survives daemon death and the next turn runs on the switched model

**What this covers**: spec Acceptance criterion 3 ("switch → crash → resume
runs on the switched model") and N3 ("Persistence and resume": synchronous
meta flush on switch, `ResolveResumeModelRef` honoring persisted
`ProfileID`/`Model` over `SERF_MODEL`). This card kills the daemon **between**
the switch and the resume, per the brief — it is the only card in this set
that proves the switch is durable across a real process death, not just a
live-notification convergence.

## Pre-state

- An isolated hub built from the branch under test, running with `-serf` set
  so it can spawn fresh daemons on resume — the scratch `$HOME` and
  kernel-assigned port from `docs/agentic-testing.md`'s Setup checklist,
  never Jesse's real hub. The isolation is load-bearing twice over here:
  this card kills a daemon it finds by globbing the run directory, and that
  glob has to be able to name only its own daemons.
- A session spawned on model A (e.g. `openai/gpt-5.5`), idle. Confirm no
  `SERF_MODEL` env override is set for the daemon (would otherwise mask a
  regression per N3's "must not override" contract).

## Steps

1. Spawn the session on model A, wait for idle. Capture the `session_id`.
2. Switch to model B (e.g. `anthropic/claude-opus-4-6` or any second real,
   distinct instance/model) via `thread/model/set` (web chip, TUI `/model`,
   or a direct RPC call) — confirm the call returns success and the marker
   turn appears in the transcript.
3. Find and **kill** the daemon backing the session (same technique as
   `reconnect-auto-resume.md`: `ls "$HOME"/.serf/run/*.json` for the pid —
   the scratch `$HOME` from Pre-state, so the glob only ever names this
   card's own daemons — or `ps aux | grep "serf serve.*<session_id>"`).
   Confirm the rendezvous file is gone and the process is dead.
4. Trigger resume: send a new turn to the session (via the hub UI or
   `turn/start`) — the hub's auto-resume path (`hubThreadResume`) should
   spawn a fresh daemon with `--resume <session_id>`.
5. Once the new turn completes, capture the turn's `ResponseProvider` /
   `ResponseModel` (or the resolved profile the daemon logs/reports) and the
   session's effective context-window/effort ladder (e.g. via
   `thread/read`'s snapshot fields `reasoningEffortLevels`,
   `supportsReasoning`).

## Expected

- Step 2: the marker turn text is `Switched model: <A> → <B>`; the session's
  live model is B before the kill.
- Step 4: a **new** daemon process exists with `--resume <session_id>` in
  its args (confirms this went through resume, not a fresh spawn).
- Step 5 (AC 3): the new turn's response model stamp is B, not A — the
  resumed session started on the switched model, not the launch model. The
  effort ladder / `supportsReasoning` reported by the snapshot match B's
  catalog entry, not A's (context-window guarantee from N3).
- Falsification: the resumed daemon runs the turn on model A (the switch
  was lost across the crash — `meta.json`'s synchronous flush on `SetModel`
  regressed, or `ResolveResumeModelRef` let a stale/env value win); or no
  new daemon spawns at all (auto-resume itself broken, unrelated to this
  spec but would mask the assertion — note it explicitly if seen).

## Cleanup

- Kill the new daemon PID if it's still running past the test window.
- Kill the isolated hub, then `rm -rf` the run directory holding its
  binaries, its scratch `$HOME`, and the session's working dir.

## Sharp edges

- Kill the daemon with a plain `kill <pid>` (not `-9` unless the plain kill
  doesn't take) — the meta flush must already have happened synchronously
  as part of the `SetModel` RPC handler returning success in step 2, so the
  kill signal choice shouldn't matter, but a clean kill keeps the failure
  mode isolated to "was the flush synchronous" rather than "did SIGKILL
  interrupt an async flush."
- Do this switch cross-provider (not just cross-model on the same instance)
  if credentials allow — it's the stronger case for `ResolveResumeModelRef`
  and matches N3's "context window and effort ladder" framing, which only
  differs meaningfully across models/providers.
- If the hub's own resume path is broken independent of this feature (see
  `reconnect-auto-resume.md`'s sharp edges), this card can't isolate the
  model-switch assertion — confirm `reconnect-auto-resume.md` passes first
  if this one fails at step 4.
