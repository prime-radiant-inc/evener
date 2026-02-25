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
- `OPENAI_API_KEY` in `.env` file (load with `export $(cat .env | xargs)`)
- SSH access to flower-garden (`jesse@192.168.118.101`) for task extraction
- Go toolchain for building serf

## Step 1: Pick a Failure to Investigate

Start from benchmark results. Categorize the failure:

| Pattern | Symptoms | Likely Fix |
|---------|----------|------------|
| Empty/null response | Transcript ends with empty assistant turn, ~4 output tokens | Code: retry logic in session.go |
| One-shot-and-quit | 1-5 rounds, communicate(result) immediately, no iteration | Prompt: verification/iteration guidance |
| Wrong approach | Many rounds but fundamentally wrong strategy | Prompt: exploration/planning guidance |
| Gave up early | 5-10 rounds, reasonable start, then premature submit | Prompt: persistence directives |
| Timeout | 900s wallclock, task still running | Code: efficiency, or accept as hard |
| Refusal | Model says "I can't help with..." | Prompt: task framing |

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

```bash
# Find the transcript
ls /tmp/serf-bench-$TASK/sessions/*.transcript.jsonl

# Parse with the transcript tool (deploy to flower-garden or use locally)
python3 /tmp/parse_transcript.py /tmp/serf-bench-$TASK/sessions/*.transcript.jsonl
```

The transcript parser (`/tmp/parse_transcript.py` on flower-garden, or write your own) shows:
- `[seq] USER: ...` — input prompt
- `[seq] ASSISTANT: call:tool_name(args)` — tool calls the model made
- `[seq] TOOL(name): result` — tool execution results

**What to look for:**
1. **How many rounds?** If < 5 of 100, the model is giving up early
2. **Did it call communicate(result)?** If yes, what did it submit? If no, did it emit bare text or null?
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

## Step 8: Validate Generality

**Critical step — don't skip this.**

Run the fix against 3-5 OTHER tasks to confirm it doesn't regress:

```bash
# Pick tasks from different categories
for TASK in crack-7z-hash cancel-async-tasks fix-code-vulnerability regex-log build-cython-ext; do
  ./serf --provider openai --model gpt-5.3-codex \
    --max-rounds 15 \
    --state-dir /tmp/serf-validation-$TASK \
    -- "$(cat /tmp/task-$TASK.md)"
done
```

If the fix helps the target task but hurts others, it's not general enough — rethink the approach.

For full validation, deploy to flower-garden and run the 89-task suite:
```bash
GOOS=linux GOARCH=amd64 go build -o /tmp/serf-linux-amd64 ./cmd/serf/
scp /tmp/serf-linux-amd64 jesse@192.168.118.101:~/git/terminal-bench/serf-linux-amd64
# Then run harbor on flower-garden (see docs/terminal-bench.md)
```

## Benchmark Result Transcripts

After a harbor run, transcripts are at:
```
/tmp/serf-<run-name>/<timestamp>/<task>__<hash>/agent/serf-state/sessions/<id>.transcript.jsonl
```

To analyze all failures from a run:
```bash
ssh jesse@192.168.118.101 'for f in /tmp/serf-<run>/*/*/result.json; do
  task=$(basename $(dirname "$f") | sed "s/__.*//")
  reward=$(python3 -c "import json; print(json.load(open(\"$f\")).get(\"verifier_result\",{}).get(\"rewards\",{}).get(\"reward\",0))")
  if [ "$reward" = "0.0" ]; then echo "$task: FAIL"; fi
done | sort'
```

## Common Pitfalls

- **Don't optimize for one task**: A prompt that says "always install scipy first" is task-specific. A prompt that says "resolve dependency errors before retrying" is general.
- **Don't ignore the verifier**: Read `tests/test.sh` and `tests/test_outputs.py` to understand exactly what's checked. Many failures are partial — the agent did 80% of the work but missed a specific requirement.
- **Don't assume the model reads the prompt**: gpt-5.3-codex in tool-calling mode often skips system prompt directives. Structural interventions (tool availability, response handling) are more reliable than prompt text.
- **Run multiple times**: Results are nondeterministic. A task that passes once may fail next time. Run 2-3 times before declaring a fix works.
