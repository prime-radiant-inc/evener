# GEPA-Optimized Serf Skills via Terminal-Bench

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Use GEPA's `optimize_anything` to evolve a skills supplement for serf's system prompt, validated against terminal-bench discriminator tasks via harbor on magic-kingdom.local.

**Architecture:** GEPA's optimization loop runs on magic-kingdom.local alongside harbor (already deployed). For each candidate skills text, the evaluator writes it to a file and launches a harbor run with `--ak system_prompt_append=<path>`. Harbor handles container lifecycle, serf execution, and result verification. GEPA reads the reward files and feeds diagnostic traces back to its reflection LM. The candidate artifact is a skills supplement injected via serf's `--system-prompt-append` flag, leaving the base prompt unchanged.

**Tech Stack:** Python 3.12, GEPA (`optimize_anything`), harbor (already on magic-kingdom), serf (Go, cross-compiled for Linux, already deployed), gpt-5.4 (agent + reflection)

**Key Machines:**
- `magic-kingdom.local` (jesse@magic-kingdom) — Ryzen 9, 16 cores, 60GB RAM, Docker, harbor installed, serf binary deployed, terminal-bench@2.0 dataset cached
- Local Mac — development, plan writing, monitoring

**Budget:** ~$500 total. Cost dominated by gpt-5.4 API calls for serf task execution.

**Existing baseline:** serf at 65.2% on terminal-bench with gpt-5.3-codex. Best known agent (Droid + GPT-5.3-Codex) achieves 77.3%. The 56 discriminator tasks (10-75% failure rate) provide the best signal for optimization.

**Key advantage over SWE-bench approach:** Harbor + terminal-bench infrastructure is already fully operational. No Docker image building. serf_agent.py already supports `system_prompt_append` as an agent kwarg.

---

## Chunk 1: Infrastructure and Evaluator

### Task 1: Install GEPA on magic-kingdom

**Files:**
- Create: `/home/jesse/git/serf-gepa/requirements.txt`

- [ ] **Step 1: Create project directory and venv**

```bash
ssh jesse@magic-kingdom "mkdir -p ~/git/serf-gepa && python3 -m venv ~/git/serf-gepa/.venv"
```

Expected: directory and venv created.

- [ ] **Step 2: Create requirements.txt and install**

SCP to magic-kingdom `/home/jesse/git/serf-gepa/requirements.txt`:

```
gepa[full]
python-dotenv
tqdm
```

Then install:

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && pip install -r ~/git/serf-gepa/requirements.txt"
```

Expected: all packages install. GEPA's `[full]` extra pulls litellm, datasets, tqdm, cloudpickle.

Note: We do NOT need `swesmith` or `swebench` — harbor handles all container management.

- [ ] **Step 3: Verify imports**

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -c \"
from gepa.optimize_anything import optimize_anything, GEPAConfig, EngineConfig
import gepa.optimize_anything as oa
print('GEPA imports OK')
\""
```

Expected: `GEPA imports OK`

- [ ] **Step 4: Verify API keys and harbor**

```bash
ssh jesse@magic-kingdom "
  test -n \"\$OPENAI_API_KEY\" && echo 'OPENAI_API_KEY: set' || echo 'OPENAI_API_KEY: MISSING'
  which harbor && harbor --version
  ls ~/git/terminal-bench/serf-linux-amd64 && echo 'serf binary: present'
"
```

Expected: API key set, harbor found, serf binary present.

