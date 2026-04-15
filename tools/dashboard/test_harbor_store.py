"""Tests for HarborStore — reads completed wave results from harbor state."""

from pathlib import Path

from harbor_store import HarborStore


def _make_wave(base: Path, wave_id: str, tasks_by_rep: dict, env_fields: dict = None):
    """Create a harbor wave result tree under base/state.

    tasks_by_rep: {rep_num: {task_name: pytest_summary_or_None}}
    env_fields: k/v to write into runs/{wave_id}.env
    """
    state = base
    runs_dir = state / "runs"
    results_dir = state / "results"
    runs_dir.mkdir(parents=True, exist_ok=True)
    results_dir.mkdir(parents=True, exist_ok=True)

    # Env file
    env = env_fields or {}
    env.setdefault("RUN_ID", wave_id)
    env.setdefault("MODEL", "openai/gpt-5.4-mini")
    env.setdefault("NUM_TASKS", "5")
    (runs_dir / f"{wave_id}.env").write_text(
        "\n".join(f"{k}={v}" for k, v in env.items()) + "\n"
    )

    # Wave dir with per-rep task data
    wave_dir = results_dir / wave_id
    wave_dir.mkdir()
    for rep_num, tasks in tasks_by_rep.items():
        rep_dir = wave_dir / f"rep-{rep_num}"
        inner = rep_dir / f"{wave_id}_rep{rep_num}"
        inner.mkdir(parents=True)
        for task_name, summary in tasks.items():
            task_dir = inner / f"{task_name}__hash{rep_num}"
            verifier = task_dir / "verifier"
            verifier.mkdir(parents=True)
            if summary is None:
                # No output (task not complete)
                pass
            else:
                (verifier / "test-stdout.txt").write_text(
                    f"some test output\n"
                    f"============ {summary} in 0.5s =============\n"
                )


class TestHarborStoreDiscovery:
    def test_empty_state(self, tmp_path):
        store = HarborStore(str(tmp_path))
        assert store.list_runs() == []

    def test_missing_state_dir(self, tmp_path):
        store = HarborStore(str(tmp_path / "does-not-exist"))
        assert store.list_runs() == []

    def test_lists_single_wave(self, tmp_path):
        _make_wave(tmp_path, "wave-abc1234-20260405-1234", {
            1: {"build-widget": "3 passed"},
        })
        store = HarborStore(str(tmp_path))
        runs = store.list_runs()
        assert len(runs) == 1
        assert runs[0]["run_id"] == "wave-abc1234-20260405-1234"
        assert runs[0]["git_sha"] == "abc1234"
        assert runs[0]["date"] == "2026-04-05"
        assert runs[0]["model"] == "openai/gpt-5.4-mini"


class TestHarborStoreScoring:
    def test_passing_task_reward_1(self, tmp_path):
        _make_wave(tmp_path, "wave-abc1234-20260405-1234", {
            1: {"build-widget": "3 passed"},
        })
        store = HarborStore(str(tmp_path))
        run = store.list_runs()[0]
        assert run["results"]["build-widget"]["score"] == 1.0
        assert run["results"]["build-widget"]["reps"] == [1.0]
        assert run["results"]["build-widget"]["reps_pass"] == 1
        assert run["results"]["build-widget"]["reps_total"] == 1

    def test_failing_task_reward_0(self, tmp_path):
        _make_wave(tmp_path, "wave-abc1234-20260405-1234", {
            1: {"build-widget": "2 failed, 1 passed"},
        })
        store = HarborStore(str(tmp_path))
        run = store.list_runs()[0]
        assert run["results"]["build-widget"]["score"] == 0.0
        assert run["results"]["build-widget"]["reps"] == [0.0]
        assert run["results"]["build-widget"]["reps_pass"] == 0

    def test_multi_rep_partial(self, tmp_path):
        _make_wave(tmp_path, "wave-abc1234-20260405-1234", {
            1: {"build-widget": "3 passed"},
            2: {"build-widget": "1 failed, 2 passed"},
            3: {"build-widget": "3 passed"},
        })
        store = HarborStore(str(tmp_path))
        run = store.list_runs()[0]
        r = run["results"]["build-widget"]
        assert r["reps"] == [1.0, 0.0, 1.0]
        assert abs(r["score"] - 0.667) < 0.001
        assert r["reps_pass"] == 2
        assert r["reps_total"] == 3

    def test_task_with_no_output_reward_none(self, tmp_path):
        _make_wave(tmp_path, "wave-abc1234-20260405-1234", {
            1: {"build-widget": None},  # task never produced output
        })
        store = HarborStore(str(tmp_path))
        run = store.list_runs()[0]
        # Task still shows up but with None reward and excluded from scoring
        assert "build-widget" not in run["results"]

    def test_reward_txt_overrides_parse(self, tmp_path):
        _make_wave(tmp_path, "wave-abc1234-20260405-1234", {
            1: {"build-widget": "2 failed, 1 passed"},
        })
        # Add explicit reward.txt saying 0.75
        task_dir = (tmp_path / "results" / "wave-abc1234-20260405-1234" /
                    "rep-1" / "wave-abc1234-20260405-1234_rep1" / "build-widget__hash1")
        (task_dir / "verifier" / "reward.txt").write_text("0.75\n")
        store = HarborStore(str(tmp_path))
        run = store.list_runs()[0]
        assert run["results"]["build-widget"]["reps"] == [0.75]


