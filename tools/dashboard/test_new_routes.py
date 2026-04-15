"""Tests for experiment API routes (/api/experiments/*, /api/scoreboard)."""

import pytest
from fastapi.testclient import TestClient

from data import RunStore
from experiment_store import ExperimentStore


def _make_experiment_client(harbor_job_dir, experiment_dir):
    """Create a TestClient with both RunStore and ExperimentStore wired up."""
    import server as srv
    srv.store = RunStore(harbor_job_dir)
    srv._cache_dir = str(harbor_job_dir / ".cache")
    srv.experiment_store = ExperimentStore(experiment_dir)
    return TestClient(srv.app)


@pytest.fixture
def client(harbor_job_dir, experiment_dir):
    return _make_experiment_client(harbor_job_dir, experiment_dir)


class TestListExperiments:
    def test_list_experiments(self, client):
        resp = client.get("/api/experiments")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 2

    def test_list_experiments_filter_waves(self, client):
        resp = client.get("/api/experiments?type=wave")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 1
        assert data[0]["run_id"].startswith("wave-")

    def test_list_experiments_filter_experiment(self, client):
        resp = client.get("/api/experiments?type=experiment")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 1
        assert not data[0]["run_id"].startswith("wave-")

    def test_list_experiments_not_configured(self, harbor_job_dir):
        """Returns 501 when experiment_store is None."""
        import server as srv
        srv.store = RunStore(harbor_job_dir)
        srv._cache_dir = str(harbor_job_dir / ".cache")
        srv.experiment_store = None
        c = TestClient(srv.app)
        resp = c.get("/api/experiments")
        assert resp.status_code == 501


class TestGetExperiment:
    def test_get_experiment(self, client):
        resp = client.get("/api/experiments/wave-abc1234-20260401-0800")
        assert resp.status_code == 200
        data = resp.json()
        assert data["run_id"] == "wave-abc1234-20260401-0800"
        assert "results" in data

    def test_get_experiment_not_found(self, client):
        resp = client.get("/api/experiments/nonexistent")
        assert resp.status_code == 404


class TestGetScoreboard:
    def test_get_scoreboard(self, client):
        resp = client.get("/api/scoreboard")
        assert resp.status_code == 200
        data = resp.json()
        assert data["total_tasks"] == 3
        assert len(data["tasks"]) == 3

    def test_get_scoreboard_filter_failing(self, client):
        resp = client.get("/api/scoreboard?filter=failing")
        assert resp.status_code == 200
        data = resp.json()
        # chess-best-move (0.667) and fix-bug (0.0) are < 1.0
        assert len(data["tasks"]) == 2
        assert "build-cython-ext" not in data["tasks"]

    def test_get_scoreboard_filter_solved(self, client):
        resp = client.get("/api/scoreboard?filter=solved")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data["tasks"]) == 1
        assert "build-cython-ext" in data["tasks"]

    def test_get_scoreboard_not_configured(self, harbor_job_dir):
        """Returns 501 when experiment_store is None."""
        import server as srv
        srv.store = RunStore(harbor_job_dir)
        srv._cache_dir = str(harbor_job_dir / ".cache")
        srv.experiment_store = None
        c = TestClient(srv.app)
        resp = c.get("/api/scoreboard")
        assert resp.status_code == 501


class TestTaskHistory:
    def test_get_task_history(self, client):
        resp = client.get("/api/experiments/tasks/chess-best-move/history")
        assert resp.status_code == 200
        data = resp.json()
        assert len(data) == 2
        # Most recent first
        assert data[0]["date"] == "2026-04-01"

    def test_task_history_unknown(self, client):
        resp = client.get("/api/experiments/tasks/nonexistent-task/history")
        assert resp.status_code == 200
        data = resp.json()
        assert data == []

    def test_task_history_not_configured(self, harbor_job_dir):
        """Returns 501 when experiment_store is None."""
        import server as srv
        srv.store = RunStore(harbor_job_dir)
        srv._cache_dir = str(harbor_job_dir / ".cache")
        srv.experiment_store = None
        c = TestClient(srv.app)
        resp = c.get("/api/experiments/tasks/chess-best-move/history")
        assert resp.status_code == 501
