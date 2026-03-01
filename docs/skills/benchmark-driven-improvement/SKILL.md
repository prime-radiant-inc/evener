---
name: benchmark-driven-improvement
description: Use when investigating serf benchmark failures, iterating on agent behavior against terminal-bench tasks, or diagnosing why serf fails specific coding challenges. Covers task extraction, local execution, transcript analysis, session interrogation, and fix iteration.
---

# Benchmark-Driven Improvement

## Overview

Systematic process for diagnosing and fixing serf failures on terminal-bench tasks. Run serf locally against extracted benchmark tasks, analyze transcripts, interrogate the agent about its decisions, then iterate on code/prompts.

**Core principle:** We're not hill-climbing on the benchmark. Terminal-bench is a proxy for good autonomous engineering. Fixes should improve general agent capability, not game specific tasks.

## Philosophy

- A fix that passes one task but breaks the agent's general reasoning is a bad fix
- Prompt changes should encode good engineering practices, not task-specific tricks
- Code changes should handle failure modes generically (e.g., "retry on empty response" not "retry on task X")
- If a fix requires knowing the task answer, it's cheating — reject it
- Test fixes on multiple tasks to confirm they're general

## Prerequisites

- serf repo checked out at `/Users/jesse/prime-radiant/serf/`
- `OPENAI_API_KEY` in `.env` file at repo root (for local runs) and in
  `~/git/terminal-bench/.env` on flower-garden (for harbor runs)
- SSH access to magic-kingdom (`jesse@magic-kingdom` via Tailscale) for harbor runs.
  Fallback: flower-garden (`jesse@192.168.118.101`). Override with `EVAL_REMOTE` env var.
- Go toolchain for building serf
- Python tools in `tools/`: `run_eval.py`, `api-log-analyze.py`, `compare_runs.py`, `generate_report.py`

## Step 1: Pick a Failure to Investigate

Start from benchmark results. Categorize the failure:

| Pattern | Symptoms | Likely Fix |
|---------|----------|------------|
| Empty/null response | Transcript ends with empty assistant turn, ~4 output tokens | Code: retry logic in session.go |
| One-shot-and-quit | 1-5 rounds, communicate immediately, no iteration | Prompt: verification/iteration guidance |
| Wrong approach | Many rounds but fundamentally wrong strategy | Prompt: exploration/planning guidance |
| Gave up early | 5-10 rounds, reasonable start, then premature submit | Prompt: persistence directives |
| Timeout | 900s wallclock, task still running | Code: efficiency, or accept as hard |
| Refusal | Model says "I can't help with..." | Prompt: task framing |
| Reviewer-approved-but-wrong | Reviewer approves, verifier fails | Reviewer prompt: thoroughness |
| Never submitted | Many rounds, no communicate call at all | Code: nudge logic, or prompt |

Pick a task that USED TO PASS with the previous model/prompt — regressions are highest value.

## Step 2: Extract the Benchmark Task

Tasks are cached on the eval server at `~/.cache/harbor/tasks/<hash>/<task-name>/`.

```bash
# Find a task (uses EVAL_REMOTE or defaults to magic-kingdom)
REMOTE=${EVAL_REMOTE:-jesse@magic-kingdom}
ssh $REMOTE 'find ~/.cache/harbor/tasks/ -name "TASK_NAME" -type d'

# Key files in each task:
#   instruction.md    — The prompt given to serf
#   task.toml         — Metadata (difficulty, timeouts, docker image)
#   tests/            — Verifier scripts (test.sh, test_outputs.py, etc.)
#   environment/      — Dockerfile and setup
#   solution/         — Reference solution (if available)
```

Copy the instruction and tests locally:
```bash
TASK=cancel-async-tasks
REMOTE=${EVAL_REMOTE:-jesse@magic-kingdom}
TASK_DIR=$(ssh $REMOTE "find ~/.cache/harbor/tasks/ -name '$TASK' -type d")
scp $REMOTE:"$TASK_DIR/instruction.md" /tmp/task-$TASK.md
scp -r $REMOTE:"$TASK_DIR/tests/" /tmp/task-$TASK-tests/
```

Read the instruction to understand what serf is asked to do. Read the tests to understand what the verifier checks — this is critical for understanding WHY the agent's output fails.

## Step 3: Run Serf Locally

Build and run serf against the task prompt:

