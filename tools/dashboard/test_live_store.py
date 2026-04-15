"""Tests for LiveStore — harbor-runner state files + AWS EC2 API."""

import json
from unittest.mock import patch, MagicMock

import pytest

from live_store import LiveStore


# ---------------------------------------------------------------------------
# Fixture data
# ---------------------------------------------------------------------------

SAMPLE_RUN = """\
RUN_ID=exp-alpha-20260404-1807
MODEL=openai/gpt-5.4-mini
BENCHMARK=terminal-bench@2.0
NUM_TASKS=89
REPS=1
INSTANCE_TYPE=r6i.large
AGENT_IMPORT=serf_agent:SerfAgent
INSTANCE_IDS=(i-01089e0808a3dd408)
LAUNCHED_AT=2026-04-04T18:07:14Z
INSTANCE=i-075d641420e79b594 REP=1 TASK=sqlite-with-gcov
INSTANCE=i-0e9709f1f4cc720c3 REP=1 TASK=extract-elf
"""

SECOND_RUN = """\
RUN_ID=exp-beta-20260404-1900
MODEL=openai/gpt-5.4-mini
BENCHMARK=terminal-bench@2.0
NUM_TASKS=2
REPS=1
INSTANCE_TYPE=r6i.large
AGENT_IMPORT=serf_agent:SerfAgent
LAUNCHED_AT=2026-04-04T19:00:00Z
INSTANCE=i-aaaaaaaaaaaa REP=1 TASK=task-one
INSTANCE=i-bbbbbbbbbbbb REP=1 TASK=task-two
"""


def _write(tmp_path, name, content):
    f = tmp_path / name
    f.write_text(content)
    return f


# ---------------------------------------------------------------------------
# list_runs / get_run
# ---------------------------------------------------------------------------

class TestListRuns:
    def test_parses_metadata_and_instances(self, tmp_path):
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        store = LiveStore(str(tmp_path))

        runs = store.list_runs()
        assert len(runs) == 1
        run = runs[0]

        assert run["run_id"] == "exp-alpha-20260404-1807"
        assert run["model"] == "openai/gpt-5.4-mini"
        assert run["benchmark"] == "terminal-bench@2.0"
        assert run["num_tasks"] == "89"
        assert run["reps"] == "1"
        assert run["instance_type"] == "r6i.large"
        assert run["agent_import"] == "serf_agent:SerfAgent"
        assert run["launched_at"] == "2026-04-04T18:07:14Z"

        assert len(run["instances"]) == 2
        assert run["instances"][0] == {
            "instance_id": "i-075d641420e79b594",
            "rep": "1",
            "task": "sqlite-with-gcov",
        }
        assert run["instances"][1] == {
            "instance_id": "i-0e9709f1f4cc720c3",
            "rep": "1",
            "task": "extract-elf",
        }

    def test_multiple_runs_concatenated_in_one_file(self, tmp_path):
        _write(tmp_path, "combined.env", SAMPLE_RUN + SECOND_RUN)
        store = LiveStore(str(tmp_path))

        runs = store.list_runs()
        assert len(runs) == 2
        # Sorted newest-first (by launched_at)
        assert runs[0]["run_id"] == "exp-beta-20260404-1900"
        assert runs[1]["run_id"] == "exp-alpha-20260404-1807"

    def test_multiple_files(self, tmp_path):
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        _write(tmp_path, "beta.env", SECOND_RUN)
        store = LiveStore(str(tmp_path))

        runs = store.list_runs()
        assert len(runs) == 2
        assert [r["run_id"] for r in runs] == [
            "exp-beta-20260404-1900",
            "exp-alpha-20260404-1807",
        ]

    def test_missing_state_dir(self, tmp_path):
        store = LiveStore(str(tmp_path / "nonexistent"))
        assert store.list_runs() == []

    def test_empty_state_dir(self, tmp_path):
        store = LiveStore(str(tmp_path))
        assert store.list_runs() == []

    def test_ignores_non_env_files(self, tmp_path):
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        _write(tmp_path, "README.md", "not a run")
        _write(tmp_path, "notes.txt", "stuff")
        store = LiveStore(str(tmp_path))

        runs = store.list_runs()
        assert len(runs) == 1
        assert runs[0]["run_id"] == "exp-alpha-20260404-1807"

    def test_sorted_by_launched_at_desc(self, tmp_path):
        older = SAMPLE_RUN  # 18:07
        newer = SECOND_RUN  # 19:00
        _write(tmp_path, "a.env", older)
        _write(tmp_path, "b.env", newer)
        store = LiveStore(str(tmp_path))

        runs = store.list_runs()
        assert runs[0]["launched_at"] > runs[1]["launched_at"]

    def test_instance_ids_array_ignored(self, tmp_path):
        """INSTANCE_IDS=(...) is ignored in favour of INSTANCE= rows."""
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        store = LiveStore(str(tmp_path))

        run = store.list_runs()[0]
        ids = {i["instance_id"] for i in run["instances"]}
        # The IDs came from INSTANCE= lines, not from the launch array.
        assert "i-01089e0808a3dd408" not in ids
        assert "i-075d641420e79b594" in ids

    def test_get_run_found(self, tmp_path):
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        store = LiveStore(str(tmp_path))

        run = store.get_run("exp-alpha-20260404-1807")
        assert run is not None
        assert run["run_id"] == "exp-alpha-20260404-1807"

    def test_get_run_not_found(self, tmp_path):
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        store = LiveStore(str(tmp_path))

        assert store.get_run("missing") is None


