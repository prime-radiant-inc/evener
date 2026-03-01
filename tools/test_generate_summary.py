"""Tests for generate_summary.py — summary generation from archive directories.

Written BEFORE implementation (TDD).
"""

import json
import os
import subprocess
import sys
import tempfile

from generate_summary import generate_summary


def _make_fixture(tmp_dir, tasks):
    """Create a minimal archive fixture.

    tasks is a dict like {"build-cython-ext": [1.0, 0.0], "fix-vuln": [1.0, 1.0]}
    Rewards of 0.0 get failure_category "wrong_answer"; rewards > 0 get empty string.
    """
    for task_name, rewards in tasks.items():
        for i, reward in enumerate(rewards, 1):
            rep_dir = os.path.join(tmp_dir, "tasks", task_name, f"rep-{i}")
            os.makedirs(rep_dir, exist_ok=True)
            with open(os.path.join(rep_dir, "reward.txt"), "w") as f:
                f.write(str(reward))
            with open(os.path.join(rep_dir, "failure_category.txt"), "w") as f:
                f.write("wrong_answer" if reward == 0.0 else "")


def _make_fixture_with_categories(tmp_dir, tasks):
    """Create an archive fixture with explicit failure categories.

    tasks is a dict like {"t1": [(1.0, ""), (0.0, "timeout")]}
    Each value is a list of (reward, failure_category) tuples.
    """
    for task_name, reps in tasks.items():
        for i, (reward, category) in enumerate(reps, 1):
            rep_dir = os.path.join(tmp_dir, "tasks", task_name, f"rep-{i}")
            os.makedirs(rep_dir, exist_ok=True)
            with open(os.path.join(rep_dir, "reward.txt"), "w") as f:
                f.write(str(reward))
            with open(os.path.join(rep_dir, "failure_category.txt"), "w") as f:
                f.write(category)


# ---------------------------------------------------------------------------
# Basic summary structure
# ---------------------------------------------------------------------------


class TestBasicSummary:
    """Core summary generation from archive directories."""

    def test_schema_version(self):
        """Summary includes schema_version = 1."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0]})
            summary = generate_summary(d, "test")
            assert summary["schema_version"] == 1

    def test_run_id(self):
        """Summary includes the provided run_id."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0]})
            summary = generate_summary(d, "my-run-123")
            assert summary["run_id"] == "my-run-123"

    def test_task_count(self):
        """Task count matches the number of task directories."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 0.0, 1.0], "task-b": [0.0, 0.0, 0.0]})
            summary = generate_summary(d, "test")
            assert summary["task_count"] == 2

    def test_majority_pass_count(self):
        """Majority pass: task-a has 2/3 pass (majority), task-b has 0/3."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 0.0, 1.0], "task-b": [0.0, 0.0, 0.0]})
            summary = generate_summary(d, "test")
            assert summary["pass_count_majority"] == 1

    def test_strict_pass_count(self):
        """Strict pass requires all reps to pass."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 0.0, 1.0], "task-b": [0.0, 0.0, 0.0]})
            summary = generate_summary(d, "test")
            assert summary["pass_count_strict"] == 0

    def test_any_pass_count(self):
        """Any pass: at least one rep passes."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 0.0, 1.0], "task-b": [0.0, 0.0, 0.0]})
            summary = generate_summary(d, "test")
            assert summary["pass_count_any"] == 1

    def test_pass_rate_majority(self):
        """Pass rate = pass_count / task_count."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 0.0, 1.0], "task-b": [0.0, 0.0, 0.0]})
            summary = generate_summary(d, "test")
            assert summary["pass_rate_majority"] == 0.5

    def test_tasks_list_length(self):
        """Tasks list has one entry per task."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 0.0, 1.0], "task-b": [0.0, 0.0, 0.0]})
            summary = generate_summary(d, "test")
            assert len(summary["tasks"]) == 2


# ---------------------------------------------------------------------------
# Per-task detail
# ---------------------------------------------------------------------------


class TestPerTaskDetail:
    """Per-task entries in the summary."""

    def test_task_name(self):
        """Task entry includes the task name."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 1.0]})
            summary = generate_summary(d, "test")
            task = summary["tasks"][0]
            assert task["name"] == "task-a"

    def test_pass_majority(self):
        """Task with 2/2 pass has pass_majority=True."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 1.0]})
            summary = generate_summary(d, "test")
            task = summary["tasks"][0]
            assert task["pass_majority"] is True

    def test_pass_strict(self):
        """Task with all reps passing has pass_strict=True."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 1.0]})
            summary = generate_summary(d, "test")
            task = summary["tasks"][0]
            assert task["pass_strict"] is True

    def test_reps_list(self):
        """Task entry includes a list of per-rep results."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 1.0]})
            summary = generate_summary(d, "test")
            task = summary["tasks"][0]
            assert len(task["reps"]) == 2

    def test_rep_structure(self):
        """Each rep has rep number, reward, and failure_category."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 0.0]})
            summary = generate_summary(d, "test")
            task = summary["tasks"][0]
            reps = task["reps"]
            assert reps[0]["rep"] == 1
            assert reps[0]["reward"] == 1.0
            assert reps[0]["failure_category"] is None
            assert reps[1]["rep"] == 2
            assert reps[1]["reward"] == 0.0
            assert reps[1]["failure_category"] == "wrong_answer"

    def test_tasks_sorted_alphabetically(self):
        """Tasks are sorted alphabetically by name."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"zeta": [1.0], "alpha": [0.0], "middle": [1.0]})
            summary = generate_summary(d, "test")
            names = [t["name"] for t in summary["tasks"]]
            assert names == ["alpha", "middle", "zeta"]


