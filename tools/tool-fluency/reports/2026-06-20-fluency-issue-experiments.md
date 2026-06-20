# Tool Fluency Issue Experiments - 2026-06-20

This journal tracks issue-focused experiments for the current Serf tool
fluency work. It is intentionally separate from broad model run reports: the
goal here is to isolate one fluency question at a time, run it against real
tools, record artifacts, and only then decide whether the right fix is prompt,
schema, runtime, harness, or probe design.

## Protocol

For each experiment:

1. State the smallest hypothesis.
2. Run the smallest probe or catalog command that can falsify it.
3. Store artifacts under a unique `/tmp/serf-fluency-issue-*` directory.
4. Use `serf-fluency` summaries and `serf-doctor` for session forensics.
5. Classify findings using the tool-fluency categories: `schema`,
   `availability`, `selection`, `arguments`, `repair`, `interpretation`,
   `churn`, `polling`, `plain_message`, or `infra`.
6. Do not call a fix successful until the same experiment passes on both
   `openai/gpt-5.4-mini` and `kimi/kimi-for-coding`, unless the issue is
   provider-specific by design.

No experiment in this file should depend on ad-hoc Python, jq, or transcript
JSONL parsing. If an inspection is hard, improve the Go runner or
`serf-doctor`.

## Current Issue Matrix

| ID | Issue | Primary probe | Models | Status |
| --- | --- | --- | --- | --- |
| E00 | Baseline inventory and reproducibility | `catalog`, committed broad reports | GPT, Kimi | complete |
| E01 | CLI harness may cancel observer callbacks before callback work runs | `job_watch.observer_callback` | GPT, Kimi | complete |
| E02 | Live-session harness needed for true watch callback verification | `job_watch.observer_callback` | GPT, Kimi | complete |
| E03 | Observer sidecar readiness should wait for watch frames fluently | `job_watch.observer_callback` | GPT, Kimi | partial |
| E04 | `job_watch` caller notification argument shape confuses GPT | `job_watch.observer_callback` | GPT, Kimi | complete |
| E05 | Waiting workflows should not poll job state | `job_watch.observer_callback`, `jobs.control_lifecycle` | GPT, Kimi | complete |
| E06 | Explicit URL fetch request should select `web_fetch` | `web_fetch.example` | GPT, Kimi | complete |
| E07 | `communicate` should be selected instead of assistant plain text | `communicate.final_message`, `read_file.happy_path`, `task_list.plan` | GPT, Kimi | complete |
| E08 | Provider aliasing should not hide `list_dir` semantics | `list_dir.inventory`, `catalog` | GPT, Kimi | complete |
| E09 | `task_list` should not churn on a tiny planning task | `task_list.plan` | GPT, Kimi | complete |
| E10 | Job lifecycle workflow should avoid shell escapes and redundant job calls | `jobs.control_lifecycle` | GPT, Kimi | complete |
| E11 | Unavailable tools should skip cleanly, not create fake failures | `web_search.current`, `catalog` | GPT, Kimi | complete |
| E12 | Full-suite regression after focused fixes | `all` | GPT, Kimi | complete |
| E13 | `communicate` should not be used as a progress update while work remains | `job_watch.observer_callback`, `communicate.final_message` | GPT, Kimi | complete |

## Experiment Log

### E00 - Baseline Inventory And Reproducibility

