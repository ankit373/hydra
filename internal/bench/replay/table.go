// SPDX-License-Identifier: MIT

package replay

import (
	"fmt"
	"math"
	"strings"
	"text/tabwriter"
)

// Table prints one line per (kind, policy, alpha). The interval is the first seed's.
func Table(rep Report) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	first := "-"
	if len(rep.Seeds) > 0 {
		first = fmt.Sprint(rep.Seeds[0])
	}
	fmt.Fprintf(w, "kind\tpolicy\talpha\tfalse accept per task (mean)\t95%% CI, seed %s\tworst split\tsplits over alpha\trefusal\t$ per task\t$ per correct accepted\n", first)
	for _, s := range rep.Summaries {
		fmt.Fprintf(w, "%s\t%s\t%.2f\t%.4f\t[%.4f, %.4f]\t%.4f\t%.0f%%\t%.1f%%\t%.6f\t%s\n",
			s.Kind, s.Policy, s.Alpha, s.MeanFalseAccept, s.FirstLo, s.FirstHi, s.WorstFalseAccept,
			100*s.OverAlpha, 100*s.Refusal, s.CostPerTask, dollars(s.CostPerCorrect))
	}
	w.Flush()
	for _, s := range rep.Summaries {
		if len(s.Bars) > 0 {
			fmt.Fprintf(&b, "bars per candidate, %s %s alpha %.2f: %s\n", s.Kind, s.Policy, s.Alpha, bars(s.Bars))
		}
		if s.Untestable > 0 {
			fmt.Fprintf(&b, "%s %s alpha %.2f: %d check pair(s) linked for lack of shared wrong answers (first split)\n",
				s.Kind, s.Policy, s.Alpha, s.Untestable)
		}
	}
	fmt.Fprintf(&b, "seeds: %s\n", strings.Trim(fmt.Sprint(rep.Seeds), "[]"))
	return b.String()
}

func dollars(u USD) string {
	if math.IsNaN(float64(u)) {
		return "n/a (none correct)"
	}
	return fmt.Sprintf("%.6f", float64(u))
}

func bars(b []float64) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%.4g", x)
	}
	return strings.Join(parts, ", ")
}
