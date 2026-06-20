"""Tests for generate_report.py -- HTML report from archive runs.

Written BEFORE implementation (TDD).
"""

import json
import os
import subprocess
import sys
import tempfile

from generate_report import generate_report


def _make_summary(run_dir, summary_data):
    """Write a summary.json file into run_dir."""
    os.makedirs(run_dir, exist_ok=True)
    with open(os.path.join(run_dir, "summary.json"), "w") as f:
        json.dump(summary_data, f)


def _make_archive(run_dir, tasks, summary_overrides=None):
    """Create a full archive fixture with tasks directory and summary.json.

    tasks: dict like {"build-cython-ext": [(1.0, None), (0.0, "wrong_answer")]}
      Each value is a list of (reward, failure_category) tuples.
    """
    os.makedirs(run_dir, exist_ok=True)

    task_summaries = []
    fc_counts = {}
    for task_name, reps_data in sorted(tasks.items()):
        reps = []
        for i, (reward, fc) in enumerate(reps_data, 1):
            rep_dir = os.path.join(run_dir, "tasks", task_name, f"rep-{i}")
            os.makedirs(rep_dir, exist_ok=True)
            with open(os.path.join(rep_dir, "reward.txt"), "w") as f:
                f.write(str(reward))
            if fc:
                with open(os.path.join(rep_dir, "failure_category.txt"), "w") as f:
                    f.write(fc)
                fc_counts[fc] = fc_counts.get(fc, 0) + 1
            reps.append({"rep": i, "reward": reward, "failure_category": fc})

        rewards = [r[0] for r in reps_data]
        pass_count = sum(1 for r in rewards if r > 0)
        total = len(rewards)
        task_summaries.append({
            "name": task_name,
            "pass_majority": pass_count > total / 2,
            "pass_strict": pass_count == total,
            "pass_any": pass_count > 0,
            "pass_rate": round(pass_count / total, 2) if total else 0,
            "reps_pass": pass_count,
            "reps_total": total,
            "reps": reps,
        })

    n_tasks = len(task_summaries)
    majority = sum(1 for t in task_summaries if t["pass_majority"])
    strict = sum(1 for t in task_summaries if t["pass_strict"])
    any_pass = sum(1 for t in task_summaries if t["pass_any"])

    summary = {
        "schema_version": 1,
        "run_id": "test-run-2026",
        "task_count": n_tasks,
        "pass_count_majority": majority,
        "pass_count_strict": strict,
        "pass_count_any": any_pass,
        "pass_rate_majority": round(majority / n_tasks, 4) if n_tasks else 0,
        "pass_rate_strict": round(strict / n_tasks, 4) if n_tasks else 0,
        "pass_rate_any": round(any_pass / n_tasks, 4) if n_tasks else 0,
        "pass_rate_majority_ci_95": [0.2, 0.8],
        "failure_categories": fc_counts,
        "tasks": task_summaries,
    }
    if summary_overrides:
        summary.update(summary_overrides)

    _make_summary(run_dir, summary)
    return summary


# ---------------------------------------------------------------------------
# Basic HTML structure
# ---------------------------------------------------------------------------


class TestBasicStructure:
    """Report contains valid HTML with expected top-level elements."""

    def test_returns_string(self):
        """generate_report returns a string."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            result = generate_report(d)
            assert isinstance(result, str)

    def test_contains_html_doctype(self):
        """Output starts with HTML doctype."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            result = generate_report(d)
            assert result.strip().startswith("<!DOCTYPE html>")

    def test_contains_closing_html(self):
        """Output ends with closing html tag."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            result = generate_report(d)
            assert "</html>" in result

    def test_contains_style_tag(self):
        """Report has embedded CSS (no external links)."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            result = generate_report(d)
            assert "<style>" in result


# ---------------------------------------------------------------------------
# Header section
# ---------------------------------------------------------------------------


