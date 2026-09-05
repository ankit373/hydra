// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ankit373/hydra/internal/ledger"
)

// Counts hide the sequence. "4 denied, 2 flagged" and "injection → recon →
// escalation → an attempt on the audit trail" are the same rows read twice;
// only the second is an incident, and only it keeps a *succeeded* flag visible.

// sessionGap is the lull that splits one actor's events into separate
// incidents. It is also the evasion window, which incidentCheck states rather
// than implying coverage it does not have. Widening it only moves the gap.
const sessionGap = time.Hour

// Stage is one observed step in an incident, each derived from a field the
// ledger already records, not an invented taxonomy.
type Stage string

const (
	// StageInjection: the content carried a known injection marker.
	StageInjection Stage = "injection"
	// StageRecon: the same resource was denied more than once.
	StageRecon Stage = "recon"
	// StageEscalation: an exec or network action was attempted.
	StageEscalation Stage = "escalation"
	// StageAuditTampering: the target was Hydra's own audit or policy machinery.
	StageAuditTampering Stage = "audit-tampering"
	// StageSucceeded: a flagged attempt was allowed rather than denied.
	// Blocked and landed are not the same event and must not score alike.
	StageSucceeded Stage = "succeeded"
)

// Severity buckets an incident by OWASP Risk Rating.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// Incident is one actor's correlated sequence of risky events.
type Incident struct {
	ID    string `json:"id"`
	Actor string `json:"actor"`
	Agent string `json:"agent,omitempty"`
	Start string `json:"start"`
	End   string `json:"end"`

	Stages []Stage `json:"stages"`
	// Likelihood and Impact are the OWASP Risk Rating factors, 0-9 each, kept
	// separate so the severity can be argued with rather than just believed.
	Likelihood int      `json:"likelihood"`
	Impact     int      `json:"impact"`
	Severity   Severity `json:"severity"`
	// Narrative is the one sentence a human actually reads.
	Narrative string `json:"narrative"`
	// Events is the evidence, oldest first.
	Events []ledger.Event `json:"events"`
}

// auditPaths are the substrings that mark a resource as Hydra's own
// accountability machinery. Deliberately narrow: the ledger and its anchor,
// the access policy, and the packages that implement them. An agent attacking
// the record of what it did is a categorically different signal from an agent
// touching application code.
var auditPaths = []string{
	"mcp_ledger.jsonl",
	".chainhash",
	"mcp_policy.json",
	"internal/ledger",
	"internal/security",
	"internal/policy",
}

