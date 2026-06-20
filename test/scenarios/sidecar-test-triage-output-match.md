# sidecar-test-triage-output-match: test triage sidecar reads a failing background job

**What this covers**: test failure triage. A background command emits a
failure signature, an `output_match` watch wakes the observer, and the
observer reads the watched job through the watch read grant.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-test-triage-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the test triage sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are TEST_TRIAGER. First turn communicate exactly
   >    TRIAGE_READY. Later, for a Watch frame, extract delivery_id and
   >    job_id, call job_read_output on that job_id, and if output
   >    contains TEST_FAIL_SIGNATURE, delegate_send to caller exactly
   >    TRIAGE_NOTE delivery=<delivery_id> failing=TestFoo
   >    diagnosis=assertion-mismatch. Then communicate exactly
   >    TRIAGE_RECORDED delivery=<delivery_id>. For nonmatching frames,
   >    return bare TRIAGE_IGNORED and use no extra tools."
   > 2. Start a background shell job:
   >    `sh -c 'sleep 2; echo TEST_FAIL_SIGNATURE TestFoo expected=1 got=0; sleep 20'`.
   >    Capture the shell job_id.
   > 3. After TRIAGE_READY, create a `job_watch` on the shell job with
   >    output_match "TEST_FAIL_SIGNATURE", send to the observer with
   >    include_excerpt true and message "Triage this failure." Capture
   >    watch_id.
   > 4. Wait for TRIAGE_NOTE if needed, clear the watch, stop the shell
   >    job if still running, and communicate exactly SCENARIO_DONE
   >    test-triage.

## Expected

- The observer's `job_read_output` against the parent-owned shell job
  succeeds.
- The parent receives `TRIAGE_NOTE`.
- The watch has one meaningful delivered output-match frame and no
  dropped deliveries.
- The observer does not attempt to edit files or rerun tests.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count job_read_output
```

## Sharp edges

- The shell job intentionally sleeps after printing so cleanup can
  exercise `job_stop`.

