# job-watch-passive-observer-noop-filter: passive observer no-ops without tools; event_filter avoids needless wakeups

**What this covers**: the passive-observer sidecar failure path where
an observer is woken by an irrelevant `assistant.tool` frame and should
be allowed to finish with a plain no-action disposition, plus the
`assistant.tool` `event_filter` path that prevents irrelevant frames
from waking the observer at all. Contract anchors:
`docs/job-control.md` `event_filter`, observer delivery with
`send.to=<delegate_id>`, and the passive sidecar guidance that no-op
watch turns do not need a harmless tool call.

## Pre-state

- Fresh binaries from the branch under test; hub on `127.0.0.1:9180`
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
   > 1. Call `delegate` with `max_wait_ms` 120000 and this exact task:
   >    "You are PASSIVE_OBSERVER. First turn: call communicate exactly
   >    PASSIVE_READY and finish. For any later user input containing
   >    'Watch frame': read delivery_id and the event kind/tool_name if
   >    present. If the frame event kind is assistant.tool and tool_name
   >    is read_file, call delegate_send with to exactly 'caller' and
   >    message exactly 'PASSIVE_READ_OK delivery=<delivery_id>'; then
   >    call communicate exactly 'PASSIVE_READ_DONE
   >    delivery=<delivery_id>' and finish. For every other Watch frame,
   >    return a regular assistant message exactly
   >    PASSIVE_IGNORED_NO_ACTION and finish; do not call communicate
   >    and do not call any other tool for ignored frames. Never call
   >    job_list or exec_command. Do not start delegates of your own."
   >    Capture the observer delegate_id, current job_id, and
   >    transcript_ref.
   > 2. Call `job_watch` with operation "create", target "caller",
   >    events ["assistant.tool"], send {to: the observer delegate_id,
   >    message: "Passive broad check."}. Capture broad_watch_id.
   > 3. Call `job_list` with no filters. This is an irrelevant
   >    assistant.tool event for the broad watch.
   > 4. Call `job_watch` with operation "clear" and watch_id equal to
   >    broad_watch_id.
   > 5. Call `job_watch` with operation "create", target "caller",
   >    events ["assistant.tool"], event_filter {"tool_name":"read_file",
   >    "status":"ok"}, send {to: the same observer delegate_id,
   >    message: "Passive filtered read check."}. Capture
   >    filtered_watch_id.
   > 6. Call `job_list` with no filters. It must not wake the filtered
   >    watch.
   > 7. Call `read_file` on `$tmpdir/missing-read-target.txt`. This
   >    error is expected; continue to the next step.
   > 8. Call `read_file` on `$tmpdir/read-target.txt`.
   > 9. If control returns after the observer's `PASSIVE_READ_OK`
   >    steering, call communicate with exactly this single line:
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
   cleanup turn through `POST /s/$SID/send`:

   ```json
   {"text":"Cleanup only. Call job_watch with operation \"clear\" and watch_id \"<filtered_watch_id>\". Then call communicate with exactly PASSIVE_CLEANUP_DONE."}
   ```

## Expected

- The observer first turn completes with `PASSIVE_READY`.
- The broad watch is accepted. Its own `job_watch` setup/inspection
  calls may also fire before the explicit `job_list`; those are still
  irrelevant `assistant.tool` frames and count for this card.
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
- The step-7 missing-file `read_file` returns a tool error, and the
  filtered watch still does not record a delivery for that failed
  `read_file`.
- The step-8 successful `read_file` records exactly one filtered
  watch delivery, and its frame contains:
  - `event:`
  - `kind: assistant.tool`
  - `tool_name: read_file`
  - the `PASSIVE_SUCCESS_SENTINEL` output
- The observer's filtered-frame turn calls `delegate_send(to="caller")`
  with `PASSIVE_READ_OK delivery=<delivery_id>` and then communicates
  `PASSIVE_READ_DONE delivery=<same delivery_id>`.
- The parent may stop after consuming the `PASSIVE_READ_OK` steering
  instead of emitting `PASSIVE_PARENT_DONE`. Do not use that final
  marker as pass/fail evidence; use the durable `jobs.jsonl` and
  observer transcript.
