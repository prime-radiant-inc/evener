# sidecar-artifact-freshness-communicate: artifact freshness sidecar catches a missing final draft

**What this covers**: the quality/artifact observer use case where a
sidecar checks whether a referenced final artifact exists or is stale.
The trigger is a caller `communicate` frame that carries the artifact
path.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-artifact-freshness-XXXXX)`.
- Do not create `$tmpdir/missing-report.md`.
- Use `kimi/kimi-for-coding` for Kimi fluency validation.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`.
2. Prompt:

   > Run the artifact freshness sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are ARTIFACT_FRESHNESS. First turn communicate exactly
   >    ARTIFACT_READY. Later, for a Watch frame whose event.message
   >    contains FINAL_DRAFT, extract delivery_id and the artifact
   >    path. If the path contains missing-report.md, delegate_send to
   >    caller exactly ARTIFACT_ALERT delivery=<delivery_id>
   >    path=missing-report.md status=missing. Then communicate exactly
   >    ARTIFACT_RECORDED delivery=<delivery_id>. For nonmatching
   >    frames, return bare ARTIFACT_IGNORED and use no tools."
   > 2. After ARTIFACT_READY, create a `job_watch` on target `caller`,
   >    events ["communicate"], send to the observer with message
   >    "Artifact freshness check.". Capture watch_id.
   > 3. Communicate exactly FINAL_DRAFT artifact=missing-report.md.
   > 4. Wait for ARTIFACT_ALERT if needed, clear the watch, and
   >    communicate exactly SCENARIO_DONE artifact-freshness.

## Expected

- The watch uses `communicate`; `assistant.message` is not a public
  watch event.
- The observer emits `ARTIFACT_ALERT` without trying to edit or create
  the missing artifact.
- Nonmatching frames, if any, are ignored with bare assistant text and
  no tools.
- The watch is cleared and has no dropped deliveries.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count job_list
```

## Sharp edges

- This intentionally checks observation/reporting, not artifact repair.
  A sidecar that starts fixing the file is out of scope.
