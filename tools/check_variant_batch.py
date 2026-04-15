#!/usr/bin/env python3
"""Check results for all v33 variant experiments."""
import subprocess, os

BASELINES = {
    "dna-assembly": 0,
    "mailman": 0,
    "feal-linear-cryptanalysis": 0.67,
}

def main():
    runs_file = "/tmp/v33-runs.txt"
    if not os.path.exists(runs_file):
        print("No /tmp/v33-runs.txt — run run_variant_batch.py first")
        return

    with open(runs_file) as f:
        runs = [line.strip().split(None, 1) for line in f if line.strip()]

    for run_id, targets in runs:
        ls = subprocess.run(
            ["aws", "s3", "ls",
             f"s3://harbor-eval-results-526275945504/runs/{run_id}/",
             "--recursive", "--region", "us-west-1"],
            capture_output=True, text=True
        )
        reward_files = [l.split()[-1] for l in ls.stdout.strip().split("\n") if "reward.txt" in l]
        results = {}
        for rf in reward_files:
            parts = rf.split("/")
            rep, task = parts[2], parts[4].split("__")[0]
            reward = subprocess.run(
                ["aws", "s3", "cp",
                 f"s3://harbor-eval-results-526275945504/{rf}", "-",
                 "--region", "us-west-1"],
                capture_output=True, text=True
            ).stdout.strip()
            results.setdefault(task, {})[rep] = reward

        name = run_id.replace("v33-", "").rsplit("-", 1)[0]
        total_results = len(reward_files)
        expected = len(targets.split(",")) * 3

        print(f"=== {name} ({total_results}/{expected}) ===")
        for task in sorted(results):
            reps = results[task]
            passes = sum(1 for v in reps.values() if v == "1")
            total = len(reps)
            bl = BASELINES.get(task, "?")
            flag = ""
            if passes > 0 and bl == 0:
                flag = " *** IMPROVED ***"
            elif isinstance(bl, (int, float)) and total >= 2 and passes / total < bl:
                flag = " ! regression"
            rep_str = " ".join(f"{r}={v}" for r, v in sorted(reps.items()))
            print(f"  {task:35s} {passes}/{total} (bl={bl})  {rep_str}{flag}")
        if not results:
            print("  (no results yet)")
        print()


if __name__ == "__main__":
    main()