Hypothesis: the broad reports are sufficient as a baseline only if a fresh
catalog still matches the current branch and the known failures reproduce in
targeted runs.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model openai/gpt-5.4-mini
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model kimi/kimi-for-coding
```

Artifacts:

- Command output captured in terminal for:
  - `go run ./tools/tool-fluency/cmd/serf-fluency catalog --model openai/gpt-5.4-mini`
  - `go run ./tools/tool-fluency/cmd/serf-fluency catalog --model kimi/kimi-for-coding`
- Prior broad run reports:
  - `tools/tool-fluency/reports/2026-06-20-openai-gpt-5.4-mini.md`
  - `tools/tool-fluency/reports/2026-06-20-kimi-for-coding.md`

Result:

- Complete.
- GPT cataloged 21 model-facing tools:
  `apply_patch`, `communicate`, `compact`, `delegate`, `delegate_send`,
  `edit_file`, `exec_command`, `find_session_transcripts`, `grep_files`,
  `job_list`, `job_read_output`, `job_stop`, `job_watch`, `list_dir`,
  `read_file`, `read_session_transcript`, `task_list`, `update_goal`,
  `use_skill`, `web_fetch`, `write_file`.
- Kimi cataloged 22 model-facing tools:
  `apply_patch`, `communicate`, `compact`, `delegate`, `delegate_send`,
  `edit_file`, `find_session_transcripts`, `glob`, `grep`, `job_list`,
  `job_read_output`, `job_stop`, `job_watch`, `list_dir`, `read_file`,
  `read_session_transcript`, `shell`, `task_list`, `update_goal`,
  `use_skill`, `web_fetch`, `write_file`.
- The initial experiment draft used `moonshot/kimi-k2-0905`, which the runner
  rejected as `unknown provider type "moonshot"`. The executable model id in
  this repo is `kimi/kimi-for-coding`; commands in this journal were corrected.
- Classification: `availability`/catalog baseline, no failure.

### E01 - CLI Harness Observer Callback Cancellation

Hypothesis: the current noninteractive CLI runner can install and deliver a
watch, but it closes the parent session quickly enough that the watch-resumed
observer is cancelled before it can call `delegate_send`.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe job_watch.observer_callback --out /tmp/serf-fluency-issue-e01-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe job_watch.observer_callback --out /tmp/serf-fluency-issue-e01-kimi
```

Artifacts:

- GPT result directory:
  `/tmp/serf-fluency-issue-e01-openai/job_watch.observer_callback/rep-01`
- GPT parent session: `01KVK0XEJKXYGBWBPJ1X52YBA7`
- GPT observer session: `01KVK0Y2DQX738W7MTDR8S49ZG`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e01-kimi/job_watch.observer_callback/rep-01`
- Kimi parent session: `01KVK0YX8SZ73N7JAQ9YRBYEQ1`
- Kimi observer session: `01KVK0Z1J7CE44M53X1WF5JDXZ`
- Doctor commands used:
  - `go run ./cmd/serf-doctor tree <parent> --state-dir <state> --observers`
  - `go run ./cmd/serf-doctor watches <parent> --state-dir <state>`
  - `go run ./cmd/serf-doctor transcript <observer> --state-dir <state> -format outline`
  - `go run ./cmd/serf-doctor transcript <observer> --state-dir <state> -count delegate_send`
  - `go run ./cmd/serf-doctor transcript <observer> --state-dir <state> -count communicate`

Result:

- Complete.
- GPT runner result: failed, `delegate:1`, `job_watch:1`, `read_file:2`.
  Findings were missing expected `communicate` in the parent and missing final
  `RESULT_JOB_WATCH`.
- Kimi runner result: failed, `delegate:1`, `job_watch:1`, `read_file:1`.
  Findings were missing expected `communicate` in the parent and missing final
  `RESULT_JOB_WATCH`.
- Both parent sessions had one observer child, and both child sessions ended
  `stopped`.
- `serf-doctor watches` showed one delivered watch frame for both models:
  - GPT: `watch_01KVK0YD510J697PZ2TH92RRVZ`, one delivered frame, no self-loop.
  - Kimi: `watch_01KVK0ZCW0GTGNTNA4MWCF7E70`, one delivered frame, no self-loop.
- Both observer transcripts had the delivered watch frame as user input, then a
  system interruption reminder before any `delegate_send`.
- `delegate_send` count was `0` for both observer sessions.
- `communicate` count was `1` for both observer sessions, so this reproduction
  did not include the earlier Kimi readiness rewrite; keep that isolated under
  E03.
- Root cause classification: `infra` for this probe shape. The current
  noninteractive CLI harness is not sufficient to verify callback completion.
  It can prove watch installation and delivery, but it cancels the resumed
  observer before callback work can finish.
- Next action: E02 must add or use a live-session/hub harness before treating
  `job_watch.observer_callback` as a model fluency failure.

### E02 - Live-Session Watch Callback Harness

Hypothesis: `job_watch.observer_callback` needs a live session/hub harness,
not a one-shot CLI harness, before it can distinguish model fluency from
teardown behavior.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --harness live --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe job_watch.observer_callback --post-turn-wait 45s --out /tmp/serf-fluency-issue-e02-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --harness live --model kimi/kimi-for-coding --probe job_watch.observer_callback --post-turn-wait 45s --out /tmp/serf-fluency-issue-e02-kimi
```

