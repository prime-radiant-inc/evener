# Eval Infrastructure

How to build, deploy, and run serf evals against terminal-bench.

## Building serf for AWS

Cross-compile for Linux using the Makefile target:

```bash
cd ~/prime-radiant/serf
make build-linux
```

This invalidates the Go build cache for the agent package (ensuring embedded
templates/sections/agent .md files are fresh) and stamps the binary with git
SHA and build time via ldflags. Output: `serf-linux-amd64` in repo root.

**CRITICAL: `go build` caches compiled packages.** When you change embedded files
(templates, sections, agent prompts), `go build` may serve stale content. `make
build-linux` handles this automatically. If you must build manually, run
`go clean -cache` first.

Always verify the binary contains expected prompt text:

```bash
strings serf-linux-amd64 | grep "expected phrase"
strings serf-linux-amd64 | grep -c "must not appear"
```

**Commit before deploying.** Every experiment that gets pushed to AWS MUST be
committed on a branch first. This gives provenance, rollback safety, and prevents
losing work to accidental `git checkout`.

```bash
git checkout -b exp/EXPERIMENT-NAME
git add -A && git commit -m "experiment: EXPERIMENT-NAME"
make build-linux
```

## Staging for harbor-runner

Create an agent directory with all required files:

```bash
mkdir -p /tmp/agent-EXPERIMENT
cp ~/prime-radiant/serf/serf-linux-amd64 /tmp/agent-EXPERIMENT/
cp ~/prime-radiant/serf/tools/serf_agent.py /tmp/agent-EXPERIMENT/
cp ~/prime-radiant/serf/tools/install-serf.sh.j2 /tmp/agent-EXPERIMENT/
```

**Do NOT put agent dirs directly under /tmp/.** harbor-runner copies `*-linux-*`
from the parent directory, which can contaminate your agent dir with old binaries.
Use an isolated subdirectory: `/tmp/exp-name/agent/`.

## Launching on AWS spot instances

```bash
cd ~/prime-radiant/harbor-runner

./launch.sh \
  --agent-dir /tmp/agent-EXPERIMENT \
  --agent-import-path serf_agent:SerfAgent \
  --model openai/gpt-5.4 \
  --task-names "task-name" \
  --reps 3 \
  --instance-type c6i.xlarge
```

### Launching multiple tasks (one per instance)

harbor-runner's `--task-names` runs all listed tasks on each instance. For
one-task-per-instance (required for long tasks), use `--run-id` to share a
single run ID across parallel launches:

```bash
AGENT_DIR=/tmp/agent-experiment
RUN_ID="my-experiment-name"
TASKS=(sanitize-git-repo feal-linear-cryptanalysis kv-store-grpc regex-log)

# First launch uploads the agent tarball (foreground — must finish before
# the rest start, or parallel launches race on the staging directory).
./launch.sh \
  --run-id "$RUN_ID" \
  --rep 1 \
  --agent-dir "$AGENT_DIR" \
  --agent-import-path serf_agent:SerfAgent \
  --model openai/gpt-5.4 \
  --task-names "${TASKS[0]}" \
  --concurrency 1 \
  --instance-type c6i.xlarge

# Remaining launches find the tarball in S3 and skip upload — safe to parallelize.
REP=2
for task in "${TASKS[@]:1}"; do
  ./launch.sh \
    --run-id "$RUN_ID" \
    --rep $((REP++)) \
    --agent-dir "$AGENT_DIR" \
    --agent-import-path serf_agent:SerfAgent \
    --model openai/gpt-5.4 \
    --task-names "$task" \
    --concurrency 1 \
    --instance-type c6i.xlarge &
done
wait
```

Each task gets a unique rep number under the shared run ID. Results land in
`s3://bucket/runs/RUN_ID/rep-N/...`. The first launch must run in the
foreground because `launch.sh` uses a staging directory keyed by run ID —
parallel launches race on creating/deleting it. Once the tarball is in S3,
subsequent launches skip the upload and can safely run in parallel.

### Launching evals

All eval launches go through a single script:

```bash
# Full 89-task eval (3 reps, one task per instance)
./tools/run_eval.sh --wave

# Subset of tasks
./tools/run_eval.sh --wave --tasks "chess-best-move,kv-store-grpc"
./tools/run_eval.sh --wave --tasks failing       # all currently failing
./tools/run_eval.sh --wave --tasks untested      # all untested
./tools/run_eval.sh --wave --tasks hard          # historically hard 16
./tools/run_eval.sh --wave --tasks failing --dry-run  # preview
```

Builds the binary, stages it, extracts task names, launches on spot instances.
Enforces clean working tree (must commit before launching). Saves launch
metadata to `.serf-launches/` for `post_run.sh`.

