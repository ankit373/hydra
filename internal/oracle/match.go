// SPDX-License-Identifier: MIT

package oracle

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/trust"
)

// detailAnswerBytes bounds how much of a failing answer a match Detail quotes.
const detailAnswerBytes = 80

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
		re, err := regexp.Compile(`^(?:` + pattern + `)$`)
		if err != nil {
			return nil, fmt.Errorf("oracle %s: %w", o.Source, err)
		}
		o.pattern = re
		return o, nil
	case len(labels) == 0:
		return nil, fmt.Errorf("oracle %s: no labels and no pattern, want exactly one", o.Source)
	}
	seen := make(map[string]string, len(labels))
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
	}
	o.labels = make(map[string]struct{}, len(seen))
	for n := range seen {
		o.labels[n] = struct{}{}
	}
	return o, nil
}

// Verify: labels → NormaliseLabel(candidate) ∈ set; pattern → the whole edge-stripped answer matches.
// Never errors on content: an empty answer is a failing verdict, not an oracle fault.
func (o *MatchOracle) Verify(_ context.Context, candidate string, _ trust.Task) (Verdict, error) {
	if o.pattern != nil {
		// `.*` matches the empty string, so an empty answer is refused before the regex sees it.
		if answer := stripEdges(candidate, isAnswerEdge); answer != "" && o.pattern.MatchString(answer) {
			return Verdict{Passed: true}, nil
		}
		return Verdict{Detail: fmt.Sprintf("does not match %s: %q", o.pattern, quoteHead(candidate))}, nil
	}
	if _, ok := o.labels[NormaliseLabel(candidate)]; ok {
		return Verdict{Passed: true}, nil
	}
	return Verdict{Detail: fmt.Sprintf("not one of %d labels: %q", len(o.labels), quoteHead(candidate))}, nil
}

// NormaliseLabel lowercases, strips edge space, quotes, backticks, '-', '_' and a trailing '.', and
// folds interior runs of space, '-' or '_' to one '_'. The benchmark harness grader (#1198) scores
// Pass with this same function, so there is no second copy.
func NormaliseLabel(s string) string {
	s = stripEdges(strings.ToLower(s), func(r rune) bool { return isAnswerEdge(r) || isSeparator(r) })
	var b strings.Builder
	inRun := false
	for _, r := range s {
		if isSeparator(r) {
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

// stripEdges trims edge runes and a trailing '.' until neither changes anything, so
// `'x.'.` and `x..` settle in one call and normalising twice changes nothing more.
func stripEdges(s string, edge func(rune) bool) string {
	for {
		t := strings.TrimSuffix(strings.TrimFunc(s, edge), ".")
		if t == s {
			return s
		}
		s = t
	}
}

func isAnswerEdge(r rune) bool { return unicode.IsSpace(r) || r == '"' || r == '\'' || r == '`' }

func isSeparator(r rune) bool { return unicode.IsSpace(r) || r == '-' || r == '_' }

// quoteHead cuts on a rune boundary, so a Detail never carries half a character.
func quoteHead(s string) string {
	if len(s) <= detailAnswerBytes {
		return s
	}
	n := detailAnswerBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
