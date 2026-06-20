"""Tests for compare_runs.py — cross-run comparison tool."""

import os
import subprocess
import sys
import tempfile

from compare_runs import compare_runs


def _make_run(tmp_dir, run_name, tasks):
    """Create a fake archive run directory.

    tasks: {"task-a": [1.0, 0.0], ...} — maps task name to list of rewards per rep.
    """
    run_dir = os.path.join(tmp_dir, "runs", run_name)
    for task, rewards in tasks.items():
        for i, r in enumerate(rewards, 1):
            rep_dir = os.path.join(run_dir, "tasks", task, f"rep-{i}")
            os.makedirs(rep_dir, exist_ok=True)
            with open(os.path.join(rep_dir, "reward.txt"), "w") as f:
                f.write(str(r))
            with open(os.path.join(rep_dir, "failure_category.txt"), "w") as f:
                f.write("" if r >= 1.0 else "wrong_answer")
    return run_dir


def test_compare_identical():
    with tempfile.TemporaryDirectory() as d:
        tasks = {"a": [1.0], "b": [0.0], "c": [1.0]}
        run_a = _make_run(d, "run-a", tasks)
        run_b = _make_run(d, "run-b", tasks)
        result = compare_runs(run_a, run_b)
        assert result["delta_majority"] == 0.0
        assert result["bootstrap_ci"][0] <= 0.0 <= result["bootstrap_ci"][1]
        assert len(result["improvements"]) == 0
        assert len(result["regressions"]) == 0


def test_compare_improvement():
    with tempfile.TemporaryDirectory() as d:
        run_a = _make_run(d, "run-a", {"a": [0.0], "b": [0.0], "c": [1.0], "d": [0.0], "e": [1.0]})
        run_b = _make_run(d, "run-b", {"a": [1.0], "b": [1.0], "c": [1.0], "d": [0.0], "e": [1.0]})
        result = compare_runs(run_a, run_b)
        assert result["delta_majority"] > 0
        assert "a" in result["improvements"]
        assert "b" in result["improvements"]
        assert len(result["regressions"]) == 0


def test_compare_regression():
    with tempfile.TemporaryDirectory() as d:
        run_a = _make_run(d, "run-a", {"a": [1.0], "b": [1.0], "c": [0.0]})
        run_b = _make_run(d, "run-b", {"a": [0.0], "b": [1.0], "c": [0.0]})
        result = compare_runs(run_a, run_b)
        assert result["delta_majority"] < 0
        assert "a" in result["regressions"]
        assert len(result["improvements"]) == 0


def test_compare_has_mcnemars():
    with tempfile.TemporaryDirectory() as d:
        run_a = _make_run(d, "run-a", {"a": [1.0], "b": [0.0]})
        run_b = _make_run(d, "run-b", {"a": [1.0], "b": [0.0]})
        result = compare_runs(run_a, run_b)
        assert "mcnemars_p" in result
        assert "mcnemars_chi2" in result


def test_compare_per_task_table():
    with tempfile.TemporaryDirectory() as d:
        run_a = _make_run(d, "run-a", {"a": [1.0], "b": [0.0]})
        run_b = _make_run(d, "run-b", {"a": [0.0], "b": [1.0]})
        result = compare_runs(run_a, run_b)
        assert len(result["tasks"]) == 2
        task_a = next(t for t in result["tasks"] if t["name"] == "a")
        assert task_a["pass_a"] is True
        assert task_a["pass_b"] is False
        assert task_a["status"] == "regression"


def test_compare_mismatched_tasks():
    """Tasks only in one run should be reported separately."""
    with tempfile.TemporaryDirectory() as d:
        run_a = _make_run(d, "run-a", {"shared": [1.0], "only-a": [1.0]})
        run_b = _make_run(d, "run-b", {"shared": [1.0], "only-b": [0.0]})
        result = compare_runs(run_a, run_b)
        # Only shared tasks are compared
        assert result["shared_task_count"] == 1
        assert len(result.get("only_in_a", [])) == 1
        assert len(result.get("only_in_b", [])) == 1


def test_compare_multi_rep():
    """With multiple reps, majority vote determines pass."""
    with tempfile.TemporaryDirectory() as d:
        run_a = _make_run(d, "run-a", {"a": [1.0, 0.0, 1.0], "b": [0.0, 0.0, 1.0]})
        run_b = _make_run(d, "run-b", {"a": [1.0, 0.0, 1.0], "b": [1.0, 1.0, 0.0]})
        result = compare_runs(run_a, run_b)
        # task a: majority pass in both -> stable
        # task b: majority fail in A (1/3), majority pass in B (2/3) -> improvement
        assert "b" in result["improvements"]


def test_cli_output():
    """CLI should print a readable table."""
    with tempfile.TemporaryDirectory() as d:
        run_a = _make_run(d, "run-a", {"a": [1.0], "b": [0.0]})
        run_b = _make_run(d, "run-b", {"a": [1.0], "b": [1.0]})
        result = subprocess.run(
            [sys.executable, "compare_runs.py", run_a, run_b],
            capture_output=True, text=True, cwd=os.path.dirname(__file__) or "."
        )
        assert result.returncode == 0
        assert "b" in result.stdout  # improved task should appear
