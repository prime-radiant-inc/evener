# Benchmark Infrastructure Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace ad-hoc eval tooling with rigorous benchmark infrastructure on magic-kingdom.

**Architecture:** Shell wrapper (`run-eval.sh`) orchestrates: build, snapshot, deploy,
launch harbor in tmux, poll progress, collect/normalize to structured archive, generate
summary and HTML report. Python tools handle statistics and reporting. The adapter
(`serf_agent.py`) extracts filtered artifacts from containers.

**Tech Stack:** Bash (orchestration), Python 3.11+ (stats/reports), Go (serf build),
harbor (eval framework), tmux (process management), SSH/rsync (remote ops).

**Design doc:** `docs/plans/2026-02-28-benchmark-infrastructure-design.md`

---

### Task 1: Statistical library (`tools/eval_stats.py`)

The foundation — Wilson CIs, bootstrap CIs, McNemar's test. Everything else depends on
these being correct, so we start here with proper TDD.

**Files:**
- Create: `tools/eval_stats.py`
- Create: `tools/test_eval_stats.py`

**Step 1: Write failing test for Wilson score interval**

```python
# tools/test_eval_stats.py
import pytest
from eval_stats import wilson_ci

def test_wilson_ci_basic():
    """52 passes out of 89 trials → CI should contain the point estimate."""
    low, high = wilson_ci(52, 89, confidence=0.95)
    point = 52 / 89
    assert low < point < high
    assert 0.47 < low < 0.50   # ~0.478
    assert 0.67 < high < 0.70  # ~0.684

def test_wilson_ci_all_pass():
    """5/5 → upper bound should be 1.0 or very close."""
    low, high = wilson_ci(5, 5)
    assert high <= 1.0
    assert low > 0.5

def test_wilson_ci_all_fail():
    """0/5 → lower bound should be 0.0 or very close."""
    low, high = wilson_ci(0, 5)
    assert low >= 0.0
    assert high < 0.5

def test_wilson_ci_zero_trials():
    """0 trials → should return (0, 1) or raise."""
    low, high = wilson_ci(0, 0)
    assert low == 0.0
    assert high == 1.0
```

**Step 2: Run test to verify it fails**

Run: `cd tools && python -m pytest test_eval_stats.py::test_wilson_ci_basic -v`
Expected: FAIL with `ModuleNotFoundError: No module named 'eval_stats'`

**Step 3: Implement Wilson CI**

```python
# tools/eval_stats.py
"""Statistical functions for benchmark evaluation."""

import math
from typing import Optional


def wilson_ci(successes: int, trials: int, confidence: float = 0.95) -> tuple[float, float]:
    """Wilson score interval for a binomial proportion.

    Better than normal approximation near 0 or 1.
    Returns (lower, upper) bounds.
    """
    if trials == 0:
        return (0.0, 1.0)

    z = _z_score(confidence)
    p = successes / trials
    n = trials

    denom = 1 + z * z / n
    center = (p + z * z / (2 * n)) / denom
    spread = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / denom

    return (max(0.0, center - spread), min(1.0, center + spread))


def _z_score(confidence: float) -> float:
    """Z-score for a two-tailed confidence interval. Supports 0.90, 0.95, 0.99."""
    table = {0.90: 1.645, 0.95: 1.96, 0.99: 2.576}
    if confidence in table:
        return table[confidence]
    raise ValueError(f"Unsupported confidence level: {confidence}. Use 0.90, 0.95, or 0.99.")
```

**Step 4: Run test to verify it passes**

Run: `cd tools && python -m pytest test_eval_stats.py -k wilson -v`
Expected: 4 PASS

**Step 5: Write failing test for bootstrap CI on pass-rate difference**

