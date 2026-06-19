# job-stop-runaway: job_stop stops a runaway delegate and delegate_send starts its next turn

**What this covers**: `job_stop` as the public stop primitive for delegate work,
plus the resumable follow-up path through `delegate_send`.

## Steps

1. Start a real Serf run with a fresh scenario state dir.
2. Ask the parent:

   > Call `delegate` (max_wait_ms unset — returns immediately) with
   > this task: "Using the shell tool, run the command: sleep 30. Only after
   > it finishes, call communicate with the message DONE_SLEEPING." Capture
   > the returned `delegate_id` and `job_id`.
   > Immediately call `job_stop` on that `job_id`; do not call `job_list`
   > first. Report the full JSON, especially status and reason.
   > Then call `delegate_send` targeting the same `delegate_id` with
   > on_idle "start", max_wait_ms 30000, and this message: "Forget the
   > sleep. Using the shell tool, run: echo RESUMED_OK. Then communicate
   > the message RESUMED_OK."
   > Report the follow-up job's `started_job_id`, `current_job_id`,
   > status, action, and output.

## Expected

- The first delegate job starts in the background and returns a `delegate_id`
  and `job_id` before the sleep finishes.
- A confirmed `job_stop` returns terminal status `cancelled` with reason
  `stopped_by_parent`. `stopped`/`stop_unconfirmed` is acceptable only when
  cancellation cannot be confirmed. `running`/`stop_pending` means the stop has
  not completed within the bounded wait; the scenario must wait for the later
  terminal notification/record before considering the stop successful.
- `delegate_send(on_idle="start")` against the stopped delegate starts a
  follow-up delegate job unless the target is explicitly not resumable.
- The follow-up completes and reports `RESUMED_OK`.
