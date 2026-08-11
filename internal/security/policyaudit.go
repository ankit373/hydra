// SPDX-License-Identifier: MIT

package security

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ankit373/hydra/internal/ledger"
)

// This file replaced a "policy adherence %" metric that measured the wrong
// thing: it reported what fraction of accesses matched an explicit rule, so a
// policy with no rules scored 0% (reading as failure when the truth is "no
// policy exists") and a single {"resource":"*","decision":"allow"} rule scored
// 100% while permitting everything — the number improved as the policy got
// more permissive. What follows answers the questions an operator actually has
// about a policy: is it fail-open, which rules never fire, and which can never
// fire at all.

// RuleStat is one policy rule and what the ledger says about it.
type RuleStat struct {
	Index    int    `json:"index"`
	Summary  string `json:"summary"`
	Decision string `json:"decision"`
	// Hits counts events whose Reason named this rule index.
	Hits int `json:"hits"`
	// Dead is a rule that has never matched anything recorded.
	Dead bool `json:"dead"`
	// ShadowedBy is the index of an earlier rule that provably always matches
	// first, making this one unreachable. nil when not shadowed.
	ShadowedBy *int `json:"shadowedBy,omitempty"`
}

// PolicyAudit is the real analysis of the loaded access policy.
type PolicyAudit struct {
	Rules   []RuleStat `json:"rules"`
	Default string     `json:"default"`
	// FailOpen is true when the default decision is Allow: anything no rule
	// names is permitted. Not automatically wrong — it is Hydra's shipped
	// default — but it is the single most consequential line in a policy and
	// belongs on the dashboard rather than buried in a JSON file.
	FailOpen bool `json:"failOpen"`
	// DefaultHits counts accesses that fell through every rule.
	DefaultHits int `json:"defaultHits"`
	// Evaluated is the number of events that went through Policy.Decide at
	// all — the denominator for the two counts above.
	Evaluated int `json:"evaluated"`
}

// DeadCount and ShadowedCount summarise the audit for a one-line readout.
func (a PolicyAudit) DeadCount() int {
	n := 0
	for _, r := range a.Rules {
		if r.Dead {
			n++
		}
	}
	return n
}

func (a PolicyAudit) ShadowedCount() int {
	n := 0
	for _, r := range a.Rules {
		if r.ShadowedBy != nil {
			n++
		}
	}
	return n
}

// AuditPolicy analyses the loaded policy against recorded events.
func AuditPolicy(pol ledger.Policy, events []ledger.Event) PolicyAudit {
	def := pol.Default
	if def == "" {
		def = ledger.Allow // Policy.Decide treats a zero Default as Allow
	}
	a := PolicyAudit{
		Default:  string(def),
		FailOpen: def == ledger.Allow,
		Rules:    make([]RuleStat, 0, len(pol.Rules)),
	}

	hits := map[int]int{}
	for _, e := range events {
		switch {
		case e.Reason == "default":
			a.DefaultHits++
			a.Evaluated++
		default:
			if idx, ok := ruleIndex(e.Reason); ok {
				hits[idx]++
				a.Evaluated++
			}
			// Any other Reason (an `mcp record` event, an unhashable-params
			// denial) never went through Decide, so it is not part of this
			// population and must not skew either count.
		}
	}

	for i, r := range pol.Rules {
		st := RuleStat{
			Index:    i,
			Summary:  ruleSummary(r),
			Decision: string(r.Decision),
			Hits:     hits[i],
			Dead:     hits[i] == 0,
		}
		if by, ok := shadowedBy(pol.Rules, i); ok {
			st.ShadowedBy = &by
		}
		a.Rules = append(a.Rules, st)
	}
	return a
}

// ruleIndex parses the leading index out of Policy.Decide's reason string,
// which has the shape "rule 3 (deny fs/etc/*)".
func ruleIndex(reason string) (int, bool) {
	const prefix = "rule "
	if !strings.HasPrefix(reason, prefix) {
		return 0, false
	}
	rest := reason[len(prefix):]
	end := strings.IndexByte(rest, ' ')
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

func ruleSummary(r ledger.Rule) string {
	f := func(s string) string {
		if s == "" {
			return "*"
		}
		return s
	}
	return fmt.Sprintf("%s %s/%s %s", f(string(r.Action)), f(r.Tool), f(r.Resource), f(r.Agent))
}

// shadowedBy reports the index of an earlier rule that makes rule i
// unreachable.
//
// The test is deliberately *sufficient rather than complete*: an earlier rule
// shadows this one only when every one of its fields is provably at least as
// general — empty/"*" (matches anything) or exactly equal. It will miss
// shadows expressible only through glob subsumption ("a/**" covering "a/b"),
// and that is the right direction to be wrong in: a security tool that invents
// a policy bug is worse than one that misses a subtle one, so this never
// reports a shadow that is not real.
func shadowedBy(rules []ledger.Rule, i int) (int, bool) {
	b := rules[i]
	for j := 0; j < i; j++ {
		a := rules[j]
		if covers(a.Agent, b.Agent) &&
			covers(a.Tool, b.Tool) &&
			covers(a.Resource, b.Resource) &&
			covers(string(a.Action), string(b.Action)) &&
			covers(a.Classification, b.Classification) {
			return j, true
		}
	}
	return 0, false
}

// covers reports whether pattern a matches everything pattern b does, by the
// only two relations that are certain without glob analysis: a is a wildcard,
// or the two are identical.
func covers(a, b string) bool { return a == "" || a == "*" || a == b }
