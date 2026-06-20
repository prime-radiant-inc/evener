# sidecar-stuckness-read-file-error: stuckness observer wakes only on read_file errors

**What this covers**: non-progress detection from failed tool calls.
The sidecar should wake on an error status filter and report a
stuckness note without waking on successful reads.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-stuckness-error-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the stuckness sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are STUCKNESS_WATCHER. First turn communicate exactly
   >    STUCK_READY. Later, for a Watch frame with assistant.tool
   >    read_file status error, extract delivery_id and delegate_send
   >    to caller exactly STUCK_ALERT delivery=<delivery_id>
   >    tool=read_file reason=missing-input. Then communicate exactly
   >    STUCK_RECORDED delivery=<delivery_id>. For other frames, return
   >    bare STUCK_IGNORED and use no tools."
   > 2. After STUCK_READY, create a `job_watch` on target `caller`,
   >    events ["assistant.tool"], event_filter
   >    {"tool_name":"read_file","status":"error"}, send to the
   >    observer with message "Stuckness check.". Capture watch_id.
   > 3. Attempt to read `missing-input.txt`; the error is expected.
   > 4. Wait for STUCK_ALERT if needed, clear the watch, and
   >    communicate exactly SCENARIO_DONE stuckness-error.

## Expected

- The watch condition includes `status=error`.
- The missing `read_file` event delivers one frame.
- The observer emits `STUCK_ALERT` and `STUCK_RECORDED` for the same
  delivery id.
- No broad `assistant.tool` watch is used.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
```

## Sharp edges

- The parent may retry the missing read. Treat duplicate failing
  reads as a fluency note unless they cause duplicate alerts or an
  uncleared active watch.

