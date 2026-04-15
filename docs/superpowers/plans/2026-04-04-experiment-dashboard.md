# Experiment Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `tools/dashboard/` into a browser-based experiment explorer with live monitoring, transcript drill-down, and run comparison.

**Architecture:** Experiment-first navigation — three Python data stores (ExperimentStore for git-tracked JSON, existing RunStore for harbor dirs, LiveStore for S3 polling) feed a FastAPI backend. Preact SPA frontend via CDN ESM imports, no build step, reusing existing CSS design system.

**Tech Stack:** Python 3 / FastAPI / Preact + htm (CDN) / SSE for live updates

**Spec:** `docs/superpowers/specs/2026-04-04-experiment-dashboard-design.md`

---

## File Map

### New files
| File | Responsibility |
|------|---------------|
| `tools/dashboard/experiment_store.py` | Read git-tracked JSON (runs, tasks, scoreboard) |
| `tools/dashboard/s3_client.py` | Thin wrapper around `aws s3` CLI |
| `tools/dashboard/live_store.py` | S3 polling for in-flight waves, SSE emission |
| `tools/dashboard/test_experiment_store.py` | Tests for ExperimentStore |
| `tools/dashboard/test_s3_client.py` | Tests for S3Client |
| `tools/dashboard/test_live_store.py` | Tests for LiveStore |
| `tools/dashboard/test_new_routes.py` | Tests for new API routes |
| `tools/dashboard/static/js/app.js` | Preact router, nav bar, layout shell |
| `tools/dashboard/static/js/experiments.js` | Experiments list page |
| `tools/dashboard/static/js/scoreboard.js` | Interactive 89-task matrix |
| `tools/dashboard/static/js/live.js` | Live wave monitor with SSE |
| `tools/dashboard/static/js/compare.js` | Enhanced run comparison |
| `tools/dashboard/static/js/run-detail.js` | Enhanced run detail (Preact port) |
| `tools/dashboard/static/js/task-detail.js` | Enhanced task detail (Preact port) |
| `tools/dashboard/static/js/components/shared.js` | Shared components (score-bar, rep-dots, status-badge, stat-card, filters) |

### Modified files
| File | Changes |
|------|---------|
| `tools/dashboard/server.py` | Add new routes for experiments, scoreboard, live, enhanced compare |
| `tools/dashboard/conftest.py` | Add fixtures for experiment JSON files |
| `tools/dashboard/requirements.txt` | Add `sse-starlette` |
| `tools/dashboard/static/index.html` | Load Preact SPA instead of old app.js |
| `tools/dashboard/static/style.css` | Extend with new page styles |

### Unchanged files
| File | Why |
|------|-----|
| `tools/dashboard/data.py` | RunStore API unchanged |
| `tools/dashboard/stats.py` | Existing stats unchanged |
| `tools/dashboard/trajectory.py` | Trajectory parsing unchanged |
| `tools/dashboard/markdown_render.py` | Markdown rendering unchanged |
| `tools/dashboard/static/app.js` | Kept in repo for rollback, no longer served |

---

## Phase 1: Backend Data Layer

### Task 1: ExperimentStore

**Files:**
- Create: `tools/dashboard/experiment_store.py`
- Create: `tools/dashboard/test_experiment_store.py`
- Modify: `tools/dashboard/conftest.py` (add experiment fixtures)

- [ ] **Step 1: Add experiment fixtures to conftest.py**

Add these fixtures at the bottom of `tools/dashboard/conftest.py`:

```python
@pytest.fixture
def experiment_dir(tmp_path):
    """Create a temporary experiment directory with sample data."""
    runs_dir = tmp_path / "runs"
    runs_dir.mkdir()

    # Wave run
    wave = {
        "run_id": "wave-abc1234-20260401-0800",
        "date": "2026-04-01",
        "git_sha": "abc1234",
        "model": "openai/gpt-5.4-mini",
        "variant": "baseline",
        "results": {
            "chess-best-move": {
                "score": 0.667, "reps": [1.0, 0.0, 1.0],
                "reps_pass": 2, "reps_total": 3,
            },
            "kv-store-grpc": {
                "score": 1.0, "reps": [1.0, 1.0, 1.0],
                "reps_pass": 3, "reps_total": 3,
            },
            "regex-log": {
                "score": 0.0, "reps": [0.0, 0.0, 0.0],
                "reps_pass": 0, "reps_total": 3,
            },
        },
        "s3_prefix": "s3://harbor-eval-results-526275945504/runs/wave-abc1234-20260401-0800/",
    }
    (runs_dir / "wave-abc1234-20260401-0800.json").write_text(json.dumps(wave))

    # Experiment run
    exp = {
        "run_id": "exp-A-medverify-def5678",
        "date": "2026-04-02",
        "git_sha": "def5678",
        "model": "openai/gpt-5.4-mini",
        "variant": "exp-A-medverify",
        "results": {
            "chess-best-move": {
                "score": 1.0, "reps": [1.0, 1.0, 1.0],
                "reps_pass": 3, "reps_total": 3,
            },
        },
        "s3_prefix": "s3://harbor-eval-results-526275945504/runs/exp-A-medverify-def5678/",
    }
    (runs_dir / "exp-A-medverify-def5678.json").write_text(json.dumps(exp))

    # Task files
    tasks_dir = tmp_path / "tasks"
    tasks_dir.mkdir()
    chess_task = {
        "task": "chess-best-move",
        "current_score": 1.0,
        "current_run": "exp-A-medverify-def5678",
        "current_date": "2026-04-02",
        "status": "tested",
        "notes": "",
        "history": [
            {"run_id": "wave-abc1234-20260401-0800", "date": "2026-04-01",
             "git_sha": "abc1234", "model": "openai/gpt-5.4-mini",
             "score": 0.667, "reps": [1.0, 0.0, 1.0]},
            {"run_id": "exp-A-medverify-def5678", "date": "2026-04-02",
             "git_sha": "def5678", "model": "openai/gpt-5.4-mini",
             "score": 1.0, "reps": [1.0, 1.0, 1.0]},
        ],
    }
    (tasks_dir / "chess-best-move.json").write_text(json.dumps(chess_task))

    # Scoreboard
    scoreboard = {
        "model": "openai/gpt-5.4-mini",
        "total_tasks": 3,
        "tested_tasks": 3,
        "mean_score": 0.556,
        "tasks": {
            "chess-best-move": {
                "score": 1.0, "last_run": "exp-A-medverify-def5678",
                "last_date": "2026-04-02", "reps": [1.0, 1.0, 1.0],
                "status": "tested",
            },
            "kv-store-grpc": {
                "score": 1.0, "last_run": "wave-abc1234-20260401-0800",
                "last_date": "2026-04-01", "reps": [1.0, 1.0, 1.0],
                "status": "tested",
            },
            "regex-log": {
                "score": 0.0, "last_run": "wave-abc1234-20260401-0800",
                "last_date": "2026-04-01", "reps": [0.0, 0.0, 0.0],
                "status": "tested",
            },
        },
    }
    (tmp_path / "scoreboard.json").write_text(json.dumps(scoreboard))

    return tmp_path
```

- [ ] **Step 2: Write failing tests for ExperimentStore**

Create `tools/dashboard/test_experiment_store.py`:

```python
"""Tests for ExperimentStore — reads git-tracked experiment JSON."""

import json
import pytest
from experiment_store import ExperimentStore


class TestListExperiments:
    def test_returns_all_runs_sorted_by_date(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        experiments = store.list_experiments()
        assert len(experiments) == 2
        # Most recent first
        assert experiments[0]["run_id"] == "exp-A-medverify-def5678"
        assert experiments[1]["run_id"] == "wave-abc1234-20260401-0800"

    def test_includes_computed_fields(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        experiments = store.list_experiments()
        wave = next(e for e in experiments if e["run_id"].startswith("wave-"))
        assert wave["mean_score"] == pytest.approx(0.556, abs=0.01)
        assert wave["task_count"] == 3
        assert wave["perfect_count"] == 1  # only kv-store-grpc is 3/3

    def test_filter_waves_only(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        waves = store.list_experiments(run_type="wave")
        assert len(waves) == 1
        assert waves[0]["run_id"].startswith("wave-")

    def test_filter_experiments_only(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        exps = store.list_experiments(run_type="experiment")
        assert len(exps) == 1
        assert not exps[0]["run_id"].startswith("wave-")

    def test_empty_directory(self, tmp_path):
        store = ExperimentStore(tmp_path)
        assert store.list_experiments() == []


class TestGetExperiment:
    def test_returns_run_with_results(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        exp = store.get_experiment("wave-abc1234-20260401-0800")
        assert exp is not None
        assert exp["git_sha"] == "abc1234"
        assert "chess-best-move" in exp["results"]

    def test_returns_none_for_missing(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        assert store.get_experiment("nonexistent") is None


class TestGetScoreboard:
    def test_returns_full_scoreboard(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        sb = store.get_scoreboard()
        assert sb["total_tasks"] == 3
        assert sb["mean_score"] == pytest.approx(0.556, abs=0.01)
        assert "chess-best-move" in sb["tasks"]

    def test_filter_failing(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        sb = store.get_scoreboard(filter="failing")
        # Only regex-log has score 0.0
        assert len(sb["tasks"]) == 1
        assert "regex-log" in sb["tasks"]

    def test_filter_solved(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        sb = store.get_scoreboard(filter="solved")
        assert all(t["score"] == 1.0 for t in sb["tasks"].values())


class TestGetTaskHistory:
    def test_returns_history_sorted_by_date(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        history = store.get_task_history("chess-best-move")
        assert len(history) == 2
        # Most recent first
        assert history[0]["run_id"] == "exp-A-medverify-def5678"
        assert history[0]["score"] == 1.0

    def test_returns_empty_for_unknown_task(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        assert store.get_task_history("nonexistent") == []


class TestReload:
    def test_picks_up_new_files(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        assert len(store.list_experiments()) == 2

        # Add a new run
        new_run = {
            "run_id": "wave-new1111-20260403-0900",
            "date": "2026-04-03",
            "git_sha": "new1111",
            "model": "openai/gpt-5.4-mini",
            "variant": "new wave",
            "results": {},
            "s3_prefix": "s3://bucket/runs/wave-new1111-20260403-0900/",
        }
        (experiment_dir / "runs" / "wave-new1111-20260403-0900.json").write_text(
            json.dumps(new_run))

        store.reload()
        assert len(store.list_experiments()) == 3
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd tools/dashboard && python -m pytest test_experiment_store.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'experiment_store'`

- [ ] **Step 4: Implement ExperimentStore**

Create `tools/dashboard/experiment_store.py`:

```python
"""Read git-tracked experiment metadata (runs, tasks, scoreboard)."""

import json
from pathlib import Path


class ExperimentStore:
    """In-memory store for experiment JSON files.

    Reads from:
        {base_dir}/runs/*.json      — per-run metadata with scores
        {base_dir}/tasks/*.json     — per-task history scorecards
        {base_dir}/scoreboard.json  — master 89-task matrix
    """

    def __init__(self, base_dir):
        self.base_dir = Path(base_dir)
        self._runs = {}        # run_id -> run dict
        self._tasks = {}       # task_name -> task dict
        self._scoreboard = {}  # raw scoreboard dict
        self.reload()

    def reload(self):
        """Re-read all JSON files from disk."""
        self._runs = self._load_runs()
        self._tasks = self._load_tasks()
        self._scoreboard = self._load_scoreboard()

    def list_experiments(self, run_type=None):
        """Return all runs, most recent first.

        run_type: None (all), "wave", or "experiment".
        """
        runs = list(self._runs.values())

        if run_type == "wave":
            runs = [r for r in runs if r["run_id"].startswith("wave-")]
        elif run_type == "experiment":
            runs = [r for r in runs if not r["run_id"].startswith("wave-")]

        runs.sort(key=lambda r: r["date"], reverse=True)
        return runs

    def get_experiment(self, run_id):
        """Return a single run dict, or None."""
        return self._runs.get(run_id)

    def get_scoreboard(self, filter=None):
        """Return the scoreboard, optionally filtered.

        filter: None (all), "failing" (score < 1.0), "solved" (score == 1.0).
        """
        sb = dict(self._scoreboard)
        if not sb:
            return {"total_tasks": 0, "tested_tasks": 0, "mean_score": 0,
                    "tasks": {}}

        if filter is not None:
            tasks = dict(sb.get("tasks", {}))
            if filter == "failing":
                tasks = {k: v for k, v in tasks.items() if v.get("score", 0) < 1.0}
            elif filter == "solved":
                tasks = {k: v for k, v in tasks.items() if v.get("score", 0) == 1.0}
            sb = dict(sb, tasks=tasks)

        return sb

    def get_task_history(self, task_name):
        """Return score history for a task, most recent first."""
        task = self._tasks.get(task_name)
        if task is None:
            return []
        history = list(task.get("history", []))
        history.sort(key=lambda h: h.get("date", ""), reverse=True)
        return history

    # ------------------------------------------------------------------
    # Loading helpers
    # ------------------------------------------------------------------

    def _load_runs(self):
        runs_dir = self.base_dir / "runs"
        if not runs_dir.is_dir():
            return {}
        runs = {}
        for path in runs_dir.glob("*.json"):
            try:
                data = json.loads(path.read_text())
            except (json.JSONDecodeError, OSError):
                continue
            run_id = data.get("run_id", path.stem)
            results = data.get("results", {})
            scores = [r["score"] for r in results.values()]
            perfect = sum(1 for r in results.values() if r.get("score", 0) == 1.0)
            data["mean_score"] = sum(scores) / len(scores) if scores else 0.0
            data["task_count"] = len(results)
            data["perfect_count"] = perfect
            runs[run_id] = data
        return runs

    def _load_tasks(self):
        tasks_dir = self.base_dir / "tasks"
        if not tasks_dir.is_dir():
            return {}
        tasks = {}
        for path in tasks_dir.glob("*.json"):
            try:
                data = json.loads(path.read_text())
            except (json.JSONDecodeError, OSError):
                continue
            task_name = data.get("task", path.stem)
            tasks[task_name] = data
        return tasks

    def _load_scoreboard(self):
        path = self.base_dir / "scoreboard.json"
        if not path.is_file():
            return {}
        try:
            return json.loads(path.read_text())
        except (json.JSONDecodeError, OSError):
            return {}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd tools/dashboard && python -m pytest test_experiment_store.py -v`
Expected: All 11 tests PASS

- [ ] **Step 6: Run existing tests to verify no breakage**

Run: `cd tools/dashboard && python -m pytest -v`
Expected: All existing tests still PASS

- [ ] **Step 7: Commit**

```bash
git add tools/dashboard/experiment_store.py tools/dashboard/test_experiment_store.py tools/dashboard/conftest.py
git commit -m "feat(dashboard): add ExperimentStore for git-tracked JSON metadata"
```

---

### Task 2: S3Client

**Files:**
- Create: `tools/dashboard/s3_client.py`
- Create: `tools/dashboard/test_s3_client.py`

- [ ] **Step 1: Write failing tests**

Create `tools/dashboard/test_s3_client.py`:

```python
"""Tests for S3Client — thin wrapper around aws CLI."""

import json
import pytest
from unittest.mock import patch, MagicMock
from s3_client import S3Client


BUCKET = "harbor-eval-results-526275945504"
REGION = "us-west-1"


class TestListObjects:
    @patch("s3_client.subprocess.run")
    def test_returns_parsed_keys(self, mock_run):
        mock_run.return_value = MagicMock(
            returncode=0,
            stdout=(
                "2026-04-01 08:00:00     1234 runs/wave-abc/rep-1/task__hash/result.json\n"
                "2026-04-01 08:01:00      567 runs/wave-abc/rep-1/task__hash/verifier/reward.txt\n"
            ),
        )
        client = S3Client(BUCKET, REGION)
        keys = client.list_objects("runs/wave-abc/")
        assert len(keys) == 2
        assert keys[0].endswith("result.json")

    @patch("s3_client.subprocess.run")
    def test_returns_empty_on_error(self, mock_run):
        mock_run.return_value = MagicMock(returncode=1, stdout="", stderr="error")
        client = S3Client(BUCKET, REGION)
        assert client.list_objects("runs/missing/") == []


class TestGetJson:
    @patch("s3_client.subprocess.run")
    def test_returns_parsed_json(self, mock_run):
        data = {"verifier_result": {"rewards": {"reward": 1.0}}}
        mock_run.return_value = MagicMock(returncode=0, stdout=json.dumps(data))
        client = S3Client(BUCKET, REGION)
        result = client.get_json("runs/wave-abc/rep-1/task/result.json")
        assert result["verifier_result"]["rewards"]["reward"] == 1.0

    @patch("s3_client.subprocess.run")
    def test_returns_none_on_error(self, mock_run):
        mock_run.return_value = MagicMock(returncode=1, stdout="", stderr="err")
        client = S3Client(BUCKET, REGION)
        assert client.get_json("missing/file.json") is None


class TestSyncToLocal:
    @patch("s3_client.subprocess.run")
    def test_builds_correct_command(self, mock_run):
        mock_run.return_value = MagicMock(returncode=0, stdout="")
        client = S3Client(BUCKET, REGION)
        client.sync_to_local("runs/wave-abc/", "/tmp/cache/wave-abc")
        args = mock_run.call_args[0][0]
        assert "s3" in args
        assert "sync" in args
        assert f"s3://{BUCKET}/runs/wave-abc/" in args
        assert "/tmp/cache/wave-abc" in args
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd tools/dashboard && python -m pytest test_s3_client.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 's3_client'`

- [ ] **Step 3: Implement S3Client**

Create `tools/dashboard/s3_client.py`:

```python
"""Thin wrapper around aws s3 CLI — no boto3 dependency."""

import json
import subprocess


class S3Client:
    """Shell out to aws CLI for S3 operations."""

    def __init__(self, bucket, region="us-west-1"):
        self.bucket = bucket
        self.region = region

    def list_objects(self, prefix):
        """List object keys under an S3 prefix.

        Returns list of full key strings (no bucket prefix).
        """
        r = subprocess.run(
            ["aws", "s3", "ls", f"s3://{self.bucket}/{prefix}",
             "--region", self.region, "--recursive"],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            return []
        keys = []
        for line in r.stdout.strip().split("\n"):
            line = line.strip()
            if not line:
                continue
            # Format: "2026-04-01 08:00:00     1234 path/to/file"
            parts = line.split(None, 3)
            if len(parts) >= 4:
                keys.append(parts[3])
        return keys

    def get_json(self, key):
        """Fetch a JSON file from S3, return parsed dict or None."""
        r = subprocess.run(
            ["aws", "s3", "cp", f"s3://{self.bucket}/{key}", "-",
             "--region", self.region],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            return None
        try:
            return json.loads(r.stdout)
        except json.JSONDecodeError:
            return None

    def sync_to_local(self, prefix, local_dir):
        """Sync an S3 prefix to a local directory.

        Returns True on success, False on error.
        """
        r = subprocess.run(
            ["aws", "s3", "sync", f"s3://{self.bucket}/{prefix}", local_dir,
             "--region", self.region],
            capture_output=True, text=True,
        )
        return r.returncode == 0
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd tools/dashboard && python -m pytest test_s3_client.py -v`
Expected: All 5 tests PASS

- [ ] **Step 5: Commit**

```bash
git add tools/dashboard/s3_client.py tools/dashboard/test_s3_client.py
git commit -m "feat(dashboard): add S3Client wrapper for aws CLI"
```

---

### Task 3: Experiment API Routes

**Files:**
- Modify: `tools/dashboard/server.py`
- Create: `tools/dashboard/test_new_routes.py`
- Modify: `tools/dashboard/requirements.txt`

- [ ] **Step 1: Write failing tests for new routes**

Create `tools/dashboard/test_new_routes.py`:

```python
"""Tests for new experiment/scoreboard API routes."""

import json
import pytest
from fastapi.testclient import TestClient


@pytest.fixture
def client(experiment_dir, harbor_job_dir):
    """Create test client with both experiment and harbor data."""
    import server as server_mod

    server_mod.store = server_mod.RunStore(str(harbor_job_dir.parent))
    server_mod._cache_dir = str(harbor_job_dir.parent / ".cache")

    from experiment_store import ExperimentStore
    server_mod.experiment_store = ExperimentStore(str(experiment_dir))

    return TestClient(server_mod.app)


class TestExperimentRoutes:
    def test_list_experiments(self, client):
        resp = client.get("/api/experiments",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 2
        # Most recent first
        assert data[0]["run_id"] == "exp-A-medverify-def5678"

    def test_list_experiments_filter_waves(self, client):
        resp = client.get("/api/experiments?type=wave",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 1
        assert data[0]["run_id"].startswith("wave-")

    def test_get_experiment(self, client):
        resp = client.get("/api/experiments/wave-abc1234-20260401-0800",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["git_sha"] == "abc1234"
        assert "chess-best-move" in data["results"]

    def test_get_experiment_not_found(self, client):
        resp = client.get("/api/experiments/nonexistent",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 404


class TestScoreboardRoute:
    def test_get_scoreboard(self, client):
        resp = client.get("/api/scoreboard",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["total_tasks"] == 3
        assert "chess-best-move" in data["tasks"]

    def test_get_scoreboard_filter_failing(self, client):
        resp = client.get("/api/scoreboard?filter=failing",
                          headers={"Accept": "application/json"})
        data = resp.json()
        assert "regex-log" in data["tasks"]
        assert "chess-best-move" not in data["tasks"]


class TestTaskHistoryRoute:
    def test_get_task_history(self, client):
        resp = client.get("/api/experiments/tasks/chess-best-move/history",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 2
        assert data[0]["score"] == 1.0

    def test_task_history_unknown_task(self, client):
        resp = client.get("/api/experiments/tasks/nonexistent/history",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        assert resp.json() == []
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd tools/dashboard && python -m pytest test_new_routes.py -v`
Expected: FAIL — routes don't exist yet