```python
# tools/test_eval_stats.py (append)
import random

def test_bootstrap_ci_identical_runs():
    """Two identical runs → CI should contain 0."""
    random.seed(42)
    rates_a = [1.0, 0.0, 1.0, 0.67, 0.33, 1.0, 0.0, 0.0, 1.0, 1.0]
    rates_b = list(rates_a)  # identical
    low, high = bootstrap_ci_pass_rate_diff(rates_a, rates_b, n_bootstrap=5000, seed=42)
    assert low <= 0.0 <= high

def test_bootstrap_ci_clear_improvement():
    """Run B clearly better → CI should be above 0 (positive diff = B better)."""
    random.seed(42)
    rates_a = [0.0] * 20 + [1.0] * 10  # 10/30
    rates_b = [1.0] * 20 + [0.0] * 10  # 20/30
    low, high = bootstrap_ci_pass_rate_diff(rates_a, rates_b, n_bootstrap=5000, seed=42)
    assert low > 0  # B is definitively better

def test_bootstrap_ci_returns_sorted():
    """Lower bound should always be <= upper bound."""
    rates_a = [0.5, 0.5, 0.5]
    rates_b = [0.5, 0.5, 0.5]
    low, high = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=42)
    assert low <= high
```

**Step 6: Run test to verify it fails**

Run: `cd tools && python -m pytest test_eval_stats.py -k bootstrap -v`
Expected: FAIL with `ImportError`

**Step 7: Implement bootstrap CI**

```python
# tools/eval_stats.py (append)
import random as _random_module


def bootstrap_ci_pass_rate_diff(
    task_rates_a: list[float],
    task_rates_b: list[float],
    n_bootstrap: int = 10000,
    confidence: float = 0.95,
    seed: Optional[int] = None,
) -> tuple[float, float]:
    """Bootstrap CI on the mean pass-rate difference (B - A) across tasks.

    Resamples at the task level (not rep level) to respect task-level correlation.
    Positive values mean B is better than A.

    Args:
        task_rates_a: Per-task pass rate for run A (e.g., [1.0, 0.0, 0.67, ...])
        task_rates_b: Per-task pass rate for run B (same length)
        n_bootstrap: Number of bootstrap iterations
        confidence: Confidence level (default 0.95)
        seed: Random seed for reproducibility

    Returns: (lower, upper) bounds of the CI on (mean_B - mean_A)
    """
    assert len(task_rates_a) == len(task_rates_b), "Runs must have the same tasks"
    n = len(task_rates_a)
    if n == 0:
        return (0.0, 0.0)

    rng = _random_module.Random(seed)
    diffs = []
    for _ in range(n_bootstrap):
        indices = [rng.randrange(n) for _ in range(n)]
        mean_a = sum(task_rates_a[i] for i in indices) / n
        mean_b = sum(task_rates_b[i] for i in indices) / n
        diffs.append(mean_b - mean_a)

    diffs.sort()
    alpha = 1 - confidence
    lo_idx = int(n_bootstrap * alpha / 2)
    hi_idx = int(n_bootstrap * (1 - alpha / 2)) - 1
    return (diffs[lo_idx], diffs[hi_idx])
```

**Step 8: Run test to verify it passes**

Run: `cd tools && python -m pytest test_eval_stats.py -k bootstrap -v`
Expected: 3 PASS

**Step 9: Write failing test for McNemar's test**

```python
# tools/test_eval_stats.py (append)
from eval_stats import mcnemars_test

def test_mcnemars_no_discordant():
    """All agree → p-value should be 1.0 (no evidence of difference)."""
    pass_a = [True, True, False, False]
    pass_b = [True, True, False, False]
    stat, p = mcnemars_test(pass_a, pass_b)
    assert p == 1.0

def test_mcnemars_clear_difference():
    """10 tasks improved, 0 regressed → p should be very small."""
    pass_a = [False] * 10 + [True] * 5
    pass_b = [True] * 10 + [True] * 5
    stat, p = mcnemars_test(pass_a, pass_b)
    assert p < 0.01

def test_mcnemars_symmetric():
    """Swapping A and B should give the same p-value."""
    pass_a = [True, True, True, False, False, False, False, False]
    pass_b = [True, False, False, True, True, True, True, False]
    _, p1 = mcnemars_test(pass_a, pass_b)
    _, p2 = mcnemars_test(pass_b, pass_a)
    assert abs(p1 - p2) < 1e-10
```

**Step 10: Implement McNemar's test**

