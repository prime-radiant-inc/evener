"""Integration tests for API routes."""

import json
import os
import pytest
from fastapi.testclient import TestClient

from data import RunStore
from conftest import _make_task, _passing_transcript, _make_task_with_artifacts


def _make_client(harbor_job_dir):
    """Create a TestClient with a store pointed at the test fixture."""
    import server as srv
    srv.store = RunStore(harbor_job_dir)
    srv._cache_dir = str(harbor_job_dir / ".cache")
    return TestClient(srv.app)


@pytest.fixture
def client(harbor_job_dir):
    """Create a test client pointing at the fixture data."""
    return _make_client(harbor_job_dir)


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
        assert data["total_tasks"] == 2

    def test_get_run_markdown(self, client):
        resp = client.get("/api/runs/full-test")
        assert resp.status_code == 200
        assert "full-test" in resp.text
        assert "PASS" in resp.text or "FAIL" in resp.text

    def test_get_unknown_run(self, client):
        resp = client.get("/api/runs/nonexistent",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 404

    def test_list_runs_includes_metadata(self, client):
        resp = client.get("/api/runs",
                          headers={"Accept": "application/json"})
        data = resp.json()
        run = [r for r in data if r["job_name"] == "full-test"][0]
        assert run["model"] == "openai/gpt-5.3-codex"
        assert run["git_sha"] == "abc1234"
        assert run["dataset_name"] == "terminal-bench"
        assert run["dataset_version"] == "2.0"
        assert run["started_at"] == "2026-03-01T12:00:00Z"
        assert run["finished_at"] == "2026-03-01T13:30:00Z"

    def test_get_run_includes_metadata(self, client):
        resp = client.get("/api/runs/full-test",
                          headers={"Accept": "application/json"})
        data = resp.json()
        assert data["model"] == "openai/gpt-5.3-codex"
        assert data["git_branch"] == "main"


class TestTaskEndpoints:
    def test_list_tasks(self, client):
        resp = client.get("/api/runs/full-test/tasks",
                          headers={"Accept": "application/json"})
        data = resp.json()
        names = [t["task_name"] for t in data]
        assert "build-widget" in names

    def test_get_task(self, client):
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["task_name"] == "build-widget"
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

    def test_task_list_has_timestamps(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks",
                          headers={"Accept": "application/json"})
        tasks = resp.json()
        bw = [t for t in tasks if t["task_name"] == "build-widget"][0]
        assert bw["started_at"] == "2026-03-01T12:00:00Z"
        assert bw["finished_at"] == "2026-03-01T12:05:30Z"

    def test_task_list_has_trial_count(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks",
                          headers={"Accept": "application/json"})
        tasks = resp.json()
        for t in tasks:
            assert t["trial_count"] == 1

    def test_task_dedup_in_api(self, harbor_job_dir_with_reps):
        client = _make_client(harbor_job_dir_with_reps)
        resp = client.get("/api/runs/reps-test/tasks",
                          headers={"Accept": "application/json"})
        tasks = resp.json()
        names = [t["task_name"] for t in tasks]
        assert names.count("build-widget") == 1
        bw = [t for t in tasks if t["task_name"] == "build-widget"][0]
        assert bw["trial_count"] == 2


class TestStatsEnrichedTasks:
    """Tests for stats-enriched /api/runs/{job}/tasks endpoint."""

    def test_tasks_endpoint_has_stats(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        tasks = resp.json()
        assert len(tasks) == 2
        assert "total_rounds" in tasks[0]

    def test_tasks_endpoint_not_found(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/nonexistent/tasks",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 404

    def test_task_detail_has_stats(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert "total_rounds" in data
        assert "total_tokens_in" in data
        # action_sequence should NOT be duplicated into task detail
        assert "action_sequence" not in data


class TestCompareEndpoint:
    """Tests for GET /api/compare?a={job_a}&b={job_b}."""

    def test_compare_endpoint(self, harbor_job_dir):
        # Create a second run with one task that fails
        job2 = harbor_job_dir / "second-run"
        t = job2 / "build-widget__xyz999"
        _make_task(t, reward=0.0, transcript_entries=_passing_transcript(),
                   agent_stdout="[communicate:final] submitted\n")

        client = _make_client(harbor_job_dir)
        resp = client.get("/api/compare?a=full-test&b=second-run",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert "improved" in data
        assert "regressed" in data
        assert "stable_pass" in data
        assert "stable_fail" in data
        assert "only_a" in data
        assert "only_b" in data
        assert "run_a" in data
        assert "run_b" in data
        assert data["run_a"]["job_name"] == "full-test"
        assert data["run_b"]["job_name"] == "second-run"

    def test_compare_running_tasks_are_pending(self, harbor_job_dir):
        """In-progress tasks go in 'pending' bucket, not 'regressed'."""
        # Create a run with one running task (has transcript, no reward)
        job2 = harbor_job_dir / "in-progress-run"
        task = job2 / "build-widget__xyz999"
        task.mkdir(parents=True)
        sessions = task / "agent" / "serf-state" / "sessions"
        sessions.mkdir(parents=True)
        (sessions / "sess.transcript.jsonl").write_text(
            '{"kind": "header", "session_id": "s1", "model": "x", "depth": 0}\n')

        client = _make_client(harbor_job_dir)
        resp = client.get("/api/compare?a=full-test&b=in-progress-run",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        # build-widget: pass in A, running in B → should be "pending", not regressed
        assert "pending" in data
        bw_pending = [e for e in data["pending"] if e["task"] == "build-widget"]
        assert len(bw_pending) == 1
        assert bw_pending[0]["b"] == "running"
        # Must NOT appear in regressed
        bw_regressed = [e for e in data["regressed"] if e["task"] == "build-widget"]
        assert len(bw_regressed) == 0

    def test_compare_missing_run_a(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/compare?a=nonexistent&b=full-test",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 404

    def test_compare_missing_run_b(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/compare?a=full-test&b=nonexistent",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 404


class TestTaskHistoryEndpoint:
    """Tests for GET /api/tasks/{task_name}/history."""

    def test_task_history_endpoint(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/tasks/build-widget/history",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        history = resp.json()
        assert len(history) >= 1
        assert history[0]["job_name"] == "full-test"

    def test_task_history_not_found(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/tasks/nonexistent-task/history",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        history = resp.json()
        assert history == []


class TestRawFileEndpoint:
    """Tests for GET /raw/{file_path:path}."""

    def test_raw_json_endpoint(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/raw/full-test/build-widget__abc123/result.json")
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]
        # Should contain pretty-printed JSON in a <pre> block
        assert "<pre>" in resp.text

    def test_raw_jsonl_endpoint(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get(
            "/raw/full-test/build-widget__abc123/agent/serf-state/sessions"
            "/sess-main.transcript.jsonl"
        )
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]
        # JSONL renders each line separated by <hr>
        assert "<hr>" in resp.text

    def test_raw_plain_file(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get(
            "/raw/full-test/build-widget__abc123/agent/command-0/stdout.txt"
        )
        assert resp.status_code == 200
        assert "text/html" in resp.headers["content-type"]
        assert "<pre>" in resp.text

    def test_raw_blocks_traversal(self, harbor_job_dir):
        """Path traversal with plain ../ is blocked by Starlette (404).
        URL-encoded %2e%2e bypasses Starlette but our handler catches it (403)."""
        client = _make_client(harbor_job_dir)
        # Starlette normalizes plain ../ so it never reaches the handler
        resp = client.get("/raw/../../etc/passwd")
        assert resp.status_code in (403, 404)
        # URL-encoded dots bypass Starlette but our check catches them
        resp = client.get("/raw/%2e%2e/%2e%2e/etc/passwd")
        assert resp.status_code == 403

    def test_raw_not_found(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/raw/full-test/nonexistent.json")
        assert resp.status_code == 404

    def test_raw_escapes_html(self, harbor_job_dir):
        """Raw endpoint must escape HTML to prevent XSS."""
        # Create a file with HTML content
        xss_dir = harbor_job_dir / "xss-test__abc"
        xss_dir.mkdir(parents=True)
        (xss_dir / "evil.txt").write_text("<script>alert('xss')</script>")

        client = _make_client(harbor_job_dir)
        resp = client.get("/raw/xss-test__abc/evil.txt")
        assert resp.status_code == 200
        # The <script> tag should be escaped
        assert "<script>" not in resp.text
        assert "&lt;script&gt;" in resp.text


class TestTaskInstruction:
    """Task detail response includes instruction from command.txt."""

    def test_task_detail_has_instruction(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["instruction"] == "Build a widget that returns 42."

    def test_task_detail_has_command_raw_file(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        data = resp.json()
        raw_files = data.get("raw_files", {})
        assert "command" in raw_files


class TestAllFiles:
    """Task detail response includes all_files list."""

    def test_task_detail_has_all_files(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        data = resp.json()
        assert "all_files" in data
        assert len(data["all_files"]) > 0
        paths = [f["path"] for f in data["all_files"]]
        assert any("reward.txt" in p for p in paths)

    def test_all_files_have_raw_url(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        data = resp.json()
        for f in data["all_files"]:
            assert "raw_url" in f
            assert f["raw_url"].startswith("/raw/")


class TestSystemPrompt:
    """Task detail includes system_prompt from transcript header."""

    def test_task_detail_has_system_prompt_key(self, harbor_job_dir):
        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        data = resp.json()
        assert "system_prompt" in data

    def test_system_prompt_from_transcript(self, harbor_job_dir):
        """When transcript has system_prompt, it appears in response."""
        import json
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"
        # Rewrite the transcript with a system_prompt in the header
        tf = sessions_dir / "sess-main.transcript.jsonl"
        lines = tf.read_text().splitlines()
        header = json.loads(lines[0])
        header["system_prompt"] = "You are serf. Build things."
        lines[0] = json.dumps(header)
        tf.write_text("\n".join(lines) + "\n")

        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        data = resp.json()
        assert data["system_prompt"] == "You are serf. Build things."

    def test_initial_prompt_on_each_trajectory_node(self, harbor_job_dir):
        """Each root and child trajectory node includes the first
        USER_INPUT text as initial_prompt."""
        import json
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"

        # Add a second session: a depth-1 child of the root with USER_INPUT
        child_tf = sessions_dir / "sess-child.transcript.jsonl"
        child_lines = [
            {"kind": "header", "format_version": 1,
             "session_id": "sess-child",
             "parent_session_id": "sess-main",
             "parent_tool_call_id": "tc-spawn",
             "depth": 1, "model": "gpt-5.3-codex",
             "profile_id": "openai",
             "created_at": "2026-03-01T12:00:10Z"},
            {"kind": "entry", "seq": 0, "turn": {
                "kind": "USER_INPUT",
                "message": {"role": "user", "content": [
                    {"kind": "text", "text": "Write widget.py returning 42."}
                ]},
            }},
        ]
        child_tf.write_text("\n".join(json.dumps(l) for l in child_lines) + "\n")

        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        data = resp.json()

        # Root session's initial prompt came from _passing_transcript
        assert data["trajectory"][0]["initial_prompt"] == \
            "Build a widget that returns 42."
        # Child session's initial prompt
        assert data["trajectory"][0]["children"][0]["initial_prompt"] == \
            "Write widget.py returning 42."

    def test_system_prompt_on_each_trajectory_node(self, harbor_job_dir):
        """Each root and child trajectory node carries its session's
        system_prompt, so the structure view can show the prompt for
        every agent in the delegation tree."""
        import json
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"

        # Rewrite the root session's header with a system_prompt.
        root_tf = sessions_dir / "sess-main.transcript.jsonl"
        root_lines = root_tf.read_text().splitlines()
        root_header = json.loads(root_lines[0])
        root_header["system_prompt"] = "I am the coordinator."
        root_lines[0] = json.dumps(root_header)
        root_tf.write_text("\n".join(root_lines) + "\n")

        # Add a second session: a depth-1 child of the root.
        child_tf = sessions_dir / "sess-child.transcript.jsonl"
        child_header = {
            "kind": "header",
            "format_version": 1,
            "session_id": "sess-child",
            "parent_session_id": "sess-main",
            "parent_tool_call_id": "tc-spawn",
            "depth": 1,
            "model": "gpt-5.3-codex",
            "profile_id": "openai",
            "created_at": "2026-03-01T12:00:10Z",
            "system_prompt": "I am the implementer.",
        }
        child_entry = {"kind": "entry", "seq": 0, "turn": {
            "kind": "ASSISTANT",
            "message": {"role": "assistant", "content": [
                {"kind": "text", "text": "Done."}
            ]},
            "timestamp": "2026-03-01T12:00:11Z",
        }}
        child_tf.write_text(
            json.dumps(child_header) + "\n" + json.dumps(child_entry) + "\n"
        )

        client = _make_client(harbor_job_dir)
        resp = client.get("/api/runs/full-test/tasks/build-widget",
                          headers={"Accept": "application/json"})
        data = resp.json()

        # The root trajectory node carries its session's system_prompt.
        assert data["trajectory"][0]["system_prompt"] == "I am the coordinator."

        # The child trajectory node carries its own system_prompt.
        children = data["trajectory"][0]["children"]
        assert len(children) == 1
        assert children[0]["system_prompt"] == "I am the implementer."


class TestS3TranscriptFallback:
    """When a task is missing locally and job_name matches wave_repN,
    the server should sync the task from S3 via s3_client.sync_task()."""

    def test_s3_fallback_populates_task(self, tmp_path, monkeypatch):
        """S3 sync fills the local cache; second get_task finds the task."""
        import server as srv

        # Empty local data dir — no tasks present.
        srv.store = RunStore(tmp_path)
        srv._cache_dir = str(tmp_path / ".cache")

        # Fake S3 sync: create the task dir on disk where RunStore expects
        def fake_sync(wave, rep, task_name, cache_base):
            assert wave == "wave-demo"
            assert rep == 1
            assert task_name == "build-widget"
            job = cache_base / f"{wave}_rep{rep}"
            task_dir = job / f"{task_name}__fromS3X"
            verifier = task_dir / "verifier"
            verifier.mkdir(parents=True)
            (verifier / "reward.txt").write_text("1.0")
            (task_dir / "result.json").write_text(json.dumps({
                "config": {"model": "gpt-5.4-mini"},
                "started_at": "2026-04-05T00:00:00Z",
                "finished_at": "2026-04-05T00:05:00Z",
            }))
            sessions = task_dir / "agent" / "agent-state" / "sessions"
            sessions.mkdir(parents=True)
            (sessions / "s.transcript.jsonl").write_text(
                json.dumps({"kind": "header", "format_version": 1,
                             "session_id": "s1", "model": "gpt-5.4-mini",
                             "depth": 0, "system_prompt": "hi"}) + "\n"
            )
            return task_dir

        class FakeS3:
            def sync_task(self, wave, rep, task_name, cache_base):
                return fake_sync(wave, rep, task_name, cache_base)

        monkeypatch.setattr(srv, "s3_client", FakeS3())
        client = TestClient(srv.app)

        resp = client.get("/api/runs/wave-demo_rep1/tasks/build-widget",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        data = resp.json()
        assert data["passed"] is True
        assert data["reward"] == 1.0
        # Trajectory surfaced from the synced transcript
        assert data["trajectory"][0]["system_prompt"] == "hi"

    def test_s3_fallback_no_client_returns_404(self, tmp_path):
        """When s3_client is None, a missing task still 404s."""
        import server as srv
        srv.store = RunStore(tmp_path)
        srv.s3_client = None
        srv._cache_dir = str(tmp_path / ".cache")
        client = TestClient(srv.app)
        resp = client.get("/api/runs/wave-demo_rep1/tasks/missing",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 404

    def test_rep_query_param_syncs_requested_rep(self, tmp_path, monkeypatch):
        """?rep=N with a bare wave URL syncs that rep, not rep 1."""
        import server as srv
        srv.store = RunStore(tmp_path)
        srv._cache_dir = str(tmp_path / ".cache")

        call_args = []
        class FakeS3:
            def sync_task(self, wave, rep, task_name, cache_base):
                call_args.append((wave, rep))
                job = cache_base / f"{wave}_rep{rep}"
                task_dir = job / f"{task_name}__h{rep}"
                verifier = task_dir / "verifier"
                verifier.mkdir(parents=True)
                (verifier / "reward.txt").write_text(f"0.{rep}")
                (task_dir / "result.json").write_text(json.dumps({
                    "config": {"model": "m"},
                    "started_at": "2026-04-05T00:00:00Z",
                    "finished_at": "2026-04-05T00:05:00Z",
                }))
                sessions = task_dir / "agent" / "agent-state" / "sessions"
                sessions.mkdir(parents=True)
                (sessions / "s.transcript.jsonl").write_text(
                    json.dumps({"kind": "header", "format_version": 1,
                                 "session_id": f"s{rep}", "model": "m",
                                 "depth": 0}) + "\n"
                )
                return task_dir

        monkeypatch.setattr(srv, "s3_client", FakeS3())
        client = TestClient(srv.app)

        resp = client.get("/api/runs/wave-abc/tasks/thing?rep=3",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        assert call_args == [("wave-abc", 3)]
        data = resp.json()
        assert data["reward"] == 0.3

    def test_s3_fallback_bare_wave_name_defaults_to_rep1(self, tmp_path, monkeypatch):
        """Bare wave name (no _rep suffix) defaults to rep 1, syncs, retries
        as {wave}_rep1."""
        import server as srv
        srv.store = RunStore(tmp_path)
        srv._cache_dir = str(tmp_path / ".cache")

        call_args = []
        def fake_sync(wave, rep, task_name, cache_base):
            call_args.append((wave, rep, task_name))
            job = cache_base / f"{wave}_rep{rep}"
            task_dir = job / f"{task_name}__barewaveH"
            verifier = task_dir / "verifier"
            verifier.mkdir(parents=True)
            (verifier / "reward.txt").write_text("1.0")
            (task_dir / "result.json").write_text(json.dumps({
                "config": {"model": "gpt-5.4-mini"},
                "started_at": "2026-04-05T00:00:00Z",
                "finished_at": "2026-04-05T00:05:00Z",
            }))
            sessions = task_dir / "agent" / "agent-state" / "sessions"
            sessions.mkdir(parents=True)
            (sessions / "s.transcript.jsonl").write_text(
                json.dumps({"kind": "header", "format_version": 1,
                             "session_id": "s1", "model": "gpt-5.4-mini",
                             "depth": 0}) + "\n"
            )
            return task_dir

        class FakeS3:
            def sync_task(self, wave, rep, task_name, cache_base):
                return fake_sync(wave, rep, task_name, cache_base)

        monkeypatch.setattr(srv, "s3_client", FakeS3())
        client = TestClient(srv.app)

        resp = client.get("/api/runs/wave-bare/tasks/some-task",
                          headers={"Accept": "application/json"})
        assert resp.status_code == 200
        # sync called with rep=1 and the bare wave name
        assert call_args == [("wave-bare", 1, "some-task")]


class TestArtifactEndpoint:
    """Tests for GET /api/runs/{job}/tasks/{task}/artifacts."""

    def test_artifacts_listed(self, harbor_job_dir):
        """Task with artifacts returns file list with paths, sizes, raw_urls."""
        job_root = harbor_job_dir / "artifact-run"
        t = job_root / "build-widget__abc123"
        _make_task_with_artifacts(
            t, reward=1.0, transcript_entries=_passing_transcript(),
            artifacts={"main.py": "print(42)\n", "lib/util.py": "# utility helpers\n\n\n"},
        )
        client = _make_client(harbor_job_dir)
        resp = client.get(
            "/api/runs/artifact-run/tasks/build-widget/artifacts",
            headers={"Accept": "application/json"},
        )
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 2
        paths = [a["path"] for a in data]
        assert "lib/util.py" in paths
        assert "main.py" in paths
        # Sizes should match content length
        by_path = {a["path"]: a for a in data}
        assert by_path["main.py"]["size"] == len("print(42)\n")
        assert by_path["lib/util.py"]["size"] == len("# utility helpers\n\n\n")
        # Each file should have a raw_url
        assert "raw_url" in by_path["main.py"]
        assert "/raw/" in by_path["main.py"]["raw_url"]

    def test_no_artifacts_returns_empty(self, harbor_job_dir):
        """Task without artifacts returns empty list."""
        client = _make_client(harbor_job_dir)
        resp = client.get(
            "/api/runs/full-test/tasks/build-widget/artifacts",
            headers={"Accept": "application/json"},
        )
        assert resp.status_code == 200
        assert resp.json() == []

    def test_nonexistent_task_returns_404(self, harbor_job_dir):
        """Nonexistent task returns 404."""
        client = _make_client(harbor_job_dir)
        resp = client.get(
            "/api/runs/full-test/tasks/nope/artifacts",
            headers={"Accept": "application/json"},
        )
        assert resp.status_code == 404


class TestLiveRunRoutes:
    """Tests for live run API routes (/api/live/runs/*)."""

    SAMPLE_ENV = (
        "RUN_ID=exp-test-20260404-1807\n"
        "MODEL=openai/gpt-5.4-mini\n"
        "BENCHMARK=terminal-bench@2.0\n"
        "NUM_TASKS=2\n"
        "REPS=1\n"
        "INSTANCE_TYPE=r6i.large\n"
        "AGENT_IMPORT=serf_agent:SerfAgent\n"
        "LAUNCHED_AT=2026-04-04T18:07:14Z\n"
        "INSTANCE=i-aaaaaaaaaaaa REP=1 TASK=task-one\n"
        "INSTANCE=i-bbbbbbbbbbbb REP=1 TASK=task-two\n"
    )

    def _make_live_client(self, harbor_job_dir, state_dir):
        """Create a TestClient with LiveStore configured."""
        from live_store import LiveStore
        import server as srv
        srv.store = RunStore(harbor_job_dir)
        srv._cache_dir = str(harbor_job_dir / ".cache")
        srv.live_store = LiveStore(str(state_dir))
        return TestClient(srv.app)

    def test_list_runs_not_configured(self, harbor_job_dir):
        """Returns 501 when live_store is None."""
        import server as srv
        srv.store = RunStore(harbor_job_dir)
        srv._cache_dir = str(harbor_job_dir / ".cache")
        srv.live_store = None
        c = TestClient(srv.app)
        resp = c.get("/api/live/runs")
        assert resp.status_code == 501

    def test_list_runs(self, harbor_job_dir, tmp_path):
        (tmp_path / "exp-test.env").write_text(self.SAMPLE_ENV)
        client = self._make_live_client(harbor_job_dir, tmp_path)
        resp = client.get("/api/live/runs")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 1
        assert data[0]["run_id"] == "exp-test-20260404-1807"
        assert len(data[0]["instances"]) == 2

    def test_get_run_not_configured(self, harbor_job_dir):
        """Returns 501 when live_store is None."""
        import server as srv
        srv.store = RunStore(harbor_job_dir)
        srv._cache_dir = str(harbor_job_dir / ".cache")
        srv.live_store = None
        c = TestClient(srv.app)
        resp = c.get("/api/live/runs/some-run")
        assert resp.status_code == 501

    def test_get_run_not_found(self, harbor_job_dir, tmp_path):
        """Returns 404 when run_id doesn't exist."""
        (tmp_path / "exp-test.env").write_text(self.SAMPLE_ENV)
        client = self._make_live_client(harbor_job_dir, tmp_path)
        resp = client.get("/api/live/runs/missing")
        assert resp.status_code == 404

    def test_get_run_enriched(self, harbor_job_dir, tmp_path):
        """Returns run with AWS state merged in."""
        from unittest.mock import patch, MagicMock
        (tmp_path / "exp-test.env").write_text(self.SAMPLE_ENV)
        client = self._make_live_client(harbor_job_dir, tmp_path)

        with patch("live_store.subprocess.run") as mock_run:
            mock_run.return_value = MagicMock(
                stdout=json.dumps([[
                    {"Id": "i-aaaaaaaaaaaa", "State": "running",
                     "LaunchTime": "2026-04-04T18:07:14+00:00",
                     "PublicIP": "1.2.3.4"},
                ]]),
                returncode=0,
            )
            resp = client.get("/api/live/runs/exp-test-20260404-1807")

        assert resp.status_code == 200
        data = resp.json()
        assert data["run_id"] == "exp-test-20260404-1807"
        instances = {i["instance_id"]: i for i in data["instances"]}
        assert instances["i-aaaaaaaaaaaa"]["aws_state"] == "running"
        assert instances["i-aaaaaaaaaaaa"]["public_ip"] == "1.2.3.4"
        assert instances["i-bbbbbbbbbbbb"]["aws_state"] == "unknown"

    def test_stream_not_configured(self, harbor_job_dir):
        """Returns 501 when live_store is None."""
        import server as srv
        srv.store = RunStore(harbor_job_dir)
        srv._cache_dir = str(harbor_job_dir / ".cache")
        srv.live_store = None
        c = TestClient(srv.app)
        resp = c.get("/api/live/runs/some-run/stream")
        assert resp.status_code == 501
