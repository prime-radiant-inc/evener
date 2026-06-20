# sidecar-runbook-capture-output-match: runbook sidecar captures a successful operational step

**What this covers**: knowledge capture and runbook building. The
observer wakes when a command output includes a runbook resolution
marker and drafts a source-linked runbook note.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-runbook-capture-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the runbook capture sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are RUNBOOK_SCRIBE. First turn communicate exactly
   >    RUNBOOK_READY. Later, for a Watch frame, extract delivery_id and
   >    job_id, call job_read_output on that job_id, and if output
   >    contains RUNBOOK_RESOLUTION, delegate_send to caller exactly
   >    RUNBOOK_DRAFT delivery=<delivery_id> step=restart-service
   >    resolution=healthy. Then communicate exactly RUNBOOK_RECORDED
   >    delivery=<delivery_id>. For nonmatching frames, return bare
   >    RUNBOOK_IGNORED and use no extra tools."
   > 2. Start a background shell job:
   >    `sh -c 'echo RUNBOOK_STEP restart-service; sleep 2; echo RUNBOOK_RESOLUTION healthy; sleep 20'`.
   >    Capture job_id.
   > 3. After RUNBOOK_READY, create a `job_watch` on that job with
   >    output_match "RUNBOOK_RESOLUTION", send to the observer with
   >    include_excerpt true and message "Capture runbook note." Capture
   >    watch_id.
   > 4. Wait for RUNBOOK_DRAFT if needed, clear the watch, stop the
   >    shell job if still running, and communicate exactly
   >    SCENARIO_DONE runbook-capture.

## Expected

- The observer sends `RUNBOOK_DRAFT`.
- The observer uses `job_read_output` once or minimally to inspect the
  watched job.
- The parent clears the watch and stops the long-tail shell job.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
```

## Sharp edges

- This should draft for human review only. It should not write an
  official runbook file.