```python
# tools/eval_stats.py (append)

def mcnemars_test(pass_a: list[bool], pass_b: list[bool]) -> tuple[float, float]:
    """McNemar's test for paired binary outcomes.

    Returns (chi_squared, p_value). Only uses discordant pairs (where A and B disagree).
    """
    assert len(pass_a) == len(pass_b)

    # Count discordant pairs
    b = sum(1 for a, bb in zip(pass_a, pass_b) if a and not bb)      # A pass, B fail
    c = sum(1 for a, bb in zip(pass_a, pass_b) if not a and bb)      # A fail, B pass

    if b + c == 0:
        return (0.0, 1.0)

    chi2 = (b - c) ** 2 / (b + c)

    # Chi-squared with 1 df → p-value (survival function)
    p = _chi2_sf(chi2, df=1)
    return (chi2, p)


def _chi2_sf(x: float, df: int = 1) -> float:
    """Survival function (1 - CDF) for chi-squared distribution.

    Uses the regularized incomplete gamma function. For df=1, this is
    1 - erf(sqrt(x/2)).
    """
    if df != 1:
        raise ValueError("Only df=1 is supported")
    return math.erfc(math.sqrt(x / 2))
```

**Step 11: Run all stats tests**

Run: `cd tools && python -m pytest test_eval_stats.py -v`
Expected: All PASS (10 tests)

**Step 12: Write failing test for summary aggregation helpers**

```python
# tools/test_eval_stats.py (append)
from eval_stats import aggregate_task_results

def test_aggregate_majority():
    """2 of 3 reps pass → majority pass."""
    result = aggregate_task_results("test-task", [1.0, 0.0, 1.0])
    assert result["pass_majority"] is True
    assert result["pass_strict"] is False
    assert result["pass_any"] is True

def test_aggregate_all_fail():
    result = aggregate_task_results("test-task", [0.0, 0.0, 0.0])
    assert result["pass_majority"] is False
    assert result["pass_strict"] is False
    assert result["pass_any"] is False

def test_aggregate_all_pass():
    result = aggregate_task_results("test-task", [1.0, 1.0, 1.0])
    assert result["pass_majority"] is True
    assert result["pass_strict"] is True
    assert result["pass_any"] is True

def test_aggregate_single_rep():
    result = aggregate_task_results("test-task", [1.0])
    assert result["pass_majority"] is True
    assert result["pass_strict"] is True
```

**Step 13: Implement aggregate_task_results**

```python
# tools/eval_stats.py (append)

def aggregate_task_results(task_name: str, rewards: list[float]) -> dict:
    """Compute strict/majority/any pass for a task from its per-rep rewards."""
    passes = sum(1 for r in rewards if r >= 1.0)
    total = len(rewards)
    return {
        "name": task_name,
        "pass_majority": passes > total / 2,
        "pass_strict": passes == total,
        "pass_any": passes > 0,
        "pass_rate": passes / total if total > 0 else 0.0,
        "reps_pass": passes,
        "reps_total": total,
    }
```

**Step 14: Run all tests, verify all pass**

Run: `cd tools && python -m pytest test_eval_stats.py -v`
Expected: All PASS (14 tests)

**Step 15: Commit**

```bash
git add tools/eval_stats.py tools/test_eval_stats.py
git commit -m "feat: statistical library for benchmark evaluation (Wilson CI, bootstrap, McNemar's)"
```

---

### Task 2: Collection and normalization script (`tools/collect-run.sh`)

Standalone script that transforms harbor's raw output into the archive format.
Idempotent, uses staging + atomic rename. This is the keystone of the archive system.

**Files:**
- Create: `tools/collect-run.sh`

**Step 1: Write the script**

Key behaviors to implement:
- Accept `--harbor-dir`, `--archive-dir`, `--run-id`, `--dry-run`
- Discover task directories (pattern: `*__*`)
- For each task, group reps by task name, sort by trial hash, assign rep numbers
- Copy (not move) from harbor layout to archive layout per the mapping in the design doc
- Apply artifact exclusion filter (`.git/`, `node_modules/`, `__pycache__/`, etc.)
- Derive failure category per rep (write `failure_category.txt`)
- Write `rep_mapping` to stdout as JSON (caller writes to manifest)
- Use staging directory: write to `$ARCHIVE_DIR.staging/`, atomic `mv` on success
- On `--dry-run`, print what would be copied without writing

