# Eval Infrastructure Reference

## Eval Servers

- **Evals run on AWS spot instances** via `~/prime-radiant/harbor-runner/launch.sh`
- **DO NOT run evals on magic-kingdom.** It gets congested with multiple Docker
  containers and causes task failures. magic-kingdom is for staging binaries and
  reading results only.
- `run_eval.py` targets magic-kingdom by default — only use it for `status` and
  `collect`, not `launch`. For launching, use harbor-runner directly.
- **Override:** `EVAL_REMOTE=user@host` for status/collect on different servers.

## run_eval.py Subcommands

### launch — Build, deploy, and run
```bash
./tools/run_eval.py launch \
  --task TASK_NAME \          # repeatable, or --task discriminators for named set
  --reps 3 \                  # repetitions per task
  --model openai/gpt-5.4 \   # model identifier
  --ak key=value \            # agent kwargs (repeatable)
  --force \                   # kill existing job with same name
  --no-build \                # skip cross-compile (reuse staged binary)
  --allow-dirty \             # allow uncommitted changes
  --dry-run                   # show what would be done
```

Auto-generates job names: `{harness}_{model}_{effort}_{sha}_{date}_{rep}`
Use `--rep N` to increment the suffix for same-day runs.

### status — Check progress
```bash
./tools/run_eval.py status --job JOB_NAME
```

### collect — Archive results locally
```bash
./tools/run_eval.py collect --job JOB_NAME --archive-dir /path/to/local/archive
```

## iterate_task.py — Single-Task Iteration

```bash
# Run task and get diagnostic report
./tools/iterate_task.py run --task TASK --note "description" --model openai/gpt-5.4

# With prompt overlay (appended to system prompt, root session only)
./tools/iterate_task.py run --task TASK --prompt path/to/overlay.md --note "description"

# Re-examine a completed job
./tools/iterate_task.py report --job JOB_NAME --task TASK

# View iteration history
./tools/iterate_task.py log --task TASK
```

Logs automatically to `tools/iteration-logs/<task>.jsonl`.

**Note:** `--prompt` uses `system_prompt_append` which only reaches the root session
(coordinator), not subagents. For prompts that need to reach implementers too, change
`agent/prompts/core.md` instead.

## Per-Run Isolation

Every `run_eval.py launch` creates an isolated staging directory on the eval server
at `~/git/terminal-bench/runs/<job-name>/` containing:
- `serf-linux-amd64` — binary built from current commit
- `serf_agent.py` — harbor adapter
- `install-serf.sh.j2` — container install template
- `.env` — copied from base terminal-bench directory

Concurrent runs never interfere. You can run focused tests and full suites
simultaneously.

## Job Data Structure

```
/data/agent-evals/runs/<job-name>/
  manifest.json                           — build provenance
  <task>__<hash>/
    result.json                           — timing, exceptions, token usage
    reward.txt                            — 0 or 1 (ground truth from verifier)
    verifier/
      test-stdout.txt                     — pytest output
      reward.txt                          — same as above
    agent/
      agent-state/
        api.jsonl                         — all LLM API calls with raw responses
        sessions/
          <id>.meta.json                  — session metadata
          <id>.transcript.jsonl           — full transcript
```

## Harbor CLI (for reference only — use run_eval.py)

| Flag | Purpose |
|------|---------|
| `--dataset "terminal-bench@2.0"` | Dataset |
| `--task-name TASK` | Filter to one task (repeatable) |
| `-k N` | Repetitions per task |
| `--job-name NAME` | Job name |
| `--jobs-dir PATH` | Output directory |
| `--agent-import-path "serf_agent:SerfAgent"` | Agent adapter |
| `--ak key=value` | Agent kwargs (repeatable) |
| `--no-delete` | Keep containers after run |

**NEVER manually run harbor commands.** `run_eval.py` handles env vars, PATH, build,
deploy, and flags correctly.

## Environment Variables

Background processes (`nohup`) do NOT inherit shell env vars. Must explicitly source:
```bash
set -a; source .env; set +a
```
The helper scripts handle this automatically.

## Task Sets

```bash
./tools/run_eval.py launch --list-tasks  # Show available named sets
```

- `discriminators` — 56 tasks, 10-75% failure rate (the useful signal)
- Individual tasks by name: `--task chess-best-move --task path-tracing`

## Transcript Format

JSONL, one entry per line:
- Line 1: `kind: "header"` — session metadata, system prompt, model
- Lines 2+: `kind: "entry"` — each turn

Entry structure:
```json
{
  "kind": "entry",
  "seq": 5,
  "turn": {
    "kind": "ASSISTANT",
    "message": {
      "role": "assistant",
      "content": [
        {"kind": "text", "text": "..."},
        {"kind": "tool_call", "tool_call": {
          "id": "call_xxx",
          "name": "exec_command",
          "arguments": {"command": "ls /app"}
        }}
      ]
    }
  }
}
```

Tool results are in separate entries with `kind: "TOOL_RESULTS"`.
Subagent sessions are separate transcript files in the same directory.