Artifacts:

- Runner change: `serf-fluency run --harness live`, which hosts a session
  in-process, wires `SetNotifyFunc` and `SetKickFunc`, and keeps the session
  alive for `--post-turn-wait` instead of closing immediately after the root
  turn.
- GPT result directory:
  `/tmp/serf-fluency-issue-e02-openai/job_watch.observer_callback/rep-01`
- GPT parent session: `01KVK19XD9VQAQDGZJQWHVNDPC`
- GPT observer session: `01KVK1A5RHFTTJ4F8GKG7D414X`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e02-kimi/job_watch.observer_callback/rep-01`
- Kimi parent session: `01KVK1F61FG97GFTRHJJ069B3F`
- Kimi observer session: `01KVK1FCWBW8RWZFK9EM3G3YNF`

Result:

- Complete.
- The live harness answered the E02 infrastructure question: GPT reached the
  observer callback path and produced the expected `RESULT_JOB_WATCH` marker.
  The one-shot CLI failure in E01 was therefore a harness limitation.
- GPT runner result: failed with meaningful fluency findings, not harness
  failure. Final output contained `RESULT_JOB_WATCH`; tool counts were
  `communicate:2`, `delegate:1`, `delegate_send:2`, `job_list:2`,
  `job_watch:2`, `read_file:2`.
- GPT findings:
  - `job_watch` had one validation/runtime error before repair.
  - The parent used `job_list` twice while waiting.
  - The parent read `watch-trigger.txt` once before the watch was installed,
    then read it again after installing the repaired watch.
  - The parent sent extra `delegate_send` messages to the observer instead of
    simply installing the watch and waiting for callback delivery.
- GPT doctor evidence:
  - `serf-doctor watches` showed one delivered watch frame and no self-loop:
    `watch_01KVK1BHGPAFFV3RZA3Z24RFKY`.
  - The observer was idle at the end, not stopped by parent teardown.
  - The parent transcript completed through `communicate`, then emitted a
    second completion after the observer delegate job notification.
- Kimi runner result: failed before watch installation. Final output was a
  `communicate` payload saying the observer was ready and that it was now
  creating the watch, but no `job_watch` or `read_file` call followed.
- Kimi doctor evidence:
  - No watches were recorded.
  - Parent transcript: `delegate`, then `communicate`, then stop.
  - Observer transcript: `communicate` readiness only.
- Root cause classification:
  - E02 harness gap is fixed by the live harness.
  - GPT now exposes `arguments`, `polling`/`churn`, and parent over-control
    issues for E04/E05/E03.
  - Kimi exposes a newly split issue: `communicate` used as a premature
    terminal result while work remains. Track this under E13.

### E03 - Observer Readiness Wait Semantics

Hypothesis: observer sidecars should communicate readiness with an active wait
state and then respond to the delivered watch frame without polling. A model
that rewrites the delegated task to `await_reply=false` is not fluent even if
the parent installed the watch.

Commands:

```sh
# No new model run. Analyze E02 live callback transcripts with serf-doctor.
```

Artifacts:

- GPT live callback result:
  `/tmp/serf-fluency-issue-e02-openai/job_watch.observer_callback/rep-01`
- Kimi live callback result:
  `/tmp/serf-fluency-issue-e02-kimi/job_watch.observer_callback/rep-01`

Result:

- Partial.
- GPT observer readiness and callback worked under the live harness: the
  observer used `communicate` for readiness, received the watch frame, then
  called `delegate_send(to="caller")`.
- GPT observer did not poll; the polling happened in the parent (`job_list:2`).
- GPT parent over-controlled the observer by sending extra `delegate_send`
  messages before the real watch callback arrived, so this is not a clean
  observer-only success.
- Kimi E02 is inconclusive for observer wait semantics because the parent
  stopped after a premature `communicate` and never installed a watch.
- The earlier Kimi readiness rewrite to `await_reply=false` did not reproduce
  in E01 or E02. Keep it as a watch item, but do not fix around a single stale
  observation.
- Next action: rerun E03 after fixing or reducing the parent workflow issues
  tracked by E04, E05, and E13.

### E04 - `job_watch` Caller Argument Shape

Hypothesis: GPT sees `send.include_excerpt` and applies it to `target="caller"`
even though caller/session notifications do not accept it. That is an argument
fluency/schema affordance issue, not a runtime failure.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe job_watch.observer_callback --repetitions 3 --out /tmp/serf-fluency-issue-e04-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe job_watch.observer_callback --repetitions 3 --out /tmp/serf-fluency-issue-e04-kimi
```

