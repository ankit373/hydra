// SPDX-License-Identifier: MIT

package security

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/trust"
)

// The spine: every analysis in this package converts into a Risk, so findings
// are one kind of object — identified, rated, aged against an SLA, quantified,
// crosswalked, evidenced. Nothing new is detected here.

// RiskClass is where a risk came from, which also drives its framework
// crosswalk and how its exposure is priced.
type RiskClass string

const (
	ClassExposure    RiskClass = "exposure"     // sensitive data reached a remote head
	ClassIncident    RiskClass = "incident"     // a correlated attack sequence
	ClassControl     RiskClass = "control"      // a control that cannot fire
	ClassPolicy      RiskClass = "policy"       // fail-open, dead or unreachable rules
	ClassSupplyChain RiskClass = "supply-chain" // a head binary changed
	ClassCoverage    RiskClass = "coverage"     // an uncovered OWASP category
	ClassEvidence    RiskClass = "evidence"     // tamper-evidence or confidence quality
)

// RiskStatus is where a risk sits in its lifecycle.
type RiskStatus string

const (
	StatusOpen      RiskStatus = "open"
	StatusAccepted  RiskStatus = "accepted"
	StatusMitigated RiskStatus = "mitigated"
)

// slaDays is the remediation window per severity, in days.
//
// These are the widely-published vulnerability-management targets (critical
// within a week, high within a month, medium within a quarter) rather than
// numbers chosen here. They are the clock only — this register deliberately
// assigns no owner, so a breach says "this is late", never "you are late".
var slaDays = map[Severity]int{
	SeverityCritical: 7,
	SeverityHigh:     30,
	SeverityMedium:   90,
	SeverityLow:      180,
}

// FrameworkRef is one control in one external framework that a risk bears on.
type FrameworkRef struct {
	Framework string `json:"framework"`
	Control   string `json:"control"`
	// Curated marks this as a hand-maintained mapping rather than something
	// derived from the data. A crosswalk is an assertion about standards, and
	// presenting an assertion as a measurement is the failure mode this whole
	// dashboard exists to avoid.
	Curated bool `json:"curated"`
}

// Risk is one governed finding.
type Risk struct {
	ID       string     `json:"id"`
	Class    RiskClass  `json:"class"`
	Title    string     `json:"title"`
	Detail   string     `json:"detail"`
	Severity Severity   `json:"severity"`
	Status   RiskStatus `json:"status"`

	// FirstSeen/AgeDays come from persisted history where one exists (coverage
	// gaps carry a real first-seen date); otherwise the age is 0 and the SLA
	// clock starts now rather than pretending to know.
	FirstSeen string `json:"firstSeen,omitempty"`
	AgeDays   int    `json:"ageDays"`
	// DueInDays is negative once the SLA window has passed.
	DueInDays int  `json:"dueInDays"`
	Breached  bool `json:"breached"`

	// Cost of ONE defect of this class, from internal/trust's defect model so
	// dashboard and router agree. Not "exposure": per-occurrence, not
	// annualised — an annual figure needs a frequency nothing here measures.
	DefectCostUSD float64        `json:"defectCostUsd"`
	Frameworks    []FrameworkRef `json:"frameworks,omitempty"`
	Evidence      []string       `json:"evidence,omitempty"`
}

// RiskRegister is the whole programme in one object.
type RiskRegister struct {
	Risks []Risk `json:"risks"`
	// SumDefectCostUSD adds the per-defect costs of open risks. It is a
	// magnitude, not a forecast — see Risk.DefectCostUSD.
	SumDefectCostUSD float64          `json:"sumDefectCostUsd"`
	Breached         int              `json:"breached"`
	BySeverity       map[Severity]int `json:"bySeverity"`
}

