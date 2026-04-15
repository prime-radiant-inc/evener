"""Tests for S3Client — thin wrapper around AWS CLI."""

import json
import subprocess

import pytest

from s3_client import S3Client


BUCKET = "harbor-eval-results-526275945504"
REGION = "us-west-1"


class TestListObjects:
    """S3Client.list_objects() parses aws s3 ls output."""

    def test_parses_recursive_ls_output(self, monkeypatch):
        """Parses 'aws s3 ls --recursive' output into key list."""
        fake_output = (
            "2026-04-01 08:00:00     1234 runs/wave-abc/rep-1/result.json\n"
            "2026-04-01 08:01:00      567 runs/wave-abc/rep-2/result.json\n"
        )
        called_with = {}

        def fake_run(cmd, **kwargs):
            called_with["cmd"] = cmd
            called_with["kwargs"] = kwargs
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout=fake_output, stderr=""
            )

        monkeypatch.setattr(subprocess, "run", fake_run)

        client = S3Client(BUCKET, region=REGION)
        keys = client.list_objects("runs/wave-abc/")

        assert keys == [
            "runs/wave-abc/rep-1/result.json",
            "runs/wave-abc/rep-2/result.json",
        ]
        # Verify correct command was built
        assert called_with["cmd"] == [
            "aws", "s3", "ls",
            f"s3://{BUCKET}/runs/wave-abc/",
            "--region", REGION, "--recursive",
        ]
        assert called_with["kwargs"]["capture_output"] is True
        assert called_with["kwargs"]["text"] is True

    def test_returns_empty_list_on_error(self, monkeypatch):
        """Returns [] when aws cli fails."""
        def fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(
                args=cmd, returncode=1, stdout="", stderr="An error occurred"
            )

        monkeypatch.setattr(subprocess, "run", fake_run)

        client = S3Client(BUCKET, region=REGION)
        assert client.list_objects("runs/nonexistent/") == []

    def test_returns_empty_list_on_empty_output(self, monkeypatch):
        """Returns [] when prefix has no objects."""
        def fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        monkeypatch.setattr(subprocess, "run", fake_run)

        client = S3Client(BUCKET, region=REGION)
        assert client.list_objects("runs/empty/") == []


class TestGetJson:
    """S3Client.get_json() fetches and parses JSON from S3."""

    def test_fetches_and_parses_json(self, monkeypatch):
        """Fetches JSON via 'aws s3 cp ... -' and parses it."""
        payload = {"score": 1.0, "task": "chess-best-move"}
        called_with = {}

        def fake_run(cmd, **kwargs):
            called_with["cmd"] = cmd
            return subprocess.CompletedProcess(
                args=cmd, returncode=0,
                stdout=json.dumps(payload), stderr=""
            )

        monkeypatch.setattr(subprocess, "run", fake_run)

        client = S3Client(BUCKET, region=REGION)
        result = client.get_json("runs/wave-abc/rep-1/result.json")

        assert result == payload
        assert called_with["cmd"] == [
            "aws", "s3", "cp",
            f"s3://{BUCKET}/runs/wave-abc/rep-1/result.json",
            "-", "--region", REGION,
        ]

    def test_returns_none_on_cli_error(self, monkeypatch):
        """Returns None when aws cli fails."""
        def fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(
                args=cmd, returncode=1, stdout="", stderr="Not found"
            )

        monkeypatch.setattr(subprocess, "run", fake_run)

        client = S3Client(BUCKET, region=REGION)
        assert client.get_json("runs/nonexistent/result.json") is None

    def test_returns_none_on_invalid_json(self, monkeypatch):
        """Returns None when output is not valid JSON."""
        def fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="not json {{{", stderr=""
            )

        monkeypatch.setattr(subprocess, "run", fake_run)

        client = S3Client(BUCKET, region=REGION)
        assert client.get_json("runs/wave-abc/result.json") is None


