"""Tests for eval results collection and scoreboard."""

import json
import os
import tempfile

import pytest

from eval_results import (
    ALL_TASKS,
    RunResult,
    TaskResult,
    compute_score,
    extract_rewards_from_cache,
    make_run_metadata,
    rebuild_scoreboard,
    update_task_scorecard,
)


class TestComputeScore:
    def test_all_pass(self):
        assert compute_score([1.0, 1.0, 1.0]) == 1.0

    def test_all_fail(self):
        assert compute_score([0.0, 0.0, 0.0]) == 0.0

    def test_mixed(self):
        assert abs(compute_score([1.0, 1.0, 0.0]) - 0.667) < 0.001

    def test_single_rep(self):
        assert compute_score([1.0]) == 1.0

    def test_empty_raises(self):
        with pytest.raises(ValueError):
            compute_score([])


class TestExtractRewardsFromCache:
    def test_reads_rewards(self, tmp_path):
        # Set up task-first cache structure:
        # tasks/kv-store-grpc/my-run/rep-1/reward.txt
        task_dir = tmp_path / "tasks" / "kv-store-grpc" / "my-run"
        for i, reward in enumerate([1.0, 0.0, 1.0], 1):
            rep = task_dir / f"rep-{i}"
            rep.mkdir(parents=True)
            (rep / "reward.txt").write_text(str(reward))

        result = extract_rewards_from_cache(tmp_path, "my-run")
        assert "kv-store-grpc" in result
        assert result["kv-store-grpc"] == [1.0, 0.0, 1.0]

    def test_multiple_tasks(self, tmp_path):
        for task, rewards in [("task-a", [1.0, 1.0]), ("task-b", [0.0])]:
            for i, r in enumerate(rewards, 1):
                rep = tmp_path / "tasks" / task / "my-run" / f"rep-{i}"
                rep.mkdir(parents=True)
                (rep / "reward.txt").write_text(str(r))

        result = extract_rewards_from_cache(tmp_path, "my-run")
        assert result["task-a"] == [1.0, 1.0]
        assert result["task-b"] == [0.0]

    def test_missing_run(self, tmp_path):
        (tmp_path / "tasks").mkdir()
        result = extract_rewards_from_cache(tmp_path, "nonexistent")
        assert result == {}


class TestMakeRunMetadata:
    def test_basic(self):
        rewards = {"kv-store-grpc": [1.0, 1.0, 0.0], "chess-best-move": [1.0, 1.0, 1.0]}
        meta = make_run_metadata(
            run_id="v20-test",
            date="2026-03-27",
            git_sha="abc1234",
            model="openai/gpt-5.4-mini",
            variant="test variant",
            rewards=rewards,
        )
        assert meta["run_id"] == "v20-test"
        assert meta["git_sha"] == "abc1234"
        assert abs(meta["results"]["kv-store-grpc"]["score"] - 0.667) < 0.001
        assert meta["results"]["chess-best-move"]["score"] == 1.0

    def test_s3_prefix(self):
        meta = make_run_metadata("v20-x", "2026-03-27", "abc", "model", "var", {"t": [1.0]})
        assert "s3://" in meta["s3_prefix"]


