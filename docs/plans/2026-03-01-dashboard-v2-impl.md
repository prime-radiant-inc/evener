# Eval Dashboard v2 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Transform the eval dashboard from a transcript viewer into an analysis workbench with computed metrics, two-state expand, raw file access, run comparison, and cross-run task history.

**Architecture:** Server-side stats module computes per-task metrics from transcripts and api.jsonl, caches results to disk. New API endpoints serve stats-enriched task lists, raw files, run comparisons, and task history. Frontend rebuilt around data tables with sortable columns, two-state expand (no nested Show more), and four new pages.

**Tech Stack:** Python 3 / FastAPI / vanilla JS / inline SVG. No frameworks, no build step.

**Design doc:** `docs/plans/2026-03-01-dashboard-v2-design.md`

---

### Task 1: Stats Module — Per-Task Metrics from Transcripts

Compute metrics from parsed transcripts. This is the foundation everything else builds on.

**Files:**
- Create: `tools/dashboard/stats.py`
- Create: `tools/dashboard/test_stats.py`

**Step 1: Write failing tests for `compute_task_stats()`**

In `test_stats.py`, import the `harbor_job_dir` fixture from conftest.py. Test that `compute_task_stats()` takes a `RunStore`, `job_name`, and `task_name` and returns a `TaskStats` dict.

```python
"""Tests for stats module."""

import json
import pytest
from data import RunStore
from stats import compute_task_stats


class TestComputeTaskStats:
    """compute_task_stats() extracts metrics from transcripts."""

    def test_total_rounds(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["total_rounds"] == 4  # explore, edit, exec, submit

    def test_rounds_by_action(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["rounds_by_action"]["EXPLORE"] == 1
        assert stats["rounds_by_action"]["EDIT"] == 1
        assert stats["rounds_by_action"]["EXEC"] == 1
        assert stats["rounds_by_action"]["SUBMIT"] == 1

    def test_wasted_rounds_zero_for_passing(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["wasted_rounds"] == 0

    def test_total_tokens(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        # From conftest: 500+600+700+750 = 2550 input, 30+40+25+20 = 115 output
        assert stats["total_tokens_in"] == 2550
        assert stats["total_tokens_out"] == 115

    def test_session_count(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["session_count"] == 1

    def test_max_depth(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["max_depth"] == 0  # single root session

    def test_first_submit_round(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["first_submit_round"] == 4

    def test_submitted_value(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["submitted_value"] == "Widget implemented."

    def test_action_sequence(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["action_sequence"] == ["EXPLORE", "EDIT", "EXEC", "SUBMIT"]

    def test_task_not_found_returns_none(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "nonexistent")
        assert stats is None

    def test_no_submit_has_zero_submit_round(self, harbor_job_dir):
        """fix-bug submits immediately after reading — submit round is 2."""
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "fix-bug")
        assert stats["first_submit_round"] == 2
```

**Step 2: Run tests, verify they fail**

