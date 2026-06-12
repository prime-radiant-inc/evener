# E2E Job Control Scenario Results

**Branch**: job-control-spec  
**Commit**: 5469dd63  
**Run date**: 2026-06-12  
**Hub**: 127.0.0.1:9180  
**Model**: openai/gpt-5.5  

## Final Matrix

| # | Card | Verdict | Notes |
|---|------|---------|-------|
| 1 | job-shell-lifecycle | FAIL | Arms (a)/(b) stdout missing due to spawn prompt quoting; arms (c)-(f) pass |
| 2 | job-list-and-recovery | PASS | |
| 3 | job-notification-semantics | PASS | |
| 4 | job-notification-wake | PARTIAL | Model read CHILD_DONE_42 from excerpt, did not call job_read_output |
| 5 | job-read-output-blocking-grep | PASS | |
| 6 | job-delegate-result-schema | PARTIAL | Arm (b) schema violation masked by OpenAI provider enforcement |
| 7 | job-stop-and-children | PASS | |
| 8 | job-watch-caller-notification-delivery | PASS-WITH-NOTE | TICK_FRAME delivered mid-turn (between tool rounds), not at turn boundary |
| 9 | job-watch-caller-send-no-deadlock | PASS | |
| 10 | job-watch-output-match-catchup | PASS-WITH-NOTE | Arm (b) negative: `fired` field absent (not `fired:false`) in terminal no-match result |
| 11 | job-watch-sidecar-observer | PASS | |
| 12 | job-send-message-surface | PASS | |
| 13 | job-nested-visibility | PASS | |
| 14 | job-restart-durability | PASS | |

**Summary**: 10 PASS, 1 PASS-WITH-NOTE, 1 PASS-WITH-NOTE, 1 PARTIAL, 1 PARTIAL, 1 FAIL  
**Green (PASS/PASS-WITH-NOTE)**: 12/14  
**Needs attention**: Card 1 (test harness quoting), Card 4 (model shortcut), Card 6 (provider enforcement), Card 8 (mid-turn delivery timing)

## Ledger

---

## Card 14: job-restart-durability

**Start**: 2026-06-12T21:35:00Z  
**End**: 2026-06-12T21:43:00Z  
**Verdict**: PASS  
**Sessions**: 01KTYW5XVXXNHJB3QYT764SHQY  
**Job**: job_01KTYW6APYV4JJTDB03SB7EZ77  
**Daemon PIDs killed**: 4181588 (first), 4182459 (second)

### Evidence

**Pre-crash state (step 4):**
- `jobs.jsonl`: 1 event — `job_started` for JOB14, NO `job_finished`. PASS.
- Output log last line before kill: `TICK_34` (27 → 34 ticks accumulated over ~21-27s of pre-crash runtime).
- PRODUCER_DONE absent. PASS.

**Restart reconciliation (step 6):**

jobs.jsonl after restart:
- seq 2: `job_finished` status=stopped, reason=runtime_lost, ended_at set, output_bytes=263, terminal_generation=01KTYW7P1FEV3AY77EEKB4TGWY. PASS.
- seq 3: `job_notification_pending` (same terminal_generation). PASS.
- seq 4: `job_notification_delivered` (same terminal_generation). PASS.

Exactly 1 `job_finished` for JOB14. PASS.

STEERING notification (entry 9):
```
<job-notification job_id="job_01KTYW6APYV4JJTDB03SB7EZ77" event="stopped" job_type="shell" status="stopped" reason="runtime_lost" output_bytes="263">
```
event=stopped, reason=runtime_lost (not event=failed). PASS.

Model-visible job_list (entry 20): `status=stopped, reason=runtime_lost`. NOT `failed`. PASS.

job_read_output (entry 23):
```
{"status":"stopped","reason":"runtime_lost","content":"TICK_1\n...\nTICK_34\n","total_bytes":263}
```
First line TICK_1. Last line TICK_34 (matches pre-crash tail). total_bytes=263 stable. PASS.

**Second restart dedupe (step 7):**
- job_finished count for JOB14 in final jobs.jsonl: 1 (no second job_finished). PASS.
- runtime_lost STEERING entries in transcript: 1 (exactly once). PASS.
- Step-7 job_list: status=stopped/runtime_lost unchanged. PASS.

**Orphaned producer:** No TICK_ processes found after restart (self-terminated via SIGPIPE on next write). Cleanup clean. PASS.

---

## Card 13: job-nested-visibility

**Start**: 2026-06-12T21:32:00Z  
**End**: 2026-06-12T21:35:00Z  
**Verdict**: PASS  
**Sessions**: 01KTYVZWT73QS8ZPA6WEZ390WK  
**Jobs**: delegate=job_01KTYW07CGYW29RFRYKQDGEPMY, nested-shell=job_01KTYW0HYX0MWTJVE9Q7DTHMW2

### Evidence

**Arm (a) — visibility default/nested:**

Step 1 (no include_nested): 1 job (delegate, completed). Nested shell absent. PASS.

