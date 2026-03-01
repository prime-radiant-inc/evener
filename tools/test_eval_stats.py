"""Tests for eval_stats.py — statistical functions for benchmark evaluation.

Written BEFORE implementation (TDD).
"""

import math

import pytest

from eval_stats import (
    aggregate_task_results,
    bootstrap_ci_pass_rate_diff,
    mcnemars_test,
    wilson_ci,
)


# ---------------------------------------------------------------------------
# wilson_ci
# ---------------------------------------------------------------------------


class TestWilsonCI:
    """Wilson score interval for binomial proportions."""

    def test_52_of_89(self):
        """52/89 successes: CI should contain the point estimate ~0.584."""
        lo, hi = wilson_ci(52, 89)
        point = 52 / 89
        assert lo < point < hi
        # Bounds should be approximately [0.47-0.50, 0.67-0.70]
        assert 0.47 <= lo <= 0.50
        assert 0.67 <= hi <= 0.70

    def test_perfect_5_of_5(self):
        """5/5: lower bound should be high (>0.5), upper <= 1.0."""
        lo, hi = wilson_ci(5, 5)
        assert lo > 0.5
        assert hi <= 1.0

    def test_zero_of_5(self):
        """0/5: upper bound should be low (<0.5), lower >= 0.0."""
        lo, hi = wilson_ci(0, 5)
        assert lo >= 0.0
        assert hi < 0.5

    def test_zero_trials(self):
        """0/0: degenerate case returns full interval (0.0, 1.0)."""
        lo, hi = wilson_ci(0, 0)
        assert lo == 0.0
        assert hi == 1.0

    def test_lower_le_upper(self):
        """Lower bound is always <= upper bound."""
        for s, n in [(0, 10), (3, 10), (10, 10), (50, 100)]:
            lo, hi = wilson_ci(s, n)
            assert lo <= hi, f"Failed for {s}/{n}: {lo} > {hi}"

    def test_bounds_within_0_1(self):
        """Bounds are always within [0, 1]."""
        for s, n in [(0, 10), (5, 10), (10, 10), (1, 1), (0, 1)]:
            lo, hi = wilson_ci(s, n)
            assert 0.0 <= lo <= 1.0
            assert 0.0 <= hi <= 1.0

    def test_confidence_90(self):
        """90% confidence produces a narrower interval than 95%."""
        lo_90, hi_90 = wilson_ci(50, 100, confidence=0.90)
        lo_95, hi_95 = wilson_ci(50, 100, confidence=0.95)
        width_90 = hi_90 - lo_90
        width_95 = hi_95 - lo_95
        assert width_90 < width_95

    def test_confidence_99(self):
        """99% confidence produces a wider interval than 95%."""
        lo_95, hi_95 = wilson_ci(50, 100, confidence=0.95)
        lo_99, hi_99 = wilson_ci(50, 100, confidence=0.99)
        width_95 = hi_95 - lo_95
        width_99 = hi_99 - lo_99
        assert width_99 > width_95

    def test_unsupported_confidence_raises(self):
        """Unsupported confidence level raises ValueError."""
        with pytest.raises(ValueError):
            wilson_ci(5, 10, confidence=0.80)


# ---------------------------------------------------------------------------
# bootstrap_ci_pass_rate_diff
# ---------------------------------------------------------------------------


class TestBootstrapCI:
    """Bootstrap CI on mean pass-rate difference (B - A) across tasks."""

    def test_identical_runs_contains_zero(self):
        """Identical pass rates: CI should contain 0."""
        rates_a = [0.6, 0.8, 0.4, 0.2, 1.0, 0.0, 0.5, 0.7, 0.3, 0.9]
        rates_b = list(rates_a)  # identical
        lo, hi = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=42)
        assert lo <= 0.0 <= hi

    def test_clear_improvement(self):
        """Clear improvement (B much better): CI should be entirely above 0."""
        # 10 tasks, A gets ~1/3, B gets ~2/3
        rates_a = [1 / 3] * 10
        rates_b = [2 / 3] * 10
        lo, hi = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=42)
        assert lo > 0.0, f"Expected CI entirely above 0, got [{lo}, {hi}]"

    def test_lower_le_upper(self):
        """Lower bound is always <= upper bound."""
        rates_a = [0.5, 0.6, 0.7]
        rates_b = [0.4, 0.5, 0.6]
        lo, hi = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=42)
        assert lo <= hi

    def test_reproducible_with_seed(self):
        """Same seed produces identical results."""
        rates_a = [0.3, 0.5, 0.7, 0.9]
        rates_b = [0.4, 0.6, 0.8, 1.0]
        r1 = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=123)
        r2 = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=123)
        assert r1 == r2

    def test_different_seeds_differ(self):
        """Different seeds can produce different results."""
        # Need enough tasks with varied rates so the discrete bootstrap
        # distribution has sufficient resolution for percentiles to differ.
        import random as _rng

        gen = _rng.Random(99)
        rates_a = [gen.random() for _ in range(30)]
        rates_b = [gen.random() for _ in range(30)]
        r1 = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=1)
        r2 = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=2)
        assert r1 != r2

    def test_b_worse_than_a(self):
        """When B is worse, CI should be below 0."""
        rates_a = [2 / 3] * 10
        rates_b = [1 / 3] * 10
        lo, hi = bootstrap_ci_pass_rate_diff(rates_a, rates_b, seed=42)
        assert hi < 0.0, f"Expected CI entirely below 0, got [{lo}, {hi}]"

    def test_mismatched_lengths_raises(self):
        """Mismatched task lists should raise ValueError."""
        with pytest.raises(ValueError):
            bootstrap_ci_pass_rate_diff([0.5, 0.6], [0.5], seed=42)


