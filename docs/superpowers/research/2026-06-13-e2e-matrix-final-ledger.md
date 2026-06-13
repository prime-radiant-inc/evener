# E2E Matrix Re-run Ledger

Run started: 2026-06-13
Hub: http://127.0.0.1:9180
Model: openai/gpt-5.5
Branch: job-control-spec HEAD f563b2ca

## Summary Table

| # | Card | Verdict | Session ID | Note |
|---|------|---------|------------|------|
| 1 | job-shell-lifecycle | PASS | 01KTZVZE6W1W1H3ZYCRW5SGFDJ | All arms a-f pass |
| 2 | job-list-and-recovery | PASS | 01KTZWDK2NSPTMZG1JD5H814ER | All assertions pass; J2 timing-soft as expected |
| 3 | job-notification-semantics | PASS | SID_A=01KTZWSD0MMYBZ3G0MYKWWN2C1 | Cardinality, format, batching all pass |
| 4 | job-notification-wake | PASS | 01KTZX7DDSHA5DN0HZEAEDAV8G | Delegate wake path works |
| 5 | job-read-output-blocking-grep | PASS | 01KTZXA412JD7K7RCKY63DC7FX | Blocking grep semantics correct |
| 6 | job-delegate-result-schema | PASS | 01KTZXJ4TH8516PRHZ6G52G0J6 | Arms a+c pass; b: call-time-gate variant (expected) |
| 7 | job-stop-and-children | PASS | 01KTZXSE5T5CPQW1KCT08Q7Z9G | All stop and children arms pass |
| 8 | job-watch-caller-notification-delivery | PASS | SID_A=01KTZXZGCZGA0BY64NJZP130KN | Both delivery flavors pass; coalescing works |
| 9 | job-watch-caller-send-no-deadlock | PASS | 01KTZYDNSWWF95P71R6Y1XCYH3 | No deadlock; loop rejected; observer works |
| 10 | job-watch-output-match-catchup | PASS | 01KTZYKHD30HBKMG7ZEXKKC97C | Level-trigger and terminal catch-up all pass |
| 11 | job-watch-sidecar-observer | PASS | 01KTZYYGP8YMSFC1DWC0FAHSTZ | Full sidecar flow including grant and comment pass |
| 12 | job-send-message-surface | PASS | 01KTZZ3ZCTBJ0S3QM1H2JBNWSA | All arms a-d pass |
| 13 | job-nested-visibility | PASS | 01KTZZS4MNCFC11PH3E4ZQ7MQK | All arms a-d pass |
| 14 | job-restart-durability | PASS | 01KV0021RJR67AWF4JXM35Z1RK | All durability/dedupe assertions pass |

## Card Details

### Card 1: job-shell-lifecycle

**Verdict: PASS**
**Session: 01KTZVZE6W1W1H3ZYCRW5SGFDJ**

#### Results

- **Arm (a)** PASS: `status:"completed"`, `reason:"exit_zero"`, `exit_code:0`, `running_in_background:false`, `timed_out:false`, no `job_id`, output="INLINE_OUT_OK\nINLINE_ERR_OK\n" (stdout+stderr both captured)
- **Arm (b)** PASS: `status:"failed"`, `reason:"exit_nonzero"`, `exit_code:7`, output="FAIL_OUT_7\nFAIL_ERR_7\n" — normal tool result, not tool error
- **Ephemerality** PASS: job_list after turn 1 shows only the promoted step-3 job, NOT steps 1 or 2
- **Arm (d)** PASS: `job_id:"job_01KTZW16FD0FK2HPCDM1W5PAF0"`, `status:"running"`, `reason:"foreground_timeout"`, `timed_out:true`, `running_in_background:true`, output contains BG_START_MARK
- **Arm (c)** PASS: step-1 returned at ~5s with `job_id`, `timed_out:true`; later read showed `status:"completed"`, content "EARLY_MARK\nLATE_MARK\n"
- **Arm (e)** PASS: step-2 returned at ~1s with promotion shape; later read showed `status:"stopped"`, `reason:"run_timeout"`. Runaway `sleep 31415` confirmed dead (ps count=0). jobs.jsonl confirms one job_finished with `status:"stopped"`,`reason:"run_timeout"`
- **timed_out discipline** PASS: arm-(e) final read does not have timed_out=true
- **Arm (f)** PASS: chatty command got `job_id`, `status:"completed"`, `truncated:true`. job_read_output returned `total_bytes:1600000`, content starts with COH_CHATTY_LINE. Quiet `exit 0` had no job_id (ephemeral). job_list shows chatty job but NOT the quiet job.
- **jobs.jsonl** PASS: job_started for promoted jobs, job_finished for arm-(e) `stopped/run_timeout`, notifications pending+delivered for arms (c) and (e)

