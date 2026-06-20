# sidecar-quality-auditor-communicate: quality auditor flags a TODO in a deliverable draft

**What this covers**: quality auditor for deliverables. The sidecar
sees a content-bearing draft frame and reports a severity-ranked
finding without becoming a second author.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-quality-auditor-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the quality auditor sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are QUALITY_AUDITOR. First turn communicate exactly
   >    QUALITY_READY. Later, for a Watch frame whose event.message
   >    contains DELIVERABLE_DRAFT and TODO, extract delivery_id and
   >    delegate_send to caller exactly QUALITY_FINDING
   >    delivery=<delivery_id> severity=medium
   >    issue=todo-left-in-draft. Then communicate exactly
   >    QUALITY_RECORDED delivery=<delivery_id>. For nonmatching frames,
   >    return bare QUALITY_IGNORED and use no tools."
   > 2. After QUALITY_READY, create a `job_watch` on target `caller`,
   >    events ["communicate"], send to the observer with message
   >    "Quality audit check.". Capture watch_id.
   > 3. Communicate exactly DELIVERABLE_DRAFT title=client-report
   >    body='All tests pass. TODO add risk section.'
   > 4. Wait for QUALITY_FINDING if needed, clear the watch, and
   >    communicate exactly SCENARIO_DONE quality-auditor.

## Expected

- The watch uses `communicate` and receives the full draft message.
- The observer emits `QUALITY_FINDING` and `QUALITY_RECORDED`.
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