The full mapping (harbor → archive):
```
task__hash/
  agent/command-0/stdout.txt     →  agent-stdout.txt
  agent/serf-state/sessions/*    →  sessions/
  agent/serf-state/api.jsonl     →  api.jsonl
  agent/artifacts/*              →  artifacts/   (filtered)
  verifier/test-stdout.txt       →  verifier-stdout.txt
  verifier/reward.txt            →  reward.txt
  result.json                    →  harbor-result.json
```

Failure categorization logic (from transcript-viewer.py):
```bash
if grep -q "AgentTimeoutError" "$harbor_result"; then
    echo "timeout"
elif grep -q "\[submit_result\]\|\[communicate\]" "$agent_stdout"; then
    echo "wrong_answer"
elif grep -q "\[error\]" "$agent_stdout"; then
    echo "api_error"
else
    echo "no_submit"
fi
```

Artifact exclusion (rsync `--exclude`):
```
.git/ node_modules/ __pycache__/ .venv/ *.pyc *.o *.so .cache/
```

**Step 2: Test manually with flower-garden data**

```bash
# Rsync a small harbor job locally for testing
rsync -avz jesse@192.168.118.101:/tmp/git-webserver-53/git-webserver-53/ /tmp/test-harbor-output/

# Dry run
./tools/collect-run.sh \
  --harbor-dir /tmp/test-harbor-output \
  --archive-dir /tmp/test-archive/runs/test-run \
  --dry-run

# Real run
./tools/collect-run.sh \
  --harbor-dir /tmp/test-harbor-output \
  --archive-dir /tmp/test-archive/runs/test-run

# Verify structure
find /tmp/test-archive -type f | head -30
cat /tmp/test-archive/runs/test-run/tasks/configure-git-webserver/rep-1/reward.txt
cat /tmp/test-archive/runs/test-run/tasks/configure-git-webserver/rep-1/failure_category.txt
```

**Step 3: Test idempotency**

```bash
# Run collect again — should produce identical output
./tools/collect-run.sh \
  --harbor-dir /tmp/test-harbor-output \
  --archive-dir /tmp/test-archive/runs/test-run

# Diff should be empty
diff -r /tmp/test-archive/runs/test-run /tmp/test-archive/runs/test-run
```

**Step 4: Commit**

```bash
git add tools/collect-run.sh
git commit -m "feat: idempotent collection script with staging + atomic rename"
```

---

### Task 3: Summary generation (`tools/generate_summary.py`)

Reads a collected archive run directory and produces `summary.json`.

**Files:**
- Create: `tools/generate_summary.py`
- Create: `tools/test_generate_summary.py`

**Step 1: Write failing test**

```python
# tools/test_generate_summary.py
import json
import os
import tempfile
from generate_summary import generate_summary

def _make_fixture(tmp_dir, tasks):
    """Create a minimal archive fixture.

    tasks is a dict like {"build-cython-ext": [1.0, 0.0], "fix-vuln": [1.0, 1.0]}
    """
    for task_name, rewards in tasks.items():
        for i, reward in enumerate(rewards, 1):
            rep_dir = os.path.join(tmp_dir, "tasks", task_name, f"rep-{i}")
            os.makedirs(rep_dir, exist_ok=True)
            with open(os.path.join(rep_dir, "reward.txt"), "w") as f:
                f.write(str(reward))
            with open(os.path.join(rep_dir, "failure_category.txt"), "w") as f:
                f.write("wrong_answer" if reward == 0.0 else "")

def test_basic_summary():
    with tempfile.TemporaryDirectory() as d:
        _make_fixture(d, {"task-a": [1.0, 0.0, 1.0], "task-b": [0.0, 0.0, 0.0]})
        summary = generate_summary(d, "test-run-id")
        assert summary["schema_version"] == 1
        assert summary["task_count"] == 2
        assert summary["pass_count_majority"] == 1  # task-a passes majority
        assert summary["pass_rate_majority"] == 0.5
        assert summary["pass_count_strict"] == 0
        assert summary["pass_count_any"] == 1
        assert len(summary["tasks"]) == 2

def test_summary_per_task_detail():
    with tempfile.TemporaryDirectory() as d:
        _make_fixture(d, {"task-a": [1.0, 1.0]})
        summary = generate_summary(d, "test-run-id")
        task = summary["tasks"][0]
        assert task["name"] == "task-a"
        assert task["pass_majority"] is True
        assert task["pass_strict"] is True
        assert len(task["reps"]) == 2
```

