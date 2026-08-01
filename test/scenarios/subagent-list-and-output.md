# subagent-list-and-output: job_list enumerates a delegate and a job: transcript read peeks it

**What this covers**: the job-control read path for delegate jobs. A parent
starts a delegate, lists jobs, reads the delegate output, and reads it a second
time to prove output inspection is non-consuming.

## Steps

1. Start a real Serf run with a fresh scenario state dir.
2. Ask the parent:

   > Call `delegate` with max_wait_ms 30000 and this task: "Using the shell
   > tool, run: echo hello-from-child. Then call communicate with this exact
   > message: RESULT=hello-from-child." Capture the returned `job_id`.
   > Then call `job_list` and report the full JSON. Confirm the job appears
   > with type `delegate`, status, and `transcript_ref`.
   > Then call `read_transcript` with transcript_ref "job:<job_id>" twice and
   > report whether both reads return the delegate result.

## Expected

- `delegate` returns a `job_id`, type `delegate`, a terminal status, and a
  `transcript_ref`.
- `job_list` includes the delegate job with the same `job_id`. Do not expect a
  `task` key on the row: no job-control response carries one. `jobListEntry`'s
  closest field is `description` (`agent/session_tools_jobs.go:1317`), which is
  populated only from the shell tool's optional `description` argument
  (`agent/jobs.go:679`) and is therefore always empty for a delegate job. The
  task text lives on the internal `JobRecord.Task`
  (`agent/internal/jobstore/record.go:234`), exposed under no JSON key here —
  read the child transcript for it.
- Both `read_transcript(transcript_ref="job:<job_id>")` calls return the same
  envelope, whose `content` is a `# Delegate Job job_...` heading, a `- status:`
  line, `- total_bytes:`, and the result text in a fenced block. The second read
  must not fail or hide the output.
- If the delegate transcript is needed for audit, the parent uses
  `read_session_transcript` with the `transcript_ref`; the `job:` ref is only
  for invocation output and final reports.
