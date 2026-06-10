# job-notification-wake: a delegate completion wakes an idle parent

**What this covers**: a non-blocking delegate job completes after the parent has
ended its turn, then Serf wakes the parent with a proactive `<job-notification>`
so the parent can read the result.

## Steps

1. Start `serf serve` and a hub client against a fresh scenario state dir.
2. In the hub, ask the parent:

   > Call `delegate` with `background=true` and this task: "Run `sleep 15` via
   > the shell tool, then communicate the exact text CHILD_DONE_42." After you
   > receive the `job_id`, do not call `job_read_output` or `job_list`. Tell me
   > the job has started and that you will report its result when notified, then
   > end your turn.

3. Wait for the delegate to finish and for Serf to inject a notification turn.
4. Confirm the parent reacts to the notification by calling `job_read_output`
   and reporting `CHILD_DONE_42`.

## Expected

- The first parent turn calls `delegate` with `background=true` and then goes
  idle without polling.
- A later steering/system entry contains a `<job-notification ...>` for the
  delegate `job_id` with terminal status.
- The follow-up parent turn calls `job_read_output` for that `job_id` and
  surfaces `CHILD_DONE_42`.
- The hub renders the delegate as a job reference/lifecycle card rather than a
  blank or unknown legacy control tile.
