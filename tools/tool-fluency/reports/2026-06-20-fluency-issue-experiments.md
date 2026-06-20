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
   `openai/gpt-5.4-mini` and `moonshot/kimi-k2-0905`, unless the issue is
   provider-specific by design.

No experiment in this file should depend on ad-hoc Python, jq, or transcript
JSONL parsing. If an inspection is hard, improve the Go runner or
`serf-doctor`.

## Current Issue Matrix

| ID | Issue | Primary probe | Models | Status |
| --- | --- | --- | --- | --- |
| E00 | Baseline inventory and reproducibility | `catalog`, committed broad reports | GPT, Kimi | planned |
| E01 | CLI harness may cancel observer callbacks before callback work runs | `job_watch.observer_callback` | GPT, Kimi | planned |
| E02 | Live-session harness needed for true watch callback verification | `job_watch.observer_callback` | GPT, Kimi | planned |
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

## Experiment Log

### E00 - Baseline Inventory And Reproducibility

Hypothesis: the broad reports are sufficient as a baseline only if a fresh
catalog still matches the current branch and the known failures reproduce in
targeted runs.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model openai/gpt-5.4-mini
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model moonshot/kimi-k2-0905
```

Artifacts:

- pending

Result:

- pending

### E01 - CLI Harness Observer Callback Cancellation

Hypothesis: the current noninteractive CLI runner can install and deliver a
watch, but it closes the parent session quickly enough that the watch-resumed
observer is cancelled before it can call `delegate_send`.

Commands:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe job_watch.observer_callback --out /tmp/serf-fluency-issue-e01-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe job_watch.observer_callback --out /tmp/serf-fluency-issue-e01-kimi
```

Artifacts:

- pending

Result:

- pending

### E02 - Live-Session Watch Callback Harness

Hypothesis: `job_watch.observer_callback` needs a live session/hub harness,
not a one-shot CLI harness, before it can distinguish model fluency from
teardown behavior.

Commands:

```sh
# To be selected after E01 confirms the harness failure shape.
```

Artifacts:

- pending

Result:

- pending

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
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe job_watch.observer_callback --repetitions 3 --out /tmp/serf-fluency-issue-e04-kimi
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
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe jobs.control_lifecycle --repetitions 3 --out /tmp/serf-fluency-issue-e05-kimi
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
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe web_fetch.example --repetitions 3 --out /tmp/serf-fluency-issue-e06-kimi
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
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe communicate.no_plain_message --repetitions 3 --out /tmp/serf-fluency-issue-e07-kimi-communicate
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
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model moonshot/kimi-k2-0905
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model openai/gpt-5.4-mini --fast-cheap-model openai/gpt-5.4-mini --clear-openai-api-key --probe list_dir.inventory --out /tmp/serf-fluency-issue-e08-openai
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe list_dir.inventory --out /tmp/serf-fluency-issue-e08-kimi
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
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe task_list.plan --repetitions 3 --out /tmp/serf-fluency-issue-e09-kimi
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
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe jobs.control_lifecycle --repetitions 3 --out /tmp/serf-fluency-issue-e10-kimi
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
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe web_search.current --out /tmp/serf-fluency-issue-e11-kimi
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
go run ./tools/tool-fluency/cmd/serf-fluency run --build --model moonshot/kimi-k2-0905 --probe all --out /tmp/serf-fluency-issue-e12-kimi
```

Artifacts:

- pending

Result:

- pending