- [ ] **Step 3: Add new routes to server.py**

Add these imports at the top of `tools/dashboard/server.py`, after the existing imports:

```python
from experiment_store import ExperimentStore
```

Add this after the existing `store = RunStore(...)` line:

```python
_experiments_dir = os.environ.get("DASHBOARD_EXPERIMENTS_DIR", "")
experiment_store = ExperimentStore(_experiments_dir) if _experiments_dir else None
```

Add these route functions before the existing `@app.get("/raw/{file_path:path}")` route:

```python
@app.get("/api/experiments")
def list_experiments(request: Request, type: str = None):
    if experiment_store is None:
        return JSONResponse({"error": "experiments not configured"}, status_code=501)
    experiments = experiment_store.list_experiments(run_type=type)
    return JSONResponse(experiments)


@app.get("/api/experiments/{run_id}")
def get_experiment(run_id: str, request: Request):
    if experiment_store is None:
        return JSONResponse({"error": "experiments not configured"}, status_code=501)
    exp = experiment_store.get_experiment(run_id)
    if exp is None:
        return JSONResponse({"error": "not found"}, status_code=404)
    return JSONResponse(exp)


@app.get("/api/scoreboard")
def get_scoreboard(request: Request, filter: str = None):
    if experiment_store is None:
        return JSONResponse({"error": "experiments not configured"}, status_code=501)
    sb = experiment_store.get_scoreboard(filter=filter)
    return JSONResponse(sb)


@app.get("/api/experiments/tasks/{task_name}/history")
def experiment_task_history(task_name: str):
    if experiment_store is None:
        return JSONResponse({"error": "experiments not configured"}, status_code=501)
    history = experiment_store.get_task_history(task_name)
    return JSONResponse(history)
```

Update the `if __name__ == "__main__"` block to accept `--experiments-dir`:

```python
    parser.add_argument("--experiments-dir", default=None,
                        help="Directory with git-tracked experiment JSON")
```

And in the block after `if args.data_dir:`, add:

```python
    if args.experiments_dir:
        sys.modules[__name__].experiment_store = ExperimentStore(args.experiments_dir)
    elif not experiment_store:
        # Default: look for docs/experiments relative to repo root
        repo_root = os.path.dirname(os.path.dirname(os.path.dirname(__file__)))
        exp_dir = os.path.join(repo_root, "docs", "experiments")
        if os.path.isdir(exp_dir):
            sys.modules[__name__].experiment_store = ExperimentStore(exp_dir)
```

- [ ] **Step 4: Run new tests to verify they pass**

Run: `cd tools/dashboard && python -m pytest test_new_routes.py -v`
Expected: All 7 tests PASS

- [ ] **Step 5: Run ALL tests to verify no breakage**

Run: `cd tools/dashboard && python -m pytest -v`
Expected: All tests PASS (existing + new)

- [ ] **Step 6: Commit**

```bash
git add tools/dashboard/server.py tools/dashboard/test_new_routes.py
git commit -m "feat(dashboard): add experiment, scoreboard, and task history API routes"
```

---

### Task 4: LiveStore + SSE

**Files:**
- Create: `tools/dashboard/live_store.py`
- Create: `tools/dashboard/test_live_store.py`
- Modify: `tools/dashboard/server.py` (SSE endpoint)
- Modify: `tools/dashboard/requirements.txt`

- [ ] **Step 1: Update requirements.txt**

Replace the contents of `tools/dashboard/requirements.txt` with:

```
fastapi>=0.115
uvicorn>=0.30
sse-starlette>=2.0
```

- [ ] **Step 2: Write failing tests for LiveStore**

Create `tools/dashboard/test_live_store.py`:

```python
"""Tests for LiveStore — wave discovery and S3 score polling."""

import json
import pytest
from unittest.mock import patch, MagicMock
from live_store import LiveStore


@pytest.fixture
def launches_dir(tmp_path):
    """Create a .serf-launches directory with sample wave metadata."""
    d = tmp_path / ".serf-launches"
    d.mkdir()
    wave = {
        "run_id": "wave-test123-20260404-1200",
        "model": "openai/gpt-5.4-mini",
        "reps": 3,
        "variant": "test wave",
        "tasks": ["chess-best-move", "kv-store-grpc"],
        "launched_at": "2026-04-04T12:00:00+00:00",
    }
    (d / "wave-test123-20260404-1200.json").write_text(json.dumps(wave))
    return d


class TestDiscoverWaves:
    def test_finds_wave_files(self, launches_dir):
        store = LiveStore(str(launches_dir), bucket="test-bucket")
        waves = store.discover_waves()
        assert len(waves) == 1
        assert waves[0]["run_id"] == "wave-test123-20260404-1200"

    def test_empty_directory(self, tmp_path):
        d = tmp_path / ".serf-launches"
        d.mkdir()
        store = LiveStore(str(d), bucket="test-bucket")
        assert store.discover_waves() == []

    def test_missing_directory(self, tmp_path):
        store = LiveStore(str(tmp_path / "nonexistent"), bucket="test-bucket")
        assert store.discover_waves() == []


class TestPollWaveScores:
    @patch("live_store.S3Client")
    def test_returns_scores_by_task(self, MockS3, launches_dir):
        mock_client = MockS3.return_value
        mock_client.list_objects.return_value = [
            "runs/wave-test/rep-1/chess-best-move__abc/result.json",
            "runs/wave-test/rep-2/chess-best-move__def/result.json",
        ]
        mock_client.get_json.side_effect = [
            {"verifier_result": {"rewards": {"reward": 1.0}}},
            {"verifier_result": {"rewards": {"reward": 0.0}}},
        ]
        store = LiveStore(str(launches_dir), bucket="test-bucket")
        scores = store.poll_wave_scores("wave-test")
        assert "chess-best-move" in scores
        assert scores["chess-best-move"]["rep-1"] == 1.0
        assert scores["chess-best-move"]["rep-2"] == 0.0

    @patch("live_store.S3Client")
    def test_empty_results(self, MockS3, launches_dir):
        mock_client = MockS3.return_value
        mock_client.list_objects.return_value = []
        store = LiveStore(str(launches_dir), bucket="test-bucket")
        scores = store.poll_wave_scores("wave-test")
        assert scores == {}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd tools/dashboard && python -m pytest test_live_store.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'live_store'`

- [ ] **Step 4: Implement LiveStore**

Create `tools/dashboard/live_store.py`:

```python
"""Live wave monitoring — S3 polling and score tracking."""

import json
import re
from pathlib import Path

from s3_client import S3Client


DEFAULT_BUCKET = "harbor-eval-results-526275945504"
DEFAULT_REGION = "us-west-1"


class LiveStore:
    """Discover active waves and poll S3 for live scores."""

    def __init__(self, launches_dir, bucket=DEFAULT_BUCKET, region=DEFAULT_REGION):
        self.launches_dir = Path(launches_dir)
        self.s3 = S3Client(bucket, region)

    def discover_waves(self):
        """Find wave metadata files in .serf-launches/ directory.

        Returns list of wave dicts sorted by launched_at (newest first).
        """
        if not self.launches_dir.is_dir():
            return []
        waves = []
        for path in self.launches_dir.glob("wave-*.json"):
            try:
                data = json.loads(path.read_text())
                waves.append(data)
            except (json.JSONDecodeError, OSError):
                continue
        waves.sort(key=lambda w: w.get("launched_at", ""), reverse=True)
        return waves

    def poll_wave_scores(self, wave_id):
        """Poll S3 for current scores of a wave.

        Returns {task_name: {rep_id: score_or_none}}.
        """
        keys = self.s3.list_objects(f"runs/{wave_id}/")
        result_keys = [k for k in keys if k.endswith("result.json")]

        scores = {}
        for key in result_keys:
            parts = key.split("/")
            if len(parts) < 5:
                continue
            rep = parts[2]  # rep-1, rep-2, etc.
            task_raw = parts[3] if len(parts) > 3 else ""
            task_name = re.sub(r"__[A-Za-z0-9]+$", "", task_raw)
            if not task_name:
                continue

            data = self.s3.get_json(key)
            if data is None:
                continue

            reward = self._extract_reward(data)
            scores.setdefault(task_name, {})[rep] = reward

        return scores

    def wave_summary(self, scores):
        """Compute summary stats from polled scores.

        Returns dict with completed, total, mean, perfect_count, etc.
        """
        if not scores:
            return {"completed": 0, "total": 0, "mean": 0.0, "perfect_count": 0}

        task_means = []
        perfect = 0
        for task, reps in scores.items():
            vals = [v for v in reps.values() if v is not None]
            if vals:
                mean = sum(vals) / len(vals)
                task_means.append(mean)
                if all(v >= 1.0 for v in vals):
                    perfect += 1

        return {
            "completed": len(task_means),
            "total": len(scores),
            "mean": sum(task_means) / len(task_means) if task_means else 0.0,
            "perfect_count": perfect,
        }

    def _extract_reward(self, data):
        """Extract reward from result.json data."""
        vr = data.get("verifier_result", {})
        if isinstance(vr, dict):
            rewards = vr.get("rewards", {})
            if isinstance(rewards, dict):
                r = rewards.get("reward")
                if r is not None:
                    return float(r)
        score = data.get("score")
        if score is not None:
            return float(score)
        return None
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd tools/dashboard && python -m pytest test_live_store.py -v`
Expected: All 5 tests PASS

- [ ] **Step 6: Add SSE endpoint to server.py**

Add this import at the top of `tools/dashboard/server.py`:

```python
from sse_starlette.sse import EventSourceResponse
```

Add these imports for the live store, alongside the ExperimentStore import:

```python
from live_store import LiveStore
```

Add this after the experiment_store initialization:

```python
_launches_dir = os.environ.get("DASHBOARD_LAUNCHES_DIR", "")
_s3_bucket = os.environ.get("DASHBOARD_S3_BUCKET", "harbor-eval-results-526275945504")
live_store = LiveStore(_launches_dir, bucket=_s3_bucket) if _launches_dir else None
```

Add these routes before the `raw_file` route:

