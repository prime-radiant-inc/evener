# Tool Fluency Experiments

Framework for measuring whether models use Serf tools fluently. This is for
real tools exposed by Serf now or added later. It is not a framework for testing
imaginary tools.

The current implementation is a Go runner at
`tools/tool-fluency/cmd/serf-fluency`. The operational skill for agents lives at
`docs/skills/tool-fluency/SKILL.md`.

## Goal

Tool fluency is the model-facing contract between a prompt, a tool schema, the
runtime, and the model. A model is fluent when it:

- selects the right tool for the job;
- supplies valid arguments with minimal repair;
- interprets tool results correctly;
- stops using tools when no tool is needed;
- recovers from expected validation failures;
- avoids unrelated inspection, polling, transcript reads, or shell escapes.

The framework lets us run the same probe set across models, providers, agent
roles, and Serf revisions, then compare behavior without hand-written glue.

## Non-goals

- Do not replace unit tests. Unit tests own deterministic runtime contracts.
- Do not make markdown scenario cards executable. Scenario cards remain human
  reproductions; fluency probes are structured data.
- Do not test that prompts or docs contain particular strings.
- Do not parse transcript JSONL with ad-hoc scripts. Use Serf's Go packages,
  especially `agent/doctor`, or structured runtime events.
- Do not require every future tool to invent a bespoke runner. Future tools add
  a small probe manifest and, when needed, a semantic oracle.

## Layout

```text
tools/tool-fluency/
  README.md
  cmd/serf-fluency/        # Go runner
  probes/
    read_file.yaml
    write_file.yaml
    shell.yaml
    delegate.yaml
    job_watch.yaml
    ...
  reports/                 # committed run summaries
  results/                 # gitignored local run output
```

The runner is Go. It imports Serf packages directly for cataloging and result
inspection and should not force agents to write custom Python, jq pipelines, or
one-off JSONL parsers.

## Quick Start

Catalog the model-facing tools for a provider/model:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency catalog --model openai/gpt-5.4-mini
```

Run every current probe with a fresh `serf` binary:

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --build \
  --model openai/gpt-5.4-mini \
  --fast-cheap-model openai/gpt-5.4-mini \
  --clear-openai-api-key \
  --probe all \
  --out /tmp/serf-fluency-openai
```

Use `--probe <id>` for a single probe. The runner writes per-repetition
`result.json`, `stdout.txt`, `stderr.ndjson`, plus run-level `results.jsonl` and
`summary.json`.

For callback or notification workflows, use the live in-process harness. The
default CLI harness intentionally matches one-shot `serf` behavior and closes
the session after the root turn; that is useful for ordinary tool probes but
cannot prove that a watch-resumed observer had time to complete callback work.

```sh
go run ./tools/tool-fluency/cmd/serf-fluency run \
  --harness live \
  --model openai/gpt-5.4-mini \
  --fast-cheap-model openai/gpt-5.4-mini \
  --clear-openai-api-key \
  --probe job_watch.observer_callback \
  --post-turn-wait 45s \
  --out /tmp/serf-fluency-live-job-watch
```

The live harness wires the same session notification and continuation callbacks
that `serf serve` wires, waits on runtime kicks, and then closes the session
after the bounded post-turn window. It is not a polling harness.

## Data model

### Suite

A suite selects probes, models, repetitions, and launch options. Suite manifests
are planned; the current runner selects probes with `--probe all` or
`--probe <id>`.

```yaml
schema: 1
name: core-smoke
models:
  - openai/gpt-5.4-mini
  - moonshot/kimi-k2-0905
repetitions: 3
launch:
  harness: serf
  agent: default
  reasoning_effort: high
probes:
  - read_file.happy_path
  - grep.pick_over_shell
  - communicate.no_plain_message
  - job_watch.observer_callback
```

### Probe

A probe is the smallest meaningful experiment. It should exercise one tool
decision or one short tool workflow.

