# job-watch-passive-observer-noop-filter: passive observer no-ops without tools; event_filter avoids needless wakeups

**What this covers**: the passive-observer sidecar failure path where
an observer is woken by an irrelevant `assistant.tool` frame and should
be allowed to finish with a plain no-action disposition, plus the
`assistant.tool` `event_filter` path that prevents irrelevant frames
from waking the observer at all. Contract anchors:
`docs/job-control.md:555` (`event_filter` drops non-matching events
before any delivery is recorded) and the observer composition at
`:1185-1191`. Driving mechanism: `delegate(watch_parent:true)` + two
successive observer-installed `job_watch(source:"parent")` configs +
terminal `communicate(end_turn:true)` callbacks.

## Pre-state

- Fresh binaries from the branch under test; an isolated hub
  (`docs/agentic-testing.md` setup checklist); credentialed model.
- `tmpdir=$(mktemp -d -t serf-e2e-passive-observer-XXXXX)`.
- Create the successful read target before spawning:

  ```bash
  printf 'PASSIVE_SUCCESS_SENTINEL\n' > "$tmpdir/read-target.txt"
  ```

## Steps

1. Spawn the parent session via `/api/spawn` with
   `working_dir=$tmpdir`. Capture `SID`.
2. Prompt the parent. Replace `$tmpdir` in the quoted prompt with the
   concrete temp directory path before sending:

   > Run this end-to-end passive observer probe. Follow the numbered
   > steps in order and report exact IDs in the final message.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are PASSIVE_OBSERVER. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['assistant.tool']. Then call communicate exactly PASSIVE_READY
   >    and finish. For any later input containing 'Watch frame': read
   >    delivery_id and the event block's kind and tool_name. If the
   >    kind is assistant.tool and tool_name is read_file, finish with
   >    communicate end_turn true and message exactly 'PASSIVE_READ_OK
   >    delivery=<delivery_id>'. For every other Watch frame, return a
   >    regular assistant message exactly PASSIVE_IGNORED_NO_ACTION and
   >    finish; do not call communicate and do not call any other tool
   >    for ignored frames. Never call job_list or shell. Do not start
   >    delegates of your own." Capture the observer delegate_id,
   >    current job_id, transcript_ref, and the broad_watch_id from the
   >    delegate result's `watches` entry.
   > 2. Call `job_list` with no filters. This is an irrelevant
   >    assistant.tool event for the broad watch.
   > 3. Call `job_watch` with operation "clear" and watch_id equal to
   >    broad_watch_id.
   > 4. Call `delegate_send` to the observer delegate_id with `on_idle`
   >    "start" and this message: "Now call job_watch with operation
   >    'create', source 'parent', events ['assistant.tool'],
   >    event_filter {\"tool_name\":\"read_file\",\"status\":\"ok\"},
   >    then communicate exactly PASSIVE_FILTERED_READY and finish."
   >    Capture filtered_watch_id from the observer's next result.
   > 5. Call `job_list` with no filters. It must not wake the filtered
   >    watch.
   > 6. Call `read_file` on `$tmpdir/missing-read-target.txt`. This
   >    error is expected; continue to the next step.
   > 7. Call `read_file` on `$tmpdir/read-target.txt`.
   > 8. After the observer's `PASSIVE_READ_OK` callback, call
   >    `job_watch` with operation "clear" and filtered_watch_id, then
   >    call communicate with exactly this single line:
   >    `PASSIVE_PARENT_DONE broad_watch=<broad_watch_id> filtered_watch=<filtered_watch_id> observer=<observer_delegate_id> transcript=<observer_transcript_ref>`

3. Poll `/api/sessions/local:$SID` until the parent is `idle`.
4. Find the parent transcript, observer transcript, and parent job log:

   ```bash
   find ~/.local/state/serf/projects -path "*sessions/$SID.transcript.jsonl"
   find ~/.local/state/serf/projects -path "*sessions/$SID/jobs.jsonl"
   ```

   The observer transcript path is the session id in the captured
   `transcript_ref`.
5. If the filtered watch remains active after the assertions, send a
   cleanup turn through `POST /api/sessions/local:$SID/send`
   (the legacy `/s/<id>/<action>` form-POSTs are gone —
   `cmd/serf-hub/web_workspace.go:20-22`, routes at
   `cmd/serf-hub/web_api_tree.go:1399,1415`):

   ```json
   {"text":"Cleanup only. Call job_watch with operation \"clear\" and watch_id \"<filtered_watch_id>\". Then call communicate with exactly PASSIVE_CLEANUP_DONE."}
   ```

## Expected

- The observer first turn completes with `PASSIVE_READY`, and the
  delegate result reports `watching: true` with the broad watch under
  `watches` — the OBSERVER installs both watches, the parent installs
  neither.
- The broad watch is accepted. The parent's own tool calls may fire it
  before the explicit `job_list`; those are still irrelevant
  `assistant.tool` frames and count for this card.
- For every broad-watch frame delivered to the observer, the observer
  transcript shows a plain assistant text response exactly
  `PASSIVE_IGNORED_NO_ACTION` and no tool calls in that assistant turn.
  Falsification: the observer calls `communicate`, `job_list`,
  `exec_command`, or any other harmless tool just to acknowledge an
  ignored frame.
- The broad watch is cleared before the filtered watch is created.
- The filtered watch result has `event_filter` exactly
  `tool_name=read_file,status=ok`.
- The filtered watch does not record a delivery for the step-6
  `job_list`.
- The step-6 missing-file `read_file` returns a tool error, and the
  filtered watch still does not record a delivery for that failed
  `read_file`.
- The step-7 successful `read_file` records exactly one filtered
  watch delivery, and its frame's event block contains:
  - `kind: assistant.tool`
  - `tool_name: read_file`
  - `status: ok`
  - `arguments_json` naming the file
  - `output:` carrying `PASSIVE_SUCCESS_SENTINEL`
- The observer's filtered-frame turn is a single terminal
  `communicate(end_turn=true)` carrying `PASSIVE_READ_OK
  delivery=<delivery_id>`, which the parent receives as an `Observer
  callback:` block. It should not emit visible assistant text in that
  turn. `delegate_send(to="caller")` is NOT the callback path any more
  and is a hard `invalid_request`
  (`agent/session_tools_jobs.go:163`).
- The parent may stop after consuming the `PASSIVE_READ_OK` callback
  instead of emitting `PASSIVE_PARENT_DONE`. Do not use that final
  marker as pass/fail evidence; use the durable `jobs.jsonl` and
  observer transcript.
- The parent `jobs.jsonl` shows no `watch_send_dropped` for either
  watch.

## Manual Inspection Recipe

Use `serf-doctor` rather than a one-off transcript or jobs parser.
Replace the selectors with the parent session id and observer
transcript ref from the run:

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:40
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count communicate
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count delegate_send  # expect 0
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count job_list
```

The parent watch report should show the broad watch ended as
`cleared`, the filtered watch condition as `events: [assistant.tool]
where tool_name=read_file, status=ok`, no dropped sends, and no
self-loop verdict. Both watches are listed on the PARENT's report even
though the observer created them — a parent-source watch is installed
into the parent's own job manager (`parentInstallWatch`,
`agent/session_tools_jobs.go:218`). The observer outline should show
ignored broad frames as bare assistant text with no tool calls, and the
successful filtered `read_file` frame as a single `communicate`.

## Cleanup

- Clear any active watch ids from the run.
- Shut down the parent session:
  `POST /api/sessions/local:$SID/shutdown`.
- Remove the hermetic workdir.

## Sharp Edges

- A broad `assistant.tool` watch can fire on its own `job_watch`
  creation result before the explicit probe tool. Do not assert a
  single broad delivery.
- A small model may inspect the watch after creation, which creates
  another broad `job_watch` frame. This is acceptable if the observer
  no-ops without tools.
- An observer callback is itself a live parent input. The parent is
  allowed to end after processing it; the durable logs are the
  assertion surface.
- Do not check raw final assistant prose for this card. Check
  `jobs.jsonl` delivery rows and the observer transcript.
- The LLM-facing `job_watch` create result should include the
  `watch_id`. If the parent needs `job_watch(operation="list")` only
  to recover an id it just created, treat that as a tool-output
  ergonomics regression.
- After the successful `read_file`, the parent may inspect delegate
  jobs before emitting the final marker. That is acceptable; the
  fluency signal to watch is whether it finds the filtered observer job
  without human steering.
- If the filtered observer emits explanatory assistant text before the
  `delegate_send`/`communicate` tool calls, treat that as prompt
  noncompliance, not a watch-delivery failure.

## Recorded Run

**Both runs below predate the 2026-06-22 tool simplification
(`9d0d777c6`) and are NOT reproducible as captured**: they drove the
card's old parent-installed `target`/`send` shape and the observer's
`delegate_send(to="caller")` reply, neither of which exists now. They
are kept for the fluency observations, which still apply — a run
against the rewritten steps above is not expected to match them
call-for-call.

Verified live on 2026-06-20 against `kimi/kimi-for-coding` using a
fresh hub on `127.0.0.1:9187`.

- Parent session: `01KVHFSX8KG4ZZB9YSWA5VE6HR`
- Observer transcript: `local:01KVHFT2M0R6M19DW0WYG5XYAG`
- Broad watch: `watch_01KVHFTBVC8H19YBR83TNGJFA7`; 3 unique
  irrelevant deliveries (`job_watch`, `job_watch`, `job_list`), all
  answered with bare `PASSIVE_IGNORED_NO_ACTION` and zero tool calls.
- Filtered watch: `watch_01KVHFTZ45VX841TVSY9H5QD8P`; 1 unique
  delivery, only for successful `read_file`; no delivery for `job_list`
  or failed `read_file`.
- Fluency note: the observer was clean and direct. The parent followed
  the ordered steps without human steering, but it needed a watch-list
  call to recover watch ids and briefly inspected an older observer job
  before finding the filtered observer job. The watch-list detour was
  traced to `job_watch` create surfacing `watch_id` only through
  `tool_state`, not model-facing text; future runs should not need
  that recovery step.
- Both watches were cleared and the session was shut down after the
  run.

Verified again after surfacing `watch_id` in the model-facing
`job_watch` output.

- Parent session: `01KVHKVE52QN6MN7MNHM63PT0N`
- Observer transcript: `local:01KVHKVN1B0G6292KWE8PJ7AWJ`
- Parent captured both watch ids directly from create results:
  `[watching caller · watch_id ...]`; it did not call
  `job_watch(operation="list")` to recover ids.
- Parent still used `job_list` and `job_read_output` after successful
  `read_file` to wait for the async observer job. That was fluent
  enough: it found the correct running observer job without human
  steering.
- Observer ignored the broad `job_watch` frame with bare
  `PASSIVE_IGNORED_NO_ACTION` and zero tool calls. The filtered
  `read_file` frame correctly called `delegate_send` and `communicate`,
  but also emitted a small explanatory text preface; the prompt above
  was tightened after this run to forbid that.
- Both watches were cleared and the session was shut down after the
  run.
