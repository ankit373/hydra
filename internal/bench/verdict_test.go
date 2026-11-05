// SPDX-License-Identifier: MIT

package bench

import (
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/stats"
)

func f64(v float64) *float64 { return &v }

// ps builds paired statistics with the fields the verdict rules read.
func ps(delta, tangoP, bootLo, bootHi float64) PairedStats {
	return PairedStats{Discordant: stats.Discordant{N: 1500}, DeltaPass: delta, TangoP: f64(tangoP),
		BootLower95: f64(bootLo), BootUpper95: f64(bootHi)}
}

// Constructed fixtures reach every H1 branch, in the order the rules are evaluated.
func TestVerdictH1_EveryBranch(t *testing.T) {
	measured := f64(0.4)
	for name, c := range map[string]struct {
		ps     PairedStats
		nonVac *float64
		want   Outcome
		reason string
	}{
		"vacuous":                 {ps(0, 0.001, -0.01, 0.01), f64(0.05), InconclusiveVacuous, "frontier"},
		"unmeasured is vacuous":   {ps(0, 0.001, -0.01, 0.01), nil, InconclusiveVacuous, "nobody measured"},
		"pass":                    {ps(-0.003, 0.004, -0.014, 0.008), measured, Pass, "rejects"},
		"fail by point":           {ps(-0.012, 0.03, -0.019, -0.004), measured, Fail, "Δ̂"},
		"fail by upper bound":     {ps(-0.009, 0.9, -0.05, -0.021), measured, Fail, "upper"},
		"tango and boot disagree": {ps(-0.005, 0.04, -0.022, 0.01), measured, Inconclusive, "rejects at α=.05: true"},
		"refused interval":        {PairedStats{Refused: []string{"x"}}, measured, Inconclusive, "refused"},
	} {
		v := verdictH1("banking77", c.ps, c.nonVac)
		if v.Outcome != c.want || !strings.Contains(v.Reason, c.reason) || !strings.Contains(v.Reason, matchCaveat) {
			t.Errorf("%s: %s (%s), want %s with %q and the match caveat", name, v.Outcome, v.Reason, c.want, c.reason)
		}
		if v.Hypothesis != "H1" || v.Ticket != "#1213" || !v.Gate {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

func TestVerdictH3_EveryBranch(t *testing.T) {
	cheaper := &Interval{Estimate: -0.002, Lower: -0.003, Upper: -0.001}
	for name, c := range map[string]struct {
		ci   *Interval
		ps   PairedStats
		want Outcome
	}{
		"pass":            {cheaper, ps(-0.01, 0, -0.02, 0), Pass},
		"fail on cost":    {&Interval{Lower: 0, Upper: 0.001}, ps(0, 0, -0.01, 0.01), Fail},
		"fail on quality": {cheaper, ps(-0.05, 0, -0.07, -0.04), Fail},
		"inconclusive":    {&Interval{Lower: -0.001, Upper: 0.001}, ps(0, 0, -0.01, 0.01), Inconclusive},
		"accepted none":   {nil, ps(0, 0, -0.01, 0.01), Inconclusive},
	} {
		p := c.ps
		p.CostPerAccDiff = c.ci
		v := verdictH3("clinc150", p)
		if v.Outcome != c.want || v.Reason == "" {
			t.Errorf("%s: %s (%s), want %s", name, v.Outcome, v.Reason, c.want)
		}
	}
	if v := verdictH3("s", PairedStats{CostPerAccDiff: cheaper, BootLower95: f64(0), BootUpper95: f64(0)}); v.Estimate != cheaper.Estimate {
		t.Fatalf("estimate %v, want the point estimate, not a midpoint", v.Estimate)
	}
}

// Verdicts run only on the full tier and only for served arms: a direct arm that is
// merely named hydra-quality is not Hydra.
func TestVerdicts_OnlyFullTierServedArms(t *testing.T) {
	s := Summary{Paired: map[string]PairedStats{
		"hydra-quality-vs-frontier": ps(0, 0.001, -0.01, 0.01),
		"hydra-cost-vs-frontier":    ps(0, 0.001, -0.01, 0.01),
	}}
	run := &Run{Suite: "banking77", Arms: map[string]ArmConfig{
		"hydra-quality": {Kind: "served"}, "hydra-cost": {Kind: "direct"}}}
	got := verdicts(run, s)
	if len(got) != 1 || got[0].Hypothesis != "H1" || got[0].Outcome != InconclusiveVacuous {
		t.Fatalf("verdicts = %+v, want one vacuous H1 for the served arm only", got)
	}
	run.Arms["hydra-cost"] = ArmConfig{Kind: "served"}
	if got := verdicts(run, s); len(got) != 2 || got[1].Hypothesis != "H3" {
		t.Fatalf("verdicts = %+v", got)
	}
	run.Tier = "full"
	full, err := Summarize(run, nil)
	if err != nil || len(full.Verdicts) != 0 {
		t.Fatalf("a full run with no rows has no pairs, so no verdicts: %+v %v", full.Verdicts, err)
	}
}