Run: `cd tools/dashboard && python -m pytest test_stats.py -v`
Expected: ImportError (stats module doesn't exist yet)

**Step 3: Implement `compute_task_stats()`**

Create `stats.py`:

```python
"""Compute metrics from transcripts and API logs.

All computation happens here. The server calls these functions and
passes the results to the frontend. No analysis logic in JavaScript.
"""

import json
from pathlib import Path

from trajectory import build_trajectory, classify_round


def compute_task_stats(store, job_name, task_name):
    """Compute per-task metrics from transcripts.

    Returns a dict with keys: total_rounds, rounds_by_action, wasted_rounds,
    total_tokens_in, total_tokens_out, session_count, max_depth,
    first_submit_round, submitted_value, action_sequence.

    Returns None if task not found.
    """
    task = store.get_task(job_name, task_name)
    if task is None:
        return None

    sessions = store.load_transcripts(task.get("transcript_files", []))
    tree = store.build_session_tree(sessions)

    total_rounds = 0
    rounds_by_action = {}
    wasted_rounds = 0
    total_tokens_in = 0
    total_tokens_out = 0
    max_depth = 0
    first_submit_round = 0
    submitted_value = ""
    action_sequence = []  # root session only

    def _process_session(session, is_root=False):
        nonlocal total_rounds, wasted_rounds, total_tokens_in, total_tokens_out
        nonlocal max_depth, first_submit_round, submitted_value

        depth = session.get("depth", 0)
        if depth > max_depth:
            max_depth = depth

        trajectory = build_trajectory(session)
        for r in trajectory:
            total_rounds += 1
            action = r["action"]
            rounds_by_action[action] = rounds_by_action.get(action, 0) + 1
            if action == "ERROR":
                wasted_rounds += 1

            usage = r.get("usage", {})
            total_tokens_in += usage.get("input_tokens", 0)
            total_tokens_out += usage.get("output_tokens", 0)

            if is_root:
                action_sequence.append(action)

            # Track first submit across all sessions
            if action == "SUBMIT" and first_submit_round == 0:
                first_submit_round = r["round"]
                # Extract submitted value
                for tc in r.get("tool_calls", []):
                    args = tc.get("arguments", {})
                    if isinstance(args, str):
                        try:
                            args = json.loads(args)
                        except (json.JSONDecodeError, TypeError):
                            args = {}
                    for key in ("result", "message", "output"):
                        if key in args:
                            submitted_value = str(args[key])
                            break
                    if submitted_value:
                        break

        for child in session.get("children", []):
            _process_session(child)

    for root in tree:
        _process_session(root, is_root=True)

    return {
        "total_rounds": total_rounds,
        "rounds_by_action": rounds_by_action,
        "wasted_rounds": wasted_rounds,
        "total_tokens_in": total_tokens_in,
        "total_tokens_out": total_tokens_out,
        "session_count": len(sessions),
        "max_depth": max_depth,
        "first_submit_round": first_submit_round,
        "submitted_value": submitted_value,
        "action_sequence": action_sequence,
    }
```

**Step 4: Run tests, verify they pass**

Run: `cd tools/dashboard && python -m pytest test_stats.py -v`
Expected: All pass

**Step 5: Commit**

```bash
git add tools/dashboard/stats.py tools/dashboard/test_stats.py
git commit -m "feat(dashboard): stats module — per-task metrics from transcripts"
```

---

### Task 2: Stats Module — Wall Time and API Metrics

Add wall time from result.json and API metrics from api.jsonl.

**Files:**
- Modify: `tools/dashboard/stats.py`
- Modify: `tools/dashboard/test_stats.py`
- Modify: `tools/dashboard/conftest.py`

**Step 1: Add test fixtures for result.json timestamps and api.jsonl**

In `conftest.py`, update `_make_task()` to accept optional `result_json` and `api_log_entries` parameters. Add timestamps to the default result.json:

```python
# In _make_task(), replace the result.json write:
result_data = result_json or {
    "config": {"model": "gpt-5.3-codex"},
    "started_at": "2026-03-01T12:00:00Z",
    "finished_at": "2026-03-01T12:05:30Z",
}
(task_dir / "result.json").write_text(json.dumps(result_data))

# Add api.jsonl support:
if api_log_entries:
    state_dir = task_dir / "agent" / "serf-state"
    state_dir.mkdir(parents=True, exist_ok=True)
    lines = [json.dumps(e) for e in api_log_entries]
    (state_dir / "api.jsonl").write_text("\n".join(lines) + "\n")
```

Add a new fixture `harbor_job_dir_with_api` that creates api.jsonl files:

```python
@pytest.fixture
def harbor_job_dir_with_api(tmp_path):
    """Job dir with api.jsonl files for API metrics testing."""
    job_root = tmp_path / "api-test" / "api-test"

    api_entries = [
        {"ts": "2026-03-01T12:00:01Z", "session_id": "sess-main", "round": 1,
         "request": {"model": "gpt-5.3-codex", "provider": "openai",
                     "message_count": 3, "tool_count": 5},
         "response": {"model": "gpt-5.3-codex", "finish_reason": "tool_calls",
                      "text_length": 50, "tool_call_count": 1,
                      "usage": {"input_tokens": 500, "output_tokens": 30}},
         "latency_ms": 1200},
        {"ts": "2026-03-01T12:00:03Z", "session_id": "sess-main", "round": 2,
         "request": {"model": "gpt-5.3-codex", "provider": "openai",
                     "message_count": 5, "tool_count": 5},
         "response": {"model": "gpt-5.3-codex", "finish_reason": "tool_calls",
                      "text_length": 0, "tool_call_count": 0,
                      "usage": {"input_tokens": 600, "output_tokens": 4}},
         "latency_ms": 800},
        {"ts": "2026-03-01T12:00:05Z", "session_id": "sess-main", "round": 3,
         "request": {"model": "gpt-5.3-codex", "provider": "openai",
                     "message_count": 7, "tool_count": 5},
         "response": {"model": "gpt-5.3-codex", "finish_reason": "tool_calls",
                      "text_length": 100, "tool_call_count": 1,
                      "usage": {"input_tokens": 700, "output_tokens": 25}},
         "latency_ms": 1500},
    ]

    t1 = job_root / "api-task__abc123"
    _make_task(t1, reward=1.0, transcript_entries=_passing_transcript(),
               api_log_entries=api_entries,
               result_json={"config": {"model": "gpt-5.3-codex"},
                           "started_at": "2026-03-01T12:00:00Z",
                           "finished_at": "2026-03-01T12:05:30Z"})
    return tmp_path / "api-test"
```

**Step 2: Write failing tests for wall_time and API metrics**

```python
class TestWallTime:
    def test_wall_time_from_result_json(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["wall_time_sec"] == 330.0  # 5 min 30 sec


class TestAPIStats:
    def test_api_call_count(self, harbor_job_dir_with_api):
        store = RunStore(harbor_job_dir_with_api)
        stats = compute_task_stats(store, "api-test", "api-task")
        assert stats["api_call_count"] == 3

    def test_total_latency(self, harbor_job_dir_with_api):
        store = RunStore(harbor_job_dir_with_api)
        stats = compute_task_stats(store, "api-test", "api-task")
        assert stats["total_latency_ms"] == 3500  # 1200+800+1500

    def test_avg_latency(self, harbor_job_dir_with_api):
        store = RunStore(harbor_job_dir_with_api)
        stats = compute_task_stats(store, "api-test", "api-task")
        assert abs(stats["avg_latency_ms"] - 1166.7) < 1.0

    def test_empty_response_count(self, harbor_job_dir_with_api):
        store = RunStore(harbor_job_dir_with_api)
        stats = compute_task_stats(store, "api-test", "api-task")
        assert stats["empty_response_count"] == 1  # entry 2 has text_length=0, tool_call_count=0

    def test_no_api_log_returns_nulls(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_task_stats(store, "full-test", "build-widget")
        assert stats["api_call_count"] is None
```

**Step 3: Run tests, verify they fail**

Run: `cd tools/dashboard && python -m pytest test_stats.py -v -k "TestWallTime or TestAPIStats"`
Expected: KeyError or assertion failures

**Step 4: Implement wall time and API metrics**

Update `compute_task_stats()` in `stats.py`:

1. Add wall time computation: parse `started_at` and `finished_at` from `result.json`, compute delta in seconds. Use `data.RunStore._read_result_json()` — but since that's a private method, add a new public method `get_result_json(job_name, task_name)` to `data.py`, OR pass `task` dict (which already has the data accessible). Simplest: read result.json directly in stats module using the task_dir path.

   Actually, the `task` dict from `get_task()` doesn't include result.json data directly. We need to add the task_dir path to the return value, or add result.json fields. The cleanest approach: add a `_find_task_dir()` method to RunStore and use it in stats.py.

   Simpler: add `task_dir` as a string to `_read_task_detail()` return dict. Then stats.py can use it.

2. Add `task_dir` to `_read_task_detail()`:
   In `data.py:223-242`, add `"task_dir": str(task_dir)` to the summary update dict.

3. Parse api.jsonl:
   ```python
   def _compute_api_stats(task_dir_path):
       api_log = Path(task_dir_path) / "agent" / "serf-state" / "api.jsonl"
       if not api_log.is_file():
           return {"api_call_count": None, "total_latency_ms": None,
                   "avg_latency_ms": None, "empty_response_count": None}
       entries = []
       for line in api_log.read_text().splitlines():
           line = line.strip()
           if not line:
               continue
           try:
               entries.append(json.loads(line))
           except json.JSONDecodeError:
               continue
       if not entries:
           return {"api_call_count": 0, "total_latency_ms": 0,
                   "avg_latency_ms": 0, "empty_response_count": 0}
       total_latency = sum(e.get("latency_ms", 0) for e in entries)
       empties = sum(1 for e in entries
                     if e.get("response", {}).get("text_length", 0) == 0
                     and e.get("response", {}).get("tool_call_count", 0) == 0)
       return {
           "api_call_count": len(entries),
           "total_latency_ms": total_latency,
           "avg_latency_ms": round(total_latency / len(entries), 1),
           "empty_response_count": empties,
       }
   ```

4. Parse wall time:
   ```python
   def _compute_wall_time(task_dir_path):
       result_file = Path(task_dir_path) / "result.json"
       if not result_file.is_file():
           return None
       try:
           data = json.loads(result_file.read_text())
       except (json.JSONDecodeError, OSError):
           return None
       started = data.get("started_at", "")
       finished = data.get("finished_at", "")
       if not started or not finished:
           return None
       from datetime import datetime, timezone
       try:
           t0 = datetime.fromisoformat(started.replace("Z", "+00:00"))
           t1 = datetime.fromisoformat(finished.replace("Z", "+00:00"))
           return (t1 - t0).total_seconds()
       except (ValueError, TypeError):
           return None
   ```

5. Merge into `compute_task_stats()` return dict:
   ```python
   wall_time = _compute_wall_time(task["task_dir"])
   api_stats = _compute_api_stats(task["task_dir"])
   result = { ... existing fields ... }
   result["wall_time_sec"] = wall_time
   result.update(api_stats)
   return result
   ```

**Step 5: Run tests, verify they pass**

Run: `cd tools/dashboard && python -m pytest test_stats.py -v`
Expected: All pass

**Step 6: Run full test suite**

Run: `cd tools/dashboard && python -m pytest -v`
Expected: All tests pass (existing + new)

**Step 7: Commit**

```bash
git add tools/dashboard/stats.py tools/dashboard/test_stats.py tools/dashboard/data.py tools/dashboard/conftest.py
git commit -m "feat(dashboard): wall time and API metrics in stats module"
```

---

### Task 3: Stats Module — Per-Run Aggregates and Disk Caching

Aggregate per-task stats into per-run summaries. Add disk caching.

**Files:**
- Modify: `tools/dashboard/stats.py`
- Modify: `tools/dashboard/test_stats.py`

**Step 1: Write failing tests for `compute_run_stats()`**

```python
class TestComputeRunStats:
    def test_run_stats_has_pass_fail_counts(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert stats["passed"] == 1
        assert stats["failed"] == 1
        assert stats["total"] == 2

    def test_run_stats_has_category_counts(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert stats["by_category"]["wrong_answer"] == 1

    def test_run_stats_has_total_rounds(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert stats["total_rounds"] > 0

    def test_run_stats_has_total_tokens(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert stats["total_tokens_in"] > 0

    def test_run_stats_has_task_stats_list(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert len(stats["tasks"]) == 2
        names = {t["task_name"] for t in stats["tasks"]}
        assert names == {"build-widget", "fix-bug"}

    def test_run_not_found_returns_none(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert compute_run_stats(store, "nonexistent") is None
```

**Step 2: Run tests, verify fail**

**Step 3: Implement `compute_run_stats()`**

```python
def compute_run_stats(store, job_name):
    """Compute per-run aggregate stats.

    Returns dict with: passed, failed, total, by_category, total_rounds,
    total_tokens_in, total_tokens_out, tasks (list of per-task stats with
    task_name, passed, failure_category, and all TaskStats fields).
    """
    tasks = store.list_tasks(job_name)
    if tasks is None:
        return None

    result = {
        "passed": 0, "failed": 0, "total": len(tasks),
        "by_category": {},
        "total_rounds": 0, "total_tokens_in": 0, "total_tokens_out": 0,
        "tasks": [],
    }

    for t in tasks:
        task_stats = compute_task_stats(store, job_name, t["task_name"])
        if task_stats is None:
            continue

        entry = dict(task_stats)
        entry["task_name"] = t["task_name"]
        entry["passed"] = t["passed"]
        entry["failure_category"] = t.get("failure_category")
        entry["reward"] = t.get("reward")
        result["tasks"].append(entry)

        if t["passed"]:
            result["passed"] += 1
        else:
            result["failed"] += 1
            cat = t.get("failure_category", "unknown")
            result["by_category"][cat] = result["by_category"].get(cat, 0) + 1

        result["total_rounds"] += task_stats["total_rounds"]
        result["total_tokens_in"] += task_stats["total_tokens_in"]
        result["total_tokens_out"] += task_stats["total_tokens_out"]

    return result
```

**Step 4: Write failing tests for disk caching**

```python
class TestStatsCache:
    def test_cache_written_on_compute(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test", cache_dir=harbor_job_dir / ".cache")
        cache_file = harbor_job_dir / ".cache" / "full-test" / "stats.json"
        assert cache_file.is_file()

    def test_cache_hit_returns_same_data(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        cache_dir = harbor_job_dir / ".cache"
        stats1 = compute_run_stats(store, "full-test", cache_dir=cache_dir)
        stats2 = compute_run_stats(store, "full-test", cache_dir=cache_dir)
        assert stats1 == stats2

    def test_cache_invalidated_on_file_change(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        cache_dir = harbor_job_dir / ".cache"
        stats1 = compute_run_stats(store, "full-test", cache_dir=cache_dir)

        # Touch a transcript file to change mtime
        import time
        time.sleep(0.05)
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"
        transcript = list(sessions_dir.iterdir())[0]
        transcript.write_text(transcript.read_text())  # rewrite, new mtime

        stats2 = compute_run_stats(store, "full-test", cache_dir=cache_dir)
        # Should recompute (same data, but cache was invalidated)
        assert stats2 is not None
```

**Step 5: Implement caching**

Add `cache_dir` parameter to `compute_run_stats()`. Cache key = hash of all transcript + api.jsonl file modification times under the job directory.

```python
import hashlib

def _cache_key(job_dir):
    """Hash of mtimes for all transcript and api.jsonl files."""
    mtimes = []
    for task_dir in sorted(job_dir.iterdir()):
        if not task_dir.is_dir() or "__" not in task_dir.name:
            continue
        sessions = task_dir / "agent" / "serf-state" / "sessions"
        if sessions.is_dir():
            for f in sorted(sessions.iterdir()):
                if f.suffix == ".jsonl":
                    mtimes.append(f"{f}:{f.stat().st_mtime_ns}")
        api_log = task_dir / "agent" / "serf-state" / "api.jsonl"
        if api_log.is_file():
            mtimes.append(f"{api_log}:{api_log.stat().st_mtime_ns}")
    return hashlib.sha256("\n".join(mtimes).encode()).hexdigest()[:16]
```

In `compute_run_stats()`, check cache before computing:
```python
def compute_run_stats(store, job_name, cache_dir=None):
    job_dir = store.data_dir / job_name
    if cache_dir:
        cache_file = Path(cache_dir) / job_name / "stats.json"
        key = _cache_key(job_dir)
        if cache_file.is_file():
            try:
                cached = json.loads(cache_file.read_text())
                if cached.get("_cache_key") == key:
                    cached.pop("_cache_key", None)
                    return cached
            except (json.JSONDecodeError, OSError):
                pass

    # ... compute stats ...

    if cache_dir:
        result["_cache_key"] = key
        cache_file.parent.mkdir(parents=True, exist_ok=True)
        cache_file.write_text(json.dumps(result))
        result.pop("_cache_key", None)

    return result
```

**Step 6: Run tests, verify pass**

Run: `cd tools/dashboard && python -m pytest test_stats.py -v`

**Step 7: Commit**

```bash
git add tools/dashboard/stats.py tools/dashboard/test_stats.py
git commit -m "feat(dashboard): run aggregates and disk caching for stats"
```

---

### Task 4: Stats Module — Cross-Run Task History

Scan all runs for the same task name.

**Files:**
- Modify: `tools/dashboard/stats.py`
- Modify: `tools/dashboard/test_stats.py`

**Step 1: Write failing tests**

```python
class TestTaskHistory:
    def test_task_history_single_run(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        history = compute_task_history(store, "build-widget")
        assert len(history) == 1
        assert history[0]["job_name"] == "full-test"
        assert history[0]["passed"] is True

    def test_task_history_includes_stats(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        history = compute_task_history(store, "build-widget")
        assert "total_rounds" in history[0]
        assert "wasted_rounds" in history[0]

    def test_task_history_multiple_runs(self, harbor_job_dir):
        """Create a second run with the same task."""
        job2 = harbor_job_dir / "second-run"
        t = job2 / "build-widget__xyz999"
        # reuse _make_task from conftest (import it or inline)
        from conftest import _make_task, _passing_transcript
        _make_task(t, reward=0.0, transcript_entries=_passing_transcript(),
                   agent_stdout="[submit_result] submitted\n")

        store = RunStore(harbor_job_dir)
        history = compute_task_history(store, "build-widget")
        assert len(history) == 2

    def test_task_not_in_any_run(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        history = compute_task_history(store, "nonexistent-task")
        assert history == []
```

**Step 2: Run tests, verify fail**

**Step 3: Implement `compute_task_history()`**

```python
def compute_task_history(store, task_name, cache_dir=None):
    """Find a task across all runs, return per-run stats.

    Returns list of dicts sorted by job directory mtime (newest first):
    {job_name, passed, failure_category, total_rounds, wasted_rounds,
     total_tokens_in, total_tokens_out, wall_time_sec}
    """
    results = []
    for run in store.list_runs():
        job_name = run["job_name"]
        tasks = store.list_tasks(job_name)
        if tasks is None:
            continue
        for t in tasks:
            if t["task_name"] == task_name:
                task_stats = compute_task_stats(store, job_name, task_name)
                if task_stats is None:
                    continue
                entry = {
                    "job_name": job_name,
                    "passed": t["passed"],
                    "failure_category": t.get("failure_category"),
                    "total_rounds": task_stats["total_rounds"],
                    "wasted_rounds": task_stats["wasted_rounds"],
                    "total_tokens_in": task_stats["total_tokens_in"],
                    "total_tokens_out": task_stats["total_tokens_out"],
                    "wall_time_sec": task_stats.get("wall_time_sec"),
                }
                # Get job dir mtime for sorting
                job_dir = store.data_dir / job_name
                try:
                    entry["_mtime"] = job_dir.stat().st_mtime
                except OSError:
                    entry["_mtime"] = 0
                results.append(entry)
                break  # found the task in this run

    results.sort(key=lambda r: r.get("_mtime", 0), reverse=True)
    for r in results:
        r.pop("_mtime", None)
    return results
```

**Step 4: Run tests, verify pass**

**Step 5: Commit**

```bash
git add tools/dashboard/stats.py tools/dashboard/test_stats.py
git commit -m "feat(dashboard): cross-run task history in stats module"
```

---

### Task 5: Server — Stats-Enriched API and New Endpoints

Wire stats into existing endpoints. Add comparison, task history, and raw file endpoints.

**Files:**
- Modify: `tools/dashboard/server.py`
- Modify: `tools/dashboard/data.py:223-242` (add task_dir to detail)
- Create or modify: `tools/dashboard/test_server.py`

**Step 1: Add `task_dir` to data layer**

In `data.py:_read_task_detail()` (line 223), add `"task_dir": str(task_dir)` to the `summary.update()` call.

**Step 2: Write failing tests for enriched API responses**

In `test_server.py`:

```python
from fastapi.testclient import TestClient

def test_run_tasks_have_stats(harbor_job_dir):
    """GET /api/runs/{job}/tasks returns stats-enriched task list."""
    import server as srv
    srv.store = RunStore(harbor_job_dir)
    client = TestClient(srv.app)
    resp = client.get("/api/runs/full-test/tasks", headers={"Accept": "application/json"})
    tasks = resp.json()
    assert tasks[0]["total_rounds"] > 0
    assert "wall_time_sec" in tasks[0]

def test_compare_endpoint(harbor_job_dir):
    """GET /api/compare returns diff between two runs."""
    # Create second run
    # ...
    import server as srv
    srv.store = RunStore(harbor_job_dir)
    client = TestClient(srv.app)
    resp = client.get("/api/compare?a=full-test&b=second-run",
                      headers={"Accept": "application/json"})
    assert resp.status_code == 200
    data = resp.json()
    assert "improved" in data
    assert "regressed" in data

def test_task_history_endpoint(harbor_job_dir):
    """GET /api/tasks/{task_name}/history returns cross-run data."""
    import server as srv
    srv.store = RunStore(harbor_job_dir)
    client = TestClient(srv.app)
    resp = client.get("/api/tasks/build-widget/history",
                      headers={"Accept": "application/json"})
    assert resp.status_code == 200
    history = resp.json()
    assert len(history) >= 1

def test_raw_file_endpoint(harbor_job_dir):
    """GET /raw/{path} serves a rendered file."""
    import server as srv
    srv.store = RunStore(harbor_job_dir)
    client = TestClient(srv.app)
    # Request result.json for a task
    resp = client.get("/raw/full-test/build-widget__abc123/result.json")
    assert resp.status_code == 200
    assert "text/html" in resp.headers["content-type"]

def test_raw_file_blocks_path_traversal(harbor_job_dir):
    """Raw endpoint rejects paths that escape data_dir."""
    import server as srv
    srv.store = RunStore(harbor_job_dir)
    client = TestClient(srv.app)
    resp = client.get("/raw/../../etc/passwd")
    assert resp.status_code == 403
```

**Step 3: Run tests, verify fail**

**Step 4: Implement server changes**

In `server.py`:

1. Import stats module:
   ```python
   from stats import compute_run_stats, compute_task_stats, compute_task_history
   ```

2. Add cache_dir setup:
   ```python
   _cache_dir = os.path.join(_data_dir, ".cache")
   ```

3. Modify `list_tasks()` endpoint to return stats-enriched tasks:
   ```python
   @app.get("/api/runs/{job_name}/tasks")
   def list_tasks(job_name: str, request: Request):
       run_stats = compute_run_stats(store, job_name, cache_dir=_cache_dir)
       if run_stats is None:
           return JSONResponse({"error": "not found"}, status_code=404)
       if _wants_json(request):
           return JSONResponse(run_stats["tasks"])
       # markdown fallback
       tasks = store.list_tasks(job_name)
       return _md_response(render_run_detail(store.get_run(job_name), tasks))
   ```

4. Add comparison endpoint:
   ```python
   @app.get("/api/compare")
   def compare_runs(a: str, b: str):
       stats_a = compute_run_stats(store, a, cache_dir=_cache_dir)
       stats_b = compute_run_stats(store, b, cache_dir=_cache_dir)
       if stats_a is None or stats_b is None:
           return JSONResponse({"error": "run not found"}, status_code=404)

       tasks_a = {t["task_name"]: t for t in stats_a["tasks"]}
       tasks_b = {t["task_name"]: t for t in stats_b["tasks"]}
       all_tasks = set(tasks_a) | set(tasks_b)

       improved, regressed, stable_pass, stable_fail = [], [], [], []
       only_a, only_b = [], []

       for name in sorted(all_tasks):
           in_a = name in tasks_a
           in_b = name in tasks_b
           if in_a and in_b:
               pa, pb = tasks_a[name]["passed"], tasks_b[name]["passed"]
               entry = {"task": name, "a": "pass" if pa else "fail",
                        "b": "pass" if pb else "fail"}
               if not pa and pb:
                   improved.append(entry)
               elif pa and not pb:
                   regressed.append(entry)
               elif pa and pb:
                   stable_pass.append(entry)
               else:
                   stable_fail.append(entry)
           elif in_a:
               only_a.append(name)
           else:
               only_b.append(name)

       return JSONResponse({
           "run_a": {"job_name": a, "passed": stats_a["passed"], "total": stats_a["total"]},
           "run_b": {"job_name": b, "passed": stats_b["passed"], "total": stats_b["total"]},
           "improved": improved,
           "regressed": regressed,
           "stable_pass": stable_pass,
           "stable_fail": stable_fail,
           "only_a": only_a,
           "only_b": only_b,
       })
   ```

5. Add task history endpoint:
   ```python
   @app.get("/api/tasks/{task_name}/history")
   def task_history(task_name: str):
       history = compute_task_history(store, task_name)
       return JSONResponse(history)
   ```

6. Add raw file endpoint:
   ```python
   from fastapi.responses import HTMLResponse
   import html as html_mod

   @app.get("/raw/{file_path:path}")
   def raw_file(file_path: str):
       full_path = (store.data_dir / file_path).resolve()
       # Block path traversal
       if not str(full_path).startswith(str(store.data_dir.resolve())):
           return JSONResponse({"error": "forbidden"}, status_code=403)
       if not full_path.is_file():
           return JSONResponse({"error": "not found"}, status_code=404)

       content = full_path.read_text(errors="replace")
       ext = full_path.suffix.lower()

       if ext == ".json":
           try:
               parsed = json.loads(content)
               pretty = json.dumps(parsed, indent=2)
           except json.JSONDecodeError:
               pretty = content
           body = f"<pre>{html_mod.escape(pretty)}</pre>"
       elif ext == ".jsonl":
           parts = []
           for line in content.splitlines():
               line = line.strip()
               if not line:
                   continue
               try:
                   parsed = json.loads(line)
                   parts.append(f"<pre>{html_mod.escape(json.dumps(parsed, indent=2))}</pre>")
               except json.JSONDecodeError:
                   parts.append(f"<pre>{html_mod.escape(line)}</pre>")
           body = "<hr>".join(parts)
       else:
           body = f"<pre>{html_mod.escape(content)}</pre>"

       page = f"""<!DOCTYPE html>
   <html><head><meta charset="utf-8"><title>{html_mod.escape(file_path)}</title>
   <style>body{{font-family:monospace;margin:24px;background:#fafaf8}}
   pre{{white-space:pre-wrap;word-break:break-word;line-height:1.5}}
   hr{{border:none;border-top:1px solid #ddd;margin:16px 0}}</style>
   </head><body><h3>{html_mod.escape(file_path)}</h3>{body}</body></html>"""
       return HTMLResponse(page)
   ```

**Step 5: Run tests, verify pass**

Run: `cd tools/dashboard && python -m pytest test_server.py -v`

**Step 6: Run full suite**

Run: `cd tools/dashboard && python -m pytest -v`

**Step 7: Commit**

```bash
git add tools/dashboard/server.py tools/dashboard/data.py tools/dashboard/test_server.py
git commit -m "feat(dashboard): stats-enriched API, comparison, history, raw file endpoints"
```

---

### Task 6: Frontend — Run Detail as Data Table

Replace the simple task list with a sortable data table showing computed metric columns.

**Files:**
- Modify: `tools/dashboard/static/app.js` (lines 247-454, `renderRunDetail()`)
- Modify: `tools/dashboard/static/style.css`

**Step 1: Define the column schema**

The API now returns stats-enriched task objects. Update `renderRunDetail()` to display these columns:

| Column | Key | Format |
|--------|-----|--------|
| Task | task_name | link |
| Result | passed | dot + Pass/Fail |
| Category | failure_category | label |
| Rounds | total_rounds | number |
| Wasted | wasted_rounds | number |
| Tokens | total_tokens_in + total_tokens_out | `Xk in / Yk out` |
| Sessions | session_count | number |
| Depth | max_depth | number |
| Submit @ | first_submit_round | number or `-` |
| Wall Time | wall_time_sec | `Xm Ys` |
| History | (from task history) | colored dots |

**Step 2: Implement sortable data table**

Rewrite `renderRunDetail()`:

1. Fetch from `/api/runs/{job}/tasks` (which now returns stats-enriched objects).
2. Build `columns` array with `{key, label, format, sortFn}`.
3. Render `<thead>` with click-to-sort headers (reuse existing sort logic but generalize).
4. Render `<tbody>` from sorted/filtered tasks.
5. Keep existing filter bar (All/Pass/Fail/Timeout/Wrong Answer/No Submit).
6. Add search box that filters by task name.

For the History column, add a separate fetch to `/api/tasks/{name}/history` for each visible task (lazy-load, or batch). Simpler: fetch history dots for all tasks from the run stats — but the server doesn't include cross-run history in the task list. Add a lightweight endpoint or skip history dots for v1 and add later.

**Decision:** Skip history dots in this task. Add them as a follow-up after the core pages work. The column header exists but shows `-` until history data is available. This keeps the task focused.

**Step 3: Add search box**

Above the filter bar, add a text input:
```javascript
const searchBox = h('input', {
    type: 'text',
    placeholder: 'Search tasks...',
    className: 'search-box',
    onInput: (e) => { searchTerm = e.target.value.toLowerCase(); renderTable(); }
});
```

Filter function:
```javascript
function matchesSearch(task) {
    if (!searchTerm) return true;
    return task.task_name.toLowerCase().includes(searchTerm);
}
```

**Step 4: Format helpers**

```javascript
function formatWallTime(sec) {
    if (sec == null) return '-';
    const m = Math.floor(sec / 60);
    const s = Math.round(sec % 60);
    return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

function formatTaskTokens(tokIn, tokOut) {
    if (!tokIn && !tokOut) return '-';
    return `${(tokIn / 1000).toFixed(1)}k / ${(tokOut / 1000).toFixed(1)}k`;
}
```

**Step 5: CSS for data table**

Add styles for `.search-box`, ensure all columns have appropriate widths, add `.numeric` class for right-aligned number columns.

```css
.search-box {
    width: 100%;
    max-width: 300px;
    padding: 8px 12px;
    font-size: 13px;
    font-family: inherit;
    border: 1px solid rgba(0,0,0,0.12);
    border-radius: 6px;
    margin-bottom: 12px;
    outline: none;
}
.search-box:focus {
    border-color: rgba(0,0,0,0.3);
}
td.numeric {
    text-align: right;
    font-variant-numeric: tabular-nums;
}
```

**Step 6: Verify manually**

Start dashboard locally, browse to a run, verify all columns render and sorting works.

**Step 7: Commit**

```bash
git add tools/dashboard/static/app.js tools/dashboard/static/style.css
git commit -m "feat(dashboard): run detail as sortable data table with search"
```

---

### Task 7: Frontend — Task Detail Redesign

Redesign the task detail page: summary card, two-state expand, search within trajectory, raw file links.

**Files:**
- Modify: `tools/dashboard/static/app.js` (lines 460-825, `renderTaskDetail()` + trajectory rendering)
- Modify: `tools/dashboard/static/style.css`

**Step 1: Summary card**

At the top of the task detail page, show key metrics (rounds, wasted, tokens, sessions, wall time, submitted value, outcome badge) all visible without clicking:

```javascript
const summaryCard = h('div', { className: 'summary-card' },
    h('div', { className: 'summary-metrics' },
        metricBox('Rounds', stats.total_rounds),
        metricBox('Wasted', stats.wasted_rounds),
        metricBox('Tokens In', formatK(stats.total_tokens_in)),
        metricBox('Tokens Out', formatK(stats.total_tokens_out)),
        metricBox('Sessions', stats.session_count),
        metricBox('Depth', stats.max_depth),
        metricBox('Wall Time', formatWallTime(stats.wall_time_sec)),
        metricBox('Submit @', stats.first_submit_round || '-'),
    ),
    stats.submitted_value ? h('div', { className: 'submitted-value' },
        h('span', { className: 'label' }, 'Submitted: '),
        h('code', null, truncate(stats.submitted_value, 200))
    ) : null
);
```

The stats come from the existing task detail API (which already includes trajectory). We need to compute stats client-side from the trajectory OR have the server include stats in the task detail response. Better: have the server include stats. Modify `get_task()` in server.py to add stats fields.

**Step 2: Two-state expand for verifier and stdout**

Replace the current Expand/Collapse toggle with two-state: click the header to toggle between one-liner and full content. No intermediate states.

Verifier: default expanded (full content visible). Click header to collapse to one line ("FAIL: test_widget..."). Click again to expand.

Stdout: default collapsed (one-liner: first 80 chars). Click to expand fully.

```javascript
function twoStateSection(title, content, defaultOpen, accentColor) {
    const oneLiner = content.split('\n')[0].slice(0, 100);
    const section = h('div', { className: 'card two-state' + (accentColor ? ` accent-${accentColor}` : '') });
    const header = h('div', { className: 'card-header clickable' },
        h('span', { className: 'card-title' }, title),
        h('span', { className: 'one-liner' }, oneLiner)
    );
    const body = h('pre', { className: 'card-body-pre' }, content);
    section.appendChild(header);
    section.appendChild(body);

    if (!defaultOpen) {
        section.classList.add('collapsed');
    }

    header.addEventListener('click', () => {
        section.classList.toggle('collapsed');
    });
    return section;
}
```

CSS:
```css
.two-state.collapsed .card-body-pre { display: none; }
.two-state.collapsed .one-liner { display: inline; }
.two-state:not(.collapsed) .one-liner { display: none; }
.two-state .card-header.clickable { cursor: pointer; }
.two-state.accent-green { border-left: 3px solid #18A34A; }
.two-state.accent-red { border-left: 3px solid #DC2626; }
```

**Step 3: Two-state expand for trajectory rounds**

Remove `makeExpandable()` and the nested Show more buttons entirely. Each round is either a one-liner (collapsed) or fully expanded with all content visible:

- Collapsed: `#4 EDIT apply_patch: main.py` (the existing round-header)
- Expanded: full assistant text + all tool call args + all tool results, no truncation, scrollable

Remove `makeExpandable()` function. In `renderToolCall()`, always show full content:
```javascript
function renderToolCall(tc, tr) {
    // ... same header ...
    if (argsStr) {
        block.appendChild(h('pre', { className: 'tool-content' }, argsStr));
    }
    if (tr) {
        // ... result header ...
        if (resultContent) {
            const cls = isError ? 'tool-content tool-error' : 'tool-content';
            block.appendChild(h('pre', { className: cls }, resultContent));
        }
    }
    return block;
}
```

CSS: remove `.collapsed` and `.expandable-wrapper` and `.expand-btn` styles. Tool content and round detail use `max-height: none; overflow-y: auto;` when expanded.

**Step 4: Search within trajectory**

Add a search box above the timeline that filters rounds by text content (tool names, args, results, assistant text):

```javascript
const trajSearch = h('input', {
    type: 'text',
    placeholder: 'Search rounds...',
    className: 'search-box',
    onInput: (e) => {
        const term = e.target.value.toLowerCase();
        const rounds = trajectorySection.querySelectorAll('.timeline-round');
        rounds.forEach(r => {
            r.style.display = r.textContent.toLowerCase().includes(term) || !term ? '' : 'none';
        });
    }
});
```

**Step 5: Raw file links**

Add small link icons next to section headers. Each opens the raw file in a new tab via the `/raw/` endpoint.

```javascript
function rawLink(path) {
    return h('a', {
        href: `/raw/${encodeURIComponent(path)}`,
        target: '_blank',
        className: 'raw-link',
        title: 'Open raw file'
    }, '\u2197');  // ↗
}
```

Add links next to: Verifier (links to test-stdout.txt), Agent Stdout (links to stdout.txt), Trajectory header (links to transcript files), task header (links to result.json, config.json).

The task detail API needs to include file paths. Add `task_dir` relative path or specific file paths to the task detail response. The simplest approach: server includes `raw_files` dict with relative paths:

```python
raw_files = {}
if (task_dir / "result.json").is_file():
    raw_files["result"] = f"{job_name}/{task_dir.name}/result.json"
# ... etc
```

**Step 6: Commit**

```bash
git add tools/dashboard/static/app.js tools/dashboard/static/style.css tools/dashboard/server.py
git commit -m "feat(dashboard): task detail redesign — summary card, two-state, search, raw links"
```

---

### Task 8: Frontend — Run Comparison Page

New page: pick two runs, see the diff.

**Files:**
- Modify: `tools/dashboard/static/app.js`
- Modify: `tools/dashboard/static/style.css`

**Step 1: Add route**

In `route()`, add:
```javascript
if ((m = hash.match(/^#\/compare$/))) {
    renderComparison(app);
} else if ((m = hash.match(/^#\/compare\?/))) {
    // parse params from hash
}
```

Or simpler: use `#/compare` and read dropdowns from the page state.

**Step 2: Implement `renderComparison()`**

1. Fetch `/api/runs` to populate two `<select>` dropdowns.
2. On selection change, fetch `/api/compare?a={run_a}&b={run_b}`.
3. Show summary line: "Run B: 45/89 (+4 vs Run A). 7 improved, 3 regressed."
4. Show table with one row per task, columns: Task, Run A result, Run B result.
5. Color rows: green (improved), red (regressed), neutral (same).
6. Sort by delta (regressions first, then improvements, then stable).
7. Tasks unique to one run in a separate section below.

**Step 3: Add navigation link**

In the breadcrumb or as a button on the run list page, add a "Compare" link to `#/compare`.

**Step 4: CSS for comparison page**

```css
.compare-row.improved { background: rgba(24,163,74,0.05); }
.compare-row.regressed { background: rgba(220,38,38,0.05); }
.compare-select {
    padding: 8px 12px;
    font-size: 14px;
    font-family: inherit;
    border: 1px solid rgba(0,0,0,0.12);
    border-radius: 6px;
    min-width: 200px;
}
```

**Step 5: Commit**

```bash
git add tools/dashboard/static/app.js tools/dashboard/static/style.css
git commit -m "feat(dashboard): run comparison page"
```

---

### Task 9: Frontend — Task History Page

New page: one task across all runs.

**Files:**
- Modify: `tools/dashboard/static/app.js`
- Modify: `tools/dashboard/static/style.css`

**Step 1: Add route**

```javascript
if ((m = hash.match(/^#\/tasks\/([^/]+)\/history$/))) {
    renderTaskHistory(app, decodeURIComponent(m[1]));
}
```

**Step 2: Implement `renderTaskHistory()`**

1. Fetch `/api/tasks/{task_name}/history`.
2. Show table: one row per run, sorted newest first.
3. Columns: Job Name (link to task detail), Result badge, Category, Rounds, Wasted, Tokens, Wall Time.
4. Each row can expand to show verifier output (two-state: collapsed by default).

**Step 3: Add history link from task detail and run detail pages**

In the task detail header, add a link: "View history across runs" → `#/tasks/{task_name}/history`.

In the run detail data table, the History column (if implemented later) links to this page.

**Step 4: Commit**

```bash
git add tools/dashboard/static/app.js tools/dashboard/static/style.css
git commit -m "feat(dashboard): task history page — one task across all runs"
```

---

### Task 10: Integration Testing and Deployment

Verify everything works against real data and deploy.

**Files:**
- No new files

**Step 1: Run full test suite**

```bash
cd tools/dashboard && python -m pytest -v
```

All tests must pass.

**Step 2: Manual testing against real data**

```bash
cd tools/dashboard
DASHBOARD_DATA_DIR=/tmp/full-mk6 python server.py --data-dir /tmp/full-mk6
```

If we don't have data locally, rsync from magic-kingdom first:
```bash
rsync -av jesse@magic-kingdom:/tmp/full-mk6/ /tmp/full-mk6/
```

Open `http://localhost:8080` and verify:
- Run list shows runs with correct pass rates
- Run detail shows data table with all metric columns, sorting works, search works, filter works
- Task detail shows summary card, two-state expand for verifier/stdout/rounds, search works, raw file links work
- Comparison page: select two runs, see diff with colored rows
- Task history: pick a task, see it across runs
- Raw file links open rendered files in new tabs

**Step 3: Deploy to magic-kingdom**

```bash
scp tools/dashboard/stats.py tools/dashboard/data.py tools/dashboard/server.py \
    tools/dashboard/static/app.js tools/dashboard/static/style.css tools/dashboard/static/index.html \
    jesse@magic-kingdom:/home/jesse/git/eval-dashboard/

scp tools/dashboard/stats.py jesse@magic-kingdom:/home/jesse/git/eval-dashboard/

# Restart
ssh jesse@magic-kingdom 'kill $(lsof -ti:8080) 2>/dev/null; cd /home/jesse/git/eval-dashboard && nohup .venv/bin/python server.py --data-dir /tmp/full-mk6 --port 8080 > /tmp/dashboard.log 2>&1 &'
```

**Step 4: Verify deployment**

```bash
curl -s http://magic-kingdom:8080/health
curl -s -H 'Accept: application/json' http://magic-kingdom:8080/api/runs | python3 -m json.tool | head -20
```

**Step 5: Commit any final fixes**

```bash
git add -p  # review changes
git commit -m "fix(dashboard): integration fixes from deployment testing"
```

---

## Implementation Order

1. **Task 1**: Stats — per-task metrics (foundation)
2. **Task 2**: Stats — wall time + API metrics
3. **Task 3**: Stats — run aggregates + caching
4. **Task 4**: Stats — cross-run task history
5. **Task 5**: Server — enriched API + new endpoints
6. **Task 6**: Frontend — run detail data table
7. **Task 7**: Frontend — task detail redesign
8. **Task 8**: Frontend — comparison page
9. **Task 9**: Frontend — task history page
10. **Task 10**: Integration test + deploy

Tasks 1-4 are backend-only (testable with pytest). Tasks 5-9 build on each other sequentially. Task 10 is verification.
