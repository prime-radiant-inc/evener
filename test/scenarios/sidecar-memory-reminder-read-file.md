# sidecar-memory-reminder-read-file: memory sidecar reminds on a successful read_file frame

**What this covers**: memory/context injection as a passive observer.
The sidecar watches only successful `read_file` tool results and sends
a scoped reminder when a project rule appears. Driving mechanism:
`delegate(watch_parent:true)` + observer-installed
`job_watch(source:"parent", events:["assistant.tool"], event_filter:
{...})` + the observer's terminal `communicate(end_turn:true)` callback
— see `docs/job-control.md` "Observer and sidecar composition" and the
reference card `job-watch-observer-snide-thread.md`.

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
   > 1. Call `delegate` with `agent_type` "subagent", `watch_parent`
   >    true, and `max_wait_ms` 120000. Its task is: "You are
   >    MEMORY_SIDECAR. First: call job_watch with operation 'create',
   >    source 'parent', events ['assistant.tool'], event_filter
   >    {\"tool_name\":\"read_file\",\"status\":\"ok\"}. Then call the
   >    communicate tool with exactly MEMORY_READY and finish. When
   >    later resumed with a message containing 'Watch frame', inspect
   >    the frame's event block. If it refers to memory.md and its
   >    output includes PROJECT_RULE: no-force-push, finish with
   >    communicate end_turn true and message exactly MEMORY_REMINDER
   >    rule=no-force-push. For unrelated Watch frames, finish with
   >    communicate end_turn true and message exactly MEMORY_IGNORED."
   > 2. After the delegate result reports `watching: true` and
   >    MEMORY_READY, capture the watch_id from its `watches` entry,
   >    then read `memory.md`.
   > 3. When the observer callback arrives, call `job_watch` with
   >    operation "clear" and that watch_id, then call the communicate
   >    tool with exactly SCENARIO_DONE memory-reminder.
   >
   > Use the delegate result, file-read result, and observer callback as
   > the happy-path signals. Diagnostic job or transcript inspection is
   > only for recovering from an actual error.

## Expected

- The observer installs the watch itself, before it reports
  `MEMORY_READY`; the readiness delegate result carries `watching:
  true` and the watch under `watches`. The parent never calls
  `job_watch(operation="create")`.
- The condition is `events: [assistant.tool] where tool_name=read_file,
  status=ok`.
- The delivered frame's `event:` block carries `tool_name: read_file`,
  `status: ok`, the `arguments_json` naming `memory.md`, and the
  `output:` containing `PROJECT_RULE: no-force-push` — everything the
  observer needs without an audit tool
  (`writeAssistantToolWatchEvent`, `agent/job_watch.go:4874`).
- The observer emits `MEMORY_REMINDER` as its terminal
  `communicate(end_turn=true)`, which reaches the parent as an
  `Observer callback:` block.
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
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count communicate
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send  # expect 0
```

## Sharp edges

- The readiness race the old parent-installed shape had is gone by
  construction: the observer installs its own watch inside its first
  turn, so `watching: true` on the readiness result IS the proof the
  watch predates the parent's `read_file`. If the parent reads
  `memory.md` before that result comes back, the frame can be missed —
  re-run rather than reinterpreting.
- `watch_parent:true` is what puts `job_watch` in an explicit
  `agent_type:"subagent"` tool set (`agent/subagents.go:534-540`).
  Without it the observer's first call fails `source_not_watchable:
  source parent requires delegate(watch_parent=true)`. `delegate_send`
  is not needed at all — the callback is the observer's own terminal
  `communicate`, so a fluent observer transcript shows
  `delegate_send: 0 calls`.
- `include_excerpt` no longer exists on any watch shape. A model that
  reaches for it gets an `additionalProperties` schema error; the
  repair is to drop it, since assistant.tool frames already carry the
  tool `output`.
- The reminder and the record collapse into one call: the terminal
  `communicate(end_turn=true)` IS the callback
  (`docs/job-control.md:1190`).