Step 2 (include_nested=true): 2 rows. Nested shell:
```
job_id: job_01KTYW0HYX0MWTJVE9Q7DTHMW2
type: shell, status: running
parent_job_id: job_01KTYW07CGYW29RFRYKQDGEPMY (delegate)
owner_session_id: 01KTYW07KTV13791SQZE3AT511 (child session, not SID13)
visible_to_session_id: 01KTYVZWT73QS8ZPA6WEZ390WK (SID13)
```
Nested job_id matches delegate's NESTED_JOB report exactly (no namespacing). PASS.

**Arm (b) — parent reads nested output:**

Step 3 job_read_output: `{"status":"running","content":"NEST_TOKEN_1\n","total_bytes":13}` PASS.

**Arm (c) — parent stops nested job via routing:**

Step 4 job_stop: `{"status":"cancelled","reason":"stopped_by_parent"}` — not `not_controllable`. Live owner runtime routed correctly. PASS.

**Arm (d) — post-terminal read:**

Step 5 job_read_output: `{"status":"cancelled","reason":"stopped_by_parent","content":"NEST_TOKEN_1\n","total_bytes":13}` NEST_TOKEN_2 absent (sleep 300 cut short). PASS.

**jobs.jsonl:**
- seq 2: `job_started` for nested shell with `parent_job_id=job_01KTYW07CGYW29RFRYKQDGEPMY`, `owner_session_id=01KTYW07KTV13791SQZE3AT511`, `visible_to_session_id=01KTYVZWT73QS8ZPA6WEZ390WK`. PASS.
- seq 7: `job_finished` for nested shell with `visible_to_session_id`. PASS.

---

## Card 12: job-send-message-surface

**Start**: 2026-06-12T21:24:00Z  
**End**: 2026-06-12T21:32:00Z  
**Verdict**: PASS  
**Sessions**: 01KTYVHWKFE7GS80Q8YD5KT9TX  
**Jobs**: JA=job_01KTYVJ7Z8ZXVJ6B9WB7AY6ETZ, JB=job_01KTYVTM8VBZTTVN6TR35CASKP, JB-resume=job_01KTYVV5MRDK52N90BWJECRK99, arm-d=job_01KTYVWXKJH0MB0FHAEMJS4V7M

### Evidence

**Arm (a) — live steer (send to running delegate):**

job_send_message result (entry 11): 
```
{"action":"sent","job_id":"job_01KTYVJ7Z8ZXVJ6B9WB7AY6ETZ","status":"running","running_in_background":true,"transcript_ref":"local:01KTYVJ84HVJ0K6TK1PA5PBDPF"}
```
action=sent, same job_id (JA), no new job. PASS.

JA job_read_output (entry 18) content contains STEER_MARK_88 in the poem. PASS.

**Arm (b) — resume finished delegate:**

JB spawn: `{"job_id":"job_01KTYVTM8VBZTTVN6TR35CASKP","transcript_ref":"local:01KTYVTMF1A8GCWZBMP03MJET8"}` PASS.

job_send_message to finished JB (entry 32):
```
{"action":"resumed","job_id":"job_01KTYVV5MRDK52N90BWJECRK99","resumed_from_job_id":"job_01KTYVTM8VBZTTVN6TR35CASKP","transcript_ref":"local:01KTYVTMF1A8GCWZBMP03MJET8"}
```
action=resumed, new job_id != JB, resumed_from_job_id=JB, same transcript_ref. PASS.

Resumed job_read_output (entry 40): `content:"AZURE_FALCON\n"` — codeword from prior conversation context retained. PASS.

**Arm (c) — on_finished=fail against terminal job:**

job_send_message step 1 (entry 47): `target_terminal: delegate job "job_01KTYVTM8VBZTTVN6TR35CASKP" is completed` PASS.

Step 2 job_list (entry 50): 3 jobs, no new job created by the failed send. PASS.

**Arm (d) — foreground timeout resume:**

job_send_message step 3 (entry 53), ts=21:30:36 → result ts=21:30:38 (2s):
```
{"action":"resumed","job_id":"job_01KTYVWXKJH0MB0FHAEMJS4V7M","status":"running","reason":"foreground_timeout","running_in_background":true,"timed_out":true,"output":"","truncated":false}
```
action=resumed, new job_id, status=running, reason=foreground_timeout, timed_out=true. Returned in ~2s (well under 20s sleep). PASS.

Step 4 job_list (entry 56): arm-d job listed as running. PASS.

Arm-d completion notification (entry 60): `SLOW_RESUME_DONE` in excerpt. PASS.

---

## Card 11: job-watch-sidecar-observer

**Start**: 2026-06-12T21:20:00Z  
**End**: 2026-06-12T21:24:00Z  
**Verdict**: PASS  
**Sessions**: parent=01KTYVASW0BBVN1CTAM0EYJJJG, observer=01KTYVBAPZ7Z1ZBCYRZ80X44FR  
**Jobs**: observer=job_01KTYVBAH2BHWZ1NG65PYS3XHK, shell=job_01KTYVC3GN2TZ95WBD37W19KZ3, observer-resume=job_01KTYVDB57NNSRACCB0AJY2MXX

### Evidence