# ---------------------------------------------------------------------------
# query_instance_states
# ---------------------------------------------------------------------------

class TestQueryInstanceStates:
    @patch("live_store.subprocess.run")
    def test_builds_correct_command(self, mock_run, tmp_path):
        mock_run.return_value = MagicMock(stdout="[]", returncode=0)
        store = LiveStore(str(tmp_path), region="us-west-1")

        store.query_instance_states(["i-abc", "i-def"])

        assert mock_run.called
        cmd = mock_run.call_args[0][0]
        assert cmd[:3] == ["aws", "ec2", "describe-instances"]
        assert "--instance-ids" in cmd
        ids_start = cmd.index("--instance-ids") + 1
        assert cmd[ids_start] == "i-abc"
        assert cmd[ids_start + 1] == "i-def"
        assert "--region" in cmd
        assert cmd[cmd.index("--region") + 1] == "us-west-1"
        assert "--output" in cmd
        assert cmd[cmd.index("--output") + 1] == "json"

    @patch("live_store.subprocess.run")
    def test_parses_json_response(self, mock_run, tmp_path):
        response = [[
            {
                "Id": "i-abc",
                "State": "running",
                "LaunchTime": "2026-04-04T18:07:14+00:00",
                "PublicIP": "54.1.2.3",
            },
            {
                "Id": "i-def",
                "State": "terminated",
                "LaunchTime": "2026-04-04T18:07:14+00:00",
                "PublicIP": None,
            },
        ]]
        mock_run.return_value = MagicMock(
            stdout=json.dumps(response), returncode=0,
        )
        store = LiveStore(str(tmp_path))

        result = store.query_instance_states(["i-abc", "i-def"])

        assert result["i-abc"]["state"] == "running"
        assert result["i-abc"]["public_ip"] == "54.1.2.3"
        assert result["i-abc"]["launch_time"] == "2026-04-04T18:07:14+00:00"
        assert result["i-def"]["state"] == "terminated"
        assert result["i-def"]["public_ip"] is None

    @patch("live_store.subprocess.run")
    def test_parses_multiple_reservations(self, mock_run, tmp_path):
        response = [
            [{"Id": "i-abc", "State": "running",
              "LaunchTime": "2026-04-04T18:07:14+00:00", "PublicIP": "1.2.3.4"}],
            [{"Id": "i-def", "State": "pending",
              "LaunchTime": "2026-04-04T18:07:14+00:00", "PublicIP": None}],
        ]
        mock_run.return_value = MagicMock(
            stdout=json.dumps(response), returncode=0,
        )
        store = LiveStore(str(tmp_path))

        result = store.query_instance_states(["i-abc", "i-def"])

        assert set(result.keys()) == {"i-abc", "i-def"}
        assert result["i-abc"]["state"] == "running"
        assert result["i-def"]["state"] == "pending"

    @patch("live_store.subprocess.run")
    def test_empty_input_skips_subprocess(self, mock_run, tmp_path):
        store = LiveStore(str(tmp_path))
        result = store.query_instance_states([])
        assert result == {}
        assert not mock_run.called

    @patch("live_store.subprocess.run")
    def test_returns_empty_on_subprocess_error(self, mock_run, tmp_path):
        import subprocess
        mock_run.side_effect = subprocess.CalledProcessError(1, "aws")
        store = LiveStore(str(tmp_path))

        result = store.query_instance_states(["i-abc"])
        assert result == {}

    @patch("live_store.subprocess.run")
    def test_returns_empty_on_json_error(self, mock_run, tmp_path):
        mock_run.return_value = MagicMock(stdout="not json", returncode=0)
        store = LiveStore(str(tmp_path))

        result = store.query_instance_states(["i-abc"])
        assert result == {}

    @patch("live_store.subprocess.run")
    def test_returns_empty_when_aws_cli_missing(self, mock_run, tmp_path):
        mock_run.side_effect = FileNotFoundError("aws")
        store = LiveStore(str(tmp_path))

        result = store.query_instance_states(["i-abc"])
        assert result == {}


