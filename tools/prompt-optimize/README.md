# Prompt Optimization Toolkit

Iterative prompt optimization for benchmark personas using model self-interrogation.
Instead of running full evals (~15min each), interrogate the model about its own
failed trajectories (~5sec each) to find what prompt language changes behavior.

## Tools

### `tools/interrogate_trajectory.py`

Interrogate ATIF trajectories (from lace eval runs) via OpenRouter Chat Completions API.

```bash
export OPENROUTER_API_KEY=<key from magic-kingdom:/home/jesse/git/terminal-bench/.env>

# Show step summaries from a trajectory (pick decision points)
python3 tools/interrogate_trajectory.py steps /tmp/trajectory.json

# Ask the model why it did something
python3 tools/interrogate_trajectory.py interrogate /tmp/trajectory.json \
    --up-to-step 14 \
    --question "Why did you use pyOpenSSL instead of openssl CLI?"

# Replay a decision point with a different persona
python3 tools/interrogate_trajectory.py replay /tmp/trajectory.json \
    --at-step 13 \
    --persona tools/prompt-optimize/personas/iter-5-interrogated.md

# Compare two personas at the same decision point
python3 tools/interrogate_trajectory.py compare /tmp/trajectory.json \
    --at-step 13 \
    --persona-a personas/iter-4.md \
    --persona-b personas/iter-5-interrogated.md

# Inject a message before a decision point (counterfactual)
python3 tools/interrogate_trajectory.py nudge /tmp/trajectory.json \
    --at-step 45 \
    --nudge "You've tried this 8 times. Pivot to a different strategy." \
    --reps 3

# Override model (default: qwen/qwen3.5-flash-02-23)
python3 tools/interrogate_trajectory.py --model gpt-5.3-codex interrogate ...
```

### `tools/lace_interrogate.py`

Interrogate lace sessions from events.jsonl files. Similar to interrogate_trajectory.py
but reads the native lace event format instead of ATIF.

```bash
export OPENROUTER_API_KEY=<key>

# Show event summary
python3 tools/lace_interrogate.py events /path/to/events.jsonl

# List sessions in a trial directory
python3 tools/lace_interrogate.py sessions /data/agent-evals/runs/job/task__trial/

# Interrogate (narrative mode — cheap, ~4K tokens)
python3 tools/lace_interrogate.py --model qwen/qwen3.5-flash-02-23 interrogate \
    /path/to/events.jsonl \
    -q "Why did you use pyOpenSSL?"

# Resume (full replay mode — shows what tool calls the model would make next)
# More reliable than interrogate for testing prompt changes
python3 tools/lace_interrogate.py --model qwen/qwen3.5-flash-02-23 resume \
    /path/to/events.jsonl \
    -q "Continue working on the task."

# Resume with persona swap and truncation (the main prompt testing workflow)
python3 tools/lace_interrogate.py --model qwen/qwen3.5-flash-02-23 resume \
    /path/to/events.jsonl \
    --up-to-event 13 \
    --persona tools/prompt-optimize/personas/iter-6-doc-reading.md \
    --reps 3 \
    -q "Continue working on the task."

# Accepts: .jsonl files, session dirs, trial dirs, trajectory.json
```

### `tools/replay_microtask.py`

Original microtask replay tool (OpenAI Responses API only, for gpt-5.x models).
Use interrogate_trajectory.py instead for OpenRouter/Qwen models.

## Getting Trajectories

Trajectories live on magic-kingdom. For lace runs, events.jsonl is in the session dir.

```bash
# List available Qwen runs
ssh jesse@magic-kingdom "ls /data/agent-evals/runs/ | grep qwen"

# Find events.jsonl for a specific task trial
ssh jesse@magic-kingdom "find /home/jesse/git/terminal-bench/runs/lace-qwen-chainfix/jobs/JOB_NAME/TASK__TRIAL/agent/agent-state/agent-sessions/ -name events.jsonl"

# Copy events.jsonl locally
scp jesse@magic-kingdom:/path/to/events.jsonl /tmp/task-events.jsonl

# Find trial IDs for a task
ssh jesse@magic-kingdom "ls /home/jesse/git/terminal-bench/runs/lace-qwen-chainfix/jobs/JOB_NAME/ | grep TASK"

# Check pass/fail
ssh jesse@magic-kingdom "python3 -c \"import json; r=json.load(open('TRIAL_DIR/result.json')); print(r['verifier_result']['rewards']['reward'])\""
```

## Personas

All personas in `tools/prompt-optimize/personas/`:

| File | Description | Status |
|------|-------------|--------|
| `iter-0.md` | Baseline (copy of benchmark-h23.md) | Tested |
| `iter-1.md` | Added self-containment + doc reading | Never loaded (deploy bug) |
| `iter-2.md` | Added data completeness | Tested, self-containment too weak |
| `iter-3.md` | Task-specific HARD RULE + cryptography example | 2/2 PASS (teaching-to-test) |
| `iter-4.md` | Generalized, softened per Jesse's feedback | 0-1/2 (nondeterministic) |
| `iter-5-interrogated.md` | Interrogation-driven improvements | 3/6 (openssl, build-cython, fix-ocaml pass) |
| `iter-6-doc-reading.md` | + Read fetched docs, enumerate columns | 1/1 count-tokens pass (nondeterministic) |
| `iter-7-component-isolation.md` | + Component isolation, file ops final, debug parsing | Testing |

