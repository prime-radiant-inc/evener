# Eval Dashboard Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a live FastAPI dashboard for eval results with trajectory-based transcript viewing and a markdown-first API.

**Architecture:** Single FastAPI process reads eval data from disk (harbor job directories). Returns markdown by default, JSON when the frontend requests it via `Accept: application/json`. Vanilla JS SPA frontend — no build step, no npm.

**Tech Stack:** Python 3.12, FastAPI, uvicorn. Vanilla HTML/CSS/JS for frontend. pytest for tests.

**Design doc:** `docs/plans/2026-03-01-eval-dashboard-design.md`

---

### Task 1: Project skeleton and test fixtures

Set up the directory structure, install dependencies, create test fixtures, and verify the skeleton runs.

**Files:**
- Create: `tools/dashboard/__init__.py`
- Create: `tools/dashboard/server.py`
- Create: `tools/dashboard/data.py`
- Create: `tools/dashboard/trajectory.py`
- Create: `tools/dashboard/markdown_render.py`
- Create: `tools/dashboard/static/index.html`
- Create: `tools/dashboard/static/style.css`
- Create: `tools/dashboard/static/app.js`
- Create: `tools/dashboard/test_data.py`
- Create: `tools/dashboard/test_trajectory.py`
- Create: `tools/dashboard/test_markdown.py`
- Create: `tools/dashboard/test_server.py`
- Create: `tools/dashboard/conftest.py`

**Step 1: Create the directory structure**

```bash
mkdir -p tools/dashboard/static
touch tools/dashboard/__init__.py
```

**Step 2: Create conftest.py with test fixtures**

The fixtures create a temporary harbor-style job directory with two tasks (one pass, one fail), each with transcript JSONL files. This is the foundation for all tests.

```python
"""Shared test fixtures for dashboard tests."""

import json
import os
import pytest


@pytest.fixture
def harbor_job_dir(tmp_path):
    """Create a harbor-style job directory with two tasks.

    Structure:
        full-test/full-test/
            build-widget__abc123/
                verifier/reward.txt (1.0 - pass)
                agent/serf-state/sessions/sess-main.transcript.jsonl
                agent/serf-state/sessions/sess-reviewer.transcript.jsonl
                agent/serf-state/api.jsonl
                agent/command-0/stdout.txt
                result.json
            fix-bug__def456/
                verifier/reward.txt (0.0 - fail)
                agent/serf-state/sessions/sess-main2.transcript.jsonl
                agent/command-0/stdout.txt
                result.json
    """
    job_root = tmp_path / "full-test" / "full-test"

    # --- Task 1: build-widget (PASS) ---
    t1 = job_root / "build-widget__abc123"
    _make_task(t1, reward=1.0, transcript_entries=_passing_transcript())

    # --- Task 2: fix-bug (FAIL, wrong answer) ---
    t2 = job_root / "fix-bug__def456"
    _make_task(t2, reward=0.0, transcript_entries=_failing_transcript(),
               agent_stdout="[submit_result] submitted\n")

    return tmp_path / "full-test"


def _make_task(task_dir, reward, transcript_entries, agent_stdout=""):
    """Create a single task directory with required files."""
    # Verifier
    verifier = task_dir / "verifier"
    verifier.mkdir(parents=True)
    (verifier / "reward.txt").write_text(str(reward))
    if reward < 1.0:
        (verifier / "test-stdout.txt").write_text(
            "FAIL: test_widget (expected 42, got 0)\n"
        )

    # Sessions
    sessions = task_dir / "agent" / "serf-state" / "sessions"
    sessions.mkdir(parents=True)

    # Write transcript
    tf = sessions / "sess-main.transcript.jsonl"
    lines = [json.dumps(e) for e in transcript_entries]
    tf.write_text("\n".join(lines) + "\n")

    # Agent stdout
    cmd_dir = task_dir / "agent" / "command-0"
    cmd_dir.mkdir(parents=True)
    (cmd_dir / "stdout.txt").write_text(agent_stdout)

    # result.json
    (task_dir / "result.json").write_text(json.dumps({
        "config": {"model": "gpt-5.3-codex"},
    }))


def _passing_transcript():
    """Transcript entries for a passing task: explore, edit, exec, submit."""
    return [
        {"kind": "header", "format_version": 1, "session_id": "sess-main",
         "created_at": "2026-03-01T12:00:00Z", "model": "gpt-5.3-codex",
         "profile_id": "openai", "depth": 0},
        {"kind": "entry", "seq": 0, "turn": {
            "kind": "USER_INPUT",
            "message": {"role": "user", "content": [
                {"kind": "text", "text": "Build a widget that returns 42."}
            ]},
            "timestamp": "2026-03-01T12:00:01Z",
        }},
        {"kind": "entry", "seq": 1, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "text", "text": "I'll start by reading the workspace."},
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-1", "name": "glob",
                    "arguments": "{\"pattern\": \"*\"}",
                }},
            ]},
            "timestamp": "2026-03-01T12:00:02Z",
            "usage": {"input_tokens": 500, "output_tokens": 30},
        }},
        {"kind": "entry", "seq": 2, "turn": {
            "kind": "TOOL_RESULTS",
            "message": {"role": "tool", "content": [
                {"kind": "tool_result", "tool_result": {
                    "tool_call_id": "tc-1", "name": "glob",
                    "content": "main.py\ntest_main.py", "is_error": False,
                }},
            ]},
        }},
        {"kind": "entry", "seq": 3, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-2", "name": "apply_patch",
                    "arguments": "{\"patch\": \"--- a/main.py\\n+++ b/main.py\\n@@ -1 +1 @@\\n-pass\\n+def widget(): return 42\"}",
                }},
            ]},
            "timestamp": "2026-03-01T12:00:03Z",
            "usage": {"input_tokens": 600, "output_tokens": 40},
        }},
        {"kind": "entry", "seq": 4, "turn": {
            "kind": "TOOL_RESULTS",
            "message": {"role": "tool", "content": [
                {"kind": "tool_result", "tool_result": {
                    "tool_call_id": "tc-2", "name": "apply_patch",
                    "content": "Applied.", "is_error": False,
                }},
            ]},
        }},
        {"kind": "entry", "seq": 5, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-3", "name": "shell",
                    "arguments": "{\"command\": \"python -c 'from main import widget; print(widget())'\"}",
                }},
            ]},
            "timestamp": "2026-03-01T12:00:04Z",
            "usage": {"input_tokens": 700, "output_tokens": 25},
        }},
        {"kind": "entry", "seq": 6, "turn": {
            "kind": "TOOL_RESULTS",
            "message": {"role": "tool", "content": [
                {"kind": "tool_result", "tool_result": {
                    "tool_call_id": "tc-3", "name": "shell",
                    "content": "42", "is_error": False,
                }},
            ]},
        }},
        {"kind": "entry", "seq": 7, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-4", "name": "communicate",
                    "arguments": "{\"result\": \"Widget implemented.\"}",
                }},
            ]},
            "timestamp": "2026-03-01T12:00:05Z",
            "usage": {"input_tokens": 750, "output_tokens": 20},
        }},
    ]


def _failing_transcript():
    """Transcript for a failing task: explore, wrong edit, submit."""
    return [
        {"kind": "header", "format_version": 1, "session_id": "sess-main2",
         "created_at": "2026-03-01T13:00:00Z", "model": "gpt-5.3-codex",
         "profile_id": "openai", "depth": 0},
        {"kind": "entry", "seq": 0, "turn": {
            "kind": "USER_INPUT",
            "message": {"role": "user", "content": [
                {"kind": "text", "text": "Fix the bug in widget."}
            ]},
            "timestamp": "2026-03-01T13:00:01Z",
        }},
        {"kind": "entry", "seq": 1, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-10", "name": "read_file",
                    "arguments": "{\"path\": \"main.py\"}",
                }},
            ]},
            "timestamp": "2026-03-01T13:00:02Z",
            "usage": {"input_tokens": 400, "output_tokens": 15},
        }},
        {"kind": "entry", "seq": 2, "turn": {
            "kind": "TOOL_RESULTS",
            "message": {"role": "tool", "content": [
                {"kind": "tool_result", "tool_result": {
                    "tool_call_id": "tc-10", "name": "read_file",
                    "content": "def widget(): return 0", "is_error": False,
                }},
            ]},
        }},
        {"kind": "entry", "seq": 3, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "tool_call", "tool_call": {
                    "id": "tc-11", "name": "communicate",
                    "arguments": "{\"result\": \"Looks fine to me.\"}",
                }},
            ]},
            "timestamp": "2026-03-01T13:00:03Z",
            "usage": {"input_tokens": 500, "output_tokens": 10},
        }},
    ]
```

