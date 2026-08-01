# sidecar-stuckness-read-file-error: stuckness observer wakes only on read_file errors

**What this covers**: non-progress detection from failed tool calls.
The sidecar should wake on an error status filter and report a
stuckness note without waking on successful reads. Driving mechanism:
`delegate(watch_parent:true)` + observer-installed
`job_watch(source:"parent", events:["assistant.tool"], event_filter:
{...})` + the observer's terminal `communicate(end_turn:true)` callback
— see `docs/job-control.md` "Observer and sidecar composition" and the
reference card `job-watch-observer-snide-thread.md`.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-stuckness-error-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the stuckness sidecar scenario.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are STUCKNESS_WATCHER. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['assistant.tool'], event_filter {\"tool_name\":\"read_file\",
   >    \"status\":\"error\"}. Then communicate exactly STUCK_READY and
   >    finish. When later resumed with a message containing 'Watch
   >    frame', read the delivery_id and the frame's event block. If
   >    that block reads kind assistant.tool, tool_name read_file,
   >    status error, finish with communicate end_turn true and message
   >    exactly STUCK_ALERT delivery=<delivery_id> tool=read_file
   >    reason=missing-input. For any other frame, finish with
   >    communicate end_turn true and message exactly STUCK_IGNORED.
   >    Use no other tools."
   > 2. After the delegate result reports `watching: true`, capture the
   >    watch_id from its `watches` entry, then attempt to read
   >    `missing-input.txt`; the error is expected.
   > 3. When the STUCK_ALERT observer callback arrives, call `job_watch`
   >    with operation "clear" and that watch_id, then communicate
   >    exactly SCENARIO_DONE stuckness-error.

## Expected

- The readiness delegate result reports `watching: true` and lists the
  observer's watch under `watches` with the `event_filter` echoed —
  the OBSERVER owns the watch, the parent installs nothing.
- The watch condition includes `status=error`.
- The missing `read_file` event delivers one frame, and that frame's
  `event:` block carries `kind: assistant.tool`, `tool_name:
  read_file`, `status: error`, and the `error:` text
  (`writeAssistantToolWatchEvent`, `agent/job_watch.go:4874`) — the
  observer decides from the frame, with no audit tools.
- The observer emits one `STUCK_ALERT` carrying that frame's
  delivery_id, as its terminal `communicate(end_turn=true)`.
- No broad `assistant.tool` watch is used: nothing the parent does
  before the failing read produces a delivery.

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
- The observer must install its watch BEFORE communicating readiness,
  or the readiness result cannot report `watching: true` and the parent
  has no watch_id to clear.
- `event_filter` drops non-matching events before any delivery is
  recorded (`docs/job-control.md` "`job_watch`" "Non-matching events
  do not create a delivery, pending row, notification, or observer
  wake"), so a filtered-out tool call
  leaves no pending row to assert on — the negative here is an absence
  in `serf-doctor watches`, bounded by the positive alert.
- The alert and the record collapse into one call: the observer's
  terminal `communicate(end_turn=true)` IS the callback
  (`docs/job-control.md` "`job_watch`" "That terminal communicate is
  the callback to the parent").