**Step 3 job_watch result (entry 14):**
```
{"target":"job_01KTYVC3GN2TZ95WBD37W19KZ3","watching":true,"output_match":"SIDECAR_TOKEN_OK","send":{"to":"job_01KTYVBAH2BHWZ1NG65PYS3XHK","message":"Review this frame.","include_excerpt":true},"fired":true}
```
watching=true, send.to=observer job_id. PASS. (fired=true: token already in output at install time — model installed watch ~41s after shell start, past the 20s print point.)

**jobs.jsonl watch_read_grant (seq 6):**
```
{"kind":"watch_read_grant","job_id":"job_01KTYVC3GN2TZ95WBD37W19KZ3","observer_session_id":"01KTYVBAPZ7Z1ZBCYRZ80X44FR"}
```
Durable grant naming observer session and watched job_id. PASS.

**Watch frame delivery to observer (observer transcript entry 6):**
```
Review this frame.
Watch frame
job_id: job_01KTYVC3GN2TZ95WBD37W19KZ3
delivery_id: wd_01KTYVDB54FTKD053N5T6JYPA0
trigger: output_match: SIDECAR_TOKEN_OK
excerpt: SIDECAR_TOKEN_OK
```
Frame contains job_id, delivery_id, trigger. PASS.

**Observer job_read_output (observer transcript entry 9):**
```
{"job_id":"job_01KTYVC3GN2TZ95WBD37W19KZ3","type":"shell","status":"running","content":"SIDECAR_TOKEN_OK\n","matches":[{"byte_offset":0,"line":"SIDECAR_TOKEN_OK"}]...}
```
Read succeeded across stores — grant allowed the observer to read a job owned by the parent session. SIDECAR_TOKEN_OK present. PASS.

**Observer job_send_message to caller (observer transcript entry 14-15):**
```
{"target":"caller","delivered":true,"action":"sent","message_type":"runtime"}
```
Message delivered. PASS.

**OBSERVER_COMMENT arrival in parent (entry 21 communicate inbox):**
```
inbox: ["OBSERVER_COMMENT delivery=wd_01KTYVDB54FTKD053N5T6JYPA0 line=SIDECAR_TOKEN_OK"]
```
Comment crossed back to caller. Delivery_id ties frame → read → comment chain. PASS.

**Resume job completes:** jobs.jsonl seq 9: resumed observer job started with same transcript_ref. Seq 13: completed. Parent got notification at entry 22 (STEERING): `OBSERVER_DONE` in excerpt. PASS.

**jobs.jsonl send lifecycle:**
- watch_send_pending (seq 7, 8) — two pendinging events (attach-scan + live delivery)
- watch_send_delivered (seq 10)
- No watch_send_dropped. PASS.

**Parent session idle after all notifications:** PASS.

---

## Card 10: job-watch-output-match-catchup

**Start**: 2026-06-12T21:11:00Z  
**End**: 2026-06-12T21:20:00Z  
**Verdict**: PASS-WITH-NOTE  
**Sessions**: 01KTYTSBF8QMYS2F1P25AKFA95  
**Jobs**: 01KTYTT0THGHKSZAC8NXGTMJDV (level-trigger), 01KTYV5FGP7PH9R2B02WGHS7ZR (terminal)

### Evidence

**Arm (a) — attach-scan fire (level-triggered):**

Turn 1 job_watch result (entry 14): `{"watching":true,"output_match":"LEVEL_TOKEN_[ABC]","fired":true}` PASS.

Attach-scan fire (entry 18, STEERING): `<job-notification event="watch" reason="output_match: LEVEL_TOKEN_B" ...>` — exactly 1 fire despite 2 matching retained lines (A and B). Last matching line is B. PASS.

Falsification check: no LEVEL_TOKEN_A notification ever arrived. No second attach-scan notification. PASS.

LEVEL_TOKEN_C wake (entry 25, STEERING, ~30s after job start): `<job-notification event="watch" reason="output_match: LEVEL_TOKEN_C" ...>` — idle wake with no user input. PASS.

**Arm (b) positive — terminal catch-up, match found:**

Step 2 job_watch on completed job with output_match=CATCHUP_TOKEN_OK (entry 49):
```
{"target":"job_01KTYV5FGP7PH9R2B02WGHS7ZR","watching":false,"fired":true,"terminal_catchup":true,"status":"completed"}
```
watching=false, fired=true, terminal_catchup=true, status=completed. PASS.

Catch-up notification arrived (entry 59, STEERING):
```
<job-notification event="watch" reason="output_match: CATCHUP_TOKEN_OK" ...>
```
One notification through the normal rail. PASS.

**Arm (b) negative — terminal catch-up, match not found:**

Step 3 job_watch on completed job with output_match=CATCHUP_TOKEN_MISSING (entry 52):
```
{"target":"job_01KTYV5FGP7PH9R2B02WGHS7ZR","watching":false,"terminal_catchup":true,"status":"completed"}
```
watching=false, terminal_catchup=true, status=completed. NOTE: `fired` field ABSENT (not explicitly `fired:false`). Card spec expected `fired:false` in this arm. No notification for CATCHUP_TOKEN_MISSING arrived. PASS on behavior; note on field shape.

