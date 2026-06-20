# sidecar-memory-reminder-read-file: memory sidecar reminds on a successful read_file frame

**What this covers**: memory/context injection as a passive observer.
The sidecar watches only successful `read_file` tool results and sends
a scoped reminder when a project rule appears.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-memory-reminder-XXXXX)`.
- Create the memory fixture:

  ```bash
  printf 'PROJECT_RULE: never use force push\n' > "$tmpdir/memory.md"
  ```

## Steps

1. Spawn a parent session with `working_dir=$tmpdir` and model
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the memory reminder sidecar scenario.
   > 1. Start observer delegate with `max_wait_ms` 120000 and task:
   >    "You are MEMORY_SIDECAR. First turn communicate exactly
   >    MEMORY_READY. Later, for a Watch frame with assistant.tool
   >    read_file output containing PROJECT_RULE, extract delivery_id
   >    and delegate_send to caller exactly MEMORY_REMINDER
   >    delivery=<delivery_id> rule=no-force-push. Then communicate
   >    exactly MEMORY_RECORDED delivery=<delivery_id>. For other
   >    frames, return bare MEMORY_IGNORED and use no tools."
   > 2. Wait until MEMORY_READY is complete. Then create a `job_watch`
   >    on target `caller`, events ["assistant.tool"], event_filter
   >    {"tool_name":"read_file","status":"ok"}, send to the observer
   >    with message "Memory read check.". Capture watch_id.
   > 3. Read `memory.md`.
   > 4. Wait for MEMORY_REMINDER if needed, clear the watch, and
   >    communicate exactly SCENARIO_DONE memory-reminder.

## Expected

- The watch is not created until after the observer's `MEMORY_READY`
  job is terminal.
- The condition is `events: [assistant.tool] where tool_name=read_file,
  status=ok`.
- The observer emits `MEMORY_REMINDER` and `MEMORY_RECORDED` for the
  same delivery id.
- The parent does not emit `SCENARIO_DONE` before the reminder exists.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send
```

## Sharp edges

- A prior Kimi run exposed a readiness race: the parent created the
  watch before the observer's first turn ended, so the real frame was
  consumed by setup behavior and the parent still declared done. That
  is a failure, even if `SCENARIO_DONE` appears.