```bash
# Build (includes git SHA, dirty state, build time via ldflags)
cd /Users/jesse/prime-radiant/serf
export $(cat .env | xargs)
make build

# Run with limited rounds for fast iteration
./serf --provider openai --model gpt-5.3-codex \
  --max-rounds 10 \
  --state-dir /tmp/serf-bench-$TASK \
  -- "$(cat /tmp/task-$TASK.md)"
```

**Important constraints:**
- Tasks expecting `/app/` paths won't work directly locally. Options:
  1. Adjust the prompt to use a local directory (e.g., replace `/app/` with `/tmp/bench-workspace/`)
  2. Run inside a Docker container matching the task's environment
  3. Just observe the agent's approach — even if files are at wrong paths, you can see its strategy
- Use `--max-rounds 10` for fast iteration, increase once you're testing persistence fixes
- `--state-dir` keeps transcripts separate per investigation

## Step 4: Analyze the Transcript

Transcripts are at `<state-dir>/sessions/<session-id>.transcript.jsonl`.

### Transcript Viewer (batch analysis)

For analyzing many failures at once, use the transcript viewer to generate an interactive HTML site:

```bash
# 1. Rsync job data from eval server to local
JOB=serf-reviewer-full2
REMOTE=${EVAL_REMOTE:-jesse@magic-kingdom}
rsync -avz --include='*/' \
  --include='result.json' --include='reward.txt' --include='test-stdout.txt' \
  --include='stdout.txt' --include='return-code.txt' --include='command.txt' \
  --include='*.json' --include='*.jsonl' --exclude='*' \
  $REMOTE:/tmp/$JOB/$JOB/ /tmp/$JOB/

# 2. Generate the viewer
python3 tools/transcript-viewer.py /tmp/$JOB --output /tmp/viewer --filter failed

# 3. Open locally or scp to Jesse's machine
open /tmp/viewer/index.html
# Or: scp /tmp/viewer/index.html <target>:/tmp/serf-viewer.html
```

**Flags:**
- `--filter failed` (default) — only show tasks with reward=0
- `--filter passed` — only show tasks with reward=1
- `--filter all` — show everything

**The viewer provides:**
- Table of contents with color-coded failure types (timeout/wrong answer/no submit/error)
- Filter buttons to isolate one failure category
- Expand All / Collapse All for quick scanning
- Per-task: verifier test output, all sessions (main agent + subagents) with formatted transcripts
- Tool calls, results, assistant text with collapsible large content
- Session labels auto-detect reviewer vs test-writer subagents

### API log analysis

Every serf run writes `api.jsonl` alongside transcripts, logging every LLM API call
with full raw provider response. This is the primary tool for diagnosing empty responses,
parsing failures, and token usage.

```bash
# Show all API calls for a local run
python3 tools/api-log-analyze.py /tmp/serf-bench-$TASK/

# Show only empty responses (no text, no tool calls)
python3 tools/api-log-analyze.py /tmp/serf-bench-$TASK/ --empty

# Per-session summary (calls, empties, tokens, avg latency)
python3 tools/api-log-analyze.py /tmp/serf-bench-$TASK/ --summary

# Show full raw API response for empty responses
python3 tools/api-log-analyze.py /tmp/serf-bench-$TASK/ --empty --raw

# Filter to a specific session
python3 tools/api-log-analyze.py /tmp/serf-bench-$TASK/ --session sess-abc

# Show only errors (rate limits, timeouts)
python3 tools/api-log-analyze.py /tmp/serf-bench-$TASK/ --errors
```

For remote runs, rsync the `api.jsonl` files along with transcripts:
```bash
REMOTE=${EVAL_REMOTE:-jesse@magic-kingdom}
rsync -avz --include='*/' --include='api.jsonl' --include='*.jsonl' \
  --include='reward.txt' --include='result.json' --exclude='*' \
  $REMOTE:/tmp/$JOB/ /tmp/$JOB/
```

### Single-task analysis

For a single transcript, read the JSONL directly:

```bash
ls /tmp/serf-bench-$TASK/sessions/*.transcript.jsonl
```

**What to look for:**
1. **How many rounds?** If < 5 of 100, the model is giving up early
2. **Did it call communicate?** If yes, what did it submit? If no, did it emit bare text or null?
3. **Did it run tests?** Good agents test their work. One-shot agents don't.
4. **Did it iterate on failures?** After a test failure, did it try to fix the issue?
5. **What was the final state?** Read the files the agent wrote — compare against verifier expectations

## Step 5: Interrogate the Agent

Use `--resume-with` to load the old session context and ask questions:

