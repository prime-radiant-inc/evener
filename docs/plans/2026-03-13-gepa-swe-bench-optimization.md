# GEPA-Optimized Serf System Prompt for SWE-Bench

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Use GEPA's `optimize_anything` to evolve a skills supplement for serf's system prompt, optimized against SWE-bench tasks, running on magic-kingdom.local.

**Architecture:** The optimization loop runs on magic-kingdom.local where Docker is available. GEPA's `optimize_anything` drives the outer loop: it proposes candidate "skills" text, an evaluator function runs serf inside SWE-smith Docker containers against SWE-bench tasks, and the F2P (fail-to-pass) score plus diagnostic traces feed back into GEPA's reflection LM. The candidate artifact is a skills supplement injected via `--system-prompt-append`, leaving serf's base prompt unchanged.

**Tech Stack:** Python 3.12, GEPA (`optimize_anything`), SWE-smith/swebench (Docker containers), serf (Go, cross-compiled for Linux), gpt-5.4 (agent + reflection)

**Key Machines:**
- `magic-kingdom.local` (jesse@magic-kingdom) — Ryzen 9, 16 cores, 60GB RAM, Docker, 227GB free disk
- Local Mac — development, plan writing, monitoring

**Budget:** ~$500 total. Cost is dominated by gpt-5.4 API calls for serf task execution. Reflection LM calls are comparatively cheap. See Task 4 for cost model and parameter tuning.

---

## Chunk 1: Infrastructure Setup (magic-kingdom)

### Task 1: Set Up Python Environment on magic-kingdom

**Files:**
- Create: `/home/jesse/git/serf-gepa/requirements.txt`
- Create: `/home/jesse/git/serf-gepa/setup.sh`

This task sets up a dedicated Python virtualenv on magic-kingdom with all dependencies: gepa, swesmith, swebench, litellm, docker SDK.

- [ ] **Step 1: SSH to magic-kingdom, create project directory and venv**

```bash
ssh jesse@magic-kingdom "mkdir -p ~/git/serf-gepa && python3 -m venv ~/git/serf-gepa/.venv"
```

Expected: directory and venv created without error.

- [ ] **Step 2: Create requirements.txt**

Write to magic-kingdom `/home/jesse/git/serf-gepa/requirements.txt`:

```
gepa[gskill]
swesmith
swebench
litellm
docker
python-dotenv
tqdm
```

- [ ] **Step 3: Install dependencies**

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && pip install -r ~/git/serf-gepa/requirements.txt"
```

Expected: all packages install successfully. If swesmith or swebench have issues, check their PyPI pages for correct package names and version constraints.

- [ ] **Step 4: Verify imports**

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -c \"
from gepa.optimize_anything import optimize_anything, GEPAConfig, EngineConfig
import swesmith
import swebench
import docker
print('All imports OK')
\""
```

Expected: `All imports OK`

- [ ] **Step 5: Ensure API keys are available**

Serf and GEPA both need `OPENAI_API_KEY` on magic-kingdom. Check:

```bash
ssh jesse@magic-kingdom "test -n \"\$OPENAI_API_KEY\" && echo 'OPENAI_API_KEY set' || echo 'MISSING'"
```

If missing, add to `~/.bashrc` or create a `.env` file at `~/git/serf-gepa/.env`. Do NOT commit API keys.

- [ ] **Step 6: Commit setup files**

```bash
# On magic-kingdom
cd ~/git/serf-gepa && git init && git add requirements.txt && git commit -m "chore: initial project setup with dependencies"
```

---

### Task 2: Build SWE-smith Docker Images

**Files:**
- No new files; uses swesmith CLI tools

SWE-smith provides pre-built Docker images with repositories checked out at specific bug-introducing commits. Each image contains the repo at `/testbed` with dependencies installed. We need to pick a target repo and build/download images.

**Repo selection:** For a first test, use `scikit-learn/scikit-learn` — it's well-represented in SWE-bench, has medium-complexity tasks, and Python-only (good for serf). Alternative: `django/django` if sklearn images are too large.

- [ ] **Step 1: Check available SWE-smith repos and disk space**

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -c \"
from datasets import load_dataset
ds = load_dataset('SWE-bench/SWE-smith', split='train')
repos = {}
for item in ds:
    repo = item.get('repo', '')
    repos[repo] = repos.get(repo, 0) + 1
for repo, count in sorted(repos.items(), key=lambda x: -x[1])[:20]:
    print(f'{count:4d}  {repo}')
\" && df -h /data 2>/dev/null || df -h /"
```

Expected: list of repos with task counts. Pick one with 80+ tasks.

**IMPORTANT:** If SWE-smith dataset is very large, this step may take a while. Consider using `streaming=True` to sample first.

- [ ] **Step 2: Download/build Docker images for target repo**

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -m swesmith.build_repo.download_images"
```