Artifacts:

- GPT result directory:
  `/tmp/serf-fluency-issue-e04-openai/job_watch.observer_callback`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e04-kimi/job_watch.observer_callback`
- GPT sessions:
  - rep 1: `01KVK1Q0A7FA2HTAE4T1FW2MVA`
  - rep 2: `01KVK1QSR6STQH4XTFYT44GVNH`
  - rep 3: `01KVK1RY8J13CD9VRXGABAAVJH`
- Kimi sessions:
  - rep 1: `01KVK1SSDTZ233MDF1WHZ8AJR0`
  - rep 2: `01KVK1T9W37YMRPTWQPZVJR305`
  - rep 3: `01KVK1TT5VRWY288KV1RWV8T54`

Result:

- Complete.
- GPT had `job_watch` argument/tool errors in 1 of 3 repetitions. The failing
  rep made three `job_watch` attempts and recorded two `job_watch` errors.
- GPT rep 2 concrete repair path:
  - First failed attempt included an invented top-level `response_length`, which
    the strict schema rejected as an additional property.
  - Second failed attempt removed `response_length` but kept
    `send.include_excerpt=true` for `target="caller"`, which the runtime
    rejected because excerpts only apply to concrete job targets.
  - Third attempt removed `include_excerpt` and installed the watch.
- Kimi had zero `job_watch` errors across 3 repetitions. Each rep made exactly
  one `job_watch` call.
- Classification: `arguments`, provider/model-specific. GPT is borrowing
  parameters from adjacent tools and sometimes applying job-target fields to
  caller/session-target watches. Kimi does not show the same argument issue in
  this sample.
- Next action: improve `job_watch` schema/tool affordance so GPT has a smaller
  invalid-argument surface. A likely YAGNI-friendly fix is documentation/schema
  pressure around `include_excerpt` and avoiding generic inherited fields like
  `response_length`; do not add conditional schema machinery unless simpler
  affordance changes fail.

### E05 - Waiting Without Polling

Hypothesis: when the runtime can deliver a watch frame, the fluent behavior is
to wait or callback, not repeatedly inspect jobs with `job_list`,
`job_read_output`, or shell polling.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe jobs.control_lifecycle --repetitions 3 --out /tmp/serf-fluency-issue-e05-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe jobs.control_lifecycle --repetitions 3 --out /tmp/serf-fluency-issue-e05-kimi
```

Artifacts:

- GPT job lifecycle result directory:
  `/tmp/serf-fluency-issue-e05-openai/jobs.control_lifecycle`
- Kimi job lifecycle result directory:
  `/tmp/serf-fluency-issue-e05-kimi/jobs.control_lifecycle`