- The parent `jobs.jsonl` shows no `watch_send_dropped` for either
  watch.

## Manual Inspection Recipe

Use a short parser rather than eyeballing full JSONL entries. Replace
the paths and watch ids with the values from the run:

```bash
python3 - <<'PY'
import json, re
from pathlib import Path

jobs = Path("PARENT_JOBS_JSONL")
observer = Path("OBSERVER_TRANSCRIPT_JSONL")
watch_ids = {
    "broad": "BROAD_WATCH_ID",
    "filtered": "FILTERED_WATCH_ID",
}

for label, wid in watch_ids.items():
    deliveries = {}
    dropped = []
    condition = None
    cleared = None
    for line in jobs.open():
        rec = json.loads(line)
        if rec.get("kind") == "watch_registered" and rec.get("watch_id") == wid:
            condition = rec.get("watch", {}).get("condition")
        if rec.get("kind") == "watch_cleared" and rec.get("watch_id") == wid:
            cleared = rec.get("watch", {}).get("end_reason")
        if not rec.get("kind", "").startswith("watch_send_"):
            continue
        ws = rec.get("watch_send") or {}
        if (ws.get("key") or {}).get("watch_id") != wid:
            continue
        did = ws.get("delivery_id")
        item = deliveries.setdefault(did, {"events": set(), "tools": set()})
        item["events"].add(rec["kind"])
        m = re.search(r"^  tool_name: (.+)$", ws.get("frame", ""), re.M)
        if m:
            item["tools"].add(m.group(1))
        if rec.get("kind") == "watch_send_dropped":
            dropped.append(did)
    print(label, wid, condition, "cleared=", cleared)
    for did, item in deliveries.items():
        print(" ", did, sorted(item["events"]), sorted(item["tools"]))
    print(" unique_deliveries=", len(deliveries), "dropped=", dropped)

pending = None
for line in observer.open():
    rec = json.loads(line)
    turn = rec.get("turn") or {}
    msg = turn.get("message") or {}
    text = "\n".join(
        c.get("text", "")
        for c in msg.get("content", [])
        if c.get("kind") == "text"
    )
    if turn.get("kind") == "USER_INPUT" and "Watch frame" in text:
        wid = re.search(r"^watch_id: (.+)$", text, re.M)
        did = re.search(r"^delivery_id: (.+)$", text, re.M)
        tool = re.search(r"^  tool_name: (.+)$", text, re.M)
        pending = {
            "watch_id": wid.group(1) if wid else "",
            "delivery_id": did.group(1) if did else "",
            "tool": tool.group(1) if tool else "",
        }
    elif pending and turn.get("kind") == "ASSISTANT":
        calls = [
            (c.get("tool_call") or {}).get("name")
            for c in msg.get("content", [])
            if c.get("kind") == "tool_call"
        ]
        texts = [
            c.get("text", "")
            for c in msg.get("content", [])
            if c.get("kind") == "text"
        ]
        print("observer", pending, "text=", texts, "tool_calls=", calls)
        pending = None
PY
```

## Cleanup

- Clear any active watch ids from the run.
- Shut down the parent session: `POST /s/$SID/shutdown`.
- Remove the hermetic workdir.

## Sharp Edges

- A broad `assistant.tool` watch can fire on its own `job_watch`
  creation result before the explicit probe tool. Do not assert a
  single broad delivery.
- A small model may inspect the watch after creation, which creates
  another broad `job_watch` frame. This is acceptable if the observer
  no-ops without tools.
- Caller steering from `delegate_send(to="caller")` is itself a live
  parent input. The parent is allowed to end after processing it; the
  durable logs are the assertion surface.
- Do not check raw final assistant prose for this card. Check
  `jobs.jsonl` delivery rows and the observer transcript.
- The parent model may need an extra `job_watch(operation="list")`
  because the short rendered `job_watch` create result does not always
  include the watch id in the text it is reasoning over.
- After the successful `read_file`, the parent may inspect delegate
  jobs before emitting the final marker. That is acceptable; the
  fluency signal to watch is whether it finds the filtered observer job
  without human steering.

## Recorded Run

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
  before finding the filtered observer job. This is acceptable but not
  effortless.
- Both watches were cleared and the session was shut down after the
  run.