class TestHeader:
    """Report header shows run ID and pass rates."""

    def test_run_id_in_header(self):
        """Run ID appears in the report."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]},
                          summary_overrides={"run_id": "my-special-run"})
            result = generate_report(d)
            assert "my-special-run" in result

    def test_pass_rate_in_header(self):
        """Majority pass rate appears in the report."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {
                "task-a": [(1.0, None), (1.0, None)],
                "task-b": [(0.0, "timeout"), (0.0, "timeout")],
            })
            result = generate_report(d)
            # 1/2 = 50%
            assert "50" in result

    def test_ci_in_header(self):
        """95% CI bounds appear in the report."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]},
                          summary_overrides={"pass_rate_majority_ci_95": [0.1234, 0.9876]})
            result = generate_report(d)
            assert "12.3" in result  # formatted as percentage
            assert "98.8" in result


# ---------------------------------------------------------------------------
# Summary table
# ---------------------------------------------------------------------------


class TestSummaryTable:
    """Sortable summary table with task results."""

    def test_task_names_in_table(self):
        """All task names appear in the report."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {
                "build-cython-ext": [(1.0, None)],
                "fix-code-vulnerability": [(0.0, "wrong_answer")],
            })
            result = generate_report(d)
            assert "build-cython-ext" in result
            assert "fix-code-vulnerability" in result

    def test_pass_label(self):
        """Passing tasks show PASS."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None), (1.0, None), (1.0, None)]})
            result = generate_report(d)
            assert "PASS" in result

    def test_fail_label(self):
        """Failing tasks show FAIL."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(0.0, "timeout"), (0.0, "timeout")]})
            result = generate_report(d)
            assert "FAIL" in result

    def test_rep_counts(self):
        """Per-task rep counts (pass/total) appear in the table."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None), (0.0, "timeout"), (1.0, None)]})
            result = generate_report(d)
            # 2/3 reps passed
            assert "2/3" in result

    def test_table_has_sortable_js(self):
        """Report includes JavaScript for table sorting."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            result = generate_report(d)
            assert "<script>" in result


# ---------------------------------------------------------------------------
# Failure breakdown
# ---------------------------------------------------------------------------


class TestFailureBreakdown:
    """Failure category visualization."""

    def test_failure_categories_shown(self):
        """Failure category names appear in report."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {
                "task-a": [(0.0, "timeout")],
                "task-b": [(0.0, "wrong_answer")],
            })
            result = generate_report(d)
            assert "timeout" in result
            assert "wrong_answer" in result

    def test_failure_counts_shown(self):
        """Failure counts appear in report."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {
                "task-a": [(0.0, "timeout"), (0.0, "timeout")],
                "task-b": [(0.0, "wrong_answer")],
            })
            result = generate_report(d)
            # timeout: 2, wrong_answer: 1
            # Both counts should be present somewhere
            assert "2" in result
            assert "1" in result

    def test_no_failure_section_when_all_pass(self):
        """When no failures, failure breakdown is minimal or absent."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None), (1.0, None)]})
            result = generate_report(d)
            # Should not contain failure category labels
            assert "timeout" not in result
            assert "wrong_answer" not in result


# ---------------------------------------------------------------------------
# Per-task details
# ---------------------------------------------------------------------------


class TestPerTaskDetails:
    """Collapsible per-task detail sections."""

    def test_details_element_present(self):
        """Report uses <details> elements for per-task detail."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            result = generate_report(d)
            assert "<details>" in result or "<details " in result

    def test_verifier_stdout_shown(self):
        """Verifier stdout content appears in task details."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            # Write verifier stdout
            rep_dir = os.path.join(d, "tasks", "task-a", "rep-1")
            with open(os.path.join(rep_dir, "verifier-stdout.txt"), "w") as f:
                f.write("All 5 tests passed!\n")
            result = generate_report(d)
            assert "All 5 tests passed!" in result

    def test_agent_stdout_shown(self):
        """Agent stdout content appears in task details."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            rep_dir = os.path.join(d, "tasks", "task-a", "rep-1")
            with open(os.path.join(rep_dir, "agent-stdout.txt"), "w") as f:
                f.write("Working on task...\nDone.\n")
            result = generate_report(d)
            assert "Working on task..." in result

    def test_agent_stdout_truncated(self):
        """Agent stdout is truncated to 200 lines with note."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            rep_dir = os.path.join(d, "tasks", "task-a", "rep-1")
            with open(os.path.join(rep_dir, "agent-stdout.txt"), "w") as f:
                for i in range(500):
                    f.write(f"line {i}\n")
            result = generate_report(d)
            # Should mention truncation
            assert "truncated" in result.lower() or "200" in result
            # Should NOT have line 499
            assert "line 499" not in result
            # Should have early lines
            assert "line 0" in result

    def test_missing_stdout_handled(self):
        """Missing stdout files don't crash the report."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            # Don't create any stdout files
            result = generate_report(d)
            # Should still produce valid HTML
            assert "</html>" in result


