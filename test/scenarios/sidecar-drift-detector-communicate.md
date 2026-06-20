# sidecar-drift-detector-communicate: drift detector flags scope creep from caller communication

**What this covers**: a scope-drift observer that should report only
when the main agent emits an explicit drift signal. This exercises
content-bearing `communicate` frames and the sidecar's default
observe/report posture.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-drift-detector-XXXXX)`.
- Use `kimi/kimi-for-coding` when validating the Kimi path.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`.
2. Prompt:

   > Run the drift detector sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are DRIFT_WATCHER. First turn communicate exactly
   >    DRIFT_READY. Later, for a Watch frame whose event.message
   >    contains DRIFT_SIGNAL, extract delivery_id and delegate_send to
   >    caller exactly DRIFT_ALERT delivery=<delivery_id>
   >    original=fix-one-test drift=rewrite-parser. Then communicate
   >    exactly DRIFT_RECORDED delivery=<delivery_id>. For nonmatching
   >    frames, return bare DRIFT_IGNORED and use no tools."
   > 2. After DRIFT_READY, create a `job_watch` on target `caller`,
   >    events ["communicate"], send to the observer with message
   >    "Drift check.". Capture watch_id.
   > 3. Communicate exactly DRIFT_SIGNAL original=fix-one-test
   >    new_scope=rewrite-parser.
   > 4. Wait for DRIFT_ALERT if needed, clear the watch, and
   >    communicate exactly SCENARIO_DONE drift-detector.

## Expected

- The registered watch condition is `events: [communicate]`.
- The observer emits one `DRIFT_ALERT` and one `DRIFT_RECORDED`.
- The observer does not call `job_list` or inspect transcripts to
  decide whether the frame is relevant.
- The parent clears the watch before finishing.

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
  That is the failure this card should catch.

