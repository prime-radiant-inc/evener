---
name: tools-reference
description: Reference for serf-report, scoreboard.py, run_eval.sh, interrogation tools, and transcript reading. Use when you need exact command syntax or flags.
---

# Eval Tools Reference

## Scoring convention

**A rep is a pass when `verifier_result.rewards.reward == 1.0` in
`result.json` (equivalently, `verifier/reward.txt` contains `1`). Anything
else is a fail. Exceptions are informational.**

A rep that hit `AgentTimeoutError` can still pass with `reward == 1.0` —
the harbor verifier runs against the post-timeout workspace state and
grades it independently. Do not classify these reps as fails.

All the tools listed below read the reward field. If you build an ad-hoc
scoring script (`grep`/`jq`/Python), use the same rule.

## serf-report — Analytics & Regression Detection

### Compare two runs
```bash
./tools/eval/serf-report compare CURRENT_WAVE BASELINE_WAVE
./tools/eval/serf-report compare CURRENT BASELINE --only regressed    # just regressions
./tools/eval/serf-report compare CURRENT BASELINE --include-reps      # show per-rep breakdown
./tools/eval/serf-report compare CURRENT BASELINE --format json       # machine-readable
./tools/eval/serf-report compare CURRENT BASELINE --min-delta 0.33    # skip small changes
```

### Find regressions (tasks below historical best)
```bash
./tools/eval/serf-report regressions                        # all regressions
./tools/eval/serf-report regressions --since WAVE           # only count runs after WAVE
./tools/eval/serf-report regressions --threshold 0.33       # skip small regressions
```

### Task history with regression markers
```bash
./tools/eval/serf-report history TASK_NAME
./tools/eval/serf-report history task1 task2 task3          # multiple tasks
```

### Token and dollar costs
```bash
./tools/eval/serf-report cost WAVE                          # run summary
./tools/eval/serf-report cost WAVE --task TASK              # per-rep breakdown
./tools/eval/serf-report cost WAVE --vs OTHER_WAVE          # cost comparison
./tools/eval/serf-report cost WAVE --sort cost              # sort by most expensive
```

### Generate HTML dashboard
```bash
./tools/eval/serf-report dashboard                          # from current data
./tools/eval/serf-report dashboard --runs W1,W2,W3 --labels "L1,L2,L3"
./tools/eval/serf-report dashboard --include-cost           # add cost column
./tools/eval/serf-report dashboard -o path/to/dashboard.html
```

### One-page markdown summary
```bash
./tools/eval/serf-report summary WAVE
./tools/eval/serf-report summary WAVE --vs BASELINE         # with comparison
```

## scoreboard.py — Current Scores

```bash
./tools/eval/scoreboard.py                         # full 89-task matrix
./tools/eval/scoreboard.py --task TASK_NAME        # single task history
./tools/eval/scoreboard.py --failing               # tasks with score < 1.0
./tools/eval/scoreboard.py --solved                # tasks with score == 1.0
./tools/eval/scoreboard.py --sort score            # sort by score descending
```

## run_eval.sh — Launch Evals

```bash
./tools/eval/run_eval.sh --wave                              # full 89-task eval
./tools/eval/run_eval.sh --wave --tasks "t1,t2"              # specific tasks
./tools/eval/run_eval.sh --wave --tasks failing              # all currently failing
./tools/eval/run_eval.sh --wave --tasks "t1,t2" --reps 3     # explicit rep count
./tools/eval/run_eval.sh --wave --tasks "t1" --dry-run       # preview without launching
```

**Rules:**
- Always use `--wave` (one task per instance)
- Must commit before launching (enforced)
- Model default: `openai/gpt-5.4-mini` (do not change without explicit instruction)
- Instance type: `r6i.large` (2 vCPU, 16 GB RAM)
- Spot quota: 128 vCPU = 64 concurrent instances
- Build uses `make build-linux` which invalidates Go embed cache

## post_run.sh — Collect Results

```bash
./tools/eval/post_run.sh WAVE_ID                             # collect + scoreboard diff
./tools/eval/post_run.sh WAVE_ID --variant "description"     # with variant label
```

Auto-reads model/SHA/branch from `.serf-launches/` if launched with `run_eval.sh`.

## Session Interrogation

```bash
# List all sessions for a rep
python3 tools/transcripts/interrogate_session.py \
    --run WAVE --rep N --task TASK --list-sessions

# Interrogate coordinator (default)
python3 tools/transcripts/interrogate_session.py \
    --run WAVE --rep N --task TASK \
    --question "Why did you not delegate?"

# Interrogate a subagent by index
python3 tools/transcripts/interrogate_session.py \
    --run WAVE --rep N --task TASK \
    --session 2 \
    --question "Your prompt says X. Why did you do Y instead?"

# Auto-interrogate all failures
./tools/transcripts/interrogate_failures.sh WAVE_ID
```

**Requires API keys:** `set -a; source .env; set +a` before running.

**S3 downloads:** `interrogate_session.py` automatically downloads transcripts
from S3 and caches them in `../harbor-runner/state/results/`. Always use this
tool (or `read_transcript.py`) for transcript analysis — never manually parse
transcript JSONL files. The transcript format is complex (`kind`/`entry`/`turn`/
`message` nesting) and tool-specific. If `read_transcript.py` can't find files
(wrong local path), use `interrogate_session.py` first — it handles the S3
download. After that, transcripts are cached locally.

## Transcript Reading

```bash
# List sessions
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --list-sessions

# Coordinator tool calls
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --tool-calls

# Specific session tool calls
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --session 0 --tool-calls

# System prompt
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --session 0 --system-prompt

# Delegation message
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --delegation

# Verifier output
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --verifier

# Full transcript dump
python3 tools/transcripts/read_transcript.py --run WAVE --rep N --task TASK --session 0 --full --limit 20
```

## Binary Verification

Always verify the binary contains your prompt changes:

```bash
make build-linux
strings serf-linux-amd64 | grep "expected phrase"
strings serf-linux-amd64 | grep -c "must not appear"
```

## Data Locations

| What | Where |
|------|-------|
| Per-task history | `docs/experiments/tasks/{task}.json` |
| Per-run snapshot | `docs/experiments/runs/{run_id}.json` |
| Scoreboard | `docs/experiments/scoreboard.json` |
| Trial results (tokens, timing) | `~/.serf-evals/tasks/{task}/{run}/rep-{N}/result.json` |
| API call log | `~/.serf-evals/tasks/{task}/{run}/rep-{N}/api.jsonl` |
| Transcripts | `~/.serf-evals/tasks/{task}/{run}/rep-{N}/sessions/*.transcript.jsonl` |
| Launch metadata | `.serf-launches/{run_id}.json` |
| Experiment templates | `docs/experiments/backlog/TEMPLATE.md` |
| Shipped experiments | `docs/experiments/completed-improved/` |
| Failed experiments | `docs/experiments/completed-did-not-improve/` |
| Archived experiments | `docs/experiments/archived/` |
