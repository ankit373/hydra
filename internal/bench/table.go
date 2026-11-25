// SPDX-License-Identifier: MIT

package bench

import (
	"fmt"
	"maps"
	"slices"
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
	b.WriteString(DetectionTable(sums))
	b.WriteString(fieldTable(sums))
	fmt.Fprintf(&b, "\nRuns: %s. Git: %s.", strings.Join(runs, ", "), strings.Join(shas, ", "))
	if !priced.IsZero() {
		fmt.Fprintf(&b, " Prices fetched %s.", priced.Format("2006-01-02"))
	}
	b.WriteString("\n\nCaveat: " + contamination + ".\n")
	for _, line := range suiteLines(sums) {
		b.WriteString(line + "\n")
	}
	return b.String()
}

// suiteLines is one public-since line per suite in sums, in first-seen order.
func suiteLines(sums []Summary) []string {
	var out []string
	for _, s := range sums {
		out = appendNew(out, publicSince(s.Breadcrumb))
	}
	return out
}

// DetectionTable is one row per (suite, arm) that graded a check, with its models;
// empty when no arm did. The oracle is left out: the best arm per item detects nothing.
func DetectionTable(sums []Summary) string {
	var b strings.Builder
	for _, s := range sums {
		for _, arm := range armOrder(s.Arms) {
			d := s.Arms[arm].Detection
			if d == nil || arm == OracleArm {
				continue
			}
			if b.Len() == 0 {
				b.WriteString("\n| Suite | Arm | Models | Recall % (95% CI) | False flags % (95% CI) |\n|---|---|---|---|---|\n")
			}
			models := "none"
			if ms := s.Breadcrumb.Arms[arm].Models; len(ms) > 0 {
				labels := make([]string, len(ms))
				for i, m := range ms {
					labels[i] = modelLabel(m)
				}
				models = strings.Join(labels, ", ")
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %.1f (%.1f–%.1f) of %d | %.1f (%.1f–%.1f) of %d |\n",
				s.Breadcrumb.Suite, arm, models, 100*d.Recall, 100*d.RecallWilson[0], 100*d.RecallWilson[1], d.Positives,
				100*d.FalseFlagRate, 100*d.FalseFlagWilson[0], 100*d.FalseFlagWilson[1], d.Negatives)
		}
	}
	return b.String()
}

const fieldHeader = "\n| Suite | Arm | Field kind | Fields | Field accuracy % | Wrong fields caught % (95% CI) | Right fields flagged % | $ / verified field |\n" +
	"|---|---|---|---|---|---|---|---|\n"

// fieldTable is, per arm whose suite tallies fields, a row for all fields then one per
// kind, or nothing when no suite tallies fields.
func fieldTable(sums []Summary) string {
	var b strings.Builder
	row := func(suite, arm, kind string, f FieldCatch) {
		cost := "—"
		if f.CostPerVerified != nil {
			cost = strconv.FormatFloat(*f.CostPerVerified, 'g', 5, 64)
		}
		caught := "—" // no wrong field, nothing to catch
		if f.Positives > 0 {
			caught = fmt.Sprintf("%.1f (%.1f–%.1f)", 100*f.Recall, 100*f.RecallWilson[0], 100*f.RecallWilson[1])
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %.1f | %s | %.1f | %s |\n", suite, arm, kind, f.Positives+f.Negatives,
			100*f.Accuracy, caught, 100*f.FalseFlagRate, cost)
	}
	for _, s := range sums {
		for _, arm := range armOrder(s.Arms) {
			f := s.Arms[arm].Fields
			if f == nil {
				continue
			}
			if b.Len() == 0 {
				b.WriteString(fieldHeader)
			}
			row(s.Breadcrumb.Suite, arm, "all", *f)
			for _, kind := range slices.Sorted(maps.Keys(f.ByKind)) {
				row(s.Breadcrumb.Suite, arm, kind, f.ByKind[kind])
			}
		}
	}
	return b.String()
}

// modelLabel says a local model is local, so its number is not read as a hosted one's.
func modelLabel(spec string) string {
	if m, ok := strings.CutPrefix(spec, Local+":"); ok {
		return m + " (local)"
	}
	return spec
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
