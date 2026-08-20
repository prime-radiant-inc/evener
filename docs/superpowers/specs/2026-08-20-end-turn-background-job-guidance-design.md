# Refuse End-Turn With an Undisposed Background Job

Fixes #297.

## Goal

A one-shot session that ends its turn with a live background shell must be told
so and given another turn to deal with it, instead of accepting the end-turn and
then hanging until something external kills the process.

## The failure

Terminal-Bench trial `hf-model-inference__X64mGVa` asked for a Flask API served
on `0.0.0.0:5000`, run in the background. The agent did exactly that, correctly,
in 149 seconds (`work_millis: 149004`; all 26 trajectory steps inside that
window). The process then sat idle **751 seconds** until the harness killed it.
The server died with the session and the trial scored 1 of 4 tests.

Two defects compound.

**The drain never returns.** `cmd/evener/run.go:311-332` drains the job tree
before `Close()`. `isOwnedDrainJob` (`agent/session_jobtree_drain.go:36-44`)
counts any session-owned `jobstore.JobShell`, and `DrainJobTree`'s own doc states
the consequence (`:323-325`): *"a subtree whose managed work never completes
blocks until the caller's context is cancelled … Every owned managed job holds
the drain open."* A service that is supposed to keep running never quiesces.
`drainStallTimeout` cannot fire because a live process resets the stall clock
each pass, and the kill path (`agent/jobs.go:668-753`) runs after the drain, so
it is unreachable.

**The warning reassures.** `runningJobsEndTurnWarning`
(`agent/session_tools_communicate.go:126-129`) promises *"each job remains
notification-armed and will report separately on completion."* One-shot has no
"separately" — the turn is the process. The agent read it and ended the turn:

> I expected `communicate(end_turn=true)` to end my active agent turn promptly
> while the background job continued running and remained notification-armed. I
> did not expect the Flask process itself to exit immediately at that point.

## A first attempt, and what it proved

Refusing at `end_turn` was built and reverted. It is too broad, and four existing
tests say so: `TestRunDrainsManagedShellBeforeExit`,
`TestRunDrainsManagedShellWhenSystemPromptNamesTheJobFrame`,
`TestRunDrainCountsOnlyDeliveredJobFramesWhenSystemPromptNamesOne`, and
`TestRunDrainContinuesWhenNotificationTurnStartsAnotherShell`.

All four encode the pattern the drain exists to support: launch a background
shell, end the turn, and let the drain collect its completion and deliver a
notification turn. A `printf shell-ok` job finishes in milliseconds and waiting
for it is correct. Blanket-refusing `end_turn` whenever a background job is live
breaks that outright.

The distinction that matters is not *background versus foreground*. It is
**finishes versus never finishes**, and nothing at `end_turn` can tell them
apart — the job may have been launched microseconds earlier.

So the mechanism cannot live at `end_turn`. It has to fire once the job has
already had its chance to complete and did not: in the drain, at the point where
today it would block forever. That is also the more literal reading of the
instruction this spec is built on — *force a new turn with the notification
rather than just timing out* — because the timing out happens in the drain.

## Design

### Force a notification turn from the drain, not a refusal at end-turn

The refusal text and the two remedies below are still right; only their trigger
point moves. The drain, on finding that its sole remaining outstanding work is a
background shell that is not completing, delivers a notification turn saying the
session cannot exit with an undisposed background job, and lets the model
dispose of it. Draining then resumes normally.

