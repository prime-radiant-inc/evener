# subagent-cancel-runaway: cancel_agent stops a runaway child mid-run yet keeps it resumable

**What this covers**: `cancel_agent` — the root-only child analog of Esc
(`agent/internal/tool/definitions.go` `DefCancelAgent`,
`agent/subagents.go` `cancelAgent`). A parent spawns a NON-blocking child
that starts a slow run (`sleep`), then `cancel_agent`s it while it is
RUNNING. The cancelled snapshot must report `status:"cancelled",
closed:false, success:false`, and — critically — the child must
still be **resumable**: a follow-up `resume_agent` starts a fresh run on
the preserved history and completes. This is the "abort the run, keep the
job" contract that distinguishes cancel_agent from close_agent (which
destroys the session).

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env (zsh — note `"$PWD/.env"`, a bare
  `. .env` fails):
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model. `openai/gpt-5.4-mini` is enough; the recipe below uses it.
- `--max-subagent-depth 1` so the root may spawn one level of children
  (pass it explicitly; do not rely on the default).
- State persistence on (default for the CLI).

## Steps

1. Hermetic project dir:
   ```bash
   proj=$(mktemp -d -t serf-e2e-cancel-XXXXX)
   ```

2. **One root run that spawns, cancels, then resumes.** The prompt is
   imperative and ordered so the root actually calls the tools in
   sequence. The child is told to `sleep 30` FIRST so the cancel lands
   while it is mid-run:
   ```bash
   /tmp/serf --model openai/gpt-5.4-mini --dir "$proj" --max-subagent-depth 1 \
     "Do these steps strictly in order and report what each tool returned.
      (1) Call spawn_agent with blocking=false and this task: 'Using the shell tool, run the command: sleep 30. Only after it finishes, call communicate with the message DONE_SLEEPING.' Capture the agent_id from the spawn result.
      (2) IMMEDIATELY call cancel_agent on that agent_id (do not wait, do not call list_agents first — the child must still be sleeping). Report the full JSON the cancel returned, especially its status and success fields.
      (3) Now call resume_agent on the SAME agent_id with blocking=true and the message: 'Forget the sleep. Using the shell tool, run: echo RESUMED_OK. Then communicate the message RESUMED_OK.' Report the resumed run's status, success, and output.
      Finally, summarize: did cancel_agent return status cancelled, and did the resume then complete?"
   ```
   Wait for exit 0.

## Expected

- After step 2, the **cancel** tool result (the `cancel_agent` line / its
  JSON) is the cancelled snapshot:
  ```json
  {"agent_id":"<id>","status":"cancelled","closed":false,"output":"context canceled","success":false,"turns_used":1,"transcript_ref":"local:<id>"}
  ```
  `status` is `cancelled`, `closed` is `false` (the child was cancelled,
  not closed), `success` is `false` (`output` is the aborting error,
  observed as `context canceled`).
- The **resume** then completes: the `resume_agent` (blocking) result
  reports `status:"completed"`, `success:true`, and `output` reflecting
  `RESUMED_OK` — proving the child survived the cancel and resumed on its
  preserved history.
- Falsification:
  - The cancel returns `status:"completed"` or `status:"failed"` despite
    the child being told to sleep 30 → the cancel did not land on a
    running child (a race — the model dawdled before calling cancel, or
    the child finished early). Lengthen the sleep or re-run; a real
    regression is cancel returning success:true.
  - `resume_agent` errors with "unknown agent_id" / "not resumable" /
    "already completed and results already consumed" → cancel destroyed
    or orphaned the child instead of keeping it resumable. That is the
    core regression this card guards.
  - cancel_agent itself errors with "agent ... is not running" → the
    child completed before the cancel arrived (race, same remedy as
    above), OR the running-state bookkeeping regressed.

## Cleanup

- Leave `$proj` and the temp dir on disk (do NOT `rm -rf`; it is blocked
  in this environment). Each run uses a fresh `mktemp -d`, so reruns are
  already hermetic. The parent + child transcripts persist under
  `~/.local/state/serf/projects/<bucket>/sessions/`.

## Sharp edges

- **Catching the child RUNNING is the whole game.** `cancel_agent`
  early-returns "agent ... is not running" unless the child is mid-run.
  `sleep 30` gives a wide window; the prompt forbids any
  list_agents/wait between spawn and cancel so the model cannot
  accidentally block past the sleep. If the model still races, bump the
  sleep to 60 or switch to `openai/gpt-5.5` (steadier sequencing).
- The model surfaces the shell tool as `[tool] shell` in CLI output, not
  `exec_command`; the prompt says "the shell tool" so it is not coupled
  to a tool name.
- `cancel_agent` waits up to 5s for the run to actually stop after
  signalling cancel; the returned snapshot is taken after the run
  unwinds, so `status` is already `cancelled` (not `running`) in the
  result you read.
- cancel CONSUMES the cancelled result (`resultConsumed=true`), but that
  does not block `resume_agent` — resume starts a brand-new run on the
  idle, preserved child. (A bare `wait` on the cancelled result would
  error "already consumed"; resume is the correct next move, which is
  exactly what this card exercises.)
- Use `blocking=false` on the spawn so control returns to the root
  immediately with the child still running — a `blocking=true` spawn
  would itself wait out the `sleep`, leaving nothing to cancel.
