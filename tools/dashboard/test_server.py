"""Integration tests for API routes."""

import os
import pytest
from fastapi.testclient import TestClient


@pytest.fixture
def client(harbor_job_dir):
    """Create a test client pointing at the fixture data."""
    os.environ["DASHBOARD_DATA_DIR"] = str(harbor_job_dir)
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