- GPT live callback polling evidence from E02:
  `/tmp/serf-fluency-issue-e02-openai/job_watch.observer_callback/rep-01`

Result:

- Complete.
- `jobs.control_lifecycle` was clean across both models:
  - GPT reps 1-3 each used `shell:1`, `job_list:1`,
    `job_read_output:1`, `job_stop:1`, `communicate:2`.
  - Kimi reps 1-3 each used `shell:1`, `job_list:1`,
    `job_read_output:1`, `job_stop:1`, `communicate:1`.
- The broad-run `jobs.control_lifecycle` noise did not reproduce in this
  focused sample.
- The polling/churn issue remains in the live callback workflow, not in basic
  job lifecycle control. GPT E02 used `job_list:2` while waiting for observer
  callback delivery.
- Classification: `polling`/`churn`, workflow-specific. The model reaches for
  job inspection when it is uncertain how the observer callback will arrive.
- Next action: improve positive tool/workflow guidance around the happy path:
  install watch, trigger event, wait for callback delivery, then audit only if
  the callback does not arrive or the task asks for audit evidence.

### E06 - Explicit URL Fetch Tool Selection

Hypothesis: GPT treats familiar public URLs as answerable from memory and does
not select `web_fetch`, even when the user asks for the tool. Kimi selected the
tool in the broad run, so this is likely provider/model-specific selection
behavior.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe web_fetch.example --repetitions 3 --out /tmp/serf-fluency-issue-e06-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe web_fetch.example --repetitions 3 --out /tmp/serf-fluency-issue-e06-kimi
```

Artifacts:

- GPT result directory:
  `/tmp/serf-fluency-issue-e06-openai/web_fetch.example`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e06-kimi/web_fetch.example`

Result:

- Complete.
- GPT selected `web_fetch` in 1 of 3 repetitions.
  - Rep 1 passed with `web_fetch:1`, `communicate:1`.
  - Reps 2 and 3 failed selection with `communicate:1` and no `web_fetch`.
  - Both failed reps still answered correctly from model knowledge and included
    `RESULT_WEB_FETCH`, which makes this a pure tool-selection failure.
- Kimi selected `web_fetch` in 3 of 3 repetitions.
  - Reps 1 and 3 passed.
  - Rep 2 called `web_fetch` but omitted the required `RESULT_WEB_FETCH`
    marker, so the failure was `interpretation`, not tool selection.
- Classification:
  - GPT: `selection`; explicit positive prompting and tool description are not
    enough to force URL-fetch tool use for a familiar public URL.
  - Kimi: mostly fluent selection; one task-output marker miss.
- Next action: test an unfamiliar URL variant before changing global prompts.
  If GPT only skips familiar URLs, the fix is likely a stronger explicit-tool
  contract or tool-choice affordance, not a `web_fetch` runtime change.

### E07 - `communicate` Versus Plain Assistant Message

Hypothesis: after the result-tool schema and positive prompting changes,
models should end through `communicate`, and should no longer need repair for
the `output` envelope on ordinary tasks.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe communicate.final_message --repetitions 3 --out /tmp/serf-fluency-issue-e07-openai-communicate
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe communicate.final_message --repetitions 3 --out /tmp/serf-fluency-issue-e07-kimi-communicate
```

Artifacts:

- GPT result directory:
  `/tmp/serf-fluency-issue-e07-openai-communicate/communicate.final_message`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e07-kimi-communicate/communicate.final_message`

Result:

- Complete.
- GPT passed 3 of 3 repetitions with exactly `communicate:1`.
- Kimi passed 3 of 3 repetitions with exactly `communicate:1`.
- Classification: no current `plain_message` issue in the dedicated final
  message probe.
- The Kimi E02 failure is not inability to call `communicate`; it is using the
  result tool as a premature progress update in a multi-step workflow. That is
  tracked separately under E13.

### E08 - `list_dir` Alias Semantics

