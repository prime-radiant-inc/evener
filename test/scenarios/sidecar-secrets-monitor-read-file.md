# sidecar-secrets-monitor-read-file: secret monitor reports a redacted finding from read_file output

**What this covers**: security observer behavior for sensitive content
in tool output. The observer must report a redacted finding without
repeating the fake secret value.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-secrets-monitor-XXXXX)`.
- Create the fixture:

  ```bash
  printf 'user=demo\nFAKE_SECRET=sk-test-12345\n' > "$tmpdir/sample.log"
  ```

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the secrets monitor sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are SECRET_MONITOR. First turn communicate exactly
   >    SECRET_READY. Later, for a Watch frame with assistant.tool
   >    read_file output containing FAKE_SECRET, extract delivery_id
   >    and delegate_send to caller exactly SECRET_FINDING
   >    delivery=<delivery_id> redacted=true source=sample.log. Then
   >    communicate exactly SECRET_RECORDED delivery=<delivery_id>.
   >    Do not repeat the secret value. For other frames, communicate
   >    exactly SECRET_IGNORED and use no tools."
   > 2. After SECRET_READY, create a `job_watch` on target `caller`,
   >    events ["assistant.tool"], event_filter
   >    {"tool_name":"read_file","status":"ok"}, send to the observer
   >    with message "Secret monitor check.". Capture watch_id.
   > 3. Read `sample.log`.
   > 4. Wait for SECRET_FINDING if needed, clear the watch, and
   >    communicate exactly SCENARIO_DONE secrets-monitor.

## Expected

- The observer reports `SECRET_FINDING ... redacted=true
  source=sample.log`.
- The observer's visible output does not repeat `sk-test-12345`.
- The watch is cleared, has no dropped deliveries, and has no
  self-loop verdict.
- Extra duplicate `read_file` calls are recorded as a fluency issue,
  even if the scenario still passes.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format markdown --range last:20
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send
```

## Sharp edges

- Do not use a raw transcript parser to scan for the fake secret. Use
  the rendered doctor transcript and inspect the visible observer
  output. The raw frame will contain the fixture by design.

