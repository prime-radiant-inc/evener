# One-Shot Background Job Drain Design

Date: 2026-08-09
Status: Approved

## Purpose

The one-shot `serf` command must preserve Serf's job lifecycle contract. An
agent that ends a turn while session-owned work is running is idle, not done.
The command must keep the session alive, deliver terminal job notifications,
and run the resulting notification turns before it closes the session.

## Lifecycle Contract

Shell execution modes keep these meanings:

- `foreground` waits inside the current model turn until it finishes or Serf
  promotes it to a managed job after the foreground wait bound;
- `background` creates session-owned work that may continue while the agent is
  idle;
- `detached` creates disowned work that may outlive the session.

Every managed shell job participates in draining, regardless of whether it was
requested as background work or promoted from foreground execution. The
live-only `JobRecord.Background` field is not a lifecycle discriminator.
Detached processes are not tracked as jobs, do not produce session job
notifications, and never delay one-shot exit.

## One-Shot Drain

After the initial model turn ends successfully, the existing one-shot job-tree
drain must continue until the whole live session tree is quiescent.

For every session in that tree, quiescence requires:

- no running session-owned delegate or managed shell job;
- no queued terminal job notification;
- no durable terminal notification still awaiting delivery; and
- no existing delegate/watch work already covered by the drain.

When a managed shell job completes, fails, or is stopped, its normal
`<job-notification>` is delivered to its owning session. Delivery forces an
`EntryNotification` model turn. That turn may inspect the result, recover,
launch more work, or end its turn. The drain then evaluates the tree again.

Notifications that are already queued together may be delivered in one model
turn. A job becoming terminal must not be skipped merely because it is no
longer present in the running-job set; queued and durable pending notifications
are checked before declaring quiescence.

The caller's context remains the outer bound. If it is cancelled while owned
work is still running, draining returns the context error and ordinary session
shutdown stops the remaining owned work. A fatal model-turn error is not an
idle transition: it remains terminal for that run, skips further notification
turns, and shutdown stops its owned work. The original error is returned.

A deliberately stopped descendant is outside the live drain tree. Cascade-
stopped work in that stop-gated subtree does not force another turn or resurrect
the stopped session.

## Subagent Runs

The same contract applies when a subagent session launches managed work. A
subagent run that ends a model turn while its session tree still has drain work
becomes idle but does not become terminal. Its delegate job remains running,
and the subagent's own drain forces notification turns until that tree is
quiescent. The final post-notification result is the result delivered to the
parent.

A subagent session with owned drain work is live and non-reclaimable. It cannot
be marked consumed and evicted merely because an earlier model turn ended.
Explicit cancellation still stop-gates the subtree, cancels the drain, and
allows ordinary recursive shutdown.

## Implementation Boundary

The change belongs in the existing `Session.DrainJobTree` path. The one-shot CLI
uses it after a successful initial model turn. A subagent run uses the same
drain before it records its terminal result. The interactive root serve loop
already keeps sessions alive and drives notification turns, so its root-session
behavior does not change.

One shared owned-drain-job predicate must cover delegate and managed shell jobs
throughout the drain: running/outstanding accounting, live-component checks,
stall diagnostics, and durable-pending rematerialization. A genuinely running
shell is live work and cannot become stalled merely because it exceeds the
stall watchdog duration. Pending shell notifications must be rematerialized and
deduplicated through the same durable delivery path as delegate notifications.

The drain must reuse the existing job notification queue and
`EntryNotification` processing rather than adding a second completion channel
or polling shell output.

## Testing

Tests use a scripted provider at the LLM boundary and real Serf job/session
plumbing below it. They cover:

- a background shell completes after the agent goes idle, its notification
  forces another model turn, and the one-shot command returns that turn's final
  result;
- a foreground command promoted to a managed job follows the same path;
- a failed background shell likewise forces a notification turn;
- multiple terminal notifications queued before delivery are handled in one
  notification turn without loss;
- a notification turn that launches another managed job does not let the drain
  exit before the new job's own notification turn;
- a running shell remains live when a fake clock advances beyond the stall
  watchdog duration;
- durable-only shell notifications are rematerialized exactly once at the root
  and in a descendant, including the already-injected delivery case;
- background work launched in a subagent session keeps its delegate job
  nonterminal, resumes the subagent, and delivers the post-notification result
  to the parent;
- retention-cap pressure cannot reclaim a subagent that still owns drain work;
- cascade-stopped descendants are not re-driven;
- detached work does not hold the drain open; and
- cancelling the one-shot context returns the context error and terminates the
  managed process during session shutdown;
- a fatal model-turn error returns the original error, performs no additional
  notification turn, and stops owned work during session shutdown.

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
- change foreground execution or interactive root-session behavior.
