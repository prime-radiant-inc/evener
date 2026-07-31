# sidecar-feedback-governor-communicate: feedback governor warns on a loop trigger

**What this covers**: cost/quota and non-progress governance. The
sidecar watches caller communications for an explicit loop trigger and
reports a concise intervention note. Driving mechanism:
`delegate(watch_parent:true)` + observer-installed
`job_watch(source:"parent")` + the observer's terminal
`communicate(end_turn:true)` callback — see `docs/job-control.md`
"Observer and sidecar composition" and the reference card
`job-watch-observer-snide-thread.md`.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-feedback-governor-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the feedback governor sidecar scenario.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are LOOP_GOVERNOR. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['communicate']. Then communicate exactly GOVERNOR_READY and
   >    finish. When later resumed with a message containing 'Watch
   >    frame' whose event message contains LOOP_TRIGGER, read the
   >    delivery_id from the frame and finish with communicate end_turn
   >    true, message exactly LOOP_GOVERNOR_ALERT delivery=<delivery_id>
   >    pattern=repeated-tool-choice recommendation=change-approach. For
   >    nonmatching frames, finish with communicate end_turn true and
   >    message exactly LOOP_IGNORED. Use no other tools."
   > 2. After the delegate result reports `watching: true`, capture the
   >    watch_id from its `watches` entry, then communicate exactly
   >    LOOP_TRIGGER tool=read_file repeats=3.
   > 3. When the LOOP_GOVERNOR_ALERT observer callback arrives, call
   >    `job_watch` with operation "clear" and that watch_id, then
   >    communicate exactly SCENARIO_DONE feedback-governor.

## Expected

- The readiness delegate result reports `watching: true` and lists the
  observer's watch under `watches` — the OBSERVER owns the watch.
- The registered watch is `events: [communicate]` on source `parent`.
- The observer reports `LOOP_GOVERNOR_ALERT` once, as an `Observer
  callback:` block from its terminal `communicate(end_turn=true)`.
- The observer does not start a debate with the parent; it reports and
  stops.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
```

## Sharp edges

- As with the drift detector, `assistant.message` is not a public watch
  event. The sidecar should observe the explicit loop signal through
  `communicate`.
- The observer must install its watch BEFORE communicating readiness,
  or the readiness result cannot report `watching: true`.
- The alert and the record collapse into one call: the observer's
  terminal `communicate(end_turn=true)` IS the callback
  (`docs/job-control.md:1190`).
- The parent's acknowledgement of the callback is itself a
  `communicate` event and re-fires the watch. Bounded by the
  self-influence breaker; clear the watch rather than reading the extra
  `LOOP_IGNORED` as a failure.