// Matches path structure, not substrings: strings.Contains scored
// "notinternal/security-notes.txt" as audit tampering, inflating severity.
// Pattern shape picks the rule: ".x" suffix, "a/b" segments, else file name.
func targetsAuditMachinery(resource string) bool {
	if resource == "" {
		return false
	}
	clean := path.Clean(strings.ReplaceAll(resource, `\`, "/"))
	base := path.Base(clean)
	for _, p := range auditPaths {
		switch {
		case strings.HasPrefix(p, "."):
			if strings.HasSuffix(base, p) {
				return true
			}
		case strings.Contains(p, "/"):
			if segmentMatch(clean, p) {
				return true
			}
		default:
			if base == p || strings.HasPrefix(base, p+".") {
				return true
			}
		}
	}
	return false
}

// segmentMatch reports whether pattern appears in resource on segment
// boundaries: "internal/ledger" matches "internal/ledger/ledger.go" and
// "/opt/hydra/internal/ledger", but not "notinternal/ledger-notes".
func segmentMatch(resource, pattern string) bool {
	segs := strings.Split(resource, "/")
	pat := strings.Split(pattern, "/")
	for i := 0; i+len(pat) <= len(segs); i++ {
		if slices.Equal(segs[i:i+len(pat)], pat) {
			return true
		}
	}
	return false
}

// riskyEvent is the population incidents are built from: something was blocked
// or something was flagged. An ordinary allowed access is not an incident.
func riskyEvent(e ledger.Event) bool {
	return e.Decision == ledger.Deny || e.Flagged
}

// CorrelateIncidents groups risky events per actor into time-windowed
// incidents and classifies each sequence.
func CorrelateIncidents(events []ledger.Event, blast BlastReport) []Incident {
	byActor := map[string][]ledger.Event{}
	for _, e := range events {
		if !riskyEvent(e) {
			continue
		}
		byActor[e.Tool] = append(byActor[e.Tool], e)
	}

	// Built once and shared by every incident's scoreImpact call below, instead
	// of each one linearly scanning all of blast.Files per event to find a
	// match, O(events_in_incident × len(blast.Files)) repeated once per
	// incident, when blast.Files is already fully built and doesn't change
	// within one CorrelateIncidents call.
	widelyDepended := widelyDependedFiles(blast)

	var out []Incident
	for actor, evs := range byActor {
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS < evs[j].TS })

		group := []ledger.Event{evs[0]}
		for _, e := range evs[1:] {
			if withinSession(group[len(group)-1].TS, e.TS) {
				group = append(group, e)
				continue
			}
			out = append(out, buildIncident(actor, group, widelyDepended))
			group = []ledger.Event{e}
		}
		out = append(out, buildIncident(actor, group, widelyDepended))
	}

	// Worst first: an operator reads top-down and stops when they run out of
	// time, so the order has to carry the priority.
	sort.Slice(out, func(i, j int) bool {
		si, sj := severityRank(out[i].Severity), severityRank(out[j].Severity)
		if si != sj {
			return si < sj
		}
		return out[i].Start > out[j].Start // newer first within a severity
	})
	return out
}

// withinSession reports whether b follows a closely enough to be the same
// incident. An unparseable timestamp keeps the events together rather than
// splitting on a formatting problem.
func withinSession(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return true
	}
	return tb.Sub(ta) <= sessionGap
}

// widelyDependedFiles indexes blast.Files by path, keeping only the files
// scoreImpact actually cares about (known to the graph and depended on by at
// least one other file), an O(1) lookup by e.Resource replaces a linear scan
// of the whole slice per event.
func widelyDependedFiles(blast BlastReport) map[string]bool {
	out := make(map[string]bool, len(blast.Files))
	for _, f := range blast.Files {
		if f.Known && f.Dependents > 0 {
			out[f.File] = true
		}
	}
	return out
}

func buildIncident(actor string, evs []ledger.Event, widelyDepended map[string]bool) Incident {
	in := Incident{
		Actor:  actor,
		Start:  evs[0].TS,
		End:    evs[len(evs)-1].TS,
		Events: evs,
	}
	for _, e := range evs {
		if e.Agent != "" {
			in.Agent = e.Agent
			break
		}
	}
	in.ID = fmt.Sprintf("%s@%s", actor, in.Start)

	deniedPerResource := map[string]int{}
	seen := map[Stage]bool{}
	for _, e := range evs {
		if e.Flagged {
			seen[StageInjection] = true
			// A flagged attempt that was *allowed* is the one that actually
			// got through, the single most important distinction here.
			if e.Decision == ledger.Allow {
				seen[StageSucceeded] = true
			}
		}
		if e.Decision == ledger.Deny && e.Resource != "" {
			deniedPerResource[e.Resource]++
			if deniedPerResource[e.Resource] >= probeThreshold {
				seen[StageRecon] = true
			}
		}
		if e.Action == ledger.Exec || e.Action == ledger.Network {
			seen[StageEscalation] = true
		}
		if targetsAuditMachinery(e.Resource) {
			seen[StageAuditTampering] = true
		}
	}
	for _, s := range []Stage{StageInjection, StageRecon, StageEscalation, StageAuditTampering, StageSucceeded} {
		if seen[s] {
			in.Stages = append(in.Stages, s)
		}
	}

	in.Likelihood = scoreLikelihood(evs, seen)
	in.Impact = scoreImpact(evs, seen, widelyDepended)
	in.Severity = rateSeverity(in.Likelihood, in.Impact)
	in.Narrative = narrate(in)
	return in
}

// scoreLikelihood is the OWASP Risk Rating likelihood factor, 0-9: how much
// evidence there is that this is deliberate rather than incidental.
func scoreLikelihood(evs []ledger.Event, seen map[Stage]bool) int {
	score := 0
	// A sequence with several distinct stages is a pattern, not a slip.
	score += 2 * len(seen)
	// Volume, capped so a noisy day cannot dominate the shape.
	switch n := len(evs); {
	case n >= 8:
		score += 3
	case n >= 4:
		score += 2
	case n >= 2:
		score += 1
	}
	// Something actually got through.
	if seen[StageSucceeded] {
		score += 2
	}
	return clamp09(score)
}

// scoreImpact is the OWASP Risk Rating impact factor, 0-9: how much damage the
// attempted operations could do.
func scoreImpact(evs []ledger.Event, seen map[Stage]bool, widelyDepended map[string]bool) int {
	score := 0
	// Action class. Executing or reaching the network dominates reading.
	for _, e := range evs {
		switch e.Action {
		case ledger.Exec, ledger.Network:
			score = maxInt(score, 5)
		case ledger.Write:
			score = maxInt(score, 3)
		case ledger.Read:
			score = maxInt(score, 2)
		}
	}
	// Attacking the record of what happened is worse than the act recorded.
	if seen[StageAuditTampering] {
		score += 3
	}
	// A touched file the graph knows to be widely depended on raises the reach.
	for _, e := range evs {
		if widelyDepended[e.Resource] {
			score += 1
		}
	}
	return clamp09(score)
}

// rateSeverity buckets likelihood × impact the way the OWASP Risk Rating
// methodology does: both factors are banded low/medium/high, and the pair
// selects the severity.
func rateSeverity(likelihood, impact int) Severity {
	l, i := band(likelihood), band(impact)
	switch {
	case l == 2 && i == 2:
		return SeverityCritical
	case l+i >= 3:
		return SeverityHigh
	case l+i >= 2:
		return SeverityMedium
	default:
		return SeverityLow
	}
}

// band maps a 0-9 factor onto OWASP's low(0)/medium(1)/high(2) levels.
func band(v int) int {
	switch {
	case v >= 6:
		return 2
	case v >= 3:
		return 1
	default:
		return 0
	}
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityHigh:
		return 1
	case SeverityMedium:
		return 2
	default:
		return 3
	}
}

// narrate writes the sentence an operator reads instead of the row dump.
func narrate(in Incident) string {
	has := map[Stage]bool{}
	for _, s := range in.Stages {
		has[s] = true
	}
	var beats []string
	if has[StageInjection] {
		if has[StageSucceeded] {
			beats = append(beats, "an injection marker was present and the request was allowed")
		} else {
			beats = append(beats, "an injection marker was present")
		}
	}
	if has[StageRecon] {
		beats = append(beats, "the same resource was denied repeatedly")
	}
	if has[StageEscalation] {
		beats = append(beats, "it escalated to an exec/network action")
	}
	if has[StageAuditTampering] {
		beats = append(beats, "it targeted the audit trail itself")
	}
	if len(beats) == 0 {
		beats = append(beats, fmt.Sprintf("%d access(es) were blocked", len(in.Events)))
	}
	return fmt.Sprintf("%s: %s.", in.Actor, strings.Join(beats, ", then "))
}

func clamp09(v int) int {
	if v < 0 {
		return 0
	}
	if v > 9 {
		return 9
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TopIncidents returns at most n incidents, worst first.
func TopIncidents(in []Incident, n int) []Incident {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

// incidentCheck summarises the incident view for the checks list.
func incidentCheck(in []Incident) Check {
	const name = "Correlated incidents"
	if len(in) == 0 {
		return Check{Name: name, Status: "none",
			Detail: "no denied or flagged access has been recorded, so there is no sequence to correlate"}
	}
	worst := in[0]
	return Check{Name: name, Status: fmt.Sprintf("%d incident(s), worst %s", len(in), worst.Severity),
		Detail: fmt.Sprintf("%s (events by one actor are correlated across gaps of up to %s; "+
			"steps paced wider than that are not linked)", worst.Narrative, sessionGap)}
}
