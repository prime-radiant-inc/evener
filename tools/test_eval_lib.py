"""Tests for eval_lib — shared benchmark infrastructure helpers."""

import json
import os
import subprocess
import tempfile

import pytest

from eval_lib import (
    REMOTE,
    REMOTE_DIR,
    DATASET,
    git_info,
    make_run_id,
    build_harbor_command,
    build_manifest,
    read_archive_tasks,
)


class TestGitInfo:
    """git_info() returns SHA, dirty flag, and branch from a real repo."""

    def test_returns_sha(self):
        # We're in the serf repo, so this should work
        repo = os.path.dirname(os.path.dirname(__file__))
        info = git_info(repo)
        assert len(info["sha"]) >= 7
        assert all(c in "0123456789abcdef" for c in info["sha"])

    def test_returns_branch(self):
        repo = os.path.dirname(os.path.dirname(__file__))
        info = git_info(repo)
        assert isinstance(info["branch"], str)
        assert len(info["branch"]) > 0

    def test_dirty_is_bool(self):
        repo = os.path.dirname(os.path.dirname(__file__))
        info = git_info(repo)
        assert isinstance(info["dirty"], bool)

    def test_bad_repo_raises(self):
        with pytest.raises(subprocess.CalledProcessError):
            git_info("/nonexistent")


class TestMakeRunId:
    def test_format(self):
        run_id = make_run_id("my-job", "abc1234")
        # Format: YYYY-MM-DDTHHMMSSZ_job-name_sha
        parts = run_id.split("_", 2)
        assert len(parts) == 3
        assert parts[1] == "my-job"
        assert parts[2] == "abc1234"
        assert parts[0].endswith("Z")

    def test_contains_job_name(self):
        run_id = make_run_id("baseline-v3", "deadbeef")
        assert "baseline-v3" in run_id
        assert "deadbeef" in run_id


class TestBuildHarborCommand:
    def test_minimal(self):
        cmd = build_harbor_command(
            adapter="serf_agent:SerfAgent",
            model="openai/gpt-5.2-codex",
            reps=3,
            concurrency=4,
            job_name="test-job",
        )
        assert "harbor run" in cmd
        assert "--agent-import-path serf_agent:SerfAgent" in cmd
        assert f"--dataset {DATASET}" in cmd
        assert "--model openai/gpt-5.2-codex" in cmd
        assert "-k 3" in cmd
        assert "-n 4" in cmd
        assert "--job-name test-job" in cmd
        assert "--jobs-dir /tmp/test-job" in cmd

    def test_with_task(self):
        cmd = build_harbor_command(
            adapter="serf_agent:SerfAgent",
            model="openai/gpt-5.2-codex",
            reps=1,
            concurrency=2,
            job_name="test",
            task_name="build-cython-ext",
        )
        assert "--task-name build-cython-ext" in cmd

    def test_without_task(self):
        cmd = build_harbor_command(
            adapter="serf_agent:SerfAgent",
            model="openai/gpt-5.2-codex",
            reps=1,
            concurrency=2,
            job_name="test",
        )
        assert "--task-name" not in cmd

    def test_with_ak_args(self):
        cmd = build_harbor_command(
            adapter="serf_agent:SerfAgent",
            model="openai/gpt-5.2-codex",
            reps=1,
            concurrency=2,
            job_name="test",
            ak_args=["enable_reviewer_gate=true", "max_rounds=50"],
        )
        assert "--ak enable_reviewer_gate=true" in cmd
        assert "--ak max_rounds=50" in cmd

    def test_no_ak_args(self):
        cmd = build_harbor_command(
            adapter="serf_agent:SerfAgent",
            model="openai/gpt-5.2-codex",
            reps=1,
            concurrency=2,
            job_name="test",
        )
        assert "--ak" not in cmd