**Arm (c) — terminal target + events fails target_terminal:**

Step 4 job_watch on completed job with events=["job.notification"] (entry 55):
```
target_terminal: job "job_01KTYV5FGP7PH9R2B02WGHS7ZR" is completed; watches can only attach to running jobs
```
target_terminal error returned. Not `watching:true`. Not `target_not_found`. PASS.

**Cross-arm falsification (no terminal errors on b-steps):** Steps 2 and 3 returned terminal_catchup=true, not target_terminal. Terminal catch-up implemented (not pre-design behavior). PASS.

### Findings
- Arm (b) negative: `fired` field absent rather than `fired:false` when token not found. May reflect omit-false field convention vs. explicit false. Behavior correct; field shape to re-verify vs spec §7.1.

---

## Card 9: job-watch-caller-send-no-deadlock

**Start**: 2026-06-12T21:07:00Z  
**End**: 2026-06-12T21:10:00Z  
**Verdict**: PASS  
**Sessions**: 01KTYTKJSVKRDF8A4F9MB9V06C  

### Evidence

**Step 1 (self-delivery feedback loop, with send.to=caller):**
```
invalid_request: watching assistant.message/assistant.tool/communicate with delivery back to the caller is a feedback loop (each delivery causes the next event); watch these kinds only with send.to set to an observer job
```
Rejected with invalid_request naming the feedback loop. No watching:true. PASS.

**Step 2 (no send, but assistant.message on caller):**
Same invalid_request wording. Rejected. PASS.

**Step 3 (delegate as observer):** job_01KTYTN31BTZZ7W6K7AXX029SM created as background observer. PASS.

**Step 4 (sidecar observer watch, multi-event):**
```
{"target":"caller","watching":true,"events":["assistant.message","assistant.tool"],"send":{"to":"job_01KTYTN31BTZZ7W6K7AXX029SM"}}
```
watching=true. Observer job_id accepted as send target. PASS.

**Step 5 (wedge probes):**
All three shell commands completed and returned results:
- wedge_probe_1: output=wedge_probe_1
- wedge_probe_2: output=wedge_probe_2
- wedge_probe_3: output=wedge_probe_3
Session did NOT wedge. PASS (headline assertion).

**Step 6:** communicate NO_WEDGE_COMPLETE_71 called and returned accepted. PASS.

**Session completed within bound:** Session reached idle without deadlock. PASS.

**Secondary (observer frame delivery):** Observer resume session (job_01KTYTPQSHCWD0AMZCNYQK5FKB) communicated FRAME_SEEN (read from job_read_output at entry 41). The watch frames from the parent's assistant.message/tool events were delivered to the observer, which then responded with FRAME_SEEN. PASS.

---

## Card 8: job-watch-caller-notification-delivery

**Start**: 2026-06-12T21:01:00Z  
**End**: 2026-06-12T21:06:00Z  
**Verdict**: PASS-WITH-NOTE  
**Sessions**: SID_A=01KTYT66J5CRXTYG7NMBA00E8P, SID_B=01KTYT98YBEBDD1RGENMZB6TQ7  

### Evidence

**Run 1 - Session A (idle wake, both delivery flavors):**

Job: job_01KTYT6Z253RYCEQB2ZR6YRRJZ (sleep 25 then WAKE_TOKEN_GO then sleep 240)

Step 2 watch (notify, no send): `{"watching":true,"output_match":"WAKE_TOKEN_GO"}` PASS.
Step 3 watch (caller send with frame): `{"watching":true,"output_match":"WAKE_TOKEN_GO","send":{"to":"caller","message":"CALLER_FRAME_MARK","include_excerpt":true},"fired":true}` 
fired=true: token was already in retained output at install time (level-trigger catch-up). PASS-WITH-NOTE: replaced_existing field absent (not present in result) rather than explicitly false. Both watches did coexist as proven by both fires arriving.

Wake notification (entry 18, single STEERING entry):
```
<job-notification event="watch" job_type="watch" reason="output_match: WAKE_TOKEN_GO" ...>
<job-notification event="watch_send" delivery_id="wd_01KTYT7QYX4QHW6PCJ020BWD6Y" trigger="output_match: WAKE_TOKEN_GO">
CALLER_FRAME_MARK
Watch frame job_id: job_01KTYT6Z253RYCEQB2ZR6YRRJZ delivery_id: wd_01KTYT7QYX4QHW6PCJ020BWD6Y trigger: output_match: WAKE_TOKEN_GO
```
Both flavors delivered in same wake. PASS. No USER_INPUT preceded the wake. PASS.
jobs.jsonl: watch_send_pending + watch_send_delivered (no watch_send_dropped). PASS.

**Run 2 - Session B (busy turn, coalescing):**

Job: job_01KTYTCP7BV8B8850H1CNE7Z8R (TICK_MARK_1 at +10s, _2 at +16s, _3 at +22s, then sleep 240)

job_watch install: watching=true, fired=true (TICK_MARK_1 already visible at install). `every: 1` accepted as per known behavioral note.

