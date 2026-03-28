# Eval Infrastructure Reference

## Running evals

Evals run on **AWS spot instances** via harbor-runner. Three workflows:

### Full baseline (all 89 tasks × 3 reps)
```bash
./tools/run_full_baseline.sh                  # build, stage, launch
./tools/run_full_baseline.sh --dry-run        # preview without launching
./tools/run_full_baseline.sh --reps 5         # more reps
```
Saves launch metadata to `.serf-launches/` for `post_run.sh` to read.

### Single experiment (specific tasks)
```bash
cd ~/prime-radiant/harbor-runner
./launch.sh \
  --agent-dir /tmp/agent-experiment \
  --agent-import-path serf_agent:SerfAgent \
  --model openai/gpt-5.4-mini \
  --task-names "chess-best-move,kv-store-grpc" \
  --reps 3 \
  --instance-type c6i.xlarge
```

### Local coordinator delegation test (~2 min/run)
```bash
set -a; source .env; set +a
./tools/coord-repro/run-test.sh label [coordinator.md-path]
./tools/coord-repro/run-batch.sh /tmp/coord-variants/ 2
```
Binary signal: DELEGATE vs BYPASS. For fast iteration on coordinator prompts.

## Post-experiment workflow

```bash
./tools/check_run.sh RUN_ID                              # poll status
./tools/post_run.sh RUN_ID --variant "description"       # collect + diff
./tools/interrogate_failures.sh RUN_ID                   # auto-interrogate
git add docs/experiments/ && git commit -m "results: RUN_ID"
```

`post_run.sh` auto-reads model/SHA/branch from `.serf-launches/` if the run
was launched with `run_full_baseline.sh`.

## Session interrogation

```bash
# List sessions
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK --list-sessions

# Interrogate coordinator (default)
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK \
    --question "Why did you not delegate?"

# Interrogate subagent by index
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK --session 2 \
    --question "Why did you override the correct answer?"

# Auto-interrogate ALL failures (coordinator + subagents)
./tools/interrogate_failures.sh RUN_ID
```

## Scoreboard

```bash
./tools/scoreboard.py                         # full 89-task matrix
./tools/scoreboard.py --task TASK             # single task history
./tools/scoreboard.py --failing               # tasks with score < 1.0
./tools/scoreboard.py --sort score            # sorted by score
```

## Results system

- S3 canonical: `s3://harbor-eval-results-526275945504/runs/RUN_ID/`
- Local cache: `~/.serf-evals/tasks/{task}/{run}/{rep}/`
- Git-tracked metadata: `docs/experiments/scoreboard.json`, `runs/*.json`, `tasks/*.json`
- Scoring: mean of reps from most recent run

## Key rules

- **NEVER run evals on magic-kingdom.** AWS spot only.
- **Commit before deploying.** `run_full_baseline.sh` enforces this.
- **`make build-linux`** invalidates Go embed cache. Never use raw `go build`.
- **Verify binaries:** `strings serf-linux-amd64 | grep "expected phrase"`
- **Spot quota:** 128 vCPU (32 × c6i.xlarge or 16 × c6i.2xlarge)
- **Staging:** use isolated subdirectory, not directly under /tmp/

## Project docs (authoritative)

- `docs/experiments/NOTEBOOK.md` — current state, experiment log
- `docs/experiments/infrastructure.md` — full deployment details
- `docs/experiments/task-sets.md` — regression and target task lists
- `docs/experiments/prompt-lessons.md` — GPT prompt engineering findings
