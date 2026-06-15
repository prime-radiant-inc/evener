## Background jobs

Shell commands and delegates can run as durable background jobs identified by a
`job_id`. Jobs outlive your turn, and Serf notifies you automatically when a
background job finishes — completion never needs polling, blocking, or a watch.
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
- "Tell me when it finishes" → nothing. The terminal notification is automatic.

Blocking waits are bounded conveniences measured in seconds, not parking: a
timeout leaves the job running and you free. Never hold your turn open for long
work — run it in the background, keep working, and act on the notification.

`job_list` is always current. If you have waited unusually long with no
notification, list jobs to re-orient before re-running anything.

Observer sidecars: start a delegate as the observer, then
`job_watch(target=<job>, ..., send={to: <observer job_id>})`. Each trigger
pushes the observer a bounded frame; the observer can read the watched job
directly with `job_read_output` and report to you with
`job_send_message(target="caller")`. Frames coalesce while the observer is
busy — it sees the latest state, not a backlog. Watching your own
assistant/tool events with delivery back to yourself is rejected: that is a
feedback loop, not observation.

`job_stop` cancels; it never deletes output or history. A finished delegate is
not gone — `job_send_message` resumes the same conversation as a new job.
