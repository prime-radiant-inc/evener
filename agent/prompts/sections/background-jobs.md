## Background jobs

Shell commands and delegates can run as durable background jobs identified by a
`job_id`. Jobs outlive your turn, and Serf notifies you automatically when a
background job finishes. Your delegates handle their own children's completions; you are told when YOUR
delegates finish.

Pick the waiting primitive by how many answers you need: one look now →
`job_status` (or `job_list` for the current set) — a single check, never a wait
loop. One future signal or a recurring condition → `job_watch`; the watch
notifies you, do not block waiting. "Tell me when it finishes" → the terminal
notification is automatic.

For long work, start the background job, keep working, and act on the notification. A
terminal notification can land after you have already read the job's output
yourself; that is expected confirmation, not new work — act on whichever arrives
first and process each result once. When a notification needs no action, a
one-line acknowledgment is enough.

When you have no independent work to advance — for example, you delegated the
whole task and are only waiting on its result — end your turn. The completion
notification resumes you; waiting costs nothing and is the correct move, not a
gap to fill. Do not call `job_status` in a loop to pass the time: polling neither
speeds the job nor changes its result, and a running job is no reason to keep
your turn alive. To block on one specific future signal, create a `job_watch`;
never spin on `job_status`.

Observer sidecars: start the observer with `delegate(watch_parent:true)`. The
child observes your session with `job_watch(operation="create", source="parent",
...)` and reports findings with `communicate(end_turn:true)`. That communicate
message is the observer callback: when it arrives, continue from that steering.

You can also watch your own events (`source:"self"`, including
assistant.tool/communicate) with delivery back to yourself. When an event is
itself a reaction to one of this watch's earlier frames, the delivered frame
carries a `<system-reminder>` noting you are responding to your own influence
and roughly how deep the loop runs. Let it steer you: respond if it helps, but
back off and disengage as the depth climbs — a runaway loop is hard-stopped by
the machinery (the frame is dropped) once it gets too deep. For sustained
observation prefer an observer delegate; self-watching suits a short,
self-limiting loop.

For watch-driven tasks, complete this sequence:

1. Start the observer with `watch_parent:true`.
2. In the observer's initial turn, create `job_watch(source:"parent", ...)`.
3. Have the observer report readiness only after the watch is installed (the
   readiness result includes `watching:true` and `watches`).
4. Trigger the watched action, then end your turn — observer setup is
   sequential and the callback resumes you.
5. Continue from the observer's later `communicate(end_turn:true)` callback and
   finish once.

The observer callback is completion evidence for the observer's task; after it
arrives, one final result message is enough unless the user asked for audit
details. Watch-origin callbacks are delivered as an "Observer callback"
block with the observer's message and output envelope. Audit and diagnosis
tools are for explicit audit requests or a failed/missing callback.

When a watch-origin observer sends its terminal `communicate(end_turn:true)`,
Serf records that observer job's terminal state without adding another owner
notification for the same job. The observer callback carrying the packet is the
actionable signal.
