# End-Turn Guidance for Live Background Jobs

Fixes #297.

## Goal

When a session ends its turn with a background shell still running, tell the
agent what will actually happen to that job and what its two options are:
relaunch it detached, or stop it. Today the agent is told the opposite of the
truth and the process then deadlocks.

## The failure

Terminal-Bench trial `hf-model-inference__X64mGVa` asked for a Flask API served
on `0.0.0.0:5000`, run in the background. The agent did exactly that, correctly,
in 149 seconds (`work_millis: 149004`, all 26 trajectory steps inside that
window). The process then sat idle for **751 seconds** until the harness killed
it. The server died with the session; the trial scored 1 of 4 tests.

Two independent defects produced that.

**The drain never returns.** `cmd/evener/run.go:311-332` drains the job tree
before `Close()`. `isOwnedDrainJob` (`agent/session_jobtree_drain.go:36-44`)
counts any session-owned `jobstore.JobShell`, and `DrainJobTree`'s own doc states
the consequence (`:323-325`):

> The wait is bounded by ctx: a subtree whose managed work never completes blocks
> until the caller's context is cancelled. Individual managed-job turns carry
> their own round/time caps, so a well-formed tree always quiesces on its own.
> Every owned managed job holds the drain open.

A service that is *supposed* to keep running is not a well-formed tree and never
quiesces. `drainStallTimeout` cannot fire because a live process resets the stall
clock each pass, and the background-job kill path (`agent/jobs.go:668-753`) runs
*after* the drain, so it is unreachable.

**The warning reassures.** `runningJobsEndTurnWarning`
(`agent/session_tools_communicate.go:126-129`) emits:

> ending turn while N job(s) are still running: …. The call still succeeds; each
> job remains notification-armed and will report separately on completion.

In a one-shot run there is no "separately" — the turn is the process. The agent
read it and ended the turn:

> I expected `communicate(end_turn=true)` to end my active agent turn promptly
> while the background job continued running and remained notification-armed. I
> did not expect the Flask process itself to exit immediately at that point.

## Design

Guidance at `end_turn`, not an exemption in the drain.

`Background` is available exactly where it would be needed to exempt the job:
`outstandingDrainJobIDs` iterates `jm.running` and passes `run.rec`, the live
in-memory record, and `JobRecord.Background` (`agent/internal/jobstore/record.go:167`)
is stamped at launch. So exempting background shells from the drain is
*implementable*.

It is still the wrong fix. The drain's job is to stop `Close()` from SIGKILLing
work the coordinator still owes a notification turn (PRI-2441). Exempting a whole
class of job weakens that for every background shell — including one the agent
started incidentally and forgot, which is precisely the case the drain exists to
catch. The agent is the only party that knows whether a given process is meant to
outlive the session. Ask it.

### 1. Distinguish background jobs at end_turn

`deps.runningJobIDs()` currently returns every running job. Split it, or return
records rather than ids, so the communicate handler can tell a background shell
from a foreground one still finishing. Use the live `rec.Background` from the
running map; do not read it from a folded store record, where it always reads
false.

Only background shells get the new guidance. A foreground shell mid-completion is
ordinary bounded work and the existing warning is accurate for it.

### 2. Say what will actually happen, and name both remedies

Replace the reassurance with the two options, keyed to the job's fate:

- **relaunch detached** — `exec_command mode:"detached"` (`runDetachedShell`,
  `agent/session_tools_shell.go:367-382`) calls `DetachCommand` and returns a
  PID. It never enters `jm.running`, so it neither holds the drain open nor dies
  with the session. This is the correct answer for "run it in the background" in
  the sense a task means it — a service that outlives the agent.
- **stop it** — `job_stop`, when the process was scratch work rather than a
  deliverable.

The message must not promise a later notification the run cannot deliver.

### 3. Make the message true in both modes

The claim "will report separately on completion" is true under `serve` and false
one-shot, and the session cannot currently tell which it is. `NonInteractive`
(`agent/session_config.go:120`) is about whether a human is available to answer
questions, not about process lifetime, so it is not a usable proxy.

Plumb an explicit one-shot flag through `SessionConfig` into `toolDeps`, set by
`cmd/evener/run.go` and not by `serve.go`. Under serve, keep today's wording;
one-shot, state that the job will be terminated when the turn ends.

### 4. Backstop the drain regardless

Guidance changes behaviour in the common case; it cannot guarantee it. A session
that ends its turn with a live background job — because the model ignored the
hint, or because the job was started by a path that never surfaced one — must
still not hang until an external kill.

Bound the one-shot drain: once no *non-background* job is outstanding and only
background shells remain, stop draining and let `Close()`'s existing kill path
run. This is narrower than exempting background jobs from `isOwnedDrainJob`,
because a background job still holds the drain open for as long as any real work
is pending; it only stops being a reason to wait *forever* once it is the sole
remaining reason.

## Open question for Jesse

The current warn-only behaviour is a recorded decision — the comment above
`runningJobsEndTurnWarning` reads *"Warn-first (2026-08-06 ruling): the
communicate call still succeeds, there is no refusal path."*

This spec keeps that ruling: the hint stays a warning. Worth noting that a
warning was already present here and did not change the outcome — though the
existing text actively reassured, so an accurate one that names the remedy is a
materially different signal, and §4 removes the catastrophic consequence either
way.

If an accurate warning still proves insufficient in a follow-up run, refusing
`end_turn` while a session-owned background job is live would need a new ruling,
and would want a bound so a model that cannot satisfy it does not loop to
`max_rounds`.

## Validation

- A one-shot session that ends its turn with a live background shell returns
  from `run()` promptly rather than blocking until context cancellation. This is
  the regression test for the deadlock; it fails today.
- The `end_turn` result for a live background shell names both `mode:"detached"`
  and `job_stop`, and does not claim a later notification. Assert on the
  structured result, not on rendered prose.
- A foreground shell still finishing keeps today's wording and today's drain
  behaviour — byte-identical.
- Serve-mode wording is unchanged, so the notification contract in
  `docs/job-control.md` still describes what serve does.
- A background shell does not suppress the drain while other work is
  outstanding: with one background shell plus one delegate still owing a
  notification turn, the drain waits for the delegate.
- A shell launched `mode:"detached"` never enters `jm.running` and never
  contributes to the drain.

Acceptance is a rerun of `hf-model-inference` on an unpatched binary, scoring 4
of 4 with the server still serving when the verifier connects.

## Out of scope

`drainStallTimeout` not firing while a live process resets the stall clock is
correct for its purpose (a stalled tree, not a deliberately long-lived one) and
is left alone. Whether `mode:"background"` is the right default for a
service-shaped command, and whether the shell tool should detect
never-terminating commands, are separate questions.
