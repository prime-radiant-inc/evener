#!/usr/bin/env python3
"""Side-by-side comparison of multiple waves.

Usage:
    ./tools/wave_compare.py WAVE1 WAVE2 [WAVE3 ...]
    ./tools/wave_compare.py --labels "control,27a,27b,27d" WAVE1 WAVE2 WAVE3 WAVE4

Shows summary stats for each wave plus per-task comparison.
"""

import argparse
import json
import subprocess
import sys
from pathlib import Path


def get_wave_scores(wave_id: str) -> dict:
    """Run wave_scores.py --json and normalize to {task: {reps: [...]}}."""
    result = subprocess.run(
        ["./tools/wave_scores.py", wave_id, "--json"],
        capture_output=True, text=True, check=False,
    )
    if result.returncode != 0:
        print(f"Error running wave_scores for {wave_id}: {result.stderr}", file=sys.stderr)
        return {}
    try:
        raw = json.loads(result.stdout)
    except json.JSONDecodeError:
        return _parse_text_output(wave_id)

    # Normalize: raw is {task: {rep-1: x, rep-2: y, ...}}
    normalized = {}
    for task, rep_dict in raw.items():
        if not isinstance(rep_dict, dict):
            continue
        reps = []
        for k in sorted(rep_dict.keys()):
            if k.startswith("rep-"):
                reps.append(rep_dict[k])
        if reps:
            normalized[task] = {"reps": reps}
    return normalized


def _parse_text_output(wave_id: str) -> dict:
    result = subprocess.run(
        ["./tools/wave_scores.py", wave_id],
        capture_output=True, text=True, check=False,
    )
    tasks = {}
    for line in result.stdout.splitlines():
        parts = line.split()
        if len(parts) < 2 or parts[0].startswith(("Task", "---", "Overall", "0/N")):
            continue
        name = parts[0]
        reps = []
        for p in parts[1:-1]:
            if p == "—":
                reps.append(None)
            else:
                try:
                    reps.append(float(p))
                except ValueError:
                    break
        if reps:
            tasks[name] = {"reps": reps}
    return tasks


def summary_stats(wave_data: dict, expected_reps: int = 3) -> dict:
    """Compute summary stats from a wave's task data."""
    scored = 0
    fully_complete = 0
    perfect = 0
    total_score = 0.0
    for task, info in wave_data.items():
        if task.endswith("result.json") or task == "?":
            continue
        reps = info.get("reps", [])
        valid_reps = [r for r in reps if r is not None]
        if not valid_reps:
            continue
        scored += 1
        if len(valid_reps) >= expected_reps:
            fully_complete += 1
        score = sum(valid_reps) / len(valid_reps)
        total_score += score
        if score >= 0.999:
            perfect += 1
    mean = total_score / scored if scored else 0.0
    return {
        "mean": mean, "scored": scored,
        "fully_complete": fully_complete, "perfect": perfect,
    }


def get_task_score(wave_data: dict, task: str, expected_reps: int = 3) -> tuple:
    """Returns (score, n_reps) for a task, or (None, 0) if not present."""
    info = wave_data.get(task)
    if not info:
        return None, 0
    reps = info.get("reps", [])
    valid = [r for r in reps if r is not None]
    if not valid:
        return None, 0
    return sum(valid) / len(valid), len(valid)


def main():
    parser = argparse.ArgumentParser(description="Side-by-side comparison of wave scores.")
    parser.add_argument("waves", nargs="+", help="Wave IDs to compare (first is baseline)")
    parser.add_argument("--labels", help="Comma-separated labels for each wave")
    parser.add_argument("--reps", type=int, default=3, help="Expected reps per task")
    parser.add_argument("--delta-only", action="store_true",
                        help="Show only tasks where deltas differ from baseline")
    parser.add_argument("--min-delta", type=float, default=0.0,
                        help="Only show deltas >= this absolute value (default: 0)")
    args = parser.parse_args()

    labels = args.labels.split(",") if args.labels else args.waves
    if len(labels) != len(args.waves):
        print(f"Error: {len(labels)} labels for {len(args.waves)} waves", file=sys.stderr)
        sys.exit(1)

    # Fetch all waves
    wave_data = {}
    for wave_id, label in zip(args.waves, labels):
        wave_data[label] = get_wave_scores(wave_id)

    # Summary stats
    print(f"{'Label':<20} {'Mean':>6} {'Scored':>7} {'Complete':>10} {'Perfect':>8}")
    print("-" * 60)
    for label in labels:
        stats = summary_stats(wave_data[label], args.reps)
        print(f"{label:<20} {stats['mean']:>6.3f} {stats['scored']:>7} "
              f"{stats['fully_complete']:>4}/{stats['scored']:<5} {stats['perfect']:>8}")
    print()

    # Per-task delta table (baseline is first wave)
    baseline = labels[0]
    baseline_data = wave_data[baseline]
    all_tasks = set()
    for data in wave_data.values():
        all_tasks.update(t for t in data if not t.endswith("result.json") and t != "?")

    # Header
    header = f"{'Task':<42}"
    for label in labels:
        header += f" {label:>8}"
    if len(labels) > 1:
        header += "  deltas"
    print(header)
    print("-" * (42 + 9 * len(labels) + 10))

    rows = []
    for task in sorted(all_tasks):
        baseline_score, _ = get_task_score(baseline_data, task, args.reps)
        scores = []
        for label in labels:
            score, n = get_task_score(wave_data[label], task, args.reps)
            scores.append((score, n))

        # Skip if all absent
        if all(s is None for s, _ in scores):
            continue

        # Compute deltas vs baseline
        deltas = []
        for i, (score, _) in enumerate(scores):
            if i == 0 or score is None or baseline_score is None:
                deltas.append(None)
            else:
                deltas.append(score - baseline_score)

        # Filter by min delta
        if args.min_delta > 0:
            max_abs = max((abs(d) for d in deltas if d is not None), default=0.0)
            if max_abs < args.min_delta:
                continue

        # Filter delta-only
        if args.delta_only:
            has_delta = any(d is not None and abs(d) > 0.01 for d in deltas)
            if not has_delta:
                continue

        row = f"{task:<42}"
        for score, n in scores:
            if score is None:
                row += "        -"
            elif n < args.reps:
                row += f" {score:.3f}({n})"
            else:
                row += f"   {score:.3f}"
        if len(labels) > 1:
            delta_parts = []
            for d in deltas[1:]:
                if d is None:
                    delta_parts.append("   -  ")
                elif d > 0:
                    delta_parts.append(f"+{d:.2f}")
                elif d < 0:
                    delta_parts.append(f"{d:.2f}")
                else:
                    delta_parts.append(" 0.00")
            row += "  " + " ".join(delta_parts)
        rows.append(row)

    for row in rows:
        print(row)

    if not rows:
        print("(no tasks to show with current filters)")


if __name__ == "__main__":
    main()