If images aren't available for download, build them:

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -m swesmith.build_repo.create_images --repos <REPO_NAME> --force -y"
```

Replace `<REPO_NAME>` with the chosen repo (e.g., `scikit-learn__scikit-learn`).

Expected: Docker images created. This may take 30-60 minutes and use 5-20GB disk.

- [ ] **Step 3: Verify a container launches**

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -c \"
from datasets import load_dataset
from swesmith.profiles import registry

ds = load_dataset('SWE-bench/SWE-smith', split='train')
# Get first task for our repo
for item in ds:
    if '<REPO_NAME>' in item.get('repo', ''):
        task = dict(item)
        break

profile = registry.get_from_inst(task)
container = profile.get_container(task)
print(f'Container ID: {container.id[:12]}')
# Verify repo is checked out
result = container.exec_run('ls /testbed', user='root')
print(result.output.decode()[:500])
container.stop()
container.remove()
print('Container lifecycle OK')
\""
```

Expected: container starts, `/testbed` contains the repository, cleanup succeeds.

- [ ] **Step 4: Commit notes on chosen repo**

Document which repo was chosen and why in `~/git/serf-gepa/README.md`.

---

### Task 3: Cross-Compile Serf and Test in Container

**Files:**
- Uses existing serf Makefile
- Create: `/home/jesse/git/serf-gepa/install-serf.sh` (container install script)

- [ ] **Step 1: Cross-compile serf for Linux on local Mac**

```bash
cd ~/prime-radiant/serf && GOOS=linux GOARCH=amd64 go build -ldflags "$(python3 tools/eval_lib.py --ldflags 2>/dev/null || echo '')" -o serf-linux-amd64 ./cmd/serf/
```

If ldflags helper doesn't work standalone, just build without:

```bash
cd ~/prime-radiant/serf && GOOS=linux GOARCH=amd64 go build -o serf-linux-amd64 ./cmd/serf/
```

Expected: `serf-linux-amd64` binary created.

- [ ] **Step 2: Copy binary to magic-kingdom**

```bash
scp ~/prime-radiant/serf/serf-linux-amd64 jesse@magic-kingdom:~/git/serf-gepa/serf-linux-amd64
```

- [ ] **Step 3: Write container install script**

Create `~/git/serf-gepa/install-serf.sh`:

```bash
#!/bin/bash
# Install serf binary in a SWE-smith container
# Usage: docker cp install-serf.sh <container>:/tmp/ && docker exec <container> bash /tmp/install-serf.sh

cp /tmp/serf-linux-amd64 /usr/local/bin/serf
chmod +x /usr/local/bin/serf
echo "serf installed at $(which serf)"
```

- [ ] **Step 4: Test serf runs inside a SWE-smith container**

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -c \"
import docker
from datasets import load_dataset
from swesmith.profiles import registry

ds = load_dataset('SWE-bench/SWE-smith', split='train')
for item in ds:
    if '<REPO_NAME>' in item.get('repo', ''):
        task = dict(item)
        break

profile = registry.get_from_inst(task)
container = profile.get_container(task)

client = docker.from_env()

# Copy serf binary into container
import subprocess
subprocess.run(['docker', 'cp', '/home/jesse/git/serf-gepa/serf-linux-amd64', f'{container.id}:/usr/local/bin/serf'], check=True)
container.exec_run('chmod +x /usr/local/bin/serf', user='root')

# Verify serf runs
result = container.exec_run('/usr/local/bin/serf --version', user='root')
print('serf version:', result.output.decode().strip())

container.stop()
container.remove()
print('Serf container test OK')
\""
```

Expected: serf reports its version inside the container.

- [ ] **Step 5: Commit install script**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add install-serf.sh && git commit -m 'feat: container install script for serf binary'"
```

---

## Chunk 2: Evaluator and Optimization Script

### Task 4: Cost Model and Parameter Selection

**Files:**
- Create: `~/git/serf-gepa/config.py` (centralized configuration)

Before writing code, nail down the budget parameters. This is a pencil-and-paper task.

- [ ] **Step 1: Estimate per-task cost**

Check gpt-5.4 pricing (look up current OpenAI pricing page or `litellm.model_cost`). Estimate tokens per SWE-bench task run:

- Typical serf SWE-bench run: ~150k-400k total tokens (input + output across all tool rounds)
- Estimate cost per task based on gpt-5.4 pricing
- Target: determine `COST_PER_TASK` in dollars

```bash
ssh jesse@magic-kingdom "source ~/git/serf-gepa/.venv/bin/activate && python3 -c \"
import litellm
# Check if gpt-5.4 pricing is known
costs = litellm.model_cost
for k, v in costs.items():
    if '5.4' in k or '5-4' in k:
        print(f'{k}: input=\${v.get(\"input_cost_per_token\", 0)*1e6:.2f}/M output=\${v.get(\"output_cost_per_token\", 0)*1e6:.2f}/M')
\""
```

- [ ] **Step 2: Calculate dataset sizes and metric budget**

Given $500 total budget and COST_PER_TASK from step 1:

```
TOTAL_BUDGET = 500
REFLECTION_BUDGET = 50  # ~10% for GEPA reflection LM calls
AGENT_BUDGET = TOTAL_BUDGET - REFLECTION_BUDGET = 450

MAX_AGENT_RUNS = AGENT_BUDGET / COST_PER_TASK

# Split: 60% train, 15% val, 25% test (test eval is separate budget)
TRAIN_METRIC_CALLS = int(MAX_AGENT_RUNS * 0.60)
VAL_RUNS = int(MAX_AGENT_RUNS * 0.15)
TEST_RUNS = int(MAX_AGENT_RUNS * 0.25)

# Dataset sizes — these determine task diversity
# GEPA evaluates each candidate on a minibatch of 3 tasks per reflection step
# More tasks = more diversity but fewer iterations
TRAIN_SIZE = min(30, TRAIN_METRIC_CALLS // 3)  # Want at least 3 iterations
VAL_SIZE = 10
TEST_SIZE = 15
```

