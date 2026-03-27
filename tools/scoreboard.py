#!/usr/bin/env python3
"""Display the eval scoreboard and per-task history.

Usage:
    ./tools/scoreboard.py                     # Full matrix
    ./tools/scoreboard.py --task kv-store-grpc  # Single task history
    ./tools/scoreboard.py --failing           # Tasks with score < 1.0
    ./tools/scoreboard.py --untested          # Tasks not yet tested
    ./tools/scoreboard.py --solved            # Tasks with score == 1.0
"""

import argparse
import json
import sys
from pathlib import Path

SERF_ROOT = Path(__file__).resolve().parent.parent
METADATA_DIR = SERF_ROOT / "docs" / "experiments"
SCOREBOARD_PATH = METADATA_DIR / "scoreboard.json"
TASKS_DIR = METADATA_DIR / "tasks"


def load_scoreboard() -> dict:
    if not SCOREBOARD_PATH.exists():
        print("No scoreboard found. Run collect_results.py first.", file=sys.stderr)
        sys.exit(1)
    return json.loads(SCOREBOARD_PATH.read_text())


def show_matrix(board: dict, filter_fn=None, sort_by="name") -> None:
    """Display the task matrix."""
    tasks = board["tasks"]
    items = [(name, info) for name, info in sorted(tasks.items()) if not filter_fn or filter_fn(info)]

    if sort_by == "score":
        items.sort(key=lambda x: (x[1]["score"] is None, -(x[1]["score"] or 0), x[0]))

    print(f"Model: {board['model']}  |  Updated: {board.get('updated', '?')}")
    print(f"Tested: {board['tested_tasks']}/{board['total_tasks']}  |  Mean: {board['mean_score']:.3f}")
    print()
    print(f"{'Task':<45} {'Score':>6}  {'Run':<25} Reps")
    print("-" * 95)

    for name, info in items:
        score = info["score"]
        if score is None:
            score_str = "  —"
            rep_str = ""
            run_str = ""
        else:
            score_str = f"{score:.3f}"
            rep_str = " ".join("+" if r > 0 else "-" for r in info.get("reps", []))
            run_str = info.get("last_run", "") or ""

        print(f"{name:<45} {score_str:>6}  {run_str:<25} {rep_str}")

    print("-" * 95)
    print(f"{len(items)} tasks shown")


def show_task_history(task: str) -> None:
    """Display full history for a single task."""
    card_path = TASKS_DIR / f"{task}.json"
    if not card_path.exists():
        print(f"No data for task '{task}'.", file=sys.stderr)
        sys.exit(1)

    card = json.loads(card_path.read_text())
    print(f"Task: {card['task']}")
    print(f"Current score: {card['current_score']:.3f} (from {card['current_run']})")
    if card.get("notes"):
        print(f"Notes: {card['notes']}")
    print()

    history = card.get("history", [])
    if not history:
        print("No run history.")
        return

    print(f"{'Date':<12} {'Run':<25} {'SHA':>8} {'Score':>6}  Reps")
    print("-" * 75)
    for h in sorted(history, key=lambda x: x["date"]):
        rep_str = " ".join("+" if r > 0 else "-" for r in h["reps"])
        sha = h.get("git_sha", "?")[:8]
        print(f"{h['date']:<12} {h['run_id']:<25} {sha:>8} {h['score']:>5.3f}  {rep_str}")


def main():
    parser = argparse.ArgumentParser(description="View eval scoreboard.")
    parser.add_argument("--task", help="Show history for a specific task")
    parser.add_argument("--failing", action="store_true", help="Only tasks with score < 1.0")
    parser.add_argument("--untested", action="store_true", help="Only untested tasks")
    parser.add_argument("--solved", action="store_true", help="Only tasks with score == 1.0")
    parser.add_argument("--sort", choices=["name", "score"], default="name")
    args = parser.parse_args()

    if args.task:
        show_task_history(args.task)
        return

    board = load_scoreboard()

    if args.failing:
        show_matrix(board, lambda i: i["score"] is not None and i["score"] < 1.0, args.sort)
    elif args.untested:
        show_matrix(board, lambda i: i["score"] is None, args.sort)
    elif args.solved:
        show_matrix(board, lambda i: i["score"] is not None and i["score"] >= 1.0, args.sort)
    else:
        show_matrix(board, sort_by=args.sort)


if __name__ == "__main__":
    main()
