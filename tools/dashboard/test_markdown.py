"""Tests for the markdown renderer."""

from markdown_render import render_run_list, render_run_detail, render_task_detail


class TestRenderRunList:
    def test_renders_table(self):
        runs = [
            {"job_name": "full-mk6", "total_tasks": 89, "passed": 12},
            {"job_name": "full-mk4", "total_tasks": 67, "passed": 13},
        ]
        md = render_run_list(runs)
        assert "full-mk6" in md
        assert "full-mk4" in md

    def test_empty_list(self):
        md = render_run_list([])
        assert "No runs" in md

    def test_pass_rate(self):
        runs = [{"job_name": "run1", "total_tasks": 10, "passed": 5}]
        md = render_run_list(runs)
        assert "50%" in md


class TestRenderRunDetail:
    def test_renders_header(self):
        run = {"job_name": "full-mk6", "total_tasks": 89, "passed": 12}
        tasks = [
            {"task_name": "build-widget", "passed": True, "reward": 1.0,
             "failure_category": None, "session_count": 3},
            {"task_name": "fix-bug", "passed": False, "reward": 0.0,
             "failure_category": "wrong_answer", "session_count": 2},
        ]
        md = render_run_detail(run, tasks)
        assert "full-mk6" in md
        assert "build-widget" in md
        assert "fix-bug" in md
        assert "PASS" in md
        assert "FAIL" in md

    def test_failure_categories(self):
        run = {"job_name": "test-run", "total_tasks": 3, "passed": 0}
        tasks = [
            {"task_name": "t1", "passed": False, "reward": 0.0,
             "failure_category": "timeout", "session_count": 1},
            {"task_name": "t2", "passed": False, "reward": 0.0,
             "failure_category": "wrong_answer", "session_count": 1},
            {"task_name": "t3", "passed": False, "reward": 0.0,
             "failure_category": "timeout", "session_count": 1},
        ]
        md = render_run_detail(run, tasks)
        assert "timeout" in md
        assert "wrong_answer" in md


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

    def test_failure_render(self):
        md = render_task_detail(
            task_name="fix-bug",
            job_name="run1",
            reward=0.0,
            failure_category="wrong_answer",
            trajectory=[],
            verifier_output="FAIL: expected 42",
        )
        assert "FAILED" in md
        assert "wrong_answer" in md
        assert "expected 42" in md

    def test_no_trajectory(self):
        md = render_task_detail(
            task_name="t1", job_name="r1",
            reward=None, failure_category="",
            trajectory=[], verifier_output="",
        )
        assert "No trajectory" in md