class TestUpdateTaskScorecard:
    def test_new_task(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()

        update_task_scorecard(
            tasks_dir=tasks_dir,
            task="kv-store-grpc",
            run_id="v20-a",
            date="2026-03-27",
            git_sha="abc",
            model="openai/gpt-5.4-mini",
            reps=[1.0, 1.0, 0.0],
        )

        card = json.loads((tasks_dir / "kv-store-grpc.json").read_text())
        assert card["task"] == "kv-store-grpc"
        assert abs(card["current_score"] - 0.667) < 0.001
        assert card["current_run"] == "v20-a"
        assert len(card["history"]) == 1

    def test_append_newer_run(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()

        # First run
        update_task_scorecard(tasks_dir, "t", "run-1", "2026-03-26", "aaa", "m", [0.0, 0.0])
        # Second (newer) run
        update_task_scorecard(tasks_dir, "t", "run-2", "2026-03-27", "bbb", "m", [1.0, 1.0])

        card = json.loads((tasks_dir / "t.json").read_text())
        assert card["current_score"] == 1.0
        assert card["current_run"] == "run-2"
        assert len(card["history"]) == 2

    def test_older_run_doesnt_update_current(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()

        # Newer run first
        update_task_scorecard(tasks_dir, "t", "run-2", "2026-03-27", "bbb", "m", [1.0, 1.0])
        # Older run added later (backfill)
        update_task_scorecard(tasks_dir, "t", "run-1", "2026-03-26", "aaa", "m", [0.0, 0.0])

        card = json.loads((tasks_dir / "t.json").read_text())
        assert card["current_score"] == 1.0  # Still from run-2
        assert card["current_run"] == "run-2"
        assert len(card["history"]) == 2

    def test_same_date_higher_score_wins(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()

        # Lower-scoring run first
        update_task_scorecard(tasks_dir, "t", "run-a", "2026-03-27", "aaa", "m", [0.0, 0.0])
        # Higher-scoring run same date
        update_task_scorecard(tasks_dir, "t", "run-b", "2026-03-27", "bbb", "m", [1.0, 1.0])

        card = json.loads((tasks_dir / "t.json").read_text())
        assert card["current_score"] == 1.0
        assert card["current_run"] == "run-b"

    def test_same_date_lower_score_doesnt_overwrite(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()

        # Higher-scoring run first
        update_task_scorecard(tasks_dir, "t", "run-a", "2026-03-27", "aaa", "m", [1.0, 1.0])
        # Lower-scoring run same date
        update_task_scorecard(tasks_dir, "t", "run-b", "2026-03-27", "bbb", "m", [0.0, 0.0])

        card = json.loads((tasks_dir / "t.json").read_text())
        assert card["current_score"] == 1.0
        assert card["current_run"] == "run-a"

    def test_duplicate_run_not_appended(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()

        update_task_scorecard(tasks_dir, "t", "run-1", "2026-03-27", "aaa", "m", [1.0])
        update_task_scorecard(tasks_dir, "t", "run-1", "2026-03-27", "aaa", "m", [1.0])

        card = json.loads((tasks_dir / "t.json").read_text())
        assert len(card["history"]) == 1


class TestRebuildScoreboard:
    def test_builds_from_task_files(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()

        # Create two task scorecards
        for task, score in [("task-a", 1.0), ("task-b", 0.667)]:
            (tasks_dir / f"{task}.json").write_text(json.dumps({
                "task": task,
                "current_score": score,
                "current_run": "run-1",
                "current_date": "2026-03-27",
                "status": "tested",
                "history": [],
            }))

        board = rebuild_scoreboard(tasks_dir, "gpt-5.4-mini")
        assert board["model"] == "gpt-5.4-mini"
        assert board["tested_tasks"] == 2
        assert board["tasks"]["task-a"]["score"] == 1.0
        assert abs(board["tasks"]["task-b"]["score"] - 0.667) < 0.001

    def test_untested_tasks_shown(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()
        # No task files — all 89 should be untested
        board = rebuild_scoreboard(tasks_dir, "gpt-5.4-mini")
        assert board["tested_tasks"] == 0
        assert board["total_tasks"] == len(ALL_TASKS)

    def test_mean_score_excludes_untested(self, tmp_path):
        tasks_dir = tmp_path / "tasks"
        tasks_dir.mkdir()
        (tasks_dir / "task-a.json").write_text(json.dumps({
            "task": "task-a",
            "current_score": 0.5,
            "current_run": "r",
            "current_date": "2026-03-27",
            "status": "tested",
            "history": [],
        }))
        board = rebuild_scoreboard(tasks_dir, "m")
        assert board["mean_score"] == 0.5  # Only tested tasks count


class TestAllTasks:
    def test_count(self):
        assert len(ALL_TASKS) == 89

    def test_includes_solvable(self):
        from task_sets import TASK_SETS
        for task in TASK_SETS["solvable"]:
            assert task in ALL_TASKS

    def test_no_duplicates(self):
        assert len(ALL_TASKS) == len(set(ALL_TASKS))
