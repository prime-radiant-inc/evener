# Detached shell jobs

## Status

Approved design for a deliberately disowned shell process that survives its launching Evener session. This document defines behavior, not an implementation plan.

## Problem

Evener background shell jobs are owned by their launching session. An agent may end a turn while a job continues, but `Session.Close` stops it. Agents sometimes need to launch a service and leave it running after the session ends without spelling out platform-specific `setsid nohup` command lines.

## Goals

- Give shell execution one mutually exclusive mode for foreground, managed-background, or detached execution.
- Return the launched process ID for inspection outside Evener.
- Keep ordinary foreground and background execution unchanged.
- Keep the facility small: detachment, not durable service management.

## Non-goals

- Discovery or control from later Evener sessions.
- Durable job history after the launching session closes.
- Readiness, health checks, restart, or desired-state management.
- Terminal notifications after the launching session closes.
- Runtime adoption or reconciliation after Evener restarts.
- Survival across machine reboot, container teardown, or a host facility that kills the entire execution cgroup.
- Supporting processes that subsequently daemonize, double-fork, or move themselves into another process group.
- Preserving hidden or historical shell parameters as part of this new contract.

## Public API

The shell tool uses one optional execution mode:

```json
{
  "command": "python3 app.py",
  "mode": "detached"
}
```

Rules:

- `mode` is one of `foreground`, `background`, or `detached`.
- Omitted `mode` defaults to `foreground`.
- `foreground` waits inline and retains the existing automatic-promotion behavior for a long-running command.
- `background` immediately creates a session-owned managed job.
- `detached` immediately disowns the process and leaves it running after session teardown.
- An execution environment that cannot detach a process rejects the request before starting it.

The previous `background` boolean is removed. Backward compatibility for that request shape is explicitly out of scope.

The model-facing description is:

> `mode` chooses foreground execution, a session-owned background job, or a detached process. A detached process continues after this Evener session closes; Evener returns its PID, but current and later sessions do not discover or control it.

## Result

A successful detached launch returns:

```json
{
  "type": "shell",
  "mode": "detached",
  "status": "started",
  "pid": 12345
}
```

The process is disowned before the tool returns. It never becomes a Evener job, receives no `job_id` or transcript ref, does not appear in `job_list`, emits no notification, and cannot be targeted by `job_status` or `job_stop`. The returned PID is the caller's only process reference; operating-system PID reuse rules apply. No Evener tool accepts it as a control handle.

## Launch behavior

Evener launches a detached command with:

- A process group or platform-equivalent execution unit separate from the Evener session's cleanup group.
- Standard input disconnected from the launching terminal.
- Standard output and standard error disconnected from the launching session. The command may perform its own explicit redirection when retained logs are required.
- A live PID returned only after process creation succeeds.

The detached process must never be enrolled in either cleanup path that stops session-owned jobs: the session job manager or the execution environment's tracked-process set. If launch fails, Evener stops any partial process tree and returns an error. The tool must never return `mode: "detached"` for a process that any Evener cleanup path still owns.

The command remains a normal foreground process inside its detached process group. Evener does not support commands that escape that group after launch.

## Session lifecycle

- Ending an agent turn has no special effect; the detached command continues.
- Natural process exit produces no Evener notification or durable result.
- `Session.Close` has no reference to or effect on the detached process.
- One-shot `evener run` does not wait for a detached process after the launch has succeeded.
- No asynchronous callback is attempted after the shell call returns.

## Platform behavior

On supported Unix-like hosts, the command is started without terminal-owned standard streams and outside the session cleanup process group. Evener must not leave a pipe whose reader exits with the shell call or session, because subsequent process output could otherwise block or terminate the detached command.

On Windows, detachment requires a process-creation mode that lets the child survive Evener process exit without inheriting terminal/control handles. If the execution environment cannot provide that behavior, it rejects `mode: "detached"` before launch.

The guarantee covers ordinary Evener session and client-process teardown. It cannot override container runtimes, service managers, or operating-system policies that kill the client's complete cgroup or execution unit.

## Testing

Before changing tests, implementation work must follow `docs/testing.md`.

Default tests use the scripted provider boundary and real Evener process plumbing below it. They must exercise behavior rather than matching rendered shell commands.

1. Omitted `mode` selects foreground execution.
2. Invalid `mode` values fail before process creation.
3. Unsupported execution environments reject `mode: "detached"` before process creation.
4. Successful detached launch returns `mode: "detached"` and a positive PID, with no `job_id` or transcript ref.
5. A detached process survives `Session.Close`.
6. The detached process is never enrolled in session or execution-environment cleanup tracking.
7. A `mode: "background"` process is stopped by `Session.Close`.
8. Foreground automatic promotion still creates a session-owned managed job.
9. Detached launch failure leaves no process behind.
10. Detached execution creates no job record, notification, or session-owned output log.
11. No current or later session discovers or controls the detached process through job tools.
12. A detached command whose original Evener process exits continues without blocking on inherited pipes.

Any live-model scenario remains explicitly opt-in under the repository's live-test policy.
