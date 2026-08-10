# Detached shell jobs

## Status

Approved design for a shell job that remains supervised after its launching Serf session exits. This document defines behavior, not an implementation plan.

## Problem

Serf background shell jobs are owned by their launching session. The agent may end a turn while a job continues, but `Session.Close` stops the job. A one-shot `serf run` deliberately excludes shell jobs from its delegate drain and then closes the session, so a background service cannot survive the invocation without escaping Serf through shell-specific techniques such as `setsid nohup`.

Agents need a general way to launch a background command whose lifetime is independent of the launching session while retaining truthful Serf supervision, logs, status, and stop control.

## Goals

- Allow an explicitly detached background command to survive the launching agent turn, session, and one-shot CLI process.
- Keep `job_id` as the stable public identity and reuse existing job-control tools.
- Return the live process ID for diagnostics without using it for recovery or control.
- Preserve current behavior for foreground and ordinary background shell commands.
- Ensure failed launch or custody transfer leaves no process behind.

## Non-goals

- Readiness or health checks.
- Automatic restart.
- Desired-state service management.
- Survival across machine reboot or container teardown.
- Adoption or control of arbitrary existing processes.
- Inferring lifetime from command text, elapsed time, or shell syntax.
- Supporting commands that daemonize, double-fork, call `setsid`, or otherwise escape the supervised process tree.
- Adding service-specific status, log, or stop tools.

## Public API

The shell tool gains one optional boolean:

```json
{
  "command": "python3 app.py",
  "background": true,
  "detach": true
}
```

`background` and `detach` describe independent concerns:

- `background` controls whether the tool call waits for command completion.
- `detach` transfers command lifetime out of the launching session while retaining Serf supervision.

Rules:

- `detach` defaults to `false`.
- `detach: true` requires `background: true`.
- Supplying `detach: true` without `background: true` returns `invalid_request` before starting a process.
- Foreground commands and existing `background: true` calls retain their current behavior.
- Execution environments that cannot provide detached custody reject the request before starting a process.

The model-facing description is:

> `detach: true` transfers a background command out of the launching session's lifetime. The command remains supervised and accessible through its `job_id` after the session closes. Use only with `background: true`.

## Result and status

A successful detached launch returns the ordinary shell result with two additional fields:

```json
{
  "job_id": "job_123",
  "type": "shell",
  "status": "running",
  "running_in_background": true,
  "detached": true,
  "pid": 12345,
  "transcript_ref": "job:job_123"
}
```

`job_status` and `job_list` expose `detached`. They expose `pid` only while a live supervisor has a verified handle to the current process. Terminal records may retain a launch PID as diagnostic history only if it is clearly distinguished from a live PID; omitting it is the preferred initial behavior.

`job_id` remains the only public control identity. No tool accepts PID as a target. PID is never used for authorization, recovery, reconciliation, or later signaling.

Existing tools remain the complete control surface:

- `job_status(job_id)` reads lifecycle and diagnostics.
- `job_list(...)` discovers detached jobs visible in the current Serf state boundary.
- `read_transcript("job:<job_id>")` reads retained output.
- `job_stop(job_id)` stops the supervised process tree.

## Runtime architecture

Each detached job is owned by a small Serf runner outside the launching session and one-shot CLI lifetime:

```text
launching session --> detached runner --> command process group
       exits               |                    |
                           |                    +-- descendants
                           +-- output log
                           +-- terminal status
                           +-- stop control
```

The detached runner is a Serf component, not user shell text. It is responsible for:

- Establishing a new supervised process group or the platform-equivalent containment unit.
- Starting the requested command inside that unit before user code can fork descendants.
- Draining stdout and stderr continuously into bounded Serf job output.
- Retaining the live process handle needed for safe wait and termination.
- Recording natural exit status and output completion.
- Receiving authenticated stop requests keyed by `job_id`.
- Terminating the complete supervised process tree on an explicit stop.
- Exiting after the command tree and its output streams terminate.

The runner must inherit no terminal input or client-owned output/control descriptors. Output goes only to runner-owned job storage. A user command must remain in the foreground of its supervised unit. Serf documents self-daemonization and process-group escape as unsupported rather than attempting unreliable descendant adoption.

The implementation may use a per-job runner or a shared supervisor, but the public contract and custody invariants are identical. The implementation plan must choose the smallest mechanism that works with the existing state directory and platform execution abstractions.

## Custody transfer

Detached launch is transactional:

