"""Generate summary.json from a collected archive run directory."""

import json
import os
import sys

from eval_lib import read_archive_tasks
from eval_stats import aggregate_task_results, wilson_ci


def generate_summary(run_dir: str, run_id: str) -> dict:
    """Read reward.txt files from archive and produce summary dict.

    Raises FileNotFoundError if <run_dir>/tasks/ does not exist.
    """
    archive_tasks = read_archive_tasks(run_dir)

    tasks = []
    for task_name, reps in archive_tasks.items():
        rewards = [r["reward"] for r in reps]
        agg = aggregate_task_results(task_name, rewards)
        agg["reps"] = reps
        tasks.append(agg)

    n_tasks = len(tasks)
    majority = sum(1 for t in tasks if t["pass_majority"])
    strict = sum(1 for t in tasks if t["pass_strict"])
    any_pass = sum(1 for t in tasks if t["pass_any"])

    # Failure category counts across all reps
    fc_counts = {}
    for t in tasks:
        for r in t["reps"]:
            fc = r.get("failure_category")
            if fc:
                fc_counts[fc] = fc_counts.get(fc, 0) + 1

    maj_lo, maj_hi = wilson_ci(majority, n_tasks) if n_tasks > 0 else (0.0, 1.0)

    return {
        "schema_version": 1,
        "run_id": run_id,
        "task_count": n_tasks,
        "pass_count_majority": majority,
        "pass_count_strict": strict,
        "pass_count_any": any_pass,
        "pass_rate_majority": round(majority / n_tasks, 4) if n_tasks else 0,
        "pass_rate_strict": round(strict / n_tasks, 4) if n_tasks else 0,
        "pass_rate_any": round(any_pass / n_tasks, 4) if n_tasks else 0,
        "pass_rate_majority_ci_95": [round(maj_lo, 4), round(maj_hi, 4)],
        "failure_categories": fc_counts,
        "tasks": tasks,
    }


if __name__ == "__main__":
    run_dir = sys.argv[1]
    run_id = sys.argv[2] if len(sys.argv) > 2 else os.path.basename(run_dir)
    summary = generate_summary(run_dir, run_id)
    print(json.dumps(summary, indent=2))
