"""Data layer -- discover runs and read task results from disk."""

import json
import re
from pathlib import Path


FINAL_COMMUNICATE_PATTERN = re.compile(r"\[(?:submit_result|communicate(?:\:final)?)\]")


class RunStore:
    """Read-only access to harbor job data on disk.

    Expected layout under data_dir:
        job_name/task-name__hash/verifier/reward.txt
        job_name/task-name__hash/agent/serf-state/sessions/*.jsonl
        job_name/task-name__hash/agent/command-0/stdout.txt
        job_name/task-name__hash/result.json
    """

    def __init__(self, data_dir):
        self.data_dir = Path(data_dir)

    # ------------------------------------------------------------------
    # Run discovery
    # ------------------------------------------------------------------

    def list_runs(self):
        """Return a list of run summary dicts, one per discovered job."""
        runs = []
        if not self.data_dir.is_dir():
            return runs
        for job_dir in sorted(self.data_dir.iterdir()):
            if not job_dir.is_dir():
                continue
            task_dirs = self._find_task_dirs(job_dir)
            if not task_dirs:
                continue
            grouped = self._group_task_dirs(task_dirs)
            tasks = [self._read_task_summary(td, tc, ad) for td, tc, ad in grouped]
            completed = [t for t in tasks if t["status"] in ("pass", "fail")]
            passed = sum(1 for t in completed if t["passed"])
            run = {
                "job_name": job_dir.name,
                "total_tasks": len(completed),
                "passed": passed,
                "running": sum(1 for t in tasks if t["status"] == "running"),
                "queued": sum(1 for t in tasks if t["status"] == "queued"),
            }
            run.update(self._read_run_metadata(job_dir))
            runs.append(run)
        return runs

    def get_run(self, job_name):
        """Return summary dict for a single job, or None."""
        job_dir = self.data_dir / job_name
        if not job_dir.is_dir():
            return None
        task_dirs = self._find_task_dirs(job_dir)
        if not task_dirs:
            return None
        grouped = self._group_task_dirs(task_dirs)
        tasks = [self._read_task_summary(td, tc, ad) for td, tc, ad in grouped]
        completed = [t for t in tasks if t["status"] in ("pass", "fail")]
        passed = sum(1 for t in completed if t["passed"])
        run = {
            "job_name": job_name,
            "total_tasks": len(completed),
            "passed": passed,
            "running": sum(1 for t in tasks if t["status"] == "running"),
            "queued": sum(1 for t in tasks if t["status"] == "queued"),
        }
        run.update(self._read_run_metadata(job_dir))
        return run

    # ------------------------------------------------------------------
    # Task listing
    # ------------------------------------------------------------------

    def list_tasks(self, job_name):
        """Return list of task summary dicts for a job, or None if missing."""
        job_dir = self.data_dir / job_name
        if not job_dir.is_dir():
            return None
        task_dirs = self._find_task_dirs(job_dir)
        grouped = self._group_task_dirs(task_dirs)
        return [self._read_task_summary(td, tc, ad) for td, tc, ad in grouped]

    def get_task(self, job_name, task_name, trial_hash=None):
        """Return detailed task dict, or None if not found.

        If trial_hash is given and the task has multiple trials, return
        detail for the specific trial matching that hash suffix.
        """
        job_dir = self.data_dir / job_name
        if not job_dir.is_dir():
            return None
        task_dirs = self._find_task_dirs(job_dir)
        grouped = self._group_task_dirs(task_dirs)
        for td, tc, all_dirs in grouped:
            if self._task_name_from_dir(td) == task_name:
                # Pick specific trial if requested
                target_dir = td
                if trial_hash and tc > 1:
                    for d in all_dirs:
                        if d.name.endswith(f"__{trial_hash}"):
                            target_dir = d
                            break
                detail = self._read_task_detail(target_dir)
                detail["trial_count"] = tc
                detail["active_trial"] = target_dir.name.split("__")[-1] if "__" in target_dir.name else ""
                # Add per-trial breakdown for multi-rep tasks
                if tc > 1:
                    trials = []
                    pass_count = 0
                    for d in all_dirs:
                        r = self._read_reward(d)
                        passed = r is not None and r >= 1.0
                        if passed:
                            pass_count += 1
                        trials.append({
                            "hash": d.name.split("__")[-1] if "__" in d.name else "",
                            "reward": r,
                            "passed": passed,
                            "status": self._task_status(r, d),
                        })
                    detail["pass_count"] = pass_count
                    detail["trials"] = trials
                return detail
        return None

    # ------------------------------------------------------------------
    # Transcript loading
    # ------------------------------------------------------------------

    def load_transcripts(self, transcript_files):
        """Parse JSONL transcript files into session dicts.

        Each session dict has header fields (session_id, parent_session_id,
        model, depth, etc.) plus an 'entries' list of entry dicts.
        """
        sessions = []
        for path in transcript_files:
            path = Path(path)
            if not path.is_file():
                continue
            session = self._parse_transcript(path)
            if session is not None:
                sessions.append(session)
        return sessions

    def build_session_tree(self, sessions):
        """Organize flat session list into a tree using parent_session_id.

        Returns a list of root session dicts, each with a 'children' list.
        """
        by_id = {}
        for s in sessions:
            node = dict(s)
            node["children"] = []
            by_id[s["session_id"]] = node

        roots = []
        for node in by_id.values():
            parent_id = node.get("parent_session_id")
            if parent_id and parent_id in by_id:
                by_id[parent_id]["children"].append(node)
            else:
                roots.append(node)
        return roots

    # ------------------------------------------------------------------
    # Artifacts
    # ------------------------------------------------------------------

    def list_artifacts(self, task_dir):
        """List artifact files with relative paths and sizes."""
        artifacts_dir = Path(task_dir) / "agent" / "artifacts"
        if not artifacts_dir.is_dir():
            return []
        files = []
        for f in sorted(artifacts_dir.rglob("*")):
            if f.is_file():
                files.append({
                    "path": str(f.relative_to(artifacts_dir)),
                    "size": f.stat().st_size,
                })
        return files

    # ------------------------------------------------------------------
    # File listing
    # ------------------------------------------------------------------

    def list_all_files(self, task_dir):
        """List all files in task directory with relative paths and sizes."""
        task_path = Path(task_dir)
        if not task_path.is_dir():
            return []
        files = []
        for f in sorted(task_path.rglob("*")):
            if f.is_file():
                files.append({
                    "path": str(f.relative_to(task_path)),
                    "size": f.stat().st_size,
                })
        return files

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _read_run_metadata(self, job_dir):
        """Read run-level metadata from manifest.json, config.json, result.json."""
        meta = {
            "model": "", "git_sha": "", "git_branch": "", "started_at": "",
            "finished_at": "", "reps": 1, "adapter": "", "dataset_name": "",
            "dataset_version": "",
        }

        # manifest.json
        manifest_file = job_dir / "manifest.json"
        if manifest_file.is_file():
            try:
                manifest = json.loads(manifest_file.read_text())
                for key in ("model", "git_sha", "git_branch", "adapter"):
                    if manifest.get(key):
                        meta[key] = manifest[key]
                if manifest.get("reps"):
                    meta["reps"] = manifest["reps"]
                if manifest.get("started_at"):
                    meta["started_at"] = manifest["started_at"]
            except (json.JSONDecodeError, OSError):
                pass

        # config.json
        config_file = job_dir / "config.json"
        if config_file.is_file():
            try:
                config = json.loads(config_file.read_text())
                datasets = config.get("datasets", [])
                if datasets:
                    meta["dataset_name"] = datasets[0].get("name", "")
                    meta["dataset_version"] = datasets[0].get("version", "")
            except (json.JSONDecodeError, OSError):
                pass

        # result.json (run-level)
        result_file = job_dir / "result.json"
        if result_file.is_file():
            try:
                result = json.loads(result_file.read_text())
                if not meta["started_at"] and result.get("started_at"):
                    meta["started_at"] = result["started_at"]
                if result.get("finished_at"):
                    meta["finished_at"] = result["finished_at"]
            except (json.JSONDecodeError, OSError):
                pass

        return meta

    def _find_task_dirs(self, inner_dir):
        """Return sorted list of task__hash directories."""
        dirs = []
        for d in inner_dir.iterdir():
            if d.is_dir() and "__" in d.name:
                dirs.append(d)
        return sorted(dirs, key=lambda d: d.name)

    def _group_task_dirs(self, task_dirs):
        """Deduplicate task dirs by name, picking the best trial.

        Returns list of (best_dir, trial_count, all_dirs) tuples sorted by
        task name.  For groups with multiple dirs, picks highest reward
        (ties broken by latest mtime).
        """
        from collections import defaultdict
        groups = defaultdict(list)
        for td in task_dirs:
            name = self._task_name_from_dir(td)
            groups[name].append(td)

        result = []
        for name in sorted(groups):
            dirs = groups[name]
            if len(dirs) == 1:
                result.append((dirs[0], 1, dirs))
            else:
                # Pick best: highest reward, then latest mtime
                def sort_key(d):
                    reward = self._read_reward(d)
                    r = reward if reward is not None else -1.0
                    try:
                        mtime = d.stat().st_mtime
                    except OSError:
                        mtime = 0
                    return (r, mtime)
                best = max(dirs, key=sort_key)
                result.append((best, len(dirs), dirs))
        return result

    def _task_name_from_dir(self, task_dir):
        """Extract task name from 'task-name__hash' directory name."""
        name = task_dir.name
        idx = name.rfind("__")
        if idx > 0:
            return name[:idx]
        return name

    def _read_reward(self, task_dir):
        """Read reward from verifier/reward.txt, or None."""
        reward_file = task_dir / "verifier" / "reward.txt"
        if not reward_file.is_file():
            return None
        try:
            return float(reward_file.read_text().strip())
        except (ValueError, OSError):
            return None

    def _read_agent_stdout(self, task_dir):
        """Read agent stdout, or empty string."""
        stdout_file = task_dir / "agent" / "command-0" / "stdout.txt"
        if not stdout_file.is_file():
            return ""
        try:
            return stdout_file.read_text(errors="replace")
        except OSError:
            return ""

    def _read_result_json(self, task_dir):
        """Read and parse result.json, or empty dict."""
        result_file = task_dir / "result.json"
        if not result_file.is_file():
            return {}
        try:
            return json.loads(result_file.read_text())
        except (json.JSONDecodeError, OSError):
            return {}

    def _read_test_output(self, task_dir):
        """Read verifier test output, or empty string."""
        test_file = task_dir / "verifier" / "test-stdout.txt"
        if not test_file.is_file():
            return ""
        try:
            return test_file.read_text(errors="replace")
        except OSError:
            return ""

    def _find_transcript_files(self, task_dir):
        """Find all transcript JSONL files for a task.

        Checks agent/agent-state/sessions/ (current harbor layout) first,
        then agent/serf-state/sessions/ (legacy) for backwards compat.
        """
        for state_name in ("agent-state", "serf-state"):
            sessions_dir = task_dir / "agent" / state_name / "sessions"
            if sessions_dir.is_dir():
                return sorted(
                    str(f) for f in sessions_dir.iterdir()
                    if f.is_file() and f.suffix == ".jsonl"
                )
        return []

    def _classify_failure(self, reward, task_dir):
        """Classify failure category for a failing task."""
        if reward is None or reward >= 1.0:
            return None

        result = self._read_result_json(task_dir)
        exc_info = result.get("exception_info", {})
        if exc_info and exc_info.get("exception_type") == "AgentTimeoutError":
            return "timeout"

        stdout = self._read_agent_stdout(task_dir)
        if FINAL_COMMUNICATE_PATTERN.search(stdout):
            return "wrong_answer"
        if "[error]" in stdout:
            return "api_error"

        return "no_submit"

    def _task_status(self, reward, task_dir):
        """Determine task status from disk artifacts.

        Harbor pre-writes result.json with started_at/finished_at at job
        launch, so those timestamps are unreliable for status. Instead:
            'pass'    — reward.txt exists and reward >= 1.0
            'fail'    — reward.txt exists and reward < 1.0
            'running' — agent has written transcripts or stdout (no reward yet)
            'queued'  — task dir exists but no agent output
        """
        if reward is not None:
            return "pass" if reward >= 1.0 else "fail"
        # Check for agent output as evidence the task actually started
        if self._find_transcript_files(task_dir):
            return "running"
        stdout_file = task_dir / "agent" / "command-0" / "stdout.txt"
        if stdout_file.is_file() and stdout_file.stat().st_size > 0:
            return "running"
        return "queued"

    def _read_task_summary(self, task_dir, trial_count=1, all_dirs=None):
        """Build a summary dict for a task directory."""
        reward = self._read_reward(task_dir)
        transcript_files = self._find_transcript_files(task_dir)
        result = self._read_result_json(task_dir)
        status = self._task_status(reward, task_dir)
        summary = {
            "task_name": self._task_name_from_dir(task_dir),
            "reward": reward,
            "passed": reward is not None and reward >= 1.0,
            "status": status,
            "failure_category": self._classify_failure(reward, task_dir),
            "session_count": len(transcript_files),
            "trial_count": trial_count,
            "started_at": result.get("started_at", ""),
            "finished_at": result.get("finished_at", ""),
        }

        # Per-trial breakdown for multi-rep tasks
        if all_dirs and trial_count > 1:
            trials = []
            pass_count = 0
            for td in all_dirs:
                r = self._read_reward(td)
                passed = r is not None and r >= 1.0
                if passed:
                    pass_count += 1
                trials.append({
                    "hash": td.name.split("__")[-1] if "__" in td.name else "",
                    "reward": r,
                    "passed": passed,
                    "status": self._task_status(r, td),
                })
            summary["pass_count"] = pass_count
            summary["trials"] = trials

        return summary

    def _read_task_instruction(self, task_dir):
        """Extract task instruction from command.txt."""
        cmd_file = task_dir / "agent" / "command-0" / "command.txt"
        if not cmd_file.is_file():
            return ""
        try:
            cmd = cmd_file.read_text(errors="replace").strip()
            # Instruction is the last argument after "-- '"
            marker = "-- '"
            idx = cmd.find(marker)
            if idx >= 0:
                instruction = cmd[idx + len(marker):]
                if instruction.endswith("'"):
                    instruction = instruction[:-1]
                return instruction
            return cmd
        except OSError:
            return ""

    def _read_task_detail(self, task_dir):
        """Build a detailed dict for a task directory."""
        summary = self._read_task_summary(task_dir)
        result = self._read_result_json(task_dir)
        model = result.get("config", {}).get("model", "")
        transcript_files = self._find_transcript_files(task_dir)

        # If model not in result.json, try to get from first transcript header
        if not model and transcript_files:
            sessions = self.load_transcripts(transcript_files[:1])
            if sessions:
                model = sessions[0].get("model", "")

        summary.update({
            "task_dir": str(task_dir),
            "test_output": self._read_test_output(task_dir),
            "model": model,
            "transcript_files": transcript_files,
            "agent_stdout": self._read_agent_stdout(task_dir),
            "instruction": self._read_task_instruction(task_dir),
        })
        return summary

    def _parse_transcript(self, path):
        """Parse a single JSONL transcript file into a session dict."""
        try:
            lines = Path(path).read_text().splitlines()
        except OSError:
            return None

        if not lines:
            return None

        # First line is the header
        try:
            header = json.loads(lines[0])
        except json.JSONDecodeError:
            return None

        if header.get("kind") != "header":
            return None

        # Parse entries
        entries = []
        for line in lines[1:]:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
                if entry.get("kind") == "entry":
                    entries.append(entry)
            except json.JSONDecodeError:
                continue

        return {
            "session_id": header.get("session_id", ""),
            "parent_session_id": header.get("parent_session_id", ""),
            "parent_tool_call_id": header.get("parent_tool_call_id", ""),
            "model": header.get("model", ""),
            "depth": header.get("depth", 0),
            "profile_id": header.get("profile_id", ""),
            "created_at": header.get("created_at", ""),
            "system_prompt": header.get("system_prompt", ""),
            "entries": entries,
        }
