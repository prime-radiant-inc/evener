---
name: tool-fluency
description: Use when designing, running, or diagnosing Serf tool-fluency experiments for a real built-in, plugin, or newly added tool. Covers probe manifests, model comparisons, semantic oracles, and interpreting fluency failures without ad-hoc transcript parsing.
---

# Tool Fluency

Use this skill when Jesse asks whether a model uses a Serf tool fluently, asks
for tool-fluency scenarios, or adds a new real tool that needs model-facing
coverage.

## Required reading

1. Read `tools/tool-fluency/README.md`.
2. Read `docs/agentic-testing.md` if the Go runner cannot exercise the live
   session shape you need.
3. For session/job/watch forensics, use `serf-doctor` or the `agent/doctor`
   package. Do not hand-parse transcript JSONL.

## Core rules

- Test real behavior, not prompt/doc strings.
- Use structured probes and semantic oracles. Do not make assertions whose main
  claim is "the transcript contains this phrase."
- Do not write custom Python or jq glue to count tool calls, watches, or
  delegate sends. Improve the Go runner or `serf-doctor` when a needed
  inspection is missing.
- Separate task success from fluency. A run can complete the task while still
  showing tool churn, invalid first arguments, polling, or wrong-tool recovery.
- Treat unavailable-by-design tools as `skipped_unavailable`, not failures.
- For future tools, add a probe only after the tool exists in the runtime tool
  catalog.

## Workflow

1. Identify the exact tool or workflow under test.
2. Confirm which contexts should expose it: root, leaf subagent, coordinator
   subagent, bundled agent role, provider behavior tag, or plugin/custom agent.
3. Define the smallest probe that forces the intended tool decision.
4. Define a semantic oracle:
   - artifact/file state;
   - structured tool result;
   - session/job/watch/delegate state through `agent/doctor`;
   - public final token only when it is the actual user-visible contract;
   - custom Go verifier only when reusable oracles are insufficient.
5. Define forbidden calls and fluency metrics separately from pass/fail.
6. Run the probe across the requested models and repetitions.
7. Inspect every failure for root cause before changing prompts, schemas, or
   probes.

## Runner commands

Catalog currently advertised tools:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model openai/gpt-5.4-mini
```

Run all current probes:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --build \
  --model openai/gpt-5.4-mini \
  --fast-cheap-model openai/gpt-5.4-mini \
  --clear-openai-api-key \
  --probe all
```

Run one probe with an existing binary:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --serf-bin /tmp/serf \
  --model openai/gpt-5.4-mini \
  --probe read_file.happy_path
```

Run one probe with an experimental prompt append:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --serf-bin /tmp/serf \
  --model openai/gpt-5.4-mini \
  --probe read_file.happy_path \
  --system-prompt-append tools/tool-fluency/variants/example.md
```

Use prompt-append variants for experiments, not as a substitute for product
fixes. Keep each variant small and atomic, and run it against the same
probe/model/harness as the failure you are trying to explain.

Run a callback or notification probe with a live session harness:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --harness live \
  --model openai/gpt-5.4-mini \
  --fast-cheap-model openai/gpt-5.4-mini \
  --clear-openai-api-key \
  --probe job_watch.observer_callback \
  --post-turn-wait 45s
```

Use `--harness live` when a probe needs `SetNotifyFunc` or `SetKickFunc`
wiring, such as observer callbacks, watch deliveries, or notification-driven
parent resumes. Use the default CLI harness for ordinary one-shot tool probes.

## Failure classification

Use these categories in reports:

- `schema`: tool definition, strict mode, JSON schema, or provider conversion
  made the right call hard or impossible.
- `availability`: tool was missing or incorrectly exposed in the context.
- `selection`: model chose the wrong tool or avoided the intended tool.
- `arguments`: model selected the right tool but supplied invalid or weak args.
- `repair`: model failed to recover from a meaningful validation error.
- `interpretation`: model got a good tool result but used it incorrectly.
- `churn`: extra calls that did not contribute to the task.
- `polling`: model repeatedly inspected state instead of waiting for the runtime
  signal or callback.
- `plain_message`: model emitted assistant text where `communicate` was the
  required channel.
- `infra`: provider quota, hub crash, bad credentials, timeout unrelated to
  model behavior.

## Reporting format

Report both the verdict and the evidence:

```text
probe: job_watch.observer_callback
model: openai/gpt-5.4-mini
status: failed
task_success: true
fluency:
  first_call_tool: delegate
  validation_errors: 1
  forbidden_tool_calls: 0
  polling_calls: 0
root_cause: schema
evidence:
  session_id: 01...
  state_dir: /tmp/...
  doctor:
    parent job_list count: 0
    parent job_read_output count: 0
    observer communicate count: 1
fix: make job_watch optional field X non-strict / clarify repair message
```

Healthy runs should have no findings. Do not manufacture "looks good" findings.

## Forensics

The runner writes `result.json`, `stdout.txt`, and `stderr.ndjson` under each
probe repetition directory. For session/job/watch inspection, prefer:

```sh
go run ./cmd/serf-doctor transcript "$SID" --state-dir "$STATE" -format outline
go run ./cmd/serf-doctor transcript "$SID" --state-dir "$STATE" -count communicate
go run ./cmd/serf-doctor tree "$SID" --state-dir "$STATE" --observers
go run ./cmd/serf-doctor watches "$SID" --state-dir "$STATE"
```

If a live observer/callback scenario needs a session to remain open across
notification turns, use `serf-fluency run --harness live`. If that harness
cannot represent the needed shape, use the live scenario process in
`docs/agentic-testing.md`, or improve the Go runner instead of accumulating
shell glue.