class TestBuildManifest:
    def test_required_fields(self):
        m = build_manifest(
            run_id="2026-02-28T200000Z_test_abc1234",
            job_name="test",
            git_sha="abc1234",
            git_dirty=False,
            git_branch="main",
            model="openai/gpt-5.2-codex",
            adapter="serf_agent:SerfAgent",
            reps=3,
            concurrency=4,
        )
        assert m["run_id"] == "2026-02-28T200000Z_test_abc1234"
        assert m["job_name"] == "test"
        assert m["git_sha"] == "abc1234"
        assert m["git_dirty"] is False
        assert m["model"] == "openai/gpt-5.2-codex"
        assert m["reps"] == 3
        assert "started_at" in m

    def test_task_name_default(self):
        m = build_manifest(
            run_id="x", job_name="test", git_sha="abc", git_dirty=False,
            git_branch="main", model="m", adapter="a", reps=1, concurrency=1,
        )
        assert m["task_name"] == "all"

    def test_task_name_set(self):
        m = build_manifest(
            run_id="x", job_name="test", git_sha="abc", git_dirty=False,
            git_branch="main", model="m", adapter="a", reps=1, concurrency=1,
            task_name="build-cython-ext",
        )
        assert m["task_name"] == "build-cython-ext"

    def test_ak_args(self):
        m = build_manifest(
            run_id="x", job_name="test", git_sha="abc", git_dirty=False,
            git_branch="main", model="m", adapter="a", reps=1, concurrency=1,
            ak_args=["foo=bar"],
        )
        assert m["ak_args"] == ["foo=bar"]

    def test_json_serializable(self):
        m = build_manifest(
            run_id="x", job_name="test", git_sha="abc", git_dirty=True,
            git_branch="main", model="m", adapter="a", reps=1, concurrency=1,
        )
        json.dumps(m)  # Should not raise


class TestReadArchiveTasks:
    def _make_archive(self, tmp_dir, tasks):
        """Create a minimal archive. tasks: {"name": [1.0, 0.0], ...}"""
        for task_name, rewards in tasks.items():
            for i, reward in enumerate(rewards, 1):
                rep_dir = os.path.join(tmp_dir, "tasks", task_name, f"rep-{i}")
                os.makedirs(rep_dir, exist_ok=True)
                with open(os.path.join(rep_dir, "reward.txt"), "w") as f:
                    f.write(str(reward))
                fc = ""
                if reward == 0.0:
                    fc = "wrong_answer"
                with open(os.path.join(rep_dir, "failure_category.txt"), "w") as f:
                    f.write(fc)

    def test_reads_tasks_and_reps(self):
        with tempfile.TemporaryDirectory() as d:
            self._make_archive(d, {"task-a": [1.0, 0.0], "task-b": [1.0]})
            tasks = read_archive_tasks(d)
            assert len(tasks) == 2
            assert "task-a" in tasks
            assert "task-b" in tasks
            assert len(tasks["task-a"]) == 2
            assert len(tasks["task-b"]) == 1

    def test_rep_structure(self):
        with tempfile.TemporaryDirectory() as d:
            self._make_archive(d, {"task-a": [1.0]})
            tasks = read_archive_tasks(d)
            rep = tasks["task-a"][0]
            assert rep["rep"] == 1
            assert rep["reward"] == 1.0
            assert rep["failure_category"] is None  # passing rep

    def test_failure_category(self):
        with tempfile.TemporaryDirectory() as d:
            self._make_archive(d, {"task-a": [0.0]})
            tasks = read_archive_tasks(d)
            rep = tasks["task-a"][0]
            assert rep["failure_category"] == "wrong_answer"

    def test_sorted_by_task_name(self):
        with tempfile.TemporaryDirectory() as d:
            self._make_archive(d, {"zzz": [1.0], "aaa": [1.0], "mmm": [1.0]})
            tasks = read_archive_tasks(d)
            assert list(tasks.keys()) == ["aaa", "mmm", "zzz"]

    def test_no_tasks_dir_raises(self):
        with tempfile.TemporaryDirectory() as d:
            with pytest.raises(FileNotFoundError):
                read_archive_tasks(d)

    def test_empty_tasks_dir(self):
        with tempfile.TemporaryDirectory() as d:
            os.makedirs(os.path.join(d, "tasks"))
            tasks = read_archive_tasks(d)
            assert len(tasks) == 0
