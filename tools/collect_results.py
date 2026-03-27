#!/usr/bin/env python3
"""Collect eval results from S3 and update the scoreboard.

Downloads run collateral from S3, normalizes into task-first local cache,
extracts rewards, and updates git-tracked metadata (run files, task
scorecards, master scoreboard).

Usage:
    ./tools/collect_results.py v20-impl-test-a \\
        --model openai/gpt-5.4-mini \\
        --git-sha abc1234 \\
        --variant "implementer: write minimal client command"

    # Light mode: only download reward.txt files (fast backfill)
    ./tools/collect_results.py v20-impl-test-a --light \\
        --model openai/gpt-5.4-mini --git-sha abc1234

    # Skip S3 download, just update metadata from existing cache
    ./tools/collect_results.py v20-impl-test-a --no-download \\
        --model openai/gpt-5.4-mini --git-sha abc1234

    # Rebuild scoreboard from existing task files (no run processing)
    ./tools/collect_results.py --rebuild-scoreboard
"""

import argparse
import json
import os
import re
import subprocess
import sys
from datetime import datetime
from pathlib import Path

# Add tools/ to path for imports
sys.path.insert(0, str(Path(__file__).parent))

from eval_results import (
    S3_BUCKET,
    compute_score,
    extract_rewards_from_cache,
    make_run_metadata,
    rebuild_scoreboard,
    update_task_scorecard,
)

SERF_ROOT = Path(__file__).resolve().parent.parent
METADATA_DIR = SERF_ROOT / "docs" / "experiments"
RUNS_DIR = METADATA_DIR / "runs"
TASKS_DIR = METADATA_DIR / "tasks"
CACHE_DIR = Path.home() / ".serf-evals"


def download_from_s3(run_id: str, light: bool = False) -> Path:
    """Download run results from S3 to a temp staging area.

    If light=True, only downloads reward.txt and result.json files
    (enough for scoreboard, skips heavy transcripts/API logs).
    """
    staging = CACHE_DIR / "_staging" / run_id
    staging.mkdir(parents=True, exist_ok=True)

    s3_url = f"s3://{S3_BUCKET}/runs/{run_id}/"
    cmd = ["aws", "s3", "sync", s3_url, str(staging), "--region", "us-west-1", "--no-cli-pager"]

    if light:
        print(f"Downloading rewards from {s3_url} (light mode) ...")
        cmd += [
            "--exclude", "*",
            "--include", "*/reward.txt",
            "--include", "*/result.json",
            "--include", "*/verifier/test-stdout.txt",
        ]
    else:
        print(f"Downloading {s3_url} (full) ...")

    subprocess.run(cmd, check=True)
    return staging


def normalize_to_cache(staging: Path, run_id: str) -> None:
    """Reorganize S3's run-first layout into task-first local cache.

    S3 layout:  staging/rep-N/{job_name}/{task}__hash/verifier/reward.txt
    Cache layout: tasks/{task}/{run_id}/rep-N/reward.txt
    """
    for rep_dir in sorted(staging.iterdir()):
        if not rep_dir.is_dir() or not rep_dir.name.startswith("rep-"):
            continue
        rep_name = rep_dir.name  # e.g. "rep-1"

        # Find task directories (inside the job-name subdirectory)
        for job_dir in rep_dir.iterdir():
            if not job_dir.is_dir():
                continue
            for entry in job_dir.iterdir():
                if not entry.is_dir():
                    continue
                # Strip __hash suffix from task name
                task_name = re.sub(r"__[A-Za-z0-9]+$", "", entry.name)
                if task_name == entry.name and not (entry / "verifier").is_dir():
                    # Not a task directory (might be config.json's parent)
                    continue

                dest = CACHE_DIR / "tasks" / task_name / run_id / rep_name
                dest.mkdir(parents=True, exist_ok=True)

                # Copy key files with flat structure
                _copy_if_exists(entry / "verifier" / "reward.txt", dest / "reward.txt")
                _copy_if_exists(entry / "verifier" / "test-stdout.txt", dest / "verifier-stdout.txt")
                _copy_if_exists(entry / "verifier" / "ctrf.json", dest / "ctrf.json")
                _copy_if_exists(entry / "result.json", dest / "result.json")
                _copy_if_exists(entry / "trial.log", dest / "trial.log")

                # Agent files
                agent_state = entry / "agent" / "agent-state"
                if agent_state.is_dir():
                    _copy_if_exists(agent_state / "api.jsonl", dest / "api.jsonl")
                    _copy_if_exists(agent_state / "trajectory.json", dest / "trajectory.json")

                    # Sessions
                    sessions_src = agent_state / "sessions"
                    if sessions_src.is_dir():
                        sessions_dest = dest / "sessions"
                        sessions_dest.mkdir(exist_ok=True)
                        for f in sessions_src.iterdir():
                            _copy_if_exists(f, sessions_dest / f.name)

                # Agent stdout
                cmd_dir = entry / "agent" / "command-0"
                if cmd_dir.is_dir():
                    _copy_if_exists(cmd_dir / "stdout.txt", dest / "agent-stdout.txt")