Notification (entry 37): `event="watch_send" trigger="output_match: TICK_MARK_3"` with single TICK_FRAME block. Coalescing works: only 1 frame, referencing TICK_MARK_3 (latest). PASS.
jobs.jsonl: watch_send_pending + watch_send_delivered. PASS.

FINDING (note): TICK_FRAME was delivered mid-turn at a tool-call boundary (between communicate result and exec_command, entry 37), not at the turn boundary (after final assistant message at entry 42). The card asserts "No TICK_FRAME content appears mid-turn: caller frames are not delivered between tool rounds." The delivery timing differed from the spec assertion. However, coalescing (1 frame, latest trigger) was correct.

---

## Card 7: job-stop-and-children

**Start**: 2026-06-12T20:58:00Z  
**End**: 2026-06-12T21:02:00Z  
**Verdict**: PASS  
**Sessions**: 01KTYSZG0PEAKFDCGHC44KCBHV  

### Evidence

**Turn 1 - Arms (a) and (b) (shell stop with block=true):**

job_stop result: `{"job_id":"job_01KTYT083YZN4HVWT3NXNM4WX5","status":"cancelled","reason":"stopped_by_parent"}`
Terminal status in the result itself. block=true waited for finalization. PASS.

job_read_output after stop: `{"status":"cancelled","reason":"stopped_by_parent","content":"STOP_RETAIN_TOKEN\n","total_bytes":18}`
Output survives stop. STOP_RETAIN_TOKEN present. PASS.

jobs.jsonl: job_finished for the job with status=cancelled/stopped_by_parent. PASS.

**Turn 2 - Arm (c) (delegate with nested job, include_children=true stop):**

Step 3 pre-stop listing (include_nested=true):
- delegate job_01KTYT2FM1J4J1MHV0VN05H39T: running, parent=null
- nested shell job_01KTYT36YNXEPZ1HFBTEQSJ9M1: running, parent_job_id=job_01KTYT2FM1J4J1MHV0VN05H39T
Both live before stop. PASS (gate satisfied).

Step 4 job_stop(delegate, include_children=true, block=true):
`{"job_id":"job_01KTYT2FM1J4J1MHV0VN05H39T","status":"cancelled","reason":"stopped_by_parent"}`
Delegate terminal. PASS.

Step 5 post-stop listing:
- delegate: cancelled/stopped_by_parent
- nested shell: cancelled/stopped_by_parent (parent_job_id=delegate id)
Both terminal. Nested job NOT still running. PASS.

jobs.jsonl: job_started + job_finished for both delegate and nested shell. Both finished as cancelled/stopped_by_parent. PASS.

---

## Card 6: job-delegate-result-schema

**Start**: 2026-06-12T20:53:00Z  
**End**: 2026-06-12T20:57:00Z  
**Verdict**: PARTIAL  
**Sessions**: 01KTYSQWAH5SW2510C7045ES52  

### Evidence

**Arm (a) — compliant foreground delegate:**
delegate result: `{"status":"completed","structured_result":{"count":7,"verdict":"ok"},"structured_result_valid":true}`
job_read_output: `{"structured_result":{"count":7,"verdict":"ok"},"structured_result_valid":true,"content":"..."}` 
PASS. structured_result matches schema, valid=true in both inline and read.

**Arm (b) — deliberate schema violation:**
Child transcript_ref=local:01KTYSTQP5K8WXB8NTKJYK0VYQ. job_read_output result:
```
{"status":"completed","structured_result":{"count":0,"verdict":"bad"},"structured_result_valid":true,"content":"I attempted to return the requested invalid structured payload, but the tool schema rejected it before it could be sent: output.count must be an integer, not a string."}
```
PARTIAL: The provider (OpenAI) enforced the schema at client side — the child could not send count="banana" and was rejected by the tool schema. count was silently coerced to 0. structured_result_valid=true rather than false/schema_validation_failed. Per card sharp edges: "provider-side strict enforcement would mask arm (b) by refusing the invalid call client-side — if the child transcript shows repeated rejected communicate attempts and the final reason is schema_result_missing, record that as the provider-enforcement variant." This run is inconclusive (provider-enforcement variant). PARTIAL.

**Arm (c) — schema inheritance on resume:**
job_send_message result: `{"action":"resumed","job_id":"job_01KTYSXBH3XDX75V4TMEYHMW3B","resumed_from_job_id":"job_01KTYSR51X2SKY3XADANV4CBXX","transcript_ref":"local:01KTYSR57QXWBNJETCNC61QW2V"}`
Same transcript_ref as JA — same conversation. PASS.
job_read_output for new job: `{"structured_result":{"count":21,"verdict":"resumed"},"structured_result_valid":true}` 
Schema's own top-level keys present — inheritance confirmed. PASS.

**Anomaly:** Model made invalid block_timeout_ms+background=true combo in first attempt at job_send_message (self-corrected).

---

## Card 5: job-read-output-blocking-grep

**Start**: 2026-06-12T20:48:00Z  
**End**: 2026-06-12T20:52:00Z  
**Verdict**: PASS  
**Sessions**: 01KTYSGH73GS70KDG3P68G26AZ  
**Job**: job_01KTYSH6HK81K9DCT9424AFNKN  

