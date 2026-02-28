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
- SSH access to flower-garden (`jesse@192.168.118.101`). Use the IP address, not the
  hostname — Tailscale MagicDNS may not resolve `flower-garden`.
- Go toolchain for building serf
- Helper scripts in `tools/`: `eval-task.sh`, `check-eval.sh`

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

Tasks are cached on flower-garden at `~/.cache/harbor/tasks/<hash>/<task-name>/`.

```bash
# Find a task
ssh jesse@192.168.118.101 'find ~/.cache/harbor/tasks/ -name "TASK_NAME" -type d'

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
TASK_DIR=$(ssh jesse@192.168.118.101 "find ~/.cache/harbor/tasks/ -name '$TASK' -type d")
scp jesse@192.168.118.101:"$TASK_DIR/instruction.md" /tmp/task-$TASK.md
scp -r jesse@192.168.118.101:"$TASK_DIR/tests/" /tmp/task-$TASK-tests/
```

Read the instruction to understand what serf is asked to do. Read the tests to understand what the verifier checks — this is critical for understanding WHY the agent's output fails.

## Step 3: Run Serf Locally

Build and run serf against the task prompt:

```bash
# Build
cd /Users/jesse/prime-radiant/serf
export $(cat .env | xargs)
go build ./cmd/serf/

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
# 1. Rsync job data from flower-garden to local
JOB=serf-reviewer-full2
rsync -avz --include='*/' \
  --include='result.json' --include='reward.txt' --include='test-stdout.txt' \
  --include='stdout.txt' --include='return-code.txt' --include='command.txt' \
  --include='*.json' --include='*.jsonl' --exclude='*' \
  192.168.118.101:~/git/terminal-bench/jobs/$JOB/ /tmp/$JOB/

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
go build ./cmd/serf/
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

## Step 8: Validate on Flower-Garden

**Use the helper scripts.** They handle building, deploying, env vars, and harbor flags
correctly. Do not manually construct harbor commands — the flags are finicky and
you will waste time on typos.

### Deploy and run a focused eval

```bash
# Single task, 3 repetitions, with reviewer gate
./tools/eval-task.sh reviewer-v3 build-cython-ext 3 enable_reviewer_gate=true

# Single task, 5 repetitions, no reviewer gate
./tools/eval-task.sh baseline-test fix-code-vulnerability 5

# Multiple agent kwargs
./tools/eval-task.sh experiment-1 build-cython-ext 3 enable_reviewer_gate=true result_tool_name=done
```

### Check eval status and results

```bash
./tools/check-eval.sh reviewer-v3
```

Output shows pass/fail/running for each rep, summary, and recent log.

### Full 89-task suite

For full validation after targeted testing:

```bash
# Build, deploy, and run all 89 tasks
./tools/eval-task.sh full-run-v4 "" 1 enable_reviewer_gate=true
```

(Empty task name = all tasks. But prefer focused evals first — full runs take hours
and cost real money.)

### A/B testing

To compare two configurations, run focused evals with different job names:

```bash
# Control: current behavior
./tools/eval-task.sh ab-control build-cython-ext 5 enable_reviewer_gate=true

# Treatment: with some change
./tools/eval-task.sh ab-treatment build-cython-ext 5 enable_reviewer_gate=true result_tool_name=done
```

Then compare results:
```bash
./tools/check-eval.sh ab-control
./tools/check-eval.sh ab-treatment
```

The `--result-tool-name` flag (`--ak result_tool_name=X`) lets you rename the result
tool at runtime without code changes. Useful for testing whether tool naming affects
model behavior. (Answer from our eval: it doesn't.)

### Custom adapters

To test prompt variants or adapter changes without rebuilding the binary:

```bash
# 1. Create adapter variant on flower-garden (e.g., serf_agent_mytest.py)
#    - Copy from serf_agent.py, change class name, modify as needed

# 2. Run with NO_BUILD=1 and custom AGENT_IMPORT_PATH
NO_BUILD=1 AGENT_IMPORT_PATH="serf_agent_mytest:MyTestAgent" \
  ./tools/eval-task.sh my-test configure-git-webserver 1 enable_reviewer_gate=true
```

**NEVER manually run harbor commands.** The helper scripts handle env vars, PATH, and
flags correctly. Manual harbor commands will forget `set -a; source .env; set +a` and
silently fail with "no LLM providers configured."

## Harbor CLI Reference

Harbor is the benchmark runner. It's installed via uv at `~/.local/bin/harbor` on
flower-garden. The helper scripts handle all of this, but if you need to run manually:

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

The serf adapter is at `~/git/terminal-bench/serf_agent.py` on flower-garden. It wraps
the serf binary for harbor's agent interface:

- Maps harbor agent kwargs to serf CLI flags (e.g., `enable_reviewer_gate` → `--enable-reviewer-gate`)
- Custom kwargs like `result_tool_name` need explicit support in the adapter — check
  that the adapter handles new flags before using them with `--ak`
- The adapter must be importable from the working directory (`cd ~/git/terminal-bench`
  before running harbor)

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
/tmp/<job-name>/<task>__<hash>/
  reward.txt                                     — 0.0 or 1.0 (ground truth)
  result.json                                    — overall result, timing
  agent/serf-state/sessions/<id>.json            — session metadata
  agent/serf-state/sessions/<id>.transcript.jsonl — full transcript (all turns)
```

**Important:** `reward.txt` is the accurate per-task reward. `result.json` intermediate
writes can be misleading — always check `reward.txt`.

For batch analysis, use the transcript viewer (see Step 4). For quick failure listing:
```bash
./tools/check-eval.sh <job-name>
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
- **Don't skip the env vars**: The #1 cause of "all tasks fail immediately" is forgetting to export OPENAI_API_KEY. The helper scripts handle this, so use them.
- **Use helper scripts**: `tools/eval-task.sh` and `tools/check-eval.sh` exist for a reason. They encode correct harbor flags, env var handling, and result checking. Don't hand-construct harbor commands.