Write these values to `~/git/serf-gepa/config.py`:

```python
"""Centralized configuration for GEPA serf optimization."""

# Budget
TOTAL_BUDGET_USD = 500
COST_PER_TASK_USD = 3.50  # UPDATE after Step 1

# Dataset
TARGET_REPO = "scikit-learn__scikit-learn"  # UPDATE after Task 2
TRAIN_SIZE = 25
VAL_SIZE = 10
TEST_SIZE = 15

# GEPA
MAX_METRIC_CALLS = 80  # Total evaluator invocations during optimization
REFLECTION_MINIBATCH_SIZE = 3
REFLECTION_MODEL = "gpt-5.4"
AGENT_MODEL = "gpt-5.4"
AGENT_PROVIDER = "openai"

# Infrastructure
N_WORKERS = 4  # Parallel Docker containers (conservative for 60GB RAM)
SERF_BINARY = "/home/jesse/git/serf-gepa/serf-linux-amd64"
SEED = 42
TIMEOUT_SECONDS = 43200  # 12 hours max
AGENT_TIMEOUT = 900  # 15 min per task (matches harbor default)
```

- [ ] **Step 3: Commit config**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add config.py && git commit -m 'feat: budget and parameter configuration'"
```

---

### Task 5: Write the Serf SWE-Bench Evaluator

**Files:**
- Create: `~/git/serf-gepa/serf_evaluator.py`

This is the core piece. The evaluator function takes a candidate (dict with "skills" key) and a SWE-bench task instance, runs serf in a Docker container, and returns (score, side_info).

**Design decisions:**
- Reuse SWE-smith's container management (`swesmith.profiles.registry`)
- Reuse SWE-smith's patch verification (`swesmith.harness.utils.run_patch_in_container`)
- Worker pool pattern from GEPA's `swe_fitness_fn.py` for parallel evaluation
- serf invoked via `docker exec` with `--system-prompt-append` for skills injection

- [ ] **Step 1: Write failing test for the evaluator**

Create `~/git/serf-gepa/test_serf_evaluator.py`:

```python
"""Tests for serf SWE-bench evaluator.

These tests verify the evaluator's contract without running real
Docker containers or LLM calls. Integration tests that hit real
infrastructure are in test_integration.py.
"""
import pytest
from unittest.mock import MagicMock, patch


def test_evaluator_returns_score_and_side_info():
    """Evaluator returns (float, dict) tuple."""
    from serf_evaluator import SerfSWEHarness

    # We can't run a real evaluation in unit tests, but we can verify
    # the harness class exists and has the right interface
    harness = SerfSWEHarness.__new__(SerfSWEHarness)
    assert hasattr(SerfSWEHarness, 'setup_task')
    assert hasattr(SerfSWEHarness, 'run_serf')
    assert hasattr(SerfSWEHarness, 'verify_patch')
    assert hasattr(SerfSWEHarness, 'cleanup')


def test_format_skills_file():
    """Skills content is written correctly for --system-prompt-append."""
    from serf_evaluator import format_skills_content

    skills = "## Debugging\n- Always read error messages first"
    result = format_skills_content(skills)
    assert "## Debugging" in result
    assert "Always read error messages" in result


def test_fitness_fn_signature():
    """Fitness function matches GEPA's expected signature."""
    from serf_evaluator import create_serf_fitness_fn

    # Verify factory function exists and is callable
    assert callable(create_serf_fitness_fn)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -m pytest test_serf_evaluator.py -v"
```

Expected: FAIL — `serf_evaluator` module not found.

- [ ] **Step 3: Write the evaluator**

Create `~/git/serf-gepa/serf_evaluator.py`:

```python
"""Serf SWE-bench evaluator for GEPA optimize_anything.

Runs serf inside SWE-smith Docker containers, verifies patches with
fail-to-pass tests, and returns GEPA-compatible (score, side_info) tuples.

Usage:
    from serf_evaluator import create_serf_fitness_fn

    fitness_fn = create_serf_fitness_fn(
        serf_binary="/path/to/serf-linux-amd64",
        model="gpt-5.4",
        n_workers=4,
    )

    # Called by optimize_anything per (candidate, example) pair:
    score, side_info = fitness_fn({"skills": "..."}, task_instance)
"""

import json
import logging
import os
import subprocess
import tempfile
import threading
import time
import uuid

import docker
from swesmith.profiles import registry

logger = logging.getLogger(__name__)


def format_skills_content(skills: str) -> str:
    """Format skills text for injection via --system-prompt-append.

    The skills are appended to serf's system prompt as-is.
    No special wrapping needed — serf's prompt resolver handles it.
    """
    return skills.strip() + "\n"