If API key missing: it needs to be available both in the shell environment (for GEPA's reflection LM via litellm) AND in harbor's environment (for serf inside containers). Check `~/.bashrc` or harbor's `.env` setup.

- [ ] **Step 5: Init git repo and commit**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git init && git add requirements.txt && git commit -m 'chore: initial setup with GEPA dependencies'"
```

---

### Task 2: Cost Model and Parameter Selection

**Files:**
- Create: `/home/jesse/git/serf-gepa/config.py`

- [ ] **Step 1: Check gpt-5.4 pricing**

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -c \"
import litellm
for k, v in litellm.model_cost.items():
    if '5.4' in k or '5-4' in k:
        inp = v.get('input_cost_per_token', 0) * 1e6
        out = v.get('output_cost_per_token', 0) * 1e6
        print(f'{k}: \\\${inp:.2f}/M input, \\\${out:.2f}/M output')
\""
```

If gpt-5.4 isn't in litellm's cost data, check OpenAI's pricing page manually. Estimate total tokens per terminal-bench task run (~200k-500k across all tool rounds).

- [ ] **Step 2: Estimate per-task cost and calculate parameters**

Estimate `COST_PER_TASK` from pricing data. Then:

```
TOTAL_BUDGET = 500
REFLECTION_BUDGET ≈ 50     # ~10% for GEPA reflection calls
AGENT_BUDGET = 450

MAX_AGENT_RUNS = AGENT_BUDGET / COST_PER_TASK

# Reserve 20% for val evaluation, 20% for final test eval
TRAIN_CALLS = int(MAX_AGENT_RUNS * 0.60)
VAL_CALLS   = int(MAX_AGENT_RUNS * 0.20)
TEST_CALLS  = int(MAX_AGENT_RUNS * 0.20)
```

For GEPA's dataset splits, we want:
- **Train:** 30 discriminator tasks (the hardest ones, 30-75% failure — most room for improvement)
- **Val:** 10 discriminator tasks (medium difficulty, 15-30% failure)
- **Test:** remaining 16 discriminators (held out for final evaluation)

GEPA evaluates `reflection_minibatch_size` tasks per reflection step (default 3). With `max_metric_calls=80`, that's ~26 reflection steps across 30 training tasks.

- [ ] **Step 3: Write config.py**

Create `/home/jesse/git/serf-gepa/config.py`:

```python
"""Configuration for GEPA optimization of serf via terminal-bench."""

# --- Budget ---
TOTAL_BUDGET_USD = 500
COST_PER_TASK_USD = 3.50  # UPDATE after Step 1

# --- Models ---
AGENT_MODEL = "openai/gpt-5.4"  # Model string for harbor (provider/model)
REFLECTION_MODEL = "gpt-5.4"    # Model string for litellm (GEPA reflection)

# --- Terminal-Bench ---
# 56 discriminator tasks sorted by failure rate (hardest first).
# Split: first 30 = train, next 10 = val, last 16 = test.
# All 56 discriminator tasks (10-75% failure rate).
# Shuffled with seed=42 for reproducibility, then split into
# train/val/test. Shuffling ensures each split has a representative
# mix of difficulty levels rather than clustering by hardness.
import random as _rng
DISCRIMINATORS = [
    "make-mips-interpreter", "gcode-to-text", "regex-chess",
    "polyglot-c-py", "polyglot-rust-c", "query-optimize",
    "path-tracing", "adaptive-rejection-sampler", "qemu-alpine-ssh",
    "path-tracing-reverse", "protein-assembly", "chess-best-move",
    "write-compressor", "configure-git-webserver", "tune-mjcf",
    "winning-avg-corewars", "cancel-async-tasks", "financial-document-processor",
    "overfull-hbox", "sanitize-git-repo", "extract-elf",
    "schemelike-metacircular-eval", "compile-compcert", "feal-linear-cryptanalysis",
    "circuit-fibsqrt", "break-filter-js-from-html", "sparql-university",
    "largest-eigenval", "build-pmars", "mailman",
    "large-scale-text-editing", "bn-fit-modify", "qemu-startup",
    "rstan-to-pystan", "build-cython-ext", "password-recovery",
    "pytorch-model-cli", "feal-differential-cryptanalysis", "count-dataset-tokens",
    "sqlite-db-truncate", "llm-inference-batching-scheduler", "reshard-c4-data",
    "mcmc-sampling-stan", "fix-ocaml-gc", "openssl-selfsigned-cert",
    "sqlite-with-gcov", "pytorch-model-recovery", "build-pov-ray",
    "crack-7z-hash", "kv-store-grpc", "hf-model-inference",
    "headless-terminal", "merge-diff-arc-agi-task", "pypi-server",
    "regex-log", "fix-code-vulnerability",
]
_shuffled = DISCRIMINATORS.copy()
_rng.Random(SEED).shuffle(_shuffled)

TRAIN_TASKS = _shuffled[:30]    # 30 tasks, mixed difficulty
VAL_TASKS = _shuffled[30:40]    # 10 tasks, mixed difficulty
TEST_TASKS = _shuffled[40:]     # 16 tasks, mixed difficulty

# --- GEPA ---
MAX_METRIC_CALLS = 80       # Total evaluator invocations during training
REFLECTION_MINIBATCH_SIZE = 3
SEED = 42

# --- Harbor ---
HARBOR_CONCURRENCY = 4      # Parallel containers (conservative for 60GB RAM)
HARBOR_REPS = 1             # 1 rep per task per evaluation (budget constraint)
AGENT_TIMEOUT = 900         # 15 min per task (harbor default)
HARBOR_DATASET = "terminal-bench@2.0"

# --- Paths (on magic-kingdom) ---
SERF_BINARY = "/home/jesse/git/terminal-bench/serf-linux-amd64"
TERMINAL_BENCH_DIR = "/home/jesse/git/terminal-bench"
RESULTS_DIR = "/home/jesse/git/serf-gepa/gepa_results"
```

- [ ] **Step 4: Commit config**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add config.py && git commit -m 'feat: budget and parameter configuration'"
```

---

### Task 3: Write the Terminal-Bench Evaluator

**Files:**
- Create: `/home/jesse/git/serf-gepa/tb_evaluator.py`
- Create: `/home/jesse/git/serf-gepa/test_tb_evaluator.py`

The evaluator bridges GEPA and harbor. For each (candidate, task) pair, it:
1. Writes candidate skills to a temp file on magic-kingdom
2. Launches `harbor run` for that single task with `--ak system_prompt_append=<path>`
3. Waits for completion
4. Reads the reward and agent traces
5. Returns (score, side_info) to GEPA

**Design decision — harbor per-task vs batch:**
Harbor is designed for batch runs but supports single-task invocation via `--task-name`. Running harbor once per GEPA evaluator call is simple and correct, but has ~30s setup overhead per task. With 80 metric calls, that's ~40 min of overhead total — acceptable.

Alternative: batch multiple evaluator calls into one harbor run. More efficient but requires custom batching logic. Start simple, optimize later if needed.

- [ ] **Step 1: Write failing test**

SCP to magic-kingdom `/home/jesse/git/serf-gepa/test_tb_evaluator.py`:

```python
"""Tests for terminal-bench evaluator."""
import pytest


def test_evaluator_module_imports():
    """Evaluator module exists and has expected functions."""
    from tb_evaluator import create_tb_fitness_fn, run_harbor_task
    assert callable(create_tb_fitness_fn)
    assert callable(run_harbor_task)


def test_parse_harbor_result():
    """Result parser extracts reward from harbor output structure."""
    from tb_evaluator import parse_harbor_result
    import json
    import tempfile
    from pathlib import Path

    # Simulate harbor output structure
    with tempfile.TemporaryDirectory() as tmpdir:
        task_dir = Path(tmpdir) / "test-task__abc123"
        task_dir.mkdir()
        (task_dir / "reward.txt").write_text("1.0\n")
        (task_dir / "result.json").write_text(json.dumps({
            "verifier": {"rewards": {"reward": 1.0}},
        }))

        reward, info = parse_harbor_result(Path(tmpdir), "test-task")
        assert reward == 1.0
        assert "reward" in info


def test_parse_harbor_result_failure():
    """Parser handles missing reward gracefully."""
    from tb_evaluator import parse_harbor_result
    import tempfile
    from pathlib import Path

    with tempfile.TemporaryDirectory() as tmpdir:
        reward, info = parse_harbor_result(Path(tmpdir), "nonexistent-task")
        assert reward == 0.0
```

- [ ] **Step 2: Run test to verify it fails**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -m pytest test_tb_evaluator.py -v"
```

Expected: FAIL — `tb_evaluator` module not found.

- [ ] **Step 3: Write the evaluator**

SCP to magic-kingdom `/home/jesse/git/serf-gepa/tb_evaluator.py`:

```python
"""Terminal-bench evaluator for GEPA optimize_anything.

Bridges GEPA's per-example evaluation to harbor's task execution on
magic-kingdom. Each evaluator call runs a single terminal-bench task
via harbor with candidate skills injected.

The evaluator is synchronous: it blocks until harbor completes the task,
then reads the reward and returns (score, side_info) to GEPA.
"""

import json
import logging
import os
import subprocess
import tempfile
import time
import uuid
from pathlib import Path

logger = logging.getLogger(__name__)


def parse_harbor_result(
    jobs_dir: Path, task_name: str
) -> tuple[float, dict]:
    """Parse harbor output to extract reward and diagnostics.

    Harbor writes results to:
        <jobs_dir>/<timestamp>/<task_name>__<hash>/reward.txt
        <jobs_dir>/<timestamp>/<task_name>__<hash>/result.json
        <jobs_dir>/<timestamp>/<task_name>__<hash>/agent/

    We search for the task directory by prefix match on task_name.

    Returns:
        (reward, info_dict) where reward is 0.0 or 1.0
    """
    info = {"reward": 0.0, "task_name": task_name}

    # Find the most recent timestamp directory
    if not jobs_dir.exists():
        return 0.0, info

    timestamp_dirs = sorted(jobs_dir.iterdir(), reverse=True)
    for ts_dir in timestamp_dirs:
        if not ts_dir.is_dir():
            continue

        # Find task directory (format: task-name__hash)
        for entry in ts_dir.iterdir():
            if entry.is_dir() and entry.name.startswith(task_name):
                # Found the task result directory
                reward_file = entry / "reward.txt"
                result_file = entry / "result.json"

                if reward_file.exists():
                    try:
                        reward = float(reward_file.read_text().strip())
                        info["reward"] = reward
                    except (ValueError, OSError):
                        reward = 0.0

                if result_file.exists():
                    try:
                        result = json.loads(result_file.read_text())
                        info["result"] = result
                    except (json.JSONDecodeError, OSError):
                        pass

                # Try to read agent trace
                agent_dir = entry / "agent"
                if agent_dir.exists():
                    # Look for trajectory or transcript
                    for trace_file in ["agent-state/trajectory.json",
                                       "command-0"]:
                        trace_path = agent_dir / trace_file
                        if trace_path.exists():
                            try:
                                trace = trace_path.read_text()
                                info["agent_trace"] = trace[:8000]
                            except OSError:
                                pass
                            break

                return info.get("reward", 0.0), info

    return 0.0, info


def run_harbor_task(
    task_name: str,
    model: str,
    skills_file: str | None = None,
    job_name: str | None = None,
    jobs_dir: str = "/data/agent-evals/runs",
    adapter: str = "serf_agent:SerfAgent",
    dataset: str = "terminal-bench@2.0",
    timeout: int = 1200,
    tb_dir: str | None = None,
) -> tuple[float, dict]:
    """Run a single terminal-bench task via harbor and return (reward, info).

    Args:
        task_name: Terminal-bench task name (e.g., "regex-chess")
        model: Model string (e.g., "openai/gpt-5.4")
        skills_file: Optional path to skills file for --system-prompt-append
        job_name: Harbor job name (auto-generated if None)
        jobs_dir: Harbor jobs directory
        adapter: Harbor agent adapter
        dataset: Harbor dataset
        timeout: Max seconds to wait for harbor
        tb_dir: Terminal-bench directory (where serf_agent.py lives)

    Returns:
        (reward, info_dict) where reward is 0.0 or 1.0
    """
    if tb_dir is None:
        tb_dir = os.environ.get(
            "TERMINAL_BENCH_DIR",
            os.path.expanduser("~/git/terminal-bench"),
        )

    if job_name is None:
        job_name = f"gepa_{task_name}_{uuid.uuid4().hex[:6]}"

    # Build harbor command
    cmd_parts = [
        "harbor", "run",
        "--agent-import-path", adapter,
        "--dataset", dataset,
        "--task-name", task_name,
        "--model", model,
        "-k", "1",           # 1 rep
        "-n", "1",           # 1 concurrent
        "--job-name", job_name,
        "--jobs-dir", jobs_dir,
        "--delete",          # Clean up container after
    ]

    # Inject skills via agent kwargs
    if skills_file:
        cmd_parts.extend(["--ak", f"system_prompt_append={skills_file}"])

    logger.info(f"Running harbor: {task_name} (job={job_name})")

    start_time = time.time()
    try:
        result = subprocess.run(
            cmd_parts,
            capture_output=True,
            text=True,
            timeout=timeout,
            cwd=tb_dir,
            env={**os.environ},  # Inherit env (API keys)
        )
        duration = time.time() - start_time

        if result.returncode != 0:
            logger.warning(
                f"Harbor failed for {task_name}: exit={result.returncode}\n"
                f"stderr: {result.stderr[:1000]}"
            )

    except subprocess.TimeoutExpired:
        duration = time.time() - start_time
        logger.error(f"Harbor timed out for {task_name} after {timeout}s")
        return 0.0, {
            "task_name": task_name,
            "error": f"harbor timeout after {timeout}s",
            "duration_seconds": duration,
        }

    # Parse results
    job_dir = Path(jobs_dir) / job_name
    reward, info = parse_harbor_result(job_dir, task_name)
    info["duration_seconds"] = duration
    info["harbor_stdout"] = result.stdout[:3000] if result.stdout else ""
    info["harbor_stderr"] = result.stderr[:1000] if result.stderr else ""

    return reward, info


def create_tb_fitness_fn(
    model: str = "openai/gpt-5.4",
    jobs_dir: str = "/data/agent-evals/runs",
    tb_dir: str | None = None,
):
    """Create a GEPA-compatible fitness function for terminal-bench.

    Returns a function with signature:
        (candidate: dict, example: dict) -> (float, dict)

    Each call launches a harbor run for one task with the candidate's
    skills injected via --system-prompt-append.

    Args:
        model: Model string for harbor (e.g., "openai/gpt-5.4")
        jobs_dir: Harbor jobs directory on magic-kingdom
        tb_dir: Terminal-bench directory

    Note: This function is NOT parallelized internally — GEPA handles
    parallelism via its own worker pool (EngineConfig.parallel=True,
    max_workers=N). Each GEPA worker calls this function sequentially.
    Harbor runs one container per call.
    """
    # Directory for candidate skills files
    skills_dir = Path("/tmp/gepa_skills")
    skills_dir.mkdir(exist_ok=True)

    def fitness_fn(
        candidate: dict, example: dict
    ) -> tuple[float, dict]:
        """Evaluate candidate skills on a single terminal-bench task.

        Args:
            candidate: dict with "skills" key
            example: dict with "task_name" key

        Returns:
            (score, side_info) where score is 0.0 or 1.0
        """
        import gepa.optimize_anything as oa

        skills = candidate.get("skills", "")
        task_name = example["task_name"]

        # Write skills to a temp file (harbor needs a file path)
        skills_file = None
        if skills.strip():
            skills_path = skills_dir / f"skills_{uuid.uuid4().hex[:8]}.md"
            skills_path.write_text(skills.strip() + "\n")
            skills_file = str(skills_path)

        try:
            reward, info = run_harbor_task(
                task_name=task_name,
                model=model,
                skills_file=skills_file,
                jobs_dir=jobs_dir,
                tb_dir=tb_dir,
            )

            # Log as Actionable Side Information for GEPA reflection
            status = "PASS" if reward == 1.0 else "FAIL"
            duration = info.get("duration_seconds", 0)
            oa.log(f"[{task_name}] {status} ({duration:.0f}s)")

            side_info = {
                "Input": {
                    "task_name": task_name,
                },
                "Generated Outputs": {
                    "agent_trace": info.get("agent_trace", "")[:5000],
                },
                "Feedback": {
                    "status": status,
                    "duration_seconds": duration,
                    "harbor_output": info.get("harbor_stdout", "")[:2000],
                },
                "scores": {"correctness": reward},
            }

            return reward, side_info

        except Exception as e:
            logger.error(f"Evaluation failed for {task_name}: {e}")
            oa.log(f"[{task_name}] HARNESS ERROR: {e}")
            return 0.0, {
                "Input": {"task_name": task_name},
                "Feedback": {"status": f"error: {e}"},
                "scores": {"correctness": 0.0},
            }

        finally:
            # Clean up skills file
            if skills_file and Path(skills_file).exists():
                Path(skills_file).unlink(missing_ok=True)

    return fitness_fn
```

- [ ] **Step 4: Run tests**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -m pytest test_tb_evaluator.py -v"
```

Expected: all 3 tests pass.

- [ ] **Step 5: Commit**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add tb_evaluator.py test_tb_evaluator.py && git commit -m 'feat: terminal-bench evaluator bridging GEPA to harbor'"
```

---

### Task 4: Write the Optimization Script

**Files:**
- Create: `/home/jesse/git/serf-gepa/optimize_serf.py`
- Create: `/home/jesse/git/serf-gepa/test_optimize_serf.py`

- [ ] **Step 1: Write failing test**

SCP to magic-kingdom `/home/jesse/git/serf-gepa/test_optimize_serf.py`:

```python
"""Tests for optimization script."""

def test_imports():
    from optimize_serf import build_dataset, main
    assert callable(build_dataset)
    assert callable(main)


def test_build_dataset():
    from optimize_serf import build_dataset

    tasks = ["task-a", "task-b", "task-c"]
    dataset = build_dataset(tasks)
    assert len(dataset) == 3
    assert dataset[0] == {"task_name": "task-a"}
    assert dataset[2] == {"task_name": "task-c"}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -m pytest test_optimize_serf.py -v"
```

Expected: FAIL.

- [ ] **Step 3: Write the optimization script**

SCP to magic-kingdom `/home/jesse/git/serf-gepa/optimize_serf.py`:

```python
"""GEPA optimization of serf system prompt via terminal-bench.

Runs on magic-kingdom.local. Uses harbor for task execution and GEPA's
optimize_anything for the outer optimization loop.

Usage:
    # Smoke test (2 tasks, few iterations)
    python optimize_serf.py --smoke-test

    # Full run
    python optimize_serf.py

    # Resume from checkpoint
    python optimize_serf.py --resume gepa_results/<run_dir>
"""

import argparse
import json
import logging
import os
import shutil
import sys
import time
from pathlib import Path

from dotenv import load_dotenv

logging.getLogger("LiteLLM").setLevel(logging.WARNING)
logging.getLogger("litellm").setLevel(logging.WARNING)
load_dotenv()

from gepa.optimize_anything import (
    EngineConfig,
    GEPAConfig,
    ReflectionConfig,
    optimize_anything,
)
from gepa.utils.stop_condition import TimeoutStopCondition

from config import (
    AGENT_MODEL,
    HARBOR_CONCURRENCY,
    MAX_METRIC_CALLS,
    REFLECTION_MINIBATCH_SIZE,
    REFLECTION_MODEL,
    RESULTS_DIR,
    SEED,
    TEST_TASKS,
    TRAIN_TASKS,
    VAL_TASKS,
)
from tb_evaluator import create_tb_fitness_fn


def build_dataset(task_names: list[str]) -> list[dict]:
    """Convert task name list to GEPA dataset format.

    GEPA expects a list of dicts. Each dict is passed as `example`
    to the evaluator function.
    """
    return [{"task_name": name} for name in task_names]


def main():
    parser = argparse.ArgumentParser(
        description="GEPA optimization of serf via terminal-bench"
    )
    parser.add_argument("--model", default=AGENT_MODEL)
    parser.add_argument("--reflection-model", default=REFLECTION_MODEL)
    parser.add_argument("--max-metric-calls", type=int, default=MAX_METRIC_CALLS)
    parser.add_argument("--concurrency", type=int, default=HARBOR_CONCURRENCY)
    parser.add_argument("--smoke-test", action="store_true")
    parser.add_argument("--resume", type=str, default=None)
    parser.add_argument("--seed", type=int, default=SEED)
    parser.add_argument("--timeout", type=int, default=43200,
                        help="Max total seconds (default: 12 hours)")
    parser.add_argument("--run-test-eval", action="store_true",
                        help="Evaluate best candidate on test tasks")
    parser.add_argument("--seed-skills", type=str, default=None,
                        help="Path to initial skills file (default: start empty)")
    args = parser.parse_args()

    # Task lists
    train_tasks = TRAIN_TASKS
    val_tasks = VAL_TASKS
    test_tasks = TEST_TASKS

    if args.smoke_test:
        train_tasks = train_tasks[:3]
        val_tasks = val_tasks[:2]
        test_tasks = test_tasks[:2]
        args.max_metric_calls = 10
        args.concurrency = 2
        print("=== SMOKE TEST MODE ===\n")

    # Build GEPA datasets
    train_data = build_dataset(train_tasks)
    val_data = build_dataset(val_tasks)
    test_data = build_dataset(test_tasks)

    # Run directory
    timestamp = time.strftime("%Y%m%d_%H%M%S")
    run_name = f"serf_tb_{timestamp}"
    run_dir = Path(RESULTS_DIR) / run_name
    run_dir.mkdir(parents=True, exist_ok=True)

    # Handle resume
    if args.resume:
        resume_dir = Path(args.resume)
        state_file = resume_dir / "gepa_state.bin"
        if not state_file.exists():
            print(f"ERROR: No gepa_state.bin in {resume_dir}")
            sys.exit(1)
        shutil.copy2(state_file, run_dir / "gepa_state.bin")
        print(f"Resuming from {resume_dir}")

    # Load seed skills if provided
    seed_skills = ""
    if args.seed_skills:
        seed_skills = Path(args.seed_skills).read_text().strip()
        print(f"Loaded seed skills from {args.seed_skills} ({len(seed_skills)} chars)")

    seed_candidate = {"skills": seed_skills}

    # Save config
    config_dict = {
        "model": args.model,
        "reflection_model": args.reflection_model,
        "train_tasks": train_tasks,
        "val_tasks": val_tasks,
        "test_tasks": test_tasks,
        "max_metric_calls": args.max_metric_calls,
        "concurrency": args.concurrency,
        "seed": args.seed,
        "seed_skills": args.seed_skills,
        "timestamp": timestamp,
    }
    with open(run_dir / "config.json", "w") as f:
        json.dump(config_dict, f, indent=2)

    # Create fitness function
    fitness_fn = create_tb_fitness_fn(model=args.model)

    # GEPA config
    gepa_config = GEPAConfig(
        engine=EngineConfig(
            run_dir=str(run_dir),
            seed=args.seed,
            display_progress_bar=True,
            max_metric_calls=args.max_metric_calls,
            candidate_selection_strategy="pareto",
            parallel=True,
            max_workers=args.concurrency,
        ),
        reflection=ReflectionConfig(
            reflection_lm=args.reflection_model,
            reflection_minibatch_size=REFLECTION_MINIBATCH_SIZE,
            skip_perfect_score=True,
            perfect_score=1.0,
        ),
        stop_callbacks=[
            TimeoutStopCondition(timeout_seconds=args.timeout),
        ],
    )

    # Print header
    print("=" * 70)
    print("GEPA Optimization of Serf via Terminal-Bench")
    print("=" * 70)
    print(f"  Model:      {args.model}")
    print(f"  Reflection: {args.reflection_model}")
    print(f"  Train:      {len(train_tasks)} tasks")
    print(f"  Val:        {len(val_tasks)} tasks")
    print(f"  Test:       {len(test_tasks)} tasks")
    print(f"  Budget:     {args.max_metric_calls} metric calls")
    print(f"  Concurrency: {args.concurrency}")
    print(f"  Run dir:    {run_dir}")
    if seed_skills:
        print(f"  Seed skills: {len(seed_skills)} chars")
    print("=" * 70 + "\n")

    # Run optimization
    result = optimize_anything(
        seed_candidate=seed_candidate,
        evaluator=fitness_fn,
        dataset=train_data,
        valset=val_data,
        config=gepa_config,
    )

    # Extract best candidate
    best = result.best_candidate
    if isinstance(best, dict):
        best_skills = best.get("skills", str(best))
    else:
        best_skills = best.candidate["skills"]

    best_idx = result.best_idx
    best_score = (
        result.val_aggregate_scores[best_idx]
        if result.val_aggregate_scores else 0.0
    )

    # Save best skills
    skills_file = run_dir / "best_skills.md"
    skills_file.write_text(best_skills)

    print("\n" + "=" * 70)
    print("Optimization Complete!")
    print(f"  Best Val Score: {best_score:.1%}")
    print(f"  Candidates:     {result.num_candidates}")
    print(f"  Metric Calls:   {result.total_metric_calls}")
    print(f"  Skills:         {len(best_skills)} chars")
    print(f"  Saved to:       {skills_file}")
    print("=" * 70)

    # Optional test evaluation
    if args.run_test_eval and test_data:
        print(f"\nEvaluating best candidate on {len(test_data)} test tasks...")
        test_scores = []
        for i, task in enumerate(test_data):
            score, info = fitness_fn({"skills": best_skills}, task)
            test_scores.append(score)
            status = "PASS" if score == 1.0 else "FAIL"
            print(f"  [{i+1}/{len(test_data)}] {task['task_name']}: {status}")

        test_pass_rate = sum(test_scores) / len(test_scores)
        passed = sum(1 for s in test_scores if s == 1.0)
        print(f"\nTest: {passed}/{len(test_data)} ({test_pass_rate:.1%})")

        with open(run_dir / "test_results.json", "w") as f:
            json.dump({
                "pass_rate": test_pass_rate,
                "passed": passed,
                "total": len(test_data),
                "scores": dict(zip(test_tasks, test_scores)),
            }, f, indent=2)

    print(f"\nAll results in: {run_dir}")


if __name__ == "__main__":
    main()
```

- [ ] **Step 4: Run tests**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -m pytest test_optimize_serf.py -v"
```

Expected: both tests pass.

- [ ] **Step 5: Commit**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add optimize_serf.py test_optimize_serf.py && git commit -m 'feat: GEPA optimization script for serf via terminal-bench'"
```

---

## Chunk 2: Execution and Integration

### Task 5: Smoke Test

- [ ] **Step 1: Rebuild serf if needed (with gpt-5.4 support)**

Check if the deployed serf binary supports gpt-5.4. If serf's model handling is pass-through to the API (likely), no rebuild needed. If there's a model allowlist, update it first.

```bash
ssh jesse@magic-kingdom "cd ~/git/terminal-bench && echo 'test' | timeout 5 ./serf-linux-amd64 --provider openai --model gpt-5.4 --max-rounds 1 -- 'echo hello' 2>&1 | head -20 || echo 'Need to check model support'"
```

If a rebuild is needed:
```bash
cd ~/prime-radiant/serf && GOOS=linux GOARCH=amd64 go build -o serf-linux-amd64 ./cmd/serf/
scp serf-linux-amd64 jesse@magic-kingdom:~/git/terminal-bench/serf-linux-amd64
```

- [ ] **Step 2: Run smoke test**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 optimize_serf.py --smoke-test 2>&1 | tee smoke_test.log"
```

Expected: completes in 30-60 min. Watch for:
- Harbor launching correctly
- Serf executing inside containers
- GEPA reflection loop running
- No unhandled exceptions

- [ ] **Step 3: Review output**

```bash
ssh jesse@magic-kingdom "grep -E '(PASS|FAIL|ERROR|Complete|Best Val|Metric Calls)' ~/git/serf-gepa/smoke_test.log"
```

- [ ] **Step 4: Fix issues and re-run until clean**

Common issues:
- Harbor can't find serf binary → check path in install template
- API key not in container → verify harbor's env includes OPENAI_API_KEY
- Skills file path wrong → check `system_prompt_append` kwarg handling
- Harbor output directory structure doesn't match parser → adjust `parse_harbor_result`

- [ ] **Step 5: Commit smoke test log**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add smoke_test.log && git commit -m 'test: smoke test passed'"
```

---

### Task 6: Run Full Optimization

- [ ] **Step 1: Start in tmux**

```bash
ssh jesse@magic-kingdom "tmux new-session -d -s gepa 'cd ~/git/serf-gepa && source .venv/bin/activate && python3 optimize_serf.py --run-test-eval 2>&1 | tee full_run.log'"
```

- [ ] **Step 2: Monitor**

```bash
# Is it running?
ssh jesse@magic-kingdom "tmux has-session -t gepa 2>/dev/null && echo 'Running' || echo 'Done'"

# Recent output
ssh jesse@magic-kingdom "tail -30 ~/git/serf-gepa/full_run.log"

# Docker containers
ssh jesse@magic-kingdom "docker ps --format 'table {{.Names}}\t{{.Status}}' | head -10"

# Cost check (grep litellm cost logs if available)
ssh jesse@magic-kingdom "du -sh /data/agent-evals/runs/gepa_* 2>/dev/null"
```

Expected runtime: several hours depending on budget and task count.

- [ ] **Step 3: Review results when complete**

```bash
# Summary
ssh jesse@magic-kingdom "grep -E '(Complete|Best Val|Test:|Metric Calls|Candidates)' ~/git/serf-gepa/full_run.log"

# Best skills content
ssh jesse@magic-kingdom "cat ~/git/serf-gepa/gepa_results/serf_tb_*/best_skills.md"

# Test results
ssh jesse@magic-kingdom "cat ~/git/serf-gepa/gepa_results/serf_tb_*/test_results.json 2>/dev/null | python3 -m json.tool"
```

- [ ] **Step 4: Commit results**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add gepa_results/ full_run.log && git commit -m 'data: full GEPA optimization results'"
```

---

### Task 7: Validate on Discriminator Tasks (OPTIONAL)

**This task is optional — only run if budget permits after the GEPA optimization.**

Use serf's existing eval infrastructure to run optimized vs baseline on all 56 discriminator tasks (10-75% failure rate). These are the tasks that actually differentiate agents — the easy stuff always passes and the impossible stuff never does, so neither provides signal.

Estimated additional cost: ~$400 (56 tasks × 2 runs × ~$3.50/task).

- [ ] **Step 1: Copy best skills to terminal-bench directory**

```bash
ssh jesse@magic-kingdom "cp ~/git/serf-gepa/gepa_results/serf_tb_*/best_skills.md ~/git/terminal-bench/gepa-skills.md"
```

- [ ] **Step 2: Launch optimized eval on discriminators**

From local Mac:

```bash
cd ~/prime-radiant/serf
python3 tools/run_eval.py launch \
    --task discriminators \
    --model openai/gpt-5.4 \
    --ak "system_prompt_append=/home/jesse/git/terminal-bench/gepa-skills.md" \
    --job gepa-optimized-v1
```

- [ ] **Step 3: Launch baseline (no skills) for comparison**

```bash
python3 tools/run_eval.py launch \
    --task discriminators \
    --model openai/gpt-5.4 \
    --job gepa-baseline-v1
```

- [ ] **Step 4: Monitor both runs**

```bash
python3 tools/run_eval.py status --job gepa-optimized-v1
python3 tools/run_eval.py status --job gepa-baseline-v1
```

- [ ] **Step 5: Collect and compare results**

```bash
python3 tools/run_eval.py collect --job gepa-optimized-v1
python3 tools/run_eval.py collect --job gepa-baseline-v1

# Compare pass rates
echo "=== Baseline ===" && cat results/gepa-baseline-v1/summary.json | python3 -c "import sys,json; d=json.load(sys.stdin); print(f'Pass: {d[\"passed\"]}/{d[\"total\"]} ({d[\"pass_rate\"]:.1%})')"
echo "=== Optimized ===" && cat results/gepa-optimized-v1/summary.json | python3 -c "import sys,json; d=json.load(sys.stdin); print(f'Pass: {d[\"passed\"]}/{d[\"total\"]} ({d[\"pass_rate\"]:.1%})')"
```

- [ ] **Step 6: Document results**

Record the comparison in `~/prime-radiant/serf/docs/experiments/gepa-terminal-bench-v1.md`:
- Baseline pass rate on discriminators (no skills, gpt-5.4)
- Optimized pass rate on discriminators (GEPA skills, gpt-5.4)
- Per-task delta (which tasks improved/regressed)
- Skills content review
- Cost summary

---

### Task 8: Integrate (If Results Are Positive)

- [ ] **Step 1: Copy skills to local machine**

```bash
scp jesse@magic-kingdom:~/git/serf-gepa/gepa_results/serf_tb_*/best_skills.md ~/prime-radiant/serf/docs/experiments/gepa-optimized-skills-v1.md
```

- [ ] **Step 2: Human review**

Jesse reads the skills and evaluates:
- Are they specific and actionable?
- Any hallucinated or incorrect advice?
- Conflict with existing core.md or system.openai.md?
- Repo-specific vs general advice?

- [ ] **Step 3: Choose integration path**

Options:
1. **`--system-prompt-append` per eval:** Keep as external file, inject when desired
2. **Embedded prompt file:** Add as `agent/prompts/skills.md`, always included
3. **Cherry-pick:** Extract best insights into core.md or system.openai.md
4. **Project-level:** Add to `.serf/prompts/` for specific projects

- [ ] **Step 4: Commit if integrating**

```bash
cd ~/prime-radiant/serf
git add <chosen-path>
git commit -m "feat: GEPA-optimized skills supplement from terminal-bench optimization"
```

---

## Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| gpt-5.4 pricing unknown → budget overrun | Check pricing in Task 2 before committing. Adjust MAX_METRIC_CALLS. Can fall back to gpt-5.3-codex (known pricing, established baseline). |
| Harbor per-task overhead too slow | Each harbor invocation has ~30s overhead. With 80 metric calls that's 40 min overhead — acceptable. If problematic, batch evaluations. |
| Skills overfit to discriminator tasks | Test split uses different discriminators than train/val. Optional Task 7 validates on ALL 56 discriminators. |
| GEPA reflection produces generic/unhelpful skills | Add `objective` and `background` params to guide reflection. e.g., objective="Discover general-purpose coding strategies that improve bug-fixing and system-building tasks." |
| Harbor result directory structure varies | Smoke test (Task 5) validates the parser. Adjust `parse_harbor_result` if needed. |
| 60GB RAM insufficient for 4 concurrent containers | Monitor with `free -h`. Reduce HARBOR_CONCURRENCY to 2 if needed. |

## Cost Summary (Estimated)

| Phase | Tasks | Runs | Est. Cost |
|-------|-------|------|-----------|
| Smoke test | 3 | ~10 | ~$35 |
| GEPA training | 30 | 80 | ~$280 |
| GEPA val | 10 | ~20 | ~$70 |
| Reflection LM | — | ~25 | ~$50 |
| Test eval | 16 | 16 | ~$56 |
| **Total** | | **~151** | **~$491** |

**Note:** Costs assume ~$3.50/task for gpt-5.4. Adjust after Task 2.

**Optional Task 7** (discriminator validation): ~$400 additional (56 tasks × 2 runs × ~$3.50/task for baseline + optimized comparison).
