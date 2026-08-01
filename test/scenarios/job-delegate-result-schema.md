# job-delegate-result-schema: valid results validate, violations report honestly, resumes inherit the schema

**What this covers**: `delegate.result_schema` end to end
(`docs/job-control.md` § `delegate` `result_schema` rule and
§ "Reading job output"). (a) A schema-backed delegate that complies
returns `structured_result` with `structured_result_valid: true` —
inline on a foreground delegate and again on a later
`read_transcript(transcript_ref="job:<job_id>")`; (b) a deliberately
schema-violating result is reported honestly: no structured result,
and a machine-readable `structured_result_reason` in its place; (c) a
resumed turn in the same delegate conversation inherits the ORIGINAL
`result_schema` although `delegate_send` has no schema argument.
Delegate `status="completed"` never asserts task success — the
structured fields are how the parent judges outcome.

## Pre-state

- Fresh binaries from the branch under test; an isolated hub
  (`docs/agentic-testing.md` setup checklist); credentialed model that
  follows deliberate-misbehavior test instructions.
- `tmpdir=$(mktemp -d -t serf-e2e-jschema-XXXXX)`.
- The schema used throughout:
  `{"type":"object","properties":{"verdict":{"type":"string"},"count":{"type":"integer"}},"required":["verdict","count"]}`.

## Steps

1. Spawn a session via `/api/spawn` with `working_dir=$tmpdir`.
   Capture `SID`.
2. Turn 1 — arm (a), compliant foreground delegate:

   > Call delegate with max_wait_ms 120000,
   > result_schema
   > `{"type":"object","properties":{"verdict":{"type":"string"},"count":{"type":"integer"}},"required":["verdict","count"]}`,
   > and this task: "Report a structured result with verdict ok and
   > count 7, with a one-line summary message." Report the full result
   > JSON verbatim. Then call read_transcript with transcript_ref
   > "job:<the returned job_id>" and report that full JSON verbatim too.
3. Turn 2 — arm (b), deliberate violation (new user prompt):

   > Call delegate (background default) with the SAME result_schema
   > and this exact task: "This is a schema-violation test. In your
   > final structured output, set verdict to the string bad and set
   > count to the STRING value banana — a string, deliberately NOT a
   > number. Do not correct the type; the test needs the invalid
   > payload." Report the job_id, then end your turn; when its
   > completion notification arrives, call read_transcript with
   > transcript_ref "job:<that job_id>" and report the full JSON verbatim.
4. Turn 3 — arm (c), schema inheritance on explicit follow-up (new user prompt):

   > Call delegate_send with `to` set to the turn-1 delegate_id,
   > `on_idle` "start", and this message: "Follow-up: report a
   > structured result with verdict resumed and count 21." Report the
   > full result JSON verbatim, then end your turn; when the started
   > job's completion notification arrives, call read_transcript with
   > transcript_ref "job:<the returned current_job_id>" and report the
   > full JSON verbatim.
5. Read the transcript and the durable log
   (`find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"`).

## Expected

- Arm (a) inline: the delegate result has `status` `"completed"`,
  `structured_result` equal to `{"verdict":"ok","count":7}`, and
  `structured_result_valid` `true`. The follow-up `job:` read exposes
  the SAME three facts in its `content` — `- status: completed` and a
  trailing `structured_result (valid=true):
  {"verdict":"ok","count":7}` line — plus the prose report in the
  fenced block. Falsification: the `structured_result` line absent, or
  carrying the default communicate envelope keys (`message`/`data`/
  `artifacts`) instead of the schema's own fields.
- Arm (b): the read of the violating job shows `- status: completed`
  (the delegate TURN ended normally) while the structured fields
  report the violation: NO `structured_result (valid=...)` line at all,
  and a `structured_result_reason: schema_validation_failed` line in
  its place. The prose report is still readable in the fenced block.
  `jobs.jsonl`'s `job_finished` for that job carries the durable
  valid/reason pair (durable, not recomputed per read).
  <!-- pin: the reason vocabulary is implementation-defined
       machine-readable text; shipped values today are
       schema_validation_failed / schema_result_missing /
       schema_result_too_large / schema_capture_failed. If the child
       dodges the instruction by never producing a structured payload,
       schema_result_missing is the honest report for THAT run —
       valid:false + populated reason is the normative assertion,
       the specific reason names the failure mode. -->
- Falsification (silent coercion): `structured_result (valid=true)`
  with `count` coerced to a number, or a structured result rendered at
  all despite the type violation — validation is decorative.
- Falsification (catastrophic honesty failure): the violating job
  reports `status` `"failed"` solely because the schema failed —
  schema validation describes the RESULT, not the lifecycle.
- Arm (c): the `delegate_send` result has `action` `"started"`, a NEW
  `started_job_id`/`current_job_id`, and the same delegate_id as the
  turn-1 result.
  The read of the new job shows `structured_result (valid=true):
  {"verdict":"resumed","count":21}` — the schema's own top-level keys,
  which can only appear if the resumed turn inherited the original
  conversation's `result_schema` (no schema was passed on follow-up).
  Falsification: the resumed result reverts to the default
  `message`/`data`/`artifacts` envelope, or the structured result is
  entirely absent from the started follow-up job — inheritance dropped.
- Both background jobs (arms b, c) deliver exactly one terminal
  notification each (format asserted in job-notification-semantics.md,
  not here).

## Cleanup

- All delegate jobs are terminal by design. Shut down the session
  (`POST /s/$SID/shutdown`); `rm -rf "$tmpdir"`.

## Sharp edges

- Arm (b) asks a model to misbehave on purpose; strong models
  sometimes "helpfully" comply with the schema anyway. If the read
  shows valid:true with count 7-style coercion BY THE CHILD (it sent a
  real integer), that run is inconclusive — rerun with the instruction
  emphasized rather than recording a validation bug. The transcript of
  the child (its `transcript_ref`) shows what the child actually sent.
- The schema replaces the child's communicate `output` parameter
  schema wholesale, and serf validates at TWO layers: the tool
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
  path is unit-covered (`agent/job_delegate_test.go`).
- Arm (c) must wait for the arm-(a) job to be terminal (it is — the
  foreground call returned completed). Do **not** expect a
  `delegate_session_busy` failure if you race it: that error is
  contract-legal (`docs/job-control.md` "Job status and reason model"
  "Canonical synchronous errors include"; "`delegate_send`" "fails
  synchronously with `delegate_session_busy`") but nothing in the Go
  source emits it. A `delegate_send` aimed at a delegate whose job is
  still running live-steers that job instead, returning
  `action:"steered"` on the running `job_id` — pinned by
  `agent/job_delegate_send_test.go:889-899`, which fails outright if the
  error string appears. Scoring a steer as a failure here would be
  scoring shipped behaviour as a bug.
- `structured_result` larger than the persistence cap downgrades to
  valid:false + `schema_result_too_large`; keep test payloads tiny so
  the size path never triggers here.