# ---------------------------------------------------------------------------
# HTML escaping
# ---------------------------------------------------------------------------


class TestHTMLEscaping:
    """User-provided content is properly escaped."""

    def test_task_name_escaped(self):
        """Task names with HTML chars are escaped."""
        with tempfile.TemporaryDirectory() as d:
            # Use a task name with angle brackets (pathological)
            _make_archive(d, {"task<script>alert(1)</script>": [(1.0, None)]})
            result = generate_report(d)
            # Raw <script> should not appear
            assert "<script>alert(1)</script>" not in result
            # Escaped version should appear
            assert "&lt;script&gt;" in result

    def test_stdout_content_escaped(self):
        """Stdout content with HTML is escaped."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            rep_dir = os.path.join(d, "tasks", "task-a", "rep-1")
            with open(os.path.join(rep_dir, "verifier-stdout.txt"), "w") as f:
                f.write('<b>bold</b> & "quoted"')
            result = generate_report(d)
            assert "&lt;b&gt;" in result
            assert "&amp;" in result


# ---------------------------------------------------------------------------
# Color coding
# ---------------------------------------------------------------------------


class TestColorCoding:
    """PASS/FAIL are visually distinguished."""

    def test_pass_has_green(self):
        """Passing tasks use green color."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None), (1.0, None)]})
            result = generate_report(d)
            # Check for green color near PASS (various ways it could appear)
            assert "green" in result.lower() or "#2" in result or "#0" in result

    def test_fail_has_red(self):
        """Failing tasks use red color."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(0.0, "timeout"), (0.0, "timeout")]})
            result = generate_report(d)
            assert "red" in result.lower() or "#e" in result.lower() or "#d" in result.lower() or "#c" in result.lower()


# ---------------------------------------------------------------------------
# CLI mode
# ---------------------------------------------------------------------------


class TestCLI:
    """CLI invocation writes report.html."""

    def test_cli_writes_report_file(self):
        """Running as script writes report.html to the run directory."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            result = subprocess.run(
                [sys.executable, "generate_report.py", d],
                capture_output=True, text=True,
                cwd=os.path.dirname(__file__) or ".",
            )
            assert result.returncode == 0, f"stderr: {result.stderr}"
            report_path = os.path.join(d, "report.html")
            assert os.path.isfile(report_path)
            with open(report_path) as f:
                content = f.read()
            assert "<!DOCTYPE html>" in content

    def test_cli_prints_output_path(self):
        """CLI prints the path of the written report."""
        with tempfile.TemporaryDirectory() as d:
            _make_archive(d, {"task-a": [(1.0, None)]})
            result = subprocess.run(
                [sys.executable, "generate_report.py", d],
                capture_output=True, text=True,
                cwd=os.path.dirname(__file__) or ".",
            )
            assert "report.html" in result.stdout