# ---------------------------------------------------------------------------
# Failure categories
# ---------------------------------------------------------------------------


class TestFailureCategories:
    """Failure category aggregation across all reps."""

    def test_counts_across_all_reps(self):
        """Failure categories are counted across all reps, not just per-task."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture_with_categories(d, {
                "t1": [(0.0, "timeout"), (0.0, "wrong_answer")],
                "t2": [(1.0, ""), (0.0, "no_submit")],
            })
            summary = generate_summary(d, "test")
            fc = summary["failure_categories"]
            assert fc["timeout"] == 1
            assert fc["wrong_answer"] == 1
            assert fc["no_submit"] == 1

    def test_no_failures(self):
        """All passing reps produce empty failure_categories."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 1.0]})
            summary = generate_summary(d, "test")
            assert summary["failure_categories"] == {}

    def test_api_error_category(self):
        """api_error failure category is tracked."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture_with_categories(d, {
                "t1": [(0.0, "api_error")],
            })
            summary = generate_summary(d, "test")
            assert summary["failure_categories"]["api_error"] == 1


# ---------------------------------------------------------------------------
# Edge cases
# ---------------------------------------------------------------------------


class TestEdgeCases:
    """Edge cases and error handling."""

    def test_empty_archive_raises(self):
        """No tasks/ directory raises FileNotFoundError."""
        with tempfile.TemporaryDirectory() as d:
            try:
                generate_summary(d, "empty")
                assert False, "Should have raised FileNotFoundError"
            except FileNotFoundError:
                pass

    def test_missing_failure_category_file(self):
        """Missing failure_category.txt is handled gracefully (None)."""
        with tempfile.TemporaryDirectory() as d:
            rep_dir = os.path.join(d, "tasks", "task-a", "rep-1")
            os.makedirs(rep_dir)
            with open(os.path.join(rep_dir, "reward.txt"), "w") as f:
                f.write("1.0")
            # No failure_category.txt
            summary = generate_summary(d, "test")
            assert summary["tasks"][0]["reps"][0]["failure_category"] is None

    def test_non_dir_entries_in_tasks_ignored(self):
        """Regular files in tasks/ directory are ignored."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0]})
            # Create a stray file in tasks/
            with open(os.path.join(d, "tasks", "README.txt"), "w") as f:
                f.write("ignore me")
            summary = generate_summary(d, "test")
            assert summary["task_count"] == 1

    def test_rep_without_reward_ignored(self):
        """Rep directories without reward.txt are skipped."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0]})
            # Create a rep dir without reward.txt
            os.makedirs(os.path.join(d, "tasks", "task-a", "rep-2"))
            summary = generate_summary(d, "test")
            assert summary["tasks"][0]["reps_total"] == 1


# ---------------------------------------------------------------------------
# Wilson CI on summary
# ---------------------------------------------------------------------------


class TestWilsonCI:
    """Pass rate CI in the summary."""

    def test_ci_present_and_valid(self):
        """CI should be a 2-element list containing the pass rate."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {
                "t1": [1.0], "t2": [0.0], "t3": [1.0], "t4": [1.0],
            })
            summary = generate_summary(d, "test")
            ci = summary["pass_rate_majority_ci_95"]
            assert len(ci) == 2
            assert ci[0] <= summary["pass_rate_majority"] <= ci[1]

    def test_ci_bounds_within_0_1(self):
        """CI bounds are always within [0, 1]."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"t1": [1.0], "t2": [1.0]})
            summary = generate_summary(d, "test")
            ci = summary["pass_rate_majority_ci_95"]
            assert 0.0 <= ci[0] <= 1.0
            assert 0.0 <= ci[1] <= 1.0


# ---------------------------------------------------------------------------
# CLI mode
# ---------------------------------------------------------------------------


class TestCLI:
    """CLI invocation produces valid JSON to stdout."""

    def test_cli_output_is_valid_json(self):
        """Running as script produces valid JSON."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0, 0.0]})
            result = subprocess.run(
                [sys.executable, "generate_summary.py", d, "cli-test"],
                capture_output=True, text=True,
                cwd=os.path.dirname(__file__) or ".",
            )
            assert result.returncode == 0, f"stderr: {result.stderr}"
            data = json.loads(result.stdout)
            assert data["run_id"] == "cli-test"

    def test_cli_default_run_id(self):
        """Without explicit run_id, uses the directory basename."""
        with tempfile.TemporaryDirectory() as d:
            _make_fixture(d, {"task-a": [1.0]})
            result = subprocess.run(
                [sys.executable, "generate_summary.py", d],
                capture_output=True, text=True,
                cwd=os.path.dirname(__file__) or ".",
            )
            assert result.returncode == 0, f"stderr: {result.stderr}"
            data = json.loads(result.stdout)
            assert data["run_id"] == os.path.basename(d)