class TestSyncToLocal:
    """S3Client.sync_to_local() builds correct aws s3 sync command."""

    def test_builds_correct_sync_command(self, monkeypatch, tmp_path):
        """Runs 'aws s3 sync' with correct args and returns True on success."""
        called_with = {}

        def fake_run(cmd, **kwargs):
            called_with["cmd"] = cmd
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        monkeypatch.setattr(subprocess, "run", fake_run)

        client = S3Client(BUCKET, region=REGION)
        local_dir = str(tmp_path / "downloads")
        result = client.sync_to_local("runs/wave-abc/", local_dir)

        assert result is True
        assert called_with["cmd"] == [
            "aws", "s3", "sync",
            f"s3://{BUCKET}/runs/wave-abc/",
            local_dir,
            "--region", REGION,
        ]

    def test_returns_false_on_error(self, monkeypatch, tmp_path):
        """Returns False when aws s3 sync fails."""
        def fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(
                args=cmd, returncode=1, stdout="", stderr="sync failed"
            )

        monkeypatch.setattr(subprocess, "run", fake_run)

        client = S3Client(BUCKET, region=REGION)
        result = client.sync_to_local("runs/wave-abc/", str(tmp_path))
        assert result is False


class TestSyncTask:
    """S3Client.sync_task() discovers the hash suffix and syncs one task."""

    def test_discovers_hash_and_syncs(self, monkeypatch, tmp_path):
        """Lists objects to find task__hash dir, syncs to local cache."""
        calls = []

        def fake_run(cmd, **kwargs):
            calls.append(cmd)
            if cmd[:3] == ["aws", "s3", "ls"]:
                # Return ls output with one task hash
                return subprocess.CompletedProcess(
                    args=cmd, returncode=0,
                    stdout=(
                        "                           PRE mytask__aBcD123/\n"
                    ),
                    stderr="",
                )
            # sync
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        monkeypatch.setattr(subprocess, "run", fake_run)
        client = S3Client(BUCKET, region=REGION)
        result = client.sync_task(
            wave="wave-abc-20260405",
            rep=1,
            task_name="mytask",
            cache_base=tmp_path,
        )

        assert result is not None
        assert result.name == "mytask__aBcD123"
        assert result.parent.name == "wave-abc-20260405_rep1"
        # sync command syncs from the matching S3 prefix
        sync_cmd = calls[-1]
        assert sync_cmd[0:3] == ["aws", "s3", "sync"]
        assert "wave-abc-20260405_rep1/mytask__aBcD123" in sync_cmd[3]

    def test_returns_none_when_task_not_found(self, monkeypatch, tmp_path):
        """Returns None if no task__hash dir matches."""
        def fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(
                args=cmd, returncode=0, stdout="", stderr=""
            )

        monkeypatch.setattr(subprocess, "run", fake_run)
        client = S3Client(BUCKET, region=REGION)
        result = client.sync_task(
            wave="wave-abc", rep=1, task_name="nope",
            cache_base=tmp_path,
        )
        assert result is None

    def test_returns_none_on_sync_failure(self, monkeypatch, tmp_path):
        """Returns None if aws s3 sync fails even after finding the hash."""
        def fake_run(cmd, **kwargs):
            if cmd[:3] == ["aws", "s3", "ls"]:
                return subprocess.CompletedProcess(
                    args=cmd, returncode=0,
                    stdout="                           PRE mytask__H1/\n",
                    stderr="",
                )
            return subprocess.CompletedProcess(
                args=cmd, returncode=1, stdout="", stderr="sync failed"
            )

        monkeypatch.setattr(subprocess, "run", fake_run)
        client = S3Client(BUCKET, region=REGION)
        result = client.sync_task(
            wave="wave-abc", rep=1, task_name="mytask",
            cache_base=tmp_path,
        )
        assert result is None


class TestInit:
    """S3Client.__init__ stores bucket and region."""

    def test_default_region(self):
        client = S3Client("my-bucket")
        assert client.bucket == "my-bucket"
        assert client.region == "us-west-1"

    def test_custom_region(self):
        client = S3Client("my-bucket", region="eu-west-1")
        assert client.bucket == "my-bucket"
        assert client.region == "eu-west-1"
