# job-send-message-surface: a job handle is rejected outright, a running delegate steers live, an idle delegate resumes on send

**What this covers**: the core `delegate_send` outcomes against the
handle-split API. (a) A `job_id` handle (a shell job, never a delegate)
addressed to `delegate_send` fails OUTRIGHT with the bare tool error
`not_controllable: delegate` — no JSON, no id resolution, nothing
delegate-shaped in the failure at all; (b) a RUNNING delegate receives a
live steer through `delegate_send`, with `action:"steered"` and no job
identity of any kind in the result; (c) an IDLE delegate's `delegate_send`
starts a NEW generation in the SAME conversation — `action:"started"`
while it runs, `action:"completed"` if the inline wait resolves in time —
again with no job identity: the only identity a send result carries is
`delegate_id` plus (once the generation is dispatched) `transcript_ref`.

**Identity and transport.** Stable `delegate` creation returns
`delegate_id` / `child_session_id` / `transcript_ref` and no `job_id`
field at all (`agent/session_tools.go#stableDelegateCreateResult`);
passing `max_wait_ms` to creation is rejected outright with
`invalid_request: delegate creation does not accept max_wait_ms`
(`agent/session_tools.go#stableDelegateCreateTool`). `delegate_send`'s own
result carries no job field either — `sendMessageResult`
(`agent/job_delegate.go#sendMessageResult`) and its wire form
`delegateSendResult` (`agent/session_tools_jobs.go#delegateSendResult`)
have `delegate_id`, `transcript_ref`, `action`, `status`; neither declares
`job_id`, `started_job_id`, `current_job_id`, or `latest_job_id`, and
nothing in the tree emits any of those four names on this path. There is
no per-target handle-type check on `delegate_send` at all — `to` is
handed straight to `(delegateRuntime).send`
(`agent/delegate_runtime.go#delegateRuntime.send`), which authorizes
through the delegate tree's own id map and never inspects the string's
shape. A `job_` handle fails only because it is not a key in that map:
`authorizeMutationLocked` (`agent/delegate_tree_controller.go#delegateTreeController.authorizeMutationLocked`)
returns `errDelegateNotControllable`
(`agent/delegate_tree_controller.go#errDelegateNotControllable`, the string
`not_controllable: delegate`) for ANY unrecognized id, and the resulting
`sendMessageResult` has `DelegateID` empty
(`agent/job_delegate.go#sendMessageFailed`), so
`stableDelegateSendTool` (`agent/session_tools.go#stableDelegateSendTool`)
returns that bare error with no result JSON at all. The string
`job_id is a job/turn handle` is real, but it lives only on the internal
WATCH delivery validator
(`agent/job_watch.go#validateWatchSendDeliveryTarget`), a different code
path `delegate_send` never calls — do not expect it here. A stable
delegate's `transcript_ref` is a session ref and reads its whole
conversation; delegates never use `job:` refs
(`agent/internal/tool/definitions.go#DefReadTranscript`).

## Pre-state