class SerfSWEHarness:
    """Manages a single serf evaluation lifecycle in a SWE-smith container.

    Each harness instance handles one task at a time:
    setup_task → run_serf → verify_patch → cleanup.
    """

    def __init__(self, serf_binary: str, model: str, provider: str = "openai",
                 agent_timeout: int = 900):
        self.serf_binary = serf_binary
        self.model = model
        self.provider = provider
        self.agent_timeout = agent_timeout
        self.docker_client = docker.from_env()
        self.container = None
        self.task = None

    def setup_task(self, task: dict):
        """Create a Docker container for this task."""
        self.task = task
        profile = registry.get_from_inst(task)
        self.container = profile.get_container(task)

        # Install serf binary
        container_id = self.container.id
        subprocess.run(
            ["docker", "cp", self.serf_binary,
             f"{container_id}:/usr/local/bin/serf"],
            check=True, capture_output=True,
        )
        self.container.exec_run("chmod +x /usr/local/bin/serf", user="root")

    def run_serf(self, skills: str) -> dict:
        """Run serf with skills in the container, return patch + trace.

        Returns dict with keys: patch, output, duration_seconds, success, error
        """
        if not self.container:
            raise RuntimeError("No container — call setup_task first")

        problem_statement = self.task.get("problem_statement", "")
        instance_id = self.task.get("instance_id", "unknown")

        # Write skills to temp file in container
        skills_content = format_skills_content(skills)
        skills_path = "/tmp/gepa_skills.md"

        # Use docker exec to write skills file
        self.container.exec_run(
            ["bash", "-c", f"cat > {skills_path} << 'GEPA_SKILLS_EOF'\n{skills_content}\nGEPA_SKILLS_EOF"],
            user="root",
        )

        # Build serf command
        serf_cmd = [
            "/usr/local/bin/serf",
            "--provider", self.provider,
            "--model", self.model,
            "--non-interactive",
            "--max-rounds", "50",
        ]
        if skills.strip():
            serf_cmd.extend(["--system-prompt-append", skills_path])

        # The task prompt
        serf_cmd.append("--")
        serf_cmd.append(problem_statement)

        # Set up environment with API keys
        env_vars = {}
        for key in ["OPENAI_API_KEY", "ANTHROPIC_API_KEY"]:
            val = os.environ.get(key)
            if val:
                env_vars[key] = val

        env_str = " ".join(f"{k}={v}" for k, v in env_vars.items())
        full_cmd = f"{env_str} {' '.join(serf_cmd)}"

        start_time = time.time()
        try:
            result = self.container.exec_run(
                ["bash", "-c", full_cmd],
                user="root",
                workdir="/testbed",
                timeout=self.agent_timeout,
            )
            duration = time.time() - start_time
            output = result.output.decode("utf-8", errors="replace")
            success = result.exit_code == 0

            # Extract git diff
            diff_result = self.container.exec_run(
                ["git", "diff"],
                workdir="/testbed",
                user="root",
            )
            patch = diff_result.output.decode("utf-8", errors="replace")

            return {
                "patch": patch,
                "output": output[:10000],  # Truncate for side_info
                "duration_seconds": duration,
                "success": success,
                "error": None if success else f"exit code {result.exit_code}",
            }

        except Exception as e:
            duration = time.time() - start_time
            return {
                "patch": "",
                "output": "",
                "duration_seconds": duration,
                "success": False,
                "error": str(e),
            }

    def verify_patch(self, patch: str) -> tuple[bool, str]:
        """Verify a patch passes fail-to-pass tests.

        Uses SWE-smith's run_patch_in_container for proper two-stage
        verification (F2P then P2P).
        """
        if not patch.strip():
            return False, "No patch generated"

        try:
            from swesmith.harness.utils import run_patch_in_container

            # run_patch_in_container creates a fresh container, applies
            # patch, checks out HEAD~1 (restores tests), runs tests
            passed, test_output = run_patch_in_container(
                self.task, patch, timeout=300
            )
            return passed, test_output

        except Exception as e:
            return False, f"Verification error: {e}"

    def cleanup(self):
        """Stop and remove the container."""
        if self.container:
            try:
                self.container.stop(timeout=5)
                self.container.remove()
            except Exception:
                pass
            self.container = None
            self.task = None


