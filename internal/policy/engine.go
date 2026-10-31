// SPDX-License-Identifier: MIT

// Package policy evaluates routing rules before a prompt is dispatched.
// Rules are evaluated in order; the first match wins.
package policy

// PII detection lives in pii.go.

// Action is what the engine tells the dispatcher to do.
type Action struct {
	Deny      bool   // block the request entirely
	LocalOnly bool   // only route to heads where LocalOnly=true
	TierCeil  string // do not use tiers above this (e.g. "standard")
	Reason    string // human-readable explanation
}

// Request carries everything the engine needs to make a decision.
type Request struct {
	Prompt   string
	TierHint string // requested tier enum, e.g. "COMPLEX"
	Tags     []string

	// PII, when non-nil, is Prompt's already-computed ContainsPII verdict (see
	// Classify), passing it skips DetectPII's regex scan a second time for
	// content a caller already classified. Nil means "unknown; derive it from
	// Prompt."
	PII *bool
}

// Rule is one conditional policy.
type Rule struct {
	Name      string
	Condition func(Request) bool
	Apply     Action
}

// Engine evaluates an ordered list of rules against a request.
type Engine struct {
	rules []Rule
}

// New constructs an Engine from a slice of rules.
func New(rules []Rule) *Engine { return &Engine{rules: rules} }

// Evaluate returns the first matching Action, or a zero Action (no restriction).
func (e *Engine) Evaluate(req Request) Action {
	for _, r := range e.rules {
		if r.Condition(req) {
			a := r.Apply
			a.Reason = r.Name
			return a
		}
	}
	return Action{}
}

// ── Default rule set ──────────────────────────────────────────────────────────

// DefaultRules returns the baseline policy applied to every dispatch.
// Users extend this by prepending or appending custom rules.
func DefaultRules(localOnlyEnabled bool) []Rule {
	rules := []Rule{}
	if localOnlyEnabled {
		rules = append(rules, Rule{
			Name:      "pii-local-only",
			Condition: ContainsPII,
			Apply:     Action{LocalOnly: true},
		})
	}
	return rules
}