**Step 3: Create minimal server.py**

```python
"""Eval dashboard server."""

from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles
import os

app = FastAPI(title="Serf Eval Dashboard")


@app.get("/health")
def health():
    return {"status": "ok"}


# Serve static files last (catch-all)
static_dir = os.path.join(os.path.dirname(__file__), "static")
if os.path.isdir(static_dir):
    app.mount("/static", StaticFiles(directory=static_dir), name="static")
```

**Step 4: Create placeholder files**

Create empty `data.py`, `trajectory.py`, `markdown_render.py`, and minimal `static/index.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Serf Eval Dashboard</title>
    <link rel="stylesheet" href="/static/style.css">
</head>
<body>
    <div id="app">Loading...</div>
    <script src="/static/app.js"></script>
</body>
</html>
```

Empty `style.css` and `app.js`.

**Step 5: Verify**

```bash
cd tools/dashboard
pip install fastapi uvicorn httpx  # httpx needed for TestClient
pytest conftest.py --co  # verify fixtures load
python -c "from server import app; print('OK')"
```

**Step 6: Commit**

```bash
git add tools/dashboard/
git commit -m "feat(dashboard): project skeleton with test fixtures"
```

---

### Task 2: Data layer — run discovery and task reading

Build the `RunStore` class that discovers runs from harbor job directories and reads
task results (rewards, failure categories, session counts).

**Files:**
- Create: `tools/dashboard/data.py`
- Modify: `tools/dashboard/test_data.py`

**Step 1: Write failing tests in test_data.py**

```python
"""Tests for the data layer."""

import pytest
from data import RunStore


class TestRunDiscovery:
    def test_discovers_run(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        runs = store.list_runs()
        assert len(runs) == 1
        assert runs[0]["job_name"] == "full-test"

    def test_empty_dir(self, tmp_path):
        store = RunStore([tmp_path])
        assert store.list_runs() == []

    def test_multiple_dirs(self, harbor_job_dir, tmp_path):
        # Create a second job
        job2 = tmp_path / "other-run" / "other-run"
        task = job2 / "some-task__xyz789" / "verifier"
        task.mkdir(parents=True)
        (task / "reward.txt").write_text("1.0")
        # Agent dirs
        sessions = job2 / "some-task__xyz789" / "agent" / "serf-state" / "sessions"
        sessions.mkdir(parents=True)

        store = RunStore([harbor_job_dir, tmp_path / "other-run"])
        runs = store.list_runs()
        assert len(runs) == 2


class TestTaskReading:
    def test_list_tasks(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        tasks = store.list_tasks("full-test")
        names = [t["name"] for t in tasks]
        assert "build-widget" in names
        assert "fix-bug" in names

    def test_task_reward(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        tasks = store.list_tasks("full-test")
        by_name = {t["name"]: t for t in tasks}
        assert by_name["build-widget"]["reward"] == 1.0
        assert by_name["fix-bug"]["reward"] == 0.0

    def test_task_pass_fail(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        tasks = store.list_tasks("full-test")
        by_name = {t["name"]: t for t in tasks}
        assert by_name["build-widget"]["passed"] is True
        assert by_name["fix-bug"]["passed"] is False

    def test_failure_category(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        tasks = store.list_tasks("full-test")
        by_name = {t["name"]: t for t in tasks}
        assert by_name["fix-bug"]["failure_category"] == "wrong_answer"

    def test_session_count(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        tasks = store.list_tasks("full-test")
        by_name = {t["name"]: t for t in tasks}
        assert by_name["build-widget"]["session_count"] >= 1

    def test_run_summary(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        run = store.get_run("full-test")
        assert run["task_count"] == 2
        assert run["pass_count"] == 1

    def test_unknown_run(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        assert store.get_run("nonexistent") is None
```

**Step 2: Run tests, verify they fail**

```bash
cd tools/dashboard && pytest test_data.py -v
```