# ---------------------------------------------------------------------------
# run_with_live_state
# ---------------------------------------------------------------------------

class TestRunWithLiveState:
    @patch("live_store.subprocess.run")
    def test_merges_aws_state_into_instances(self, mock_run, tmp_path):
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        aws_response = [[
            {"Id": "i-075d641420e79b594", "State": "running",
             "LaunchTime": "2026-04-04T18:07:14+00:00", "PublicIP": "1.2.3.4"},
            {"Id": "i-0e9709f1f4cc720c3", "State": "terminated",
             "LaunchTime": "2026-04-04T18:07:14+00:00", "PublicIP": None},
        ]]
        mock_run.return_value = MagicMock(
            stdout=json.dumps(aws_response), returncode=0,
        )
        store = LiveStore(str(tmp_path))

        enriched = store.run_with_live_state("exp-alpha-20260404-1807")

        assert enriched is not None
        assert enriched["run_id"] == "exp-alpha-20260404-1807"
        assert len(enriched["instances"]) == 2

        first = enriched["instances"][0]
        assert first["instance_id"] == "i-075d641420e79b594"
        assert first["task"] == "sqlite-with-gcov"
        assert first["aws_state"] == "running"
        assert first["public_ip"] == "1.2.3.4"
        assert first["launch_time"] == "2026-04-04T18:07:14+00:00"

        second = enriched["instances"][1]
        assert second["aws_state"] == "terminated"
        assert second["public_ip"] is None

    @patch("live_store.subprocess.run")
    def test_unknown_state_when_not_in_aws_response(self, mock_run, tmp_path):
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        mock_run.return_value = MagicMock(stdout="[]", returncode=0)
        store = LiveStore(str(tmp_path))

        enriched = store.run_with_live_state("exp-alpha-20260404-1807")
        assert enriched is not None
        for inst in enriched["instances"]:
            assert inst["aws_state"] == "unknown"
            assert inst["launch_time"] is None
            assert inst["public_ip"] is None

    def test_returns_none_when_run_missing(self, tmp_path):
        _write(tmp_path, "alpha.env", SAMPLE_RUN)
        store = LiveStore(str(tmp_path))

        assert store.run_with_live_state("nope") is None


# ---------------------------------------------------------------------------
# sync_results / read_results / enrich_with_results
# ---------------------------------------------------------------------------