### Card 2: job-list-and-recovery

**Verdict: PASS**
**Session: 01KTZWDK2NSPTMZG1JD5H814ER**

#### Results

- **Filter type=["shell"]**: J1, J2, J3 present (NOT J4 delegate). PASS.
- **Filter status=["running"]**: J3 (LIST_RUN_TOKEN job) only. J1/J2 were already terminal at list time (timing-soft, expected per card). PASS.
- **Filter status=["failed","completed"]**: J4(completed), J2(completed), J1(failed). PASS.
- **Filter status=["cancelled"]**: J3 after stop. PASS.
- **Ordering step-8 (newest-first)**: J4→J3→J2→J1 (matches started_at descending). PASS.
- **J2 durable presence**: J2 appeared in the unfiltered listing as "completed" (was already completed by running-filter time due to timing; both lists showed J2 in at least the unfiltered form). PARTIAL-PASS (timing-soft per card).
- **Row fields**: All rows have job_id, type, status, reason, started_at, output_bytes. J4 has transcript_ref and resumable. PASS.
- **Re-orientation (turn 2)**: J5 (sleep 6) started at 07:03:43, completed at 07:03:49. job_list at step 3 (entry 60) returned J5 as "completed" BEFORE the notification turn (entry 64). Ordering confirmed in transcript. PASS.
- **Watches field**: Present and empty ([]). PASS.
- **Paging**: count field present, no cursor. PASS.

### Card 3: job-notification-semantics

**Verdict: PASS**
**Sessions: SID_A=01KTZWSD0MMYBZ3G0MYKWWN2C1, SID_B=01KTZX0965C6MCW23ZV7EWPZTX**

#### Results

Run 1 (Session A):
- J1 shell notification: exactly 1 occurrence, `event="completed"`, `job_type="shell"`, `status="completed"`, `reason="exit_zero"`, `exit_code="0"`, excerpt contains NOTIF_SHELL_TOKEN. PASS.
- J2 delegate notification: exactly 1 occurrence, `event="completed"`, `job_type="delegate"`, `status="completed"`, `transcript_ref` present (J2 `reason=""` is acceptable). PASS.
- jobs.jsonl: each job has exactly one `job_notification_pending` and one `job_notification_delivered`. PASS.
- 3-minute window: no duplicates observed. PASS.

Run 2 (Session B):
- No early delivery: no `<job-notification` before essay turn's final communicate (entry 33). PASS.
- Batched delivery: BOTH J3 and J4 in ONE STEERING entry (entry 35), after the essay turn boundary. PASS.
- J3: `event="completed"`, `status="completed"`, `reason="exit_zero"`, excerpt=BATCH_OK_TOKEN. PASS.
- J4: `event="failed"`, `status="failed"`, `reason="exit_nonzero"`, `exit_code="3"`, excerpt=BATCH_FAIL_TOKEN. PASS.
- Mixed-status preserved: J4 reports failed correctly, not smeared to completed. PASS.
- Result excerpts (d): both blocks carry excerpts with the token lines. PASS.

### Card 4: job-notification-wake

**Verdict: PASS**
**Session: 01KTZX7DDSHA5DN0HZEAEDAV8G**

#### Results

- First turn: `delegate` (no max_wait_ms) returned job_id immediately → session idle (no polling). PASS.
- Notification wake (entry 16): session left idle without user input. PASS.
- Notification `event="completed"`, `job_type="delegate"`, `status="completed"`, `transcript_ref` present, excerpt contains CHILD_DONE_42. PASS.
- Follow-up assistant turn (entry 19): model reacted, session returned to idle. PASS.
- CHILD_DONE_42 surfaced from excerpt (no job_read_output required). PASS.
- Hub rendered delegate as job reference (transcript_ref in notification). PASS.

### Card 5: job-read-output-blocking-grep

**Verdict: PASS**
**Session: 01KTZXA412JD7K7RCKY63DC7FX**

#### Results

