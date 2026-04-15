"""Thin wrapper around the AWS CLI for S3 operations.

Shells out to `aws s3` commands -- no boto3 dependency required.
Used by the dashboard to fetch experiment results from S3 on-demand.
"""

import json
import subprocess
from pathlib import Path


class S3Client:
    """S3 operations via the AWS CLI."""

    def __init__(self, bucket: str, region: str = "us-west-1"):
        self.bucket = bucket
        self.region = region

    def list_objects(self, prefix: str) -> list[str]:
        """List object keys under prefix.

        Runs `aws s3 ls --recursive` and parses output lines.
        Each line has format: "date time size key"
        Returns list of key strings, or [] on error.
        """
        r = subprocess.run(
            ["aws", "s3", "ls", f"s3://{self.bucket}/{prefix}",
             "--region", self.region, "--recursive"],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            return []
        return [line.split()[-1] for line in r.stdout.strip().split("\n") if line]

    def get_json(self, key: str) -> dict | None:
        """Fetch a JSON object from S3 and parse it.

        Runs `aws s3 cp <key> -` to stream to stdout.
        Returns parsed dict, or None on error or invalid JSON.
        """
        text = self.get_text(key)
        if text is None:
            return None
        try:
            return json.loads(text)
        except (json.JSONDecodeError, ValueError):
            return None

    def get_text(self, key: str) -> str | None:
        """Fetch a text file from S3.

        Returns the file contents as a string, or None on error.
        """
        r = subprocess.run(
            ["aws", "s3", "cp", f"s3://{self.bucket}/{key}", "-",
             "--region", self.region],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            return None
        return r.stdout

    def sync_to_local(self, prefix: str, local_dir: str) -> bool:
        """Sync an S3 prefix to a local directory.

        Runs `aws s3 sync`. Returns True on success, False on error.
        """
        r = subprocess.run(
            ["aws", "s3", "sync", f"s3://{self.bucket}/{prefix}",
             local_dir, "--region", self.region],
            capture_output=True, text=True,
        )
        return r.returncode == 0

    def sync_run(self, wave_id: str, local_dir: str) -> bool:
        """Sync an entire run's data from S3 to local directory.

        Syncs runs/{wave_id}/ to {local_dir}/{wave_id}/.
        Returns True on success, False on error.
        """
        s3_prefix = f"runs/{wave_id}/"
        local_path = Path(local_dir) / wave_id
        local_path.mkdir(parents=True, exist_ok=True)
        return self.sync_to_local(s3_prefix, str(local_path))

    def sync_task(
        self, wave: str, rep: int, task_name: str, cache_base: Path
    ) -> Path | None:
        """Sync a single task's files from S3 to a local cache.

        Harbor S3 layout:
            runs/{wave}/rep-{rep}/{wave}_rep{rep}/{task_name}__{hash}/

        Discovers the hash by listing task_name__* dirs, then syncs the
        matching prefix to:
            {cache_base}/{wave}_rep{rep}/{task_name}__{hash}/

        Returns the local task dir Path on success, or None if the task
        doesn't exist on S3 or the sync fails.
        """
        job_name = f"{wave}_rep{rep}"
        s3_run_prefix = f"runs/{wave}/rep-{rep}/{job_name}/"

        # List task__* dirs to discover the hash suffix
        r = subprocess.run(
            ["aws", "s3", "ls",
             f"s3://{self.bucket}/{s3_run_prefix}{task_name}__",
             "--region", self.region],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            return None

        task_dir_name = None
        for line in r.stdout.splitlines():
            # Format: "                           PRE mytask__hash/"
            parts = line.split()
            if parts and parts[0] == "PRE":
                name = parts[1].rstrip("/")
                if name.startswith(f"{task_name}__"):
                    task_dir_name = name
                    break
        if not task_dir_name:
            return None

        local_task_dir = Path(cache_base) / job_name / task_dir_name
        local_task_dir.mkdir(parents=True, exist_ok=True)
        s3_task_prefix = f"{s3_run_prefix}{task_dir_name}/"

        r = subprocess.run(
            ["aws", "s3", "sync",
             f"s3://{self.bucket}/{s3_task_prefix}",
             str(local_task_dir),
             "--region", self.region],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            return None
        return local_task_dir