class TestSyncResults:
    @patch("live_store.subprocess.run")
    def test_builds_correct_command(self, mock_run, tmp_path):
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        store = LiveStore(str(tmp_path), region="us-west-1")
        cache_dir = tmp_path / "cache"

        ok = store.sync_results("exp-alpha", str(cache_dir))

        assert ok is True
        assert mock_run.called
        cmd = mock_run.call_args[0][0]
        assert cmd[:3] == ["aws", "s3", "sync"]
        assert cmd[3] == "s3://harbor-eval-results-526275945504/runs/exp-alpha/"
        assert cmd[4] == f"{cache_dir / 'exp-alpha'}/"
        assert "--exclude" in cmd
        assert cmd[cmd.index("--exclude") + 1] == "*"
        assert "--include" in cmd
        assert cmd[cmd.index("--include") + 1] == "*/result.json"
        assert "--region" in cmd
        assert cmd[cmd.index("--region") + 1] == "us-west-1"
        # Destination directory is created before sync.
        assert (cache_dir / "exp-alpha").is_dir()

    @patch("live_store.subprocess.run")
    def test_custom_bucket(self, mock_run, tmp_path):
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        store = LiveStore(str(tmp_path))
        store.sync_results(
            "exp-alpha", str(tmp_path / "cache"), bucket="my-test-bucket",
        )
        cmd = mock_run.call_args[0][0]
        assert cmd[3] == "s3://my-test-bucket/runs/exp-alpha/"

    @patch("live_store.subprocess.run")
    def test_returns_false_on_nonzero_exit(self, mock_run, tmp_path):
        mock_run.return_value = MagicMock(returncode=1, stdout="", stderr="no")
        store = LiveStore(str(tmp_path))
        assert store.sync_results("run", str(tmp_path / "cache")) is False

    @patch("live_store.subprocess.run")
    def test_returns_false_when_aws_cli_missing(self, mock_run, tmp_path):
        mock_run.side_effect = FileNotFoundError("aws")
        store = LiveStore(str(tmp_path))
        assert store.sync_results("run", str(tmp_path / "cache")) is False


class TestReadResults:
    def test_reads_from_cache_dir(self, tmp_path):
        """Walks cache dir, parses reward from verifier_result.rewards.reward."""
        cache_dir = tmp_path / "cache"
        run_id = "exp-alpha"
        run_dir = cache_dir / run_id

        # rep-1/build/task-one__abc123/result.json (pass)
        t1 = run_dir / "rep-1" / "build" / "task-one__abc123"
        t1.mkdir(parents=True)
        (t1 / "result.json").write_text(json.dumps({
            "verifier_result": {"rewards": {"reward": 1.0}},
        }))

        # rep-2/build/task-one__abc123/result.json (fail)
        t2 = run_dir / "rep-2" / "build" / "task-one__abc123"
        t2.mkdir(parents=True)
        (t2 / "result.json").write_text(json.dumps({
            "verifier_result": {"rewards": {"reward": 0.0}},
        }))

        # rep-1/build/task-two__def456/result.json (partial)
        t3 = run_dir / "rep-1" / "build" / "task-two__def456"
        t3.mkdir(parents=True)
        (t3 / "result.json").write_text(json.dumps({
            "verifier_result": {"rewards": {"reward": 0.5}},
        }))

        store = LiveStore(str(tmp_path))
        results = store.read_results(run_id, str(cache_dir))

        assert results == {
            "task-one": {1: 1.0, 2: 0.0},
            "task-two": {1: 0.5},
        }

    def test_empty_when_cache_dir_missing(self, tmp_path):
        store = LiveStore(str(tmp_path))
        assert store.read_results("nope", str(tmp_path / "cache")) == {}

    def test_falls_back_to_score_field(self, tmp_path):
        cache_dir = tmp_path / "cache"
        run_dir = cache_dir / "exp-alpha" / "rep-1" / "task__hash"
        run_dir.mkdir(parents=True)
        (run_dir / "result.json").write_text(json.dumps({"score": 0.75}))

        store = LiveStore(str(tmp_path))
        results = store.read_results("exp-alpha", str(cache_dir))
        assert results == {"task": {1: 0.75}}

    def test_reward_none_when_missing(self, tmp_path):
        cache_dir = tmp_path / "cache"
        run_dir = cache_dir / "exp-alpha" / "rep-1" / "task__hash"
        run_dir.mkdir(parents=True)
        (run_dir / "result.json").write_text(json.dumps({"other": "data"}))

        store = LiveStore(str(tmp_path))
        results = store.read_results("exp-alpha", str(cache_dir))
        assert results == {"task": {1: None}}

    def test_task_without_hash_suffix(self, tmp_path):
        cache_dir = tmp_path / "cache"
        run_dir = cache_dir / "exp-alpha" / "rep-1" / "plain-task"
        run_dir.mkdir(parents=True)
        (run_dir / "result.json").write_text(json.dumps({
            "verifier_result": {"rewards": {"reward": 1.0}},
        }))

        store = LiveStore(str(tmp_path))
        results = store.read_results("exp-alpha", str(cache_dir))
        assert results == {"plain-task": {1: 1.0}}

    def test_skips_files_without_rep_segment(self, tmp_path):
        cache_dir = tmp_path / "cache"
        # No rep-N in path — should be skipped.
        bad = cache_dir / "exp-alpha" / "other" / "task__hash"
        bad.mkdir(parents=True)
        (bad / "result.json").write_text(json.dumps({
            "verifier_result": {"rewards": {"reward": 1.0}},
        }))

        store = LiveStore(str(tmp_path))
        assert store.read_results("exp-alpha", str(cache_dir)) == {}

    def test_skips_invalid_json(self, tmp_path):
        cache_dir = tmp_path / "cache"
        run_dir = cache_dir / "exp-alpha" / "rep-1" / "task__hash"
        run_dir.mkdir(parents=True)
        (run_dir / "result.json").write_text("not json")

        store = LiveStore(str(tmp_path))
        assert store.read_results("exp-alpha", str(cache_dir)) == {}