```python
@app.get("/api/live/waves")
def list_live_waves():
    if live_store is None:
        return JSONResponse({"error": "live monitoring not configured"}, status_code=501)
    return JSONResponse(live_store.discover_waves())


@app.get("/api/live/waves/{wave_id}")
def get_live_wave(wave_id: str):
    if live_store is None:
        return JSONResponse({"error": "live monitoring not configured"}, status_code=501)
    scores = live_store.poll_wave_scores(wave_id)
    summary = live_store.wave_summary(scores)
    return JSONResponse({"wave_id": wave_id, "scores": scores, "summary": summary})


@app.get("/api/live/waves/{wave_id}/stream")
async def stream_wave(wave_id: str):
    """SSE endpoint that polls S3 and pushes score updates."""
    import asyncio

    if live_store is None:
        return JSONResponse({"error": "live monitoring not configured"}, status_code=501)

    async def event_generator():
        last_scores = {}
        while True:
            scores = live_store.poll_wave_scores(wave_id)
            summary = live_store.wave_summary(scores)

            # Emit task_complete events for newly completed tasks/reps
            for task, reps in scores.items():
                old_reps = last_scores.get(task, {})
                for rep, reward in reps.items():
                    if rep not in old_reps and reward is not None:
                        yield {
                            "event": "task_complete",
                            "data": json.dumps({
                                "task": task, "rep": rep, "reward": reward,
                            }),
                        }

            # Always emit progress
            yield {
                "event": "wave_progress",
                "data": json.dumps(summary),
            }

            last_scores = scores
            await asyncio.sleep(30)

    return EventSourceResponse(event_generator())
```

Update the `if __name__ == "__main__"` block to accept live monitoring args:

```python
    parser.add_argument("--launches-dir", default=None,
                        help="Directory with .serf-launches wave metadata")
    parser.add_argument("--s3-bucket", default=None,
                        help="S3 bucket for eval results")
```

And add after the experiments-dir handling:

```python
    if args.launches_dir:
        bucket = args.s3_bucket or "harbor-eval-results-526275945504"
        sys.modules[__name__].live_store = LiveStore(args.launches_dir, bucket=bucket)
    elif not live_store:
        repo_root = os.path.dirname(os.path.dirname(os.path.dirname(__file__)))
        launches = os.path.join(repo_root, ".serf-launches")
        if os.path.isdir(launches):
            sys.modules[__name__].live_store = LiveStore(launches)
```

- [ ] **Step 7: Run ALL tests**

Run: `cd tools/dashboard && python -m pytest -v`
Expected: All tests PASS

- [ ] **Step 8: Commit**

```bash
git add tools/dashboard/live_store.py tools/dashboard/test_live_store.py tools/dashboard/server.py tools/dashboard/requirements.txt
git commit -m "feat(dashboard): add LiveStore with S3 polling and SSE streaming"
```

---

## Phase 2: Frontend — Preact SPA

### Task 5: Preact Shell + Router + Shared Components

**Files:**
- Modify: `tools/dashboard/static/index.html`
- Create: `tools/dashboard/static/js/app.js`
- Create: `tools/dashboard/static/js/components/shared.js`

- [ ] **Step 1: Update index.html to load Preact SPA**

Replace the contents of `tools/dashboard/static/index.html` with:

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Serf Experiment Dashboard</title>
    <link rel="stylesheet" href="/static/style.css">
    <style>
        #app:empty { display: flex; align-items: center; justify-content: center;
                     min-height: 60vh; color: #A0A0A0; font-size: 14px; }
        #app:empty::after { content: 'Loading...'; }
    </style>
</head>
<body>
    <nav id="breadcrumb"></nav>
    <main id="app"></main>
    <script type="module" src="/static/js/app.js"></script>
</body>
</html>
```

- [ ] **Step 2: Create shared components**

Create directory: `tools/dashboard/static/js/components/`

Create `tools/dashboard/static/js/components/shared.js`:

```javascript
import { h } from 'https://esm.sh/preact@10.25.4'
import htm from 'https://esm.sh/htm@3.1.1'

const html = htm.bind(h)

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------

export function fmtScore(score) {
    if (score == null) return '—'
    return score.toFixed(3)
}

export function fmtPercent(n, total) {
    if (!total) return '0%'
    return `${((n / total) * 100).toFixed(1)}%`
}

export function fmtDate(dateStr) {
    if (!dateStr) return '—'
    return dateStr
}

export function fmtModel(model) {
    if (!model) return '—'
    // Strip provider prefix
    return model.includes('/') ? model.split('/')[1] : model
}

export function fmtWallTime(seconds) {
    if (seconds == null) return '—'
    const m = Math.floor(seconds / 60)
    const s = Math.round(seconds % 60)
    return m > 0 ? `${m}m ${s}s` : `${s}s`
}

export function fmtTokens(n) {
    if (n == null) return '—'
    if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
    if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
    return String(n)
}

// ---------------------------------------------------------------------------
// Data fetching
// ---------------------------------------------------------------------------

export async function fetchJSON(url) {
    const resp = await fetch(url, { headers: { Accept: 'application/json' } })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    return resp.json()
}

// ---------------------------------------------------------------------------
// Shared components
// ---------------------------------------------------------------------------

/** Score bar — horizontal green/red bar showing pass rate. */
export function ScoreBar({ score, width = 80 }) {
    const pct = Math.round((score || 0) * 100)
    return html`
        <div class="pass-rate-bar" style="width:${width}px">
            <div class="pass-rate-fill" style="width:${pct}%"></div>
            <span class="pass-rate-label">${fmtScore(score)}</span>
        </div>
    `
}

/** Rep dots — colored circles for individual rep results. */
export function RepDots({ reps }) {
    if (!reps || !reps.length) return null
    return html`
        <span class="rep-dots">
            ${reps.map((r, i) => html`
                <span key=${i} class="dot ${r >= 1.0 ? 'pass' : 'fail'}"
                      title="Rep ${i + 1}: ${r >= 1.0 ? 'pass' : 'fail'}"></span>
            `)}
        </span>
    `
}

/** Status badge — colored label for pass/fail/running/queued. */
export function StatusBadge({ status }) {
    return html`<span class="status-dot ${status}">${status}</span>`
}

/** Stat card — metric with label. */
export function StatCard({ label, value, sub }) {
    return html`
        <div class="stat-card">
            <div class="stat-value">${value}</div>
            <div class="stat-label">${label}</div>
            ${sub && html`<div class="stat-sub">${sub}</div>`}
        </div>
    `
}

/** Filter bar — row of filter buttons. */
export function FilterBar({ options, active, onSelect }) {
    return html`
        <div class="filter-bar">
            ${options.map(opt => html`
                <button key=${opt.value}
                    class="filter-btn ${active === opt.value ? 'active' : ''}"
                    onClick=${() => onSelect(opt.value)}>
                    ${opt.label}${opt.count != null ? ` (${opt.count})` : ''}
                </button>
            `)}
        </div>
    `
}
```

- [ ] **Step 3: Create Preact router and app shell**

Create `tools/dashboard/static/js/app.js`:

```javascript
import { h, render } from 'https://esm.sh/preact@10.25.4'
import { useState, useEffect } from 'https://esm.sh/preact@10.25.4/hooks'
import htm from 'https://esm.sh/htm@3.1.1'

const html = htm.bind(h)