# ---------------------------------------------------------------------------
# mcnemars_test
# ---------------------------------------------------------------------------


class TestMcNemarsTest:
    """McNemar's test for paired binary outcomes."""

    def test_no_discordant_pairs(self):
        """No discordant pairs: chi2=0, p=1.0."""
        # Both pass
        pass_a = [True, True, True]
        pass_b = [True, True, True]
        chi2, p = mcnemars_test(pass_a, pass_b)
        assert chi2 == 0.0
        assert p == 1.0

    def test_no_discordant_all_same(self):
        """All pairs concordant (mix of pass/fail): chi2=0, p=1.0."""
        pass_a = [True, False, True, False]
        pass_b = [True, False, True, False]
        chi2, p = mcnemars_test(pass_a, pass_b)
        assert chi2 == 0.0
        assert p == 1.0

    def test_10_improvements_0_regressions(self):
        """10 tasks improved, 0 regressed: highly significant."""
        # A fails, B passes for 10 tasks; concordant otherwise
        pass_a = [False] * 10 + [True] * 5
        pass_b = [True] * 10 + [True] * 5
        chi2, p = mcnemars_test(pass_a, pass_b)
        assert p < 0.01, f"Expected p < 0.01, got {p}"
        assert chi2 > 0.0

    def test_symmetric(self):
        """Swapping A and B gives the same p-value."""
        pass_a = [True, False, True, False, True, True, False, False]
        pass_b = [False, True, True, False, False, True, True, False]
        chi2_1, p_1 = mcnemars_test(pass_a, pass_b)
        chi2_2, p_2 = mcnemars_test(pass_b, pass_a)
        assert chi2_1 == chi2_2
        assert p_1 == p_2

    def test_equal_discordant_high_p(self):
        """Equal discordant counts: chi2=0, p=1.0."""
        # 5 improve, 5 regress
        pass_a = [False] * 5 + [True] * 5
        pass_b = [True] * 5 + [False] * 5
        chi2, p = mcnemars_test(pass_a, pass_b)
        assert chi2 == 0.0
        assert p == 1.0

    def test_mismatched_lengths_raises(self):
        """Mismatched lists raise ValueError."""
        with pytest.raises(ValueError):
            mcnemars_test([True, False], [True])


# ---------------------------------------------------------------------------
# aggregate_task_results
# ---------------------------------------------------------------------------


class TestAggregateTaskResults:
    """Aggregation of per-rep rewards into pass/fail metrics."""

    def test_2_of_3_pass(self):
        """2/3 pass: majority=True, strict=False, any=True."""
        result = aggregate_task_results("task-a", [1.0, 0.0, 1.0])
        assert result["name"] == "task-a"
        assert result["pass_majority"] is True
        assert result["pass_strict"] is False
        assert result["pass_any"] is True
        assert result["reps_pass"] == 2
        assert result["reps_total"] == 3
        assert abs(result["pass_rate"] - 2 / 3) < 1e-9

    def test_0_of_3_all_false(self):
        """0/3 pass: all metrics False."""
        result = aggregate_task_results("task-b", [0.0, 0.0, 0.0])
        assert result["pass_majority"] is False
        assert result["pass_strict"] is False
        assert result["pass_any"] is False
        assert result["reps_pass"] == 0
        assert result["reps_total"] == 3
        assert result["pass_rate"] == 0.0

    def test_3_of_3_all_true(self):
        """3/3 pass: all metrics True."""
        result = aggregate_task_results("task-c", [1.0, 1.0, 1.0])
        assert result["pass_majority"] is True
        assert result["pass_strict"] is True
        assert result["pass_any"] is True
        assert result["reps_pass"] == 3
        assert result["reps_total"] == 3
        assert result["pass_rate"] == 1.0

    def test_1_of_1(self):
        """1/1 pass: majority=True, strict=True."""
        result = aggregate_task_results("task-d", [1.0])
        assert result["pass_majority"] is True
        assert result["pass_strict"] is True
        assert result["pass_any"] is True
        assert result["reps_pass"] == 1
        assert result["reps_total"] == 1

    def test_1_of_3_minority(self):
        """1/3 pass: majority=False, any=True."""
        result = aggregate_task_results("task-e", [0.0, 1.0, 0.0])
        assert result["pass_majority"] is False
        assert result["pass_strict"] is False
        assert result["pass_any"] is True
        assert result["reps_pass"] == 1

    def test_fractional_reward(self):
        """Reward > 0 counts as pass."""
        result = aggregate_task_results("task-f", [0.5, 0.0, 0.3])
        assert result["reps_pass"] == 2
        assert result["pass_majority"] is True

    def test_name_preserved(self):
        """Task name is preserved in output."""
        result = aggregate_task_results("my-fancy-task", [1.0])
        assert result["name"] == "my-fancy-task"
