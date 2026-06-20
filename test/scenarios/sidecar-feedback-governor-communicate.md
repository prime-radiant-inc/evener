# sidecar-feedback-governor-communicate: feedback governor warns on a loop trigger

**What this covers**: cost/quota and non-progress governance. The
sidecar watches caller communications for an explicit loop trigger and
reports a concise intervention note.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-feedback-governor-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the feedback governor sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are LOOP_GOVERNOR. First turn communicate exactly
   >    GOVERNOR_READY. Later, for a Watch frame whose event.message
   >    contains LOOP_TRIGGER, extract delivery_id and delegate_send to
   >    caller exactly LOOP_GOVERNOR_ALERT delivery=<delivery_id>
   >    pattern=repeated-tool-choice recommendation=change-approach.
   >    Then communicate exactly LOOP_RECORDED delivery=<delivery_id>.
   >    For nonmatching frames, return bare LOOP_IGNORED and use no
   >    tools."
   > 2. After GOVERNOR_READY, create a `job_watch` on target `caller`,
   >    events ["communicate"], send to the observer with message
   >    "Loop governor check.". Capture watch_id.
   > 3. Communicate exactly LOOP_TRIGGER tool=read_file repeats=3.
   > 4. Wait for LOOP_GOVERNOR_ALERT if needed, clear the watch, and
   >    communicate exactly SCENARIO_DONE feedback-governor.

## Expected

- The registered watch is `events: [communicate]`.
- The observer reports `LOOP_GOVERNOR_ALERT` once.
- The observer does not start a debate with the parent; it reports and
  records.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
```

## Sharp edges

- As with the drift detector, `assistant.message` is the wrong trigger.
  It can make the sidecar react to the parent's internal tool-call
  turns instead of the explicit loop signal.

