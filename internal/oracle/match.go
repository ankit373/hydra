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

// labelEdgeRunes are wrappers a model puts around a label: quotes, curly quotes,
// markdown emphasis, bullets and zero-width marks. Whitespace is handled by unicode.IsSpace.
const labelEdgeRunes = "\"'`\u201c\u201d\u2018\u2019*_\u2022\u200b\ufeff"

// MatchOracle passes an answer that is one of a closed label set, or matches a pattern.
type MatchOracle struct {
	// Source is the calibration key, "verifier:match:<id>".
	Source  string
	labels  map[string]string // normalised → as configured
	pattern *regexp.Regexp    // anchored, so the whole answer must match
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
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("oracle %s: %w", o.Source, err)
		}
		o.pattern = regexp.MustCompile(`^(?:` + pattern + `)$`)
		return o, nil
	case len(labels) == 0:
		return nil, fmt.Errorf("oracle %s: no labels and no pattern, want exactly one", o.Source)
	}
	o.labels = make(map[string]string, len(labels))
	for _, l := range labels {
		n := NormaliseLabel(l)
		// An empty label would let an empty or all-punctuation answer pass.
		if n == "" {
			return nil, fmt.Errorf("oracle %s: label %q is empty once normalised", o.Source, l)
		}
		// Two labels one answer satisfies cannot be told apart, so the set is ambiguous.
		if prev, ok := o.labels[n]; ok {
			return nil, fmt.Errorf("oracle %s: labels %q and %q both normalise to %q", o.Source, prev, l, n)
		}
		o.labels[n] = l
	}
	return o, nil
}

// Verify: labels → NormaliseLabel(candidate) ∈ set; pattern → the whole answer, trimmed of
// whitespace and otherwise not rewritten, matches. An empty answer fails; it is never an error.
func (o *MatchOracle) Verify(_ context.Context, candidate string, _ trust.Task) (Verdict, error) {
	if o.pattern != nil {
		// `.*` matches the empty string, so an empty answer is refused before the regex sees it.
		if answer := strings.TrimSpace(candidate); answer != "" && o.pattern.MatchString(answer) {
			return Verdict{Passed: true}, nil
		}
		return Verdict{Detail: matchDetail("does not match %s", o.pattern, candidate)}, nil
	}
	if _, ok := o.labels[NormaliseLabel(candidate)]; ok {
		return Verdict{Passed: true}, nil
	}
	return Verdict{Detail: matchDetail("not one of %d labels", len(o.labels), candidate)}, nil
}

// matchDetail quotes the answer's head and caps the whole line, both through truncate.
func matchDetail(format string, arg any, candidate string) string {
	head := fmt.Sprintf(format, arg)
	return truncate(fmt.Sprintf("%s: %q", head, truncate(candidate, detailAnswerBytes)), detailMaxLen)
}

// NormaliseLabel lowercases (strings.ToLower; labels are ASCII per the design), strips edge
// whitespace, labelEdgeRunes, a "- " bullet and trailing dots to a fixed point, then folds
// interior runs of space, '-' or '_' to one '_'. A '-' attached to a value, as in "-1", stays.
func NormaliseLabel(s string) string {
	s = strings.ToLower(s)
	for {
		t := strings.TrimSuffix(strings.TrimFunc(s, isLabelEdge), ".")
		if rest, ok := strings.CutPrefix(t, "-"); ok && strings.TrimLeftFunc(rest, unicode.IsSpace) != rest {
			t = rest
		}
		if t == s {
			break
		}
		s = t
	}
	r := []rune(s)
	var b strings.Builder
	for i := 0; i < len(r); {
		if !isSeparator(r[i]) {
			b.WriteRune(r[i])
			i++
			continue
		}
		j := i
		for j < len(r) && isSeparator(r[j]) {
			j++
		}
		if i > 0 && j < len(r) {
			b.WriteByte('_')
		} else {
			// Only a '-' can survive at an edge, and there it is a sign or a dash, not a gap.
			b.WriteString(strings.Repeat("-", strings.Count(string(r[i:j]), "-")))
		}
		i = j
	}
	return b.String()
}

func isLabelEdge(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(labelEdgeRunes, r) }

func isSeparator(r rune) bool { return unicode.IsSpace(r) || r == '-' || r == '_' }
