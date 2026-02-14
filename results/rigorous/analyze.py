#!/usr/bin/env python3
"""Statistical analysis of RLM context strategy evaluation results.

Reads all result JSON files from /tmp/serfeval-rigorous/results/ and produces
a comprehensive statistical report comparing the 4 context strategies.
"""

import json
import os
import math
from collections import defaultdict
from pathlib import Path

RESULTS_DIR = Path("/tmp/serfeval-rigorous/results")
STRATEGIES = ["compact", "recall", "session-log", "ooda"]
TASKS = [
    "django__django-11276",
    "astropy__astropy-13977",
    "pylint-dev__pylint-4604",
    "pydata__xarray-6992",
    "sympy__sympy-13091",
]


def load_results():
    """Load all result JSON files into a nested dict: data[task][strategy] = [results]."""
    data = defaultdict(lambda: defaultdict(list))
    for task in TASKS:
        for strategy in STRATEGIES:
            strategy_dir = RESULTS_DIR / task / strategy
            if not strategy_dir.exists():
                continue
            for run_file in sorted(strategy_dir.glob("run-*.json")):
                try:
                    with open(run_file) as f:
                        result = json.load(f)
                    data[task][strategy].append(result)
                except (json.JSONDecodeError, IOError) as e:
                    print(f"  WARN: Failed to load {run_file}: {e}")
    return data


def mean(values):
    if not values:
        return 0.0
    return sum(values) / len(values)


def stdev(values):
    if len(values) < 2:
        return 0.0
    m = mean(values)
    variance = sum((x - m) ** 2 for x in values) / (len(values) - 1)
    return math.sqrt(variance)


def ci95(values):
    """95% confidence interval half-width (t-based for small N)."""
    n = len(values)
    if n < 2:
        return 0.0
    # t critical values for 95% CI (two-tailed) by df
    t_crit = {1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776, 5: 2.571,
              6: 2.447, 7: 2.365, 8: 2.306, 9: 2.262, 10: 2.228,
              15: 2.131, 20: 2.086, 25: 2.060, 30: 2.042}
    df = n - 1
    t = t_crit.get(df, 1.96)  # fallback to z for large N
    return t * stdev(values) / math.sqrt(n)


def welch_t_test(a, b):
    """Welch's t-test (unequal variances). Returns (t_stat, df, p_approx)."""
    n1, n2 = len(a), len(b)
    if n1 < 2 or n2 < 2:
        return (0.0, 0, 1.0)
    m1, m2 = mean(a), mean(b)
    s1, s2 = stdev(a), stdev(b)
    se1 = s1**2 / n1
    se2 = s2**2 / n2
    se = math.sqrt(se1 + se2)
    if se == 0:
        return (0.0, n1 + n2 - 2, 1.0)
    t_stat = (m1 - m2) / se
    # Welch-Satterthwaite degrees of freedom
    df = (se1 + se2)**2 / (se1**2 / (n1 - 1) + se2**2 / (n2 - 1)) if (se1 + se2) > 0 else n1 + n2 - 2
    # Approximate p-value using normal distribution (good enough for reporting)
    # For a proper p-value we'd need scipy, but this gives a reasonable approximation
    z = abs(t_stat)
    # Two-tailed p-value approximation using logistic approximation
    p = 2 * (1 / (1 + math.exp(0.07056 * z**3 + 1.5976 * z)))
    return (t_stat, df, p)


def cohens_d(a, b):
    """Cohen's d effect size (pooled standard deviation)."""
    n1, n2 = len(a), len(b)
    if n1 < 2 or n2 < 2:
        return 0.0
    s1, s2 = stdev(a), stdev(b)
    pooled = math.sqrt(((n1 - 1) * s1**2 + (n2 - 1) * s2**2) / (n1 + n2 - 2))
    if pooled == 0:
        return 0.0
    return (mean(a) - mean(b)) / pooled


def effect_label(d):
    """Interpret Cohen's d."""
    d = abs(d)
    if d < 0.2:
        return "negligible"
    elif d < 0.5:
        return "small"
    elif d < 0.8:
        return "medium"
    else:
        return "large"


def p_label(p):
    if p < 0.001:
        return "***"
    elif p < 0.01:
        return "**"
    elif p < 0.05:
        return "*"
    else:
        return "ns"


