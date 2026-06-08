// Package policy evaluates routing rules before a prompt is dispatched.
// Rules are evaluated in order; the first match wins.
package policy

import (
	"regexp"
)

// piiPatterns are compiled once at package init to avoid per-call allocations.
var piiPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b\d{3}[-.\s]?\d{2}[-.\s]?\d{4}\b`),                              // SSN
	regexp.MustCompile(`\b\d{4}[- ]?\d{4}[- ]?\d{4}[- ]?\d{4}\b`),                        // credit card
	regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`),           // email (fixed: [A-Za-z] not [A-Z|a-z])
	regexp.MustCompile(`(?i)\b(password|secret|api[-_]?key|token|private[-_]?key)\s*[:=]\s*\S+`), // credentials
	regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`),                         // IP address
}

// Action is what the engine tells the dispatcher to do.
type Action struct {
	Deny       bool   // block the request entirely
	LocalOnly  bool   // only route to heads where LocalOnly=true
	TierCeil   string // do not use tiers above this (e.g. "standard")
	Reason     string // human-readable explanation
}

// Request carries everything the engine needs to make a decision.
type Request struct {
	Prompt   string
	TierHint string // requested tier enum, e.g. "COMPLEX"
	Tags     []string
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

// ── Built-in conditions ───────────────────────────────────────────────────────

// ContainsPII returns true if the prompt likely contains personally identifiable
// information. This is a fast heuristic; replace with Presidio via sidecar for
// production-grade detection.
func ContainsPII(req Request) bool {
	for _, p := range piiPatterns {
		if p.MatchString(req.Prompt) {
			return true
		}
	}
	return false
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
