# sidecar-quality-auditor-communicate: quality auditor flags a TODO in a deliverable draft

**What this covers**: quality auditor for deliverables. The sidecar
sees a content-bearing draft frame and reports a severity-ranked
finding without becoming a second author. Driving mechanism:
`delegate(watch_parent:true)` + observer-installed
`job_watch(source:"parent")` + the observer's terminal
`communicate(end_turn:true)` callback — see `docs/job-control.md`
"Observer and sidecar composition" and the reference card
`job-watch-observer-snide-thread.md`.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-quality-auditor-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the quality auditor sidecar scenario.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are QUALITY_AUDITOR. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['communicate']. Then communicate exactly QUALITY_READY and
   >    finish. When later resumed with a message containing 'Watch
   >    frame' whose event message contains DELIVERABLE_DRAFT and TODO,
   >    read the delivery_id from the frame and finish with communicate
   >    end_turn true, message exactly QUALITY_FINDING
   >    delivery=<delivery_id> severity=medium issue=todo-left-in-draft.
   >    For nonmatching frames, finish with communicate end_turn true
   >    and message exactly QUALITY_IGNORED. Use no other tools."
   > 2. After the delegate result reports `watching: true`, capture the
   >    watch_id from its `watches:` line, then communicate exactly
   >    DELIVERABLE_DRAFT title=client-report body='All tests pass. TODO
   >    add risk section.'
   > 3. When the QUALITY_FINDING observer callback arrives, call
   >    `job_watch` with operation "clear" and that watch_id, then
   >    communicate exactly SCENARIO_DONE quality-auditor.

## Expected

- The readiness delegate result reports `watching: true` and lists the
  observer's watch under `watches:` — the OBSERVER owns the watch.
- The watch uses `events: [communicate]` on source `parent`, and the
  delivered frame's `event:` block carries the full draft text in its
  `message:` field (that is what the observer audits — it never reads
  the parent's transcript).
- The observer emits `QUALITY_FINDING` as its terminal
  `communicate(end_turn=true)`, which the parent sees as an `Observer
  callback:` block.
- The observer does not edit the deliverable or run unrelated tools.
- The watch is cleared before completion.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send
```

## Sharp edges

- If the model stalls before the first observer delegate call, that is
  a Kimi fluency failure for this scenario, not a watch-runtime
  failure.
- The observer must install its watch BEFORE communicating readiness,
  or the readiness result cannot report `watching: true`.
- The finding and the record collapse into one call: the observer's
  terminal `communicate(end_turn=true)` IS the callback
  (`docs/job-control.md:1190`).
- The draft body is capped at 1000 characters inside the frame's
  `event: message:` field (`maxMessageChars` in
  `writeCommunicateWatchEvent`, `agent/job_watch.go:4844`), with
  `truncated: true` when it is cut. Keep the draft short so the TODO
  stays inside the window.

