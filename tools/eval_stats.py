"""Statistical functions for benchmark evaluation.

All functions use only the Python standard library (math, random).
"""

import math
import random

# z-score lookup for supported confidence levels
_Z_SCORES = {
    0.90: 1.645,
    0.95: 1.96,
    0.99: 2.576,
}


def _z_score(confidence: float) -> float:
    """Return the z-score for a supported confidence level.

    Raises ValueError for unsupported levels.
    """
    try:
        return _Z_SCORES[confidence]
    except KeyError:
        supported = ", ".join(str(k) for k in sorted(_Z_SCORES))
        raise ValueError(
            f"Unsupported confidence level {confidence}. "
            f"Supported: {supported}"
        )


def _chi2_sf(x: float, df: int = 1) -> float:
    """Survival function (1 - CDF) for the chi-squared distribution, df=1 only.

    Uses the relationship: for df=1, chi2 CDF = 2 * Phi(sqrt(x)) - 1,
    so sf = erfc(sqrt(x / 2)).
    """
    if df != 1:
        raise ValueError("Only df=1 is supported")
    if x <= 0:
        return 1.0
    return math.erfc(math.sqrt(x / 2))


def wilson_ci(successes: int, trials: int, confidence: float = 0.95):
    """Wilson score confidence interval for a binomial proportion.

    Returns (lower, upper). For 0 trials, returns (0.0, 1.0).
    """
    if trials == 0:
        return (0.0, 1.0)

    z = _z_score(confidence)
    z2 = z * z
    n = trials
    p_hat = successes / n

    denominator = 1 + z2 / n
    centre = (p_hat + z2 / (2 * n)) / denominator
    margin = (z / denominator) * math.sqrt(p_hat * (1 - p_hat) / n + z2 / (4 * n * n))

    lower = max(0.0, centre - margin)
    upper = min(1.0, centre + margin)
    return (lower, upper)


def bootstrap_ci_pass_rate_diff(
    task_rates_a,
    task_rates_b,
    n_bootstrap: int = 10000,
    confidence: float = 0.95,
    seed=None,
):
    """Bootstrap confidence interval on mean pass-rate difference (B - A).

    Resamples at the task level (not rep level). Positive values mean B is
    better than A.

    task_rates_a and task_rates_b must have the same length (one entry per
    task).

    Returns (lower, upper).
    """
    if len(task_rates_a) != len(task_rates_b):
        raise ValueError(
            f"task_rates_a ({len(task_rates_a)}) and task_rates_b "
            f"({len(task_rates_b)}) must have the same length"
        )

    n_tasks = len(task_rates_a)
    rng = random.Random(seed)

    diffs = []
    for _ in range(n_bootstrap):
        indices = [rng.randrange(n_tasks) for _ in range(n_tasks)]
        mean_a = sum(task_rates_a[i] for i in indices) / n_tasks
        mean_b = sum(task_rates_b[i] for i in indices) / n_tasks
        diffs.append(mean_b - mean_a)

    diffs.sort()
    alpha = 1 - confidence
    lo_idx = int(math.floor((alpha / 2) * n_bootstrap))
    hi_idx = int(math.ceil((1 - alpha / 2) * n_bootstrap)) - 1
    # Clamp indices
    lo_idx = max(0, min(lo_idx, n_bootstrap - 1))
    hi_idx = max(0, min(hi_idx, n_bootstrap - 1))

    return (diffs[lo_idx], diffs[hi_idx])


def mcnemars_test(pass_a, pass_b):
    """McNemar's test for paired binary outcomes.

    pass_a and pass_b are lists of booleans (or truthy/falsy values) of the
    same length, one entry per task.

    Returns (chi_squared, p_value).
    """
    if len(pass_a) != len(pass_b):
        raise ValueError(
            f"pass_a ({len(pass_a)}) and pass_b ({len(pass_b)}) "
            f"must have the same length"
        )

    # b: A fails, B passes (improvement)
    # c: A passes, B fails (regression)
    b = 0
    c = 0
    for a_i, b_i in zip(pass_a, pass_b):
        if not a_i and b_i:
            b += 1
        elif a_i and not b_i:
            c += 1

    if b + c == 0:
        return (0.0, 1.0)

    chi2 = (b - c) ** 2 / (b + c)
    p = _chi2_sf(chi2, df=1)
    return (chi2, p)


def aggregate_task_results(task_name: str, rewards):
    """Compute strict/majority/any pass from per-rep reward values.

    A reward > 0 counts as a pass.

    Returns a dict with: name, pass_majority, pass_strict, pass_any,
    pass_rate, reps_pass, reps_total.
    """
    reps_total = len(rewards)
    reps_pass = sum(1 for r in rewards if r > 0)
    pass_rate = reps_pass / reps_total if reps_total > 0 else 0.0

    return {
        "name": task_name,
        "pass_majority": reps_pass > reps_total / 2,
        "pass_strict": reps_pass == reps_total,
        "pass_any": reps_pass > 0,
        "pass_rate": pass_rate,
        "reps_pass": reps_pass,
        "reps_total": reps_total,
    }
