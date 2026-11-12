// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"math"
	"strings"
)

// schema is the report shape; a compare across two shapes is refused.
const schema = 1

// marker is how the comment job finds the comment it already posted.
const marker = "<!-- hydra-routing-bench -->"

// The advisory gate. A PR crossing one is reported as "would fail"; it fails
// only under -fail-on-gate, which no workflow passes yet.
const (
	maxCostRise       = 0.10 // relative rise in total estimated cost
	maxEscalationRise = 0.10 // absolute rise in the escalation rate
)

// Report is one tree's routing decisions over the suite.
type Report struct {
	Schema  int      `json:"schema"`
	Suite   string   `json:"suite"`
	Results []Result `json:"results"`
}

// Result is one prompt's decision. CostUSD is the SPRT's spend when it ran,
// else the chosen head's estimate.
type Result struct {
	ID        string  `json:"id"`
	Head      string  `json:"head,omitempty"`
	Tier      int     `json:"tier,omitempty"`
	Fallbacks int     `json:"fallbacks"`
	Rule      string  `json:"rule,omitempty"`
	Refused   string  `json:"refused,omitempty"`
	CostUSD   float64 `json:"cost_usd"`
	SPRT      *SPRT   `json:"sprt,omitempty"`
}

// SPRT is the ensemble's outcome. Correct is the verifier: the accepted
// candidate matches the suite's expected answer.
type SPRT struct {
	Decision   string  `json:"decision"`
	Samples    int     `json:"samples,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	SpentUSD   float64 `json:"spent_usd,omitempty"`
	Correct    bool    `json:"correct,omitempty"`
}

// escalated: the ensemble needed more than one head to reach its decision.
func (s *SPRT) escalated() bool { return s != nil && s.Samples > 1 }

// Totals aggregates a report for the summary table.
type Totals struct {
	Prompts, Refused, Ensembles, Escalated, Accepted, Correct int
	CostUSD                                                   float64
}

func totals(r Report) Totals {
	var t Totals
	for _, x := range r.Results {
		t.Prompts++
		t.CostUSD += x.CostUSD
		if x.Refused != "" {
			t.Refused++
		}
		if x.SPRT == nil {
			continue
		}
		t.Ensembles++
		if x.SPRT.escalated() {
			t.Escalated++
		}
		if x.SPRT.Decision == "accept" {
			t.Accepted++
		}
		if x.SPRT.Correct {
			t.Correct++
		}
	}
	return t
}

func (t Totals) escalationRate() float64 {
	return float64(t.Escalated) / float64(t.Ensembles)
}

// Change is a prompt whose decision differs; a nil side is a prompt one suite lacks.
type Change struct {
	ID         string
	Base, Head *Result
}

// Diff is base against head.
type Diff struct {
	Base, Head Totals
	Changes    []Change
	Gate       []string
}

// compare diffs two reports in the head's prompt order.
func compare(base, head Report) (Diff, error) {
	if base.Schema != head.Schema {
		return Diff{}, fmt.Errorf("report schema %d against %d", base.Schema, head.Schema)
	}
	d := Diff{Base: totals(base), Head: totals(head)}
	byID := make(map[string]*Result, len(base.Results))
	for i := range base.Results {
		byID[base.Results[i].ID] = &base.Results[i]
	}
	for i := range head.Results {
		h := &head.Results[i]
		b := byID[h.ID]
		delete(byID, h.ID)
		if b == nil || cell(*b) != cell(*h) {
			d.Changes = append(d.Changes, Change{ID: h.ID, Base: b, Head: h})
		}
		if b == nil {
			continue
		}
		if b.Refused == "" && h.Refused != "" {
			d.Gate = append(d.Gate, fmt.Sprintf("`%s` routed on base and is refused on head (%s)", h.ID, h.Refused))
		}
		if b.SPRT != nil && b.SPRT.Correct && (h.SPRT == nil || !h.SPRT.Correct) {
			d.Gate = append(d.Gate, fmt.Sprintf("`%s` verified correct on base, not on head", h.ID))
		}
	}
	for _, r := range base.Results {
		if byID[r.ID] != nil {
			d.Changes = append(d.Changes, Change{ID: r.ID, Base: byID[r.ID]})
		}
	}
	// No base results means nothing to measure against, not a rise from zero.
	switch base, head := d.Base.CostUSD, d.Head.CostUSD; {
	case d.Base.Prompts == 0 || head <= base:
	case base == 0:
		d.Gate = append(d.Gate, fmt.Sprintf("estimated cost rose from $0 to $%.6f", head))
	case (head-base)/base > maxCostRise:
		d.Gate = append(d.Gate, fmt.Sprintf("estimated cost rose %s, over the %+.0f%% bar",
			pct(base, head), maxCostRise*100))
	}
	if rise, ok := d.escalationRise(); ok && rise > maxEscalationRise {
		d.Gate = append(d.Gate, fmt.Sprintf("escalation rate rose %+.1f pp, over the %+.0f pp bar",
			rise*100, maxEscalationRise*100))
	}
	return d, nil
}

// escalationRise is head's rate minus base's, undefined when either ran no ensemble.
func (d Diff) escalationRise() (float64, bool) {
	if d.Base.Ensembles == 0 || d.Head.Ensembles == 0 {
		return 0, false
	}
	return d.Head.escalationRate() - d.Base.escalationRate(), true
}

// cell is one side of a decision as the table shows it, and what equality means.
func cell(r Result) string {
	if r.Refused != "" {
		return "refused: " + r.Refused
	}
	s := fmt.Sprintf("%s T%d, %d fallbacks, $%.6f", r.Head, r.Tier, r.Fallbacks, r.CostUSD)
	if r.Rule != "" {
		s += ", rule " + r.Rule
	}
	if p := r.SPRT; p != nil {
		s += ", sprt " + p.Decision
		if p.Samples > 0 {
			verdict := "wrong"
			if p.Correct {
				verdict = "correct"
			}
			s += fmt.Sprintf(" after %d heads at %.1f%%, %s", p.Samples, p.Confidence*100, verdict)
		}
	}
	return s
}

// render is the PR comment. note says why there is no base, when there is none.
func render(d Diff, suite, note string) string {
	var b strings.Builder
	b.WriteString(marker + "\n### Routing bench\n\n")
	if note != "" {
		fmt.Fprintf(&b, "%s\n\n", note)
	}
	fmt.Fprintf(&b, "%d of %d prompts changed their routing decision. Suite `%s`, no keys, no network: "+
		"heads are fake binaries and SPRT answers are scripted, so this measures routing, not model quality.\n\n",
		len(d.Changes), d.Head.Prompts, suite)
	b.WriteString("| | base | head | change |\n|---|---|---|---|\n")
	fmt.Fprintf(&b, "| Estimated cost (USD) | %.6f | %.6f | %s |\n", d.Base.CostUSD, d.Head.CostUSD, pct(d.Base.CostUSD, d.Head.CostUSD))
	rise := "n/a"
	if r, ok := d.escalationRise(); ok {
		rise = fmt.Sprintf("%+.1f pp", r*100)
	}
	fmt.Fprintf(&b, "| Escalation rate (SPRT > 1 head) | %s | %s | %s |\n", ratio(d.Base.Escalated, d.Base.Ensembles),
		ratio(d.Head.Escalated, d.Head.Ensembles), rise)
	fmt.Fprintf(&b, "| SPRT accepted | %s | %s | %+d |\n", ratio(d.Base.Accepted, d.Base.Ensembles),
		ratio(d.Head.Accepted, d.Head.Ensembles), d.Head.Accepted-d.Base.Accepted)
	fmt.Fprintf(&b, "| Verifier correct | %s | %s | %+d |\n", ratio(d.Base.Correct, d.Base.Ensembles),
		ratio(d.Head.Correct, d.Head.Ensembles), d.Head.Correct-d.Base.Correct)
	fmt.Fprintf(&b, "| Refused | %d | %d | %+d |\n\n", d.Base.Refused, d.Head.Refused, d.Head.Refused-d.Base.Refused)

	if len(d.Changes) > 0 {
		fmt.Fprintf(&b, "<details open><summary>Decision changes (%d)</summary>\n\n", len(d.Changes))
		b.WriteString("| prompt | base | head |\n|---|---|---|\n")
		for _, c := range d.Changes {
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", c.ID, side(c.Base), side(c.Head))
		}
		b.WriteString("\n</details>\n\n")
	}
	if len(d.Gate) == 0 {
		b.WriteString("Gate (advisory): nothing crossed.\n")
		return b.String()
	}
	b.WriteString("Gate (advisory, not enforced): this PR would fail on\n")
	for _, g := range d.Gate {
		fmt.Fprintf(&b, "- %s\n", g)
	}
	return b.String()
}

func side(r *Result) string {
	if r == nil {
		return "(not in suite)"
	}
	return cell(*r)
}

func ratio(n, of int) string {
	if of == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", n, of, 100*float64(n)/float64(of))
}

func pct(base, head float64) string {
	switch {
	case base == head:
		return "0%"
	case base == 0:
		return "new spend"
	}
	return fmt.Sprintf("%+.1f%%", 100*(head-base)/math.Abs(base))
}
