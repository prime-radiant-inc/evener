#!/usr/bin/env python3
"""Show live scores for a running or completed eval wave.

Pulls result.json files directly from S3 — no local download needed.
Groups by task with per-rep breakdown and summary stats.

Usage:
    ./tools/eval/wave_scores.py WAVE_ID
    ./tools/eval/wave_scores.py WAVE_ID --json
    ./tools/eval/wave_scores.py  # auto-detects most recent wave from .serf-launches/

Examples:
    ./tools/eval/wave_scores.py wave-625cbaf-20260331-1616
    ./tools/eval/wave_scores.py wave-625cbaf-20260331-1616 --json
"""

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

BUCKET = "harbor-eval-results-526275945504"
REGION = "us-west-1"


def s3_ls(prefix: str) -> list[str]:
    """List S3 objects under prefix, return lines."""
    r = subprocess.run(
        ["aws", "s3", "ls", f"s3://{BUCKET}/{prefix}", "--region", REGION, "--recursive"],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        return []
    return [l for l in r.stdout.strip().split("\n") if l]


def s3_get_json(key: str) -> dict | None:
    """Fetch a JSON file from S3."""
    r = subprocess.run(
        ["aws", "s3", "cp", f"s3://{BUCKET}/{key}", "-", "--region", REGION],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        return None
    try:
        return json.loads(r.stdout)
    except json.JSONDecodeError:
        return None


def launches_dir() -> Path:
    return Path(__file__).resolve().parent.parent.parent / ".serf-launches"


def find_latest_wave() -> str | None:
    """Find the most recent wave from .serf-launches/."""
    d = launches_dir()
    if not d.exists():
        return None
    files = sorted(d.glob("wave-*.json"), key=lambda p: p.stat().st_mtime)
    if not files:
        return None
    return files[-1].stem


def load_wave_meta(run_id: str) -> dict | None:
    """Load wave launch metadata (reps, task_count, etc.) if available."""
    path = launches_dir() / f"{run_id}.json"
    if not path.exists():
        return None
    try:
        return json.loads(path.read_text())
    except (json.JSONDecodeError, OSError):
        return None


def get_scores(run_id: str) -> dict[str, dict[str, float | None]]:
    """Fetch all scores for a run. Returns {task: {rep-N: score}}."""
    lines = s3_ls(f"runs/{run_id}/")
    result_paths = [l.split()[-1] for l in lines if l.endswith("/result.json")]

    scores: dict[str, dict[str, float | None]] = {}
    for path in result_paths:
        # Expected layout: runs/WAVE/rep-N/WAVE_repN/TASK__HASH/result.json
        parts = path.split("/")
        if len(parts) < 6:
            continue  # skip wave-level summary result.json at shallower depth
        rep = parts[2]  # rep-1, rep-2, rep-3
        task_raw = parts[4]
        task = re.sub(r"__[A-Za-z0-9]+$", "", task_raw)

        data = s3_get_json(path)
        if data is None:
            continue

        score = None
        # Try verifier_result.rewards.reward (terminal-bench format)
        vr = data.get("verifier_result", {})
        if isinstance(vr, dict):
            rewards = vr.get("rewards", {})
            if isinstance(rewards, dict):
                score = rewards.get("reward")
        # Fallback: top-level score
        if score is None:
            score = data.get("score")

        existing = scores.setdefault(task, {}).get(rep)
        if existing is None or (score is not None and score > existing):
            scores[task][rep] = score

    return scores


def print_table(scores: dict[str, dict[str, float | None]], expected_reps: int | None = None) -> None:
    """Print a formatted score table."""
    # Determine rep columns: use observed reps, but size the "fully complete"
    # threshold from expected_reps (from wave metadata) when available.
    observed_reps = sorted({r for reps in scores.values() for r in reps})
    if not observed_reps:
        print("No results found.")
        return

    complete_threshold = expected_reps if expected_reps is not None else len(observed_reps)

    # Header
    header = f"{'Task':<40}"
    for rep in observed_reps:
        header += f" {rep:>5}"
    header += "   mean"
    print(header)
    print("-" * len(header))

    total_mean = 0.0
    scored_tasks = 0
    fully_complete = 0
    perfect = 0
    regressions = []

    for task in sorted(scores):
        row = f"{task:<40}"
        vals = []
        for rep in observed_reps:
            s = scores[task].get(rep)
            if s is not None:
                row += f" {s:>5.1f}"
                vals.append(s)
            else:
                row += f" {'—':>5}"
        if vals:
            mean = sum(vals) / len(vals)
            row += f"   {mean:.2f}"
            total_mean += mean
            scored_tasks += 1
            rep_count = len(vals)
            if rep_count >= complete_threshold:
                fully_complete += 1
                if all(v == 1.0 for v in vals):
                    perfect += 1
                if mean == 0.0:
                    regressions.append(task)
        else:
            row += "      —"
        print(row)

    # Summary
    print("-" * len(header))
    if scored_tasks:
        overall = total_mean / scored_tasks
        print(
            f"Overall mean: {overall:.3f} | "
            f"{scored_tasks} tasks scored | "
            f"{fully_complete}/{scored_tasks} fully complete | "
            f"{perfect} perfect ({complete_threshold}/{complete_threshold})"
        )
    if regressions:
        print(f"0/N tasks: {', '.join(regressions)}")


def main():
    parser = argparse.ArgumentParser(description="Show live scores for an eval wave")
    parser.add_argument("run_id", nargs="?", help="Wave run ID (auto-detects if omitted)")
    parser.add_argument("--json", action="store_true", help="Output raw JSON")
    args = parser.parse_args()

    run_id = args.run_id
    if not run_id:
        run_id = find_latest_wave()
        if not run_id:
            print("No run ID given and no waves found in .serf-launches/", file=sys.stderr)
            sys.exit(1)
        print(f"Auto-detected: {run_id}\n", file=sys.stderr)

    scores = get_scores(run_id)
    meta = load_wave_meta(run_id)
    expected_reps = meta.get("reps") if meta else None

    if args.json:
        json.dump(scores, sys.stdout, indent=2)
        print()
    else:
        print_table(scores, expected_reps=expected_reps)


if __name__ == "__main__":
    main()