```bash
./serf --provider openai --model gpt-5.3-codex \
  --resume-with SESSION_ID \
  --state-dir /tmp/serf-bench-$TASK \
  -- "You just completed a task but the verifier says your solution is wrong. \
      Look at what you submitted and explain: \
      1. What was your strategy? \
      2. Why did you stop when you did? \
      3. What would you do differently if you could start over? \
      4. Did you verify your solution before submitting?"
```

This reveals the agent's internal reasoning — rationalizations, misconceptions, and gaps that inform prompt fixes.

**Key questions to ask:**
- "Why did you stop after N rounds when you had 100 available?"
- "Did you run the evaluation script? What did it show?"
- "Your solution has [specific bug]. How would you fix it?"
- "The verifier expects [X] but you produced [Y]. What went wrong?"

## Step 6: Identify the Fix

Based on transcript analysis and interrogation, determine the fix category:

### Code Fix (session.go, context_manager.go, etc.)
- Empty/null response → retry logic
- Tool execution failures → error handling
- Context window issues → compaction improvements

### Prompt Fix (agent/prompts/base.md, system.openai.md)
- One-shot behavior → verification/iteration directives
- Wrong approach → exploration/planning guidance
- Premature submission → persistence directives
- Missing capabilities → tool usage guidance

### Profile Fix (agent/profile.go, tool definitions)
- Tool not available → add/modify tool definitions
- Wrong tool behavior → fix tool implementation

### Reviewer Prompt Fix (agent/agents/reviewer.md)
- Reviewer-approved-but-wrong → more thorough verification steps
- Reviewer missed file types → search completeness guidance
- Reviewer tested wrong artifact → verify installed/deployed state

## Step 7: Implement and Verify

Follow TDD: write a test for the fix, watch it fail, implement, watch it pass.

For code fixes: unit tests in `agent/session_test.go` or similar.
For prompt fixes: the benchmark task IS the test — re-run serf against the same task.

```bash
# After making changes, rebuild and re-run
make build
./serf --provider openai --model gpt-5.3-codex \
  --max-rounds 15 \
  --state-dir /tmp/serf-bench-$TASK-v2 \
  -- "$(cat /tmp/task-$TASK.md)"
```

Compare transcripts before/after:
- More rounds used? (persistence fix working)
- Different strategy? (approach fix working)
- Tests run before submitting? (verification fix working)
- Correct output? (actual pass)

## Step 8: Validate on Eval Server

**Use `tools/run_eval.py`.** It handles building, deploying, env vars, and harbor flags
correctly. Do not manually construct harbor commands — the flags are finicky and
you will waste time on typos.

### Per-run isolation

Every `run_eval.py launch` creates an isolated staging directory on the eval server
at `~/git/terminal-bench/runs/<job-name>/` containing its own copy of:
- `serf-linux-amd64` — the binary built from the current commit
- `serf_agent.py` — the harbor adapter
- `install-serf.sh.j2` — the container install template
- `.env` — copied from the base terminal-bench directory

This means concurrent runs never interfere with each other. You can run a focused test
and a full suite simultaneously without worrying about binary swaps or shared state.

### Deploy and run a focused eval

```bash
# Single task, 3 repetitions, with reviewer gate
./tools/run_eval.py launch --job reviewer-v3 --task build-cython-ext --reps 3 --ak enable_reviewer_gate=true

# Single task, 5 repetitions, no reviewer gate
./tools/run_eval.py launch --job baseline-test --task fix-code-vulnerability --reps 5

# Multiple agent kwargs
./tools/run_eval.py launch --job experiment-1 --task build-cython-ext --reps 3 \
  --ak enable_reviewer_gate=true --ak result_tool_name=done

# Dry run (show what would be done without executing)
./tools/run_eval.py launch --job test --task build-cython-ext --reps 1 --dry-run
```

### Check eval status and results

```bash
./tools/run_eval.py status --job reviewer-v3
```

Output shows pass/fail/running for each rep, summary, manifest, and recent log.

### Collect and summarize a finished run

```bash
./tools/run_eval.py collect --job reviewer-v3
```

Rsyncs results from the eval server, runs `collect-run.sh` to create the archive,
generates `summary.json` with all three pass rates and Wilson CI.

### Full 89-task suite

For full validation after targeted testing:

```bash
# Build, deploy, and run all 89 tasks
./tools/run_eval.py launch --job full-run-v4 --reps 1 --ak enable_reviewer_gate=true
```