Hypothesis: OpenAI's model-facing `list_dir` surface currently maps to
canonical `glob`, while Kimi receives canonical `list_dir`. This can pass the
task while still making tool fluency reports confusing.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model openai/gpt-5.4-mini
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model kimi/kimi-for-coding
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe list_dir.inventory --out /tmp/serf-fluency-issue-e08-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe list_dir.inventory --out /tmp/serf-fluency-issue-e08-kimi
```

Artifacts:

- GPT result directory:
  `/tmp/serf-fluency-issue-e08-openai/list_dir.inventory`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e08-kimi/list_dir.inventory`
- Catalog command output captured in terminal for both models.

Result:

- Complete.
- Both models passed `list_dir.inventory`.
- GPT result counts: `glob:1`, `communicate:1`.
- Kimi result counts: `list_dir:1`, `communicate:1`.
- Catalog split:
  - GPT exposes OpenAI-facing names such as `exec_command` and `grep_files`,
    and its model-facing `list_dir` path records canonical `glob`.
  - Kimi exposes canonical `shell`, `grep`, `glob`, and `list_dir`.
- Classification: reporting/schema clarity, not task failure. Current results
  can make a `list_dir` probe look like it was satisfied by the wrong canonical
  tool on OpenAI. Future catalog output should include both model-facing and
  canonical names so fluency reports do not require prior provider-profile
  knowledge.

### E09 - `task_list` Churn

Hypothesis: the current task-list prompt or reminder loop encourages extra
`task_list` calls on a tiny planning task. The task passes, but high call count
is churn.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe task_list.plan --repetitions 3 --out /tmp/serf-fluency-issue-e09-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe task_list.plan --repetitions 3 --out /tmp/serf-fluency-issue-e09-kimi
```

Artifacts:

- GPT result directory:
  `/tmp/serf-fluency-issue-e09-openai/task_list.plan`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e09-kimi/task_list.plan`
- GPT rep 1 session: `01KVK2DEW1CJ6Z506FSYQZ23W8`
- Kimi rep 1 session: `01KVK2FM7A8EEJWJT67R1DVMV6`

Result:

- Complete.
- GPT used `task_list:4`, `communicate:1` in all 3 repetitions.
- Kimi used:
  - rep 1: `task_list:4`, `list_dir:1`, `communicate:1`
  - rep 2: `task_list:3`, `communicate:1`
  - rep 3: `task_list:4`, `list_dir:1`, `communicate:1`
- Doctor transcripts show the extra task-list calls are driven by task-list
  workflow reminders, not random repeated inspection:
  - The first calls create/update the two-step plan.
  - Marking task 1 done injects a `<CURRENT-TASK id="2">` reminder.
  - Completing the final task injects the "completed all tasks" reminder before
    final `communicate`.
- Classification: workflow-induced `churn`, with a probe-design caveat. The
  probe asks for a plan "with one item in progress" and then asks the agent to
  finish the probe, so the model treats the plan as executable task state.
- Next action: add explicit runner metrics for expected maximum call counts
  before treating this as a prompt/tool bug. If the desired contract is "create
  a plan but do not execute it", that should be a separate task-list probe with
  a different semantic oracle.

### E10 - Job Lifecycle Churn

Hypothesis: `jobs.control_lifecycle` currently succeeds while using extra
shell and job-control calls. The experiment should separate required job
operations from unrelated escape hatches and redundant inspection.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe jobs.control_lifecycle --repetitions 3 --out /tmp/serf-fluency-issue-e10-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe jobs.control_lifecycle --repetitions 3 --out /tmp/serf-fluency-issue-e10-kimi
```

Artifacts:

- Reused E05 focused job lifecycle evidence:
  - `/tmp/serf-fluency-issue-e05-openai/jobs.control_lifecycle`
  - `/tmp/serf-fluency-issue-e05-kimi/jobs.control_lifecycle`

Result:

- Complete.
- Focused reruns did not reproduce broad-run job lifecycle churn.
- GPT reps 1-3 each used exactly one required job lifecycle call:
  `shell:1`, `job_list:1`, `job_read_output:1`, `job_stop:1`, plus
  `communicate:2`.
- Kimi reps 1-3 each used exactly one required job lifecycle call:
  `shell:1`, `job_list:1`, `job_read_output:1`, `job_stop:1`, plus
  `communicate:1`.
- Classification: no current issue in the focused sample. Keep watching this in
  full-suite regressions, but do not optimize based on the earlier noisy pass
  alone.

### E11 - Unavailable Tool Skip Semantics

Hypothesis: `web_search.current` should be `skipped_unavailable` on model
surfaces that do not advertise Serf's `web_search`, and that skip should not
hide a catalog exposure bug.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe web_search.current --out /tmp/serf-fluency-issue-e11-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe web_search.current --out /tmp/serf-fluency-issue-e11-kimi
```

