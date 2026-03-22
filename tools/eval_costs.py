#!/usr/bin/env python3
"""Analyze token usage and costs across eval runs."""

import json
import os
import glob
import sys

RUNS_DIR = "/data/agent-evals/runs"


def analyze_runs():
    runs = sorted(os.listdir(RUNS_DIR))
    results = []

    for run_name in runs:
        run_dir = os.path.join(RUNS_DIR, run_name)
        if not os.path.isdir(run_dir):
            continue

        api_files = glob.glob(
            os.path.join(run_dir, "*/agent/agent-state/api.jsonl")
        )
        if not api_files:
            continue

        manifest_path = os.path.join(run_dir, "manifest.json")
        manifest = {}
        if os.path.exists(manifest_path):
            with open(manifest_path) as f:
                manifest = json.load(f)

        total_input = 0
        total_output = 0
        total_reasoning = 0
        total_cached = 0
        total_calls = 0
        total_errors = 0
        trial_count = len(api_files)

        for api_file in api_files:
            with open(api_file) as f:
                for line in f:
                    try:
                        entry = json.loads(line)
                    except Exception:
                        continue
                    if "error" in entry:
                        total_errors += 1
                        continue
                    resp = entry.get("response", {})
                    usage = resp.get("usage", {})
                    total_input += usage.get("input_tokens", 0)
                    total_output += usage.get("output_tokens", 0)
                    total_reasoning += usage.get("reasoning_tokens", 0)
                    total_cached += usage.get("cache_read_tokens", 0)
                    total_calls += 1

        if total_calls == 0:
            continue

        task_count = len(manifest.get("task_names", []))
        reps = manifest.get("reps", "?")
        model = manifest.get("model", "?")

        results.append({
            "name": run_name,
            "model": model,
            "tasks": task_count,
            "reps": reps,
            "trials": trial_count,
            "api_calls": total_calls,
            "errors": total_errors,
            "input_tokens": total_input,
            "output_tokens": total_output,
            "reasoning_tokens": total_reasoning,
            "cached_tokens": total_cached,
            "total_tokens": total_input + total_output,
        })

    return results


def compute_cost(r):
    """Estimate cost using gpt-5.3-codex pricing.

    Pricing (per 1M tokens) from pricepertoken.com:
      Input: $1.75, Cached input: $0.175, Output: $14.00
    Reasoning tokens are part of output tokens, not billed separately.
    """
    fresh_input = r["input_tokens"] - r["cached_tokens"]
    cached_input = r["cached_tokens"]
    output = r["output_tokens"]

    cost = (
        (fresh_input / 1e6 * 1.75)
        + (cached_input / 1e6 * 0.175)
        + (output / 1e6 * 14.0)
    )
    return cost


def main():
    results = analyze_runs()
    results.sort(key=lambda x: x["total_tokens"], reverse=True)

    # Header
    hdr = (
        f"{'Run':<48} {'Tasks':>5} {'Reps':>4} {'Trials':>6} "
        f"{'Calls':>6} {'Input(M)':>9} {'Out(M)':>8} "
        f"{'Reason(M)':>9} {'Cache(M)':>9} {'$Est':>8} "
        f"{'$/Trial':>8}"
    )
    print(hdr)
    print("-" * len(hdr))

    grand_cost = 0
    for r in results:
        cost = compute_cost(r)
        grand_cost += cost
        per_trial = cost / r["trials"] if r["trials"] else 0
        print(
            f"{r['name']:<48} {r['tasks']:>5} {str(r['reps']):>4} "
            f"{r['trials']:>6} {r['api_calls']:>6} "
            f"{r['input_tokens']/1e6:>9.2f} {r['output_tokens']/1e6:>8.2f} "
            f"{r['reasoning_tokens']/1e6:>9.2f} {r['cached_tokens']/1e6:>9.2f} "
            f"{cost:>8.2f} {per_trial:>8.2f}"
        )

    print()
    grand_input = sum(r["input_tokens"] for r in results)
    grand_output = sum(r["output_tokens"] for r in results)
    grand_reasoning = sum(r["reasoning_tokens"] for r in results)
    grand_cached = sum(r["cached_tokens"] for r in results)
    grand_trials = sum(r["trials"] for r in results)
    print(
        f"GRAND TOTAL: {grand_input/1e6:.1f}M input, "
        f"{grand_output/1e6:.1f}M output, "
        f"{grand_reasoning/1e6:.1f}M reasoning, "
        f"{grand_cached/1e6:.1f}M cached"
    )
    print(f"ESTIMATED TOTAL COST: ${grand_cost:.2f}")
    print(f"TOTAL TRIALS: {grand_trials}")
    print(f"AVG COST PER TRIAL: ${grand_cost/grand_trials:.2f}" if grand_trials else "")


if __name__ == "__main__":
    main()