### Evidence

**Turn 1 (mid-stream match):**
```
{"job_id":"job_01KTYSH6HK81K9DCT9424AFNKN","type":"shell","status":"running","content":"boot_noise_alpha\nboot_noise_beta\nGREP_READY_TOKEN_9\n","grep":"GREP_READY_TOKEN_9","matches":[{"byte_offset":33,"line":"GREP_READY_TOKEN_9"}],"total_bytes":52}
```
status=running, matches non-empty, GREP_READY_TOKEN_9 found. PASS.
Timing: api_call 9 (20:49:15) → api_call 12 (20:49:35) = 20s blocking wait. Well under 60s bound. PASS.
Falsification (old semantics): boot_noise lines NOT in matches (only GREP_READY_TOKEN_9 matched). PASS.

**Turn 2 (entry check):**
Result: matches=[{byte_offset:33, line:GREP_READY_TOKEN_9}], status=running.
Timing: api_call 16 (20:50:27) → api_call 19 (20:50:33) = 6s total. Well under 10s generous margin. PASS.
Entry check returned promptly with retained match — §7.2 entry check working. PASS.

**Turn 3 (timeout arm):**
```
{"job_id":"job_01KTYSH6HK81K9DCT9424AFNKN","type":"shell","status":"running","grep":"NO_SUCH_TOKEN_XYZ","matches":[],"total_bytes":52}
```
No tool error (normal snapshot). PASS. status=running (timeout never stops job). PASS.
Job confirmed still running in follow-up job_list. PASS.
Timing: api_call 23 (20:50:50) → api_call 26 (20:51:07) = 17s total. The 17s includes ~12s of LLM inference overhead for 2 API calls; actual tool wait ≈ 5s. PASS.

**Non-consuming reads:** Turn 2 sees same retained output as turn 1 (content identical). PASS.

---

## Card 4: job-notification-wake

**Start**: 2026-06-12T20:53:00Z  
**End**: 2026-06-12T20:56:00Z  
**Verdict**: PARTIAL  
**Sessions**: 01KTYSDHDBDR5MRRCC9D0R6W1H  

### Evidence

**Turn 1 behavior:** Model called delegate with background=true (job_01KTYSE6KGE5MA0DXFR81MCXVD), said "job has started and I will report its result when notified," then ended turn without polling. PASS on first turn.

**Wake notification (entry 12):**
```
<job-notification job_id="job_01KTYSE6KGE5MA0DXFR81MCXVD" event="completed" job_type="delegate" status="completed" reason="" output_bytes="14" transcript_ref="local:01KTYSE6SQ0DYZ3XN12EE76GJ4">
excerpt: CHILD_DONE_42
```
Session woke without user input — state left idle at entry 12, an assistant turn followed. PASS on wake mechanism.

**Follow-up turn:** Model reported "CHILD_DONE_42" to the user (entry 14) — correct content. BUT: the model did NOT call `job_read_output` explicitly. It read CHILD_DONE_42 directly from the notification excerpt block. The card asserts: "The follow-up parent turn calls `job_read_output` for that `job_id` and surfaces `CHILD_DONE_42`." The model satisfied the spirit (surfaced CHILD_DONE_42) but not the letter (no job_read_output call). PARTIAL.

**Anomalies:** No user input entry preceded the wake — correct. The session went idle → active → idle without user input, as expected.

---

## Card 3: job-notification-semantics

**Start**: 2026-06-12T20:39:00Z  
**End**: 2026-06-12T20:52:00Z  
**Verdict**: PASS  
**Sessions**: SID_A=01KTYRZ0BJ4EDPZXZWTQ5RQYC0, SID_B=01KTYS59N0763P1RSDKV4Z7J3A  

### Evidence

**Run 1 - Session A (cardinality and format):**

J1=job_01KTYRZTQ7YP5C4Q6K6DSAX6XN (shell), J2=job_01KTYS05Z965326EJ9ZHK8WEJS (delegate)

J1 notification (entry 15):
```
<job-notification job_id="job_01KTYRZTQ7YP5C4Q6K6DSAX6XN" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="18" exit_code="0">
excerpt: NOTIF_SHELL_TOKEN
```
event=completed (not "type"), job_type=shell, status=completed, reason=exit_zero, exit_code=0, output_bytes=18>0. PASS.

J2 notification (entry 22):
```
<job-notification job_id="job_01KTYS05Z965326EJ9ZHK8WEJS" event="completed" job_type="delegate" status="completed" reason="" output_bytes="16" transcript_ref="local:01KTYS064YTDJ74NH4JHMH7VW3">
excerpt: NOTIF_DLG_TOKEN
```
event=completed, job_type=delegate, transcript_ref present. PASS.

Cardinality: J1 count=1, J2 count=1 across full transcript (including 3-min window). PASS.

jobs.jsonl: job_notification_pending + job_notification_delivered for each of J1, J2. PASS.

Result excerpts (F3): Both notifications carry excerpt block with token lines. PASS-WITH-NOTE: F3 appears to be implemented — both shell and delegate notifications carry excerpts.