**Step 2: Run test to verify it fails**

Run: `cd tools && python -m pytest test_generate_summary.py -v`
Expected: FAIL with `ModuleNotFoundError`

**Step 3: Implement**

```python
# tools/generate_summary.py
"""Generate summary.json from a collected archive run directory."""

import json
import os
import sys
from eval_stats import wilson_ci, aggregate_task_results


def generate_summary(run_dir: str, run_id: str) -> dict:
    """Read reward.txt files from archive and produce summary dict."""
    tasks_dir = os.path.join(run_dir, "tasks")
    if not os.path.isdir(tasks_dir):
        raise FileNotFoundError(f"No tasks/ directory in {run_dir}")

    tasks = []
    for task_name in sorted(os.listdir(tasks_dir)):
        task_path = os.path.join(tasks_dir, task_name)
        if not os.path.isdir(task_path):
            continue

        reps = []
        for rep_name in sorted(os.listdir(task_path)):
            rep_path = os.path.join(task_path, rep_name)
            reward_file = os.path.join(rep_path, "reward.txt")
            if not os.path.isfile(reward_file):
                continue

            reward = float(open(reward_file).read().strip())
            rep_num = int(rep_name.replace("rep-", ""))

            # Optional fields
            fc_file = os.path.join(rep_path, "failure_category.txt")
            failure_cat = None
            if os.path.isfile(fc_file):
                fc = open(fc_file).read().strip()
                if fc:
                    failure_cat = fc

            reps.append({
                "rep": rep_num,
                "reward": reward,
                "failure_category": failure_cat,
            })

        if not reps:
            continue

        rewards = [r["reward"] for r in reps]
        agg = aggregate_task_results(task_name, rewards)
        agg["reps"] = reps
        tasks.append(agg)

    # Aggregate
    n_tasks = len(tasks)
    majority = sum(1 for t in tasks if t["pass_majority"])
    strict = sum(1 for t in tasks if t["pass_strict"])
    any_pass = sum(1 for t in tasks if t["pass_any"])

    # Failure category counts (across all reps)
    fc_counts = {}
    for t in tasks:
        for r in t["reps"]:
            fc = r.get("failure_category")
            if fc:
                fc_counts[fc] = fc_counts.get(fc, 0) + 1

    maj_lo, maj_hi = wilson_ci(majority, n_tasks) if n_tasks > 0 else (0, 1)

    return {
        "schema_version": 1,
        "run_id": run_id,
        "task_count": n_tasks,
        "pass_count_majority": majority,
        "pass_count_strict": strict,
        "pass_count_any": any_pass,
        "pass_rate_majority": round(majority / n_tasks, 4) if n_tasks else 0,
        "pass_rate_strict": round(strict / n_tasks, 4) if n_tasks else 0,
        "pass_rate_any": round(any_pass / n_tasks, 4) if n_tasks else 0,
        "pass_rate_majority_ci_95": [round(maj_lo, 4), round(maj_hi, 4)],
        "failure_categories": fc_counts,
        "tasks": tasks,
    }


if __name__ == "__main__":
    run_dir = sys.argv[1]
    run_id = sys.argv[2] if len(sys.argv) > 2 else os.path.basename(run_dir)
    summary = generate_summary(run_dir, run_id)
    print(json.dumps(summary, indent=2))
```

**Step 4: Run tests**

Run: `cd tools && python -m pytest test_generate_summary.py -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add tools/generate_summary.py tools/test_generate_summary.py
git commit -m "feat: summary.json generation with all three pass rate metrics"
```

---

### Task 4: Update serf adapter (`serf_agent.py`)

Add artifact filtering and bind-mount state dir. This file lives on the eval server,
not in the serf repo, but we keep a copy in `tools/` for version control.

**Files:**
- Create: `tools/serf_agent.py` (canonical copy, deployed to eval servers)

**Step 1: Write the updated adapter**

Key changes from existing `serf_agent.py`:
- Change `_CONTAINER_STATE_DIR` from `/tmp/serf-state` to `/logs/agent/serf-state`
- Add artifact extraction with exclusion filter in `run()` `finally` block
- Keep the existing `download_dir` for serf-state as fallback

