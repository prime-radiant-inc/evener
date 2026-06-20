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

Observer sidecars: start a delegate as the observer, then
`job_watch(operation="create", target=<job>, ..., send={to: <observer delegate_id>})`. Each trigger
pushes the observer a bounded frame; the observer can read the watched job
directly with `job_read_output` and report to you with
`delegate_send(to="caller")`. That caller message is the observer callback:
when it arrives, continue from that steering. The happy path is create the
observer, create the watch, trigger the watched condition, and let the callback
drive the next step. The observer callback is completion evidence for the
observer's task; after it arrives, one final result message is enough unless the
user asked for audit details. Use `job_list` or `job_read_output` afterward when
you need explicit audit/diagnosis evidence. Frames coalesce while the observer
is busy — it sees the latest state, not a backlog. Watching your own
self-generated events with delivery back to yourself is rejected: that is a
feedback loop, not observation. Use `events: ["communicate"]` for explicit
result/status messages sent through the result tool; `communicate` is the
watchable result/status channel. A communicate observer watch is just
`target:"caller"`, `events:["communicate"]`, and `send.to:<observer delegate_id>`;
that complete watch sends communicate frames to the observer, and the observer's
task is the predicate for content such as `APPROVAL_REQUEST`. Use
`events: ["assistant.tool"]` for tool events; the complete filtered tool-watch
shape adds `event_filter:{"tool_name":"read_file","status":"ok"}`.

While an observer callback is expected, a terminal notification for the observer
delegate can arrive as confirmation of work already represented by the delegate
result or callback job. The callback steering carrying the observer's packet is
the next actionable signal.

Observer setup is sequential when the trigger depends on the watch existing:
start the observer and receive its readiness result, create the watch and receive
the `watch_id`, then trigger the watched event in the following response. After
the trigger, Serf yields the caller turn when the frame is handed to the observer;
continue from the observer callback when it arrives, then finish once.

`job_stop` cancels and preserves output/history. A finished delegate remains
resumable — `delegate_send(to=<delegate_id>, on_idle="start")` starts its next
turn in the same conversation as a new job.
