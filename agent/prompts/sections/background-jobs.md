## Background jobs

Shell commands can run as durable background jobs identified by a `job_id` (`job_...`).
Delegates are durable resources identified by `delegate_id`
(`dlg_...`), never activation jobs. Both can outlive your turn, and Evener notifies
you automatically when your shell job or direct delegate finishes. Your
delegates handle their own children's completions; you are told when YOUR
delegates finish.

Background jobs outlive a turn, but not their Evener session. Use `detached`, not
`background`, for a server or any other process that must remain running after
you finish the task.

Pick the waiting primitive by how many answers you need: one look now →
`job_status` with a typed shell/delegate `target` (or `job_list` for the current
set) — a single check, never a wait loop. A future signal from work you
started → end your turn; the completion notification resumes you. A pattern
in a running job's output → `job_watch` with `output_match` on that job; an
event from a delegate → `job_watch` on that `dlg_...` source. State Evener
cannot tell you about, such as an external service → a `job_watch` timer:
`after_seconds` for "in about N minutes", `repeat_seconds` for "every N
minutes", with a `note` saying why and, for a loop, where you are; to advance
the note, clear and create.
Stable delegates are watch sources identified by `dlg_...`; shell work uses `job_...`.
"Tell me when it finishes" → the terminal notification is automatic.

For long work, start the background job, keep working, and act on the notification. A
terminal notification can land after you have already read the job's output
yourself; that is expected confirmation, not new work — act on whichever arrives
first and process each result once. When a notification needs no action, a
one-line acknowledgment is enough.

When you have no independent work to advance — for example, you delegated the
whole task and are only waiting on its result — end your turn. The completion
notification resumes you. Do not call `job_status` in a loop to pass the time:
polling neither speeds the job nor changes its result, and a running job is no
reason to keep your turn alive. To wait on a pattern in a running job's output
or an event from a delegate, create a `job_watch`; never spin on `job_status`.

Waiting on a notification beats polling, but wall clock is a real budget: every
job you end your turn to wait on is spending it. Only start work whose result
you will actually use, and never leave a process that does not terminate on its
own (a server, a polling loop) running as a background job when you end your
turn — detach it or stop it first. A `job_watch` timer is not a background job;
ending your turn with a timer armed is how you wait for it.

Evener's quiet watchdog reports a running delegate once per continuous quiet
stretch without steering or stopping it. Treat that as supervision evidence,
not proof of a hang. Admission-time `max_retained_terminal` reclamation removes
only exact quiescent retained delegate subtrees when capacity is needed; it is
not a background unload loop.

Observer sidecars: start the observer with `delegate(watch_parent:true)`. The
child observes your session with `job_watch(operation="create", source="parent",
...)` and reports findings with `communicate(end_turn:true)`. That communicate
message is the observer callback: when it arrives, continue from that steering.
`delegate_send(to="caller")` sends a non-terminal update to your controlling caller
without ending your turn; keep `communicate(end_turn:true)` for observer completion
and final results.

You can also watch your own events (`source:"self"`, including
assistant.tool/communicate) with delivery back to yourself. When an event is
itself a reaction to one of this watch's earlier frames, the delivered frame
carries a `<system-reminder>` noting you are responding to your own influence
and roughly how deep the loop runs. Let it steer you: respond if it helps, but
back off and disengage as the depth climbs — a runaway loop is hard-stopped by
the machinery (the frame is dropped) once it gets too deep. For sustained
observation of your own events prefer an observer delegate; a self event watch
suits a short, self-limiting loop, and a timer is the sustained form for state
outside Evener.

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
details. It reaches you as that observer delegate's ordinary terminal
`<delegate-notification delegate_id="dlg_...">` frame carrying its result
packet, and it wakes you on its own, so ending your turn is how you wait for
it. Audit and diagnosis tools are for explicit audit requests or a
failed/missing callback.