(Omit `--task` for all tasks. But prefer focused evals first — full runs take hours
and cost real money.)

### A/B testing

To compare two configurations, run focused evals with different job names:

```bash
# Control: current behavior
./tools/run_eval.py launch --job ab-control --task build-cython-ext --reps 5 --ak enable_reviewer_gate=true

# Treatment: with some change
./tools/run_eval.py launch --job ab-treatment --task build-cython-ext --reps 5 \
  --ak enable_reviewer_gate=true --ak result_tool_name=done
```

Then compare results:
```bash
# Collect both runs
./tools/run_eval.py collect --job ab-control
./tools/run_eval.py collect --job ab-treatment

# Cross-run comparison with bootstrap CIs, McNemar's test, regressions/improvements
python3 tools/compare_runs.py /data/serf-evals/runs/<run-id-a> /data/serf-evals/runs/<run-id-b>

# Generate HTML report for a single run
python3 tools/generate_report.py /data/serf-evals/runs/<run-id>
```

### Custom adapters

Each run gets its own staging directory, so adapter modifications won't affect other
runs. To test adapter changes without rebuilding the binary:

```bash
# --no-build skips cross-compile; the staging dir won't have a binary, so ensure
# a previous run's binary exists or copy one manually.
./tools/run_eval.py launch --job my-test --task configure-git-webserver --reps 1 \
  --no-build --adapter "serf_agent:SerfAgent" --ak enable_reviewer_gate=true
```

For a completely different adapter class, modify `tools/serf_agent.py` locally and
launch — `run_eval.py` will deploy your modified version to the run's isolated staging
directory without touching any other run.

### Targeting a different server

By default, `run_eval.py` targets magic-kingdom. To use flower-garden or another server:

```bash
EVAL_REMOTE=jesse@192.168.118.101 ./tools/run_eval.py launch --job test --task build-cython-ext --reps 1
```

**NEVER manually run harbor commands.** `run_eval.py` handles env vars, PATH, and
flags correctly. Manual harbor commands will forget `set -a; source .env; set +a` and
silently fail with "no LLM providers configured."

## Harbor CLI Reference

Harbor is the benchmark runner. It's installed via uv at `~/.local/bin/harbor` on
the eval servers (magic-kingdom v0.1.45, flower-garden v0.1.44). `run_eval.py` handles
all of this, but if you need to understand the flags:

### Correct flags (verified working)

| Flag | Purpose | Example |
|------|---------|---------|
| `--dataset` | Dataset to run | `"terminal-bench@2.0"` |
| `--task-name` | Filter to one task | `"build-cython-ext"` |
| `-k` | Repetitions per task | `3` |
| `--job-name` | Name for this run | `"my-eval"` |
| `--jobs-dir` | Output directory | `"/tmp/my-eval"` |
| `--agent-import-path` | Agent adapter | `"serf_agent:SerfAgent"` |
| `--ak` | Agent kwargs (repeatable) | `enable_reviewer_gate=true` |

### Wrong flags (will error)

| Wrong | Correct | Error |
|-------|---------|-------|
| `--job-dir` | `--jobs-dir` | "No such option" |
| `--agent serf` | `--agent-import-path "serf_agent:SerfAgent"` | serf is not a built-in agent |
| File path as import | `module:ClassName` format | Import error |

### Agent adapter (serf_agent.py)

The serf adapter is at `tools/serf_agent.py` in the repo. `run_eval.py launch`
deploys it to each run's isolated staging directory at
`~/git/terminal-bench/runs/<job-name>/`. It wraps the serf binary for harbor's
agent interface:

- Maps harbor agent kwargs to serf CLI flags (e.g., `enable_reviewer_gate` → `--enable-reviewer-gate`)
- Custom kwargs like `result_tool_name` need explicit support in the adapter — check
  that the adapter handles new flags before using them with `--ak`
- State dir at `/logs/agent/serf-state` (bind-mounted by harbor), captures transcripts + api.jsonl
- Downloads `/app` artifacts post-run, prunes large/binary directories locally
- Requires `install-serf.sh.j2` alongside it (also auto-deployed)

### Environment variables

**CRITICAL**: Background processes (`nohup`) do NOT inherit interactive shell env vars.
You must explicitly source the env file before launching:

```bash
# Correct: set -a exports all vars, works with nohup
set -a; source .env; set +a

# Also works but messier with special characters in values
export $(cat .env | xargs)

# WRONG: source without set -a does not export
source .env  # vars set but not exported to child processes
```

