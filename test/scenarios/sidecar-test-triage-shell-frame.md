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
  `agent/jobs_nested.go#resolveDescendantJobOwner`, walks down only). This card's observer
  needs an ANCESTOR's job. Jesse ruled on 2026-07-30 that ancestor-job
  `output_match` access is NOT to be added. That ruling stands, and this
  card is repaired against the existing mechanism rather than around it.
- A terminal `job.notification` frame is not a delegate completion
  subscription and is not a read grant. Direct delegate completion arrives
  as a `delegate-notification` carrying the delegate's session transcript;
  `job:` transcript refs are for shell output only. This card therefore
  uses the existing `assistant.tool` frame, whose bounded output carries the
  failure signature.
- What DOES carry the failing text across the session boundary is an
  `assistant.tool` frame: it includes the tool's own `output` (or
  `error`) up to 1000 characters
  (`agent/job_watch.go#writeAssistantToolWatchEvent`). So the parent
  runs the failing command in
  the FOREGROUND and the signature rides the frame. The trigger is
  weaker than a live mid-stream match — it lands when the command
  returns, not when the line prints — which is the tradeoff the ruling
  already accepted.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t evener-e2e-test-triage-XXXXX)`.

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
  parent receives it as an `<delegate-notification>` block.
- The observer does not attempt to edit files or rerun tests.
- Falsification (boundary): the observer does not attempt to read or control
  a parent-owned job. The frame's bounded `output` is the triage evidence;
  shell `job:` refs and job-control operations are not a delegate completion
  handoff mechanism.
- Falsification (dead trigger): a `job_watch` create with
  `output_match` against a parent-owned job_id fails
  `target_not_found` — the observer's own store has no such job. If it
  installs, ancestor `output_match` access was added against the
  2026-07-30 ruling; stop and check with Jesse before treating that as
  a pass.

## Doctor audit

```bash
go run ./cmd/evener doctor watches "$SID"
go run ./cmd/evener doctor tree "$SID" --observers
go run ./cmd/evener doctor transcript "$SID" --format outline --range last:30
go run ./cmd/evener doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/evener doctor transcript "$OBSERVER_REF" --count communicate
```

## Sharp edges

- The command must run in the FOREGROUND. A `mode: "background"` shell
  call returns a job handle immediately, so the `assistant.tool` frame
  carries the handle rather than the failure text, and the output only
  ever exists inside a job this observer never gains a grant on — an
  `assistant.tool` delivery mints none.
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