1. Validate arguments, execution-environment capability, working directory, and sandbox policy.
2. Allocate `job_id` and create the runner's durable launch record.
3. Start the runner and command containment unit.
4. The runner durably acknowledges that it owns the live process handle, output streams, and stop endpoint.
5. The launching session removes the process from its own cleanup ownership.
6. The shell tool returns success.

If any step before durable acknowledgement fails, the launching process stops the created process tree, removes incomplete custody state, and returns an error. A shell result must never claim `detached: true` while session cleanup can still kill the job.

## Lifecycle

- An agent may end its turn immediately after a detached launch. The detached job continues without holding the session active.
- `Session.Close` and execution-environment cleanup do not signal a successfully transferred detached job.
- Natural command exit records the normal completed or failed status and exit code.
- `job_stop(job_id)` requests runner-owned process-tree termination and remains idempotent under the existing stop contract.
- If the originating session is live when the job terminates, it receives the existing terminal notification.
- If the originating session is gone, terminal state and output remain durable for later inspection.
- Detached jobs do not participate in the one-shot delegate drain and do not keep `serf run` alive after custody acknowledgement.

## Visibility and authorization

Detached jobs use the existing Serf state-directory and operating-system user boundary. This design does not introduce project-global or user-global service ownership.

Because the launching session may no longer exist, detached job lookup and control cannot depend on its live `jobManager`. The durable record and runner endpoint must be resolvable through the enclosing state directory. Existing session-owned job visibility remains unchanged; only detached records gain post-session resolution.

The stop endpoint must authenticate the local caller and bind requests to the exact job record. An unguessable `job_id` is not sufficient authorization by itself.

## Reconciliation and failure behavior

- A runner that loses its command records the observed terminal state when possible.
- A command that outlives a crashed runner is not adopted by persisted PID.
- A Serf process that finds a detached job without an authenticated live runner marks it `runtime_lost`.
- Reconciliation never signals a PID loaded from disk because PID and process-group identifiers can be reused.
- A runner or control-channel failure must not cause an unrelated process to be signaled.
- Log retention remains bounded. The implementation must drain process output independently of model or UI readers so a slow reader cannot backpressure the command.
- Detached jobs lease any Serf-managed temporary directory or worktree required for their execution. Disposal either refuses while the job is live or explicitly stops the job first under the existing destructive-operation contract.

## Platform contract

On Unix-like systems, the runner owns a separate process group and stops the complete group. The runner, not a persisted numeric PID, retains the live process handle.

On Windows, detached jobs require an equivalent process-tree containment primitive such as a Job Object. Killing only the direct shell child is not equivalent and must not be advertised as support.

If an execution environment or platform cannot uphold custody, output draining, and process-tree stop semantics, `detach: true` fails before launch. Ordinary shell execution remains available.

## Testing

Tests must exercise real lifecycle behavior rather than matching rendered shell commands.

### Deterministic contract tests

1. `detach: true` without `background: true` fails before process creation.
2. An execution environment without detached custody support fails before process creation.
3. Custody-transfer failure terminates the entire partial process tree and leaves no running job.
4. Successful transfer removes the command from session cleanup ownership.
5. `Session.Close` stops an ordinary background job but not a detached job.
6. Detached job metadata remains resolvable after the launching session is closed.
7. Natural exit records terminal status, exit code, and complete retained output.
8. `job_stop(job_id)` stops the process tree and is idempotent.
9. No public control path accepts PID.
10. Reconciliation never trusts a persisted PID and reports `runtime_lost` when runner custody is absent.
11. Existing foreground and ordinary background shell behavior is unchanged.

### Process integration tests

1. A detached command survives the launching `serf run` process exiting.
2. Its stdout and stderr remain readable through `job:<job_id>`.
3. A command with a child process is fully terminated by `job_stop`.
4. A noisy command cannot block because no UI or transcript reader is attached.
5. A running detached job prevents removal of a leased worktree or working directory.

Live provider behavior is not part of default tests. Any model-driven end-to-end scenario must remain explicitly opt-in under the repository's live-test policy.

## Deferred extensions

The following require separate designs if future use demonstrates a need:

- Output-, TCP-, HTTP-, or command-based readiness checks.
- Automatic or manual restart with multiple process generations.
- Health monitoring and desired-state reconciliation.
- Machine-reboot survival.
- Cross-user or cross-project sharing.
- Native `systemd`, `launchd`, or Windows Service Manager integration.