def _copy_if_exists(src: Path, dest: Path) -> None:
    """Copy file if source exists."""
    if src.is_file():
        import shutil
        shutil.copy2(src, dest)


def update_metadata(run_id: str, date: str, git_sha: str, model: str, variant: str) -> None:
    """Extract rewards from cache and update all git-tracked metadata."""
    RUNS_DIR.mkdir(parents=True, exist_ok=True)
    TASKS_DIR.mkdir(parents=True, exist_ok=True)

    rewards = extract_rewards_from_cache(CACHE_DIR, run_id)
    if not rewards:
        print(f"No rewards found for {run_id} in cache.", file=sys.stderr)
        sys.exit(1)

    # Write run metadata
    run_meta = make_run_metadata(run_id, date, git_sha, model, variant, rewards)
    run_path = RUNS_DIR / f"{run_id}.json"
    run_path.write_text(json.dumps(run_meta, indent=2) + "\n")
    print(f"  Run metadata: {run_path.relative_to(SERF_ROOT)}")

    # Update per-task scorecards
    for task, reps in sorted(rewards.items()):
        update_task_scorecard(TASKS_DIR, task, run_id, date, git_sha, model, reps)

    # Rebuild scoreboard
    board = rebuild_scoreboard(TASKS_DIR, model)
    board_path = METADATA_DIR / "scoreboard.json"
    board["updated"] = date
    board_path.write_text(json.dumps(board, indent=2) + "\n")
    print(f"  Scoreboard:   {board_path.relative_to(SERF_ROOT)}")

    # Print summary
    print()
    print(f"Run {run_id}: {len(rewards)} tasks")
    print(f"{'Task':<45} {'Score':>6}  Reps")
    print("-" * 70)
    for task, reps in sorted(rewards.items()):
        score = compute_score(reps)
        rep_str = " ".join("+" if r > 0 else "-" for r in reps)
        print(f"{task:<45} {score:>5.3f}  {rep_str}")
    print("-" * 70)
    all_scores = [compute_score(r) for r in rewards.values()]
    print(f"{'Mean':<45} {sum(all_scores)/len(all_scores):>5.3f}")
    print()
    print(f"Scoreboard: {board['tested_tasks']}/{board['total_tasks']} tested, "
          f"mean {board['mean_score']:.3f}")


def main():
    parser = argparse.ArgumentParser(description="Collect eval results from S3.")
    parser.add_argument("run_id", nargs="?", help="Run ID to collect")
    parser.add_argument("--model", default="openai/gpt-5.4-mini", help="Model used")
    parser.add_argument("--git-sha", help="Git SHA of the serf binary")
    parser.add_argument("--variant", default="", help="Variant description")
    parser.add_argument("--date", help="Run date (default: today)")
    parser.add_argument("--light", action="store_true",
                        help="Only download reward/result files (fast backfill)")
    parser.add_argument("--no-download", action="store_true", help="Skip S3 download")
    parser.add_argument("--rebuild-scoreboard", action="store_true",
                        help="Rebuild scoreboard from existing task files")
    args = parser.parse_args()

    if args.rebuild_scoreboard:
        TASKS_DIR.mkdir(parents=True, exist_ok=True)
        board = rebuild_scoreboard(TASKS_DIR, args.model)
        board["updated"] = args.date or datetime.now().strftime("%Y-%m-%d")
        board_path = METADATA_DIR / "scoreboard.json"
        board_path.write_text(json.dumps(board, indent=2) + "\n")
        print(f"Scoreboard rebuilt: {board['tested_tasks']}/{board['total_tasks']} tested, "
              f"mean {board['mean_score']:.3f}")
        return

    if not args.run_id:
        parser.error("run_id is required (unless --rebuild-scoreboard)")

    date = args.date or datetime.now().strftime("%Y-%m-%d")

    if not args.no_download:
        staging = download_from_s3(args.run_id, light=args.light)
        print("Normalizing to task-first cache...")
        normalize_to_cache(staging, args.run_id)
        # Clean staging
        import shutil
        shutil.rmtree(staging, ignore_errors=True)
        print(f"  Cache: {CACHE_DIR / 'tasks'}")

    update_metadata(args.run_id, date, args.git_sha or "unknown", args.model, args.variant)


if __name__ == "__main__":
    main()