- **Turn 1 (mid-stream match)**: `job_read_output(max_wait_ms=60000, grep="GREP_READY_TOKEN_9")` returned at ~20s with `matches:[{byte_offset:33, line:"GREP_READY_TOKEN_9"}]`, `status:"running"` (not yet terminal). Two noise lines (boot_noise_alpha, boot_noise_beta) did NOT cause early return. PASS.
- **Turn 2 (entry check)**: Same call with 30s bound. Returned with match quickly (wall time ~25s including model latency ~12s; actual blocking sub-10s). No 30s timeout hit. PASS.
- **Reads non-consuming**: Turn 2 saw same content as turn 1. PASS.
- **Turn 3 (timeout)**: `job_read_output(max_wait_ms=5000, grep="NO_SUCH_TOKEN_XYZ")` returned normally (not tool error) with `matches:[]`, `status:"running"`. Job still running in job_list. PASS.

### Card 6: job-delegate-result-schema

**Verdict: PASS**  (arm b: call-time-gate variant, inconclusive per card's sharp edges — not a product bug)
**Session: 01KTZXJ4TH8516PRHZ6G52G0J6**

#### Results

- **Arm (a)** PASS: delegate with schema returned `structured_result: {"count":7,"verdict":"ok"}`, `structured_result_valid:true`. job_read_output confirmed same. PASS.
- **Arm (b)** INCONCLUSIVE (expected): gpt-5.5 tried to emit `count:"banana"`, tool registry rejected at call-time, child retried with `count:0` → `valid:true`. Per card sharp edges: "call-time-gate variant, not a validation bug." Not a product FAIL; this outcome was observed and documented in the original 2026-06-12 run.
- **Arm (c)** PASS: `job_send_message` on completed JOB_A returned `action:"resumed"`, new job_id, `resumed_from_job_id=JOB_A`, same `transcript_ref`. Resumed job produced `structured_result: {"count":21,"verdict":"resumed"}`, `structured_result_valid:true`. Schema inherited from original. PASS.
- Both background jobs (b, c) delivered exactly one terminal notification each. PASS.

### Card 7: job-stop-and-children

**Verdict: PASS**
**Session: 01KTZXSE5T5CPQW1KCT08Q7Z9G**

#### Results

- **Arm (a) confirmed stop**: `job_stop(max_wait_ms=5000)` returned `status:"cancelled"`, `reason:"stopped_by_parent"` synchronously. PASS.
- **Arm (b) output retained**: `job_read_output` after stop returned `status:"cancelled"`, `content` containing STOP_RETAIN_TOKEN. PASS.
- **Arm (c) nested visibility**: Step-3 listing showed both delegate running and nested shell running with `parent_job_id=delegate_job_id`. PASS.
- **Arm (c) include_children stop**: `job_stop(include_children=true, max_wait_ms=5000)` returned delegate `cancelled/stopped_by_parent`. PASS.
- **Arm (c) both terminal**: Step-5 listing shows both delegate and nested shell as `cancelled/stopped_by_parent` with `parent_job_id` preserved. PASS.
- jobs.jsonl: confirmed `job_finished` events for both arm-(a) and arm-(c) jobs. PASS.

### Card 8: job-watch-caller-notification-delivery

**Verdict: PASS**
**Sessions: SID_A=01KTZXZGCZGA0BY64NJZP130KN, SID_B=01KTZY3WZTM7HYY28G0Q1D1YZ8**

#### Results

Run 1 (Session A):
- Both watches installed with `watching:true`. `replaced_existing:false` for step 3 (distinct keys). PASS.
- Session was idle before the fire; woke without user input. PASS.
- Both delivery flavors in ONE notification turn (entry 22):
  - Notify-flavor: `event="watch"`, `job_type="watch"`, `reason="output_match: WAKE_TOKEN_GO"`. PASS.
  - Caller-send frame: `event="watch_send"`, `delivery_id` present, `trigger="output_match: WAKE_TOKEN_GO"`, `CALLER_FRAME_MARK` and `Watch frame` block with all required fields. PASS.
- An assistant turn followed; session returned to idle. PASS.
- jobs.jsonl: `watch_send_pending` + `watch_send_delivered`. No dropped. PASS.

Run 2 (Session B):
- All three TICK_MARK fires while turn was still busy. No notification mid-stream. PASS.
- ONE rendered TICK_FRAME after essay turn boundary (entry 44). `trigger="output_match: TICK_MARK_3"` (latest). PASS.
- jobs.jsonl: one `watch_send_pending`, one `watch_send_delivered` (coalesced). PASS.

### Card 9: job-watch-caller-send-no-deadlock

**Verdict: PASS**
**Session: 01KTZYDNSWWF95P71R6Y1XCYH3**

#### Results

- **Steps 1 and 2 REJECTED**: Both `job_watch` calls with self-delivery loop returned `invalid_request: watching assistant.message/assistant.tool/communicate with delivery back to the caller is a feedback loop`. Neither returned `watching:true`. PASS.
- **Step 4 ACCEPTED**: `watching:true` with observer job_id as `send.to`. PASS.
- **THE HEADLINE - No deadlock**: Turn completed. All 3 `wedge_probe_*` shell commands executed and returned. `NO_WEDGE_COMPLETE_71` communicate succeeded. Session reached idle within 122s (well under 240s bound). PASS.
- **Secondary**: Observer session eventually showed `FRAME_SEEN` notification (resumed job completed). PASS.
- **Cleanup**: `job_watch(target="caller", clear=true)` executed (watching:false result). PASS.

### Card 10: job-watch-output-match-catchup

**Verdict: PASS**
**Session: 01KTZYKHD30HBKMG7ZEXKKC97C**

#### Results

Turn 1 (arm a - attach after retained output):
- `job_watch(output_match="LEVEL_TOKEN_[ABC]")` returned `watching:true`, `fired:true` (attach scan found retained A+B).
- Attach-scan notification (entry 22): `reason="output_match: LEVEL_TOKEN_B"` — LAST matching retained line. EXACTLY ONE notification. PASS.
- Second notification (entry 29) for LEVEL_TOKEN_C (new match ~30s later) via idle wake. PASS.

Turn 2/3 (arms b and c on terminal job):
- **Arm (b) positive**: `watching:false`, `fired:true`, `terminal_catchup:true` — one-shot catch-up found the token. PASS.
- **Arm (b) negative**: `watching:false`, `fired:false`, `terminal_catchup:true` — one-shot catch-up, token not present. PASS.
- **Arm (c)**: `job_watch(events=["job.notification"])` on terminal job returned `target_terminal` error. PASS.
- Catch-up notification (from arm b positive) arrived at boundary. PASS.

### Card 11: job-watch-sidecar-observer

**Verdict: PASS**
**Session: 01KTZYYGP8YMSFC1DWC0FAHSTZ**

#### Results

- `delegate` returned immediately with job_id (observer started). PASS.
- Shell job started with `max_wait_ms 1000`. PASS.
- `job_watch(output_match="SIDECAR_TOKEN_OK", send.to=observer_job_id, include_frame=true)` returned `watching:true`. PASS.
- Watch fired when SIDECAR_TOKEN_OK printed ~20s later; frame delivered to observer. PASS.
- Observer resumed with Watch frame; read the watched shell job via `job_read_output` (grant worked — cross-store read succeeded). PASS.
- Observer commented back: `OBSERVER_COMMENT delivery=wd_01KTZZ06NEHWHDCX5Y8QZR5PFE line=SIDECAR_TOKEN_OK`. Comment reached parent transcript (entry 27). PASS.
- `delivery_id` in comment matches the watch frame's delivery_id. PASS.
- jobs.jsonl: `watch_read_grant`, `watch_send_pending`, `watch_send_delivered` all present. PASS.
- Observer resumed job's terminal notification woke parent; session returned to idle. PASS.

### Card 14: job-restart-durability

**Verdict: PASS**
**Session: 01KV0021RJR67AWF4JXM35Z1RK**

#### Results

- **Pre-restart (step 4)**: jobs.jsonl contained only `job_started` (no `job_finished`). Output log at TICK_35 (35 lines, no PRODUCER_DONE). Operator SIGKILL at PID 1531448. PASS.
- **Reconciliation (step 6 jobs.jsonl)**: Exactly one `job_finished` with `status:"stopped"`, `reason:"runtime_lost"`, `terminal_generation:"01KV004ANDWY229V0GFV8F4B6K"`. One `job_notification_pending` + one `job_notification_delivered` with same terminal_generation. PASS.
- **Model-visible (step 6 transcript)**: job_list reports `stopped`/`runtime_lost` (not `failed`). job_read_output: `status=stopped`, `reason=runtime_lost`, `total_bytes=271`, first line=TICK_1, last line=TICK_35 (matches pre-crash tail). Content does not contain PRODUCER_DONE. PASS.
- **Exactly-once notification**: Exactly ONE STEERING entry with `event="stopped"`, `status="stopped"`, `reason="runtime_lost"` at seq 8. No duplicate. PASS.
- **Second-restart dedupe (step 7)**: Second daemon (PID 1532388) SIGKILL'd. jobs.jsonl job_finished count for the job: still 1 (no new record). job_list after second restart: still `stopped`/`runtime_lost` unchanged. Transcript still has exactly ONE runtime_lost STEERING entry. PASS.

### Card 13: job-nested-visibility

**Verdict: PASS**
**Session: 01KTZZS4MNCFC11PH3E4ZQ7MQK**

#### Results

- **Turn 1**: Delegate (`job_01KTZZSJ2DQBGGWFKJWK4ERRR8`) spawned nested shell (`job_01KTZZSMKGYYHXY5NACRNKJ6SZ`). Delegate completed with excerpt `NESTED_JOB job_01KTZZSMKGYYHXY5NACRNKJ6SZ`. ID equality confirmed (no namespacing). PASS.
- **Arm (a) step-1**: `job_list(include_nested=false)` shows only delegate (1 row, type=delegate, completed). Nested shell NOT present. PASS.
- **Arm (a) step-2**: `job_list(include_nested=true)` shows 2 rows: nested shell + delegate. Nested: `type=shell`, `status=running`, `parent_job_id=job_01KTZZSJ2DQBGGWFKJWK4ERRR8` (delegate), `owner_session_id=01KTZZSJ6R1HE2635X4E9QWBZ2` (child session != parent SID), `visible_to_session_id=01KTZZS4MNCFC11PH3E4ZQ7MQK` (parent). PASS.
- **Arm (b)**: `job_read_output(nested_job_id)` returned `status=running`, `content="NEST_TOKEN_1\n"`. Cross-store read succeeded via parent-visible id. PASS.
- **Arm (c)**: `job_stop(nested_job_id, max_wait_ms=5000)` returned `status=cancelled`, `reason=stopped_by_parent`. NOT `not_controllable`. Live-owner routing worked. PASS.
- **Arm (d)**: Second `job_read_output` after stop returned `status=cancelled`, `reason=stopped_by_parent`, `content="NEST_TOKEN_1\n"`, NEST_TOKEN_2 absent (sleep cut short). Forwarded output retained post-terminal. PASS.
- **jobs.jsonl**: `job_started` for nested job with `parent_job_id=delegate_job_id` and `owner_session_id=child_session`. `job_finished` for nested job with `status=cancelled`, `reason=stopped_by_parent`. Notifications: one pending + one delivered each for both jobs. PASS.

### Card 12: job-send-message-surface

**Verdict: PASS**
**Session: 01KTZZ3ZCTBJ0S3QM1H2JBNWSA**

#### Results

- **Arm (a) - steer running delegate**: `job_send_message` on running JA returned `action:"sent"`, `job_id:JA` (no new job), `status:"running"`. JA completed; job_read_output confirmed content contains STEER_MARK_88. Live steer path taken. PASS.
- **Arm (b) - resume finished delegate**: JB=`job_01KTZZBH6RWH7PPRCAA702AF8Y` completed with READY_TO_RESUME. `job_send_message` returned `action:"resumed"`, new `job_id=job_01KTZZC190CQ9X2VTTP15CYMAD`, `resumed_from_job_id=JB`, same `transcript_ref=local:01KTZZBHBVY6HGZJSEF3CV649F`, `running_in_background:true`. Resumed job output: AZURE_FALCON (prior context retained). PASS.
- **Arm (c) - on_finished=fail**: `job_send_message(on_finished="fail")` against terminal JB returned `target_terminal: delegate job "job_01KTZZBH6RWH7PPRCAA702AF8Y" is completed` synchronously. No new job created (job_list step 2: count unchanged at 3). PASS.
- **Arm (d) - max_wait_ms 2000 foreground timeout**: `job_send_message(max_wait_ms=2000)` returned at ~2006ms with `action:"resumed"`, `reason:"foreground_timeout"`, `timed_out:true`, `running_in_background:true`, new `job_id=job_01KTZZP16DT35ASG0HVZB0JXZ4`. step-4 listing confirmed job still running. Terminal notification arrived with SLOW_RESUME_DONE. PASS.

