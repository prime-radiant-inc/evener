## Background jobs

Shell commands and delegates can run as durable background jobs identified by a
`job_id`. Jobs outlive your turn, and Serf notifies you automatically when a
background job finishes. Completion is notification-driven.
Your delegates handle their own children's completions; you are told when YOUR
delegates finish.

Pick the waiting primitive by how many answers you need:

- The result of a quick command now → plain `shell` (foreground). To launch long
  work without waiting → `shell` with `background: true` (returns a `job_id`
  immediately; you are notified when it finishes).
- One signal ("the server printed ready") → `job_read_output` with `max_wait_ms`
  and `grep`. One bounded wait, nothing to clean up afterward.
- A recurring condition (every new match, periodic progress, event frames to an
  observer) → `job_watch`.
- "Tell me when it finishes" → the terminal notification is automatic.

Blocking waits are bounded conveniences measured in seconds, not parking: a
timeout leaves the job running and you free. For long work, start the background
job, keep working, and act on the notification. A
terminal notification can land after you have already read the job's output
yourself; that is expected confirmation, not new work — act on whichever arrives
first and process each result once. When a notification needs no action, a
one-line acknowledgment is enough.

`job_list` is always current. If you have waited unusually long with no
notification, list jobs to re-orient before re-running anything.

Ordinary watches: create the watch on the thing you want to observe. For a
background job, use `job_watch(operation="create", source=<job_id>, ...)`.
Delivery is implicit: matching frames return to the session that created the
watch. Use `output_match` for concrete job output, `progress_interval_ms` for
periodic progress, and `events`/`event_filter` for event frames.

Observer sidecars: start the observer with `delegate(watch_parent:true)`. The
child observes your session with `job_watch(operation="create", source="parent",
...)` and reports findings with `communicate(end_turn:true)`. That communicate
message is the observer callback: when it arrives, continue from that steering.

For watch-driven tasks, complete this sequence:

1. Start the observer with `watch_parent:true`.
2. Wait for observer readiness.
3. Have the observer create its parent watch.
4. Trigger the watched action.
5. Finish from the observer's `communicate(end_turn:true)` callback.

The observer callback is completion evidence for the observer's task; after it
arrives, one final result message is enough unless the user asked for audit
details. For normal watched-condition work, the completion path is callback to
final result. Audit and diagnosis tools are for explicit audit requests or a
failed/missing callback. For `job_watch` create calls, keep the public shape
source-owned: `operation`, `source`, and optional trigger fields such as
`output_match`, `events`, `event_filter`, `every`, or `progress_interval_ms`.
For `source:"parent"` and other session-event watches, omit trigger fields to
receive bounded public frames, or add `events`/`event_filter` to narrow. Frames
coalesce while the observer is busy — it sees the latest state, not a backlog.
Use `events: ["communicate"]` for explicit result/status messages sent through
the result tool; `communicate` is the watchable result/status channel. Use
`events: ["assistant.tool"]` for tool events; the complete filtered tool-watch
shape adds `event_filter:{"tool_name":"read_file","status":"ok"}`. Assistant
tool frames include the matched `status` and original tool `arguments_json`;
use those frame fields as the first evidence before reaching for audit tools.

When a watch-origin observer sends its terminal `communicate(end_turn:true)`,
Serf records that observer job's terminal state without adding another owner
notification for the same job. The observer callback carrying the packet is the
actionable signal.

Observer setup is sequential when the trigger depends on the watch existing.
After the trigger, Serf yields the caller turn when the frame is handed to the
observer; continue from the observer callback when it arrives, then finish once.

`job_stop` cancels and preserves output/history. A finished delegate remains
resumable — `delegate_send(to=<delegate_id>, on_idle="start")` starts its next
turn in the same conversation as a new job.
