"""Tests for data layer: run discovery, task reading, transcript parsing."""

import json
import pytest

from data import RunStore


class TestRunDiscovery:
    """RunStore.list_runs() discovers job directories."""

    def test_list_runs_finds_job(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        assert len(runs) == 1
        assert runs[0]["job_name"] == "full-test"

    def test_list_runs_counts_tasks(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        assert runs[0]["total_tasks"] == 2

    def test_list_runs_counts_passes(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        assert runs[0]["passed"] == 1

    def test_list_runs_empty_dir(self, tmp_path):
        store = RunStore(tmp_path)
        assert store.list_runs() == []

    def test_list_runs_multiple_jobs(self, harbor_job_dir, tmp_path):
        """A second job directory is also discovered."""
        job2_root = harbor_job_dir / "second-run"
        task = job2_root / "some-task__aaa111"
        task.mkdir(parents=True)
        (task / "verifier").mkdir()
        (task / "verifier" / "reward.txt").write_text("1.0")

        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        names = {r["job_name"] for r in runs}
        assert names == {"full-test", "second-run"}


class TestGetRun:
    """RunStore.get_run() returns summary for a single job."""

    def test_get_run_returns_summary(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        run = store.get_run("full-test")
        assert run["job_name"] == "full-test"
        assert run["total_tasks"] == 2
        assert run["passed"] == 1

    def test_get_run_missing_returns_none(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert store.get_run("nonexistent") is None


class TestRunMetadata:
    """RunStore reads run-level metadata from manifest/config/result files."""

    def test_list_runs_includes_model(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        assert runs[0]["model"] == "openai/gpt-5.3-codex"

    def test_list_runs_includes_git_sha(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        assert runs[0]["git_sha"] == "abc1234"

    def test_list_runs_includes_dataset(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        assert runs[0]["dataset_name"] == "terminal-bench"
        assert runs[0]["dataset_version"] == "2.0"

    def test_list_runs_includes_timestamps(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        assert runs[0]["started_at"] == "2026-03-01T12:00:00Z"
        assert runs[0]["finished_at"] == "2026-03-01T13:30:00Z"

    def test_list_runs_includes_reps(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        runs = store.list_runs()
        assert runs[0]["reps"] == 1

    def test_get_run_includes_metadata(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        run = store.get_run("full-test")
        assert run["model"] == "openai/gpt-5.3-codex"
        assert run["git_sha"] == "abc1234"
        assert run["git_branch"] == "main"
        assert run["adapter"] == "serf_agent:SerfAgent"

    def test_metadata_missing_files(self, tmp_path):
        """Runs without metadata files still work with empty defaults."""
        job_root = tmp_path / "bare-run"
        task = job_root / "some-task__aaa111"
        task.mkdir(parents=True)
        (task / "verifier").mkdir()
        (task / "verifier" / "reward.txt").write_text("1.0")

        store = RunStore(tmp_path)
        runs = store.list_runs()
        assert runs[0]["model"] == ""
        assert runs[0]["git_sha"] == ""
        assert runs[0]["dataset_name"] == ""
        assert runs[0]["started_at"] == ""


class TestTaskDeduplication:
    """Duplicate task dirs from reps are grouped, best trial selected."""

    def test_list_tasks_deduplicates(self, harbor_job_dir_with_reps):
        store = RunStore(harbor_job_dir_with_reps)
        tasks = store.list_tasks("reps-test")
        names = [t["task_name"] for t in tasks]
        assert names.count("build-widget") == 1

    def test_dedup_picks_best_reward(self, harbor_job_dir_with_reps):
        store = RunStore(harbor_job_dir_with_reps)
        tasks = store.list_tasks("reps-test")
        bw = [t for t in tasks if t["task_name"] == "build-widget"][0]
        assert bw["reward"] == 1.0
        assert bw["passed"] is True

    def test_dedup_includes_trial_count(self, harbor_job_dir_with_reps):
        store = RunStore(harbor_job_dir_with_reps)
        tasks = store.list_tasks("reps-test")
        bw = [t for t in tasks if t["task_name"] == "build-widget"][0]
        assert bw["trial_count"] == 2

    def test_single_trial_has_count_1(self, harbor_job_dir_with_reps):
        store = RunStore(harbor_job_dir_with_reps)
        tasks = store.list_tasks("reps-test")
        fb = [t for t in tasks if t["task_name"] == "fix-bug"][0]
        assert fb["trial_count"] == 1

    def test_list_runs_counts_unique_tasks(self, harbor_job_dir_with_reps):
        store = RunStore(harbor_job_dir_with_reps)
        runs = store.list_runs()
        assert runs[0]["total_tasks"] == 2  # not 3

    def test_get_run_counts_unique_tasks(self, harbor_job_dir_with_reps):
        store = RunStore(harbor_job_dir_with_reps)
        run = store.get_run("reps-test")
        assert run["total_tasks"] == 2

    def test_get_task_returns_best_trial(self, harbor_job_dir_with_reps):
        store = RunStore(harbor_job_dir_with_reps)
        task = store.get_task("reps-test", "build-widget")
        assert task["reward"] == 1.0


class TestListTasks:
    """RunStore.list_tasks() returns per-task summaries."""

    def test_list_tasks_returns_all(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        assert len(tasks) == 2

    def test_task_names_extracted(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        names = {t["task_name"] for t in tasks}
        assert names == {"build-widget", "fix-bug"}

    def test_passing_task_fields(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        passing = [t for t in tasks if t["task_name"] == "build-widget"][0]
        assert passing["reward"] == 1.0
        assert passing["passed"] is True

    def test_failing_task_fields(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        failing = [t for t in tasks if t["task_name"] == "fix-bug"][0]
        assert failing["reward"] == 0.0
        assert failing["passed"] is False

    def test_session_count(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        for t in tasks:
            assert t["session_count"] >= 1

    def test_list_tasks_missing_run(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert store.list_tasks("nonexistent") is None

    def test_task_summary_includes_timestamps(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        bw = [t for t in tasks if t["task_name"] == "build-widget"][0]
        assert bw["started_at"] == "2026-03-01T12:00:00Z"
        assert bw["finished_at"] == "2026-03-01T12:05:30Z"

    def test_task_summary_timestamps_missing(self, tmp_path):
        """Task without result.json returns empty timestamps."""
        job_root = tmp_path / "no-ts-run"
        task = job_root / "some-task__aaa111"
        task.mkdir(parents=True)
        (task / "verifier").mkdir()
        (task / "verifier" / "reward.txt").write_text("1.0")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("no-ts-run")
        assert tasks[0]["started_at"] == ""
        assert tasks[0]["finished_at"] == ""


class TestTaskStatus:
    """Tasks have status: queued, running, pass, or fail.

    Status is determined by disk artifacts, not result.json timestamps
    (harbor pre-writes those at job launch).
    """

    def test_completed_pass(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        bw = [t for t in tasks if t["task_name"] == "build-widget"][0]
        assert bw["status"] == "pass"

    def test_completed_fail(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        fb = [t for t in tasks if t["task_name"] == "fix-bug"][0]
        assert fb["status"] == "fail"

    def test_running_has_transcripts(self, tmp_path):
        """Task with transcript files but no reward is running."""
        from conftest import _passing_transcript
        job_root = tmp_path / "active-run"
        task = job_root / "in-prog__aaa111"
        task.mkdir(parents=True)
        # Write a transcript (agent has started)
        sessions = task / "agent" / "serf-state" / "sessions"
        sessions.mkdir(parents=True)
        (sessions / "sess.transcript.jsonl").write_text(
            json.dumps({"kind": "header", "format_version": 1,
                         "session_id": "s1", "model": "x", "depth": 0}) + "\n")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("active-run")
        assert tasks[0]["status"] == "running"
        assert tasks[0]["passed"] is False

    def test_running_has_stdout(self, tmp_path):
        """Task with non-empty stdout but no reward is running."""
        job_root = tmp_path / "active-run"
        task = job_root / "in-prog__aaa111"
        task.mkdir(parents=True)
        cmd = task / "agent" / "command-0"
        cmd.mkdir(parents=True)
        (cmd / "stdout.txt").write_text("some output\n")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("active-run")
        assert tasks[0]["status"] == "running"

    def test_queued_no_agent_output(self, tmp_path):
        """Task dir with result.json but no agent output is queued."""
        job_root = tmp_path / "bare-run"
        task = job_root / "bare__aaa111"
        task.mkdir(parents=True)
        # Harbor pre-writes result.json at launch — doesn't mean task started
        (task / "result.json").write_text(json.dumps({
            "started_at": "2026-03-01T12:00:00Z",
            "finished_at": "2026-03-01T12:00:01Z",
        }))

        store = RunStore(tmp_path)
        tasks = store.list_tasks("bare-run")
        assert tasks[0]["status"] == "queued"

    def test_queued_empty_dir(self, tmp_path):
        """Task dir with nothing in it is queued."""
        job_root = tmp_path / "bare-run"
        task = job_root / "bare__aaa111"
        task.mkdir(parents=True)

        store = RunStore(tmp_path)
        tasks = store.list_tasks("bare-run")
        assert tasks[0]["status"] == "queued"

    def test_queued_empty_stdout(self, tmp_path):
        """Task with empty stdout (no real output) is queued."""
        job_root = tmp_path / "bare-run"
        task = job_root / "bare__aaa111"
        task.mkdir(parents=True)
        cmd = task / "agent" / "command-0"
        cmd.mkdir(parents=True)
        (cmd / "stdout.txt").write_text("")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("bare-run")
        assert tasks[0]["status"] == "queued"

    def test_run_counts_exclude_incomplete(self, tmp_path):
        """list_runs total_tasks and passed exclude non-final tasks."""
        from conftest import _make_task, _passing_transcript
        job_root = tmp_path / "mixed-run"
        # One completed passing task
        t1 = job_root / "done-task__abc123"
        _make_task(t1, reward=1.0, transcript_entries=_passing_transcript())
        # One queued (no agent output)
        t2 = job_root / "queued-task__def456"
        t2.mkdir(parents=True)
        # One running (has transcript, no reward)
        t3 = job_root / "running-task__ghi789"
        t3.mkdir(parents=True)
        sessions = t3 / "agent" / "serf-state" / "sessions"
        sessions.mkdir(parents=True)
        (sessions / "s.transcript.jsonl").write_text(
            json.dumps({"kind": "header", "format_version": 1,
                         "session_id": "s1", "model": "x", "depth": 0}) + "\n")

        store = RunStore(tmp_path)
        runs = store.list_runs()
        run = runs[0]
        assert run["total_tasks"] == 1
        assert run["passed"] == 1
        assert run["running"] == 1
        assert run["queued"] == 1


class TestFailureClassification:
    """Failure categories are correctly identified."""

    def test_wrong_answer(self, harbor_job_dir):
        """fix-bug has submit_result in stdout -> wrong_answer."""
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        failing = [t for t in tasks if t["task_name"] == "fix-bug"][0]
        assert failing["failure_category"] == "wrong_answer"

    def test_passing_has_no_failure(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        tasks = store.list_tasks("full-test")
        passing = [t for t in tasks if t["task_name"] == "build-widget"][0]
        assert passing["failure_category"] is None

    def test_timeout(self, tmp_path):
        """result.json with AgentTimeoutError -> timeout."""
        job_root = tmp_path / "timeout-run"
        task_dir = job_root / "slow-task__xyz789"
        task_dir.mkdir(parents=True)
        (task_dir / "verifier").mkdir()
        (task_dir / "verifier" / "reward.txt").write_text("0.0")
        (task_dir / "result.json").write_text(json.dumps({
            "config": {},
            "exception_info": {
                "exception_type": "AgentTimeoutError",
                "exception_message": "timed out",
            },
        }))
        cmd = task_dir / "agent" / "command-0"
        cmd.mkdir(parents=True)
        (cmd / "stdout.txt").write_text("")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("timeout-run")
        assert tasks[0]["failure_category"] == "timeout"

    def test_api_error(self, tmp_path):
        """stdout with [error] -> api_error."""
        job_root = tmp_path / "error-run"
        task_dir = job_root / "broken-task__err000"
        task_dir.mkdir(parents=True)
        (task_dir / "verifier").mkdir()
        (task_dir / "verifier" / "reward.txt").write_text("0.0")
        (task_dir / "result.json").write_text(json.dumps({"config": {}}))
        cmd = task_dir / "agent" / "command-0"
        cmd.mkdir(parents=True)
        (cmd / "stdout.txt").write_text("[error] API rate limit exceeded\n")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("error-run")
        assert tasks[0]["failure_category"] == "api_error"

    def test_no_submit(self, tmp_path):
        """No markers at all -> no_submit."""
        job_root = tmp_path / "quiet-run"
        task_dir = job_root / "silent-task__qqq111"
        task_dir.mkdir(parents=True)
        (task_dir / "verifier").mkdir()
        (task_dir / "verifier" / "reward.txt").write_text("0.0")
        (task_dir / "result.json").write_text(json.dumps({"config": {}}))
        cmd = task_dir / "agent" / "command-0"
        cmd.mkdir(parents=True)
        (cmd / "stdout.txt").write_text("doing stuff...\n")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("quiet-run")
        assert tasks[0]["failure_category"] == "no_submit"


class TestGetTask:
    """RunStore.get_task() returns detailed info for one task."""

    def test_get_task_returns_detail(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        assert task is not None
        assert task["task_name"] == "build-widget"
        assert task["reward"] == 1.0

    def test_get_task_includes_test_output(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "fix-bug")
        assert "test_widget" in task["test_output"]

    def test_get_task_includes_model(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        assert task["model"] == "gpt-5.3-codex"

    def test_get_task_missing_returns_none(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert store.get_task("full-test", "nonexistent") is None

    def test_get_task_missing_run_returns_none(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert store.get_task("nonexistent", "build-widget") is None

    def test_get_task_includes_transcript_files(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        assert len(task["transcript_files"]) >= 1


class TestMissingFiles:
    """Graceful handling of missing or partial data."""

    def test_missing_reward_file(self, tmp_path):
        """Task dir without reward.txt is still listed."""
        job_root = tmp_path / "partial-run"
        task_dir = job_root / "no-reward__aaa111"
        task_dir.mkdir(parents=True)

        store = RunStore(tmp_path)
        tasks = store.list_tasks("partial-run")
        assert len(tasks) == 1
        assert tasks[0]["reward"] is None

    def test_missing_stdout_file(self, tmp_path):
        """Task without stdout still classifies failure."""
        job_root = tmp_path / "no-stdout-run"
        task_dir = job_root / "no-out__bbb222"
        task_dir.mkdir(parents=True)
        (task_dir / "verifier").mkdir()
        (task_dir / "verifier" / "reward.txt").write_text("0.0")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("no-stdout-run")
        assert tasks[0]["failure_category"] == "no_submit"

    def test_missing_result_json(self, tmp_path):
        """Task without result.json still works."""
        job_root = tmp_path / "no-result-run"
        task_dir = job_root / "no-result__ccc333"
        task_dir.mkdir(parents=True)
        (task_dir / "verifier").mkdir()
        (task_dir / "verifier" / "reward.txt").write_text("0.0")
        cmd = task_dir / "agent" / "command-0"
        cmd.mkdir(parents=True)
        (cmd / "stdout.txt").write_text("stuff\n")

        store = RunStore(tmp_path)
        tasks = store.list_tasks("no-result-run")
        assert tasks[0]["failure_category"] == "no_submit"


class TestTranscriptLoading:
    """RunStore.load_transcripts() parses JSONL into session dicts."""

    def test_load_single_transcript(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        assert len(sessions) == 1

    def test_session_has_header_fields(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        sess = sessions[0]
        assert sess["session_id"] == "sess-main"
        assert sess["model"] == "gpt-5.3-codex"
        assert sess["depth"] == 0

    def test_session_has_entries(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        sess = sessions[0]
        # header + entries in passing transcript
        assert len(sess["entries"]) > 0
        assert sess["entries"][0]["kind"] == "entry"

    def test_session_has_parent_tool_call_id(self, harbor_job_dir):
        """Child transcript header includes parent_tool_call_id."""
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"
        child_entries = [
            {"kind": "header", "format_version": 1, "session_id": "sess-child",
             "parent_session_id": "sess-main",
             "parent_tool_call_id": "call_spawn_42",
             "created_at": "2026-03-01T12:01:00Z", "model": "gpt-5.3-codex",
             "profile_id": "openai", "depth": 1},
        ]
        child_file = sessions_dir / "sess-child.transcript.jsonl"
        child_file.write_text(json.dumps(child_entries[0]) + "\n")

        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        child = [s for s in sessions if s["session_id"] == "sess-child"][0]
        assert child["parent_tool_call_id"] == "call_spawn_42"

    def test_session_missing_parent_tool_call_id_defaults_empty(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        # The main session has no parent_tool_call_id in its header
        main = sessions[0]
        assert main["parent_tool_call_id"] == ""

    def test_load_empty_file_list(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        sessions = store.load_transcripts([])
        assert sessions == []


class TestSessionTree:
    """RunStore.build_session_tree() organizes sessions by parent-child."""

    def test_single_root_session(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        tree = store.build_session_tree(sessions)
        assert len(tree) == 1
        assert tree[0]["session_id"] == "sess-main"
        assert tree[0]["children"] == []

    def test_child_session_nested(self, harbor_job_dir):
        """Add a child transcript and verify tree structure."""
        # Create a child session transcript
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"
        child_entries = [
            {"kind": "header", "format_version": 1, "session_id": "sess-child-1",
             "parent_session_id": "sess-main",
             "created_at": "2026-03-01T12:01:00Z", "model": "gpt-5.3-codex",
             "profile_id": "openai", "depth": 1},
            {"kind": "entry", "seq": 0, "turn": {
                "kind": "USER_INPUT",
                "message": {"role": "user", "content": [
                    {"kind": "text", "text": "Write tests for widget."}
                ]},
            }},
            {"kind": "entry", "seq": 1, "turn": {
                "kind": "ASSISTANT",
                "message": {"role": "assistant", "content": [
                    {"kind": "text", "text": "Writing tests..."},
                ]},
                "usage": {"input_tokens": 100, "output_tokens": 20},
            }},
        ]
        child_file = sessions_dir / "sess-child-1.transcript.jsonl"
        lines = [json.dumps(e) for e in child_entries]
        child_file.write_text("\n".join(lines) + "\n")

        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        tree = store.build_session_tree(sessions)

        assert len(tree) == 1
        root = tree[0]
        assert root["session_id"] == "sess-main"
        assert len(root["children"]) == 1
        assert root["children"][0]["session_id"] == "sess-child-1"
        assert root["children"][0]["depth"] == 1

    def test_multiple_children(self, harbor_job_dir):
        """Multiple child sessions under the same parent."""
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"

        for i in range(3):
            child_entries = [
                {"kind": "header", "format_version": 1,
                 "session_id": f"sess-child-{i}",
                 "parent_session_id": "sess-main",
                 "created_at": f"2026-03-01T12:0{i}:00Z",
                 "model": "gpt-5.3-codex", "profile_id": "openai", "depth": 1},
            ]
            f = sessions_dir / f"sess-child-{i}.transcript.jsonl"
            f.write_text(json.dumps(child_entries[0]) + "\n")

        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        tree = store.build_session_tree(sessions)

        root = tree[0]
        assert len(root["children"]) == 3

    def test_grandchild_nesting(self, harbor_job_dir):
        """Three-level nesting: root -> child -> grandchild."""
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"

        child = [{"kind": "header", "format_version": 1,
                  "session_id": "sess-child",
                  "parent_session_id": "sess-main",
                  "created_at": "2026-03-01T12:01:00Z",
                  "model": "gpt-5.3-codex", "profile_id": "openai", "depth": 1}]
        (sessions_dir / "sess-child.transcript.jsonl").write_text(
            json.dumps(child[0]) + "\n")

        grandchild = [{"kind": "header", "format_version": 1,
                       "session_id": "sess-grandchild",
                       "parent_session_id": "sess-child",
                       "created_at": "2026-03-01T12:02:00Z",
                       "model": "gpt-5.3-codex", "profile_id": "openai", "depth": 2}]
        (sessions_dir / "sess-grandchild.transcript.jsonl").write_text(
            json.dumps(grandchild[0]) + "\n")

        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        tree = store.build_session_tree(sessions)

        root = tree[0]
        assert root["session_id"] == "sess-main"
        assert len(root["children"]) == 1
        child_node = root["children"][0]
        assert child_node["session_id"] == "sess-child"
        assert len(child_node["children"]) == 1
        assert child_node["children"][0]["session_id"] == "sess-grandchild"


class TestTaskInstruction:
    """RunStore reads task instruction from command.txt."""

    def test_get_task_includes_instruction(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        assert task["instruction"] == "Build a widget that returns 42."

    def test_instruction_missing_command_txt(self, tmp_path):
        """Task without command.txt returns empty instruction."""
        from conftest import _make_task, _passing_transcript
        job_root = tmp_path / "no-cmd-run" / "no-cmd-run"
        t = job_root / "some-task__aaa111"
        _make_task(t, reward=1.0, transcript_entries=_passing_transcript())
        store = RunStore(tmp_path / "no-cmd-run")
        task = store.get_task("no-cmd-run", "some-task")
        assert task["instruction"] == ""


class TestListAllFiles:
    """RunStore.list_all_files() enumerates all files in a task dir."""

    def test_lists_files(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        files = store.list_all_files(task["task_dir"])
        paths = [f["path"] for f in files]
        assert any("reward.txt" in p for p in paths)
        assert any("stdout.txt" in p for p in paths)
        assert any("result.json" in p for p in paths)

    def test_files_have_size(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        files = store.list_all_files(task["task_dir"])
        for f in files:
            assert "size" in f
            assert isinstance(f["size"], int)

    def test_nonexistent_dir_returns_empty(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert store.list_all_files("/nonexistent/path") == []


class TestSystemPromptInTranscript:
    """Transcript parser extracts system_prompt from header."""

    def test_system_prompt_parsed(self, tmp_path):
        """Transcript with system_prompt in header returns it."""
        import json
        tf = tmp_path / "sess.jsonl"
        header = {
            "kind": "header", "format_version": 1,
            "session_id": "sess-test", "model": "gpt-5.3-codex",
            "profile_id": "openai", "depth": 0,
            "system_prompt": "You are serf. Do the task.",
        }
        tf.write_text(json.dumps(header) + "\n")
        store = RunStore(tmp_path)
        sessions = store.load_transcripts([str(tf)])
        assert sessions[0]["system_prompt"] == "You are serf. Do the task."

    def test_missing_system_prompt_defaults_empty(self, harbor_job_dir):
        """Old transcripts without system_prompt return empty string."""
        store = RunStore(harbor_job_dir)
        task = store.get_task("full-test", "build-widget")
        sessions = store.load_transcripts(task["transcript_files"])
        assert sessions[0]["system_prompt"] == ""
