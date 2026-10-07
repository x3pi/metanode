#!/usr/bin/env python3
"""
Unit Test Suite for Scientific Statistical Utilities (stats_util.py)
Includes independent reference verification, edge cases, and negative control testing.
"""

import math
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from stats_util import (
    mean,
    sample_sd,
    t_pdf,
    t_two_tailed_p,
    t_critical,
    welch_t_test,
    linear_regression,
    spearman_correlation,
)

class TestStatsUtil(unittest.TestCase):
    def test_independent_t_distribution_references(self):
        """
        Verify exact Student's t two-tailed p-values against standard reference tables:
        1. t=1.98, df=9.6  => p ≈ 0.0771
        2. t=3.50, df=11.7 => p ≈ 0.0045
        3. t=1.00, df=9.5  => p ≈ 0.3421
        Tolerance: <= 1e-3
        """
        cases = [
            (1.98, 9.6, 0.0771),
            (3.50, 11.7, 0.0045),
            (1.00, 9.5, 0.3421),
        ]
        for t_val, df, expected_p in cases:
            calc_p = t_two_tailed_p(t_val, df)
            diff = abs(calc_p - expected_p)
            self.assertLessEqual(
                diff,
                0.001,
                f"Failed reference for t={t_val}, df={df}: got {calc_p:.5f}, expected ~{expected_p:.5f} (diff={diff:.5f})"
            )

    def test_negative_control_gaussian_approximation_rejected(self):
        """
        NEGATIVE CONTROL:
        If an implementation inappropriately uses Gaussian/Normal approximation z = |t|:
        p_gaussian(1.98) = 2 * (1 - Phi(1.98)) ≈ 0.0477 (< 0.05 falsely claiming significance!).
        The true Student's t p-value is ≈ 0.0771 (>= 0.05, correctly INCONCLUSIVE).
        This test MUST FAIL if someone replaces Student's t with Normal approximation.
        """
        z = 1.98
        p_normal = 2.0 * (1.0 - 0.5 * (1.0 + math.erf(z / math.sqrt(2.0))))
        self.assertAlmostEqual(p_normal, 0.0477, places=3)

        # True Student's t p-value
        p_true = t_two_tailed_p(1.98, 9.6)
        self.assertAlmostEqual(p_true, 0.0771, places=3)

        # Ensure the difference is substantial (> 0.025) and crosses the 0.05 significance threshold
        self.assertGreater(p_true, 0.05, "Student's t p-value must be >= 0.05 for (t=1.98, df=9.6)")
        self.assertLess(p_normal, 0.05, "Normal approximation is mistakenly < 0.05")
        self.assertGreater(abs(p_true - p_normal), 0.025)

    def test_t_critical_inversion(self):
        """Verify that t_critical accurately inverts t_two_tailed_p at alpha=0.05."""
        for df in [5.0, 9.6, 11.7, 20.0]:
            tc = t_critical(df, alpha=0.05)
            p_at_crit = t_two_tailed_p(tc, df)
            self.assertAlmostEqual(
                p_at_crit,
                0.05,
                places=4,
                msg=f"t_critical inversion failed for df={df}: p={p_at_crit}"
            )

    def test_welch_t_test_controlled(self):
        """Verify Welch's t-test with controlled datasets."""
        # Dataset with clear difference
        group_a = [10.0, 11.0, 12.0, 11.5, 10.5]
        group_b = [20.0, 21.0, 22.0, 21.5, 20.5]

        t_stat, df, p_val, (ci_low, ci_high) = welch_t_test(group_a, group_b)
        self.assertLess(t_stat, -10.0)
        self.assertLess(p_val, 1e-4)
        # CI of difference should not contain 0
        self.assertLess(ci_high, 0.0)

    def test_linear_regression(self):
        """Verify OLS regression slope, intercept, R2, and slope CI."""
        # y = 2.0 * x + 5.0
        xs = [1.0, 2.0, 3.0, 4.0, 5.0]
        ys = [7.0, 9.0, 11.0, 13.0, 15.0]

        slope, intercept, r2, (ci_low, ci_high), se = linear_regression(xs, ys)
        self.assertAlmostEqual(slope, 2.0, places=4)
        self.assertAlmostEqual(intercept, 5.0, places=4)
        self.assertAlmostEqual(r2, 1.0, places=4)
        self.assertAlmostEqual(se, 0.0, places=4)

    def test_spearman_correlation(self):
        """Verify Spearman rank correlation."""
        # Monotonically increasing
        x = [1, 2, 3, 4, 5, 6, 7]
        y = [10, 15, 25, 40, 50, 75, 100]
        rho = spearman_correlation(x, y)
        self.assertAlmostEqual(rho, 1.0, places=4)

        # Monotonically decreasing
        y_rev = [100, 75, 50, 40, 25, 15, 10]
        rho_rev = spearman_correlation(x, y_rev)
        self.assertAlmostEqual(rho_rev, -1.0, places=4)

if __name__ == "__main__":
    unittest.main()