### Changes by iteration

**iter-5** (vs iter-4): consequence-based self-containment, hard data inspection rule, GATE 3 with import enumeration, explicit "attempt" definition, automate-don't-hand-solve, stay oriented

**iter-6** (vs iter-5): "NEVER write code until you have read the full content of every documentation file", enumerate ALL columns matching a data category

**iter-7** (vs iter-6): build/test components independently before combining, file operations are final (never move back), debug parsing logic instead of manually correcting output

## Running a Full Eval

Deploy persona and run via harbor on magic-kingdom:

```bash
# 1. Copy persona to magic-kingdom staging dir
scp tools/prompt-optimize/personas/iter-7-component-isolation.md \
    jesse@magic-kingdom:/home/jesse/git/terminal-bench/runs/lace-qwen-chainfix/lace/packages/agent/config/agent-personas/benchmark-opt.md

# 2. Launch eval (can run from local machine via SSH heredoc)
ssh jesse@magic-kingdom bash -s <<'REMOTE'
export PATH="$HOME/.local/bin:$PATH"
cd /home/jesse/git/terminal-bench/runs/lace-qwen-chainfix
set -a; source /home/jesse/git/terminal-bench/.env; set +a

nohup harbor run \
    --dataset "terminal-bench@2.0" \
    --agent-import-path lace_agent:LaceAgent \
    --job-name YOUR_JOB_NAME \
    --model openrouter/qwen/qwen3.5-flash-02-23 \
    --ak persona=benchmark-opt \
    --agent-setup-timeout-multiplier 3.0 \
    -t openssl-selfsigned-cert \
    -t count-dataset-tokens \
    -t circuit-fibsqrt \
    -t financial-document-processor \
    -t fix-ocaml-gc \
    -t build-cython-ext \
    > /tmp/YOUR_JOB_NAME.log 2>&1 &
echo "PID: $!"
REMOTE

# 3. Check status
ssh jesse@magic-kingdom 'for d in /home/jesse/git/terminal-bench/runs/lace-qwen-chainfix/jobs/YOUR_JOB_NAME/*/; do [ -f "$d/result.json" ] && echo "$(basename $d): $(python3 -c "import json; print(json.load(open(\"${d}result.json\")).get(\"verifier_result\",{}).get(\"rewards\",{}).get(\"reward\",\"?\"))")" || echo "$(basename $d): RUNNING"; done'
```

### IMPORTANT: Harbor gotchas

- **Model flag**: Use `--model openrouter/qwen/qwen3.5-flash-02-23`, NOT `--ak model=...`. The `--ak model` is silently ignored — the lace agent only reads model from harbor's `--model` flag.
- **Task flag**: Use `-t TASK` or `--task-name TASK`, NOT `--task`.
- **Harbor PATH**: `export PATH="$HOME/.local/bin:$PATH"` is required.
- **Env vars**: `set -a; source .env; set +a` (NOT `source .env` alone).
- **Root-owned dirs**: Docker containers create root-owned files in job dirs. Use `sudo rm -rf` to clean up failed jobs.
- **Stale containers**: Kill harbor PID and `docker rm -f CONTAINER` for leftovers from aborted runs.
- **count-dataset-tokens**: Needs `--agent-setup-timeout-multiplier 3.0` minimum.
- **Background launch**: Use `nohup ... &` inside SSH heredoc, NOT direct SSH with `&`.

## Interrogation-Driven Optimization Loop

```
1. Get a failed trajectory (events.jsonl from a lace eval run)
2. Run `events` to see the trajectory overview
3. Run `interrogate` asking "why did you do X? what prompt language would change this?"
4. Take the model's suggested wording
5. Update the persona
6. Run `resume --up-to-event N --persona NEW.md --reps 3` to verify behavior changes
   (This is MORE RELIABLE than interrogate for testing prompt changes)
7. Compare with old persona at the same point to confirm it's the change, not noise
8. After 5-8 iterations, run a full eval to validate
```

### Key Finding: What Makes Instructions Stick for Qwen 3.5 Flash

From 10+ interrogation iterations, the model told us:

1. **"NEVER" >> "prefer"** — absolute language weighted much higher than suggestions
2. **Consequences > prohibitions** — "pip won't persist in clean container" > "don't use pip"
3. **Observable actions > mental checks** — "print column names" forces verifiable tool call
4. **Position in Critical Rules section** — carries more weight than later sections
5. **Gates with enumerate+verify** — "list your imports, is each stdlib?" > "would scripts work?"
6. **Define "attempt" explicitly** — "rewriting same file = same strategy" prevents thrashing
7. **"Fetching ≠ reading"** — model will fetch a URL and skip reading the saved file unless told explicitly

## Trajectory & Score Table

See `trajectory.md` for full iteration history, score table, and detailed findings.
