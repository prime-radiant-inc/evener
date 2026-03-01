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


class TestComputeTaskStatsNotFound:
    """Returns None for non-existent tasks and jobs."""

    def test_missing_task(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert compute_task_stats(store, "full-test", "no-such-task") is None

    def test_missing_job(self, harbor_job_dir):
        store = RunStore(harbor_job_dir)
        assert compute_task_stats(store, "no-such-job", "build-widget") is None