```yaml
schema: 1
id: read_file.happy_path
tool: read_file
contexts: [root, leaf_subagent]
intent: "Read a known fixture and report its token."
prompt: |
  Read fixture.txt and report the TOKEN value exactly.
fixture:
  files:
    fixture.txt: "TOKEN=FLUENCY_123\n"
expect:
  calls:
    - tool: read_file
      args:
        file_path: fixture.txt
  forbidden_calls: [shell, grep, read_session_transcript]
  artifacts:
    - path: fixture.txt
      unchanged: true
  final_contains: "FLUENCY_123"
metrics:
  max_tool_calls: 2
  first_call_tool: read_file
```

### Future-tool probe

For a future real tool, the author adds a manifest after the tool exists. The
framework can infer schema and availability from the runtime catalog, but it
cannot infer the semantic oracle.

```yaml
schema: 1
id: new_tool.happy_path
tool: new_tool
contexts: [root]
prompt: |
  Use the project tool to inspect the prepared fixture and report its verdict.
fixture:
  files:
    input.txt: "case=valid\n"
expect:
  calls:
    - tool: new_tool
  semantic_oracle:
    type: command
    command: ["go", "test", "./..."]
metrics:
  max_validation_errors: 0
  max_unnecessary_calls: 1
```

If a future tool has no semantic oracle, the runner may still report schema,
availability, selection, and argument fluency, but the probe result must be
marked `semantic_unverified`.

## Tool catalog

The runner has a first-class catalog export. It should not scrape prompt text.

Current catalog records:

- model-facing tool name;
- description;
- strict mode.

Planned catalog records:

- canonical tool name;
- provider-facing name after profile renaming;
- JSON schema;
- available agent contexts;
- gating reason when unavailable;
- built-in vs plugin/custom source;
- result tool name when `communicate` is renamed.

The catalog is produced by the same registry/profile path that sessions use
(`agent/session_tool_registry.go`, profile tool definitions, runtime registered
tools). This catches prompt/schema drift before a live model run.

## Contexts

Every probe declares the contexts it applies to:

- `root`: default root session.
- `leaf_subagent`: typed or allowance-zero child that cannot delegate.
- `coordinator_subagent`: child with delegation allowance.
- `agent:<name>`: bundled or configured agent role.
- `provider:<behavior_tag>`: provider-specific surface such as Google
  function web search.

Unavailable-by-design is not a failure. The report should distinguish:

- `passed`;
- `failed`;
- `skipped_unavailable`;
- `blocked_infra`;
- `semantic_unverified`.

## Oracles

The runner should support a small set of reusable oracle types:

| Oracle | Purpose |
| --- | --- |
| `tool_call` | Tool was called, not called, or called with structured args. |
| `tool_result` | Tool result status or structured result has expected fields. |
| `artifact` | File exists, content changed, content unchanged, or command verifies workspace. |
| `session_state` | Final state is idle, awaiting input, closed, etc. |
| `doctor_count` | `agent/doctor.Count` reports structural call counts. |
| `doctor_tree` | Parent/delegate/observer topology is correct. |
| `doctor_watches` | Watch deliveries, drops, and self-loop verdicts are correct. |
| `custom_go` | A registered Go verifier for high-value tool-specific semantics. |

String matching belongs only at the public boundary, such as a required final
token or marker. It must not be the main assertion for a tool behavior.

## Metrics

Every run should write pass/fail plus fluency metrics:

- `task_success`;
- `semantic_verified`;
- `first_call_tool`;
- `expected_tool_calls`;
- `forbidden_tool_calls`;
- `unnecessary_tool_calls`;
- `validation_errors`;
- `repair_success`;
- `plain_message_count`;
- `turn_count`;
- `tool_call_count`;
- `latency_ms`;
- `input_tokens`;
- `output_tokens`;
- `estimated_cost`;
- `provider_errors`.

The summary should preserve raw per-repetition records. Averages hide the
failure modes we need to inspect.

## Built-in coverage plan

The initial `core.yaml` suite should cover each model-facing built-in tool with
one happy path and one negative or repair path where that is meaningful:

