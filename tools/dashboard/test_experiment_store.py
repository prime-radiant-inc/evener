"""Tests for ExperimentStore: experiment run metadata, scoreboard, task history."""

import json

import pytest

from experiment_store import ExperimentStore


class TestListExperiments:
    """ExperimentStore.list_experiments() returns run summaries."""

    def test_returns_all_runs_sorted_by_date_desc(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        runs = store.list_experiments()
        assert len(runs) == 2
        # Most recent first
        assert runs[0]["run_id"] == "wave-abc1234-20260401-0800"
        assert runs[1]["run_id"] == "v10-deleg-goldplate"

    def test_includes_computed_mean_score(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        runs = store.list_experiments()
        wave = runs[0]
        # (0.667 + 1.0) / 2 = 0.8335
        assert abs(wave["mean_score"] - 0.8335) < 0.001

    def test_includes_computed_task_count(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        runs = store.list_experiments()
        assert runs[0]["task_count"] == 2
        assert runs[1]["task_count"] == 1

    def test_includes_computed_perfect_count(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        runs = store.list_experiments()
        # Wave run: build-cython-ext has score 1.0
        assert runs[0]["perfect_count"] == 1
        # Experiment run: chess-best-move has score 0.0
        assert runs[1]["perfect_count"] == 0

    def test_filter_wave_runs(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        runs = store.list_experiments(run_type="wave")
        assert len(runs) == 1
        assert runs[0]["run_id"] == "wave-abc1234-20260401-0800"

    def test_filter_experiment_runs(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        runs = store.list_experiments(run_type="experiment")
        assert len(runs) == 1
        assert runs[0]["run_id"] == "v10-deleg-goldplate"

    def test_empty_directory(self, tmp_path):
        (tmp_path / "runs").mkdir()
        store = ExperimentStore(tmp_path)
        assert store.list_experiments() == []


class TestGetExperiment:
    """ExperimentStore.get_experiment() returns a single run."""

    def test_returns_run_dict_with_results(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        run = store.get_experiment("wave-abc1234-20260401-0800")
        assert run is not None
        assert run["run_id"] == "wave-abc1234-20260401-0800"
        assert "results" in run
        assert "chess-best-move" in run["results"]

    def test_nonexistent_returns_none(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        assert store.get_experiment("nonexistent") is None


class TestGetScoreboard:
    """ExperimentStore.get_scoreboard() returns scoreboard data."""

    def test_returns_full_scoreboard(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        sb = store.get_scoreboard()
        assert sb["model"] == "openai/gpt-5.4-mini"
        assert sb["total_tasks"] == 3
        assert len(sb["tasks"]) == 3

    def test_filter_failing(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        sb = store.get_scoreboard(filter="failing")
        # chess-best-move (0.667) and fix-bug (0.0) are < 1.0
        assert len(sb["tasks"]) == 2
        assert "build-cython-ext" not in sb["tasks"]

    def test_filter_solved(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        sb = store.get_scoreboard(filter="solved")
        # Only build-cython-ext has score == 1.0
        assert len(sb["tasks"]) == 1
        assert "build-cython-ext" in sb["tasks"]


class TestGetTaskHistory:
    """ExperimentStore.get_task_history() returns per-task history."""

    def test_returns_history_sorted_by_date_desc(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        history = store.get_task_history("chess-best-move")
        assert len(history) == 2
        # Most recent first
        assert history[0]["date"] == "2026-04-01"
        assert history[1]["date"] == "2026-03-25"

    def test_nonexistent_task_returns_empty_list(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        assert store.get_task_history("nonexistent") == []


class TestReload:
    """ExperimentStore.reload() picks up newly added files."""

    def test_reload_picks_up_new_run(self, experiment_dir):
        store = ExperimentStore(experiment_dir)
        assert len(store.list_experiments()) == 2

        # Add a new run file
        new_run = {
            "run_id": "v99-new-experiment",
            "date": "2026-04-03",
            "git_sha": "fff9999",
            "model": "openai/gpt-5.4-mini",
            "variant": "new thing",
            "results": {
                "chess-best-move": {
                    "score": 1.0,
                    "reps": [1.0],
                    "reps_pass": 1,
                    "reps_total": 1,
                },
            },
        }
        (experiment_dir / "runs" / "v99-new-experiment.json").write_text(
            json.dumps(new_run)
        )

        # Before reload, still 2
        assert len(store.list_experiments()) == 2

        store.reload()
        runs = store.list_experiments()
        assert len(runs) == 3
        # New run should be first (most recent date)
        assert runs[0]["run_id"] == "v99-new-experiment"
