# Reference values for stats_test.go. Run by hand, never in CI, in a throwaway venv:
#   python3 -m venv /tmp/v && /tmp/v/bin/pip install scipy==1.18.1 statsmodels==0.15.0
#   /tmp/v/bin/python internal/stats/testdata/gold.py
from fractions import Fraction
from math import comb, nextafter, sqrt

from scipy.optimize import brentq
from scipy.stats import beta, binomtest, norm
from statsmodels.stats.contingency_tables import mcnemar

print("# McNemar exact: P(X >= B) and two-sided, X ~ Bin(B+C, 1/2)")
for b, c in [(10, 2), (6, 8), (3, 9), (9, 3), (40, 45), (600, 500)]:
    m = b + c
    one = Fraction(sum(comb(m, i) for i in range(b, m + 1)), 2**m)
    two = binomtest(b, m, 0.5).pvalue
    sm = mcnemar([[0, b], [c, 0]], exact=True).pvalue
    print(f"B={b} C={c} one={float(one)!r} two(scipy)={two!r} two(statsmodels)={sm!r}")


# Tango's z with the restricted MLE found by root-finding the likelihood's score
# rather than by the closed-form quadratic, and Var(b - c) = N((q12+q21) - (q12-q21)^2).
def tango(n, b, c, theta):
    d = n - b - c

    def score(q21):
        # d/dq21 of b·log(q21+θ) + c·log(q21) + d·log(1-2·q21-θ); q12 = q21 + θ under H0.
        return b / (q21 + theta) + c / q21 - 2 * d / (1 - 2 * q21 - theta)

    lo, hi = max(0.0, -theta), (1 - theta) / 2
    a = nextafter(lo, 1)
    if b == 0 and score(a) <= 0:
        q21 = lo  # the likelihood falls from the boundary q12 = 0 (Tango's Table I, section 6.1)
    else:
        q21 = brentq(score, a, nextafter(hi, 0), xtol=1e-300, rtol=4 * 2.0**-52, maxiter=1000)
    z = (b - c - n * theta) / sqrt(n * (2 * q21 + theta - theta**2))
    return z, norm.sf(z)


print("# Tango z, p = 1 - Phi(z), theta0 = -margin")
for n, b, c, m in [(200, 6, 8, 0.02), (1500, 40, 45, 0.02), (100, 10, 2, 0.02), (100, 10, 2, 0.0),
                   (44, 0, 1, 0.1), (30, 0, 1, 0.1), (80, 0, 2, 0.1), (100, 0, 0, 0.02),
                   # B = 0 cases where the discriminant rounds just below zero in float64
                   (21, 0, 2, 0.05), (42, 0, 4, 0.05), (55, 0, 10, 0.1), (63, 0, 6, 0.05),
                   (77, 0, 14, 0.1)]:
    z, p = tango(n, b, c, -m)
    print(f"N={n} B={b} C={c} margin={m} z={z!r} p={p!r}")

print("# Clopper-Pearson one-sided upper, beta.ppf(1 - alpha, k + 1, n - k)")
for k, n in [(0, 100), (3, 50), (35, 500), (64, 910), (1, 1000)]:
    print(f"k={k} n={n} upper={beta.ppf(0.95, k + 1, n - k)!r}")
for alpha in (0.7, 0.9):  # alpha >= 0.5 puts the bound below k/n
    for k, n in [(3, 50), (35, 500), (0, 100)]:
        print(f"alpha={alpha} k={k} n={n} upper={beta.ppf(1 - alpha, k + 1, n - k)!r}")
print(f"k=0 n=100 closed form 1 - 0.05**(1/100) = {1 - 0.05 ** (1 / 100)!r}")


def wilson(k, n, z):
    p = k / n
    d = 1 + z * z / n
    centre = (p + z * z / (2 * n)) / d
    spread = z * sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / d
    return max(0, centre - spread), min(1, centre + spread)


print("# Wilson, z = 1.959964; statsmodels proportion_confint(method='wilson') uses its own z")
print(f"k=81 n=263 {wilson(81, 263, 1.959964)!r}")

print("# pass^k = mean C(c,k)/C(n,k), exact")
for k in (1, 2):
    v = sum(Fraction(comb(c, k), comb(4, k)) for c in (4, 3, 2, 0)) / 4
    print(f"n=4 c=4,3,2,0 k={k} {v} {float(v)!r}")

print("# Normal")
print(f"Phi(1.959964)={norm.cdf(1.959964)!r} Phi^-1(0.95)={norm.ppf(0.95)!r} Phi^-1(1e-10)={norm.ppf(1e-10)!r}")