def create_serf_fitness_fn(
    serf_binary: str,
    model: str = "gpt-5.4",
    provider: str = "openai",
    n_workers: int = 4,
    agent_timeout: int = 900,
):
    """Create a GEPA-compatible fitness function with a worker pool.

    Returns a function with signature:
        (candidate: dict, example: dict) -> (float, dict)

    The function is thread-safe and manages a pool of SerfSWEHarness
    instances for parallel evaluation.
    """
    # Worker pool
    harness_pool = [
        SerfSWEHarness(serf_binary, model, provider, agent_timeout)
        for _ in range(n_workers)
    ]
    harness_available = [True] * n_workers
    harness_lock = threading.Lock()

    def get_harness() -> tuple[int, SerfSWEHarness]:
        """Get an available harness, blocking until one is free."""
        while True:
            with harness_lock:
                for i, available in enumerate(harness_available):
                    if available:
                        harness_available[i] = False
                        return i, harness_pool[i]
            time.sleep(0.5)

    def release_harness(idx: int):
        with harness_lock:
            harness_available[idx] = True

    def fitness_fn(candidate: dict, example: dict) -> tuple[float, dict]:
        """Evaluate a candidate's skills on a single SWE-bench task.

        Args:
            candidate: dict with "skills" key containing the skills text
            example: SWE-bench task instance dict

        Returns:
            (score, side_info) where score is 0.0 or 1.0
        """
        import gepa.optimize_anything as oa

        skills = candidate.get("skills", "")
        instance_id = example.get("instance_id", "unknown")

        idx, harness = get_harness()
        try:
            # Setup
            harness.setup_task(example)

            # Run serf
            run_result = harness.run_serf(skills)

            # Verify patch
            passed = False
            test_output = ""
            if run_result["patch"].strip():
                passed, test_output = harness.verify_patch(run_result["patch"])
            else:
                test_output = "No patch generated"

            score = 1.0 if passed else 0.0

            # Log diagnostics as Actionable Side Information
            status = "passed" if passed else "failed"
            if not run_result["patch"].strip():
                status = "no_patch"
            elif run_result.get("error"):
                status = f"error: {run_result['error']}"

            oa.log(f"[{instance_id}] {status} ({run_result['duration_seconds']:.0f}s)")

            side_info = {
                "Input": {
                    "instance_id": instance_id,
                    "problem_statement": example.get("problem_statement", "")[:500],
                },
                "Generated Outputs": {
                    "patch": run_result["patch"][:3000],
                    "agent_trace": run_result["output"][:5000],
                },
                "Feedback": {
                    "status": status,
                    "test_output": test_output[:3000],
                    "duration_seconds": run_result["duration_seconds"],
                },
                "scores": {"correctness": score},
            }

            return score, side_info

        except Exception as e:
            logger.error(f"Evaluation failed for {instance_id}: {e}")
            oa.log(f"[{instance_id}] HARNESS ERROR: {e}")
            return 0.0, {
                "Input": {"instance_id": instance_id},
                "Feedback": {"status": f"harness_error: {e}"},
                "scores": {"correctness": 0.0},
            }

        finally:
            harness.cleanup()
            release_harness(idx)

    return fitness_fn
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -m pytest test_serf_evaluator.py -v"
```

Expected: all 3 tests pass.

- [ ] **Step 5: Commit evaluator**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add serf_evaluator.py test_serf_evaluator.py && git commit -m 'feat: serf SWE-bench evaluator with worker pool'"
```

---

### Task 6: Write the Optimization Script

**Files:**
- Create: `~/git/serf-gepa/optimize_serf.py`

This is the main script that ties everything together. It loads SWE-bench data, creates the fitness function, configures GEPA, and runs the optimization.

Modeled on GEPA's `gskill/train_optimize_anything.py` but simplified for serf.

- [ ] **Step 1: Write failing test**

Create `~/git/serf-gepa/test_optimize_serf.py`:

```python
"""Tests for optimization script structure."""

def test_load_and_split_data_function_exists():
    from optimize_serf import load_and_split_data
    assert callable(load_and_split_data)

def test_main_function_exists():
    from optimize_serf import main
    assert callable(main)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -m pytest test_optimize_serf.py -v"
```

Expected: FAIL — `optimize_serf` not found.

- [ ] **Step 3: Write the optimization script**

Create `~/git/serf-gepa/optimize_serf.py`:

```python
"""GEPA optimization of serf's system prompt skills for SWE-bench.

Runs on magic-kingdom.local. Uses optimize_anything to evolve a skills
supplement that improves serf's bug-fixing performance.

Usage:
    # Smoke test (3 train, 2 val, few iterations)
    python optimize_serf.py --smoke-test

    # Full run
    python optimize_serf.py --repo scikit-learn__scikit-learn

    # Resume from checkpoint
    python optimize_serf.py --resume gepa_results/logs/run_xxx
"""

import argparse
import json
import logging
import os
import random
import sys
from pathlib import Path

from dotenv import load_dotenv

# Suppress verbose logging
logging.getLogger("LiteLLM").setLevel(logging.WARNING)
logging.getLogger("litellm").setLevel(logging.WARNING)

load_dotenv()

from datasets import load_dataset

from gepa.optimize_anything import (
    EngineConfig,
    GEPAConfig,
    ReflectionConfig,
    TrackingConfig,
    optimize_anything,
)
from gepa.utils.stop_condition import TimeoutStopCondition

from config import (
    AGENT_MODEL,
    AGENT_PROVIDER,
    AGENT_TIMEOUT,
    MAX_METRIC_CALLS,
    N_WORKERS,
    REFLECTION_MINIBATCH_SIZE,
    REFLECTION_MODEL,
    SEED,
    SERF_BINARY,
    TARGET_REPO,
    TEST_SIZE,
    TIMEOUT_SECONDS,
    TRAIN_SIZE,
    VAL_SIZE,
)
from serf_evaluator import create_serf_fitness_fn


def load_and_split_data(
    repo: str, train_size: int, val_size: int, test_size: int, seed: int = 42
) -> tuple[list, list, list]:
    """Load SWE-smith data for a repo and split into train/val/test."""
    total_needed = train_size + val_size + test_size
    print(f"Loading {repo} data from HuggingFace SWE-smith (need {total_needed})...")

    ds = load_dataset("SWE-bench/SWE-smith", split="train")

    repo_pattern = repo if "/" in repo else f"swesmith/{repo}"
    all_data = []
    for item in ds:
        if repo_pattern in item.get("repo", ""):
            all_data.append(dict(item))
            if len(all_data) >= total_needed * 2:  # Grab extra for safety
                break

    print(f"Found {len(all_data)} {repo} examples")

    random.seed(seed)
    random.shuffle(all_data)

    if len(all_data) < total_needed:
        print(f"WARNING: Only {len(all_data)} examples, need {total_needed}")
        ratio = len(all_data) / total_needed
        train_size = int(train_size * ratio)
        val_size = int(val_size * ratio)
        test_size = len(all_data) - train_size - val_size

    train = all_data[:train_size]
    val = all_data[train_size : train_size + val_size]
    test = all_data[train_size + val_size : train_size + val_size + test_size]

    print(f"Split: {len(train)} train, {len(val)} val, {len(test)} test")
    return train, val, test


def main():
    parser = argparse.ArgumentParser(
        description="GEPA optimization of serf system prompt for SWE-bench"
    )
    parser.add_argument("--repo", type=str, default=TARGET_REPO)
    parser.add_argument("--train-size", type=int, default=TRAIN_SIZE)
    parser.add_argument("--val-size", type=int, default=VAL_SIZE)
    parser.add_argument("--test-size", type=int, default=TEST_SIZE)
    parser.add_argument("--model", type=str, default=AGENT_MODEL)
    parser.add_argument("--reflection-model", type=str, default=REFLECTION_MODEL)
    parser.add_argument("--max-metric-calls", type=int, default=MAX_METRIC_CALLS)
    parser.add_argument("--workers", type=int, default=N_WORKERS)
    parser.add_argument("--smoke-test", action="store_true")
    parser.add_argument("--resume", type=str, default=None)
    parser.add_argument("--seed", type=int, default=SEED)
    parser.add_argument("--timeout", type=int, default=TIMEOUT_SECONDS)
    parser.add_argument("--run-test-eval", action="store_true",
                        help="Evaluate best candidate on test set after optimization")
    args = parser.parse_args()

    if args.smoke_test:
        args.train_size = 3
        args.val_size = 2
        args.test_size = 2
        args.max_metric_calls = 10
        args.workers = 2
        print("=== SMOKE TEST MODE ===")

    # Setup output directory
    run_dir = Path(f"gepa_results/serf_{args.repo}_{args.seed}")
    run_dir.mkdir(parents=True, exist_ok=True)

    # Handle resume
    if args.resume:
        resume_dir = Path(args.resume)
        state_file = resume_dir / "gepa_state.bin"
        if not state_file.exists():
            print(f"ERROR: No gepa_state.bin in {resume_dir}")
            sys.exit(1)
        import shutil
        shutil.copy2(state_file, run_dir / "gepa_state.bin")
        print(f"Resuming from {resume_dir}")

    # Load data
    train_data, val_data, test_data = load_and_split_data(
        args.repo, args.train_size, args.val_size, args.test_size, args.seed
    )

    # Save config
    config_dict = {
        "repo": args.repo,
        "model": args.model,
        "reflection_model": args.reflection_model,
        "train_size": len(train_data),
        "val_size": len(val_data),
        "test_size": len(test_data),
        "max_metric_calls": args.max_metric_calls,
        "workers": args.workers,
        "seed": args.seed,
    }
    with open(run_dir / "config.json", "w") as f:
        json.dump(config_dict, f, indent=2)

    # Create fitness function
    print(f"\nCreating evaluator with {args.workers} workers...")
    fitness_fn = create_serf_fitness_fn(
        serf_binary=SERF_BINARY,
        model=args.model,
        provider=AGENT_PROVIDER,
        n_workers=args.workers,
        agent_timeout=AGENT_TIMEOUT,
    )

    # Initial candidate: empty skills (baseline)
    seed_candidate = {"skills": ""}

    # GEPA config
    gepa_config = GEPAConfig(
        engine=EngineConfig(
            run_dir=str(run_dir),
            seed=args.seed,
            display_progress_bar=True,
            max_metric_calls=args.max_metric_calls,
            candidate_selection_strategy="pareto",
            parallel=True,
            max_workers=args.workers,
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

    # Run optimization
    print("\n" + "=" * 70)
    print("Starting GEPA Optimization of Serf System Prompt")
    print("=" * 70)
    print(f"  Repo: {args.repo}")
    print(f"  Model: {args.model}")
    print(f"  Reflection: {args.reflection_model}")
    print(f"  Train: {len(train_data)}, Val: {len(val_data)}, Test: {len(test_data)}")
    print(f"  Budget: {args.max_metric_calls} metric calls")
    print(f"  Workers: {args.workers}")
    print("=" * 70 + "\n")

    result = optimize_anything(
        seed_candidate=seed_candidate,
        evaluator=fitness_fn,
        dataset=train_data,
        valset=val_data,
        config=gepa_config,
    )

    # Extract results
    best = result.best_candidate
    if isinstance(best, dict):
        best_skills = best.get("skills", str(best))
    else:
        best_skills = best.candidate["skills"]

    best_idx = result.best_idx
    best_score = (result.val_aggregate_scores[best_idx]
                  if result.val_aggregate_scores else 0.0)

    # Save best skills
    skills_file = run_dir / "best_skills.md"
    with open(skills_file, "w") as f:
        f.write(best_skills)

    print("\n" + "=" * 70)
    print(f"Optimization Complete!")
    print(f"  Best Val Score: {best_score:.1%}")
    print(f"  Candidates: {result.num_candidates}")
    print(f"  Metric Calls: {result.total_metric_calls}")
    print(f"  Skills: {len(best_skills)} chars")
    print(f"  Saved to: {skills_file}")
    print("=" * 70)

    # Optional test evaluation
    if args.run_test_eval:
        print("\nEvaluating best candidate on test set...")
        test_scores = []
        for i, task in enumerate(test_data):
            score, info = fitness_fn({"skills": best_skills}, task)
            test_scores.append(score)
            status = "PASS" if score == 1.0 else "FAIL"
            iid = task.get("instance_id", "?")[:50]
            print(f"  [{i+1}/{len(test_data)}] {iid}: {status}")

        test_pass_rate = sum(test_scores) / len(test_scores)
        print(f"\nTest Results: {sum(s == 1.0 for s in test_scores)}/{len(test_data)} ({test_pass_rate:.1%})")

        with open(run_dir / "test_results.json", "w") as f:
            json.dump({"pass_rate": test_pass_rate, "scores": test_scores}, f, indent=2)


if __name__ == "__main__":
    main()
```