Artifacts:

- GPT result directory:
  `/tmp/serf-fluency-issue-e11-openai/web_search.current`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e11-kimi/web_search.current`

Result:

- Complete.
- GPT status: `skipped_unavailable`, no tool calls.
- Kimi status: `skipped_unavailable`, no tool calls.
- Classification: healthy `availability` skip. The probe correctly reflects
  that Serf's `web_search` function is not advertised on these model surfaces.

### E12 - Full-Suite Regression

Hypothesis: after focused fixes, the full suite should pass or have only
understood, logged skips. Regressions in unrelated probes mean the fix changed
tool selection globally.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe all --out /tmp/serf-fluency-issue-e12-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe all --out /tmp/serf-fluency-issue-e12-kimi
```

Artifacts:

- GPT result directory:
  `/tmp/serf-fluency-issue-e12-openai`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e12-kimi`
- GPT notable sessions:
  - `job_watch.observer_callback`: `01KVK38P14ZBZEYZ6MESCT8MX1`
  - `web_fetch.example`: `01KVK3D12Z6BY5WJA3TYQ21Z7K`
- Kimi notable sessions:
  - `apply_patch.happy_path`: `01KVK367PGPVXH8ZSY87VA6HHT`
  - `grep.find_token`: `01KVK38TP0X128YNRJVF92R2HF`
  - `job_watch.observer_callback`: `01KVK3925MDJN5J9MAXG7GXYM2`
  - `web_fetch.example`: `01KVK3MVRTJX963KMMNSFAQ4XD`

Result:

- Complete.
- GPT full suite: 17 passed, 2 failed, 1 skipped unavailable.
- GPT failures:
  - `job_watch.observer_callback`: failed under the CLI harness with
    `delegate:1`, `job_watch:2`, `read_file:2`, no final `communicate`, and
    one `job_watch` error. Doctor/error evidence shows the known
    `include_excerpt` misuse for a caller/session-target watch:
    `include_excerpt requires a concrete job target`.
  - `web_fetch.example`: called `web_fetch`, then produced the correct final
    marker, but first called `communicate` with both `message` and
    `output.message` empty. Classification: `arguments` on result-tool
    envelope, not web-fetch selection.
- GPT skipped `web_search.current` because the tool is unavailable.
- Kimi full suite: 15 passed, 4 failed, 1 skipped unavailable.
- Kimi failures:
  - `apply_patch.happy_path`: reached the correct final content and
    `RESULT_APPLY_PATCH`, but the first `apply_patch` call omitted the required
    `*** Begin Patch` wrapper and had to repair. Classification: `arguments`.
  - `grep.find_token`: found the token with `shell` instead of `grep`.
    Classification: `selection` plus forbidden-tool `churn`.
  - `job_watch.observer_callback`: read unrelated session transcripts before
    delegating, then hit the known CLI-harness observer interruption path after
    watch delivery. Classification: mixed `churn` plus `infra`; use E02/E13
    live-harness results for callback fluency.
  - `web_fetch.example`: selected `web_fetch` but omitted the required
    `RESULT_WEB_FETCH` marker. Classification: `interpretation`.
- Kimi skipped `web_search.current` because the tool is unavailable.
- Cross-suite conclusion:
  - The primary sidecar failures remain concentrated in `job_watch` workflow
    affordance, not missing tool availability.
  - The CLI harness is still useful as a broad regression tripwire, but it is
    not the acceptance harness for observer callback completion.
  - Other tool fluency gaps are small and specific: result-tool empty-message
    repair, Kimi patch format repair, Kimi shell-over-grep selection, and Kimi
    final-marker omission for `web_fetch`.

### E13 - Premature `communicate` As Progress Update

Hypothesis: some models use `communicate` as a progress/status update even
when they still intend to do more tool work. Because `communicate` is the result
tool, this ends the turn and strands the task. The model should use ordinary
tool calls until it has a user-visible final result, or use `communicate` with
`await_reply=true` only when it is intentionally waiting for external input.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --harness live --model kimi/kimi-for-coding --probe job_watch.observer_callback --repetitions 3 --post-turn-wait 15s --timeout 8m --out /tmp/serf-fluency-issue-e13-kimi
go run ./tools/tool-fluency/cmd/serf-fluency run --harness live --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe job_watch.observer_callback --repetitions 3 --post-turn-wait 15s --timeout 8m --out /tmp/serf-fluency-issue-e13-openai
```

