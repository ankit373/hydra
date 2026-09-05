// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ankit373/hydra/internal/trust"
)

// Confidence is only earned if each sample is independent and discriminating.
// Two silent failures, both already measured by internal/trust: same-family
// heads voting once while reporting several, and sources whose D is ~0.

// weakPowerNats is the diagnostic power below which a source's agreement
// carries no usable information. D is expected |LLR| in nats and is exactly 0
// when se+sp==1 (indistinguishable from guessing); this is a small margin
// above that, not a quality bar.
const weakPowerNats = 0.05

// FamilyRisk is one model family and how correlated its members' answers are.
type FamilyRisk struct {
	Family string `json:"family"`
	// Coupling is the measured excess same-family agreement rate (Jaccard).
	Coupling float64 `json:"coupling"`
	// Critical is trust's own threshold for "effectively one vote".
	Critical bool `json:"critical"`
}

// SourcePower is a source's calibrated ability to discriminate.
type SourcePower struct {
	Source string  `json:"source"`
	Domain string  `json:"domain"`
	D      float64 `json:"d"`
	// Observations excludes the Laplace prior, so 0 means never calibrated,
	// which is not the same finding as measured-and-useless.
	Observations float64 `json:"observations"`
}

// EvidenceQuality is whether the ensemble's confidence rests on independent,
// discriminating evidence.
type EvidenceQuality struct {
	Runs int `json:"runs"`
	// Correlated families, worst first.
	Families []FamilyRisk `json:"families,omitempty"`
	// WeakSources were measured and carry ~no information.
	WeakSources []SourcePower `json:"weakSources,omitempty"`
	// UncalibratedSources were used in a run but have no recorded outcomes,
	// so their contribution rests entirely on the prior. Absence of data, not
	// evidence of weakness, kept separate so the two are never conflated.
	UncalibratedSources []string `json:"uncalibratedSources,omitempty"`
}

// AssessEvidence reports what the confidence in runs rests on. runs is what
// Build already loaded (via trust.LoadRuns), passed in rather than reloaded
// here, since owasp.go's LLM09 check needs the identical data. Every input is
// optional: a machine that has never run an ensemble gets an empty result,
// not an invented one.
func AssessEvidence(runs []trust.RunLog) EvidenceQuality {
	var q EvidenceQuality
	q.Runs = len(runs)

	// Which sources actually carried a vote, a weak source nobody uses is
	// not a finding.
	used := map[string]bool{}
	for _, r := range runs {
		for _, m := range r.Models {
			used[m] = true
		}
	}

	coPath := trust.DefaultCoAgreementPath()
	coupling := trust.AllFamilyCoupling(coPath)
	for _, fam := range trust.KnownFamilies(coPath) {
		if r := coupling[fam]; r.Warn {
			q.Families = append(q.Families, FamilyRisk{Family: fam, Coupling: r.J, Critical: true})
		}
	}
	sort.Slice(q.Families, func(i, j int) bool { return q.Families[i].Coupling > q.Families[j].Coupling })

	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		return q
	}
	calibrated := map[string]bool{}
	for _, st := range cal.Report() {
		if st.N <= 0 {
			continue // prior-only: no measurement to judge
		}
		calibrated[st.Source] = true
		if !used[st.Source] {
			continue
		}
		if st.D < weakPowerNats {
			q.WeakSources = append(q.WeakSources, SourcePower{
				Source: st.Source, Domain: st.Domain, D: st.D, Observations: st.N,
			})
		}
	}
	for src := range used {
		if !calibrated[src] {
			q.UncalibratedSources = append(q.UncalibratedSources, src)
		}
	}
	sort.Strings(q.UncalibratedSources)
	sort.Slice(q.WeakSources, func(i, j int) bool { return q.WeakSources[i].D < q.WeakSources[j].D })
	return q
}

// evidenceCheck reports what the reported confidence is actually built on.
func evidenceCheck(q EvidenceQuality) Check {
	const name = "Confidence evidence quality"
	if q.Runs == 0 {
		return Check{Name: name, Status: "no ensemble runs",
			Detail: "hyctl dispatch --confidence has never been used, so no confidence has been claimed"}
	}
	var problems []string
	if n := len(q.Families); n > 0 {
		problems = append(problems, fmt.Sprintf("%d correlated family(ies)", n))
	}
	if n := len(q.WeakSources); n > 0 {
		problems = append(problems, fmt.Sprintf("%d undiagnostic source(s)", n))
	}
	if len(problems) == 0 {
		detail := fmt.Sprintf("%d run(s); no correlated families and no measured-undiagnostic sources", q.Runs)
		if n := len(q.UncalibratedSources); n > 0 {
			detail += fmt.Sprintf(", %d source(s) are uncalibrated, so their weight rests on the prior", n)
		}
		return Check{Name: name, Status: "no correlation detected", Detail: detail}
	}
	return Check{Name: name, Status: strings.Join(problems, ", "),
		Detail: evidenceDetail(q)}
}

func evidenceDetail(q EvidenceQuality) string {
	var parts []string
	for _, f := range q.Families {
		parts = append(parts, fmt.Sprintf("%s heads agree %.0f%% beyond chance, they vote as one",
			f.Family, f.Coupling*100))
	}
	for _, s := range q.WeakSources {
		parts = append(parts, fmt.Sprintf("%s has D=%.3f nats over %.0f outcomes, its agreement carries ~no information",
			s.Source, s.D, s.Observations))
	}
	return strings.Join(parts, "; ")
}
