"""Tests for per-task metrics computed from transcripts."""

import pytest

from data import RunStore
from stats import compute_task_stats


class TestComputeTaskStatsBuildWidget:
    """Metrics for build-widget: 4 rounds, PASS."""

    @pytest.fixture(autouse=True)
    def _setup(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        self.result = compute_task_stats(store, "full-test", "build-widget")

    def test_total_rounds(self):
        assert self.result["total_rounds"] == 4

    def test_rounds_by_action(self):
        assert self.result["rounds_by_action"] == {
            "EXPLORE": 1, "EDIT": 1, "EXEC": 1, "SUBMIT": 1,
        }

    def test_wasted_rounds(self):
        assert self.result["wasted_rounds"] == 0

    def test_total_tokens_in(self):
        assert self.result["total_tokens_in"] == 2550

    def test_total_tokens_out(self):
        assert self.result["total_tokens_out"] == 115

    def test_session_count(self):
        assert self.result["session_count"] == 1

    def test_max_depth(self):
        assert self.result["max_depth"] == 0

    def test_first_submit_round(self):
        assert self.result["first_submit_round"] == 4

    def test_submitted_value(self):
        assert self.result["submitted_value"] == "Widget implemented."

    def test_action_sequence(self):
        assert self.result["action_sequence"] == [
            "EXPLORE", "EDIT", "EXEC", "SUBMIT",
        ]


class TestComputeTaskStatsFixBug:
    """Metrics for fix-bug: 2 rounds, FAIL."""

    @pytest.fixture(autouse=True)
    def _setup(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        self.result = compute_task_stats(store, "full-test", "fix-bug")

    def test_total_rounds(self):
        assert self.result["total_rounds"] == 2

    def test_rounds_by_action(self):
        assert self.result["rounds_by_action"] == {"EXPLORE": 1, "SUBMIT": 1}

    def test_wasted_rounds(self):
        assert self.result["wasted_rounds"] == 0

    def test_total_tokens_in(self):
        assert self.result["total_tokens_in"] == 900

    def test_total_tokens_out(self):
        assert self.result["total_tokens_out"] == 25

    def test_session_count(self):
        assert self.result["session_count"] == 1

    def test_max_depth(self):
        assert self.result["max_depth"] == 0

    def test_first_submit_round(self):
        assert self.result["first_submit_round"] == 2

    def test_submitted_value(self):
        assert self.result["submitted_value"] == "Looks fine to me."

    def test_action_sequence(self):
        assert self.result["action_sequence"] == ["EXPLORE", "SUBMIT"]


class TestWallTime:
    """Wall time computed from result.json timestamps."""

    def test_wall_time_from_result_json(self, harbor_job_dir):
        """5m 30s = 330.0 seconds from default timestamps."""
        store = RunStore(harbor_job_dir)
        result = compute_task_stats(store, "full-test", "build-widget")
        assert result["wall_time_sec"] == 330.0

    def test_wall_time_missing_timestamps_returns_none(self, tmp_path):
        """result.json without started_at/finished_at returns None."""
        from conftest import _make_task, _passing_transcript
        job_root = tmp_path / "full-test" / "full-test"
        t1 = job_root / "build-widget__abc123"
        _make_task(t1, reward=1.0, transcript_entries=_passing_transcript(),
                   result_json={"config": {"model": "gpt-5.3-codex"}})
        store = RunStore(tmp_path / "full-test")
        result = compute_task_stats(store, "full-test", "build-widget")
        assert result["wall_time_sec"] is None


class TestApiMetrics:
    """API metrics computed from api.jsonl."""

    def test_api_call_count(self, harbor_job_dir_with_api):
        store = RunStore(harbor_job_dir_with_api)
        result = compute_task_stats(store, "full-test", "build-widget")
        assert result["api_call_count"] == 3

    def test_total_latency(self, harbor_job_dir_with_api):
        store = RunStore(harbor_job_dir_with_api)
        result = compute_task_stats(store, "full-test", "build-widget")
        assert result["total_latency_ms"] == 3500

    def test_avg_latency(self, harbor_job_dir_with_api):
        store = RunStore(harbor_job_dir_with_api)
        result = compute_task_stats(store, "full-test", "build-widget")
        assert result["avg_latency_ms"] == pytest.approx(1166.667, rel=1e-3)

    def test_empty_response_count(self, harbor_job_dir_with_api):
        store = RunStore(harbor_job_dir_with_api)
        result = compute_task_stats(store, "full-test", "build-widget")
        assert result["empty_response_count"] == 1

    def test_no_api_log_returns_none(self, harbor_job_dir):
        """Task without api.jsonl returns None for all API fields."""
        store = RunStore(harbor_job_dir)
        result = compute_task_stats(store, "full-test", "build-widget")
        assert result["api_call_count"] is None
        assert result["total_latency_ms"] is None
        assert result["avg_latency_ms"] is None
        assert result["empty_response_count"] is None


class TestComputeTaskStatsNotFound:
    """Returns None for non-existent tasks and jobs."""

    def test_missing_task(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert compute_task_stats(store, "full-test", "no-such-task") is None

    def test_missing_job(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert compute_task_stats(store, "no-such-job", "build-widget") is None