Placement note: this lands inside `drainJobTreeWith`, which carries an explicit
cross-signal concurrency invariant and a documented flake history (the "PRI-2441
B1 flake" comment at `agent/session_jobtree_drain.go:351-374`). It wants its own
change, its own tests, and its own review rather than being folded into a
same-day patch.

### The refusal content (unchanged)

`communicate` already refuses by returning an error — `return nil,
errors.New("communicate requires end_turn")` at `session_tools_communicate.go:41`.
A handler error becomes an `IsError` tool result (`agent/session_tools.go:575-583`)
that goes back to the model, and nothing in the loop terminates on it. So a
refusal *is* the "force a new turn with the notification" mechanism; no new
machinery is required.

When a one-shot session calls `end_turn: true` with a live session-owned
background shell, refuse with a message naming the job and both remedies:

- **relaunch detached** — `exec_command mode:"detached"` (`runDetachedShell`,
  `session_tools_shell.go:367-382`) calls `DetachCommand`, returns a PID, and
  never enters `jm.running`, so it neither holds the drain open nor dies with the
  session. This is what a task means by "run it in the background".
- **stop it** — `job_stop`, when the process was scratch rather than a
  deliverable.

Refuse *before* any side effect — before the `EventCommunicate` emit and before
`drainSteering()`, which consumes the steering queue. A refused turn must not
swallow steering messages.

### One-shot only

Under `serve` the session outlives the turn, background jobs genuinely do report
later, and the existing notification contract in `docs/job-control.md` is
correct. Refusing there would break a working design.

The session cannot currently tell the two apart. `NonInteractive`
(`agent/session_config.go:120`) is about whether a human is available to answer
questions, not about process lifetime, so it is not a usable proxy. Add an
explicit flag to `SessionConfig`, set by `cmd/evener/run.go` and not by
`serve.go`, and plumb it to `toolDeps`.

### Detect only background shells, from the live record

`jm.runningJobIDs()` (`agent/jobs.go:1442`) returns every durably-started running
job. Add a sibling that filters on `run.rec.Background`
(`agent/internal/jobstore/record.go:167`). Read it from the running map's live
record — the field is `json:"-"` and always reads false when folded from the
store, which is what `isOwnedDrainJob`'s "does not reliably preserve execution
mode" comment refers to.

A foreground shell still finishing is ordinary bounded work and must keep today's
behaviour.

### Backstop the drain anyway

A refusal changes behaviour in the common case; it cannot guarantee it. A session
can still reach `Close()` with a live background job — a job started by a path
that never surfaced a refusal, or a model that exhausts `max_rounds` still
refusing to dispose of it. That must not hang until an external kill.

Bound the one-shot drain: when the only outstanding drain jobs are background
shells, stop draining and let `Close()`'s existing kill path run. This is
narrower than exempting background jobs from `isOwnedDrainJob` — a background job
still holds the drain open while any other work is pending; it merely stops being
a reason to wait *forever* once it is the only reason left.

## Supersedes the 2026-08-06 warn-first ruling

`runningJobsEndTurnWarning`'s comment records *"Warn-first (2026-08-06 ruling):
the communicate call still succeeds, there is no refusal path."* This spec
introduces exactly that refusal path, on Jesse's 2026-08-20 instruction: *"it
should force a new turn with the notification rather than just timing out. 'You
cannot exit with an undisposed background job.'"*

The narrowing that makes this consistent rather than a reversal: the refusal
applies only one-shot, only to background shells, and only where the previous
warning was factually wrong. Warn-first still governs every other case,
including foreground jobs and all of serve.

## Risk

A model that cannot dispose of the job will retry until `max_rounds`, producing
no final message where previously it produced one (and then hung). Three things
bound that: the refusal names both remedies and the job id so the action is
available; `max_rounds` already caps the loop; and the §4 backstop means the
process exits cleanly rather than hanging even in that case. Worth watching in
the adversarial test rather than assuming.

## Validation

- One-shot `end_turn` with a live background shell returns an error naming the
  job id, `mode:"detached"`, and `job_stop` — and does **not** end the turn.
- The same call in serve mode succeeds and keeps today's warning.
- One-shot `end_turn` with only a *foreground* shell running keeps today's
  behaviour byte-identical.
- A refused end-turn does not consume the steering queue.
- After the agent relaunches detached and calls `end_turn` again, it succeeds.
- A one-shot session that reaches `Close()` with a live background job returns
  promptly instead of blocking until context cancellation — the deadlock
  regression test, which fails today.
- A background shell does not suppress the drain while other work is
  outstanding: with one background shell plus a delegate still owing a
  notification turn, the drain waits for the delegate.

Acceptance is a rerun of `hf-model-inference` on an unpatched binary scoring 4 of
4 with the server still serving when the verifier connects, plus an adversarial
review of the built change.

## Out of scope

`drainStallTimeout` not firing while a live process resets the stall clock is
correct for its purpose (a stalled tree, not a deliberately long-lived one).
Whether `mode:"background"` is the right default for a service-shaped command,
and whether the shell tool should recognise never-terminating commands, are
separate questions.
