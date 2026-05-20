# meta-flush-on-completion: meta.json turn_count tracks committed exchanges

**What this covers**: katas `3tgv` (commit `fbdaffc`), `ztne`
(`6fecff3`), `wnfz` (`03de4c5`). Before these fixes, `meta.json`'s
`turn_count` would drift behind reality when the agent loop bailed
on an error / panic / ctx-cancellation / cfg setter without
flushing. The fixes layered: explicit flush on the recoverable LLM
error (`3tgv`), a deferred flush + panic recovery at the top of
`processOneInput` (`ztne`), and explicit flush on `SetModel`/
`SetReasoningEffort`/`SetTimeout` (`wnfz`).

## Pre-state

- Hub running.
- Path to write a transcript: `~/.local/state/serf/projects/`.

## Steps

### Happy path (3tgv baseline)

1. Spawn a session with a single completable prompt. e.g.:
   ```bash
   curl -s -X POST -H "Content-Type: application/json" \
        -H "Authorization: Bearer $(cat ~/.serf/auth-token)" \
        -d '{"prompt":"Reply with literal OK.","model":"anthropic/claude-haiku-4-5-20251001","working_dir":"/tmp","harness":"serf","branch":"","access_mode":"full","agent":"default","launch_overrides":{}}' \
        http://localhost:9180/api/spawn
   ```
2. Wait for idle (~10s).
3. Read `meta.json`:
   ```bash
   find ~/.local/state/serf/projects -name "<session_id>.meta.json" \
     -exec cat {} \; | python3 -c "import sys, json; \
     d=json.load(sys.stdin); print('turn_count:', d['turn_count'])"
   ```

### Error-path coverage (ztne)

The ztne `defer s.maybeAutoSave()` at the top of `processOneInput`
covers 5 exit paths: empty-response-exhausted, bare-text-exhausted,
MaxToolRoundsPerInput, ctx.Done, panic. Most of these are awkward to
exercise from outside (require specific model misbehavior). The
unit tests in `agent/session_test.go::TestSession_*FlushesMeta`
verify each exit individually with mocked LLMs. For a manual repro:

1. Spawn a session as above. Wait for idle.
2. `pkill -f 'serf serve.*<session_id>'` — kill mid-life.
3. Send another turn from the workspace UI.
4. While the agent is processing (status=active), send a SECOND
   message immediately. The hub's send-while-processing returns a
   `Conflict` error; the agent loop is unaffected. (This is NOT the
   ztne path — included as a control to confirm normal flush still
   works during interaction.)
5. Verify meta.json shows turn_count >= 2 after the second turn
   completes.

To trigger an actual ztne path (e.g. ctx-cancellation), kill the
daemon WHILE it's in a tool round — meta.json should still reflect
all completed user inputs.

### Cfg-setter coverage (wnfz)

1. Open the session in the workspace UI.
2. Change the model via the model picker chip.
3. Read `meta.json`: `model` field should reflect the new value
   IMMEDIATELY, before any further turn boundary.
4. Falsification: `meta.json` still shows the old model.

## Expected

- After step 3 (happy path): `turn_count: 1` (or whatever number of
  exchanges committed). Specifically, every USER_INPUT + successful
  assistant turn counts as 1.
- After step 5: `turn_count` reflects the second turn.
- After cfg change: `model` field matches the new model.
- Falsification: `turn_count: 0` after a clearly-completed turn, or
  the cfg field drifts behind a setter call. The persistence path
  regressed.

## Cleanup

- Sessions accumulate; not strictly necessary.

## Sharp edges

- `maybeAutoSave` re-acquires `s.mu` via `Meta()` → some setters
  intentionally drop their lock before calling it (see `wnfz`
  commit). If a future setter adds its own flush without unlocking
  first, deadlock.
- The deferred `recover()` in `processOneInput` re-panics after
  flushing. Tests must wrap in their own `recover()` or they'll
  fail with the original panic.
- `meta.json` is written atomically (`SaveSessionMeta` does
  `WriteFile` + `Rename`). Mid-write reads see the previous file —
  no half-flushed state. If you see a half-flushed JSON, file a
  kata.
