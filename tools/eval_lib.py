"""Shared helpers for benchmark infrastructure tools.

Constants, git helpers, archive readers, and command builders used by
run_eval.py, generate_summary.py, compare_runs.py, and generate_report.py.
"""

import datetime
import os
import subprocess

# --- Remote server config ---

REMOTE = "jesse@192.168.118.101"
REMOTE_DIR = "git/terminal-bench"
DATASET = "terminal-bench@2.0"

# --- Defaults ---

DEFAULT_MODEL = "openai/gpt-5.2-codex"
DEFAULT_ADAPTER = "serf_agent:SerfAgent"
DEFAULT_REPS = 3
DEFAULT_CONCURRENCY = 4
DEFAULT_ARCHIVE_ROOT = "/data/serf-evals"


# --- Git helpers ---

def git_info(repo_root: str) -> dict:
    """Return git SHA, dirty status, and branch name.

    Returns: {"sha": str, "dirty": bool, "branch": str}
    Raises subprocess.CalledProcessError if repo_root is not a git repo.
    """
    def _git(*args):
        return subprocess.check_output(
            ["git", "-C", repo_root] + list(args),
            stderr=subprocess.DEVNULL,
        ).decode().strip()

    sha = _git("rev-parse", "--short", "HEAD")
    branch = _git("branch", "--show-current")
    try:
        _git("diff", "--quiet")
        _git("diff", "--cached", "--quiet")
        dirty = False
    except subprocess.CalledProcessError:
        dirty = True

    return {"sha": sha, "dirty": dirty, "branch": branch}


def build_ldflags(repo_root: str) -> str:
    """Build Go ldflags string for buildinfo stamping."""
    info = git_info(repo_root)
    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    dirty_val = "true" if info["dirty"] else ""
    return (
        f"-X primeradiant.com/serf/buildinfo.GitSHA={info['sha']} "
        f"-X primeradiant.com/serf/buildinfo.GitDirty={dirty_val} "
        f"-X primeradiant.com/serf/buildinfo.BuildTime={now}"
    )


# --- Run ID ---

def make_run_id(job_name: str, git_sha: str) -> str:
    """Generate a run ID like 2026-02-28T200000Z_baseline-v1_abc1234."""
    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H%M%SZ")
    return f"{now}_{job_name}_{git_sha}"


# --- Harbor command ---

def build_harbor_command(
    adapter: str,
    model: str,
    reps: int,
    concurrency: int,
    job_name: str,
    task_name: str = "",
    ak_args: list[str] | None = None,
) -> str:
    """Build a harbor run command string."""
    parts = [
        "harbor run",
        f"--agent-import-path {adapter}",
        f"--dataset {DATASET}",
    ]
    if task_name:
        parts.append(f"--task-name {task_name}")
    parts.extend([
        f"--model {model}",
        f"-k {reps}",
        f"-n {concurrency}",
        f"--job-name {job_name}",
        f"--jobs-dir /tmp/{job_name}",
    ])
    for ak in (ak_args or []):
        parts.append(f"--ak {ak}")
    return " ".join(parts)


# --- Manifest ---

def build_manifest(
    run_id: str,
    job_name: str,
    git_sha: str,
    git_dirty: bool,
    git_branch: str,
    model: str,
    adapter: str,
    reps: int,
    concurrency: int,
    task_name: str = "",
    ak_args: list[str] | None = None,
) -> dict:
    """Build a manifest dict for a benchmark run."""
    now = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    return {
        "run_id": run_id,
        "job_name": job_name,
        "git_sha": git_sha,
        "git_dirty": git_dirty,
        "git_branch": git_branch,
        "model": model,
        "adapter": adapter,
        "task_name": task_name or "all",
        "reps": reps,
        "concurrency": concurrency,
        "started_at": now,
        "ak_args": ak_args or [],
    }


# --- Archive reading ---

def read_archive_tasks(run_dir: str) -> dict:
    """Read tasks and reps from a collected archive directory.

    Returns: OrderedDict-like dict (sorted by task name) mapping
    task_name -> list of rep dicts, each with:
        {"rep": int, "reward": float, "failure_category": str|None}
    """
    tasks_dir = os.path.join(run_dir, "tasks")
    if not os.path.isdir(tasks_dir):
        raise FileNotFoundError(f"No tasks/ directory in {run_dir}")

    result = {}
    for task_name in sorted(os.listdir(tasks_dir)):
        task_path = os.path.join(tasks_dir, task_name)
        if not os.path.isdir(task_path):
            continue

        reps = []
        for rep_name in sorted(os.listdir(task_path)):
            rep_path = os.path.join(task_path, rep_name)
            reward_file = os.path.join(rep_path, "reward.txt")
            if not os.path.isfile(reward_file):
                continue

            with open(reward_file) as f:
                reward = float(f.read().strip())

            rep_num = int(rep_name.replace("rep-", ""))

            fc_file = os.path.join(rep_path, "failure_category.txt")
            failure_cat = None
            if os.path.isfile(fc_file):
                fc = open(fc_file).read().strip()
                if fc:
                    failure_cat = fc

            reps.append({
                "rep": rep_num,
                "reward": reward,
                "failure_category": failure_cat,
            })

        if reps:
            result[task_name] = reps

    return result


# --- SSH helper ---

def ssh_run(command: str, remote: str = REMOTE, check: bool = True) -> subprocess.CompletedProcess:
    """Run a command on the remote server via SSH."""
    return subprocess.run(
        ["ssh", remote, "bash", "-c", command],
        capture_output=True, text=True, check=check,
    )
