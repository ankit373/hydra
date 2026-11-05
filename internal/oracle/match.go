// SPDX-License-Identifier: MIT

package oracle

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/ankit373/hydra/internal/trust"
)

// detailAnswerBytes bounds how much of a failing answer a match Detail quotes.
const detailAnswerBytes = 80

// labelEdges is what NormaliseLabel trims from either end of an answer.
const labelEdges = " \t\r\n\"'`"

// MatchOracle passes an answer that is one of a closed label set, or matches a pattern.
type MatchOracle struct {
	// Source is the calibration key, "verifier:match:<id>".
	Source  string
	labels  map[string]struct{} // normalised
	pattern *regexp.Regexp
}

var _ Oracle = (*MatchOracle)(nil)

// NewMatch builds from exactly one of labels (non-empty) or pattern (compiles); else errors.
func NewMatch(id string, labels []string, pattern string) (*MatchOracle, error) {
	o := &MatchOracle{Source: "verifier:match:" + id}
	switch {
	case len(labels) > 0 && pattern != "":
		return nil, fmt.Errorf("oracle %s: both labels and a pattern given, want exactly one", o.Source)
	case pattern != "":
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("oracle %s: %w", o.Source, err)
		}
		o.pattern = re
		return o, nil
	case len(labels) == 0:
		return nil, fmt.Errorf("oracle %s: no labels and no pattern, want exactly one", o.Source)
	}
	o.labels = make(map[string]struct{}, len(labels))
	for i, l := range labels {
		n := NormaliseLabel(l)
		// An empty label would let an empty or all-punctuation answer pass.
		if n == "" {
			return nil, fmt.Errorf("oracle %s: label %d (%q) is empty once normalised", o.Source, i, l)
		}
		o.labels[n] = struct{}{}
	}
	return o, nil
}

// Verify: labels → NormaliseLabel(candidate) ∈ set; pattern → MatchString(TrimSpace(candidate)).
// Never errors on content: an empty answer is a failing verdict, not an oracle fault.
func (o *MatchOracle) Verify(_ context.Context, candidate string, _ trust.Task) (Verdict, error) {
	quoted := candidate
	if len(quoted) > detailAnswerBytes {
		quoted = quoted[:detailAnswerBytes]
	}
	if o.pattern != nil {
		// A pattern such as `.*` matches nothing at all; an empty answer still fails.
		if answer := strings.TrimSpace(candidate); answer != "" && o.pattern.MatchString(answer) {
			return Verdict{Passed: true}, nil
		}
		return Verdict{Detail: fmt.Sprintf("does not match %s: %q", o.pattern, quoted)}, nil
	}
	if _, ok := o.labels[NormaliseLabel(candidate)]; ok {
		return Verdict{Passed: true}, nil
	}
	return Verdict{Detail: fmt.Sprintf("not one of %d labels: %q", len(o.labels), quoted)}, nil
}

// NormaliseLabel lowercases, trims space, quotes, backticks and a trailing '.', and folds
// runs of space or '-' to '_'. E7 scores Pass with this same function, so there is no second copy.
func NormaliseLabel(s string) string {
	s = strings.Trim(strings.ToLower(s), labelEdges)
	// Trimmed on both sides of the '.', so `"Card Arrival."` and `"card_arrival".` agree.
	s = strings.Trim(strings.TrimSuffix(s, "."), labelEdges)
	var b strings.Builder
	inRun := false
	for _, r := range s {
		if r == '-' || unicode.IsSpace(r) {
			if !inRun {
				b.WriteByte('_')
			}
			inRun = true
			continue
		}
		inRun = false
		b.WriteRune(r)
	}
	return b.String()
}