**Always use `--wave` (one task per instance).** Each task gets its own
c6i.xlarge instance. The launcher saturates spot quota (32 concurrent at
128 vCPU), polls for completion, and backfills freed slots. Full 89-task
×3 reps completes in ~90 min, with the first complete rep of scores
available in ~30 min.

Override defaults: `--reps 5 --model openai/gpt-5.4 --instance-type c6i.2xlarge`

### Monitoring and collecting

```bash
./tools/run_status.sh RUN_ID                             # live instance status
./tools/post_run.sh RUN_ID --variant "description"       # collect + scoreboard diff
```

`post_run.sh` auto-reads model/SHA/branch from `.serf-launches/` if the run
was launched with `run_eval.sh`.

### Spot instance rules

- **1 task per instance, always.** Never batch multiple tasks on one instance.
  One task's timeout eats into others' budgets, and resource contention
  changes agent behavior.
- **Always use `--wave` mode.** The default batch mode (multiple tasks per
  instance with `--concurrency 8`) is broken and should not be used.
- **NEVER run evals on magic-kingdom.** It gets congested with Docker containers
  and causes failures. magic-kingdom is for staging and reading results only.
- Use `--run-id` for parallel launches. Without it, auto-generated IDs have
  minute granularity and will collide.
- On-demand vCPU quota is 64; spot quota is 128.
- Clean /tmp between experiment rounds — downloaded results fill the Mac's disk.

## Collecting and checking results

### S3 results location

Results upload to: `s3://harbor-eval-results-526275945504/runs/RUN_ID/`

Check results with the python script pattern:

```python
# List runs
s3_ls("runs/")

# Check a specific run's results
s3_cat(f"runs/{run_id}/rep-1/task-name/reward.txt")
```

Instances self-terminate after uploading. "User initiated" termination in the AWS
console is normal behavior.

### Verifier vs agent visibility

**CRITICAL: The verifier's `/tests/` directory is mounted ONLY during the
verification phase, AFTER the agent has finished.** The running agent CANNOT
see verifier tests. The agent can only see:
- Files in the task workspace (`/app/` or similar)
- Files it creates itself
- Tests that the TASK provides (e.g., a Makefile test target, test.py in /app/)

When analyzing failures, remember: the agent could never have run the verifier
tests. It must self-verify using its own tests or by checking its output against
the task description's requirements. "The agent should have run the tests under
/tests/" is always wrong — it couldn't.

### Transcript locations

Full agent transcripts: `agent/agent-state/sessions/*.transcript.jsonl`

Each line is JSON. Line 1 has `kind: "header"` with session metadata and system
prompt. Lines 2+ have `kind: "entry"` with conversation turns. Always check
transcript headers after a run to confirm the correct prompt was used.

### api.jsonl

`agent/agent-state/api.jsonl` contains all LLM API calls with raw responses.
Useful for debugging token usage and model behavior, but large — read transcripts
first.

## serf_agent.py kwargs

The harbor adapter (`serf_agent.py`) accepts these kwargs via `--ak key=value`:

- `reasoning_effort` — reasoning effort level for the model
- `system_prompt_as_user` — deliver system prompt as user message instead of instructions parameter
- `system_prompt_append` — path to file appended to system prompt (root session only, does NOT reach subagents)
- `plugin_dirs` — additional plugin directories

**`system_prompt_append` only reaches the root session (coordinator).** If the
implementer needs to see a prompt change, modify the section files in
`agent/prompts/sections/` — these are embedded in the binary and reach all agents.

## Local testing

### Full coordinator pipeline (~15 min per run)

```bash
set -a; source .env; set +a
make build
./serf --provider openai --model gpt-5.4-mini \
  --max-rounds 20 \
  --state-dir /tmp/serf-test-TASK \
  -- "$(cat /tmp/task-description.md)"
```

Tasks expecting `/app/` paths won't resolve locally. Options: adjust the prompt,
run in Docker, or just observe the agent's strategy from the transcript.

### Implementer-only repro (~3 min per run)

For fast iteration on implementer behavior, bypassing the coordinator:

```bash
OPENAI_API_KEY=... ./tools/impl-repro/run-test.sh /path/to/serf-binary label
```

This sends the implementer an AWS-style coordinator delegation message directly.
The harness and data file live in `tools/impl-repro/`. Use when:
- The problem is implementer behavior, not coordinator delegation
- You need to test 10+ prompt variants quickly
- Local coordinator runs don't reproduce the AWS failure mode

### Coordinator delegation repro (~2 min per run)

For fast iteration on whether the coordinator delegates or bypasses, using
chess-best-move as the test case:

```bash
OPENAI_API_KEY=... ./tools/coord-repro/run-test.sh label [coordinator.md-path]
```

