// SPDX-License-Identifier: MIT

package bench

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const tableHeader = "| Suite | Arm | n | Pass % (95% CI) | Δ vs frontier, pts (one-sided 95% LB) | $ / accepted | Escalation % | Verdict |\n" +
	"|---|---|---|---|---|---|---|---|\n"

// Table renders summaries as one markdown row per (suite, arm). It reads only what
// the summaries hold, and never rounds an interval into a pass.
func Table(sums []Summary) string {
	var b strings.Builder
	b.WriteString(tableHeader)
	var runs, shas []string
	var priced time.Time
	for _, s := range sums {
		runs = append(runs, s.RunID)
		shas = appendNew(shas, s.Breadcrumb.GitSHA)
		for _, p := range s.Breadcrumb.Prices {
			if p.FetchedAt.After(priced) {
				priced = p.FetchedAt
			}
		}
		for _, arm := range armOrder(s.Arms) {
			a := s.Arms[arm]
			delta := "—"
			if ps, ok := s.Paired[arm+"-vs-"+Frontier]; ok && ps.BootLower95 != nil {
				delta = fmt.Sprintf("%s (%s)", pts(ps.DeltaPass), pts(*ps.BootLower95))
			}
			cost := "—"
			if a.CostPerAccepted != nil {
				cost = strconv.FormatFloat(*a.CostPerAccepted, 'g', 5, 64)
			}
			// No hypothesis verdict exists until served arms do (PR 6); the column says
			// why a row could not be one even then.
			v := "—"
			switch {
			case s.Incomplete:
				v = fmt.Sprintf("INCOMPLETE (%d unscored)", s.Unscored)
			case s.Breadcrumb.LicenceVerified == "":
				v = "unpublishable (licence)"
			case s.Tier == "smoke":
				v = "not a verdict (smoke)"
			}
			fmt.Fprintf(&b, "| %s | %s | %d | %.1f (%.1f–%.1f) | %s | %s | — | %s |\n", s.Breadcrumb.Suite, arm, a.N,
				100*a.PassRate, 100*a.PassWilson[0], 100*a.PassWilson[1], delta, cost, v)
		}
	}
	fmt.Fprintf(&b, "\nRuns: %s. Git: %s.", strings.Join(runs, ", "), strings.Join(shas, ", "))
	if !priced.IsZero() {
		fmt.Fprintf(&b, " Prices fetched %s.", priced.Format("2006-01-02"))
	}
	b.WriteString("\n\nCaveat: " + contamination + ".\n")
	return b.String()
}

// armOrder puts the frontier first, the computed oracle last, and the rest by name.
func armOrder(arms map[string]ArmSummary) []string {
	var out []string
	for a := range arms {
		out = append(out, a)
	}
	rank := func(a string) int {
		switch a {
		case Frontier:
			return 0
		case OracleArm:
			return 2
		}
		return 1
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// pts is a proportion difference in points, one decimal, with a real minus sign.
func pts(d float64) string {
	return strings.Replace(fmt.Sprintf("%+.1f", 100*d), "-", "−", 1)
}

func appendNew(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}
