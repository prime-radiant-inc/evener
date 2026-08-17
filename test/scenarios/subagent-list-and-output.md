# subagent-list-and-output: job_list enumerates a delegate and its session ref reads it, twice

**What this covers**: the read path for a stable delegate. A parent starts a
delegate, lists it, reads its conversation through the delegate's own
`transcript_ref`, and reads it a second time to prove inspection is
non-consuming.

**Identity.** A delegate is not a job and is not addressed like one.
`delegate` creation is asynchronous, returns `delegate_id` /
`child_session_id` / `transcript_ref` and no `job_id`
(`agent/session_tools.go#stableDelegateCreateResult`), and rejects
`max_wait_ms` outright with `invalid_request: delegate creation does not
accept max_wait_ms` (`agent/session_tools.go#stableDelegateCreateTool`).
`read_transcript`'s own description is normative here: "A stable
delegate's session ref reads its conversation; delegates never use job:
refs", and a `job:<job_id>` ref "reads shell output only". There is no
delegate job record to address either — `jobstore.JobType` has exactly
one value, `shell` (`agent/internal/jobstore/record.go#JobType`).

## Steps

1. Start a real Serf run with a fresh scenario state dir.
2. Ask the parent:

   > Call `delegate` with NO max_wait_ms and this task: "Using the shell
   > tool, run: echo hello-from-child. Then call communicate with this exact
   > message: RESULT=hello-from-child." Report the full creation result JSON
   > verbatim and capture the returned `delegate_id` and `transcript_ref`.
   > Then end your turn without waiting.
3. After the delegate's terminal notification has rendered, ask the parent:

   > Call `job_list` and report every row exactly as the tool printed it.
   > Then call `read_transcript` with the `transcript_ref` the delegate's
   > creation returned, twice, and report whether both reads return the
   > child's conversation.

## Expected

- `delegate` returns `delegate_id` (a `dlg_...`), `child_session_id`,
  `type` `delegate`, a lifecycle `status`, and a `transcript_ref`. No
  `job_id` field is present at all. Falsification: any job identity on
  the creation result.
- The terminal arrives as a `<delegate-notification delegate_id="dlg_...">`
  frame on the parent's rail, carrying the delegate id and no job identity
  (`agent/delegate_delivery.go#delegateNotificationContent`).
- `job_list` lists the delegate as a delegate RESOURCE row, not a job
  record: the id is the `dlg_...` delegate_id, the type is `delegate`, and
  the status is the two-valued lifecycle — `running` while it works,
  `idle` once it has reported, never `completed`
  (`agent/delegate_tree_controller.go#captureDelegateSnapshot`). The run's
  outcome reason rides the row's `[started · reason · exit · bytes]` tail,
  not the status column. The row's structured state does carry `task`,
  populated from the descriptor (`agent/session_tools_jobs.go#jobListEntry`;
  `agent/session_tools_jobs.go#projectStableDelegateListItem`), but
  `formatJobList` never prints it — the model-facing line shows a label
  taken from `description`. Assert on the printed line, and read the task
  text from the child's transcript rather than expecting the listing to
  show it.
- Both `read_transcript` calls on the delegate's `transcript_ref` return
  the same envelope — the child's semantic transcript in markdown,
  containing the shell call and the `RESULT=hello-from-child` communicate.
  The second read must not fail or hide anything. Falsification: the
  second read is empty, errors, or differs.
- Falsification (the retired contract): a `job:` ref built from anything
  this card captured is accepted and returns the delegate's output. Per
  `read_transcript`'s contract a delegate has no `job:` address, so a
  successful read that way means the job/delegate split has regressed.
