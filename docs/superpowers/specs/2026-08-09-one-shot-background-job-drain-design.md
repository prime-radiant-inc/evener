# One-Shot Background Job Drain Design

Date: 2026-08-09
Status: Proposed

## Purpose

The one-shot `serf` command must preserve Serf's job lifecycle contract. An
agent that ends a turn while session-owned work is running is idle, not done.
The command must keep the session alive, deliver terminal job notifications,
and run the resulting notification turns before it closes the session.

## Lifecycle Contract

Shell execution modes keep these meanings:

- `foreground` waits inside the current model turn;
- `background` creates session-owned work that may continue while the agent is
  idle;
- `detached` creates disowned work that may outlive the session.

Only session-owned work participates in one-shot draining. Detached processes
are not tracked as jobs, do not produce session job notifications, and never
delay one-shot exit.

## One-Shot Drain

After the initial model turn ends, the existing one-shot job-tree drain must
continue until the whole live session tree is quiescent.

For every session in that tree, quiescence requires:

- no running session-owned delegate or background shell job;
- no queued terminal job notification;
- no durable terminal notification still awaiting delivery; and
- no existing delegate/watch work already covered by the drain.

When a background shell job completes, fails, or is stopped, its normal
`<job-notification>` is delivered to its owning session. Delivery forces an
`EntryNotification` model turn. That turn may inspect the result, recover,
launch more work, or end its turn. The drain then evaluates the tree again.

Notifications that are already queued together may be delivered in one model
turn. A job becoming terminal must not be skipped merely because it is no
longer present in the running-job set; queued and durable pending notifications
are checked before declaring quiescence.

The caller's context remains the outer bound. If it is cancelled while owned
work is still running, draining returns the context error and ordinary session
shutdown stops the remaining owned work.

## Implementation Boundary

The change belongs in the existing `Session.DrainJobTree` path used by the
one-shot CLI. The serve loop already keeps sessions alive and drives
notification turns, so its behavior does not change.

The drain's outstanding-work accounting must include background shell jobs at
the root and in live descendant sessions. It must reuse the existing job
notification queue and `EntryNotification` processing rather than adding a
second completion channel or polling shell output.

## Testing

Tests use a scripted provider at the LLM boundary and real Serf job/session
plumbing below it. They cover:

- a background shell completes after the agent goes idle, its notification
  forces another model turn, and the one-shot command returns that turn's final
  result;
- a failed background shell likewise forces a notification turn;
- multiple terminal notifications queued before delivery are handled in one
  notification turn without loss;
- background work launched in a descendant session keeps the tree alive and
  resumes its owning session;
- detached work does not hold the drain open; and
- context cancellation releases a drain waiting on background work.

The existing test that requires one-shot draining to ignore background shell
jobs is replaced by these behavioral contracts. Tests do not assert prompt or
warning wording.

## Scope Lock

This design does not:

- infer required task artifacts;
- create dependency graphs for shell commands;
- infer whether a process is a service;
- convert background work to detached work automatically;
- expose an outer runtime limit to the model;
- make detached processes visible to later sessions; or
- change foreground execution or interactive serve behavior.
