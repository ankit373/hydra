// SPDX-License-Identifier: MIT

package decision

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// optionText is an option as one line of text, for a dialect that has no separate description.
func optionText(o Option) string {
	if o.Description == "" {
		return o.Name
	}
	return o.Name + ": " + o.Description
}

// keyedProb is one reported probability, keyed by the outcome it was reported for.
type keyedProb struct {
	key string
	p   float64
}

// aligned orders a keyed distribution by keys, refusing one that names a
// different set of outcomes than were asked about, or names one twice.
func aligned(probs []keyedProb, keys []string) ([]float64, error) {
	byKey := make(map[string]float64, len(probs))
	for _, kp := range probs {
		// A repeat would overwrite, hiding mass that Check must see.
		if _, seen := byKey[kp.key]; seen {
			return nil, fmt.Errorf("gave %q more than one probability", kp.key)
		}
		byKey[kp.key] = kp.p
	}
	if len(byKey) != len(keys) {
		return nil, fmt.Errorf("answered %d outcomes for %d asked", len(byKey), len(keys))
	}
	out := make([]float64, len(keys))
	for i, k := range keys {
		p, ok := byKey[k]
		if !ok {
			return nil, fmt.Errorf("gave no probability for %q", k)
		}
		out[i] = p
	}
	return out, nil
}

// names are a question's option names, which key a choice's probabilities.
func names(opts []Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Name
	}
	return out
}

// levelKeys are "0".."n-1", which key a score's probabilities.
func levelKeys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(i)
	}
	return out
}

// predicateAnswer turns P(true) into the two-outcome distribution a predicate reports.
func predicateAnswer(pTrue *float64) (Answer, error) {
	if pTrue == nil {
		return Answer{}, errors.New("gave no probability of true")
	}
	return Answer{Probabilities: []float64{*pTrue, 1 - *pTrue}}, nil
}

// confidenceOf reads an optional confidence, which only choices and scores carry.
func confidenceOf(c *float64) float64 {
	if c == nil {
		return 0
	}
	return *c
}

// joinNonEmpty joins the parts that say something, so an empty one leaves no blank line.
func joinNonEmpty(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}