- Fresh binaries from the branch under test; an isolated hub
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t evener-e2e-dsend-XXXXX)`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — arm (a), a real job handle rejected outright, and arm (b),
   live steer of a running delegate:

   > Do these steps in order.
   > 1. Run the shell tool with mode: "background" and command:
   >    `sleep 300`. Capture the job_id (call it SHJOB) — this is a real
   >    job handle, from a shell job, not a delegate.
   > 2. Call delegate (background default) with this exact task:
   >    "FIRST run the shell command `sleep 15`. Then communicate
   >    exactly RUNNING_DONE. If you receive a steering message
   >    containing STEER_MARK_88, include STEER_MARK_88 in your final
   >    communicate." Capture the `delegate_id` (call it DLG) and
   >    `transcript_ref`. There is no job_id to capture here — delegate
   >    creation returns none.
   > 3. Immediately call delegate_send with `to` set to SHJOB and
   >    message "wrong handle probe". Report the raw tool result
   >    verbatim, exactly as your client shows it — do not paraphrase
   >    or wrap it.
   > 4. Immediately call delegate_send with `to` set to DLG and message
   >    "Mid-task instruction: include STEER_MARK_88." Report the full
   >    result JSON verbatim.
   > 5. Say STEER_SENT and end your turn. When DLG's terminal
   >    `<delegate-notification>` arrives, report that frame's full text
   >    verbatim including the JSON inside it, then call read_transcript
   >    with the transcript_ref DLG's creation returned and report what
   >    the child actually sent.
   > 6. `job_stop` SHJOB with max_wait_ms 5000 so it does not outlive the
   >    card.
3. Turn 2 — arm (c), idle delegate resumes on send (new user prompt):

   > Do these steps in order.
   > 1. Call delegate (background default, NO max_wait_ms) with this
   >    exact task: "Remember the codeword AZURE_FALCON for later turns
   >    of this conversation. Communicate exactly READY_TO_RESUME and
   >    finish." Capture the returned `delegate_id` (call it DLG2) and
   >    `transcript_ref`.
   > 2. Say WAITING_FOR_IDLE and end your turn without waiting.
4. Turn 3 — after DLG2's terminal notification has rendered (new user
   prompt), so DLG2 is genuinely idle and not still running:

   > Call delegate_send with `to` set to DLG2, message "Reply via
   > communicate with exactly the codeword you were told earlier, and
   > nothing else.", and max_wait_ms 120000. Report the full result JSON
   > verbatim — both the text the tool printed and, if your client
   > exposes it, the structured tool state. Then end your turn.
5. Read the parent transcript, DLG2's own transcript (via its
   `transcript_ref`), and
   `find ~/.local/state/evener/projects -path "*sessions/$SID/jobs.jsonl"`.

## Expected

- Arm (a): the raw tool result for the SHJOB call is the bare error
  string `not_controllable: delegate` — nothing else. No JSON envelope,
  no `delegate_id`, no interpolated id of any kind, no `job_id is a
  job/turn handle` (that string belongs to the watch-delivery validator,
  a path `delegate_send` never reaches — see Identity and transport
  above). `jobs.jsonl` gains no new record attributable to this call: a
  rejected `delegate_send` never reaches `jobstore` at all, on a
  `job_`-handle target or any other. Falsification: any JSON structure
  in the result, any id interpolated into the error text, or the
  watch-path string appearing instead.
- Arm (b): the step-4 result has `action` `"steered"`, `delegate_id`
  equal to DLG, `status` `"running"`, and `running_in_background` true —
  and NO `transcript_ref` key at all (the steer branch never sets one,
  and the field is `omitempty`), and no `job_id`/`started_job_id`/
  `current_job_id`/`latest_job_id` field of any kind (none exist on this
  type). STEER_MARK_88 does not appear in the steer result itself — a
  live steer returns on delivery, before the child has replied. It shows
  up in DLG's terminal `<delegate-notification delegate_id="<DLG>">`
  frame and in the child's own transcript, as a STEERING entry ahead of
  the final communicate. Falsification: the token absent from both the
  terminal frame and the child transcript, a `transcript_ref` present on
  the steer result, or a job-shaped field present anywhere in it.
- Arm (c): the step-4-of-turn-3 result names DLG2's `delegate_id` and
  carries no job identity of any kind, matching arm (b)'s shape. Ask for
  120000 ms: `clampJobBlockTimeout` caps any inline wait at
  `maxJobBlockTimeoutMS`, 60000
  (`agent/session_tools_jobs.go#clampJobBlockTimeout`,
  `agent/session_tools_jobs.go#maxJobBlockTimeoutMS`), so the actual wait
  is half what was asked. If the new generation finishes inside that
  window: `action` `"completed"`, `running_in_background` false,
  `transcript_ref` equal to the SAME ref DLG2's creation returned (same
  conversation, new generation), and an `output` field containing
  AZURE_FALCON — a fact present only in the prior generation's context,
  so its presence proves conversation continuity across the resume, not
  a fresh guess. If the wait expires first: `action` stays `"started"`,
  `running_in_background` true, `timed_out` true, and no `output` field
  yet; read DLG2's transcript (via its `transcript_ref` — never
  `job:<anything>`, delegates do not have one) for the eventual reply
  instead of treating the timeout as a failure.
  Falsification: the codeword missing from a completed result and its
  successor transcript read both, the transcript ref changing between
  creation and resume, or any `job_id`-shaped field appearing.

## Cleanup

- SHJOB is stopped in turn 1. Both delegate generations are terminal by
  design once their notifications render. Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- The live steer must land while DLG is still in its initial sleep; the
  turn-1 step-4 call follows step-2 immediately, so the expected path is
  stable under normal hub latency.
- Arm (a)'s SHJOB must be a REAL job handle, not an invented string —
  the point is that `delegate_send` gives the exact same
  `not_controllable: delegate` refusal to a syntactically well-formed
  handle from the wrong namespace as it would to garbage; inventing the
  id would leave the card unable to tell "wrong namespace" from "no such
  id at all", which happen to share an error string here but are not the
  same claim.
- Do not expect any structured guidance toward `delegate_id` in arm
  (a)'s error. The rejection is a raw `errors.New`, not a result type
  with a hint field — asking a runner to "report the corresponding
  delegate_id" from this error is asking for an invented answer.
- Turn 3 must wait for DLG2 to be genuinely idle before sending. Creation
  is asynchronous and nothing about turn 2 proves the generation ended;
  DLG2 is idle once its terminal notification has rendered, which is
  what makes the delegate_send in turn 4 land on the `errDelegateTargetBusy`→
  `ReserveStart` resume path instead of live-steering a still-running
  generation.
