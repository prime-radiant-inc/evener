# Daemon idle retirement

A daemon that the Hub launches retires itself after an hour of *proven*
inactivity, so long-running idle sessions stop holding a process, a listener and
a runtime tree. Retirement is non-terminal: the saved root stays resumable, and
the next message to the same session starts a replacement that restores the same
identity.

The mechanism is daemon-owned. The daemon runs its own idle timer; the Hub
observes and recovers, and never signals a process to retire it. Detached
external processes (shell jobs, delegates' worktrees, retained scratch) keep
their existing independent lifetime — retirement does not sweep them away.

## Configuration

The Hub's timeout lives in `hub.toml`:

```toml
# hub.toml; affects subsequently spawned or resumed daemons only.
daemon_idle_timeout = "1h"
# Set "0s" to disable automatic retirement, not to select the default.
```

A directly-run `evener serve` defaults to *disabled* and stays resident:

```sh
# Standalone remains resident by default.
evener serve --daemon-idle-timeout=0s
# Explicit standalone opt-in.
evener serve --daemon-idle-timeout=30m
```

Both surfaces accept a Go duration string. Negative or malformed values are
rejected before the daemon opens its listener.

Settings → Hub shows both the Hub default and each daemon's reported *effective*
timeout, which is what the daemon actually armed. A Hub TOML change affects
subsequently spawned or resumed daemons; it does not retune a daemon that is
already running. `0s` means "no timer", never "use the default": an omitted
field and an explicit `0s` stay distinct.

The snippets above illustrate the lifecycle flags only. In a reader's
environment they supplement the normal `evener serve` invocation (which also
needs model/provider configuration); they are not claimed to be credential-free
runtime tests.

## What counts as inactivity

Retirement requires an hour of *continuous proven* inactivity — eligibility is
measured from the most recent settled instant, and any admitted work restarts
the full interval rather than consuming the remainder.

Work that resets the interval:

- turn execution, including provider and tool rounds, compaction, cancellation
  and settlement still in progress;
- accepted input: durable starts, pending executions, steering, queue/drain
  operations, reserved mutations;
- autonomous work: goals and continuations, notifications, naming and
  maintenance tasks, delivery/attention retries;
- human decisions: outstanding questions and sandbox escalations;
- shell jobs, watches, delegate work and environment work (worktree and sandbox
  operations) that are not yet settled;
- persistence that is unsaved, unflushable or unreadable.

Passive viewing is ignored. Discovery, status, transcript/history reads, list
calls, subscriptions and polling a mounted Settings → Hub section never extend
residency. A long-lived subscription receives closure when retirement commits;
it does not pin the daemon.

Questions and watches **can** pin the daemon indefinitely. An unanswered
`ask_user` question is a human-decision blocker, and an active watch is a watch
blocker; neither is a timeout that expires on its own. That is deliberate: the
session has work a person asked for, so it is not idle.

Idle delegates, worktrees and scratch are preserved. An idle, resumable delegate
does not block retirement once its whole subtree is settled and its descriptor,
transcript, environment and lane ownership can be preserved; the delegate's
worktree and scratch are pinned so a later resume finds them. A missing runtime
pointer is never treated as proof of eligibility — uncertain state blocks.

## Recovery

The next message to a retired session resumes the same saved root. The retry
uses the same client mutation ID, so the mutation produces exactly one accepted
turn with the same stable turn ID, whether the caller reached the old daemon,
the retirement wait, or the replacement. Concurrent senders produce one daemon
and one accepted turn.

Reads, probes, shutdown and stale background retries never resurrect a daemon.

## Retired versus unavailable versus incompatible

- **Retired** — the daemon exited after a clean, committed retirement. Its
  rendezvous record is gone; the saved root is intact and resumes on demand.
- **Unavailable** — a live daemon refused a mutation because it is retiring (or
  its exit could not yet be confirmed). The caller gets a typed,
  lifecycle-unavailable result; retrying the same request after the confirmed
  exit resumes the replacement.
- **Incompatible** — a daemon whose reported protocol does not match this Hub's.
  It is visible in the resident inventory but has unknown eligibility and no
  safe-retirement capability. It is never presumed idle.

## Settings → Hub: the resident inventory

The resident section lists one row per *discovered process identity* — not per
delegate or session alias, and not a machine-wide process scan. Discovery is the
configured rendezvous scope: a process this Hub cannot see is not a row, and the
list must not be read as a complete inventory of OS processes. Archived and
incompatible rows remain visible with explicitly unknown status and
`CanRetire:false`.

Rows show the root reference/name, PID, start time, reported protocol and
compatibility, archive status, effective timeout, retirement phase/deadline and
blocker categories. Tokens and raw process environments are never exposed, and
there is no arbitrary-PID targeting: an action addresses a rendered identity and
rechecks it, so a changed or reused PID refuses the stale target instead of
stopping a replacement.

Controls:

- **Retire now** skips the elapsed-time requirement but keeps every safety
  check, and works even when automatic retirement is disabled (`0s`). It is safe
  at zero. If the daemon refuses, the UI shows the *fresh* blockers from that
  attempt and the daemon keeps running.
- **Force stop** is the explicit destructive operation. It uses the existing
  verified-identity path, requires a confirmation naming the session and
  process and warning that work and watches may be interrupted, and preserves
  the explicit **Resume** requirement — a force-stopped session does not resume
  automatically; the next action must be an explicit resume.
- **Refresh/retry** follows the existing UI patterns. Polling runs only while
  the section is mounted, reuses discovery snapshots, and never counts as
  activity.

## Failures an operator should report

A retirement that fails before committing leaves the daemon resident and records
one of these on its lifecycle diagnostics:

- `prepare_failed` — the daemon could not validate and preserve its runtime tree
  (unreadable durable evidence, a failed required flush, a delegate whose
  reconstruction could not be proved). Nothing was released; the daemon keeps
  serving.
- `release_failed` — preparation and commit succeeded but the non-terminal
  release hit an error. Admission never reopens and the process is never killed
  to force the issue: the daemon stays retiring, and a later resume must not be
  attempted until its exit is confirmed.
- `reader_drain_failed` — in-flight readers did not leave within the bounded
  drain. As above, admission stays closed and the process stays retiring.

An empty failure field with a resident phase is the healthy case.

## Rollout and compatibility

- Already-running daemons are unchanged. The setting applies to daemons spawned
  or resumed after it changed; an existing process keeps the timeout it armed.
- There is no legacy upgrade and no automatic kill. An old binary that does not
  report the current protocol stays visible but cannot be safely retired: an
  operator must explicitly stop the verified process and then explicitly resume
  it under the current binary. Age, parent PID and apparent idleness are never
  grounds for stopping a process.
- Provider stream idle timeout and archive retention are separate features with
  their own configuration and lifetimes. Retirement does not change either, and
  it never deletes an archived session.

## A note on the browser guard gate in this environment

`make test-web-browser` needs a short `TMPDIR` here, for example:

```sh
TMPDIR=/tmp/eb make test-web-browser
```

Chrome's process-singleton Unix socket otherwise exceeds `sun_path` under the
default sandbox root. This is environmental — it applies to all six browser
guards, including `retirementguard` — and is not a test result. A run that fails
before Chrome starts is a prerequisite failure, not a retirement failure.