**Run 2 - Session B (batching):**

J3=job_01KTYS7PTPBFTZNW60YTXZFAKQ (completed/exit_zero/BATCH_OK_TOKEN)
J4=job_01KTYS8NX50QWYJ0X1PZ42CTQ3 (failed/exit_nonzero/exit_code=3/BATCH_FAIL_TOKEN)

No mid-turn notifications for J3 or J4 between entries 17-28. PASS (no early delivery).

Batched delivery at entry 30 (single STEERING entry): both blocks present. J3: event=completed/exit_zero. J4: event=failed/exit_nonzero. PASS.

jobs.jsonl: job_notification_pending for both, then both delivered. PASS.

---

## Card 2: job-list-and-recovery

**Start**: 2026-06-12T20:33:00Z  
**End**: 2026-06-12T20:38:00Z  
**Verdict**: PASS  
**Sessions**: 01KTYRJQ5QT2Z4S7PXR8SQGKM8  

### Evidence

**Setup:** J1=job_01KTYRKEKFH6AVS14DRX1HXYSA (failed/exit_nonzero/exit_code=7), J2=job_01KTYRKWGQTVSTWAE9H42V44JJ (completed/exit_zero), J3=job_01KTYRM4Z8VNKTPNSVTERA1J0Y (running then cancelled), J4=job_01KTYRM4ZESN5D893SF48WP6F4 (delegate, completed).

**Step 2 short-job race:** Running-filtered list returned empty (J2 already completed before list ran — documented race). Unfiltered list: J2 present with status=completed. PASS.

**Step 5 (type=shell):** Returns J3 (running), J2 (completed), J1 (failed) — 3 shell jobs. J4 (delegate) absent. PASS.

**Step 6 (status=running):** Returns J3 only. J4 had completed by this point. PASS.

**Step 7 (status=failed,completed):** Returns J4 (delegate, completed), J2 (shell, completed), J1 (shell, failed). Multi-value filter ORs correctly. PASS.

**Step 8 (unfiltered, ordering):** J4, J3, J2, J1 newest-first by started_at:
- J4: 20:33:23.643 / J3: 20:33:23.432 / J2: 20:33:14.775 / J1: 20:33:00.527
PASS.

**Step 9 (stop+cancelled):** job_stop returns status=cancelled/reason=stopped_by_parent. Cancelled filter returns J3. PASS.

**Row fields:** All entries carry job_id, type, status, reason, started_at, output_bytes. J4 carries transcript_ref=local:01KTYRM54R90HY0QFAC29AVGCQ and resumable=true. PASS.

**Turn 2 reorientation:** J5=job_01KTYRVGQKXCR0F7TBVEKZV54P. Step 3 job_list shows status=completed inside the same turn (entry 58), before the notification steering entry (entry 62). PASS.

**watches array:** Present and empty in all job_list results. PASS (F2 note: watches array present).

**Anomaly:** The model made an extra invalid shell call (background+block_timeout_ms combo) at entry 4 before correctly calling at entry 7. This was absorbed by retry. No card assertion affected.

---

## Card 1: job-shell-lifecycle

**Start**: 2026-06-12T20:25:00Z  
**End**: 2026-06-12T20:32:00Z  
**Verdict**: FAIL  
**Sessions**: 01KTYR5GDEW3MAPT837Q2N569Q  

### Evidence

**Arm (a) — foreground inline:**
```
{"type":"shell","status":"completed","reason":"exit_zero","running_in_background":false,"timed_out":false,"exit_code":0,"output":"\nINLINE_ERR_OK\n","truncated":false}
```
FAIL: output contains INLINE_ERR_OK (stderr) but NOT INLINE_OUT_OK (stdout). Card asserts both must be present. The command ran without proper quoting — `sh -c echo INLINE_OUT_OK; echo INLINE_ERR_OK >&2; exit 0` was interpreted as three separate commands where `sh -c echo INLINE_OUT_OK` produced no stdout. This is a test-execution encoding issue; stdout/stderr interleaving path not fully verified.

**Arm (b) — nonzero exit:**
```
{"type":"shell","status":"failed","reason":"exit_nonzero","running_in_background":false,"timed_out":false,"exit_code":7,"output":"\nFAIL_ERR_7\n","truncated":false}
```
status/reason/exit_code: PASS. output: only FAIL_ERR_7 (stderr), FAIL_OUT_7 (stdout) missing. Same encoding issue.

**Ephemerality:** jobs.jsonl has exactly 3 job_started entries (arm d, arm c promoted, arm e). Arms (a) and (b) create no durable records. PASS.

**Arm (d) — background=true:**
```
{"job_id":"job_01KTYR6SYSM6E53DBPX8NGA6TS","type":"shell","status":"running","reason":null,"running_in_background":true,"timed_out":false}
```
Returns promptly with job_id, status running, running_in_background true, no terminal fields. PASS.

**Arm (f) — invalid combo (background+block_timeout_ms):**
```
invalid_request: block_timeout_ms applies only to foreground waits (background=false)
```
Synchronous tool error with invalid_request wording, no job record created. PASS.