Builds serf from current source (or with a replacement coordinator.md), sets up
a workspace with the chess board image, runs the full coordinator pipeline with
`--max-rounds 8`, and checks the transcript for `spawn_agent` (DELEGATE) vs
`write_file`/shell write (BYPASS).

To batch-test many variants at once:

```bash
# Create a directory of coordinator.md variants
mkdir /tmp/coord-variants
cp agent/agents/coordinator.md /tmp/coord-variants/00-baseline.md
# ... create more variants ...

# Run each variant 2x (default) or Nx
OPENAI_API_KEY=... ./tools/coord-repro/run-batch.sh /tmp/coord-variants/ 3
```

The harness lives in `tools/coord-repro/`. Use when:
- The problem is coordinator delegation behavior
- You need to test 10+ prompt variants quickly
- The signal is binary (delegate vs bypass), not answer correctness

Uses `--reasoning-effort none` and `gpt-5.4-mini` to match AWS eval conditions.

### Workspace tree (fixed)

The system prompt includes a workspace tree showing files in the working
directory. Previously, `scanParentTree` dumped the parent directory contents,
which under `/tmp/` picked up thousands of experiment files (inflating prompts
from ~10K to ~88K). This was a confounder for local testing — fixed by
replacing parent tree with workspace tree (commit 9a284f5).

## Building experiment variants

Never delegate binary builds to a subagent. The subagent loses context about clean
state and applies patches incrementally instead of resetting to a known base.

For each variant:
1. Write clean base files to `/tmp/clean-*.md`
2. Copy clean base, apply changes, build, verify with `strings`, reset
3. Start experiments on 1 task before running the full suite
4. Kill variants that go 0/2 on tasks the baseline passes 3/3

## Results collection and scoreboard

Three-layer results system: S3 (canonical archive) → local cache (collateral for
interrogation) → git-tracked metadata (scores and history).

### Collecting results after a run

```bash
./tools/collect_results.py RUN_ID \
    --model openai/gpt-5.4-mini \
    --git-sha $(git rev-parse --short HEAD) \
    --variant "description of what changed"
```

This downloads from S3, normalizes into `~/.serf-evals/tasks/{task}/{run}/{rep}/`,
extracts rewards, and updates git-tracked metadata:

- `docs/experiments/runs/{run-id}.json` — per-run metadata
- `docs/experiments/tasks/{task}.json` — per-task scorecard with full history
- `docs/experiments/scoreboard.json` — the 89-task matrix

Use `--light` for fast backfill (only downloads reward.txt, not transcripts).

### Viewing the scoreboard

```bash
./tools/scoreboard.py                         # Full 89-task matrix
./tools/scoreboard.py --task kv-store-grpc    # Single task history
./tools/scoreboard.py --failing               # Tasks with score < 1.0
./tools/scoreboard.py --untested              # Tasks not yet tested
./tools/scoreboard.py --sort score            # Sort by score descending
```

### Scoring rule

Score = mean of reps from the most recent run. For parallel experiments on the
same date, the highest score wins. Previous runs are history, not part of the
current score.

### Local cache

Collateral lives at `~/.serf-evals/tasks/{task}/{run}/{rep}/` organized
task-first for easy investigation. Full transcripts and API logs included
in default mode. Cache can be cleaned without losing metadata (re-download
from S3 on demand).

### Post-experiment workflow

```bash
# 1. Check status (repeat until all instances terminated)
./tools/check_run.sh RUN_ID

# 2. Collect results + update scoreboard (auto-reads launch metadata)
./tools/post_run.sh RUN_ID --variant "description of what changed"

# 3. Auto-interrogate every failure (coordinator + all subagents)
./tools/interrogate_failures.sh RUN_ID

# 4. Commit metadata update
git add docs/experiments/ && git commit -m "results: RUN_ID"
```

If the run was launched with `run_eval.sh`, `post_run.sh` auto-reads
the model, git SHA, and branch from `.serf-launches/RUN_ID.json` — no need
to pass `--model` or `--git-sha` manually.

## Session interrogation

Resume a completed session and ask the model about its decisions. The model is
placed back in its exact original context (same system prompt, tool calls, results).

```bash
# List all sessions for a rep (shows role, model, turn count)
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK --list-sessions

# Interrogate the coordinator (default — session 1)
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK \
    --question "Why did you not delegate?"

# Interrogate a subagent by index (from --list-sessions)
python3 tools/interrogate_session.py \
    --run RUN_ID --rep REP --task TASK \
    --session 2 \
    --question "Why did you override the computational proof?"

# Interrogate all failures at once (coordinator + subagents, standard questions)
./tools/interrogate_failures.sh RUN_ID
```

Always interrogate both the coordinator AND subagents in the failure chain.
Standard questions cover delegation decisions (coordinator) and verification
approach (subagents). Add `--question` for task-specific follow-ups.
