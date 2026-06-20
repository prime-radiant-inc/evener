"""Core library for eval results collection and scoreboard.

Handles reward extraction, run metadata, per-task scorecards, and the
master scoreboard. Used by collect_results.py and scoreboard.py.
"""

import json
from pathlib import Path

from task_sets import TASK_SETS

# Complete 89-task list: solvable (72) + excluded too-hard (17).
_EXCLUDED = [
    "make-doom-for-mips",
    "sam-cell-seg",
    "install-windows-3.11",
    "caffe-cifar-10",
    "filter-js-from-html",
    "gpt2-codegolf",
    "extract-moves-from-video",
    "raman-fitting",
    "train-fasttext",
    "mteb-retrieve",
    "video-processing",
    "torch-tensor-parallelism",
    "dna-assembly",
    "db-wal-recovery",
    "torch-pipeline-parallelism",
    "dna-insert",
    "mteb-leaderboard",
]
ALL_TASKS = sorted(set(TASK_SETS["solvable"] + _EXCLUDED))

S3_BUCKET = "harbor-eval-results-526275945504"
S3_PREFIX = f"s3://{S3_BUCKET}/runs"

# Type aliases for clarity
RunResult = dict  # {run_id, date, git_sha, model, variant, results, s3_prefix}
TaskResult = dict  # {task, current_score, current_run, current_date, status, notes, history}


def compute_score(reps: list[float]) -> float:
    """Mean of rep rewards. Raises ValueError if empty."""
    if not reps:
        raise ValueError("Cannot compute score from empty reps")
    return sum(reps) / len(reps)


def extract_rewards_from_cache(cache_dir: Path, run_id: str) -> dict[str, list[float]]:
    """Read rewards from the task-first local cache.

    Expects: cache_dir/tasks/{task}/{run_id}/rep-{N}/reward.txt
    Returns: {task_name: [reward_1, reward_2, ...]} sorted by rep number.
    """
    results: dict[str, list[float]] = {}
    tasks_dir = cache_dir / "tasks"
    if not tasks_dir.is_dir():
        return results

    for task_dir in sorted(tasks_dir.iterdir()):
        if not task_dir.is_dir():
            continue
        run_dir = task_dir / run_id
        if not run_dir.is_dir():
            continue

        reps: list[tuple[int, float]] = []
        for rep_dir in sorted(run_dir.iterdir()):
            if not rep_dir.is_dir() or not rep_dir.name.startswith("rep-"):
                continue
            reward_file = rep_dir / "reward.txt"
            if reward_file.exists():
                rep_num = int(rep_dir.name.split("-")[1])
                reward = float(reward_file.read_text().strip())
                reps.append((rep_num, reward))

        if reps:
            reps.sort(key=lambda x: x[0])
            results[task_dir.name] = [r for _, r in reps]

    return results


def make_run_metadata(
    run_id: str,
    date: str,
    git_sha: str,
    model: str,
    variant: str,
    rewards: dict[str, list[float]],
) -> RunResult:
    """Build run metadata dict from extracted rewards."""
    results = {}
    for task, reps in sorted(rewards.items()):
        results[task] = {
            "score": round(compute_score(reps), 3),
            "reps": reps,
            "reps_pass": sum(1 for r in reps if r > 0),
            "reps_total": len(reps),
        }

    return {
        "run_id": run_id,
        "date": date,
        "git_sha": git_sha,
        "model": model,
        "variant": variant,
        "results": results,
        "s3_prefix": f"{S3_PREFIX}/{run_id}/",
    }


def update_task_scorecard(
    tasks_dir: Path,
    task: str,
    run_id: str,
    date: str,
    git_sha: str,
    model: str,
    reps: list[float],
) -> None:
    """Create or update a per-task scorecard JSON file.

    Appends this run to history. Updates current_score only if this run is
    newer than the existing current (by date, then run_id as tiebreaker).
    """
    card_path = tasks_dir / f"{task}.json"
    score = round(compute_score(reps), 3)
    entry = {
        "run_id": run_id,
        "date": date,
        "git_sha": git_sha,
        "model": model,
        "score": score,
        "reps": reps,
    }

    if card_path.exists():
        card = json.loads(card_path.read_text())
        # Don't duplicate
        if any(h["run_id"] == run_id for h in card["history"]):
            return
        card["history"].append(entry)
        # Update current if this run is newer OR same date with higher score.
        # For same-date parallel experiments, the best score wins.
        current_date = card.get("current_date", "")
        current_score = card.get("current_score", -1)
        if date > current_date or (date == current_date and score > current_score):
            card["current_score"] = score
            card["current_run"] = run_id
            card["current_date"] = date
    else:
        card = {
            "task": task,
            "current_score": score,
            "current_run": run_id,
            "current_date": date,
            "status": "tested",
            "notes": "",
            "history": [entry],
        }

    card_path.write_text(json.dumps(card, indent=2) + "\n")


def rebuild_scoreboard(tasks_dir: Path, model: str) -> dict:
    """Rebuild the master scoreboard from per-task scorecard files."""
    tasks = {}
    tested_scores = []

    # Read all task scorecard files
    for card_path in sorted(tasks_dir.glob("*.json")):
        card = json.loads(card_path.read_text())
        task_name = card["task"]
        score = card["current_score"]
        current_run = card["current_run"]
        # Find reps from the current run (not just last history entry)
        current_reps = []
        for h in card.get("history", []):
            if h["run_id"] == current_run:
                current_reps = h["reps"]
                break
        tasks[task_name] = {
            "score": score,
            "last_run": current_run,
            "last_date": card.get("current_date", ""),
            "reps": current_reps,
            "status": card.get("status", "tested"),
        }
        tested_scores.append(score)

    # Add untested tasks
    for task in ALL_TASKS:
        if task not in tasks:
            tasks[task] = {
                "score": None,
                "last_run": None,
                "last_date": None,
                "reps": [],
                "status": "untested",
            }

    tested_count = len(tested_scores)
    mean_score = round(sum(tested_scores) / tested_count, 3) if tested_count else 0.0

    return {
        "model": model,
        "total_tasks": len(ALL_TASKS),
        "tested_tasks": tested_count,
        "mean_score": mean_score,
        "tasks": tasks,
    }
