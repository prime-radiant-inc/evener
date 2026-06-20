# sidecar-progress-digest-output-match: progress digest sidecar summarizes a milestone

**What this covers**: long-running job concierge behavior. The sidecar
wakes on a meaningful milestone instead of periodic heartbeat noise.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-progress-digest-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the progress digest sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are PROGRESS_DIGESTER. First turn communicate exactly
   >    DIGEST_READY. Later, for a Watch frame, extract delivery_id and
   >    job_id, call job_read_output on that job_id, and if output
   >    contains PROGRESS_MILESTONE, delegate_send to caller exactly
   >    PROGRESS_DIGEST delivery=<delivery_id> milestone=build-green
   >    next=integration-tests. Then communicate exactly
   >    DIGEST_RECORDED delivery=<delivery_id>. For nonmatching frames,
   >    return bare DIGEST_IGNORED and use no extra tools."
   > 2. Start a background shell job:
   >    `sh -c 'echo booting; sleep 2; echo PROGRESS_MILESTONE build=green; sleep 20'`.
   >    Capture the job_id.
   > 3. After DIGEST_READY, create a `job_watch` on that job with
   >    output_match "PROGRESS_MILESTONE", send to the observer with
   >    include_excerpt true and message "Summarize milestone." Capture
   >    watch_id.
   > 4. Wait for PROGRESS_DIGEST if needed, clear the watch, stop the
   >    shell job if still running, and communicate exactly
   >    SCENARIO_DONE progress-digest.

## Expected

- The observer reports one concise `PROGRESS_DIGEST`.
- The observer reads the watched job through the grant.
- The parent does not poll repeatedly with `job_list` after the watch
  is in place.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
```

## Sharp edges

- This scenario is milestone-based. Do not convert it to a periodic
  progress-interval watch unless the behavior under test changes.

