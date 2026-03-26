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

# First launch uploads the agent tarball; subsequent ones skip it.
REP=1
for task in "${TASKS[@]}"; do
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
`s3://bucket/runs/RUN_ID/rep-N/...`. No sleep needed — `--run-id` overrides
the timestamp-based ID, and `--rep` prevents S3 path collisions.

### Spot instance rules

- **1 task per instance for long tasks.** One task's timeout eats into the other's
  budget. Use the loop pattern above.
- Fast regression tasks (< 5 min) are the exception: batch with
  `--task-names "task1,task2,..." --concurrency 8` on one instance.
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

### Workspace tree vs parent tree

The parent tree scan (`scanParentTree`) dumps the workspace's parent directory
contents into the system prompt. When the workspace is under `/tmp/`, this picks
up thousands of experiment files, inflating subagent prompts from ~10K (AWS) to
~88K (local).

This was a massive confounder: local implementers appeared to read READMEs because
the dump included HuggingFace cache paths. On AWS (clean Docker, `/app` workspace),
prompts were lean and the implementer never found the README.

**Fix:** The workspace tree change in `profile.go` replaces parent tree with
workspace tree. When running local experiments before this fix ships, use workspaces
outside `/tmp/` or verify that prompt sizes match the target environment.

## Building experiment variants

Never delegate binary builds to a subagent. The subagent loses context about clean
state and applies patches incrementally instead of resetting to a known base.

For each variant:
1. Write clean base files to `/tmp/clean-*.md`
2. Copy clean base, apply changes, build, verify with `strings`, reset
3. Start experiments on 1 task before running the full suite
4. Kill variants that go 0/2 on tasks the baseline passes 3/3