class TestHarborStoreGetRun:
    def test_get_run_by_id(self, tmp_path):
        _make_wave(tmp_path, "wave-abc-20260405-1234", {
            1: {"build-widget": "3 passed"},
        })
        store = HarborStore(str(tmp_path))
        run = store.get_run("wave-abc-20260405-1234")
        assert run is not None
        assert run["run_id"] == "wave-abc-20260405-1234"

    def test_get_run_unknown(self, tmp_path):
        store = HarborStore(str(tmp_path))
        assert store.get_run("missing") is None


def _make_env_only(base: Path, wave_id: str, env_fields: dict = None):
    """Create only a .env file (no local results) - simulates S3-only run."""
    runs_dir = base / "runs"
    results_dir = base / "results"
    runs_dir.mkdir(parents=True, exist_ok=True)
    results_dir.mkdir(parents=True, exist_ok=True)

    env = env_fields or {}
    env.setdefault("RUN_ID", wave_id)
    env.setdefault("MODEL", "openai/gpt-5.4-mini")
    env.setdefault("NUM_TASKS", "89")
    (runs_dir / f"{wave_id}.env").write_text(
        "\n".join(f"{k}={v}" for k, v in env.items()) + "\n"
    )


class TestHarborStoreS3Only:
    """Tests for runs that exist on S3 but not locally."""

    def test_list_includes_env_only_runs(self, tmp_path):
        """Runs with .env but no local results should appear in list."""
        # One run with local results
        _make_wave(tmp_path, "wave-local-20260405-1234", {
            1: {"build-widget": "3 passed"},
        })
        # One run with only .env (S3-only)
        _make_env_only(tmp_path, "wave-s3only-20260406-1234")

        store = HarborStore(str(tmp_path))
        runs = store.list_runs()

        assert len(runs) == 2
        run_ids = {r["run_id"] for r in runs}
        assert "wave-local-20260405-1234" in run_ids
        assert "wave-s3only-20260406-1234" in run_ids

    def test_env_only_run_has_metadata(self, tmp_path):
        """S3-only runs should have basic metadata from .env file."""
        _make_env_only(tmp_path, "wave-s3only-20260406-1234", {
            "MODEL": "anthropic/claude-sonnet-4",
        })

        store = HarborStore(str(tmp_path))
        runs = store.list_runs()

        assert len(runs) == 1
        run = runs[0]
        assert run["run_id"] == "wave-s3only-20260406-1234"
        assert run["date"] == "2026-04-06"
        assert run["model"] == "anthropic/claude-sonnet-4"
        assert run["results"] == {}  # No local results yet

    def test_env_only_run_sorted_by_date(self, tmp_path):
        """S3-only runs should sort correctly with local runs."""
        _make_wave(tmp_path, "wave-older-20260401-1234", {
            1: {"task": "3 passed"},
        })
        _make_env_only(tmp_path, "wave-newer-20260410-1234")

        store = HarborStore(str(tmp_path))
        runs = store.list_runs()

        # Should be sorted newest first
        assert runs[0]["run_id"] == "wave-newer-20260410-1234"
        assert runs[1]["run_id"] == "wave-older-20260401-1234"


class TestHarborStoreS3Backfill:
    """Tests for S3 backfill when fetching a run without local results."""

    def test_get_run_s3_only_without_client_returns_empty(self, tmp_path):
        """get_run for S3-only run without s3_client returns run with empty results."""
        _make_env_only(tmp_path, "wave-s3only-20260406-1234")

        store = HarborStore(str(tmp_path), s3_client=None)
        run = store.get_run("wave-s3only-20260406-1234")

        assert run is not None
        assert run["run_id"] == "wave-s3only-20260406-1234"
        assert run["results"] == {}
