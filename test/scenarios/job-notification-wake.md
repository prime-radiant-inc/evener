# job-notification-wake: a delegate completion wakes an idle parent

**What this covers**: a non-blocking delegate job completes after the parent has
ended its turn, then Serf wakes the parent with a proactive `<job-notification>`
so the parent can read the result.

## Steps

1. Start `serf serve` and a hub client against a fresh scenario state dir.
2. In the hub, ask the parent:

   > Call `delegate` (max_wait_ms unset — returns job_id immediately) with this
   > task: "Run `sleep 15` via the shell tool, then communicate the exact text
   > CHILD_DONE_42." After you receive the `job_id`, do not call
   > `job_read_output` or `job_list`. Tell me the job has started and that you
   > will report its result when notified, then end your turn.

3. Wait for the delegate to finish and for Serf to inject a notification turn.
4. Confirm the parent reacts to the notification by surfacing `CHILD_DONE_42`
   to the user.

## Expected

- The first parent turn calls `delegate` (max_wait_ms unset) and then goes
  idle without polling — the job_id is returned immediately and the delegate
  runs in the background.
- A later steering/system entry contains a `<job-notification ...>` for the
  delegate `job_id` with terminal status.
- The follow-up parent turn surfaces `CHILD_DONE_42` to the user. Terminal
  notifications carry a result excerpt (`75c11569`), so for an output this
  small the excerpt contains the full text and reading it from the excerpt is
  the designed, optimal path — a `job_read_output` call for the same `job_id`
  is equally acceptable but MUST NOT be required. What is asserted: the
  surfaced text matches the child's exact output, and its provenance is the
  notification (excerpt or read), not invention.
- The hub renders the delegate as a job reference/lifecycle card rather than a
  blank or unknown legacy control tile.