Artifacts:

- Initial evidence from E02 Kimi:
  `/tmp/serf-fluency-issue-e02-kimi/job_watch.observer_callback/rep-01`
- Kimi result directory:
  `/tmp/serf-fluency-issue-e13-kimi/job_watch.observer_callback`
- Kimi sessions:
  - rep 1: `01KVK2M024PT37NM1RCJPASJ0Z`
  - rep 2: `01KVK2WG1KM08GQVKG6VQKFSY3`
  - rep 3: `01KVK2XAFZKEWTNSGJSCZAFDG7`
- GPT result directory:
  `/tmp/serf-fluency-issue-e13-openai/job_watch.observer_callback`
- GPT sessions:
  - rep 1: `01KVK2YV9BKQNFSWXR3KG3T33A`
  - rep 2: `01KVK312KW1KAK7EZK2NYNC5D7`
  - rep 3: `01KVK333G1HEJWDX6VS245FYE6`

Result:

- Complete.
- The original Kimi E02 failure, where the parent used `communicate` as a
  progress update before installing the watch, did not reproduce in this
  focused 3-repetition live run.
- Kimi passed 2 of 3 repetitions with the minimal happy path:
  `communicate:1`, `delegate:1`, `job_watch:1`, `read_file:1`.
- Kimi rep 1 reached the correct final result but failed the strict probe
  because the parent used `job_read_output:2` to inspect delegate state. Doctor
  outline: `delegate`, `job_read_output`, `job_watch`, `read_file`, callback
  steering `WATCH_OBSERVED`, another `job_read_output`, then two final
  `communicate` calls after delegate completion notifications.
- GPT failed all 3 repetitions, but all failures were already-covered callback
  workflow issues rather than plain-message or premature-result-tool failures:
  - rep 1: `job_watch:2` with one `job_watch` error, `job_read_output:1`,
    duplicate final `communicate`.
  - rep 2: `job_list:2`, duplicate final `communicate`.
  - rep 3: `job_watch:2` with one `job_watch` error, duplicate final
    `communicate`.
- Doctor transcripts confirm the callback itself was delivered in every GPT
  rep. The sidecar sent `WATCH_OBSERVED`; GPT then either finalized immediately
  and finalized again after delegate completion, or audited job state before
  finalizing.
- Classification:
  - Premature `communicate`: not reproduced in focused sample; keep as watch
    item, but do not fix from the stale E02 single run alone.
  - GPT: `arguments`, `churn`, and duplicate completion in the observer
    callback workflow.
  - Kimi: mostly clean callback path, with one `churn` failure from
    job-output inspection.
- Next action: target the root workflow affordance instead of adding "do not"
  prompt patches. The positive happy path should make the completion contract
  obvious: start observer, install watch, trigger event, accept callback
  steering as completion evidence, then produce one final `communicate`.
