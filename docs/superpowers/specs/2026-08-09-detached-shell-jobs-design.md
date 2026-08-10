# Detached shell jobs

## Status

Approved design for a deliberately disowned shell process that survives its launching Serf session. This document defines behavior, not an implementation plan.

## Problem

Serf background shell jobs are owned by their launching session. An agent may end a turn while a job continues, but `Session.Close` stops it. Agents sometimes need to launch a service and leave it running after the session ends without spelling out platform-specific `setsid nohup` command lines.

## Goals

- Add one explicit shell option for a process that survives launching-session teardown.
- Return the launched process ID for inspection outside Serf.
- Keep ordinary foreground and background execution unchanged.
- Keep the facility small: detachment, not durable service management.

## Non-goals

- Discovery or control from later Serf sessions.
- Durable job history after the launching session closes.
- Readiness, health checks, restart, or desired-state management.
- Terminal notifications after the launching session closes.
- Runtime adoption or reconciliation after Serf restarts.
- Survival across machine reboot, container teardown, or a host facility that kills the entire execution cgroup.
- Supporting processes that subsequently daemonize, double-fork, or move themselves into another process group.
- Preserving hidden or historical shell parameters as part of this new contract.

## Public API

The shell tool gains one optional boolean:

```json
{
  "command": "python3 app.py",
  "background": true,
  "detach": true
}
```

Rules:

- `detach` defaults to `false`.
- `detach: true` requires `background: true`.
- Supplying `detach: true` without `background: true` returns `invalid_request` before starting a process.
- `background` controls whether the tool call waits.
- `detach` means session teardown leaves the process running.
- An execution environment that cannot detach a process rejects the request before starting it.

The model-facing description is:

> `detach: true` leaves a background command running after this Serf session closes. Serf returns its PID, but later sessions do not discover or control it. Use only with `background: true`.

## Result

A successful detached launch returns:

```json
{
  "type": "shell",
  "status": "started",
  "running_in_background": true,
  "detached": true,
  "pid": 12345
}
```

The process is disowned before the tool returns. It never becomes a Serf job, receives no `job_id` or transcript ref, does not appear in `job_list`, emits no notification, and cannot be targeted by `job_status` or `job_stop`. The returned PID is the caller's only process reference; operating-system PID reuse rules apply. No Serf tool accepts it as a control handle.

## Launch behavior

Serf launches a detached command with:

- A process group or platform-equivalent execution unit separate from the Serf session's cleanup group.
- Standard input disconnected from the launching terminal.
- Standard output and standard error disconnected from the launching session. The command may perform its own explicit redirection when retained logs are required.
- A live PID returned only after process creation succeeds.

The detached process must never be enrolled in either cleanup path that stops session-owned jobs: the session job manager or the execution environment's tracked-process set. If launch fails, Serf stops any partial process tree and returns an error. The tool must never return `detached: true` for a process that any Serf cleanup path still owns.

The command remains a normal foreground process inside its detached process group. Serf does not support commands that escape that group after launch.

## Session lifecycle

- Ending an agent turn has no special effect; the detached command continues.
- Natural process exit produces no Serf notification or durable result.
- `Session.Close` has no reference to or effect on the detached process.
- One-shot `serf run` does not wait for a detached process after the launch has succeeded.
- No asynchronous callback is attempted after the shell call returns.

## Platform behavior

On supported Unix-like hosts, the command is started without terminal-owned standard streams and outside the session cleanup process group. Serf must not leave a pipe whose reader exits with the shell call or session, because subsequent process output could otherwise block or terminate the detached command.

On Windows, detachment requires a process-creation mode that lets the child survive Serf process exit without inheriting terminal/control handles. If the execution environment cannot provide that behavior, it rejects `detach: true` before launch.

The guarantee covers ordinary Serf session and client-process teardown. It cannot override container runtimes, service managers, or operating-system policies that kill the client's complete cgroup or execution unit.

## Testing

Before changing tests, implementation work must follow `docs/testing.md`.

Default tests use the scripted provider boundary and real Serf process plumbing below it. They must exercise behavior rather than matching rendered shell commands.

1. `detach: true` without `background: true` fails before process creation.
2. Unsupported execution environments fail before process creation.
3. Successful launch returns `detached: true` and a positive PID, with no `job_id` or transcript ref.
4. A detached process survives `Session.Close`.
5. The detached process is never enrolled in session or execution-environment cleanup tracking.
6. An ordinary background process is still stopped by `Session.Close`.
7. Launch failure leaves no process behind.
8. Detached execution creates no job record, notification, or session-owned output log.
9. No current or later session discovers or controls the detached process through job tools.
10. A detached command whose original Serf process exits continues without blocking on inherited pipes.

Any live-model scenario remains explicitly opt-in under the repository's live-test policy.
