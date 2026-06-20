# sidecar-handoff-packager-job-notification: handoff sidecar packages a completed delegate result

**What this covers**: workflow handoff packaging. The parent starts a
worker delegate and an observer sidecar; a `job.notification` watch
for the worker completion wakes the observer to read and package the
handoff.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-handoff-packager-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the handoff packager sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are HANDOFF_PACKAGER. First turn communicate exactly
   >    HANDOFF_READY. Later, for a Watch frame with job.notification,
   >    extract delivery_id and job_id, call job_read_output on that
   >    job_id, and if output contains HANDOFF_WORKER_RESULT,
   >    delegate_send to caller exactly HANDOFF_PACKAGE
   >    delivery=<delivery_id> owner=release-team artifact=summary.
   >    Then communicate exactly HANDOFF_RECORDED delivery=<delivery_id>.
   >    For nonmatching frames, return bare HANDOFF_IGNORED and use no
   >    extra tools."
   > 2. Start a worker delegate with task: "Communicate exactly
   >    HANDOFF_WORKER_RESULT artifact=summary status=ready."
   >    Capture the worker job_id.
   > 3. After HANDOFF_READY, create a `job_watch` on target equal to
   >    the worker job_id, events ["job.notification"], send to the
   >    observer with message "Package this handoff.". Capture watch_id.
   > 4. Wait for HANDOFF_PACKAGE if needed, clear the watch, and
   >    communicate exactly SCENARIO_DONE handoff-packager.

## Expected

- The observer reads the completed worker job output successfully.
- The parent receives `HANDOFF_PACKAGE`.
- The tree shows both a worker delegate and an observer delegate.
- The watch condition is `events: [job.notification]`.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:40
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count job_read_output
```

## Sharp edges

- If the worker finishes before the watch is created, the event may not
  be observed. Keep the worker simple but ensure the watch is attached
  before relying on the completion notification.

