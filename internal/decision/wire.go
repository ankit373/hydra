// SPDX-License-Identifier: MIT

package decision

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errImageNotRouted refuses an image on the serving side, where routing one is not built yet.
var errImageNotRouted = errors.New("images are not routed yet; send text only")

// textOf reads a field the wire allows as a string or as structured JSON. Structure
// is kept as compact JSON text, since a routed question carries text only.
func textOf(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", err
	}
	return compact.String(), nil
}

// optionText is an option as one line of text, for a dialect that has no separate description.
func optionText(o Option) string {
	if o.Description == "" {
		return o.Name
	}
	return o.Name + ": " + o.Description
}

// aligned orders a keyed distribution by keys, refusing one that names a
// different set of outcomes than were asked about.
func aligned(probs map[string]float64, keys []string) ([]float64, error) {
	if len(probs) != len(keys) {
		return nil, fmt.Errorf("answered %d outcomes for %d asked", len(probs), len(keys))
	}
	out := make([]float64, len(keys))
	for i, k := range keys {
		p, ok := probs[k]
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
