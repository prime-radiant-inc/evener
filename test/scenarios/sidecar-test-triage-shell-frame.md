# sidecar-test-triage-shell-frame: test triage sidecar diagnoses a failure signature from the frame

**What this covers**: test failure triage. A command emits a failure
signature, the signature reaches the observer inside an `assistant.tool`
watch frame, and the observer returns a diagnosis without touching the
parent's jobs. Driving mechanism: `delegate(watch_parent:true)` +
observer-installed `job_watch(source:"parent", events:["assistant.tool"],
event_filter:{"tool_name":"shell"})` + the observer's terminal
`communicate(end_turn:true)` callback — see `docs/job-control.md`
"Observer and sidecar composition".

**Renamed** from `sidecar-test-triage-output-match.md`: it no longer
uses `output_match`, and a name that says otherwise is the same drift
this rewrite exists to remove. Why it cannot:

- `output_match` requires a concrete `job_id` the watcher owns or whose
  owner is a live DESCENDANT (`resolveDescendantJobOwner`,
  `agent/jobs_nested.go:312`, walks down only). This card's observer
  needs an ANCESTOR's job. Jesse ruled on 2026-07-30 that ancestor-job
  `output_match` access is NOT to be added. That ruling stands, and this
  card is repaired against the existing mechanism rather than around it.
- The ruling's suggested repair — wake on `job.notification`, then read
  the referenced job through a frame-granted `job_read_output` — is not
  reachable either, for two reasons proven live and pinned by
  `sidecar-handoff-packager-job-notification.md`: session-source watches
  mint no read grant, and `job_read_output` was retired from the
  model-facing tool registry entirely (commit `cf84923c6`, 2026-06-23).
  A `job.notification` frame carries `status` and `output_bytes`, never
  the failing text.
- What DOES carry the failing text across the session boundary is an
  `assistant.tool` frame: it includes the tool's own `output` (or
  `error`) up to 1000 characters (`writeAssistantToolWatchEvent`,
  `agent/job_watch.go:4874`). So the parent runs the failing command in
  the FOREGROUND and the signature rides the frame. The trigger is
  weaker than a live mid-stream match — it lands when the command
  returns, not when the line prints — which is the tradeoff the ruling
  already accepted.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-test-triage-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the test triage sidecar scenario.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are TEST_TRIAGER. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['assistant.tool'], event_filter {\"tool_name\":\"shell\"}. Then
   >    communicate exactly TRIAGE_READY and finish. When later resumed
   >    with a message containing 'Watch frame', read the delivery_id
   >    and the frame's event block. If its output contains
   >    TEST_FAIL_SIGNATURE, finish with communicate end_turn true and
   >    message exactly TRIAGE_NOTE delivery=<delivery_id>
   >    failing=TestFoo diagnosis=assertion-mismatch. For any other
   >    frame, finish with communicate end_turn true and message exactly
   >    TRIAGE_IGNORED. Use no other tools."
   > 2. After the delegate result reports `watching: true`, capture the
   >    watch_id from its `watches` entry, then run the shell tool in
   >    the FOREGROUND with command
   >    `printf 'TEST_FAIL_SIGNATURE TestFoo expected=1 got=0\n'`.
   > 3. When the TRIAGE_NOTE observer callback arrives, call `job_watch`
   >    with operation "clear" and that watch_id, then communicate
   >    exactly SCENARIO_DONE test-triage.

## Expected

- The readiness delegate result reports `watching: true` and lists the
  observer's watch with `source: "parent"` and condition
  `events: [assistant.tool] where tool_name=shell`.
- The failing command delivers exactly one frame, and its `event:` block
  carries `kind: assistant.tool`, `tool_name: shell`, a `status`, the
  `arguments_json` holding the command, and an `output:` containing
  `TEST_FAIL_SIGNATURE TestFoo expected=1 got=0`. That output IS the
  triage evidence — the observer needs no audit tool and has no access
  to one.
- The observer returns exactly one `TRIAGE_NOTE` carrying that frame's
  `delivery_id`, through its terminal `communicate(end_turn=true)`; the
  parent receives it as an `Observer callback:` block.
- The observer does not attempt to edit files or rerun tests.
- Falsification (boundary): the observer resolves the parent's job at
  all. `job_status(job_id=<a parent job>)` from the observer must fail
  `job "job_..." not found — use job_list to see this session's jobs`,
  and `job_read_output` must not exist. Success on either means
  cross-session observer reads were added and this card's premise needs
  re-deriving.
- Falsification (dead trigger): a `job_watch` create with
  `output_match` against a parent-owned job_id fails
  `target_not_found` — the observer's own store has no such job. If it
  installs, ancestor `output_match` access was added against the
  2026-07-30 ruling; stop and check with Jesse before treating that as
  a pass.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count communicate
```

## Sharp edges

- The command must run in the FOREGROUND. A `background: true` shell
  call returns a job handle immediately, so the `assistant.tool` frame
  carries the handle rather than the failure text, and the output only
  ever exists inside a job the observer cannot read.
- Keep the signature short: the frame's `output:` is capped at 1000
  characters and flagged `output_truncated: true` past that.
- `event_filter` with `tool_name` but no `status` matches both `ok` and
  `error`. A `printf` exits zero, so the frame's status is `ok` — do not
  assert `status: error` here.
- The observer must install its watch BEFORE communicating readiness, or
  the readiness result cannot report `watching: true` and the parent has
  no watch_id to clear.
- Clear the watch as soon as the note arrives: the parent's own
  acknowledgement is another tool round, and a broad `assistant.tool`
  watch keeps waking the observer until the 50-delivery budget
  auto-clears it.
