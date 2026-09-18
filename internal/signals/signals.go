// SPDX-License-Identifier: MIT

// Package signals turns a dispatch into named, typed facts, and evaluates
// priority-ordered rules over them.
//
// It exists so a new routing input is a signal and a rule rather than another
// branch in selectHeads. Two inputs were hardcoded there and a third had
// nowhere to go; that is how internal/config's CapScore bands became a second
// routing table that disagreed with the first (#782).
package signals

import (
	"strings"

	"github.com/ankit373/hydra/internal/policy"
)

// Set is the value of every signal for one dispatch.
type Set map[string]any

// Input is everything a dispatch knows that a signal might be derived from.
//
// Already-resolved rather than lazily fetched, so this package imports neither
// internal/graph nor internal/trust and a test needs no fixtures for either.
type Input struct {
	Prompt string

	// BlastRadius is the transitive dependent count for the file this dispatch
	// acts on. Nil when there is no file or no code graph: a file with zero
	// dependents is a real reading, and a nil that became 0 would be
	// indistinguishable from it.
	BlastRadius *int

	// Calibrated says whether internal/trust has evidence for this domain.
	// Nil when the calibration store could not be read at all.
	Calibrated *bool
}

// Keyword is a named set of phrases, declared in signals.yaml, that becomes
// the signal keyword.<name>.matched.
type Keyword struct {
	Name string   `yaml:"name"`
	Any  []string `yaml:"any"`
}

// Normalize turns a human name into an identifier the expression language can
// hold: lowercase, with runs of anything else collapsed to one underscore.
//
// The single derivation. policy's detector names carry spaces ("aws access key
// id"), so the schema and the collector must agree on the spelling, and a
// normalization applied on one side only makes the two halves disagree about a
// key that then matches nothing.
func Normalize(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

// Signal names that do not depend on what the rules file declares.
const (
	SigPIIAny          = "pii.any"
	SigInjection       = "injection.matched"
	SigBlastRadius     = "graph.blast_radius"
	SigTrustCalibrated = "trust.calibrated"
)

// PIISignal is the signal name for one detector.
func PIISignal(detector string) string { return "pii." + Normalize(detector) + ".matched" }

// KeywordSignal is the signal name for one declared keyword set.
func KeywordSignal(name string) string { return "keyword." + Normalize(name) + ".matched" }

// SchemaFor declares every signal a rule may name: the fixed ones, one per PII
// detector, and one per keyword set the file itself declares.
func SchemaFor(keywords []Keyword) Schema {
	sc := Schema{
		SigPIIAny:          KindBool,
		SigInjection:       KindBool,
		SigBlastRadius:     KindNumber,
		SigTrustCalibrated: KindBool,
	}
	for _, d := range policy.DetectorNames() {
		sc[PIISignal(d)] = KindBool
	}
	for _, k := range keywords {
		sc[KeywordSignal(k.Name)] = KindBool
	}
	return sc
}

// Collect computes every signal's value for one dispatch.
//
// Pure and run once per dispatch, like policy.Classify: the result is reused
// across fallback candidates rather than recomputed per head, or a rule could
// decide differently for two candidates of the same task.
func Collect(in Input, keywords []Keyword) Set {
	vals := Set{}

	req := policy.Request{Prompt: in.Prompt}
	hits := policy.DetectPII(req)
	for _, d := range policy.DetectorNames() {
		vals[PIISignal(d)] = false
	}
	for _, h := range hits {
		vals[PIISignal(h)] = true
	}
	vals[SigPIIAny] = len(hits) > 0

	_, injected := policy.InjectionMarker(req)
	vals[SigInjection] = injected

	// Absent rather than zero when unknown: see Input.BlastRadius.
	if in.BlastRadius != nil {
		vals[SigBlastRadius] = float64(*in.BlastRadius)
	}
	if in.Calibrated != nil {
		vals[SigTrustCalibrated] = *in.Calibrated
	}

	lower := strings.ToLower(in.Prompt)
	for _, k := range keywords {
		matched := false
		for _, phrase := range k.Any {
			if p := strings.ToLower(strings.TrimSpace(phrase)); p != "" && strings.Contains(lower, p) {
				matched = true
				break
			}
		}
		vals[KeywordSignal(k.Name)] = matched
	}
	return vals
}