Expected: ImportError or AttributeError (RunStore doesn't exist yet).

**Step 3: Implement data.py**

```python
"""Data layer — discover runs and read task results from disk."""

import glob
import json
import os
import re


class RunStore:
    """Discovers and reads eval runs from harbor job directories.

    Each data_dir should contain a subdirectory named after the job,
    which contains task directories in harbor's task__hash format.

    Example: /tmp/full-mk6/full-mk6/build-cython-ext__abc123/
    """

    def __init__(self, data_dirs: list[str]):
        self._data_dirs = [str(d) for d in data_dirs]

    def _discover_jobs(self) -> dict[str, str]:
        """Return {job_name: job_path} for all discovered jobs."""
        jobs = {}
        for data_dir in self._data_dirs:
            if not os.path.isdir(data_dir):
                continue
            # Harbor creates: data_dir/job_name/task__hash/
            # Check if data_dir itself contains task dirs
            if self._has_task_dirs(data_dir):
                name = os.path.basename(data_dir)
                jobs[name] = data_dir
                continue
            # Otherwise scan subdirectories
            for entry in os.listdir(data_dir):
                sub = os.path.join(data_dir, entry)
                if os.path.isdir(sub) and self._has_task_dirs(sub):
                    jobs[entry] = sub
        return jobs

    def _has_task_dirs(self, path: str) -> bool:
        """Check if a directory contains harbor task directories (name__hash)."""
        try:
            for entry in os.listdir(path):
                if "__" in entry and os.path.isdir(os.path.join(path, entry)):
                    return True
        except OSError:
            pass
        return False

    def _task_dirs(self, job_path: str) -> list[tuple[str, str]]:
        """Return [(task_name, task_dir_path)] for a job directory."""
        results = []
        for entry in sorted(os.listdir(job_path)):
            full = os.path.join(job_path, entry)
            if not os.path.isdir(full) or "__" not in entry:
                continue
            task_name = entry.rsplit("__", 1)[0]
            results.append((task_name, full))
        return results

    def list_runs(self) -> list[dict]:
        """List all discovered runs with summary info."""
        runs = []
        for job_name, job_path in sorted(self._discover_jobs().items()):
            tasks = self._task_dirs(job_path)
            completed = 0
            passed = 0
            for _, td in tasks:
                reward = self._read_reward(td)
                if reward is not None:
                    completed += 1
                    if reward > 0:
                        passed += 1

            manifest = self._read_manifest(job_path)

            runs.append({
                "job_name": job_name,
                "path": job_path,
                "task_count": len(tasks),
                "completed": completed,
                "pass_count": passed,
                "status": "complete" if completed == len(tasks) else "running",
                "model": manifest.get("model", ""),
                "git_sha": manifest.get("git_sha", ""),
                "started_at": manifest.get("started_at", ""),
            })
        return runs

    def get_run(self, job_name: str) -> dict | None:
        """Get detailed info for a specific run."""
        jobs = self._discover_jobs()
        if job_name not in jobs:
            return None

        job_path = jobs[job_name]
        tasks = self.list_tasks(job_name)
        manifest = self._read_manifest(job_path)

        pass_count = sum(1 for t in tasks if t["passed"])
        fail_cats = {}
        for t in tasks:
            cat = t.get("failure_category", "")
            if cat:
                fail_cats[cat] = fail_cats.get(cat, 0) + 1

        return {
            "job_name": job_name,
            "path": job_path,
            "task_count": len(tasks),
            "pass_count": pass_count,
            "failure_categories": fail_cats,
            "status": "complete" if all(
                t["reward"] is not None for t in tasks
            ) else "running",
            "model": manifest.get("model", ""),
            "git_sha": manifest.get("git_sha", ""),
            "started_at": manifest.get("started_at", ""),
            "manifest": manifest,
        }

    def list_tasks(self, job_name: str) -> list[dict]:
        """List all tasks in a run with results."""
        jobs = self._discover_jobs()
        if job_name not in jobs:
            return []

        job_path = jobs[job_name]
        results = []
        for task_name, task_dir in self._task_dirs(job_path):
            reward = self._read_reward(task_dir)
            passed = reward is not None and reward > 0
            results.append({
                "name": task_name,
                "reward": reward,
                "passed": passed,
                "failure_category": self._classify_failure(task_dir, reward),
                "session_count": self._count_sessions(task_dir),
                "dir": task_dir,
            })
        return results

    def get_task(self, job_name: str, task_name: str) -> dict | None:
        """Get a specific task's data including transcript paths."""
        jobs = self._discover_jobs()
        if job_name not in jobs:
            return None

        for name, task_dir in self._task_dirs(jobs[job_name]):
            if name == task_name:
                task = {
                    "name": name,
                    "reward": self._read_reward(task_dir),
                    "failure_category": self._classify_failure(
                        task_dir, self._read_reward(task_dir)
                    ),
                    "session_count": self._count_sessions(task_dir),
                    "dir": task_dir,
                    "transcript_files": self._find_transcripts(task_dir),
                    "verifier_output": self._read_file(
                        os.path.join(task_dir, "verifier", "test-stdout.txt")
                    ),
                    "agent_stdout": self._read_file(
                        os.path.join(task_dir, "agent", "command-0", "stdout.txt")
                    ),
                }
                task["passed"] = (
                    task["reward"] is not None and task["reward"] > 0
                )
                return task
        return None

    def _read_reward(self, task_dir: str) -> float | None:
        path = os.path.join(task_dir, "verifier", "reward.txt")
        if not os.path.isfile(path):
            return None
        try:
            return float(open(path).read().strip())
        except (ValueError, OSError):
            return None

    def _read_manifest(self, job_path: str) -> dict:
        """Read manifest.json from the job directory or its parent."""
        for candidate in [
            os.path.join(job_path, "manifest.json"),
            os.path.join(os.path.dirname(job_path), "manifest.json"),
        ]:
            if os.path.isfile(candidate):
                try:
                    return json.load(open(candidate))
                except (json.JSONDecodeError, OSError):
                    pass
        return {}

    def _classify_failure(self, task_dir: str, reward: float | None) -> str:
        """Classify why a task failed."""
        if reward is None:
            return "running"
        if reward > 0:
            return ""

        # Check result.json for timeout
        result_path = os.path.join(task_dir, "result.json")
        if os.path.isfile(result_path):
            try:
                r = json.load(open(result_path))
                exc = r.get("exception_info", {})
                if exc.get("exception_type") == "AgentTimeoutError":
                    return "timeout"
            except (json.JSONDecodeError, OSError):
                pass

        # Check agent stdout for submission
        stdout_path = os.path.join(
            task_dir, "agent", "command-0", "stdout.txt"
        )
        stdout = self._read_file(stdout_path) or ""
        if "[submit_result]" in stdout or "[communicate]" in stdout:
            return "wrong_answer"

        if "[error]" in stdout:
            return "api_error"

        return "no_submit"

    def _count_sessions(self, task_dir: str) -> int:
        sessions_dir = os.path.join(
            task_dir, "agent", "serf-state", "sessions"
        )
        if not os.path.isdir(sessions_dir):
            return 0
        return len([
            f for f in os.listdir(sessions_dir)
            if f.endswith(".transcript.jsonl")
        ])

    def _find_transcripts(self, task_dir: str) -> list[str]:
        sessions_dir = os.path.join(
            task_dir, "agent", "serf-state", "sessions"
        )
        if not os.path.isdir(sessions_dir):
            return []
        return sorted(
            os.path.join(sessions_dir, f)
            for f in os.listdir(sessions_dir)
            if f.endswith(".transcript.jsonl")
        )

    def _read_file(self, path: str) -> str | None:
        if not os.path.isfile(path):
            return None
        try:
            return open(path).read()
        except OSError:
            return None
```

**Step 4: Run tests, verify they pass**

```bash
cd tools/dashboard && pytest test_data.py -v
```

Expected: all pass.

**Step 5: Commit**

```bash
git add tools/dashboard/data.py tools/dashboard/test_data.py
git commit -m "feat(dashboard): data layer — run discovery and task reading"
```

---

### Task 3: Transcript parsing and session hierarchy

Parse transcript JSONL files into structured sessions with parent-child relationships.

**Files:**
- Modify: `tools/dashboard/data.py` (add `load_transcripts()`)
- Modify: `tools/dashboard/test_data.py` (add transcript tests)

**Step 1: Write failing tests**

Add to `test_data.py`:

```python
class TestTranscriptLoading:
    def test_loads_sessions(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        assert len(sessions) >= 1

    def test_session_header(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        main = sessions[0]
        assert main["session_id"] == "sess-main"
        assert main["model"] == "gpt-5.3-codex"
        assert main["depth"] == 0

    def test_session_entries(self, harbor_job_dir):
        store = RunStore([harbor_job_dir])
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        main = sessions[0]
        assert len(main["entries"]) > 0
        assert main["entries"][0]["turn"]["kind"] == "USER_INPUT"

    def test_session_tree(self, harbor_job_dir, tmp_path):
        """Test parent-child session linking."""
        store = RunStore([harbor_job_dir])
        task = store.get_task("full-test", "build-widget")

        # Add a child session transcript to the task
        sessions_dir = os.path.join(
            task["dir"], "agent", "serf-state", "sessions"
        )
        child_entries = [
            {"kind": "header", "format_version": 1,
             "session_id": "sess-child",
             "parent_session_id": "sess-main",
             "parent_tool_call_id": "tc-4",
             "task": "Review the implementation",
             "created_at": "2026-03-01T12:01:00Z",
             "model": "gpt-5.3-codex",
             "profile_id": "openai", "depth": 1},
            {"kind": "entry", "seq": 0, "turn": {
                "kind": "USER_INPUT",
                "message": {"role": "user", "content": [
                    {"kind": "text", "text": "Review this work."}
                ]},
            }},
        ]
        child_path = os.path.join(sessions_dir, "sess-child.transcript.jsonl")
        with open(child_path, "w") as f:
            for e in child_entries:
                f.write(json.dumps(e) + "\n")

        # Reload
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        tree = store.build_session_tree(sessions)
        assert len(tree) == 1  # one root session
        assert tree[0]["session_id"] == "sess-main"
        assert len(tree[0].get("children", [])) == 1
        assert tree[0]["children"][0]["session_id"] == "sess-child"
```

**Step 2: Run tests, verify they fail**

```bash
cd tools/dashboard && pytest test_data.py::TestTranscriptLoading -v
```

**Step 3: Implement `load_transcripts()` and `build_session_tree()`**

Add to `RunStore` class in `data.py`:

```python
def load_transcripts(self, transcript_files: list[str]) -> list[dict]:
    """Parse transcript JSONL files into session dicts."""
    sessions = []
    for path in transcript_files:
        session = self._parse_transcript(path)
        if session:
            sessions.append(session)
    sessions.sort(key=lambda s: s.get("created_at", ""))
    return sessions

def _parse_transcript(self, path: str) -> dict | None:
    """Parse a single transcript JSONL file."""
    header = {}
    entries = []
    try:
        with open(path) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                obj = json.loads(line)
                if obj.get("kind") == "header":
                    header = obj
                elif obj.get("kind") == "entry":
                    entries.append(obj)
    except (OSError, json.JSONDecodeError):
        return None

    if not header:
        return None

    return {
        "session_id": header.get("session_id", ""),
        "parent_session_id": header.get("parent_session_id", ""),
        "parent_tool_call_id": header.get("parent_tool_call_id", ""),
        "task": header.get("task", ""),
        "created_at": header.get("created_at", ""),
        "model": header.get("model", ""),
        "depth": header.get("depth", 0),
        "build_version": header.get("build_version", ""),
        "entries": entries,
    }

def build_session_tree(self, sessions: list[dict]) -> list[dict]:
    """Organize flat sessions into a parent-child tree."""
    by_id = {s["session_id"]: s for s in sessions}

    for s in sessions:
        s["children"] = []

    roots = []
    for s in sessions:
        parent_id = s.get("parent_session_id", "")
        if parent_id and parent_id in by_id:
            by_id[parent_id]["children"].append(s)
        else:
            roots.append(s)

    return roots
```

**Step 4: Run tests, verify they pass**

```bash
cd tools/dashboard && pytest test_data.py -v
```

**Step 5: Commit**

```bash
git add tools/dashboard/data.py tools/dashboard/test_data.py
git commit -m "feat(dashboard): transcript parsing with session hierarchy"
```

---

### Task 4: Trajectory parser

Parse raw transcript entries into the high-level trajectory view — the core innovation
of this dashboard. Each "round" is classified by its dominant action (EXPLORE, EDIT,
EXEC, SPAWN, SUBMIT, REVIEW, PLAN, ERROR).

**Files:**
- Create: `tools/dashboard/trajectory.py`
- Create: `tools/dashboard/test_trajectory.py`

**Step 1: Write failing tests in test_trajectory.py**

```python
"""Tests for the trajectory parser."""

import json
import pytest
from trajectory import build_trajectory, classify_round


class TestClassifyRound:
    def test_explore_tools(self):
        tools = ["read_file", "glob", "grep"]
        assert classify_round(tools, has_text=False) == "EXPLORE"

    def test_edit_tools(self):
        tools = ["apply_patch"]
        assert classify_round(tools, has_text=False) == "EDIT"

    def test_exec_tools(self):
        tools = ["shell"]
        assert classify_round(tools, has_text=False) == "EXEC"

    def test_submit_tool(self):
        tools = ["communicate"]
        assert classify_round(tools, has_text=False) == "SUBMIT"

    def test_submit_result_tool(self):
        tools = ["submit_result"]
        assert classify_round(tools, has_text=False) == "SUBMIT"

    def test_spawn_tool(self):
        tools = ["spawn_agent"]
        assert classify_round(tools, has_text=False) == "SPAWN"

    def test_text_only(self):
        tools = []
        assert classify_round(tools, has_text=True) == "PLAN"

    def test_mixed_defaults_to_dominant(self):
        tools = ["read_file", "shell", "apply_patch"]
        # When mixed, EDIT takes priority over EXPLORE
        result = classify_round(tools, has_text=False)
        assert result in ("EDIT", "EXEC")


class TestBuildTrajectory:
    def test_basic_trajectory(self, harbor_job_dir):
        from data import RunStore
        store = RunStore([harbor_job_dir])
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        trajectory = build_trajectory(sessions[0])

        assert len(trajectory) > 0
        # First round should be EXPLORE (glob)
        assert trajectory[0]["action"] == "EXPLORE"
        # Last round should be SUBMIT (communicate)
        assert trajectory[-1]["action"] == "SUBMIT"

    def test_round_has_summary(self, harbor_job_dir):
        from data import RunStore
        store = RunStore([harbor_job_dir])
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        trajectory = build_trajectory(sessions[0])

        for r in trajectory:
            assert "summary" in r
            assert "action" in r
            assert "round" in r
            assert isinstance(r["summary"], str)
            assert len(r["summary"]) > 0

    def test_round_preserves_raw_entries(self, harbor_job_dir):
        from data import RunStore
        store = RunStore([harbor_job_dir])
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        trajectory = build_trajectory(sessions[0])

        for r in trajectory:
            assert "entries" in r
            assert len(r["entries"]) > 0

    def test_short_session(self):
        """A session with only a user input and one assistant turn."""
        session = {
            "entries": [
                {"kind": "entry", "seq": 0, "turn": {
                    "kind": "USER_INPUT",
                    "message": {"role": "user", "content": [
                        {"kind": "text", "text": "Do something."}
                    ]},
                }},
                {"kind": "entry", "seq": 1, "turn": {
                    "kind": "ASSISTANT",
                    "message": {"role": "assistant", "content": [
                        {"kind": "text", "text": "I'll plan this out."},
                    ]},
                }},
            ]
        }
        trajectory = build_trajectory(session)
        assert len(trajectory) == 1
        assert trajectory[0]["action"] == "PLAN"
```

**Step 2: Run tests, verify they fail**

```bash
cd tools/dashboard && pytest test_trajectory.py -v
```

**Step 3: Implement trajectory.py**

```python
"""Parse transcripts into high-level trajectory timelines.

A trajectory is a list of "rounds" — each one a high-level action the
agent took (EXPLORE, EDIT, EXEC, SPAWN, SUBMIT, REVIEW, PLAN, ERROR).
This gives a forest-level view of what happened, instead of requiring
humans to scroll through dozens of raw transcript entries.
"""

# Tools classified by action type.
EXPLORE_TOOLS = {"read_file", "glob", "grep"}
EDIT_TOOLS = {"apply_patch", "edit_file", "write_file"}
EXEC_TOOLS = {"shell"}
SUBMIT_TOOLS = {"communicate", "submit_result"}
SPAWN_TOOLS = {"spawn_agent"}
REVIEW_TOOLS = {"approve", "reject"}

# Priority order for classification when a round has mixed tool types.
# Higher priority wins.
ACTION_PRIORITY = {
    "SUBMIT": 6,
    "SPAWN": 5,
    "EDIT": 4,
    "EXEC": 3,
    "EXPLORE": 2,
    "PLAN": 1,
    "ERROR": 0,
}


def classify_round(tool_names: list[str], has_text: bool) -> str:
    """Classify a round by its dominant tool type."""
    if not tool_names:
        return "PLAN" if has_text else "ERROR"

    actions = set()
    for name in tool_names:
        if name in SUBMIT_TOOLS:
            actions.add("SUBMIT")
        elif name in SPAWN_TOOLS:
            actions.add("SPAWN")
        elif name in EDIT_TOOLS:
            actions.add("EDIT")
        elif name in EXEC_TOOLS:
            actions.add("EXEC")
        elif name in EXPLORE_TOOLS:
            actions.add("EXPLORE")
        elif name in REVIEW_TOOLS:
            actions.add("SUBMIT")  # Review tools are verdict actions

    if not actions:
        return "PLAN" if has_text else "EXEC"  # Unknown tools → EXEC

    return max(actions, key=lambda a: ACTION_PRIORITY.get(a, 0))


def _summarize_round(action: str, tool_calls: list[dict],
                     tool_results: list[dict],
                     text: str) -> str:
    """Generate a one-line summary for a trajectory round."""
    if action == "PLAN":
        # Show first ~80 chars of assistant text
        preview = text.strip().replace("\n", " ")[:80]
        return f'"{preview}{"..." if len(text.strip()) > 80 else ""}'

    if action == "SUBMIT":
        for tc in tool_calls:
            if tc.get("name") in SUBMIT_TOOLS:
                try:
                    args = tc.get("arguments", "{}")
                    if isinstance(args, str):
                        args = __import__("json").loads(args)
                    result_text = args.get("result", "")[:80]
                    return f'{tc["name"]}("{result_text}...")'
                except Exception:
                    return tc.get("name", "submit")
        return "submit"

    if action == "SPAWN":
        for tc in tool_calls:
            if tc.get("name") == "spawn_agent":
                try:
                    args = tc.get("arguments", "{}")
                    if isinstance(args, str):
                        args = __import__("json").loads(args)
                    agent = args.get("agent", "")
                    task_preview = args.get("task", "")[:60]
                    return f'{agent}: "{task_preview}..."'
                except Exception:
                    return "spawn_agent"
        return "spawn_agent"

    if action == "EXEC":
        commands = []
        for tc in tool_calls:
            if tc.get("name") == "shell":
                try:
                    args = tc.get("arguments", "{}")
                    if isinstance(args, str):
                        args = __import__("json").loads(args)
                    cmd = args.get("command", "")[:60]
                    commands.append(cmd)
                except Exception:
                    commands.append("shell")
        return "; ".join(commands) if commands else "shell"

    if action == "EDIT":
        files = []
        for tc in tool_calls:
            if tc.get("name") in EDIT_TOOLS:
                try:
                    args = tc.get("arguments", "{}")
                    if isinstance(args, str):
                        args = __import__("json").loads(args)
                    path = args.get("path", args.get("file_path", ""))
                    if path:
                        files.append(path.split("/")[-1])
                except Exception:
                    pass
        return ", ".join(files) if files else "edit"

    if action == "EXPLORE":
        targets = []
        for tc in tool_calls:
            try:
                args = tc.get("arguments", "{}")
                if isinstance(args, str):
                    args = __import__("json").loads(args)
                path = args.get("path", args.get("pattern", ""))
                if path:
                    targets.append(path.split("/")[-1])
            except Exception:
                pass
        return ", ".join(targets[:3]) if targets else "explore"

    return ""


def build_trajectory(session: dict) -> list[dict]:
    """Parse a session's entries into a trajectory of classified rounds.

    A "round" is one assistant turn plus its tool results. The user input
    at the start is skipped (it's the task description, shown elsewhere).
    """
    entries = session.get("entries", [])
    rounds = []
    round_num = 0

    # Group entries into rounds: each ASSISTANT entry starts a new round.
    current_assistant = None
    current_tool_results = []
    buffered_entries = []

    for entry in entries:
        turn = entry.get("turn", {})
        kind = turn.get("kind", "")

        if kind == "USER_INPUT":
            # Skip the initial task input
            continue

        if kind == "STEERING":
            continue

        if kind == "ASSISTANT":
            # Flush previous round
            if current_assistant is not None:
                round_num += 1
                r = _make_round(round_num, current_assistant,
                                current_tool_results, buffered_entries)
                rounds.append(r)

            current_assistant = entry
            current_tool_results = []
            buffered_entries = [entry]

        elif kind == "TOOL_RESULTS":
            current_tool_results.append(entry)
            buffered_entries.append(entry)

        elif kind in ("CHECKPOINT", "SUMMARY"):
            buffered_entries.append(entry)

    # Flush last round
    if current_assistant is not None:
        round_num += 1
        r = _make_round(round_num, current_assistant,
                        current_tool_results, buffered_entries)
        rounds.append(r)

    return rounds


def _make_round(round_num: int, assistant_entry: dict,
                tool_result_entries: list[dict],
                raw_entries: list[dict]) -> dict:
    """Build a trajectory round from an assistant turn + its results."""
    turn = assistant_entry.get("turn", {})
    msg = turn.get("message", {})
    content = msg.get("content", [])
    usage = turn.get("usage", {})

    # Extract tool calls and text
    tool_calls = []
    text_parts = []
    for c in content:
        if c.get("kind") == "tool_call":
            tool_calls.append(c.get("tool_call", {}))
        elif c.get("kind") == "text":
            t = c.get("text", "").strip()
            if t:
                text_parts.append(t)

    # Extract tool results
    tool_results = []
    for tr_entry in tool_result_entries:
        for c in tr_entry.get("turn", {}).get("message", {}).get("content", []):
            if c.get("kind") == "tool_result":
                tool_results.append(c.get("tool_result", {}))

    tool_names = [tc.get("name", "") for tc in tool_calls]
    has_text = bool(text_parts)

    action = classify_round(tool_names, has_text)
    text = "\n".join(text_parts)
    summary = _summarize_round(action, tool_calls, tool_results, text)

    return {
        "round": round_num,
        "action": action,
        "summary": summary,
        "tool_calls": tool_calls,
        "tool_results": tool_results,
        "text": text,
        "usage": usage,
        "entries": raw_entries,
    }
```

**Step 4: Run tests, verify they pass**

```bash
cd tools/dashboard && pytest test_trajectory.py -v
```

**Step 5: Commit**

```bash
git add tools/dashboard/trajectory.py tools/dashboard/test_trajectory.py
git commit -m "feat(dashboard): trajectory parser — classify rounds by action"
```

---

### Task 5: Markdown renderer

Render dashboard views as markdown. This is the default response format for all API
endpoints. An agent or curl user gets readable, structured markdown without any flags.

**Files:**
- Create: `tools/dashboard/markdown_render.py`
- Create: `tools/dashboard/test_markdown.py`

**Step 1: Write failing tests in test_markdown.py**

```python
"""Tests for the markdown renderer."""

from markdown_render import render_run_list, render_run_detail, render_task_detail


class TestRenderRunList:
    def test_renders_table(self):
        runs = [
            {"job_name": "full-mk6", "model": "gpt-5.3-codex",
             "pass_count": 12, "task_count": 89, "completed": 34,
             "status": "running", "git_sha": "abc1234",
             "started_at": "2026-03-01T12:00:00Z"},
            {"job_name": "full-mk4", "model": "gpt-5.3-codex",
             "pass_count": 13, "task_count": 67, "completed": 67,
             "status": "complete", "git_sha": "def5678",
             "started_at": "2026-02-28T10:00:00Z"},
        ]
        md = render_run_list(runs)
        assert "full-mk6" in md
        assert "full-mk4" in md
        assert "running" in md
        assert "complete" in md

    def test_empty_list(self):
        md = render_run_list([])
        assert "No runs" in md


class TestRenderRunDetail:
    def test_renders_header(self):
        run = {"job_name": "full-mk6", "model": "gpt-5.3-codex",
               "git_sha": "abc1234", "pass_count": 12,
               "task_count": 89, "status": "running",
               "failure_categories": {"timeout": 5, "wrong_answer": 10},
               "started_at": "", "manifest": {}}
        tasks = [
            {"name": "build-widget", "passed": True,
             "reward": 1.0, "failure_category": "",
             "session_count": 3},
            {"name": "fix-bug", "passed": False,
             "reward": 0.0, "failure_category": "wrong_answer",
             "session_count": 2},
        ]
        md = render_run_detail(run, tasks)
        assert "full-mk6" in md
        assert "build-widget" in md
        assert "fix-bug" in md
        assert "PASS" in md
        assert "FAIL" in md


class TestRenderTaskDetail:
    def test_renders_trajectory(self):
        trajectory = [
            {"round": 1, "action": "EXPLORE", "summary": "ls, main.py",
             "tool_calls": [], "tool_results": [], "text": "",
             "usage": {}, "entries": []},
            {"round": 2, "action": "SUBMIT",
             "summary": 'communicate("Done")',
             "tool_calls": [], "tool_results": [], "text": "",
             "usage": {}, "entries": []},
        ]
        md = render_task_detail(
            task_name="build-widget",
            job_name="full-mk6",
            reward=1.0,
            failure_category="",
            trajectory=trajectory,
            verifier_output="ALL TESTS PASSED",
        )
        assert "EXPLORE" in md
        assert "SUBMIT" in md
        assert "build-widget" in md
        assert "PASSED" in md
```

**Step 2: Run tests, verify they fail**

```bash
cd tools/dashboard && pytest test_markdown.py -v
```

**Step 3: Implement markdown_render.py**

```python
"""Render dashboard views as markdown.

Markdown is the default response format. Every endpoint returns markdown
unless the client sends Accept: application/json.
"""


def render_run_list(runs: list[dict]) -> str:
    """Render the dashboard overview as a markdown table."""
    if not runs:
        return "# Eval Dashboard\n\nNo runs found.\n"

    lines = ["# Eval Dashboard", ""]
    lines.append(
        "| Run | Model | Pass Rate | Status | Git SHA |"
    )
    lines.append(
        "|-----|-------|-----------|--------|---------|"
    )
    for r in runs:
        total = r.get("completed", r.get("task_count", 0))
        pass_count = r.get("pass_count", 0)
        pct = f"{pass_count/total*100:.0f}%" if total > 0 else "-"
        rate = f"{pass_count}/{total} ({pct})"
        status = r.get("status", "?")
        if status == "running":
            done = r.get("completed", 0)
            of = r.get("task_count", 0)
            status = f"running ({done}/{of})"
        lines.append(
            f"| {r['job_name']} | {r.get('model', '')} "
            f"| {rate} | {status} | {r.get('git_sha', '')[:7]} |"
        )
    lines.append("")
    return "\n".join(lines)


def render_run_detail(run: dict, tasks: list[dict]) -> str:
    """Render a single run's detail page as markdown."""
    lines = [f"# {run['job_name']}", ""]

    # Summary
    total = run.get("task_count", 0)
    passed = run.get("pass_count", 0)
    pct = f"{passed/total*100:.0f}%" if total > 0 else "-"
    lines.append(f"**Pass rate:** {passed}/{total} ({pct})")
    lines.append(f"**Model:** {run.get('model', '?')}")
    lines.append(f"**Git SHA:** {run.get('git_sha', '?')}")
    lines.append(f"**Status:** {run.get('status', '?')}")

    # Failure breakdown
    fc = run.get("failure_categories", {})
    if fc:
        lines.append("")
        lines.append("**Failures:**")
        for cat, count in sorted(fc.items()):
            lines.append(f"- {cat}: {count}")

    # Task table
    lines.extend(["", "## Tasks", ""])
    lines.append("| Task | Result | Category | Sessions |")
    lines.append("|------|--------|----------|----------|")
    for t in sorted(tasks, key=lambda t: (t["passed"], t["name"])):
        result = "PASS" if t["passed"] else "FAIL"
        cat = t.get("failure_category", "") or ""
        lines.append(
            f"| {t['name']} | {result} | {cat} | {t.get('session_count', 0)} |"
        )
    lines.append("")
    return "\n".join(lines)


def render_task_detail(task_name: str, job_name: str,
                       reward: float | None,
                       failure_category: str,
                       trajectory: list[dict],
                       verifier_output: str = "",
                       reviewer_verdict: str = "") -> str:
    """Render a task detail page with trajectory as markdown."""
    result = "PASSED" if reward and reward > 0 else "FAILED"
    lines = [
        f"# {task_name} — {job_name}",
        f"**Result:** {result}",
    ]
    if failure_category:
        lines.append(f"**Failure category:** {failure_category}")

    # Trajectory
    lines.extend(["", "## Trajectory", ""])
    if not trajectory:
        lines.append("No trajectory data.")
    else:
        for r in trajectory:
            pad = str(r["round"]).rjust(3)
            action = r["action"].ljust(7)
            lines.append(f"    {pad}  {action}  {r['summary']}")

    # Verifier output
    if verifier_output:
        lines.extend([
            "", "## Verifier Output", "",
            "```", verifier_output.strip()[-2000:], "```",
        ])

    # Reviewer verdict
    if reviewer_verdict:
        lines.extend([
            "", "## Reviewer Verdict", "",
            reviewer_verdict,
        ])

    lines.append("")
    return "\n".join(lines)
```

**Step 4: Run tests, verify they pass**

```bash
cd tools/dashboard && pytest test_markdown.py -v
```

**Step 5: Commit**

```bash
git add tools/dashboard/markdown_render.py tools/dashboard/test_markdown.py
git commit -m "feat(dashboard): markdown renderer for all views"
```

---

### Task 6: API routes with content negotiation

Wire up all endpoints. Markdown by default, JSON when `Accept: application/json`.

**Files:**
- Modify: `tools/dashboard/server.py`
- Create: `tools/dashboard/test_server.py`

**Step 1: Write failing tests in test_server.py**

```python
"""Integration tests for API routes."""

import json
import os
import pytest
from fastapi.testclient import TestClient


@pytest.fixture
def client(harbor_job_dir):
    """Create a test client pointing at the fixture data."""
    os.environ["DASHBOARD_DATA_DIRS"] = str(harbor_job_dir)
    # Re-import to pick up env var
    import importlib
    import server as srv
    importlib.reload(srv)
    return TestClient(srv.app)


class TestContentNegotiation:
    def test_default_is_markdown(self, client):
        resp = client.get("/api/runs")
        assert resp.status_code == 200
        assert "text/markdown" in resp.headers["content-type"]
        assert "# Eval Dashboard" in resp.text

    def test_json_with_accept_header(self, client):
        resp = client.get("/api/runs",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert isinstance(data, list)
        assert len(data) >= 1


class TestRunEndpoints:
    def test_list_runs(self, client):
        resp = client.get("/api/runs",
                          headers={"Accept": "application/json"})
        data = resp.json()
        names = [r["job_name"] for r in data]
        assert "full-test" in names

    def test_get_run(self, client):
        resp = client.get("/api/runs/full-test",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["job_name"] == "full-test"
        assert data["task_count"] == 2

    def test_get_run_markdown(self, client):
        resp = client.get("/api/runs/full-test")
        assert resp.status_code == 200
        assert "full-test" in resp.text
        assert "PASS" in resp.text or "FAIL" in resp.text

    def test_get_unknown_run(self, client):
        resp = client.get("/api/runs/nonexistent",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 404


class TestTaskEndpoints:
    def test_list_tasks(self, client):
        resp = client.get("/api/runs/full-test/tasks",
                          headers={"Accept": "application/json"})
        data = resp.json()
        names = [t["name"] for t in data]
        assert "build-widget" in names

    def test_get_task(self, client):
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["name"] == "build-widget"
        assert "trajectory" in data

    def test_get_task_markdown(self, client):
        resp = client.get("/api/runs/full-test/tasks/build-widget")
        assert resp.status_code == 200
        assert "build-widget" in resp.text
        assert "Trajectory" in resp.text

    def test_unknown_task(self, client):
        resp = client.get("/api/runs/full-test/tasks/nope",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 404
```

**Step 2: Run tests, verify they fail**

```bash
cd tools/dashboard && pytest test_server.py -v
```

**Step 3: Implement server.py routes**

```python
"""Eval dashboard server.

Markdown by default. Send Accept: application/json for JSON.
"""

import os
from fastapi import FastAPI, Request
from fastapi.responses import PlainTextResponse, JSONResponse
from fastapi.staticfiles import StaticFiles

from data import RunStore
from trajectory import build_trajectory
from markdown_render import render_run_list, render_run_detail, render_task_detail

app = FastAPI(title="Serf Eval Dashboard")

# Configure data dirs from env or defaults.
_data_dirs_str = os.environ.get("DASHBOARD_DATA_DIRS", "/data/serf-evals/runs")
_data_dirs = [d.strip() for d in _data_dirs_str.split(",") if d.strip()]
store = RunStore(_data_dirs)


def _wants_json(request: Request) -> bool:
    accept = request.headers.get("accept", "")
    return "application/json" in accept


def _md_response(content: str) -> PlainTextResponse:
    return PlainTextResponse(content, media_type="text/markdown; charset=utf-8")


# --- Health ---

@app.get("/health")
def health():
    return {"status": "ok"}


# --- Run endpoints ---

@app.get("/api/runs")
def list_runs(request: Request):
    runs = store.list_runs()
    if _wants_json(request):
        return JSONResponse(runs)
    return _md_response(render_run_list(runs))


@app.get("/api/runs/{job_name}")
def get_run(job_name: str, request: Request):
    run = store.get_run(job_name)
    if run is None:
        if _wants_json(request):
            return JSONResponse({"error": "not found"}, status_code=404)
        return _md_response(f"# Not Found\n\nRun `{job_name}` not found.\n")
    tasks = store.list_tasks(job_name)
    if _wants_json(request):
        run["tasks"] = tasks
        return JSONResponse(run)
    return _md_response(render_run_detail(run, tasks))


# --- Task endpoints ---

@app.get("/api/runs/{job_name}/tasks")
def list_tasks(job_name: str, request: Request):
    tasks = store.list_tasks(job_name)
    if not tasks:
        if _wants_json(request):
            return JSONResponse({"error": "not found"}, status_code=404)
        return _md_response(f"# Not Found\n\nRun `{job_name}` not found.\n")
    if _wants_json(request):
        return JSONResponse(tasks)
    return _md_response(render_run_detail(store.get_run(job_name), tasks))


@app.get("/api/runs/{job_name}/tasks/{task_name}")
def get_task(job_name: str, task_name: str, request: Request):
    task = store.get_task(job_name, task_name)
    if task is None:
        if _wants_json(request):
            return JSONResponse({"error": "not found"}, status_code=404)
        return _md_response(
            f"# Not Found\n\n`{task_name}` not found in `{job_name}`.\n"
        )

    # Build trajectory from transcripts
    sessions = store.load_transcripts(task["transcript_files"])
    tree = store.build_session_tree(sessions)

    trajectories = []
    for root_session in tree:
        trajectories.append({
            "session_id": root_session["session_id"],
            "model": root_session["model"],
            "depth": root_session["depth"],
            "trajectory": build_trajectory(root_session),
            "children": [
                {
                    "session_id": child["session_id"],
                    "task": child.get("task", ""),
                    "depth": child["depth"],
                    "trajectory": build_trajectory(child),
                }
                for child in root_session.get("children", [])
            ],
        })

    if _wants_json(request):
        task["trajectory"] = trajectories
        # Remove non-serializable fields
        task.pop("dir", None)
        task.pop("transcript_files", None)
        return JSONResponse(task)

    # Build main trajectory for markdown
    main_trajectory = trajectories[0]["trajectory"] if trajectories else []
    return _md_response(render_task_detail(
        task_name=task_name,
        job_name=job_name,
        reward=task.get("reward"),
        failure_category=task.get("failure_category", ""),
        trajectory=main_trajectory,
        verifier_output=task.get("verifier_output", ""),
    ))


# --- SPA catch-all ---

@app.get("/")
def index():
    """Serve the SPA shell."""
    static_dir = os.path.join(os.path.dirname(__file__), "static")
    index_path = os.path.join(static_dir, "index.html")
    if os.path.isfile(index_path):
        return PlainTextResponse(
            open(index_path).read(), media_type="text/html"
        )
    return PlainTextResponse("Dashboard not built yet.", media_type="text/html")


# Serve static files
static_dir = os.path.join(os.path.dirname(__file__), "static")
if os.path.isdir(static_dir):
    app.mount("/static", StaticFiles(directory=static_dir), name="static")
```

**Step 4: Run tests, verify they pass**

```bash
cd tools/dashboard && pytest test_server.py -v
```

**Step 5: Commit**

```bash
git add tools/dashboard/server.py tools/dashboard/test_server.py
git commit -m "feat(dashboard): API routes with markdown-first content negotiation"
```

---

### Task 7: Frontend — SPA shell, dashboard page, run detail

Build the vanilla JS frontend with client-side routing. The dashboard page shows all
runs; clicking a run shows its task grid.

**Files:**
- Modify: `tools/dashboard/static/index.html`
- Create: `tools/dashboard/static/style.css`
- Create: `tools/dashboard/static/app.js`

**Step 1: Implement index.html**

The SPA shell. Clean, warm-neutral design. Loads style.css and app.js.

See `docs/plans/2026-03-01-eval-dashboard-design.md` for the visual design spec:
- Background `#F8F8F6`, cards `#FFFFFF` with subtle borders
- System font stack for body, monospace for code
- Pass `#18A34A`, Fail `#DC2626`, Timeout `#CA8A04`, No Submit `#9CA3AF`
- 8px spacing grid, 14px body text, generous whitespace

The HTML should include:
- A `<nav>` with breadcrumb navigation
- A `<main id="app">` container for page content
- Links to style.css and app.js

**Step 2: Implement style.css**

Follow the warm neutral design spec exactly. Key elements:
- Card component (`.card`) — white background, subtle border, slight shadow
- Table styles — clean rows, hover highlight, no heavy borders
- Status badges — small colored dots, not pills
- Responsive layout — works on 13" laptop and 27" monitor
- Stat cards — for pass rate, failure breakdown
- Filter bar — for result/category filtering

**Step 3: Implement app.js**

Client-side SPA with hash-based routing. All data fetched via the API with
`Accept: application/json`. Minimum viable pages:

```javascript
// Routes:
// #/              → dashboard (run list)
// #/runs/{name}   → run detail (task grid)
// #/runs/{name}/tasks/{task}  → task detail (trajectory view)
```

**Dashboard page (`renderDashboard`):**
- Fetch `GET /api/runs` with JSON accept header
- Render as a table: run name (link), model, pass rate bar, status, git SHA
- Running runs pinned to top with live progress

**Run detail page (`renderRunDetail`):**
- Fetch `GET /api/runs/{name}` with JSON accept header
- Stat cards: pass count, fail count, failure category breakdown
- Task grid: sortable table with name, result badge, category, session count
- Filter buttons: All, Pass, Fail, Timeout, Wrong Answer, No Submit

**Step 4: Verify manually**

```bash
cd tools/dashboard
DASHBOARD_DATA_DIRS=/tmp/full-mk6 uvicorn server:app --reload --port 8080
# Open http://localhost:8080 in browser
```

Verify: dashboard loads, shows runs, clicking a run shows tasks.

**Step 5: Commit**

```bash
git add tools/dashboard/static/
git commit -m "feat(dashboard): frontend — dashboard and run detail pages"
```

---

### Task 8: Frontend — task detail with trajectory view

The core feature. Renders the trajectory timeline with drill-down into raw entries
and a context panel for verifier output.

**Files:**
- Modify: `tools/dashboard/static/app.js`
- Modify: `tools/dashboard/static/style.css`

**Step 1: Implement trajectory timeline rendering**

Fetch `GET /api/runs/{name}/tasks/{task}` with JSON accept header. Render:

**Left panel (trajectory):**
- Vertical timeline with dots and connecting line (like git log)
- Each round: round number, action badge (colored dot), summary text
- Action colors: EXPLORE=blue, EDIT=yellow, EXEC=gray, SPAWN=purple,
  SUBMIT=green, REVIEW=red, PLAN=muted, ERROR=red
- Click round to expand raw entries below it (tool args, results, usage)
- Subagent sessions shown indented inline at their spawn point

**Right panel (context):**
- Task instruction (from the USER_INPUT entry)
- Verifier test output
- Failure category
- Reward

**Step 2: Implement expand/collapse for raw entries**

Each round is clickable. When expanded, show:
- Tool call name + arguments (collapsible if > 300 chars)
- Tool result content (collapsible if > 400 chars)
- Usage stats (input/output tokens)

Use the same truncation and expand logic as the existing transcript-viewer.py
but with the warm neutral styling.

**Step 3: Implement subagent session inlining**

When a round has action=SPAWN, check if the response includes child sessions.
Render the child session's trajectory indented beneath the spawn round, with a
visual connector showing it's a nested session. Include the child's verdict
(APPROVED/REJECTED) if it's a reviewer.

**Step 4: Verify manually**

```bash
cd tools/dashboard
DASHBOARD_DATA_DIRS=/tmp/full-mk6 uvicorn server:app --reload --port 8080
# Open http://localhost:8080, navigate to a task, verify trajectory view
```

Verify:
- Trajectory shows labeled rounds
- Clicking expands raw entries
- Context panel shows verifier output
- Subagent sessions appear inline

**Step 5: Commit**

```bash
git add tools/dashboard/static/
git commit -m "feat(dashboard): trajectory view with drill-down and context panel"
```

---

### Task 9: Deploy to magic-kingdom

Deploy the dashboard as a persistent service on magic-kingdom.

**Files:**
- Create: `tools/dashboard/requirements.txt`
- Modify: `tools/dashboard/server.py` (add CLI args)

**Step 1: Create requirements.txt**

```
fastapi>=0.115
uvicorn>=0.30
```

**Step 2: Add CLI argument parsing to server.py**

Add an `if __name__ == "__main__":` block that accepts `--data-dir` (repeatable),
`--host`, and `--port` arguments. Pass data dirs to `RunStore`. Run uvicorn.

```python
if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="Serf Eval Dashboard")
    parser.add_argument("--data-dir", action="append", default=[],
                        help="Directory to scan for eval runs (repeatable)")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()

    if args.data_dir:
        global store
        store = RunStore(args.data_dir)

    import uvicorn
    uvicorn.run(app, host=args.host, port=args.port)
```

**Step 3: Deploy**

```bash
# Copy dashboard to magic-kingdom
rsync -avz tools/dashboard/ jesse@magic-kingdom:~/git/eval-dashboard/

# Install deps
ssh jesse@magic-kingdom 'cd ~/git/eval-dashboard && pip install -r requirements.txt'

# Launch in screen session
ssh jesse@magic-kingdom 'screen -dmS dashboard python3 ~/git/eval-dashboard/server.py \
  --data-dir /tmp/full-mk6 \
  --data-dir /tmp/full-mk4 \
  --data-dir /tmp/full-mk3 \
  --port 8080'
```

**Step 4: Verify**

```bash
# Health check
curl http://magic-kingdom:8080/health

# Markdown API
curl http://magic-kingdom:8080/api/runs

# JSON API
curl -H "Accept: application/json" http://magic-kingdom:8080/api/runs

# Open in browser
open http://magic-kingdom:8080
```

**Step 5: Commit**

```bash
git add tools/dashboard/requirements.txt
git commit -m "feat(dashboard): CLI args and requirements for deployment"
```

---

## Phase 2 (future, not part of this plan)

- **Comparison views:** `/compare` for cross-run diff, `/tasks/{task}` for task history
- **Multi-trial support:** Per-task pass rates across reps, trajectory comparison
- **Cross-harness comparison:** serf vs codex on same tasks
- **Auto-refresh:** Poll for new results on running evals
- **Agent workflow integration:** Agent dispatches investigation tasks via dashboard API
- **Write eval data to /data/ directly** (Task #50)
