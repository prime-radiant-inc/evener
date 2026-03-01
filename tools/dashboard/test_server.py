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
                   agent_stdout="[submit_result] submitted\n")

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
