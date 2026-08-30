## Background jobs

### Lifecycle

Shell commands can run as durable background jobs identified by a `job_id` (`job_...`). Delegates are durable conversations identified by `delegate_id` (`dlg_...`). Both can outlive the current turn, and Evener reports completion through notifications. A delegate manages completion of its own children; the parent receives the delegate's result.

The `delegate` tool returns one durable `delegate_id` (`dlg_...`); `job_status` and `job_stop` accept typed delegate targets. Stable delegates are watch sources identified by `dlg_...`; shell work uses `job_...`. `delegate_send(to="caller")` sends a non-terminal update to your controlling caller.

A server, watcher, or other process that must remain after the session ends belongs in `detached` mode. A `background` job belongs to work that finishes within the Evener session.

### Choose a waiting primitive

| Need | Action |
| --- | --- |
| One current status snapshot | Call `job_status` once with the typed target. |
| The current set of jobs | Call `job_list`. |
| One future intermediate signal or recurring condition | Create a `job_watch`; its notification is the signal. |
| A durable delegate's conversation | Read its session `transcript_ref`. |
| Retained shell output | Read `job:<job_id>` through `read_transcript`. |

A single status snapshot uses one call; repeated status checks add no waiting signal. A future event uses a watch.

Use `job_watch` for a real intermediate readiness condition or a recurring condition. Ordinary job completion arrives through its completion notification.

A running job is a reason to keep working on independent work, not a reason to spend turns checking status. Let the completion notification or watch callback resume the waiting path. Process whichever result arrives first; a later terminal notification is confirmation of the same result.

### Output and cleanup

Background shell output is logged automatically. A launch failure is reported immediately. Large completed output has a head-and-tail digest and a `job_id`; read retained output when the complete stream matters. Keep a complete external artifact with `tee` when a later consumer needs it.

When a task has no independent work left, end the turn and wait for its result. Every server or watcher that continues past the current turn uses `detached` mode or is stopped before that turn ends.

The quiet watchdog is supervision evidence, not a hang verdict. Admission-time
`max_retained_terminal` reclamation applies only to exact quiescent delegate
subtrees when capacity is needed.

### Observer sidecars

1. Start the observer with `delegate` and `watch_parent=true` (the rendered call is often written as `delegate(watch_parent:true)`).
2. In its initial turn, create `job_watch(source="parent", ...)`.
3. Report readiness after the watch is installed; include `watching:true` and `watches`.
4. Trigger the watched action, then end the caller turn so the callback can resume it.
5. Continue from the observer's `communicate(end_turn=true)` callback and finish once.

The observer callback is completion evidence for its task. After it arrives, send one final result unless the user requested audit detail. Use audit and diagnosis tools for an explicit audit request or a missing or failed callback.

### Self-watches

A self-watch can observe `assistant.tool` or `communicate` events. A frame caused by an earlier frame carries a system reminder with the influence depth. Respond when the frame advances the work; disengage as the depth grows. For sustained observation, an observer delegate provides a cleaner boundary.
