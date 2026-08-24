# job-delegate-result-schema: valid results validate, violations report honestly, resumes inherit the schema

**What this covers**: `delegate.result_schema` end to end
(`docs/job-control.md` § `delegate` `result_schema` rule and
§ "Reading job output"). (a) A schema-backed delegate that complies
reports `structured_result` with `structured_result_valid: true` — in
the terminal notification frame the parent is woken with, and durably in
the root's `delegates.jsonl`; (b) a deliberately schema-violating result
is reported honestly: the invalid payload is retained as sent, flagged
`structured_result_valid: false` with a machine-readable
`structured_result_reason` alongside it; (c) a follow-up turn in the same
delegate conversation inherits the ORIGINAL `result_schema` although
`delegate_send` has no schema argument. Delegate `status="completed"`
never asserts task success — the structured fields are how the parent
judges outcome.

**Identity and transport.** Stable `delegate` creation is asynchronous
and returns `delegate_id` / `child_session_id` / `transcript_ref` and no
`job_id` at all (`agent/session_tools.go#stableDelegateCreateResult`);
passing `max_wait_ms` is rejected outright with
`invalid_request: delegate creation does not accept max_wait_ms`
(`agent/session_tools.go#stableDelegateCreateTool`), so there is no
foreground delegate creation to read a result from. Delegate generations
create no job records ("Durable job records" "Delegate generations never
create job records."), so `read_transcript(transcript_ref="job:<id>")`
addresses nothing here and `jobs.jsonl` holds no evidence for this card.
Every observation below comes from the notification frame, the child's
own `transcript_ref`, the `delegate_send` result, or `delegates.jsonl`.

## Pre-state

- Fresh binaries from the branch under test; an isolated hub
  (`docs/developing-evener/agentic-testing.md` setup checklist); credentialed model that
  follows deliberate-misbehavior test instructions.
- `tmpdir=$(mktemp -d -t evener-e2e-jschema-XXXXX)`.
- The schema used throughout:
  `{"type":"object","properties":{"verdict":{"type":"string"},"count":{"type":"integer"}},"required":["verdict","count"]}`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — arm (a), compliant delegate:

   > Call delegate with NO max_wait_ms, result_schema
   > `{"type":"object","properties":{"verdict":{"type":"string"},"count":{"type":"integer"}},"required":["verdict","count"]}`,
   > and this task: "Report a structured result with verdict ok and
   > count 7, with a one-line summary message." Report the full creation
   > result JSON verbatim — note its delegate_id (call it DLG1) and its
   > transcript_ref. Then END YOUR TURN without waiting.
3. Turn 2 — arm (a) observation, after DLG1's terminal notification has
   rendered (new user prompt):

   > Report verbatim the full text of every `<delegate-notification
   > delegate_id="...">` frame that has rendered on your rail so far,
   > including the JSON inside each one. Then call read_transcript with
   > the transcript_ref DLG1's creation returned, and report what the
   > child actually sent. Then end your turn.
4. Turn 3 — arm (b), deliberate violation (new user prompt):

   > Call delegate with NO max_wait_ms, the SAME result_schema, and this
   > exact task: "This is a schema-violation test. In your final
   > structured output, set verdict to the string bad and set count to
   > the STRING value banana — a string, deliberately NOT a number. Do
   > not correct the type; the test needs the invalid payload." Report
   > the delegate_id (call it DLG2) and its transcript_ref, then end
   > your turn; when its terminal notification arrives, report that
   > frame's full text including the JSON inside it.
5. Turn 4 — arm (c), schema inheritance on explicit follow-up (new user
   prompt):

   > Call delegate_send with `to` set to DLG1, max_wait_ms 60000, and
   > this message: "Follow-up: report a structured result with verdict
   > resumed and count 21." Report the full result verbatim — both the
   > text the tool printed and, if your client exposes it, the
   > structured tool state. Then end your turn.
6. Read the child transcripts and the durable delegate journal — NOT
   `jobs.jsonl`, which carries no delegate generation:
   `find ~/.local/state/evener/projects -path "*sessions/$SID/delegates.jsonl"`.

## Expected

- Arm (a): DLG1's terminal frame on the parent's rail is
  `<delegate-notification delegate_id="<DLG1>">{...}</delegate-notification>`
  — the tag carries the DELEGATE id and no job identity, and its body is
  the terminal packet marshalled whole
  (`agent/delegate_delivery.go#delegateNotificationContent`). That JSON
  has `"structured_result":{"verdict":"ok","count":7}` and
  `"structured_result_valid":true`, and no `structured_result_reason`.
  The root's `delegates.jsonl` carries the same pair durably — on the
  `delegate_terminal_prepared` event for DLG1's generation, under
  `terminal_prepared.packet`. **Not** on `delegate_run_finished`: a
  normally-reported delegate finishes through the settling branch, which
  passes a nil packet, so its `run_finished.packet` is absent entirely
  (`agent/delegate_tree_finish.go#delegateTreeController.FinishGeneration`
  — only the stopping branch attaches one, and that packet is the
  stopped-by-parent boilerplate with no structured result). Match on the
  prepared event, or accept either event within the generation; asserting
  the pair on `run_finished` alone fails a healthy run. The child's
  own transcript, read through the `transcript_ref` creation returned,
  shows the `communicate` call it made. Falsification: `structured_result`
  absent from the frame, or carrying the default communicate envelope
  keys (`message`/`data`/`artifacts`) instead of the schema's own fields.
- Arm (b): DLG2's frame shows the delegate TURN ended normally — the
  packet's `"kind"` is `"reported"`, not `"terminal_error"`, and the
  delegate's outcome is `completed`
  (`agent/internal/delegatestore/record.go#PacketKind` has exactly those
  two values) — while the structured fields report the violation:
  `"structured_result"` RETAINED with the invalid payload exactly as the
  child sent it (`count` still the string `"banana"`),
  `"structured_result_valid":false`, and
  `"structured_result_reason":"schema_validation_failed"` alongside it
  (`agent/subagents.go#captureDelegateStructuredResult` captures the
  payload first and only then downgrades `valid` on the schema check;
  retention of the invalid payload is pinned by
  `agent/delegate_resource_runtime_test.go#TestDelegateResourceRuntime_InvalidStructuredResultIsBoundedAndExplained`). The prose
  report is still there as the packet's `message`. `delegates.jsonl` carries
  the same retained payload and durable valid/reason pair for DLG2 on its
  `delegate_terminal_prepared` event, for the reason given under arm (a) —
  `delegate_run_finished` carries no packet on this path.
  <!-- pin: the reason vocabulary is implementation-defined
       machine-readable text; shipped values today are
       schema_validation_failed / schema_result_missing /
       schema_result_too_large / schema_capture_failed. If the child
       dodges the instruction by never producing a structured payload,
       schema_result_missing is the honest report for THAT run —
       valid:false + populated reason is the normative assertion,
       the specific reason names the failure mode. -->
- Falsification (silent coercion): `structured_result_valid` `true` with
  `count` coerced to a number, or `true` over the unmodified string
  payload — validation is decorative. Also falsifying (silent drop): the
  `structured_result` key ABSENT even though the child's invalid payload
  reached capture — honest reporting retains the payload and marks it
  invalid; only the missing/oversized/marshal-failure reasons
  legitimately omit it.
- Falsification (catastrophic honesty failure): the violating delegate's
  outcome is `failed` solely because the schema failed — schema
  validation describes the RESULT, not the lifecycle.
- Arm (c): the `delegate_send` result names the SAME `delegate_id` as
  turn 1 and carries no job identity of any kind — the stable result has
  no `job_id`, `started_job_id` or `current_job_id` field to carry one
  (`agent/session_tools_jobs.go#marshalDelegateSendResult`). Ask for at
  most 60000 ms: `clampJobBlockTimeout` caps any inline wait at
  `maxJobBlockTimeoutMS` (`agent/session_tools_jobs.go#clampJobBlockTimeout`),
  so a larger number silently buys nothing. With the wait satisfied in
  time it reads `action` `"completed"`
  and `running_in_background` false, and the text the tool printed ends
  with a `[delegate_id <DLG1> · completed · completed]` footer and a
  trailing `structured_result (valid=true): {"verdict":"resumed","count":21}`
  line (`agent/session_tools_jobs.go#formatDelegateSend`). Those are the
  schema's own top-level keys, which can only appear if the follow-up
  turn inherited the original conversation's `result_schema` — no schema
  was passed on the send.
  If the wait expires first the result reads `action` `"started"` /
  `status` `running` / `running_in_background` true and prints no
  structured result at all; that is an unfinished wait, so read the
  terminal frame when it arrives rather than scoring inheritance as
  dropped.
  Falsification: the follow-up result reverts to the default
  `message`/`data`/`artifacts` envelope, or `structured_result` is
  entirely absent from a follow-up that DID complete — inheritance
  dropped.
- Arms (a) and (b) deliver exactly one terminal notification each
  (format asserted in job-notification-semantics.md, not here).

## Cleanup

- All delegate generations are terminal by design. Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- **`structured_result_reason` is not in the text a `delegate_send`
  result prints.** `formatDelegateSend` renders the report, a bracket
  footer, warnings and a `structured_result (valid=<bool>)` line — and
  stops; the reason rides only the structured tool state alongside it
  (`agent/session_tools_jobs.go#delegateSendResult`). So arm (b)'s reason
  assertion must be read from the terminal notification frame's JSON or
  from `delegates.jsonl`, never from what a send printed. Asking a runner
  for a field the formatter omits is how a card gets an invented answer.
- Arm (b) asks a model to misbehave on purpose; strong models
  sometimes "helpfully" comply with the schema anyway. If the read
  shows valid:true with count 7-style coercion BY THE CHILD (it sent a
  real integer), that run is inconclusive — rerun with the instruction
  emphasized rather than recording a validation bug. The transcript of
  the child (its `transcript_ref`) shows what the child actually sent.
- The schema replaces the child's communicate `output` parameter
  schema wholesale, and evener validates at TWO layers: the tool
  registry's call-time args-schema check
  (`agent/internal/tool/registry.go` "tool args schema validation
  failed", which rejects the invalid call with an `is_error` tool
  result before anything is captured) and the capture-time
  structured-result check (the valid/reason triplet arm (b) asserts).
  Arm (b)'s capture-time signature is therefore masked BY DESIGN
  whenever the call-time gate is in the path — an invalid payload
  never reaches capture. Observed live (2026-06-12, `gpt-5.5`): the
  child emitted `count:"banana"`, the registry rejected it with
  `expected integer, but got string`, and the child retried with a
  VALID payload (`count:0`) → `valid:true`. That run shape is
  inconclusive for arm (b) — record it as the call-time-gate variant,
  not a validation bug. Provider-side strict enforcement is a second
  potential masking layer ABOVE the registry (the model never emits
  the call at all; final reason `schema_result_missing`) — record
  that as the provider-enforcement variant. The capture-time triplet
  remains normative for any payload that reaches capture invalid; that
  path is unit-covered
  (`agent/delegate_resource_runtime_test.go#TestDelegateResourceRuntime_InvalidStructuredResultIsBoundedAndExplained`).
- Arm (c) must wait for the arm-(a) run to be terminal. Creation is
  asynchronous, so nothing about turn 1 proves it: DLG1 is terminal once
  its notification frame has rendered, which is what turn 2 establishes.
  `job_status(target=<DLG1>)` orients you (`type`, lifecycle `status`,
  `transcript_ref`) but is NOT the way to learn a result — its own
  description says delegate status "never returns terminal packet
  contents" and "Completion is notification-driven; do not poll this
  waiting for completed". Do **not** expect a busy-refusal failure if
  you race it: `docs/job-control.md`'s reason vocabulary has no code
  for this case (kata xmag retired the busy-session code it used to
  name here, once it was clear no Go source emitted it), and a
  `delegate_send` aimed at a delegate that is still running
  live-steers it instead.
  That result is addressed by `delegate_id`, not `job_id` — the wire
  result carries `delegate_id`, `type`, `status` and `action:"steered"`
  and exposes no job identity at all
  (`agent/session_tools_jobs.go#marshalDelegateSendResult`, which is
  also where `sendMessageResult.Target` is dropped: the delegate you
  addressed is the `to` you sent, not a field that comes back). Expect
  `action:"steered"` against the same `delegate_id` you sent to, and no
  successor generation.
  The behaviour is pinned by
  `agent/delegate_resource_tools_test.go#TestStableDelegateTools_LiveSteerRejectsIgnoredWait`,
  which asserts the `wait_ignored_reason` a live steer reports; note it
  does NOT assert `action` or the delegate identity, so do not read it as
  a pin on the whole result shape. Scoring a steer as a failure here
  would be scoring shipped behaviour as a bug.
- `structured_result` larger than the persistence cap downgrades to
  valid:false + `schema_result_too_large`; keep test payloads tiny so
  the size path never triggers here.
