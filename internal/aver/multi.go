// SPDX-License-Identifier: MIT

package aver

import (
	"fmt"
	"math"
)

// Spending rules for splitting alpha over candidate answers.
const (
	RuleSpend      = "spend"
	RuleBonferroni = "bonferroni"
)

// Weights splits alpha over n candidates, fixed before any answer is seen: spend
// gives 1/2, 1/4, ... and the last the rest; bonferroni 1/n, only for n <= 3.
func Weights(n int, rule string) ([]float64, error) {
	if n < 1 {
		return nil, fmt.Errorf("aver: %d candidates, need at least 1", n)
	}
	w := make([]float64, n)
	switch rule {
	case RuleSpend:
		for j := range n - 1 {
			w[j] = math.Ldexp(1, -(j + 1))
		}
		w[n-1] = math.Ldexp(1, -(n - 1))
	case RuleBonferroni:
		if n > 3 {
			return nil, fmt.Errorf("aver: bonferroni is for at most 3 candidates, got %d; use spend", n)
		}
		for j := range w {
			w[j] = 1 / float64(n)
		}
	default:
		return nil, fmt.Errorf("aver: no spending rule %q (spend, bonferroni)", rule)
	}
	return w, nil
}

// Bar is the product a candidate with weight w must reach: 1/(alpha w).
func Bar(alpha, w float64) float64 { return 1 / (alpha * w) }