class TestEnrichWithResults:
    @patch("live_store.subprocess.run")
    def test_merges_by_task_rep(self, mock_run, tmp_path):
        # Mock sync_results — we seed the cache dir directly.
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        cache_dir = tmp_path / "cache"
        run_id = "exp-alpha"

        # Seed cache with two results.
        t1 = cache_dir / run_id / "rep-1" / "task-one__abc"
        t1.mkdir(parents=True)
        (t1 / "result.json").write_text(json.dumps({
            "verifier_result": {"rewards": {"reward": 1.0}},
        }))
        t2 = cache_dir / run_id / "rep-1" / "task-two__def"
        t2.mkdir(parents=True)
        (t2 / "result.json").write_text(json.dumps({
            "verifier_result": {"rewards": {"reward": 0.0}},
        }))

        store = LiveStore(str(tmp_path))
        run_state = {
            "run_id": run_id,
            "instances": [
                {"instance_id": "i-a", "task": "task-one", "rep": "1",
                 "aws_state": "terminated"},
                {"instance_id": "i-b", "task": "task-two", "rep": "1",
                 "aws_state": "terminated"},
                {"instance_id": "i-c", "task": "task-three", "rep": "1",
                 "aws_state": "running"},
            ],
        }

        enriched = store.enrich_with_results(run_state, str(cache_dir))

        # task-one / rep 1 -> pass
        assert enriched["instances"][0]["reward"] == 1.0
        # task-two / rep 1 -> fail
        assert enriched["instances"][1]["reward"] == 0.0
        # task-three / rep 1 -> no result
        assert enriched["instances"][2]["reward"] is None

        # Original fields preserved
        assert enriched["instances"][0]["instance_id"] == "i-a"
        assert enriched["instances"][0]["aws_state"] == "terminated"

        # scores dict at top level
        assert enriched["scores"] == {
            "task-one": {1: 1.0},
            "task-two": {1: 0.0},
        }
        assert enriched["run_id"] == run_id

    @patch("live_store.subprocess.run")
    def test_handles_non_integer_rep(self, mock_run, tmp_path):
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        store = LiveStore(str(tmp_path))
        run_state = {
            "run_id": "exp-alpha",
            "instances": [
                {"instance_id": "i-a", "task": "task-one", "rep": ""},
                {"instance_id": "i-b", "task": "task-two", "rep": "abc"},
            ],
        }
        enriched = store.enrich_with_results(run_state, str(tmp_path / "c"))
        assert enriched["instances"][0]["reward"] is None
        assert enriched["instances"][1]["reward"] is None

    @patch("live_store.subprocess.run")
    def test_empty_instances(self, mock_run, tmp_path):
        mock_run.return_value = MagicMock(returncode=0, stdout="", stderr="")
        store = LiveStore(str(tmp_path))
        run_state = {"run_id": "exp-alpha", "instances": []}
        enriched = store.enrich_with_results(run_state, str(tmp_path / "c"))
        assert enriched["instances"] == []
        assert enriched["scores"] == {}