```python
# Added constants:
_CONTAINER_STATE_DIR = "/logs/agent/serf-state"   # bind-mounted by harbor

_ARTIFACT_EXCLUDES = [
    ".git", "node_modules", "__pycache__", ".venv",
    "*.pyc", "*.o", "*.so", ".cache",
]
_ARTIFACT_WARN_MB = 100
```

The `run()` override becomes:
```python
async def run(self, instruction, environment, context):
    try:
        await super().run(instruction, environment, context)
    finally:
        local_state_dir = self.logs_dir / "serf-state"
        try:
            await environment.download_dir(_CONTAINER_STATE_DIR, local_state_dir)
            logger.info("Downloaded serf traces to %s", local_state_dir)
        except Exception as e:
            logger.warning("Could not download serf traces: %s", e)

        # Extract agent artifacts from /app (filtered)
        try:
            artifacts_dir = self.logs_dir / "artifacts"
            await environment.download_dir(
                "/app", artifacts_dir,
                exclude=_ARTIFACT_EXCLUDES,
            )
            # Warn if artifacts are large
            total = sum(
                f.stat().st_size for f in artifacts_dir.rglob("*") if f.is_file()
            ) if artifacts_dir.exists() else 0
            if total > _ARTIFACT_WARN_MB * 1024 * 1024:
                logger.warning(
                    "Large artifacts: %dMB in /app (threshold: %dMB)",
                    total // (1024 * 1024), _ARTIFACT_WARN_MB,
                )
        except Exception as e:
            logger.warning("Could not download /app artifacts: %s", e)
```

**Step 2: Verify harbor's `download_dir` supports `exclude`**

Check harbor source on flower-garden:
```bash
ssh jesse@192.168.118.101 'grep -n "exclude" ~/.local/share/uv/tools/harbor/lib/python3.13/site-packages/harbor/environments/base.py'
```

If `exclude` is not supported, fall back to:
```python
# Download everything, then prune locally
await environment.download_dir("/app", artifacts_dir)
for pattern in _ARTIFACT_EXCLUDES:
    for match in artifacts_dir.rglob(pattern):
        if match.is_dir():
            shutil.rmtree(match)
        else:
            match.unlink()
```

**Step 3: Deploy and smoke-test**

```bash
scp tools/serf_agent.py jesse@192.168.118.101:~/git/terminal-bench/serf_agent.py
# Run single task to verify artifacts are captured
NO_BUILD=1 MODEL="openai/gpt-5.3-codex" ./tools/eval-task.sh adapter-test build-cython-ext 1 enable_reviewer_gate=true
```

**Step 4: Commit**

```bash
git add tools/serf_agent.py
git commit -m "feat: serf adapter with artifact extraction and bind-mount state dir"
```

---

### Task 5: Orchestration wrapper (`tools/run-eval.sh`)

The main tool. Replaces `eval-task.sh` and `check-eval.sh` with one script
that handles the full lifecycle.

**Files:**
- Create: `tools/run-eval.sh`

**Step 1: Implement the script**

This is a ~200-line bash script. Key sections:

**Argument parsing:**
```
--job NAME          Job name (required)
--model MODEL       e.g., openai/gpt-5.3-codex
--task TASK          Single task name (omit for full suite)
--reps N            Repetitions (default: 3)
--concurrency N     Parallel tasks (default: 4)
--ak KEY=VALUE      Agent kwarg (repeatable)
--adapter PATH      Agent import path (default: serf_agent:SerfAgent)
--no-build          Skip cross-compile
--allow-dirty       Run from dirty git tree (stores diff)
--collect-only      Just collect/report an already-finished job
--status            Show status of a running job
--resume            Resume a crashed job
--force             Kill existing tmux session before launching
```

**Section: preflight**
```bash
# Check clean tree
if [ "$ALLOW_DIRTY" != "1" ]; then
    if ! git diff --quiet; then
        echo "ERROR: Dirty working tree. Commit or use --allow-dirty."
        exit 1
    fi
fi
```

**Section: build** (reuse from eval-task.sh lines 60-71)