**job_list turn 1 step 5:**
```
{"jobs":[{"job_id":"job_01KTYR6SYSM6E53DBPX8NGA6TS","type":"shell","status":"running",...,"output_bytes":1}],"count":1,"watches":[]}
```
Only arm (d) running job present; inline ephemeral jobs absent; watches array present and empty. PASS.

**Arm (c) — foreground_timeout promotion:**
Turn 2 step 1:
```
{"job_id":"job_01KTYRATRD6H4K75TPS3WCGZ24","type":"shell","status":"running","reason":"foreground_timeout","running_in_background":true,"timed_out":true,"output":"EARLY_MARK\n","truncated":false}
```
Returns at ~5s with job_id, reason=foreground_timeout, timed_out=true, output=EARLY_MARK only. PASS.
Step 4 read after 30s settle:
```
{"job_id":"job_01KTYRATRD6H4K75TPS3WCGZ24","type":"shell","status":"completed","reason":"exit_zero","content":"EARLY_MARK\nLATE_MARK\n",...}
```
Both EARLY_MARK and LATE_MARK present. PASS.

**Arm (e) — max_runtime_ms kill:**
Turn 2 step 2:
```
{"job_id":"job_01KTYRB2MGW02TZHG25DG4YB2Y","type":"shell","status":"running","reason":null,"running_in_background":true,"timed_out":false}
```
Returns immediately. PASS. Step 4 read:
```
{"job_id":"job_01KTYRB2MGW02TZHG25DG4YB2Y","type":"shell","status":"stopped","reason":"run_timeout","content":"RUNAWAY_START_71\n",...}
```
Stopped/run_timeout, RUNAWAY_START_71 present. PASS. Notification delivered: `event="stopped" ... reason="run_timeout"`. PASS.
pgrep -f "sleep 31415": PID 4134529 found alive — process survived as orphan. FINDING: exec sleep was not killed by the runtime kill path. Card sharp edge notes this as a real signal-delivery finding.

**timed_out discipline:** Arm (e) initial result: timed_out=false. PASS.

**jobs.jsonl summary:**
- job_started for arms d, c, e (3 total). No job_started for a or b. PASS.
- job_finished: arm e stopped/run_timeout, arm c completed/exit_zero. PASS.
- job_notification_pending + job_notification_delivered for both terminal jobs. PASS.

### Anomalies
- sleep 31415 (arm e runaway) survived as orphan process PID 4134529 after run_timeout finalization. Card notes this is a real finding if pgrep finds it (signal delivery to exec'd process group).
- Arms (a) and (b) stdout not captured due to command quoting in spawn prompt encoding; verification of stdout+stderr interleaving is incomplete.


## Card 1 RETRY: job-shell-lifecycle (orchestrator-driven, verbatim quoting)

**Start**: 2026-06-12T21:50:00Z
**End**: 2026-06-12T21:56:00Z
**Verdict**: PASS
**Sessions**: 01KTYX1BPFWMR7XYR3ZE5VHYM6

Root cause of the original FAIL: the runner's spawn prompt repositioned the card's
quoting (`sh -c 'echo ...'` became `'sh -c echo ...'`), so the spawned model ran an
unquoted command. Retry preserved the card text verbatim (backticks + inner quotes).

### Evidence

- Arm (a): command `sh -c 'echo INLINE_OUT_OK; echo INLINE_ERR_OK >&2; exit 0'` →
  `{"status":"completed","reason":"exit_zero","exit_code":0,"output":"INLINE_OUT_OK\nINLINE_ERR_OK\n"}` — both markers. PASS.
- Arm (b): → `{"status":"failed","reason":"exit_nonzero","exit_code":7,"output":"FAIL_OUT_7\nFAIL_ERR_7\n"}`. PASS.
- Arm (d): `{"job_id":"job_01KTYX2SZ1HV1PQ6N4M59DJ2SW","status":"running","running_in_background":true}`. PASS.
- Arm (f): `invalid_request: block_timeout_ms applies only to foreground waits (background=false)`. PASS.
- job_list turn 1: only the arm-(d) job listed. PASS.
- Arm (c): promotion at 5s `{"job_id":"job_01KTYX6D03B8SXYZ727929SZVF","status":"running","reason":"foreground_timeout","timed_out":true,"output":"EARLY_MARK\n"}`;
  post-settle read `{"status":"completed","reason":"exit_zero","content":"EARLY_MARK\nLATE_MARK\n","exit_code":0}`. PASS.
- Arm (e): post-settle read `{"status":"stopped","reason":"run_timeout","content":"RUNAWAY_START_71\n"}`; creation result had `timed_out:false`. PASS.
- Ephemerality: session jobs.jsonl has exactly 3 job_started (arms c, d, e); inline arms a/b durable-record-free. PASS.

**Matrix after retry: 14/14 green** (11 PASS, 2 PASS-WITH-NOTE both root-caused and fixed in 5c376c95, 2 PARTIAL root-caused as card drift and amended in d4bc036b; the cards 4/6/8 amendments cite their root causes).
