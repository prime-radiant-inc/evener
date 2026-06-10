# job-list-and-output: job_list enumerates a delegate and job_read_output peeks it

**What this covers**: the job-control read path for delegate jobs. A parent
starts a delegate, lists jobs, reads the delegate output, and reads it a second
time to prove output inspection is non-consuming.

## Steps

1. Start a real Serf run with a fresh scenario state dir.
2. Ask the parent:

   > Call `delegate` with `background=false` and this task: "Using the shell
   > tool, run: echo hello-from-child. Then call communicate with this exact
   > message: RESULT=hello-from-child." Capture the returned `job_id`.
   > Then call `job_list` and report the full JSON. Confirm the job appears
   > with type `delegate`, status, task, and `transcript_ref`.
   > Then call `job_read_output` for that `job_id` twice and report whether
   > both reads return the delegate result.

## Expected

- `delegate` returns a `job_id`, type `delegate`, a terminal status, and a
  `transcript_ref`.
- `job_list` includes the delegate job with the same `job_id`.
- Both `job_read_output` calls return the result text. The second read must not
  fail or hide the output.
- If the delegate transcript is needed for audit, the parent uses
  `read_session_transcript` with the `transcript_ref`; `job_read_output` is only
  for invocation output and final reports.
