#!/usr/bin/env python3
"""Compare two benchmark runs with statistical significance testing.

Reads two archive run directories, compares per-task majority-vote outcomes,
and reports improvements, regressions, bootstrap CIs on pass-rate difference,
and McNemar's test for paired binary outcomes.
"""

import sys

from eval_lib import read_archive_tasks
from eval_stats import aggregate_task_results, bootstrap_ci_pass_rate_diff, mcnemars_test


def compare_runs(run_dir_a: str, run_dir_b: str, seed: int = 42) -> dict:
    """Compare two archive runs.

    Returns dict with:
    - pass_a, pass_b: majority pass counts
    - delta_majority: (B - A) / shared_task_count
    - bootstrap_ci: (lower, upper) on the pass-rate difference
    - mcnemars_chi2, mcnemars_p: McNemar's test results
    - improvements: task names that went from fail to pass (A fail -> B pass)
    - regressions: task names that went from pass to fail (A pass -> B fail)
    - tasks: per-task comparison table (list of dicts)
    - only_in_a, only_in_b: task names not shared between runs
    - shared_task_count: number of tasks compared
    """
    tasks_a = read_archive_tasks(run_dir_a)
    tasks_b = read_archive_tasks(run_dir_b)

    all_tasks_a = set(tasks_a.keys())
    all_tasks_b = set(tasks_b.keys())
    shared = sorted(all_tasks_a & all_tasks_b)
    only_a = sorted(all_tasks_a - all_tasks_b)
    only_b = sorted(all_tasks_b - all_tasks_a)

    task_results = []
    pass_list_a = []
    pass_list_b = []
    rate_list_a = []
    rate_list_b = []
    improvements = []
    regressions = []

    for task_name in shared:
        rewards_a = [r["reward"] for r in tasks_a[task_name]]
        rewards_b = [r["reward"] for r in tasks_b[task_name]]

        agg_a = aggregate_task_results(task_name, rewards_a)
        agg_b = aggregate_task_results(task_name, rewards_b)

        pa = agg_a["pass_majority"]
        pb = agg_b["pass_majority"]
        pass_list_a.append(pa)
        pass_list_b.append(pb)
        rate_list_a.append(agg_a["pass_rate"])
        rate_list_b.append(agg_b["pass_rate"])

        if not pa and pb:
            status = "improvement"
            improvements.append(task_name)
        elif pa and not pb:
            status = "regression"
            regressions.append(task_name)
        else:
            status = "stable"

        task_results.append({
            "name": task_name,
            "pass_a": pa,
            "pass_b": pb,
            "rate_a": agg_a["pass_rate"],
            "rate_b": agg_b["pass_rate"],
            "status": status,
        })

    n_shared = len(shared)
    count_a = sum(pass_list_a)
    count_b = sum(pass_list_b)
    delta = (count_b - count_a) / n_shared if n_shared > 0 else 0.0

    # Bootstrap CI on pass rate difference
    if n_shared > 0:
        ci = bootstrap_ci_pass_rate_diff(rate_list_a, rate_list_b, seed=seed)
    else:
        ci = (0.0, 0.0)

    # McNemar's test on majority-vote outcomes
    if n_shared > 0:
        chi2, p = mcnemars_test(pass_list_a, pass_list_b)
    else:
        chi2, p = 0.0, 1.0

    return {
        "shared_task_count": n_shared,
        "pass_a": count_a,
        "pass_b": count_b,
        "delta_majority": delta,
        "bootstrap_ci": ci,
        "mcnemars_chi2": chi2,
        "mcnemars_p": p,
        "improvements": improvements,
        "regressions": regressions,
        "tasks": task_results,
        "only_in_a": only_a,
        "only_in_b": only_b,
    }


def _print_report(result: dict) -> None:
    """Print a human-readable comparison report."""
    print(f"{'Task':<35} {'A':>6} {'B':>6}  Status")
    print("-" * 60)
    for t in result["tasks"]:
        pa = "PASS" if t["pass_a"] else "FAIL"
        pb = "PASS" if t["pass_b"] else "FAIL"
        marker = ""
        if t["status"] == "improvement":
            marker = " <- improved"
        elif t["status"] == "regression":
            marker = " <- REGRESSED"
        print(f"{t['name']:<35} {pa:>6} {pb:>6}  {marker}")

    print()
    n = result["shared_task_count"]
    print(f"Summary: A={result['pass_a']}/{n}  B={result['pass_b']}/{n}")
    print(f"  +{len(result['improvements'])} improved  -{len(result['regressions'])} regressed")
    print(f"  Bootstrap 95% CI on diff: [{result['bootstrap_ci'][0]:+.3f}, {result['bootstrap_ci'][1]:+.3f}]")
    print(f"  McNemar's p={result['mcnemars_p']:.4f}")

    if result["only_in_a"]:
        print(f"\nOnly in A: {', '.join(result['only_in_a'])}")
    if result["only_in_b"]:
        print(f"Only in B: {', '.join(result['only_in_b'])}")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(f"Usage: {sys.argv[0]} <run_dir_a> <run_dir_b>", file=sys.stderr)
        sys.exit(1)

    run_a = sys.argv[1]
    run_b = sys.argv[2]
    result = compare_runs(run_a, run_b)
    _print_report(result)