The helper scripts handle this automatically.

## Job Data Structure

After a harbor run, job data is at:
```
/tmp/<job-name>/
  manifest.json                                  — build provenance (git SHA, branch, model, adapter)
  <task>__<hash>/
    reward.txt                                   — 0.0 or 1.0 (ground truth)
    result.json                                  — overall result, timing
    agent/serf-state/api.jsonl                   — all LLM API calls with raw responses
    agent/serf-state/sessions/<id>.json          — session metadata
    agent/serf-state/sessions/<id>.transcript.jsonl — full transcript (all turns)
```

**Important:** `reward.txt` is the accurate per-task reward. `result.json` intermediate
writes can be misleading — always check `reward.txt`.

The `manifest.json` ties the run to exact source code — `run_eval.py status` prints it.
The `api.jsonl` captures every LLM API call with the full raw provider response,
enabling postmortem analysis of empty responses, parsing failures, and token usage
without throwaway scripts.

For batch analysis, use the transcript viewer (see Step 4). For quick failure listing:
```bash
./tools/run_eval.py status --job <job-name>
```

### Collected archive structure

After `run_eval.py collect`, the archive at `/data/serf-evals/runs/<run-id>/` has:
```
<run-id>/
  manifest.json
  summary.json
  tasks/
    <task-name>/
      rep-1/
        reward.txt
        failure_category.txt
        transcript.jsonl
        api.jsonl
      rep-2/
        ...
```

## Reviewer Gate Failure Patterns

When running with `--enable-reviewer-gate`, failures fall into these categories:

| Pattern | Frequency | Cause | Fix |
|---------|-----------|-------|-----|
| Reviewer-approved-but-wrong | ~40% | Reviewer approves work that verifier rejects | Improve reviewer thoroughness |
| Timeout | ~33% | Task + reviewer overhead exceeds wallclock limit | Efficiency, or accept as hard |
| Never submitted | ~23% | Agent never calls communicate | Nudge logic, prompt |
| API error | ~2% | Transient provider failures | Retry logic |

### Reviewer-approved-but-wrong deep dive

From analyzing build-cython-ext failures (4/5 same root cause):

1. **Missed file types**: Agent patched `.py` files but missed `.pyx` (Cython source).
   The reviewer ran the project's own test suite (18/18 pass) but didn't grep for
   remaining instances of the deprecated pattern in ALL file types. Fix: reviewer
   prompt now says to search exhaustively across all file types.

2. **Tested wrong artifact**: Agent built extensions in-place, then `pip install`
   created a pure-Python wheel. Reviewer tested from source directory (where `.so`
   files existed), but verifier tested the installed package (where they didn't).
   Fix: reviewer prompt now says to test the final installed/deployed artifact.

3. **All-or-nothing scoring**: Even 10/11 tests passing yields reward=0.
   Tasks that look close are actually failures. The reviewer needs to be strict
   about complete coverage.

These fixes are in `agent/agents/reviewer.md` and encode general principles
(search all file types, test final artifact) not task-specific knowledge.

## Common Pitfalls

- **Don't optimize for one task**: A prompt that says "always install scipy first" is task-specific. A prompt that says "resolve dependency errors before retrying" is general.
- **Don't ignore the verifier**: Read `tests/test.sh` and `tests/test_outputs.py` to understand exactly what's checked. Many failures are partial — the agent did 80% of the work but missed a specific requirement.
- **Don't assume the model reads the prompt**: gpt-5.3-codex in tool-calling mode often skips system prompt directives. Structural interventions (tool availability, response handling) are more reliable than prompt text.
- **Run multiple times**: Results are nondeterministic. A task that passes once may fail next time. Run 2-3 times before declaring a fix works.
- **Don't build to the benchmark**: If a fix requires knowing the task answer, it's cheating. If it only helps one task and hurts others, it's overfitting. Every fix should be a general engineering principle.
- **Don't skip the env vars**: The #1 cause of "all tasks fail immediately" is forgetting to export OPENAI_API_KEY. `run_eval.py` handles this, so use it.
- **Use run_eval.py**: It encodes correct harbor flags, env var handling, build/deploy, and result checking. Don't hand-construct harbor commands.
- **Check build provenance**: Run `./serf --version` to verify the binary you're testing. Every transcript header includes `build_version`. Every eval run writes a `manifest.json` with the git SHA. If you can't trace a result to the code that produced it, the result is worthless.