**Section: snapshot**
```bash
SNAPSHOT_DIR="$RUN_STAGING/agent"
mkdir -p "$SNAPSHOT_DIR"
cp /tmp/serf-linux-amd64 "$SNAPSHOT_DIR/"
cp -r agent/prompts/ "$SNAPSHOT_DIR/prompts/"
cp -r agent/agents/ "$SNAPSHOT_DIR/agents/"
cp -r agent/skills/ "$SNAPSHOT_DIR/skills/"
cp tools/serf_agent.py "$SNAPSHOT_DIR/adapter.py"
cp tools/install-serf.sh.j2 "$SNAPSHOT_DIR/" 2>/dev/null || true
if [ "$ALLOW_DIRTY" = "1" ]; then
    git diff HEAD > "$SNAPSHOT_DIR/git-diff.patch"
fi
```

**Section: manifest** — write initial manifest with `status: "running"`

**Section: launch** — SSH, check `tmux has-session`, create tmux session with harbor command

**Section: monitor** — poll loop counting `reward.txt` files + liveness check

**Section: collect** — calls `tools/collect-run.sh`

**Section: summarize** — calls `tools/generate_summary.py`, updates manifest status

**Section: status** — print progress for `--status` mode

**Section: resume** — `harbor jobs resume` for `--resume` mode

**Step 2: Test each mode manually**

```bash
# Build + snapshot + manifest (no launch)
./tools/run-eval.sh --job test-smoke --task build-cython-ext --reps 1 --dry-run

# Full launch on magic-kingdom
./tools/run-eval.sh --job smoke-test --task build-cython-ext --reps 1

# Status check
./tools/run-eval.sh --job smoke-test --status

# Collect after completion
./tools/run-eval.sh --job smoke-test --collect-only
```

**Step 3: Commit**

```bash
git add tools/run-eval.sh
git commit -m "feat: run-eval.sh orchestration wrapper with full lifecycle"
```

---

### Task 6: HTML report generation (`tools/generate-report.py`)

Produces a static HTML report from a collected archive run. Reuses parsing
logic from `transcript-viewer.py` but reads the archive format.

**Files:**
- Create: `tools/generate_report.py`

**Step 1: Implement**

The report structure:

1. **Header**: Run ID, model, agent SHA, suite, pass rates with CIs
2. **Summary table**: Sortable table of all tasks — name, majority vote, per-rep rewards, duration, failure category
3. **Failure breakdown**: Count per category (timeout/wrong_answer/no_submit/api_error)
4. **Per-task detail** (collapsible): Verifier stdout, agent stdout, session list
5. **Per-session transcript** (collapsible): Reuses transcript-viewer.py parsing

Input: archive run directory (with `summary.json` already generated).
Output: `report.html` written to the run directory.

The HTML/CSS/JS is embedded in the Python script (same approach as transcript-viewer.py).
This is a single standalone file, no external dependencies beyond Python stdlib.

**Step 2: Test with real data**

```bash
# Assuming a collected archive at /tmp/test-archive/runs/test-run/
python tools/generate_report.py /tmp/test-archive/runs/test-run/
open /tmp/test-archive/runs/test-run/report.html
```

**Step 3: Commit**

```bash
git add tools/generate_report.py
git commit -m "feat: HTML report generation from archive runs"
```

---

### Task 7: Cross-run comparison (`tools/compare-runs.py`)

Compare two archive runs with bootstrap CIs and McNemar's test.

**Files:**
- Create: `tools/compare_runs.py`
- Create: `tools/test_compare_runs.py`

**Step 1: Write failing test**

```python
# tools/test_compare_runs.py
import tempfile
import os
from compare_runs import compare_runs

def _make_run(tmp_dir, run_name, tasks):
    """tasks: {"task-a": [1.0, 0.0], ...}"""
    run_dir = os.path.join(tmp_dir, "runs", run_name)
    for task, rewards in tasks.items():
        for i, r in enumerate(rewards, 1):
            rep_dir = os.path.join(run_dir, "tasks", task, f"rep-{i}")
            os.makedirs(rep_dir)
            with open(os.path.join(rep_dir, "reward.txt"), "w") as f:
                f.write(str(r))
    return run_dir

def test_compare_identical():
    with tempfile.TemporaryDirectory() as d:
        tasks = {"a": [1.0], "b": [0.0], "c": [1.0]}
        run_a = _make_run(d, "run-a", tasks)
        run_b = _make_run(d, "run-b", tasks)
        result = compare_runs(run_a, run_b)
        assert result["delta_majority"] == 0.0
        assert result["bootstrap_ci"][0] <= 0.0 <= result["bootstrap_ci"][1]

def test_compare_improvement():
    with tempfile.TemporaryDirectory() as d:
        run_a = _make_run(d, "run-a", {"a": [0.0], "b": [0.0], "c": [1.0]})
        run_b = _make_run(d, "run-b", {"a": [1.0], "b": [1.0], "c": [1.0]})
        result = compare_runs(run_a, run_b)
        assert result["delta_majority"] > 0
        assert len(result["improvements"]) == 2
        assert len(result["regressions"]) == 0
```