// BuildRegister converts every analysis in this report into governed risks.
func BuildRegister(r *Report, now time.Time) RiskRegister {
	var out []Risk

	// Exposure: sensitive data that actually left the machine.
	if n := ConfirmedRemote(r.Exposures); n > 0 {
		out = append(out, newRisk(ClassExposure, SeverityCritical,
			fmt.Sprintf("%d sensitive access(es) reached a remote head", n),
			exposureSummary(r.Exposures), "", now,
			trust.Task{TouchesPII: true, Production: true, Irreversible: true},
			evidenceFor(r.Exposures)))
	}

	// Incidents: correlated attack sequences carry their own rating already.
	for _, in := range r.Incidents {
		out = append(out, newRisk(ClassIncident, in.Severity, in.Narrative,
			fmt.Sprintf("%d event(s) from %s to %s; stages: %s",
				len(in.Events), in.Start, in.End, stagesText(in.Stages)),
			in.Start, now,
			trust.Task{
				TouchesPII:   hasStage(in, StageSucceeded),
				Irreversible: hasStage(in, StageAuditTampering),
				Production:   hasStage(in, StageEscalation),
			},
			[]string{in.ID}))
	}

	// Controls that cannot fire: protection believed to exist.
	for _, c := range r.Controls {
		if c.Declared && !c.Wired {
			out = append(out, newRisk(ClassControl, SeverityHigh,
				c.Name+" is declared but never runs", c.Detail, "", now,
				trust.Task{Production: true}, nil))
		}
	}

	// Policy defects.
	if r.PolicyAudit.FailOpen && len(r.PolicyAudit.Rules) > 0 {
		out = append(out, newRisk(ClassPolicy, SeverityMedium,
			"Access policy is fail-open",
			"anything no rule names is allowed; set \"default\": \"deny\" to invert that", "", now,
			trust.Task{Production: true}, nil))
	}
	if n := r.PolicyAudit.ShadowedCount(); n > 0 {
		out = append(out, newRisk(ClassPolicy, SeverityMedium,
			fmt.Sprintf("%d policy rule(s) are unreachable", n),
			"an earlier rule always matches first, so these can never fire", "", now,
			trust.Task{}, nil))
	}

	// Supply chain: a head binary moved under us.
	for _, b := range r.SupplyChain.Binaries {
		if b.Changed {
			out = append(out, newRisk(ClassSupplyChain, SeverityHigh,
				fmt.Sprintf("%s binary changed since it was last seen", b.HeadID),
				b.Path+" — an upgrade and a swap look identical here", "", now,
				trust.Task{Irreversible: true, Production: true}, []string{b.Path}))
		}
	}

	// Tamper-evidence and confidence quality.
	if !r.IntegrityIntact {
		out = append(out, newRisk(ClassEvidence, SeverityCritical,
			"The audit ledger was modified after recording",
			"every other finding rests on this log, so none of them can be trusted", "", now,
			trust.Task{Irreversible: true, Production: true}, nil))
	}
	for _, f := range r.Evidence.Families {
		out = append(out, newRisk(ClassEvidence, SeverityMedium,
			fmt.Sprintf("%s heads vote as one", f.Family),
			fmt.Sprintf("they agree %.0f%% beyond chance, so ensemble confidence is overstated", f.Coupling*100),
			"", now, trust.Task{}, nil))
	}

	// Coverage gaps carry a real persisted age, so their SLA clock is honest.
	for _, c := range r.Coverage.Categories {
		if c.Status != Gap {
			continue
		}
		out = append(out, newRisk(ClassCoverage, coverageSeverity(c),
			fmt.Sprintf("%s %s is uncovered", c.ID, c.Name), c.Detail,
			c.GapSince, now, trust.Task{}, []string{c.ID}))
	}

	reg := RiskRegister{BySeverity: map[Severity]int{}}
	for _, k := range out {
		k.Frameworks = crosswalk(k.Class)
		reg.Risks = append(reg.Risks, k)
		reg.BySeverity[k.Severity]++
		if k.Status == StatusOpen {
			reg.SumDefectCostUSD += k.DefectCostUSD
		}
		if k.Breached {
			reg.Breached++
		}
	}
	// Worst first, then most overdue — the order an operator works down.
	sort.SliceStable(reg.Risks, func(i, j int) bool {
		si, sj := severityRank(reg.Risks[i].Severity), severityRank(reg.Risks[j].Severity)
		if si != sj {
			return si < sj
		}
		return reg.Risks[i].DueInDays < reg.Risks[j].DueInDays
	})
	return reg
}

// newRisk stamps identity, the SLA clock and the priced exposure.
func newRisk(class RiskClass, sev Severity, title, detail, firstSeen string,
	now time.Time, task trust.Task, evidence []string) Risk {

	k := Risk{
		Class: class, Severity: sev, Title: title, Detail: detail,
		Status: StatusOpen, FirstSeen: firstSeen, Evidence: evidence,
	}
	// A stable ID so the same risk keeps its identity between runs.
	sum := sha256.Sum256([]byte(string(class) + "|" + title))
	k.ID = "R-" + hex.EncodeToString(sum[:])[:8]

	if firstSeen != "" {
		if t, err := time.Parse(time.RFC3339, firstSeen); err == nil {
			k.AgeDays = int(now.Sub(t).Hours() / 24)
		}
	}
	k.DueInDays = slaDays[sev] - k.AgeDays
	k.Breached = k.DueInDays < 0

	// Price it with the model that already governs routing, so the dashboard
	// and the router cannot disagree about what a defect costs.
	m := trust.NewDefectModel()
	k.DefectCostUSD = m.CostUSD(task)
	return k
}

// coverageSeverity rates an uncovered category by how long it has been open,
// reusing the aging buckets the action queue already established.
func coverageSeverity(c Category) Severity {
	switch {
	case c.GapAgeDays >= gapStaleDays:
		return SeverityHigh
	case c.GapAgeDays >= gapAgingDays:
		return SeverityMedium
	default:
		return SeverityLow
	}
}

func hasStage(in Incident, s Stage) bool {
	for _, v := range in.Stages {
		if v == s {
			return true
		}
	}
	return false
}

func stagesText(ss []Stage) string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, string(s))
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

func evidenceFor(exps []Exposure) []string {
	var out []string
	for _, e := range exps {
		if e.Remote && e.Known {
			out = append(out, e.Head+":"+e.Resource)
		}
	}
	return out
}
