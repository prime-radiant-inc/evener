# sidecar-memory-reminder-read-file: memory sidecar reminds on a successful read_file frame

**What this covers**: memory/context injection as a passive observer.
The sidecar watches only successful `read_file` tool results and sends
a scoped reminder when a project rule appears.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-memory-reminder-XXXXX)`.
- Create the memory fixture:

  ```bash
  printf 'PROJECT_RULE: no-force-push\n' > "$tmpdir/memory.md"
  ```

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`. Run at least one
   pass with `kimi/kimi-for-coding`; repeat with
   `openai/gpt-5.4-mini` when that model is available.
2. Prompt:

   > Run the memory reminder sidecar scenario.
   > 1. Start an observer delegate with `agent_type` "subagent" and
   >    `max_wait_ms` 120000. Its task is: "You are MEMORY_SIDECAR.
   >    First turn call the communicate tool with exactly
   >    MEMORY_READY. Later, when a Watch frame arrives for a
   >    successful assistant.tool read_file event, inspect the
   >    delivered frame. If it refers to memory.md and the content
   >    includes PROJECT_RULE: no-force-push, call
   >    delegate_send(to=\"caller\") with exactly MEMORY_REMINDER
   >    rule=no-force-push, then call the communicate tool with exactly
   >    MEMORY_RECORDED. For unrelated Watch frames, call the
   >    communicate tool with exactly MEMORY_IGNORED."
   > 2. After MEMORY_READY is returned by the delegate call, create a
   >    `job_watch` on target "caller" for events ["assistant.tool"]
   >    with event_filter {"tool_name":"read_file","status":"ok"} and
   >    send.to set to the observer delegate_id.
   > 3. In the response after the watch creation result, read
   >    `memory.md`.
   > 4. When the observer callback arrives, call the communicate tool
   >    with exactly SCENARIO_DONE memory-reminder.
   >
   > Use the delegate result, watch result, file-read result, and
   > observer callback as the happy-path signals. Diagnostic job or
   > transcript inspection is only for recovering from an actual error.

## Expected

- The watch is not created until after the observer's `MEMORY_READY`
  job is terminal.
- The condition is `events: [assistant.tool] where tool_name=read_file,
  status=ok`.
- The observer emits `MEMORY_REMINDER` with `delegate_send(to="caller")`
  and records `MEMORY_RECORDED` with `communicate`.
- The parent does not emit `SCENARIO_DONE` before the reminder exists.
- The parent does not use `job_list` or `job_read_output` as a waiting
  mechanism before the callback.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$SID" --count job_list
go run ./cmd/serf-doctor transcript "$SID" --count job_read_output
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send
```

## Sharp edges

- A prior Kimi run exposed a readiness race: the parent created the
  watch before the observer's first turn ended, so the real frame was
  consumed by setup behavior and the parent still declared done. That
  is a failure, even if `SCENARIO_DONE` appears.
- Another Kimi run exposed a tool-surface bug: explicit
  `agent_type:"subagent"` did not include `delegate_send`, so the
  observer could not call back. A fluent run should show
  `delegate_send: 1 call` in the observer transcript.
- GPT-5.4 mini may include a harmless repair if it asks for
  `include_excerpt` on a caller-session watch. The repair is acceptable
  when the next watch creation succeeds before the watched `read_file`
  is triggered.