| Area | Tools | Probe shape |
| --- | --- | --- |
| Filesystem read | `read_file`, `list_dir`, `glob`, `grep` | Find fixture data without shell or transcript tools. |
| Filesystem write | `write_file`, `edit_file`, `apply_patch` | Mutate a fixture and verify exact workspace state. |
| Shell | `shell` | Use shell only when command execution is the task; avoid shell when a safer tool exists. |
| Communication | `communicate` | Use result tool for final/status/input request; no plain assistant message. |
| Tasks/skills | `task_list`, `use_skill` | Use when task structure or skill trigger requires it; avoid gratuitous use. |
| Jobs | `job_list`, `job_read_output`, `job_stop` | Recover/job-control workflows without polling loops. |
| Delegation | `delegate`, `delegate_send` | Foreground, background, idle send, callback, unavailable target repair. |
| Watches | `job_watch` | Caller notification, observer callback, invalid filter repair, no self-loop. |
| Web | `web_fetch`, `web_search` | Use only when enabled and current information is needed. |
| Transcripts | `find_session_transcripts`, `read_session_transcript` | Archive/audit use only; avoid as live delegate/watch working evidence. |

Workflow suites can combine tools after single-tool probes pass. Sidecar
fluency is a workflow suite, not just a `job_watch` unit.

## Current Runner Flow

1. Build a fresh `serf` binary when requested.
2. For OpenAI OAuth runs, clear inherited `OPENAI_API_KEY` when requested.
3. For each probe repetition, create a hermetic workdir and `SERF_STATE_DIR`.
4. Materialize fixtures.
5. Run noninteractive `serf --verbose` with the selected model/context.
6. Collect structured events and transcript data from the session state dir.
7. Evaluate tool-call, artifact, final-output, and tool-error expectations.
8. Write `result.json`, append `results.jsonl`, and store relevant transcripts
   and artifacts under the run directory.
9. Print a compact matrix summary and the exact commands/session IDs needed for
    forensic follow-up.

This CLI harness intentionally started small. It does not yet keep a session
alive after the initial noninteractive invocation to drive follow-up
`EntryNotification` turns. Observer sidecar probes can therefore expose both
model fluency and CLI/harness lifecycle gaps. A live-session or hub-backed
runner is the next step for full `job_watch` callback verification.

## Result shape

```json
{
  "schema": 1,
  "suite": "core-smoke",
  "probe": "read_file.happy_path",
  "model": "openai/gpt-5.4-mini",
  "repetition": 1,
  "status": "passed",
  "semantic_verified": true,
  "session_id": "01...",
  "state_dir": "/tmp/serf-fluency-...",
  "metrics": {
    "first_call_tool": "read_file",
    "tool_call_count": 2,
    "forbidden_tool_calls": 0,
    "validation_errors": 0
  },
  "findings": []
}
```

Failed results should include actionable findings. Healthy runs should have an
empty findings list.

## How to add coverage for a new real tool

1. Add or verify the tool in Serf's runtime registry.
2. Run catalog export and confirm the tool appears in the intended contexts.
3. Add a probe manifest under `tools/tool-fluency/probes/`.
4. Choose an oracle. Prefer artifact or structured-state verification. Use a
   custom Go verifier only when reusable oracle types cannot express the
   behavior.
5. Add the probe to the smallest relevant suite.
6. Run at least one cheap model and one target model.
7. Inspect every failure through the generated result directory and doctor data.
8. Fix the root cause in runtime schema, tool description, system prompt, or
   tool behavior. Do not "fix" the probe to match bad behavior.

## Next Improvements

1. Add a live-session or hub-backed harness that can drive notification turns
   after observer callbacks.
2. Expand the catalog with canonical names, provider aliases, JSON schema, and
   context availability.
3. Promote common checks into reusable oracles: first-call tool, max tool calls,
   doctor counts, doctor tree, doctor watches, and session state.
4. Add suite manifests after the single-probe runner shape has proven useful.
5. Add cross-model comparison summaries without hiding individual repetition
   failures.

Stop there until the reports have paid for themselves. More dashboards and
automatic probe generation are YAGNI until repeatable runs produce useful
failures.
