"""Tests for per-task and per-run metrics computed from transcripts."""

import pytest

from data import RunStore
from stats import compute_task_stats, compute_run_stats


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


class TestComputeRunStats:
    """Per-run aggregate metrics."""

    def test_pass_fail_counts(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert stats["passed"] == 1
        assert stats["failed"] == 1
        assert stats["total"] == 2

    def test_category_counts(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert stats["by_category"]["wrong_answer"] == 1

    def test_total_rounds(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert stats["total_rounds"] == 6  # 4 + 2

    def test_total_tokens(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert stats["total_tokens_in"] == 3450  # 2550 + 900

    def test_tasks_list(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        assert len(stats["tasks"]) == 2
        names = {t["task_name"] for t in stats["tasks"]}
        assert names == {"build-widget", "fix-bug"}

    def test_task_entries_have_stats(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        stats = compute_run_stats(store, "full-test")
        for t in stats["tasks"]:
            assert "total_rounds" in t
            assert "task_name" in t
            assert "passed" in t

    def test_not_found_returns_none(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert compute_run_stats(store, "nonexistent") is None


class TestStatsCache:
    """Disk caching for run stats."""

    def test_cache_written(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        cache_dir = harbor_job_dir / ".cache"
        compute_run_stats(store, "full-test", cache_dir=cache_dir)
        assert (cache_dir / "full-test" / "stats.json").is_file()

    def test_cache_hit(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        cache_dir = harbor_job_dir / ".cache"
        s1 = compute_run_stats(store, "full-test", cache_dir=cache_dir)
        s2 = compute_run_stats(store, "full-test", cache_dir=cache_dir)
        assert s1 == s2

    def test_cache_invalidation(self, harbor_job_dir):
        import time
        store = RunStore(harbor_job_dir)
        cache_dir = harbor_job_dir / ".cache"
        compute_run_stats(store, "full-test", cache_dir=cache_dir)

        time.sleep(0.05)
        # Touch a transcript file
        task_dir = harbor_job_dir / "full-test" / "build-widget__abc123"
        sessions_dir = task_dir / "agent" / "serf-state" / "sessions"
        tf = list(sessions_dir.iterdir())[0]
        tf.write_text(tf.read_text())

        s2 = compute_run_stats(store, "full-test", cache_dir=cache_dir)
        assert s2 is not None  # recomputed successfully