// Lazy-load pages
const pages = {
    experiments: () => import('./experiments.js'),
    scoreboard: () => import('./scoreboard.js'),
    live: () => import('./live.js'),
    compare: () => import('./compare.js'),
    runDetail: () => import('./run-detail.js'),
    taskDetail: () => import('./task-detail.js'),
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

function parseHash() {
    const hash = location.hash || '#/'
    let m
    if ((m = hash.match(/^#\/experiments\/([^/]+)\/tasks\/([^/?]+)/)))
        return { page: 'taskDetail', params: { runId: decodeURIComponent(m[1]), task: decodeURIComponent(m[2]) } }
    if ((m = hash.match(/^#\/experiments\/([^/]+)$/)))
        return { page: 'runDetail', params: { runId: decodeURIComponent(m[1]) } }
    if (hash.startsWith('#/scoreboard'))
        return { page: 'scoreboard', params: {} }
    if (hash.startsWith('#/live'))
        return { page: 'live', params: {} }
    if (hash.startsWith('#/compare'))
        return { page: 'compare', params: {} }
    // Legacy routes — redirect to new ones
    if ((m = hash.match(/^#\/runs\/([^/]+)\/tasks\/([^/?]+)/)))
        return { page: 'taskDetail', params: { runId: decodeURIComponent(m[1]), task: decodeURIComponent(m[2]), legacy: true } }
    if ((m = hash.match(/^#\/runs\/([^/]+)$/)))
        return { page: 'runDetail', params: { runId: decodeURIComponent(m[1]), legacy: true } }
    return { page: 'experiments', params: {} }
}

function useRoute() {
    const [route, setRoute] = useState(parseHash)
    useEffect(() => {
        const handler = () => setRoute(parseHash())
        window.addEventListener('hashchange', handler)
        return () => window.removeEventListener('hashchange', handler)
    }, [])
    return route
}

// ---------------------------------------------------------------------------
// Nav
// ---------------------------------------------------------------------------

const NAV_ITEMS = [
    { label: 'Experiments', hash: '#/' },
    { label: 'Scoreboard', hash: '#/scoreboard' },
    { label: 'Live', hash: '#/live' },
    { label: 'Compare', hash: '#/compare' },
]

function NavBar({ currentPage }) {
    return html`
        <div class="nav-bar">
            ${NAV_ITEMS.map(item => html`
                <a key=${item.hash} href=${item.hash}
                   class="nav-item ${currentPage === item.label.toLowerCase() ? 'active' : ''}">
                    ${item.label}
                </a>
            `)}
        </div>
    `
}

function Breadcrumb({ items }) {
    return html`
        <div class="breadcrumb-items">
            ${items.map((item, i) => html`
                <${item.href ? 'a' : 'span'} key=${i}
                    href=${item.href || undefined}
                    class=${!item.href ? 'current' : ''}>
                    ${item.label}
                <//>
                ${i < items.length - 1 && html`<span class="sep">/</span>`}
            `)}
        </div>
    `
}

// ---------------------------------------------------------------------------
// Page loader
// ---------------------------------------------------------------------------

function PageLoader({ page, params }) {
    const [Component, setComponent] = useState(null)
    const [error, setError] = useState(null)

    useEffect(() => {
        setComponent(null)
        setError(null)
        const loader = pages[page]
        if (!loader) {
            setError(`Unknown page: ${page}`)
            return
        }
        loader()
            .then(mod => setComponent(() => mod.default))
            .catch(err => setError(err.message))
    }, [page])

    if (error) return html`<div class="error">Error loading page: ${error}</div>`
    if (!Component) return html`<div class="loading">Loading...</div>`
    return html`<${Component} ...${params} />`
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

function App() {
    const { page, params } = useRoute()

    // Build breadcrumb based on current route
    const crumbs = [{ label: 'Experiments', href: '#/' }]
    if (page === 'scoreboard') crumbs.push({ label: 'Scoreboard' })
    else if (page === 'live') crumbs.push({ label: 'Live' })
    else if (page === 'compare') crumbs.push({ label: 'Compare' })
    else if (page === 'runDetail') crumbs.push({ label: params.runId })
    else if (page === 'taskHistory') crumbs.push({ label: `${params.task} History` })
    else if (page === 'taskDetail') {
        crumbs.push({ label: params.runId, href: `#/experiments/${encodeURIComponent(params.runId)}` })
        crumbs.push({ label: params.task })
    }

    return html`
        <${NavBar} currentPage=${page} />
        <${Breadcrumb} items=${crumbs} />
        <${PageLoader} page=${page} params=${params} />
    `
}

// Mount
const nav = document.getElementById('breadcrumb')
const app = document.getElementById('app')

render(html`<${App} />`, app)
// Clear the old breadcrumb — Preact now manages it inside #app
nav.style.display = 'none'
```

- [ ] **Step 4: Verify the shell loads in browser**

Run: `cd tools/dashboard && python server.py --experiments-dir ../../docs/experiments --host 0.0.0.0 --port 8080`

Open browser to the host. Verify:
- Nav bar shows: Experiments, Scoreboard, Live, Compare
- Page shows "Loading..." then "Error loading page" (pages not yet implemented)
- Clicking nav items changes the hash and breadcrumb updates
- No console errors from Preact/htm loading

- [ ] **Step 5: Commit**

```bash
git add tools/dashboard/static/index.html tools/dashboard/static/js/
git commit -m "feat(dashboard): Preact SPA shell with router, nav, and shared components"
```

---

### Task 6: Experiments Page

**Files:**
- Create: `tools/dashboard/static/js/experiments.js`

- [ ] **Step 1: Implement experiments page**

Create `tools/dashboard/static/js/experiments.js`:

```javascript
import { h } from 'https://esm.sh/preact@10.25.4'
import { useState, useEffect, useMemo } from 'https://esm.sh/preact@10.25.4/hooks'
import htm from 'https://esm.sh/htm@3.1.1'
import { fetchJSON, fmtScore, fmtDate, fmtModel, ScoreBar, FilterBar } from './components/shared.js'

const html = htm.bind(h)

function ExperimentsPage() {
    const [experiments, setExperiments] = useState(null)
    const [error, setError] = useState(null)
    const [typeFilter, setTypeFilter] = useState('all')
    const [sortCol, setSortCol] = useState('date')
    const [sortDir, setSortDir] = useState('desc')

    useEffect(() => {
        fetchJSON('/api/experiments')
            .then(setExperiments)
            .catch(e => setError(e.message))
    }, [])

    const filtered = useMemo(() => {
        if (!experiments) return []
        let list = [...experiments]
        if (typeFilter === 'wave')
            list = list.filter(e => e.run_id.startsWith('wave-'))
        else if (typeFilter === 'experiment')
            list = list.filter(e => !e.run_id.startsWith('wave-'))

        list.sort((a, b) => {
            let va = a[sortCol], vb = b[sortCol]
            if (typeof va === 'string') va = va.toLowerCase()
            if (typeof vb === 'string') vb = vb.toLowerCase()
            if (va < vb) return sortDir === 'asc' ? -1 : 1
            if (va > vb) return sortDir === 'asc' ? 1 : -1
            return 0
        })
        return list
    }, [experiments, typeFilter, sortCol, sortDir])

    function toggleSort(col) {
        if (sortCol === col) setSortDir(d => d === 'asc' ? 'desc' : 'asc')
        else { setSortCol(col); setSortDir('desc') }
    }

    function sortIndicator(col) {
        if (sortCol !== col) return ''
        return sortDir === 'asc' ? ' \u25B2' : ' \u25BC'
    }

    if (error) return html`<div class="error">Error: ${error}</div>`
    if (!experiments) return html`<div class="loading">Loading experiments...</div>`

    const waveCount = experiments.filter(e => e.run_id.startsWith('wave-')).length
    const expCount = experiments.length - waveCount

    return html`
        <div class="page-header">
            <h1>Experiments</h1>
            <div class="header-stats">
                <span>${experiments.length} runs</span>
                <span>${waveCount} waves</span>
                <span>${expCount} experiments</span>
            </div>
        </div>

        <${FilterBar}
            options=${[
                { value: 'all', label: 'All', count: experiments.length },
                { value: 'wave', label: 'Waves', count: waveCount },
                { value: 'experiment', label: 'Experiments', count: expCount },
            ]}
            active=${typeFilter}
            onSelect=${setTypeFilter}
        />

        <table class="data-table">
            <thead>
                <tr>
                    <th onClick=${() => toggleSort('date')}>Date${sortIndicator('date')}</th>
                    <th onClick=${() => toggleSort('variant')}>Variant${sortIndicator('variant')}</th>
                    <th onClick=${() => toggleSort('git_sha')}>SHA${sortIndicator('git_sha')}</th>
                    <th onClick=${() => toggleSort('model')}>Model${sortIndicator('model')}</th>
                    <th onClick=${() => toggleSort('mean_score')}>Mean${sortIndicator('mean_score')}</th>
                    <th onClick=${() => toggleSort('task_count')}>Tasks${sortIndicator('task_count')}</th>
                    <th onClick=${() => toggleSort('perfect_count')}>Perfect${sortIndicator('perfect_count')}</th>
                </tr>
            </thead>
            <tbody>
                ${filtered.map(exp => html`
                    <tr key=${exp.run_id} class="clickable-row"
                        onClick=${() => location.hash = `#/experiments/${encodeURIComponent(exp.run_id)}`}>
                        <td>${fmtDate(exp.date)}</td>
                        <td class="variant-cell" title=${exp.variant}>${exp.variant}</td>
                        <td class="mono">${exp.git_sha}</td>
                        <td>${fmtModel(exp.model)}</td>
                        <td><${ScoreBar} score=${exp.mean_score} /></td>
                        <td>${exp.task_count}</td>
                        <td>${exp.perfect_count}</td>
                    </tr>
                `)}
            </tbody>
        </table>
    `
}

export default ExperimentsPage
```

- [ ] **Step 2: Verify in browser**

Restart the dashboard server. Open browser. Verify:
- Experiments page loads and shows all 134+ runs
- Sorting works by clicking column headers
- Filter buttons work (All / Waves / Experiments)
- Clicking a row navigates to `#/experiments/{run_id}`

- [ ] **Step 3: Commit**

```bash
git add tools/dashboard/static/js/experiments.js
git commit -m "feat(dashboard): experiments list page with sorting and filtering"
```

---

### Task 7: Scoreboard Page

**Files:**
- Create: `tools/dashboard/static/js/scoreboard.js`

- [ ] **Step 1: Implement scoreboard page**

Create `tools/dashboard/static/js/scoreboard.js`:

```javascript
import { h } from 'https://esm.sh/preact@10.25.4'
import { useState, useEffect, useMemo } from 'https://esm.sh/preact@10.25.4/hooks'
import htm from 'https://esm.sh/htm@3.1.1'
import { fetchJSON, fmtScore, RepDots, FilterBar, StatCard } from './components/shared.js'

const html = htm.bind(h)

function scoreColor(score) {
    if (score >= 1.0) return '#2dd66a'
    if (score >= 0.5) return '#d4a020'
    if (score > 0) return '#e8a040'
    return '#e84444'
}

function ScoreboardPage() {
    const [scoreboard, setScoreboard] = useState(null)
    const [error, setError] = useState(null)
    const [filter, setFilter] = useState('all')

    useEffect(() => {
        const param = filter === 'all' ? '' : `?filter=${filter}`
        fetchJSON(`/api/scoreboard${param}`)
            .then(setScoreboard)
            .catch(e => setError(e.message))
    }, [filter])

    if (error) return html`<div class="error">Error: ${error}</div>`
    if (!scoreboard) return html`<div class="loading">Loading scoreboard...</div>`

    const tasks = scoreboard.tasks || {}
    const taskNames = Object.keys(tasks).sort()
    const solved = Object.values(tasks).filter(t => t.score >= 1.0).length
    const failing = Object.values(tasks).filter(t => t.score < 1.0).length
    const zero = Object.values(tasks).filter(t => t.score === 0).length

    return html`
        <div class="page-header">
            <h1>Scoreboard</h1>
            <div class="header-stats">
                <span>Mean: ${fmtScore(scoreboard.mean_score)}</span>
                <span>${scoreboard.tested_tasks}/${scoreboard.total_tasks} tested</span>
            </div>
        </div>

        <div class="stat-cards">
            <${StatCard} label="Solved" value=${solved} />
            <${StatCard} label="Failing" value=${failing} />
            <${StatCard} label="Zero Score" value=${zero} />
            <${StatCard} label="Mean" value=${fmtScore(scoreboard.mean_score)} />
        </div>

        <${FilterBar}
            options=${[
                { value: 'all', label: 'All', count: taskNames.length },
                { value: 'failing', label: 'Failing' },
                { value: 'solved', label: 'Solved' },
            ]}
            active=${filter}
            onSelect=${setFilter}
        />

        <table class="data-table scoreboard-table">
            <thead>
                <tr>
                    <th>Task</th>
                    <th>Score</th>
                    <th>Reps</th>
                    <th>Last Run</th>
                    <th>Date</th>
                </tr>
            </thead>
            <tbody>
                ${taskNames.map(name => {
                    const t = tasks[name]
                    return html`
                        <tr key=${name}>
                            <td>
                                <a href="#/experiments/tasks/${encodeURIComponent(name)}/history"
                                   class="task-link">${name}</a>
                            </td>
                            <td>
                                <span class="score-cell"
                                      style="color: ${scoreColor(t.score)}">
                                    ${fmtScore(t.score)}
                                </span>
                            </td>
                            <td><${RepDots} reps=${t.reps} /></td>
                            <td class="mono">
                                <a href="#/experiments/${encodeURIComponent(t.last_run)}">
                                    ${t.last_run ? t.last_run.substring(0, 20) : '—'}
                                </a>
                            </td>
                            <td>${t.last_date || '—'}</td>
                        </tr>
                    `
                })}
            </tbody>
        </table>
    `
}

export default ScoreboardPage
```

- [ ] **Step 2: Add task history route to router**

In `tools/dashboard/static/js/app.js`, add this route in the `parseHash` function, before the scoreboard match:

```javascript
    if ((m = hash.match(/^#\/experiments\/tasks\/([^/]+)\/history$/)))
        return { page: 'taskHistory', params: { task: decodeURIComponent(m[1]) } }
```

And add to the `pages` object:

```javascript
    taskHistory: () => import('./task-history.js'),
```

- [ ] **Step 3: Create task history page**

Create `tools/dashboard/static/js/task-history.js`:

```javascript
import { h } from 'https://esm.sh/preact@10.25.4'
import { useState, useEffect } from 'https://esm.sh/preact@10.25.4/hooks'
import htm from 'https://esm.sh/htm@3.1.1'
import { fetchJSON, fmtScore, fmtDate, RepDots, ScoreBar } from './components/shared.js'

const html = htm.bind(h)

function TaskHistoryPage({ task }) {
    const [history, setHistory] = useState(null)
    const [error, setError] = useState(null)

    useEffect(() => {
        fetchJSON(`/api/experiments/tasks/${encodeURIComponent(task)}/history`)
            .then(setHistory)
            .catch(e => setError(e.message))
    }, [task])

    if (error) return html`<div class="error">Error: ${error}</div>`
    if (!history) return html`<div class="loading">Loading history...</div>`

    return html`
        <div class="page-header">
            <h1>${task}</h1>
            <div class="header-stats">
                <span>${history.length} runs</span>
            </div>
        </div>

        <table class="data-table">
            <thead>
                <tr>
                    <th>Date</th>
                    <th>Run</th>
                    <th>SHA</th>
                    <th>Score</th>
                    <th>Reps</th>
                </tr>
            </thead>
            <tbody>
                ${history.map(h => html`
                    <tr key=${h.run_id}>
                        <td>${fmtDate(h.date)}</td>
                        <td>
                            <a href="#/experiments/${encodeURIComponent(h.run_id)}"
                               class="mono">${h.run_id}</a>
                        </td>
                        <td class="mono">${h.git_sha}</td>
                        <td><${ScoreBar} score=${h.score} /></td>
                        <td><${RepDots} reps=${h.reps} /></td>
                    </tr>
                `)}
            </tbody>
        </table>
    `
}

export default TaskHistoryPage
```

- [ ] **Step 4: Verify in browser**

Restart dashboard. Verify:
- Scoreboard page loads with 89 tasks
- Filter buttons work (All / Failing / Solved)
- Task names link to history pages
- Run IDs link to run detail
- Score colors are correct (green/yellow/red)

- [ ] **Step 5: Commit**

```bash
git add tools/dashboard/static/js/scoreboard.js tools/dashboard/static/js/task-history.js tools/dashboard/static/js/app.js
git commit -m "feat(dashboard): interactive scoreboard and task history pages"
```

---

### Task 8: Run Detail Page

**Files:**
- Create: `tools/dashboard/static/js/run-detail.js`

- [ ] **Step 1: Implement run detail page**

Create `tools/dashboard/static/js/run-detail.js`:

```javascript
import { h } from 'https://esm.sh/preact@10.25.4'
import { useState, useEffect, useMemo } from 'https://esm.sh/preact@10.25.4/hooks'
import htm from 'https://esm.sh/htm@3.1.1'
import { fetchJSON, fmtScore, fmtDate, fmtModel, ScoreBar, RepDots,
         StatusBadge, StatCard, FilterBar } from './components/shared.js'

const html = htm.bind(h)

function RunDetailPage({ runId, legacy }) {
    const [experiment, setExperiment] = useState(null)
    const [error, setError] = useState(null)
    const [statusFilter, setStatusFilter] = useState('all')
    const [sortCol, setSortCol] = useState('task')
    const [sortDir, setSortDir] = useState('asc')

    useEffect(() => {
        fetchJSON(`/api/experiments/${encodeURIComponent(runId)}`)
            .then(setExperiment)
            .catch(e => setError(e.message))
    }, [runId])

    const tasks = useMemo(() => {
        if (!experiment || !experiment.results) return []
        let list = Object.entries(experiment.results).map(([name, r]) => ({
            name, ...r,
            status: r.score >= 1.0 ? 'pass' : r.score > 0 ? 'partial' : 'fail',
        }))

        if (statusFilter === 'pass') list = list.filter(t => t.score >= 1.0)
        else if (statusFilter === 'fail') list = list.filter(t => t.score < 1.0)

        list.sort((a, b) => {
            const va = sortCol === 'task' ? a.name : a[sortCol]
            const vb = sortCol === 'task' ? b.name : b[sortCol]
            if (va < vb) return sortDir === 'asc' ? -1 : 1
            if (va > vb) return sortDir === 'asc' ? 1 : -1
            return 0
        })
        return list
    }, [experiment, statusFilter, sortCol, sortDir])

    function toggleSort(col) {
        if (sortCol === col) setSortDir(d => d === 'asc' ? 'desc' : 'asc')
        else { setSortCol(col); setSortDir(col === 'task' ? 'asc' : 'desc') }
    }

    if (error) return html`<div class="error">Error: ${error}</div>`
    if (!experiment) return html`<div class="loading">Loading...</div>`

    const results = experiment.results || {}
    const taskCount = Object.keys(results).length
    const passed = Object.values(results).filter(r => r.score >= 1.0).length
    const failed = taskCount - passed

    return html`
        <div class="page-header">
            <h1>${experiment.variant || runId}</h1>
            <div class="header-meta">
                <span class="mono">${experiment.git_sha}</span>
                <span>${fmtDate(experiment.date)}</span>
                <span>${fmtModel(experiment.model)}</span>
            </div>
        </div>

        <div class="stat-cards">
            <${StatCard} label="Mean Score" value=${fmtScore(experiment.mean_score)} />
            <${StatCard} label="Passed" value=${passed} sub=${`of ${taskCount}`} />
            <${StatCard} label="Failed" value=${failed} />
            <${StatCard} label="Perfect (3/3)" value=${experiment.perfect_count} />
        </div>

        <${FilterBar}
            options=${[
                { value: 'all', label: 'All', count: taskCount },
                { value: 'pass', label: 'Pass', count: passed },
                { value: 'fail', label: 'Fail', count: failed },
            ]}
            active=${statusFilter}
            onSelect=${setStatusFilter}
        />

        <table class="data-table">
            <thead>
                <tr>
                    <th onClick=${() => toggleSort('task')}>Task</th>
                    <th onClick=${() => toggleSort('score')}>Score</th>
                    <th>Reps</th>
                </tr>
            </thead>
            <tbody>
                ${tasks.map(t => html`
                    <tr key=${t.name} class="clickable-row"
                        onClick=${() => location.hash = `#/experiments/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(t.name)}`}>
                        <td>${t.name}</td>
                        <td><${ScoreBar} score=${t.score} /></td>
                        <td><${RepDots} reps=${t.reps} /></td>
                    </tr>
                `)}
            </tbody>
        </table>
    `
}

export default RunDetailPage
```

- [ ] **Step 2: Verify in browser**

Navigate to an experiment. Verify:
- Header shows variant, SHA, date, model
- Stat cards show mean, passed, failed, perfect
- Task table is sortable
- Filter buttons work
- Clicking a task navigates to task detail

- [ ] **Step 3: Commit**

```bash
git add tools/dashboard/static/js/run-detail.js
git commit -m "feat(dashboard): run detail page with experiment metadata"
```

---

### Task 9: Task Detail Page

**Files:**
- Create: `tools/dashboard/static/js/task-detail.js`

This is the most complex page — it needs to show trajectories, session trees, and timing data. For the initial version, it shows experiment-level data (scores, reps, history sidebar). Full transcript viewing is available when harbor data exists via the existing RunStore routes.

- [ ] **Step 1: Implement task detail page**

Create `tools/dashboard/static/js/task-detail.js`:

```javascript
import { h } from 'https://esm.sh/preact@10.25.4'
import { useState, useEffect } from 'https://esm.sh/preact@10.25.4/hooks'
import htm from 'https://esm.sh/htm@3.1.1'
import { fetchJSON, fmtScore, fmtDate, fmtWallTime, fmtTokens,
         RepDots, ScoreBar, StatCard } from './components/shared.js'

const html = htm.bind(h)

const ACTION_COLORS = {
    EXPLORE: '#4898f0', EDIT: '#d4a020', EXEC: '#888',
    SPAWN: '#b050b8', SUBMIT: '#2dd66a', REVIEW: '#e8a040',
    TASK: '#20b2aa', ERROR: '#e84444', PLAN: '#6a5acd', TOOL: '#999',
}

function TrajectoryRound({ round, expanded, onToggle }) {
    const color = ACTION_COLORS[round.action] || '#999'
    return html`
        <div class="trajectory-round">
            <div class="round-header" onClick=${onToggle}>
                <span class="round-num">${round.round}</span>
                <span class="round-action" style="color:${color}">${round.action}</span>
                <span class="round-summary">${round.summary}</span>
                <span class="round-meta">
                    ${round.tool_names ? `${round.tool_names.length} tools` : ''}
                    ${round.usage ? ` | ${fmtTokens(round.usage.input_tokens)}in` : ''}
                </span>
            </div>
            ${expanded && html`
                <div class="round-detail">
                    ${round.text && html`<pre class="round-text">${round.text.substring(0, 2000)}</pre>`}
                    ${round.tool_calls && round.tool_calls.map((tc, i) => html`
                        <div key=${i} class="tool-call-block">
                            <div class="tool-name">${tc.name}</div>
                            ${tc.args && html`<pre class="tool-args">${JSON.stringify(tc.args, null, 2).substring(0, 500)}</pre>`}
                            ${tc.result && html`<pre class="tool-result">${String(tc.result).substring(0, 500)}</pre>`}
                        </div>
                    `)}
                </div>
            `}
        </div>
    `
}

function SessionTree({ sessions }) {
    const [expandedRounds, setExpandedRounds] = useState({})

    function toggleRound(sessionId, roundNum) {
        const key = `${sessionId}-${roundNum}`
        setExpandedRounds(prev => ({ ...prev, [key]: !prev[key] }))
    }

    return html`
        <div class="session-tree">
            ${sessions.map(session => html`
                <div key=${session.session_id}
                     class="session-block"
                     style="margin-left: ${(session.depth || 0) * 24}px; border-left: 3px solid ${session.depth ? '#b050b8' : '#4898f0'}">
                    <div class="session-header">
                        <span class="session-id">${session.session_id.substring(0, 8)}</span>
                        <span class="session-model">${session.model || ''}</span>
                        <span class="session-rounds">${session.trajectory?.length || 0} rounds</span>
                    </div>
                    ${(session.trajectory || []).map(round => html`
                        <${TrajectoryRound} key=${round.round}
                            round=${round}
                            expanded=${expandedRounds[`${session.session_id}-${round.round}`]}
                            onToggle=${() => toggleRound(session.session_id, round.round)}
                        />
                    `)}
                    ${(session.children || []).map(child => html`
                        <${SessionTree} key=${child.session_id} sessions=${[child]} />
                    `)}
                </div>
            `)}
        </div>
    `
}

function TaskDetailPage({ runId, task }) {
    const [experiment, setExperiment] = useState(null)
    const [harborData, setHarborData] = useState(null)
    const [history, setHistory] = useState(null)
    const [error, setError] = useState(null)

    useEffect(() => {
        // Fetch experiment-level data for this task
        fetchJSON(`/api/experiments/${encodeURIComponent(runId)}`)
            .then(setExperiment)
            .catch(e => setError(e.message))

        // Fetch task history
        fetchJSON(`/api/experiments/tasks/${encodeURIComponent(task)}/history`)
            .then(setHistory)
            .catch(() => {})  // non-critical

        // Try to fetch harbor detail (may 404 if not cached locally)
        fetchJSON(`/api/runs/${encodeURIComponent(runId)}/tasks/${encodeURIComponent(task)}`)
            .then(setHarborData)
            .catch(() => {})  // expected if no harbor data
    }, [runId, task])

    if (error) return html`<div class="error">Error: ${error}</div>`
    if (!experiment) return html`<div class="loading">Loading...</div>`

    const taskResult = experiment.results?.[task]

    return html`
        <div class="page-header">
            <h1>${task}</h1>
            <div class="header-meta">
                <span>Run: ${runId}</span>
                <span>${fmtDate(experiment.date)}</span>
            </div>
        </div>

        <div class="task-detail-layout">
            <div class="task-main">
                ${taskResult && html`
                    <div class="stat-cards">
                        <${StatCard} label="Score" value=${fmtScore(taskResult.score)} />
                        <${StatCard} label="Reps" value=${`${taskResult.reps_pass}/${taskResult.reps_total}`} />
                        ${harborData && html`
                            <${StatCard} label="Wall Time" value=${fmtWallTime(harborData.wall_time_sec)} />
                            <${StatCard} label="Rounds" value=${harborData.total_rounds || '—'} />
                            <${StatCard} label="Tokens" value=${fmtTokens((harborData.total_tokens_in || 0) + (harborData.total_tokens_out || 0))} />
                        `}
                    </div>

                    <div class="section">
                        <h3>Reps</h3>
                        <${RepDots} reps=${taskResult.reps} />
                    </div>
                `}

                ${harborData && harborData.trajectory && html`
                    <div class="section">
                        <h3>Trajectory</h3>
                        <${SessionTree} sessions=${harborData.trajectory} />
                    </div>
                `}

                ${harborData && harborData.test_output && html`
                    <details class="section">
                        <summary><h3 style="display:inline">Verifier Output</h3></summary>
                        <pre class="verifier-output">${harborData.test_output}</pre>
                    </details>
                `}

                ${!harborData && html`
                    <div class="section info-box">
                        No harbor data available locally. Transcript viewing requires
                        collecting results with <code>collect_results.py</code> first.
                    </div>
                `}
            </div>

            ${history && history.length > 0 && html`
                <aside class="task-history-sidebar">
                    <h3>History</h3>
                    <div class="history-list">
                        ${history.slice(0, 15).map(h => html`
                            <a key=${h.run_id} class="history-entry ${h.run_id === runId ? 'current' : ''}"
                               href="#/experiments/${encodeURIComponent(h.run_id)}/tasks/${encodeURIComponent(task)}">
                                <span class="history-date">${fmtDate(h.date)}</span>
                                <${ScoreBar} score=${h.score} width=${60} />
                            </a>
                        `)}
                    </div>
                </aside>
            `}
        </div>
    `
}

export default TaskDetailPage
```

- [ ] **Step 2: Verify in browser**

Navigate to an experiment, click a task. Verify:
- Score and rep information displays correctly
- History sidebar shows (when task JSON exists)
- If harbor data is cached locally, trajectory renders with expandable rounds
- If no harbor data, info box displays

- [ ] **Step 3: Commit**

```bash
git add tools/dashboard/static/js/task-detail.js
git commit -m "feat(dashboard): task detail page with trajectory and history sidebar"
```

---

### Task 10: Compare Page

**Files:**
- Create: `tools/dashboard/static/js/compare.js`

- [ ] **Step 1: Implement compare page**

Create `tools/dashboard/static/js/compare.js`:

```javascript
import { h } from 'https://esm.sh/preact@10.25.4'
import { useState, useEffect } from 'https://esm.sh/preact@10.25.4/hooks'
import htm from 'https://esm.sh/htm@3.1.1'
import { fetchJSON, fmtScore, RepDots, StatCard } from './components/shared.js'

const html = htm.bind(h)

function ComparePage() {
    const [experiments, setExperiments] = useState(null)
    const [runA, setRunA] = useState('')
    const [runB, setRunB] = useState('')
    const [comparison, setComparison] = useState(null)
    const [error, setError] = useState(null)
    const [loading, setLoading] = useState(false)

    useEffect(() => {
        fetchJSON('/api/experiments').then(setExperiments).catch(() => {})
    }, [])

    function doCompare() {
        if (!runA || !runB) return
        setLoading(true)
        setError(null)

        // Fetch both experiments and compute comparison client-side
        Promise.all([
            fetchJSON(`/api/experiments/${encodeURIComponent(runA)}`),
            fetchJSON(`/api/experiments/${encodeURIComponent(runB)}`),
        ]).then(([a, b]) => {
            const resultsA = a.results || {}
            const resultsB = b.results || {}
            const allTasks = [...new Set([...Object.keys(resultsA), ...Object.keys(resultsB)])].sort()

            const improved = [], regressed = [], stablePass = [], stableFail = [], onlyA = [], onlyB = []

            for (const task of allTasks) {
                const inA = task in resultsA
                const inB = task in resultsB
                if (inA && !inB) { onlyA.push({ task, a: resultsA[task] }); continue }
                if (inB && !inA) { onlyB.push({ task, b: resultsB[task] }); continue }

                const sa = resultsA[task], sb = resultsB[task]
                const passA = sa.score >= 0.5, passB = sb.score >= 0.5

                const entry = { task, a: sa, b: sb }
                if (!passA && passB) improved.push(entry)
                else if (passA && !passB) regressed.push(entry)
                else if (passA && passB) stablePass.push(entry)
                else stableFail.push(entry)
            }

            setComparison({ a, b, improved, regressed, stablePass, stableFail, onlyA, onlyB })
            setLoading(false)
        }).catch(e => { setError(e.message); setLoading(false) })
    }

    return html`
        <div class="page-header"><h1>Compare Runs</h1></div>

        <div class="compare-selectors">
            <div class="selector">
                <label>Run A (baseline)</label>
                <select value=${runA} onChange=${e => setRunA(e.target.value)}>
                    <option value="">Select...</option>
                    ${(experiments || []).map(exp => html`
                        <option key=${exp.run_id} value=${exp.run_id}>
                            ${exp.date} — ${exp.variant} (${fmtScore(exp.mean_score)})
                        </option>
                    `)}
                </select>
            </div>
            <div class="selector">
                <label>Run B (candidate)</label>
                <select value=${runB} onChange=${e => setRunB(e.target.value)}>
                    <option value="">Select...</option>
                    ${(experiments || []).map(exp => html`
                        <option key=${exp.run_id} value=${exp.run_id}>
                            ${exp.date} — ${exp.variant} (${fmtScore(exp.mean_score)})
                        </option>
                    `)}
                </select>
            </div>
            <button class="compare-btn" onClick=${doCompare}
                    disabled=${!runA || !runB || loading}>
                ${loading ? 'Comparing...' : 'Compare'}
            </button>
        </div>

        ${error && html`<div class="error">${error}</div>`}

        ${comparison && html`
            <div class="stat-cards">
                <${StatCard} label="Improved" value=${comparison.improved.length} />
                <${StatCard} label="Regressed" value=${comparison.regressed.length} />
                <${StatCard} label="Stable Pass" value=${comparison.stablePass.length} />
                <${StatCard} label="Stable Fail" value=${comparison.stableFail.length} />
            </div>

            ${comparison.improved.length > 0 && html`
                <div class="section">
                    <h3 class="compare-improved">Improved (${comparison.improved.length})</h3>
                    <${CompareTable} items=${comparison.improved} />
                </div>
            `}

            ${comparison.regressed.length > 0 && html`
                <div class="section">
                    <h3 class="compare-regressed">Regressed (${comparison.regressed.length})</h3>
                    <${CompareTable} items=${comparison.regressed} />
                </div>
            `}

            ${comparison.stableFail.length > 0 && html`
                <div class="section">
                    <h3>Stable Fail (${comparison.stableFail.length})</h3>
                    <${CompareTable} items=${comparison.stableFail} />
                </div>
            `}

            ${comparison.stablePass.length > 0 && html`
                <details class="section">
                    <summary><h3 style="display:inline">Stable Pass (${comparison.stablePass.length})</h3></summary>
                    <${CompareTable} items=${comparison.stablePass} />
                </details>
            `}
        `}
    `
}

function CompareTable({ items }) {
    return html`
        <table class="data-table compare-table">
            <thead>
                <tr><th>Task</th><th>Run A</th><th>Run B</th></tr>
            </thead>
            <tbody>
                ${items.map(({ task, a, b }) => html`
                    <tr key=${task}>
                        <td>${task}</td>
                        <td>
                            ${a ? html`<span>${fmtScore(a.score)} <${RepDots} reps=${a.reps} /></span>` : '—'}
                        </td>
                        <td>
                            ${b ? html`<span>${fmtScore(b.score)} <${RepDots} reps=${b.reps} /></span>` : '—'}
                        </td>
                    </tr>
                `)}
            </tbody>
        </table>
    `
}

export default ComparePage
```

- [ ] **Step 2: Verify in browser**

Navigate to Compare. Verify:
- Dropdowns populate with experiments
- Clicking Compare shows categorized results
- Improved/Regressed sections show with correct data
- Rep dots display per-rep detail

- [ ] **Step 3: Commit**

```bash
git add tools/dashboard/static/js/compare.js
git commit -m "feat(dashboard): compare page with side-by-side run analysis"
```

---

### Task 11: Live Monitor Page

**Files:**
- Create: `tools/dashboard/static/js/live.js`

- [ ] **Step 1: Implement live monitor page**

Create `tools/dashboard/static/js/live.js`:

```javascript
import { h } from 'https://esm.sh/preact@10.25.4'
import { useState, useEffect, useRef } from 'https://esm.sh/preact@10.25.4/hooks'
import htm from 'https://esm.sh/htm@3.1.1'
import { fetchJSON, fmtScore, StatCard } from './components/shared.js'

const html = htm.bind(h)

function taskColor(reps) {
    if (!reps || Object.keys(reps).length === 0) return '#444'  // pending
    const vals = Object.values(reps).filter(v => v !== null)
    if (vals.length === 0) return '#4898f0'  // in progress
    const mean = vals.reduce((a, b) => a + b, 0) / vals.length
    if (mean >= 1.0) return '#2dd66a'
    if (mean > 0) return '#d4a020'
    return '#e84444'
}

function LiveMonitorPage() {
    const [waves, setWaves] = useState(null)
    const [selectedWave, setSelectedWave] = useState(null)
    const [scores, setScores] = useState(null)
    const [summary, setSummary] = useState(null)
    const [error, setError] = useState(null)
    const eventSourceRef = useRef(null)

    // Discover waves
    useEffect(() => {
        fetchJSON('/api/live/waves')
            .then(w => {
                setWaves(w)
                if (w.length > 0 && !selectedWave) setSelectedWave(w[0].run_id)
            })
            .catch(e => setError(e.message))
    }, [])

    // Connect SSE when wave selected
    useEffect(() => {
        if (!selectedWave) return

        // Initial fetch
        fetchJSON(`/api/live/waves/${encodeURIComponent(selectedWave)}`)
            .then(data => { setScores(data.scores); setSummary(data.summary) })
            .catch(() => {})

        // SSE stream
        const es = new EventSource(`/api/live/waves/${encodeURIComponent(selectedWave)}/stream`)

        es.addEventListener('wave_progress', e => {
            setSummary(JSON.parse(e.data))
        })

        es.addEventListener('task_complete', e => {
            const { task, rep, reward } = JSON.parse(e.data)
            setScores(prev => {
                const next = { ...prev }
                next[task] = { ...(next[task] || {}), [rep]: reward }
                return next
            })
        })

        es.onerror = () => {
            // SSE auto-reconnects; just log
            console.warn('SSE connection error, reconnecting...')
        }

        eventSourceRef.current = es
        return () => es.close()
    }, [selectedWave])

    if (error) return html`<div class="error">Error: ${error}</div>`

    return html`
        <div class="page-header">
            <h1>Live Monitor</h1>
            ${waves && waves.length > 0 && html`
                <select class="wave-selector" value=${selectedWave}
                        onChange=${e => setSelectedWave(e.target.value)}>
                    ${waves.map(w => html`
                        <option key=${w.run_id} value=${w.run_id}>
                            ${w.run_id} — ${w.variant || ''}
                        </option>
                    `)}
                </select>
            `}
        </div>

        ${!waves || waves.length === 0
            ? html`<div class="info-box">No active waves found in .serf-launches/</div>`
            : html`
                ${summary && html`
                    <div class="stat-cards">
                        <${StatCard} label="Completed" value=${`${summary.completed}/${summary.total}`} />
                        <${StatCard} label="Mean Score" value=${fmtScore(summary.mean)} />
                        <${StatCard} label="Perfect" value=${summary.perfect_count} />
                    </div>
                `}

                ${scores && html`
                    <div class="task-grid">
                        ${Object.entries(scores).sort(([a], [b]) => a.localeCompare(b)).map(([task, reps]) => {
                            const vals = Object.values(reps).filter(v => v !== null)
                            const mean = vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : null
                            return html`
                                <div key=${task} class="grid-cell"
                                     style="background: ${taskColor(reps)}"
                                     title="${task}: ${mean != null ? fmtScore(mean) : 'pending'}">
                                    <span class="grid-label">${task.substring(0, 20)}</span>
                                </div>
                            `
                        })}
                    </div>
                `}
            `
        }
    `
}

export default LiveMonitorPage
```

- [ ] **Step 2: Verify in browser**

Start the dashboard with `--launches-dir ../../.serf-launches`. Navigate to Live. Verify:
- Wave selector shows discovered waves
- Task grid renders with color-coded cells
- Stat cards update (may need a running wave to test SSE)
- Hovering cells shows task name and score

- [ ] **Step 3: Commit**

```bash
git add tools/dashboard/static/js/live.js
git commit -m "feat(dashboard): live wave monitor with SSE real-time updates"
```

---

### Task 12: CSS Extensions

**Files:**
- Modify: `tools/dashboard/static/style.css`

- [ ] **Step 1: Add new styles**

Append to the end of `tools/dashboard/static/style.css`:

```css
/* ==================================================================
   New styles for Preact SPA
   ================================================================== */

/* Nav bar */
.nav-bar {
    display: flex;
    gap: 2px;
    padding: 8px 24px;
    background: #1A1A1A;
    border-bottom: 1px solid #333;
}
.nav-item {
    padding: 8px 16px;
    color: #A0A0A0;
    text-decoration: none;
    font-size: 14px;
    border-radius: 4px;
    transition: color 0.15s;
}
.nav-item:hover { color: #F0F0F0; }
.nav-item.active { color: #F0F0F0; background: #333; }

/* Breadcrumb override for Preact */
.breadcrumb-items {
    padding: 8px 24px;
    font-size: 13px;
    color: #6B6B6B;
}
.breadcrumb-items a { color: #4898f0; text-decoration: none; }
.breadcrumb-items .sep { margin: 0 6px; color: #999; }
.breadcrumb-items .current { color: #333; }

/* Header stats */
.header-stats { display: flex; gap: 16px; color: #6B6B6B; font-size: 13px; margin-top: 4px; }
.header-meta { display: flex; gap: 12px; color: #6B6B6B; font-size: 13px; margin-top: 4px; }

/* Stat cards row */
.stat-cards { display: flex; gap: 12px; margin: 16px 0; flex-wrap: wrap; }

/* Sortable table */
.data-table th { cursor: pointer; user-select: none; }
.data-table th:hover { background: #E8E8E4; }
.clickable-row { cursor: pointer; }
.clickable-row:hover { background: #F0F0EC; }

/* Variant cell truncation */
.variant-cell { max-width: 300px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* Monospace */
.mono { font-family: 'SF Mono', 'Cascadia Code', 'Fira Code', monospace; font-size: 12px; }

/* Scoreboard */
.scoreboard-table .score-cell { font-weight: 600; font-family: monospace; }
.task-link { color: #4898f0; text-decoration: none; }
.task-link:hover { text-decoration: underline; }

/* Compare */
.compare-selectors { display: flex; gap: 16px; align-items: end; margin: 16px 0; flex-wrap: wrap; }
.compare-selectors .selector { display: flex; flex-direction: column; gap: 4px; }
.compare-selectors label { font-size: 12px; color: #6B6B6B; }
.compare-selectors select { padding: 8px; border: 1px solid #ccc; border-radius: 4px; min-width: 300px; }
.compare-btn { padding: 8px 24px; background: #1A1A1A; color: white; border: none;
               border-radius: 4px; cursor: pointer; font-size: 14px; }
.compare-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.compare-improved { color: #2dd66a; }
.compare-regressed { color: #e84444; }
.compare-table td:nth-child(2), .compare-table td:nth-child(3) { font-family: monospace; }

/* Task detail layout */
.task-detail-layout { display: grid; grid-template-columns: 1fr 240px; gap: 24px; }
@media (max-width: 900px) { .task-detail-layout { grid-template-columns: 1fr; } }

/* History sidebar */
.task-history-sidebar { border-left: 1px solid #ddd; padding-left: 16px; }
.task-history-sidebar h3 { font-size: 14px; margin: 0 0 12px; }
.history-list { display: flex; flex-direction: column; gap: 4px; }
.history-entry { display: flex; align-items: center; gap: 8px; padding: 4px 8px;
                 border-radius: 4px; text-decoration: none; color: inherit; font-size: 12px; }
.history-entry:hover { background: #F0F0EC; }
.history-entry.current { background: #E8E8E4; font-weight: 600; }

/* Trajectory */
.session-tree { margin: 8px 0; }
.session-block { margin: 4px 0; padding-left: 12px; }
.session-header { display: flex; gap: 12px; padding: 4px 0; font-size: 12px; color: #6B6B6B; }
.trajectory-round { margin: 2px 0; }
.round-header { display: flex; gap: 8px; padding: 6px 8px; cursor: pointer; border-radius: 4px;
                font-size: 13px; align-items: center; }
.round-header:hover { background: #F0F0EC; }
.round-num { width: 24px; text-align: right; color: #999; font-size: 11px; }
.round-action { font-weight: 600; width: 60px; font-size: 12px; }
.round-summary { flex: 1; color: #444; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.round-meta { color: #999; font-size: 11px; }
.round-detail { padding: 8px 8px 8px 40px; background: #FAFAF8; border-radius: 0 0 4px 4px; }
.round-text { font-size: 12px; white-space: pre-wrap; max-height: 200px; overflow: auto; }
.tool-call-block { margin: 8px 0; padding: 8px; background: white; border: 1px solid #eee; border-radius: 4px; }
.tool-name { font-weight: 600; font-size: 12px; color: #333; }
.tool-args, .tool-result { font-size: 11px; max-height: 150px; overflow: auto; margin-top: 4px; }

/* Info box */
.info-box { padding: 16px; background: #F0F0EC; border-radius: 6px; color: #666; font-size: 14px; }

/* Live monitor */
.wave-selector { padding: 6px 12px; border: 1px solid #ccc; border-radius: 4px; }
.task-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr)); gap: 4px; margin: 16px 0; }
.grid-cell { padding: 8px; border-radius: 4px; color: white; font-size: 11px;
             text-align: center; min-height: 40px; display: flex; align-items: center;
             justify-content: center; cursor: default; }
.grid-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* Loading / error */
.loading { padding: 48px; text-align: center; color: #A0A0A0; }
.error { padding: 16px; background: #fde8e8; color: #e84444; border-radius: 6px; margin: 16px 0; }

/* Rep dots inline */
.rep-dots { display: inline-flex; gap: 3px; }
```

- [ ] **Step 2: Verify visual consistency in browser**

Check all pages load with correct styling. Verify no visual regressions.

- [ ] **Step 3: Commit**

```bash
git add tools/dashboard/static/style.css
git commit -m "feat(dashboard): CSS extensions for Preact SPA pages"
```

---

### Task 13: Integration Test and Final Wiring

**Files:**
- Modify: `tools/dashboard/server.py` (verify all module loading)
- Verify: all existing tests still pass

- [ ] **Step 1: Run the full test suite**

Run: `cd tools/dashboard && python -m pytest -v`
Expected: All tests PASS (existing + new)

- [ ] **Step 2: Start the dashboard with all data sources and verify manually**

Run:
```bash
cd tools/dashboard && python server.py \
    --experiments-dir ../../docs/experiments \
    --launches-dir ../../.serf-launches \
    --host 0.0.0.0 --port 8080
```

Verify each page:
1. **Experiments** — 134+ runs load, sorting and filtering works
2. **Scoreboard** — 89 tasks, color-coded scores, filters work
3. **Run Detail** — click a wave, see tasks with scores and reps
4. **Task Detail** — click a task, see score + history sidebar
5. **Compare** — select two runs, see categorized differences
6. **Live** — waves discovered (if any active), grid renders

- [ ] **Step 3: Verify existing tools are unaffected**

Run these to confirm nothing is broken:
```bash
./tools/scoreboard.py --failing
./tools/wave_scores.py --help
```

- [ ] **Step 4: Commit final state**

```bash
git add -A  # after reviewing git status
git commit -m "feat(dashboard): experiment dashboard v1 — complete Preact SPA"
```

---

## Post-Implementation Notes

**What's included in v1:**
- ExperimentStore reading all git-tracked JSON
- S3Client for on-demand data fetching
- LiveStore with S3 polling and SSE streaming
- 6 Preact pages: Experiments, Scoreboard, Live, Compare, Run Detail, Task Detail
- Task history sidebar on task detail
- Trajectory viewer (when harbor data available)
- All existing tools and routes untouched

**Future enhancements (not in scope):**
- SSH proxy to EC2 instances for deep container monitoring
- Filesystem watcher for auto-reload of ExperimentStore (currently requires restart or manual reload)
- Score trend charts (sparklines or time-series graphs)
- Statistical significance display in comparison (McNemar, bootstrap CI)
- Keyboard shortcuts for navigation