- [ ] **Step 4: Run test to verify it passes**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -m pytest test_optimize_serf.py -v"
```

Expected: both tests pass.

- [ ] **Step 5: Commit optimization script**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add optimize_serf.py test_optimize_serf.py && git commit -m 'feat: GEPA optimization script for serf SWE-bench'"
```

---

## Chunk 3: Execution

### Task 7: Run Smoke Test

**Files:**
- No new files; runs existing scripts

Smoke test validates the full pipeline end-to-end with minimal budget (3 train tasks, 2 val, 10 metric calls). This catches Docker issues, API connectivity, and integration bugs before spending real money.

- [ ] **Step 1: Run smoke test**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 optimize_serf.py --smoke-test 2>&1 | tee smoke_test.log"
```

Expected: completes without errors. May take 30-60 minutes (3 tasks × ~10 min each for initial eval, plus a few reflection iterations).

Watch for:
- Docker container creation failures
- API key issues
- SWE-smith image not found errors
- serf binary execution errors inside container
- Timeout issues

- [ ] **Step 2: Review smoke test output**

Check `smoke_test.log` for:
- At least one successful serf execution
- At least one patch generated
- GEPA reflection loop ran at least once
- No unhandled exceptions

```bash
ssh jesse@magic-kingdom "cat ~/git/serf-gepa/smoke_test.log | grep -E '(PASS|FAIL|ERROR|Optimization Complete|Best Val)'"
```

- [ ] **Step 3: Fix any issues found**

If the smoke test reveals problems:
- Docker issues → check `docker ps`, image names, disk space
- API issues → verify OPENAI_API_KEY is set in the container environment
- serf issues → test serf binary manually inside a container
- GEPA issues → check import paths, config values

Iterate until smoke test passes cleanly.

- [ ] **Step 4: Commit smoke test log (if clean)**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add smoke_test.log && git commit -m 'test: smoke test passed'"
```

---

### Task 8: Run Baseline Evaluation

**Files:**
- Creates: `~/git/serf-gepa/gepa_results/baseline/`

Before optimization, establish serf's baseline performance on the val and test sets with no skills supplement.

- [ ] **Step 1: Run baseline on val set**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && source .venv/bin/activate && python3 -c \"
from serf_evaluator import create_serf_fitness_fn
from optimize_serf import load_and_split_data
from config import *
import json
from pathlib import Path

train, val, test = load_and_split_data(TARGET_REPO, TRAIN_SIZE, VAL_SIZE, TEST_SIZE, SEED)

fitness_fn = create_serf_fitness_fn(SERF_BINARY, AGENT_MODEL, AGENT_PROVIDER, N_WORKERS, AGENT_TIMEOUT)

results = []
for i, task in enumerate(val):
    score, info = fitness_fn({'skills': ''}, task)
    results.append(score)
    iid = task.get('instance_id', '?')[:50]
    print(f'  [{i+1}/{len(val)}] {iid}: {\"PASS\" if score == 1.0 else \"FAIL\"}')

pass_rate = sum(results) / len(results)
print(f'Baseline val: {sum(s == 1.0 for s in results)}/{len(val)} ({pass_rate:.1%})')

out_dir = Path('gepa_results/baseline')
out_dir.mkdir(parents=True, exist_ok=True)
with open(out_dir / 'val_results.json', 'w') as f:
    json.dump({'pass_rate': pass_rate, 'scores': results}, f, indent=2)
\" 2>&1 | tee baseline.log"
```

Expected: baseline pass rate (likely 20-60% depending on repo and model).

- [ ] **Step 2: Record baseline**

Note the baseline pass rate — this is what GEPA needs to beat.

```bash
ssh jesse@magic-kingdom "cat ~/git/serf-gepa/gepa_results/baseline/val_results.json"
```

- [ ] **Step 3: Commit baseline**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add gepa_results/baseline/ baseline.log && git commit -m 'data: baseline serf performance on SWE-bench val set'"
```

---

