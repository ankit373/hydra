// SPDX-License-Identifier: MIT

package oracle

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/ankit373/hydra/internal/trust"
)

// detailAnswerBytes bounds how much of a failing answer a match Detail quotes.
const detailAnswerBytes = 80

// labelWrappers are what a model puts around a label: quotes, curly quotes, markdown
// emphasis, '_' and the '*' and U+2022 bullets. Never '-', which can be part of the label.
const labelWrappers = "\"'`\u201c\u201d\u2018\u2019*_\u2022"

// MatchOracle passes an answer that is one of a closed label set, or matches a pattern.
type MatchOracle struct {
	// Source is the calibration key, "verifier:match:<id>".
	Source  string
	labels  map[string]struct{} // normalised
	pattern *regexp.Regexp      // anchored, so the whole answer must match
}

var _ Oracle = (*MatchOracle)(nil)

// NewMatch builds from exactly one of labels (non-empty) or pattern (compiles); else errors.
func NewMatch(id string, labels []string, pattern string) (*MatchOracle, error) {
	o := &MatchOracle{Source: "verifier:match:" + id}
	switch {
	case len(labels) > 0 && pattern != "":
		return nil, fmt.Errorf("oracle %s: both labels and a pattern given, want exactly one", o.Source)
	case pattern != "":
		// Compiled bare first: `a)|(.*` is unbalanced alone but balances inside ^(?:…)$.
		// The wrap can still fail on its own, one nesting level past the parser's limit.
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("oracle %s: %w", o.Source, err)
		}
		re, err := regexp.Compile(`^(?:` + pattern + `)$`)
		if err != nil {
			return nil, fmt.Errorf("oracle %s: %w", o.Source, err)
		}
		o.pattern = re
		return o, nil
	case len(labels) == 0:
		return nil, fmt.Errorf("oracle %s: no labels and no pattern, want exactly one", o.Source)
	}
	o.labels = make(map[string]struct{}, len(labels))
	seen := make(map[string]string, len(labels)) // for naming both sides of a collision
	for _, l := range labels {
		n := NormaliseLabel(l)
		// An empty label would let an empty or all-punctuation answer pass.
		if n == "" {
			return nil, fmt.Errorf("oracle %s: label %q is empty once normalised", o.Source, l)
		}
		// Two labels one answer satisfies cannot be told apart, so the set is ambiguous.
		if prev, ok := seen[n]; ok {
			return nil, fmt.Errorf("oracle %s: labels %q and %q both normalise to %q", o.Source, prev, l, n)
		}
		seen[n] = l
		o.labels[n] = struct{}{}
	}
	return o, nil
}

// Verify: labels → NormaliseLabel(candidate) ∈ set; pattern → the whole answer, without format
// runes and edge whitespace and otherwise not rewritten, matches. Empty always fails.
func (o *MatchOracle) Verify(_ context.Context, candidate string, _ trust.Task) (Verdict, error) {
	if o.pattern != nil {
		// `.*` matches the empty string, so an empty answer is refused before the regex sees it.
		if answer := strings.TrimSpace(dropFormat(candidate)); answer != "" && o.pattern.MatchString(answer) {
			return Verdict{Passed: true}, nil
		}
		return Verdict{Detail: matchDetail("does not match "+o.pattern.String(), candidate)}, nil
	}
	if _, ok := o.labels[NormaliseLabel(candidate)]; ok {
		return Verdict{Passed: true}, nil
	}
	return Verdict{Detail: matchDetail(fmt.Sprintf("not one of %d labels", len(o.labels)), candidate)}, nil
}

// matchDetail always keeps the quoted head of the answer, the part a reader acts on,
// and cuts what describes the check instead so the line fits detailMaxLen.
func matchDetail(check, candidate string) string {
	head, cut := clip(candidate, detailAnswerBytes)
	quoted := strconv.Quote(head)
	if cut {
		quoted += truncMarker
	}
	return truncate(check, detailMaxLen-len(": ")-len(quoted)-len(truncMarker)) + ": " + quoted
}

// NormaliseLabel lowercases (labels are ASCII per the design), drops format runes, strips edge
// whitespace, labelWrappers and trailing dots to a fixed point, and folds interior runs of space or
// '_' to one '_'. '-' is always significant: "- " is no bullet, as it reads the same as a sign.
func NormaliseLabel(s string) string {
	s = dropFormat(strings.ToLower(s))
	for {
		t := strings.TrimSuffix(strings.TrimFunc(s, isLabelEdge), ".")
		if t == s {
			break
		}
		s = t
	}
	var b strings.Builder
	inRun := false
	for _, r := range s {
		if unicode.IsSpace(r) || r == '_' {
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

// dropFormat removes invisible format runes (category Cf: BOM, zero-width space) wherever
// they are, since no label or pattern answer means anything by them.
func dropFormat(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func isLabelEdge(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(labelWrappers, r) }