def main():
    data = load_results()

    # Collect per-strategy aggregates
    strat_retention = defaultdict(list)
    strat_tokens = defaultdict(list)
    strat_turns = defaultdict(list)
    strat_duration = defaultdict(list)
    strat_compaction = defaultdict(list)

    print("=" * 80)
    print("RLM CONTEXT STRATEGY EVALUATION — RIGOROUS STATISTICAL ANALYSIS")
    print("=" * 80)
    print(f"Model: gpt-5.2  |  Threshold scale: 0.15  |  Max turns: 150")
    print(f"Tasks: {len(TASKS)}  |  Strategies: {len(STRATEGIES)}  |  Runs per condition: 5")
    print()

    # ── Per-Task, Per-Strategy Table ──
    print("=" * 80)
    print("SECTION 1: PER-TASK, PER-STRATEGY RESULTS")
    print("=" * 80)

    for task in TASKS:
        print(f"\n{'─' * 60}")
        print(f"Task: {task}")
        print(f"{'─' * 60}")
        print(f"  {'Strategy':<14} {'N':>3}  {'Retention':>10}  {'±95%CI':>7}  {'Turns':>7}  {'Tokens':>10}  {'Duration':>9}")
        print(f"  {'':─<14} {'':─>3}  {'':─>10}  {'':─>7}  {'':─>7}  {'':─>10}  {'':─>9}")

        for strategy in STRATEGIES:
            runs = data[task][strategy]
            n = len(runs)
            if n == 0:
                print(f"  {strategy:<14} {0:>3}  {'N/A':>10}")
                continue

            retentions = [r["retention_score"] for r in runs]
            tokens = [r["total_tokens"] for r in runs]
            turns = [r["turn_count"] for r in runs]
            durations = [r.get("duration_seconds", 0) for r in runs]
            compactions = [r.get("compaction_events", 0) for r in runs]

            # Add to aggregate
            strat_retention[strategy].extend(retentions)
            strat_tokens[strategy].extend(tokens)
            strat_turns[strategy].extend(turns)
            strat_duration[strategy].extend(durations)
            strat_compaction[strategy].extend(compactions)

            m = mean(retentions)
            ci = ci95(retentions)
            print(f"  {strategy:<14} {n:>3}  {m:>10.3f}  {ci:>7.3f}  {mean(turns):>7.1f}  {mean(tokens):>10.0f}  {mean(durations):>8.1f}s")

    # ── Aggregate Strategy Comparison ──
    print()
    print("=" * 80)
    print("SECTION 2: AGGREGATE STRATEGY COMPARISON (ALL TASKS)")
    print("=" * 80)
    print()
    print(f"  {'Strategy':<14} {'N':>3}  {'Retention':>10}  {'StdDev':>7}  {'±95%CI':>7}  {'Turns':>7}  {'Tokens':>10}  {'Compactions':>11}")
    print(f"  {'':─<14} {'':─>3}  {'':─>10}  {'':─>7}  {'':─>7}  {'':─>7}  {'':─>10}  {'':─>11}")

    for strategy in STRATEGIES:
        r = strat_retention[strategy]
        t = strat_tokens[strategy]
        tu = strat_turns[strategy]
        c = strat_compaction[strategy]
        n = len(r)
        print(f"  {strategy:<14} {n:>3}  {mean(r):>10.3f}  {stdev(r):>7.3f}  {ci95(r):>7.3f}  {mean(tu):>7.1f}  {mean(t):>10.0f}  {mean(c):>11.1f}")

    # ── Retention per million tokens ──
    print()
    print("  Token efficiency (retention per million tokens):")
    for strategy in STRATEGIES:
        r = strat_retention[strategy]
        t = strat_tokens[strategy]
        if r and t:
            efficiency = [ret / (tok / 1_000_000) for ret, tok in zip(r, t)]
            print(f"    {strategy:<14} {mean(efficiency):.3f} ret/Mtok  (stdev {stdev(efficiency):.3f})")

    # ── Pairwise Comparisons ──
    print()
    print("=" * 80)
    print("SECTION 3: PAIRWISE STATISTICAL COMPARISONS (RETENTION)")
    print("=" * 80)
    print()
    print("  Welch's t-test (unequal variances), two-tailed")
    print("  Significance: *** p<0.001, ** p<0.01, * p<0.05, ns = not significant")
    print()
    print(f"  {'Comparison':<30} {'Δ Mean':>7}  {'Cohen d':>8}  {'Effect':>10}  {'t':>7}  {'df':>5}  {'p':>8}  {'Sig':>4}")
    print(f"  {'':─<30} {'':─>7}  {'':─>8}  {'':─>10}  {'':─>7}  {'':─>5}  {'':─>8}  {'':─>4}")

    comparisons = [
        ("compact", "recall"),
        ("compact", "session-log"),
        ("compact", "ooda"),
        ("recall", "session-log"),
        ("recall", "ooda"),
        ("session-log", "ooda"),
    ]

    for s1, s2 in comparisons:
        a = strat_retention[s1]
        b = strat_retention[s2]
        delta = mean(b) - mean(a)
        d = cohens_d(b, a)
        t_stat, df, p = welch_t_test(b, a)
        label = f"{s1} vs {s2}"
        print(f"  {label:<30} {delta:>+7.3f}  {d:>8.3f}  {effect_label(d):>10}  {t_stat:>7.3f}  {df:>5.1f}  {p:>8.4f}  {p_label(p):>4}")

    # ── Per-Task Pairwise (compact as baseline) ──
    print()
    print("=" * 80)
    print("SECTION 4: PER-TASK PAIRWISE vs COMPACT (RETENTION)")
    print("=" * 80)
    print()

    for task in TASKS:
        print(f"  {task}:")
        compact_r = [r["retention_score"] for r in data[task]["compact"]]
        for strategy in ["recall", "session-log", "ooda"]:
            other_r = [r["retention_score"] for r in data[task][strategy]]
            if not compact_r or not other_r:
                print(f"    vs {strategy:<14} N/A")
                continue
            delta = mean(other_r) - mean(compact_r)
            d = cohens_d(other_r, compact_r)
            t_stat, df, p = welch_t_test(other_r, compact_r)
            print(f"    vs {strategy:<14} Δ={delta:>+.3f}  d={d:>+.3f} ({effect_label(d)})  p={p:.4f} {p_label(p)}")
        print()

    # ── Win/Loss Matrix ──
    print("=" * 80)
    print("SECTION 5: WIN/LOSS MATRIX (WHICH STRATEGY WON PER TASK)")
    print("=" * 80)
    print()
    print(f"  {'Task':<30} {'Winner':<14} {'Best Ret':>8}  {'Worst':<14} {'Worst Ret':>9}")
    print(f"  {'':─<30} {'':─<14} {'':─>8}  {'':─<14} {'':─>9}")

    strategy_wins = defaultdict(int)
    for task in TASKS:
        best_strategy = None
        best_mean = -1
        worst_strategy = None
        worst_mean = 2
        for strategy in STRATEGIES:
            runs = data[task][strategy]
            if not runs:
                continue
            r = mean([r["retention_score"] for r in runs])
            if r > best_mean:
                best_mean = r
                best_strategy = strategy
            if r < worst_mean:
                worst_mean = r
                worst_strategy = strategy
        strategy_wins[best_strategy] += 1
        print(f"  {task:<30} {best_strategy:<14} {best_mean:>8.3f}  {worst_strategy:<14} {worst_mean:>9.3f}")

    print()
    print("  Task wins per strategy:")
    for strategy in STRATEGIES:
        print(f"    {strategy:<14} {strategy_wins[strategy]} / {len(TASKS)}")

    # ── Token Cost Analysis ──
    print()
    print("=" * 80)
    print("SECTION 6: TOKEN COST ANALYSIS")
    print("=" * 80)
    print()
    print(f"  {'Strategy':<14} {'Mean Tokens':>12}  {'StdDev':>10}  {'Min':>10}  {'Max':>12}  {'Total (all)':>14}")
    print(f"  {'':─<14} {'':─>12}  {'':─>10}  {'':─>10}  {'':─>12}  {'':─>14}")

    for strategy in STRATEGIES:
        t = strat_tokens[strategy]
        if not t:
            continue
        print(f"  {strategy:<14} {mean(t):>12,.0f}  {stdev(t):>10,.0f}  {min(t):>10,}  {max(t):>12,}  {sum(t):>14,}")

    # ── Completion Rates ──
    print()
    print("=" * 80)
    print("SECTION 7: COMPLETION AND RELIABILITY")
    print("=" * 80)
    print()
    total_expected = len(TASKS) * 5
    for strategy in STRATEGIES:
        n = len(strat_retention[strategy])
        print(f"  {strategy:<14} {n}/{total_expected} completed ({n/total_expected*100:.0f}%)")

    # ── Summary & Recommendations ──
    print()
    print("=" * 80)
    print("SECTION 8: SUMMARY")
    print("=" * 80)
    print()

    # Rank strategies by mean retention
    ranked = sorted(STRATEGIES, key=lambda s: mean(strat_retention[s]), reverse=True)
    print("  Strategy ranking by mean retention score:")
    for i, s in enumerate(ranked, 1):
        r = strat_retention[s]
        print(f"    {i}. {s:<14} {mean(r):.3f} ± {ci95(r):.3f}")

    # Check if the best strategy is significantly better than compact
    best = ranked[0]
    if best != "compact":
        a = strat_retention["compact"]
        b = strat_retention[best]
        t_stat, df, p = welch_t_test(b, a)
        d = cohens_d(b, a)
        print(f"\n  Best strategy ({best}) vs baseline (compact):")
        print(f"    Δ = {mean(b) - mean(a):+.3f}, Cohen's d = {d:.3f} ({effect_label(d)}), p = {p:.4f} {p_label(p)}")
    else:
        print(f"\n  Baseline (compact) has the highest mean retention.")

    print()
    print("=" * 80)
    print("END OF REPORT")
    print("=" * 80)


if __name__ == "__main__":
    main()
