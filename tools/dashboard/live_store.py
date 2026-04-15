"""Live run monitoring from harbor-runner state files + AWS EC2 API.

Reads shell-style .env state files written by harbor-runner when launching
eval runs, and queries the EC2 API for current instance state.
"""

import json
import subprocess
from pathlib import Path


class LiveStore:
    """Reads harbor-runner state files and queries AWS EC2 for live state."""

    # Keys that belong to the run-level metadata (not instance rows).
    METADATA_KEYS = {
        "RUN_ID", "MODEL", "BENCHMARK", "NUM_TASKS", "REPS",
        "INSTANCE_TYPE", "AGENT_IMPORT", "LAUNCHED_AT",
    }

    def __init__(self, state_dir: str, region: str = "us-west-1"):
        self.state_dir = Path(state_dir)
        self.region = region

    def list_runs(self) -> list[dict]:
        """Parse all .env files in state_dir, return list of run dicts.

        Each run dict has keys: run_id, model, benchmark, num_tasks, reps,
        instance_type, agent_import, launched_at, instances (list of
        {instance_id, rep, task} dicts).

        Sorted by launched_at descending (most recent first). Returns
        empty list if state_dir is missing.
        """
        if not self.state_dir.is_dir():
            return []

        runs = []
        for f in sorted(self.state_dir.glob("*.env")):
            try:
                text = f.read_text()
            except OSError:
                continue
            runs.extend(self._parse_env_file(text))

        runs.sort(key=lambda r: r.get("launched_at", ""), reverse=True)
        return runs

    def get_run(self, run_id: str) -> dict | None:
        """Return run dict for the given run_id, or None if not found."""
        for run in self.list_runs():
            if run.get("run_id") == run_id:
                return run
        return None

    def query_instance_states(self, instance_ids: list[str]) -> dict[str, dict]:
        """Call aws ec2 describe-instances for the given IDs.

        Returns {instance_id: {"state": ..., "launch_time": ..., "public_ip": ...}}.
        Returns an empty dict on any subprocess or JSON error, or if the
        input list is empty.
        """
        if not instance_ids:
            return {}

        cmd = [
            "aws", "ec2", "describe-instances",
            "--instance-ids", *instance_ids,
            "--region", self.region,
            "--query",
            "Reservations[*].Instances[*].{Id:InstanceId,State:State.Name,"
            "LaunchTime:LaunchTime,PublicIP:PublicIpAddress}",
            "--output", "json",
        ]

        try:
            result = subprocess.run(
                cmd, capture_output=True, text=True, check=True,
            )
        except (subprocess.CalledProcessError, FileNotFoundError, OSError):
            return {}

        try:
            data = json.loads(result.stdout)
        except (json.JSONDecodeError, ValueError):
            return {}

        out: dict[str, dict] = {}
        # data is list-of-lists: Reservations[*].Instances[*]
        for reservation in data:
            for inst in reservation:
                iid = inst.get("Id")
                if not iid:
                    continue
                out[iid] = {
                    "state": inst.get("State"),
                    "launch_time": inst.get("LaunchTime"),
                    "public_ip": inst.get("PublicIP"),
                }
        return out

    def run_with_live_state(self, run_id: str) -> dict | None:
        """Return run dict with AWS state merged into each instance.

        Each instance dict gets three added fields: aws_state, launch_time,
        public_ip. Instances whose IDs are not in the AWS response get
        aws_state="unknown" (and None for the other two fields).

        Returns None if the run is not found.
        """
        run = self.get_run(run_id)
        if run is None:
            return None

        instances = run.get("instances", [])
        instance_ids = [i["instance_id"] for i in instances]
        states = self.query_instance_states(instance_ids)

        enriched = []
        for inst in instances:
            iid = inst["instance_id"]
            aws_info = states.get(iid, {})
            enriched.append({
                **inst,
                "aws_state": aws_info.get("state", "unknown"),
                "launch_time": aws_info.get("launch_time"),
                "public_ip": aws_info.get("public_ip"),
            })

        return {**run, "instances": enriched}

    def sync_results(
        self,
        run_id: str,
        cache_dir: str,
        bucket: str = "harbor-eval-results-526275945504",
    ) -> bool:
        """Bulk-download all result.json files for a run from S3.

        Uses `aws s3 sync` with an include filter to fetch every
        result.json under runs/{run_id}/ in parallel. Returns True on
        success, False on subprocess or CLI error.
        """
        dest = Path(cache_dir) / run_id
        dest.mkdir(parents=True, exist_ok=True)
        cmd = [
            "aws", "s3", "sync",
            f"s3://{bucket}/runs/{run_id}/",
            f"{dest}/",
            "--exclude", "*",
            "--include", "*/result.json",
            "--region", self.region,
        ]
        try:
            result = subprocess.run(cmd, capture_output=True, text=True)
        except (FileNotFoundError, OSError):
            return False
        return result.returncode == 0

    def read_results(self, run_id: str, cache_dir: str) -> dict[str, dict]:
        """Read all synced result.json files for a run.

        Walks {cache_dir}/{run_id}/ for result.json files. For each file,
        extracts rep from the rep-N path segment, task from the parent
        directory name (stripping the __hash suffix), and reward from
        verifier_result.rewards.reward (falling back to score, then None).

        Returns {task_name: {rep_int: reward}}.
        """
        root = Path(cache_dir) / run_id
        results: dict[str, dict] = {}
        if not root.is_dir():
            return results

        for result_file in root.rglob("result.json"):
            rep = None
            for part in result_file.parts:
                if part.startswith("rep-"):
                    try:
                        rep = int(part[len("rep-"):])
                    except ValueError:
                        rep = None
                    break
            if rep is None:
                continue

            task_dir = result_file.parent.name
            if "__" in task_dir:
                task = task_dir.rsplit("__", 1)[0]
            else:
                task = task_dir

            try:
                data = json.loads(result_file.read_text())
            except (OSError, json.JSONDecodeError, ValueError):
                continue

            reward = None
            verifier = data.get("verifier_result") or {}
            rewards = verifier.get("rewards") if isinstance(verifier, dict) else None
            if isinstance(rewards, dict) and "reward" in rewards:
                reward = rewards.get("reward")
            elif "score" in data:
                reward = data.get("score")

            results.setdefault(task, {})[rep] = reward

        return results

    def enrich_with_results(self, run_state: dict, cache_dir: str) -> dict:
        """Attach S3-fetched rewards to each instance in a run state.

        Calls sync_results + read_results, then adds a `reward` field to
        each instance dict (matched by task + rep). Instances without a
        result get reward=None. Also adds a top-level `scores` dict
        ({task: {rep: reward}}) to the returned state.
        """
        run_id = run_state.get("run_id", "")
        self.sync_results(run_id, cache_dir)
        scores = self.read_results(run_id, cache_dir)

        enriched_instances = []
        for inst in run_state.get("instances", []):
            task = inst.get("task", "")
            try:
                rep = int(inst.get("rep", ""))
            except (TypeError, ValueError):
                rep = None
            reward = None
            if rep is not None:
                reward = scores.get(task, {}).get(rep)
            enriched_instances.append({**inst, "reward": reward})

        return {**run_state, "instances": enriched_instances, "scores": scores}

    def _parse_env_file(self, text: str) -> list[dict]:
        """Parse shell-style env file text into one or more run dicts.

        A new run starts at each RUN_ID= line. Lines matching
        'INSTANCE=<id> REP=<n> TASK=<name>' add an instance to the current
        run. All other KEY=VALUE lines set metadata on the current run.
        Lines like 'INSTANCE_IDS=(...)' are ignored (the INSTANCE= lines
        are authoritative).
        """
        runs: list[dict] = []
        current: dict | None = None

        for line in text.splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue

            # INSTANCE=<id> REP=<n> TASK=<name> — instance mapping row.
            if line.startswith("INSTANCE=") and " REP=" in line:
                if current is None:
                    # Instance row without a run header — skip.
                    continue
                parts = self._parse_instance_row(line)
                if parts is not None:
                    current["instances"].append(parts)
                continue

            # Skip the launch-time array — INSTANCE_IDS=(i-xxx i-yyy).
            if line.startswith("INSTANCE_IDS="):
                continue

            # KEY=VALUE metadata.
            if "=" in line:
                key, _, value = line.partition("=")
                if key == "RUN_ID":
                    # Start a new run.
                    if current is not None:
                        runs.append(current)
                    current = self._new_run(value)
                    continue
                if current is None:
                    continue
                if key in self.METADATA_KEYS:
                    current[self._metadata_field(key)] = value

        if current is not None:
            runs.append(current)
        return runs

    @staticmethod
    def _new_run(run_id: str) -> dict:
        return {
            "run_id": run_id,
            "model": "",
            "benchmark": "",
            "num_tasks": "",
            "reps": "",
            "instance_type": "",
            "agent_import": "",
            "launched_at": "",
            "instances": [],
        }

    @staticmethod
    def _metadata_field(key: str) -> str:
        """Map env key to our run dict field name."""
        mapping = {
            "RUN_ID": "run_id",
            "MODEL": "model",
            "BENCHMARK": "benchmark",
            "NUM_TASKS": "num_tasks",
            "REPS": "reps",
            "INSTANCE_TYPE": "instance_type",
            "AGENT_IMPORT": "agent_import",
            "LAUNCHED_AT": "launched_at",
        }
        return mapping[key]

    @staticmethod
    def _parse_instance_row(line: str) -> dict | None:
        """Parse 'INSTANCE=<id> REP=<n> TASK=<name>' into a dict."""
        instance_id = ""
        rep = ""
        task = ""
        for field in line.split():
            if "=" not in field:
                continue
            key, _, value = field.partition("=")
            if key == "INSTANCE":
                instance_id = value
            elif key == "REP":
                rep = value
            elif key == "TASK":
                task = value
        if not instance_id:
            return None
        return {"instance_id": instance_id, "rep": rep, "task": task}
