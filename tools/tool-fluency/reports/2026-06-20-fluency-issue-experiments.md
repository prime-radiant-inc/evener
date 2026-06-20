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
| E03 | Observer sidecar readiness should wait for watch frames fluently | `job_watch.observer_callback` | GPT, Kimi | planned |
| E04 | `job_watch` caller notification argument shape confuses GPT | `job_watch.observer_callback` | GPT, Kimi | planned |
| E05 | Waiting workflows should not poll job state | `job_watch.observer_callback`, `jobs.control_lifecycle` | GPT, Kimi | planned |
| E06 | Explicit URL fetch request should select `web_fetch` | `web_fetch.example` | GPT, Kimi | planned |
| E07 | `communicate` should be selected instead of assistant plain text | `communicate.no_plain_message`, `read_file.happy_path`, `task_list.plan` | GPT, Kimi | planned |
| E08 | Provider aliasing should not hide `list_dir` semantics | `list_dir.inventory`, `catalog` | GPT, Kimi | planned |
| E09 | `task_list` should not churn on a tiny planning task | `task_list.plan` | GPT, Kimi | planned |
| E10 | Job lifecycle workflow should avoid shell escapes and redundant job calls | `jobs.control_lifecycle` | GPT, Kimi | planned |
| E11 | Unavailable tools should skip cleanly, not create fake failures | `web_search.current`, `catalog` | GPT, Kimi | planned |
| E12 | Full-suite regression after focused fixes | `all` | GPT, Kimi | planned |
| E13 | `communicate` should not be used as a progress update while work remains | `job_watch.observer_callback`, `communicate.no_plain_message` | GPT, Kimi | planned |

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
# Depends on E02 harness support, because the one-shot CLI cancels the resume.
```

Artifacts:

- pending

Result:

- pending

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

- pending

Result:

- pending

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

- pending

Result:

- pending

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

- pending

Result:

- pending

### E07 - `communicate` Versus Plain Assistant Message

Hypothesis: after the result-tool schema and positive prompting changes,
models should end through `communicate`, and should no longer need repair for
the `output` envelope on ordinary tasks.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe communicate.no_plain_message --repetitions 3 --out /tmp/serf-fluency-issue-e07-openai-communicate
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model kimi/kimi-for-coding --probe communicate.no_plain_message --repetitions 3 --out /tmp/serf-fluency-issue-e07-kimi-communicate
```

Artifacts:

- pending

Result:

- pending

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

- pending

Result:

- pending

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

- pending

Result:

- pending

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

- pending

Result:

- pending

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

- pending

Result:

- pending

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

- pending

Result:

- pending

### E13 - Premature `communicate` As Progress Update

Hypothesis: some models use `communicate` as a progress/status update even
when they still intend to do more tool work. Because `communicate` is the result
tool, this ends the turn and strands the task. The model should use ordinary
tool calls until it has a user-visible final result, or use `communicate` with
`await_reply=true` only when it is intentionally waiting for external input.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --harness live --model kimi/kimi-for-coding --probe job_watch.observer_callback --repetitions 3 --post-turn-wait 45s --out /tmp/serf-fluency-issue-e13-kimi
go run ./tools/tool-fluency/cmd/serf-fluency run --harness live --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe job_watch.observer_callback --repetitions 3 --post-turn-wait 45s --out /tmp/serf-fluency-issue-e13-openai
```

Artifacts:

- Initial evidence from E02 Kimi:
  `/tmp/serf-fluency-issue-e02-kimi/job_watch.observer_callback/rep-01`

Result:

- pending
