# recursion-deaf-coordinator-drivedown: an idle coordinator receives workers' delegate notifications

**What this covers**: the recursive drive-down boundary from design §9. A
stable delegate coordinator (granted `delegation_allowance=1` under
`MaxSubagentDepth=2`) starts two leaf delegates and ends its turn. The workers'
direct delegate completions are delivered to the coordinator's own session,
which is driven for a notification turn while idle. The root receives only the
coordinator's direct delegate notification. This is not sibling fan-in: the
coordinator is the owner of its direct workers, and each completion is addressed
by stable `delegate_id` plus the worker session `transcript_ref`.

## Falsifiable claims

1. **The coordinator is driven (not deaf):** its transcript contains a
   post-idle notification turn for both worker delegate completions.
2. **Owner-scoped:** the root receives only the coordinator's
   `<delegate-notification delegate_id="dlg_...">`; no worker delegate ID or
   worker report appears as a root notification subject.

This is the single-coordinator focus of `recursion-coordinator-fanout.md`.

## Pre-state

- Fresh binaries and isolated hub/state directory, per
  `docs/developing-evener/agentic-testing.md`.
- `tmpdir=$(mktemp -d -t evener-e2e-recdeaf-XXXXX)`.
- Set `launch_overrides.maxSubagentDepth=2`; the root's allowance must permit a
  coordinator grant of 1, and the coordinator's workers receive allowance 0.

## Steps

1. Spawn a root session with `working_dir=$tmpdir` and the depth override.
2. Prompt the root:

   > Call `delegate` with `delegation_allowance` 1 and this exact task:
   > "You are COORDINATOR. Start TWO worker delegates, each with
   > `delegation_allowance` 0. Worker 1 runs
   > `sh -c 'echo W1_DONE; sleep 6'`; worker 2 runs
   > `sh -c 'echo W2_DONE; sleep 6'`. Capture each worker's `delegate_id` and
   > session `transcript_ref`. After starting both, communicate exactly
   > `COORD_WORKERS <worker-1 delegate_id> <worker-2 delegate_id>` and end your
   > turn immediately. Do not poll or call job tools."
   > Capture the coordinator's `delegate_id` and session `transcript_ref`, then
   > end the root turn without polling.

3. Let the workers finish while the coordinator is idle. Do not send input
   during their sleep; this must exercise drive-down rather than same-turn
   work.
4. After the coordinator's completion, inspect the root transcript and report
   every direct `<delegate-notification delegate_id="...">` frame on the root
   rail. Then inspect the coordinator session transcript using its returned
   `transcript_ref`.
5. Optionally use `find_session_transcripts({children_of: ...})` to enumerate
   worker sessions and read each worker's session transcript. Do not construct
   a `job:` ref for a delegate.

## Expected

- The coordinator creation result has a stable `delegate_id`, child/session
  metadata, and a session `transcript_ref`; it has no delegate `job_id`.
- The coordinator's transcript contains a post-idle notification turn carrying
  both workers' direct delegate completions and their stable IDs. Each worker's
  report can be verified in that worker's session transcript.
- The root receives exactly one direct coordinator
  `<delegate-notification delegate_id="...">` and no worker notification
  subject or worker report inside a root notification. The worker IDs may occur
  in the coordinator's transcript and in explicit transcript audit output;
  those are not root notification frames.
- The stable delegate controller/session records remain available for on-demand
  `find_session_transcripts`; there is no delegate `JobRecord`, forwarded
  delegate job record, `job_type="delegate"`, or delegate `job:` output.
- This card does not add a sibling subscription, observer grant, or fan-in
  feature. Workers report to their owner coordinator through the existing
  direct-delegate completion route.

## Cleanup

Shut down the root session and remove only the fixture-owned `$tmpdir`.

## Sharp edges

- The coordinator must end its first turn before workers finish. Do not use
  `job_list`, `job_status`, `read_transcript(job:...)`, sleeps, or polling to
  wait.
- Assert the coordinator wake on its own session transcript, not the root UI
  rail. Assert root ownership by matching notification subjects to the specific
  worker `delegate_id`s captured in `COORD_WORKERS`.
- A direct delegate notification is distinct from a shell `<job-notification>`;
  shell `job_id` and `job:` refs are out of scope for this recursive delegate
  contract.
