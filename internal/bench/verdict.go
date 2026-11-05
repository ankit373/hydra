// SPDX-License-Identifier: MIT

package bench

import (
	"fmt"
	"sort"
)

// Outcome is a hypothesis verdict. Inconclusive is first-class: an interval that
// crosses the margin is never a pass.
type Outcome string

const (
	Pass                Outcome = "pass"
	Fail                Outcome = "fail"
	Inconclusive        Outcome = "inconclusive"
	InconclusiveVacuous Outcome = "inconclusive-vacuous"
)

// The frozen thresholds (#1213). Changing one is a reviewed diff made before results exist.
const (
	pointFloor        = -0.01 // H1/T2: Δ̂ must be ≥ −1.0 pt
	nonVacuityFloor   = 0.10  // T2: first source ≠ frontier on at least 10% of items
	h3PassFloor       = -0.03 // H3/T4: Δpass bootstrap LB > −3.0 pts
	verdictTicket     = "#1213"
	matchCaveat       = "the match verifier checks that an answer is a valid label, not the right one, so any gain comes from measured competence and routing order, not from verification"
	hypothesisQuality = "hydra-quality"
	hypothesisCost    = "hydra-cost"
)

// Verdict is one pre-registered test, computed once per (hypothesis, suite).
type Verdict struct {
	Hypothesis string  `json:"hypothesis"`
	Threshold  string  `json:"threshold"`
	Ticket     string  `json:"ticket"`
	Suite      string  `json:"suite"`
	Arm        string  `json:"arm"`
	Gate       bool    `json:"gate"`
	Outcome    Outcome `json:"outcome"`
	Reason     string  `json:"reason"` // always set, a pass included
	Estimate   float64 `json:"estimate"`
	Lower      float64 `json:"lower"`
	Upper      float64 `json:"upper"`
	N          int     `json:"n"`
}

// verdicts runs only on the full tier and only for served arms: a direct arm that
// happens to be named hydra-quality is not Hydra.
func verdicts(run *Run, s Summary) []Verdict {
	var out []Verdict
	for _, arm := range []string{hypothesisQuality, hypothesisCost} {
		ps, ok := s.Paired[arm+"-vs-"+Frontier]
		if !ok || run.Arms[arm].Kind != "served" {
			continue
		}
		if arm == hypothesisQuality {
			out = append(out, verdictH1(run.Suite, ps, nil))
		} else {
			out = append(out, verdictH3(run.Suite, ps))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hypothesis < out[j].Hypothesis })
	return out
}

// verdictH1 is non-inferiority of hydra-quality to the frontier. firstNotFrontier is
// the share of items whose first source was not the frontier; nil is unmeasured.
func verdictH1(suite string, ps PairedStats, firstNotFrontier *float64) Verdict {
	v := Verdict{Hypothesis: "H1", Threshold: "E1 T2", Ticket: verdictTicket, Suite: suite, Arm: hypothesisQuality,
		Gate: true, Estimate: ps.DeltaPass, N: ps.Discordant.N}
	if ps.BootLower95 != nil {
		v.Lower, v.Upper = *ps.BootLower95, *ps.BootUpper95
	}
	switch {
	case firstNotFrontier == nil || *firstNotFrontier < nonVacuityFloor:
		v.Outcome, v.Reason = InconclusiveVacuous, "the first source was the frontier on over 90% of items, or nobody measured it"
	case ps.TangoP == nil || ps.BootLower95 == nil:
		v.Outcome, v.Reason = Inconclusive, fmt.Sprintf("an interval was refused: %v", ps.Refused)
	case *ps.TangoP < alpha && *ps.BootLower95 > -margin && ps.DeltaPass >= pointFloor:
		v.Outcome, v.Reason = Pass, fmt.Sprintf("Tango p=%.4g rejects Δ ≤ −%.0f pts, bootstrap LB %.4g > −%.2g, Δ̂ %.4g ≥ %.2g",
			*ps.TangoP, margin*100, *ps.BootLower95, margin, ps.DeltaPass, pointFloor)
	case ps.DeltaPass < pointFloor || *ps.BootUpper95 < -margin:
		v.Outcome, v.Reason = Fail, fmt.Sprintf("Δ̂ %.4g, bootstrap upper %.4g against −%.2g", ps.DeltaPass, *ps.BootUpper95, margin)
	default:
		v.Outcome, v.Reason = Inconclusive, fmt.Sprintf("Tango p=%.4g (rejects at α=.05: %v), bootstrap LB %.4g (> −%.2g: %v)",
			*ps.TangoP, *ps.TangoP < alpha, *ps.BootLower95, margin, *ps.BootLower95 > -margin)
	}
	v.Reason += "; " + matchCaveat
	return v
}

// verdictH3 is hydra-cost being cheaper per accepted answer without losing 3 pts.
func verdictH3(suite string, ps PairedStats) Verdict {
	v := Verdict{Hypothesis: "H3", Threshold: "E1 T4", Ticket: verdictTicket, Suite: suite, Arm: hypothesisCost,
		Gate: true, N: ps.Discordant.N}
	ci := ps.CostPerAccDiff
	switch {
	case ci == nil || ps.BootLower95 == nil:
		v.Outcome, v.Reason = Inconclusive, fmt.Sprintf("an arm accepted nothing, or an interval was refused: %v", ps.Refused)
	case ci.Upper < 0 && *ps.BootLower95 > h3PassFloor:
		v.Outcome, v.Reason = Pass, fmt.Sprintf("cost per accepted CI [%.4g, %.4g] < 0, Δpass LB %.4g > %.2g", ci.Lower, ci.Upper, *ps.BootLower95, h3PassFloor)
	case ci.Lower >= 0 || *ps.BootUpper95 < h3PassFloor:
		v.Outcome, v.Reason = Fail, fmt.Sprintf("cost per accepted CI [%.4g, %.4g], Δpass upper %.4g", ci.Lower, ci.Upper, *ps.BootUpper95)
	default:
		v.Outcome, v.Reason = Inconclusive, fmt.Sprintf("cost per accepted CI [%.4g, %.4g], Δpass LB %.4g", ci.Lower, ci.Upper, *ps.BootLower95)
	}
	if ci != nil {
		v.Estimate, v.Lower, v.Upper = ci.Estimate, ci.Lower, ci.Upper
	}
	v.Reason += "; " + matchCaveat
	return v
}