**Step 2: Run test to verify it fails, then implement, then verify it passes**

The comparison logic:
1. Read both `summary.json` files (or generate from reward.txt if missing)
2. Match tasks by name
3. For each task, compute majority-vote pass in both runs
4. Compute delta, improvements, regressions
5. Run `bootstrap_ci_pass_rate_diff` and `mcnemars_test` from `eval_stats.py`
6. Return structured result dict

**Step 3: Commit**

```bash
git add tools/compare_runs.py tools/test_compare_runs.py
git commit -m "feat: cross-run comparison with bootstrap CIs and McNemar's"
```

---

### Task 8: magic-kingdom setup and smoke test

Set up the eval server and run an end-to-end test.

**Files:**
- No code changes — this is infrastructure setup

**Step 1: Verify magic-kingdom access**

```bash
ssh jesse@magic-kingdom 'uname -a && docker --version && tmux -V && python3 --version'
```

**Step 2: Create directory structure**

```bash
ssh jesse@magic-kingdom 'mkdir -p /data/serf-evals/runs ~/eval/jobs'
```

**Step 3: Install harbor**

```bash
ssh jesse@magic-kingdom 'pip install uv && uv tool install harbor==0.1.44'
```

**Step 4: Deploy env and adapter**

```bash
scp .env jesse@magic-kingdom:~/eval/.env
scp tools/serf_agent.py jesse@magic-kingdom:~/eval/serf_agent.py
scp tools/install-serf.sh.j2 jesse@magic-kingdom:~/eval/install-serf.sh.j2
ssh jesse@magic-kingdom 'chmod 600 ~/eval/.env'
```

**Step 5: End-to-end smoke test**

```bash
./tools/run-eval.sh --job smoke-test --task build-cython-ext --reps 1 \
  --model openai/gpt-5.3-codex --ak enable_reviewer_gate=true

# Watch it
./tools/run-eval.sh --job smoke-test --status

# After completion, verify archive
ssh jesse@magic-kingdom 'find /data/serf-evals/runs/ -type f | head -30'
ssh jesse@magic-kingdom 'cat /data/serf-evals/runs/*/manifest.json'
ssh jesse@magic-kingdom 'cat /data/serf-evals/runs/*/summary.json'
```

**Step 6: Verify the full chain**

```bash
# Collect should have happened automatically
# Check the report
rsync -avz jesse@magic-kingdom:/data/serf-evals/runs/*smoke-test*/ /tmp/smoke-test-result/
open /tmp/smoke-test-result/report.html
```

**Step 7: Commit any fixes from smoke test**

```bash
git add -p  # review changes
git commit -m "fix: smoke test fixes for magic-kingdom deployment"
```

---

## Implementation Order and Dependencies

```
Task 1 (stats library)     ← no dependencies, foundational
  ↓
Task 2 (collect-run.sh)    ← standalone, uses failure categorization logic
  ↓
Task 3 (summary generation) ← depends on Task 1 (eval_stats), reads archive from Task 2
  ↓
Task 4 (adapter update)     ← independent of Tasks 1-3, but good to do after collect is defined
  ↓
Task 5 (run-eval.sh)        ← depends on Tasks 2, 3, 4 (calls collect, summary, deploys adapter)
  ↓
Task 6 (HTML report)        ← depends on Task 3 (reads summary.json)
  ↓
Task 7 (compare-runs)       ← depends on Tasks 1, 3 (uses stats, reads summaries)
  ↓
Task 8 (magic-kingdom)      ← depends on all above
```

Tasks 1-3 are the critical path. Task 4 can be done in parallel with Tasks 2-3.
Tasks 6-7 can be done in parallel with each other after Task 5.
