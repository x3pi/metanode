#!/usr/bin/env python3
"""
Scientific Statistical Utilities for Metanode Verification Suite
Strict Anti-Fabrication Protocol:
- Exact Student's t distribution (no Gaussian/Normal approximation for small samples)
- Welch-Satterthwaite degrees of freedom
- Two-tailed p-values via exact Simpson numerical integration of Student's t PDF
- Exact inverse t-critical value for 95% confidence intervals
- OLS Linear Regression with standard errors and slope confidence intervals
- Spearman rank correlation
"""

import math

def mean(vals):
    n = len(vals)
    if n == 0:
        return 0.0
    return sum(vals) / float(n)

def sample_sd(vals):
    n = len(vals)
    if n <= 1:
        return 0.0
    m = mean(vals)
    var = sum((x - m) ** 2 for x in vals) / float(n - 1)
    return math.sqrt(var)

def t_pdf(x, df):
    """Probability density function of Student's t distribution with df degrees of freedom."""
    coef = math.exp(math.lgamma((df + 1.0) / 2.0) - math.lgamma(df / 2.0)) / math.sqrt(df * math.pi)
    return coef * ((1.0 + (x * x) / df) ** (-(df + 1.0) / 2.0))

def t_two_tailed_p(t, df, n_steps=4000):
    """
    Computes two-tailed p-value for Student's t distribution with df degrees of freedom.
    Uses substitution u = 1 / (1 + x - |t|) mapping [|t|, inf) -> (0, 1]
    and applies composite Simpson's rule for high numerical stability.
    """
    t_val = abs(float(t))
    df = float(df)
    if df <= 0.0:
        return 1.0
    if t_val == 0.0:
        return 1.0

    n = n_steps if n_steps % 2 == 0 else n_steps + 1
    h = 1.0 / n

    def integrand(u):
        if u <= 1e-15:
            return 0.0
        x = t_val + 1.0 / u - 1.0
        return t_pdf(x, df) / (u * u)

    s = integrand(0.0) + integrand(1.0)
    for i in range(1, n):
        u = i * h
        weight = 4.0 if (i % 2 == 1) else 2.0
        s += weight * integrand(u)

    one_tail = s * h / 3.0
    p = 2.0 * one_tail
    return min(1.0, max(0.0, p))

def t_critical(df, alpha=0.05):
    """
    Computes critical value t_crit such that P(|T| >= t_crit) = alpha.
    Uses binary search over monotonic two-tailed p-value.
    """
    df = float(df)
    low = 0.0
    high = 100.0
    for _ in range(40):
        mid = (low + high) / 2.0
        p = t_two_tailed_p(mid, df, n_steps=2000)
        if p > alpha:
            low = mid
        else:
            high = mid
    return (low + high) / 2.0

def welch_t_test(group1, group2, alpha=0.05):
    """
    Performs Welch's t-test for unequal variances and potentially unequal sample sizes.
    Returns: t_stat, df, p_val, (ci_low, ci_high) for difference (mean1 - mean2).
    """
    n1, n2 = len(group1), len(group2)
    if n1 <= 1 or n2 <= 1:
        return 0.0, 1.0, 1.0, (0.0, 0.0)

    m1, m2 = mean(group1), mean(group2)
    s1, s2 = sample_sd(group1), sample_sd(group2)

    v1 = s1 * s1
    v2 = s2 * s2

    se = math.sqrt(v1 / n1 + v2 / n2)
    if se == 0.0:
        return 0.0, 1.0, 1.0, (m1 - m2, m1 - m2)

    t_stat = (m1 - m2) / se

    # Welch-Satterthwaite formula for df
    df_denom = ((v1 / n1) ** 2) / (n1 - 1) + ((v2 / n2) ** 2) / (n2 - 1)
    df = ((v1 / n1 + v2 / n2) ** 2) / df_denom if df_denom > 0 else 1.0

    p_val = t_two_tailed_p(t_stat, df)
    t_crit = t_critical(df, alpha=alpha)

    diff = m1 - m2
    ci = (diff - t_crit * se, diff + t_crit * se)

    return t_stat, df, p_val, ci

def linear_regression(x_vals, y_vals, scale=1.0, alpha=0.05):
    """
    Fits y = slope * x + intercept via OLS.
    Returns: slope, intercept, r_squared, (ci_low, ci_high), se_slope
    scale adjusts slope and CI (e.g. per 100,000 units).
    """
    n = len(x_vals)
    if n < 3:
        return 0.0, 0.0, 0.0, (0.0, 0.0), 0.0

    sum_x = sum(x_vals)
    sum_y = sum(y_vals)
    sum_xx = sum(x * x for x in x_vals)
    sum_yy = sum(y * y for y in y_vals)
    sum_xy = sum(x * y for x, y in zip(x_vals, y_vals))

    denom = n * sum_xx - sum_x * sum_x
    if denom == 0:
        return 0.0, 0.0, 0.0, (0.0, 0.0), 0.0

    slope = (n * sum_xy - sum_x * sum_y) / denom
    intercept = (sum_y - slope * sum_x) / n

    # Total and residual sum of squares
    mean_y = sum_y / n
    ss_tot = sum((y - mean_y) ** 2 for y in y_vals)
    ss_res = sum((y - (slope * x + intercept)) ** 2 for x, y in zip(x_vals, y_vals))

    r_squared = 1.0 - (ss_res / ss_tot) if ss_tot > 0 else 1.0

    # Degrees of freedom for regression residuals = n - 2
    df = n - 2
    s_err_sq = ss_res / df if df > 0 else 0.0
    var_x = sum((x - (sum_x / n)) ** 2 for x in x_vals)
    se_slope = math.sqrt(s_err_sq / var_x) if var_x > 0 else 0.0

    t_crit = t_critical(df, alpha=alpha)
    ci = ((slope - t_crit * se_slope) * scale, (slope + t_crit * se_slope) * scale)

    return slope * scale, intercept, r_squared, ci, se_slope * scale

def _rank(vals):
    """Computes fractional ranks for tied values."""
    sorted_pairs = sorted(enumerate(vals), key=lambda x: x[1])
    ranks = [0.0] * len(vals)
    i = 0
    n = len(vals)
    while i < n:
        j = i
        while j < n - 1 and sorted_pairs[j][1] == sorted_pairs[j + 1][1]:
            j += 1
        avg_rank = (i + 1 + j + 1) / 2.0
        for k in range(i, j + 1):
            orig_idx = sorted_pairs[k][0]
            ranks[orig_idx] = avg_rank
        i = j + 1
    return ranks

def spearman_correlation(x_vals, y_vals):
    """Computes Spearman rank correlation coefficient rho."""
    n = len(x_vals)
    if n < 2 or len(y_vals) != n:
        return 0.0

    rx = _rank(x_vals)
    ry = _rank(y_vals)

    mx = mean(rx)
    my = mean(ry)

    num = sum((rx[i] - mx) * (ry[i] - my) for i in range(n))
    den_x = math.sqrt(sum((rx[i] - mx) ** 2 for i in range(n)))
    den_y = math.sqrt(sum((ry[i] - my) ** 2 for i in range(n)))

    denom = den_x * den_y
    if denom == 0:
        return 0.0
    return num / denom