### Task 9: Run Full GEPA Optimization

**Files:**
- Creates: `~/git/serf-gepa/gepa_results/serf_<repo>_<seed>/`

This is the main event. Run in a tmux session so it survives SSH disconnection.

- [ ] **Step 1: Start optimization in tmux**

```bash
ssh jesse@magic-kingdom "tmux new-session -d -s gepa 'cd ~/git/serf-gepa && source .venv/bin/activate && python3 optimize_serf.py --run-test-eval 2>&1 | tee optimization.log'"
```

- [ ] **Step 2: Monitor progress**

```bash
# Check if still running
ssh jesse@magic-kingdom "tmux has-session -t gepa 2>/dev/null && echo 'Running' || echo 'Done'"

# Tail the log
ssh jesse@magic-kingdom "tail -50 ~/git/serf-gepa/optimization.log"

# Check Docker containers
ssh jesse@magic-kingdom "docker ps --format 'table {{.Names}}\t{{.Status}}' | head -10"
```

Expected: runs for several hours (depends on budget and task count). Monitor periodically.

- [ ] **Step 3: Review results**

After completion:

```bash
ssh jesse@magic-kingdom "cat ~/git/serf-gepa/optimization.log | grep -E '(Optimization Complete|Best Val|Test Results|Metric Calls)'"
ssh jesse@magic-kingdom "cat ~/git/serf-gepa/gepa_results/serf_*/best_skills.md"
ssh jesse@magic-kingdom "cat ~/git/serf-gepa/gepa_results/serf_*/test_results.json 2>/dev/null"
```

Key metrics:
- Best val score vs baseline
- Test score (if --run-test-eval was used)
- Skills content (review for quality and specificity)

- [ ] **Step 4: Commit results**

```bash
ssh jesse@magic-kingdom "cd ~/git/serf-gepa && git add gepa_results/ optimization.log && git commit -m 'data: GEPA optimization results'"
```

---

### Task 10: Integrate Best Skills into Serf

**Files:**
- Modify: `~/prime-radiant/serf/agent/prompts/` (new skills file)
- Or: `~/prime-radiant/serf/.serf/prompts/` (project-level override)

This is where we take the best skills from GEPA and make them usable.

- [ ] **Step 1: Copy best skills from magic-kingdom**

```bash
scp jesse@magic-kingdom:~/git/serf-gepa/gepa_results/serf_*/best_skills.md ~/prime-radiant/serf/docs/experiments/gepa-optimized-skills.md
```

- [ ] **Step 2: Review the skills content**

Read the generated skills. Evaluate:
- Are they specific and actionable?
- Do they conflict with existing core.md or system.openai.md guidance?
- Are they repo-specific (bad for generalization) or general (good)?
- Any hallucinated or incorrect advice?

This is a human review step — Jesse should read and evaluate the skills before integrating.

- [ ] **Step 3: Decide on integration path**

Options:
1. **Append to system prompt:** Add as a new embedded prompt file (`agent/prompts/skills.md`)
2. **Project-level override:** Add to `.serf/prompts/system.md` for specific projects
3. **CLI injection:** Use `--system-prompt-append` per invocation
4. **Cherry-pick:** Extract the best individual insights and weave into core.md

Jesse decides which path based on the skills quality and specificity.

- [ ] **Step 4: Run terminal-bench comparison**

Use serf's existing eval infrastructure to compare baseline vs optimized on terminal-bench discriminators:

```bash
cd ~/prime-radiant/serf
# Build with new skills integrated
GOOS=linux GOARCH=amd64 go build -o serf-linux-amd64 ./cmd/serf/
python3 tools/run_eval.py launch --task discriminators --model openai/gpt-5.4
```

This validates that SWE-bench-optimized skills also help on terminal-bench (different benchmark = generalization test).

- [ ] **Step 5: Commit integration (if skills are good)**

```bash
cd ~/prime-radiant/serf
git add <skills-file-path>
git commit -m "feat: GEPA-optimized skills supplement for SWE-bench"
```

---

## Risks and Mitigations

| Risk | Mitigation |
|------|-----------|
| gpt-5.4 pricing unknown → budget overrun | Check pricing in Task 4 Step 1 before committing. Adjust MAX_METRIC_CALLS. |
| SWE-smith Docker images too large for 227GB free | Pick a single medium-sized repo. Monitor `df -h` during build. |
| serf fails to run inside SWE-smith containers | Smoke test (Task 7) catches this early. Debug container env. |
| Optimized skills overfit to one repo | Use diverse tasks or run on SWE-bench Verified (multi-repo). Compare on terminal-bench. |
| 60GB RAM insufficient for parallel containers | Start with 4 workers, monitor `free -h`, reduce if needed. |
| Skills are repo-specific garbage | Human review in Task 10 Step 2. Can also add `background` param to GEPA with "skills must be repo-agnostic". |

## Alternative: Terminal-Bench Instead of SWE-Bench

If SWE-smith Docker setup proves too heavy, consider adapting this plan to use terminal-bench instead. The evaluator would SSH to magic-kingdom and use harbor directly. Advantages: infrastructure already set up, 56 discriminator tasks with known difficulty, serf baseline is 65.2%. The GEPA evaluator would wrap harbor task execution instead of SWE-smith containers.
