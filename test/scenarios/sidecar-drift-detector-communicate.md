# sidecar-drift-detector-communicate: drift detector flags scope creep from caller communication

**What this covers**: a scope-drift observer that should report only
when the main agent emits an explicit drift signal. This exercises
content-bearing `communicate` frames and the sidecar's default
observe/report posture. Driving mechanism: `delegate(watch_parent:true)`
+ observer-installed `job_watch(source:"parent")` + the observer's
terminal `communicate(end_turn:true)` callback — see
`docs/job-control.md` "Observer and sidecar composition" and the
reference card `job-watch-observer-snide-thread.md`.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-drift-detector-XXXXX)`.
- Use `kimi/kimi-for-coding` when validating the Kimi path.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`.
2. Prompt:

   > Run the drift detector sidecar scenario.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are DRIFT_WATCHER. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['communicate']. Then communicate exactly DRIFT_READY and
   >    finish. When later resumed with a message containing 'Watch
   >    frame' whose event message contains DRIFT_SIGNAL, read the
   >    delivery_id from the frame and finish with communicate end_turn
   >    true, message exactly DRIFT_ALERT delivery=<delivery_id>
   >    original=fix-one-test drift=rewrite-parser. For nonmatching
   >    frames, finish with communicate end_turn true and message
   >    exactly DRIFT_IGNORED. Use no other tools."
   > 2. After the delegate result reports `watching: true`, capture the
   >    watch_id from its `watches:` line, then communicate exactly
   >    DRIFT_SIGNAL original=fix-one-test new_scope=rewrite-parser.
   > 3. When the DRIFT_ALERT observer callback arrives, call `job_watch`
   >    with operation "clear" and that watch_id, then communicate
   >    exactly SCENARIO_DONE drift-detector.

## Expected

- The readiness delegate result reports `watching: true` and lists the
  observer's watch under `watches:` — the OBSERVER owns the watch, the
  parent never installs one.
- The registered watch condition is `events: [communicate]` on source
  `parent`.
- The observer emits exactly one `DRIFT_ALERT`, and it arrives at the
  parent as an `Observer callback:` block (the observer's terminal
  `communicate(end_turn=true)`), not as a steering message.
- The observer does not call `job_list` or inspect transcripts to
  decide whether the frame is relevant.
- The parent clears the watch before finishing. The parent can clear it
  by id even though the observer created it: a parent-source watch is
  installed into the parent's own job manager
  (`parentInstallWatch`, `agent/session_tools_jobs.go:218`).

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
```

## Sharp edges

- A prior Kimi run first created an `assistant.message` watch, causing
  repeated `DRIFT_IGNORED` turns before it corrected to `communicate`.
  That historical wrong-trigger path should now be rejected as an
  invalid event selection; this card should catch whether the agent
  recovers by creating a `communicate` watch.
- The observer must install its watch BEFORE communicating readiness,
  or the readiness result cannot report `watching: true` and the parent
  has no watch_id to clear.
- The alert and the record collapse into one call now: the observer's
  terminal `communicate(end_turn=true)` IS the callback to the parent
  (`docs/job-control.md:1190`). A separate "recorded" message would be
  a second turn, not part of this scenario.
- The parent's own acknowledgement of the callback is itself a
  `communicate` event and re-fires the watch. That is expected and
  bounded by the self-influence breaker; clear the watch rather than
  treating the extra `DRIFT_IGNORED` as a failure.
