# subagent-cancel-runaway: job_stop stops a runaway delegate and delegate_send starts its next turn

**What this covers**: `job_stop` as the public stop primitive for delegate work,
plus the resumable follow-up path through `delegate_send`.

## Steps

1. Start a real Serf run with a fresh scenario state dir.
2. Ask the parent:

   > Call `delegate` (max_wait_ms unset — creation always returns
   > immediately) with this task: "Using the shell tool, run the command:
   > sleep 30. Only after it finishes, call communicate with the message
   > DONE_SLEEPING." Capture the returned `delegate_id`.
   > Immediately call `job_stop` with `target` set to that `delegate_id`;
   > do not call `job_list` first. Report the full JSON, especially
   > status, reason and outcome.
   > Then call `delegate_send` targeting the same `delegate_id` with
   > max_wait_ms 30000 and this message: "Forget the
   > sleep. Using the shell tool, run: echo RESUMED_OK. Then communicate
   > the message RESUMED_OK."
   > Report the follow-up result's `delegate_id`, status, action, and
   > output.

## Expected

- Creation returns a `delegate_id` and no job identity of any kind — a
  stable delegate is addressed only by `delegate_id`
  (`agent/session_tools.go#stableDelegateCreateResult`), which is also
  the only handle `job_stop` and `delegate_send` accept for it.
- A confirmed `job_stop` on a delegate returns `status` `idle` — the
  reusable resource's lifecycle, which has only `running` and `idle`
  (`agent/delegate_tree_controller.go#captureDelegateSnapshot`) — with
  `reason` `stopped_by_parent`, `previous_status` `running`, and
  `outcome` `cancelled_by_request`, the field that records that the
  request cancelled a live run rather than finding it `already_idle`
  (`agent/session_tools_jobs.go#stableDelegateStopResult`). The RUN's own
  terminal is `stopped`/`stopped_by_parent`, readable through
  `job_status(target=<delegate_id>)`'s `last_outcome`; the fold forces
  that pair for any finish landing in the stopping phase
  (`agent/internal/delegatestore/fold.go#applyRunFinished`), so
  `cancelled` is not reachable here. `running`/`stop_pending` with
  `outcome` `stop_requested` means the stop has not completed within the
  bounded wait; the scenario must wait for the later terminal
  notification/record before considering the stop successful.
- `delegate_send` against the stopped delegate starts a new generation in
  the same conversation unless the target is explicitly not resumable. It
  returns the SAME `delegate_id` and no job identity — the stable result
  carries no `job_id`, `started_job_id` or `current_job_id`
  (`agent/session_tools_jobs.go#marshalDelegateSendResult`). With the
  30s inline wait satisfied it reads `action` `completed`; if the wait
  expires first it reads `action` `started` / `status` `running`, which
  is an unfinished wait rather than a failure.
- The follow-up completes and reports `RESUMED_OK`.
