// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/ankit373/hydra/internal/budget"
	"github.com/ankit373/hydra/internal/entropy"
	"github.com/ankit373/hydra/internal/util"
)

const (
	// defaultMinAgree is the share of probes that must agree for a pass.
	defaultMinAgree = 0.8
	// proseOverlap is the word overlap two prose answers need to agree.
	proseOverlap = 0.5
	// unknownWindow is assumed for a head that reports no window; the pack is
	// then probed per group and marked partial.
	unknownWindow = 4096
	// maxDecisionProbes bounds the questions one pack can ask about decisions.
	maxDecisionProbes = 5
)

// probe is a question asked of both the full turns and the pack.
type probe struct {
	Kind     string
	Question string
}

const answerPrompt = "Answer from the context only, in one or two short sentences. " +
	"If the context does not say, reply: not stated.\n\nQuestion: "

// modelProbes are written by the head from the full turns: one recall, one
// decision with its reason, one next step.
var modelProbes = []struct{ kind, ask string }{
	{"recall", "a fact, number or name that later work could need"},
	{"reason", "why one decision in it was made"},
	{"next", "what should happen next"},
}

const questionPrompt = "Write one short question a reader could answer from this conversation about "

// deterministicProbes asks about the state itself, so no model writes them:
// the rules, the files, what is open, and each decision and its reason.
func deterministicProbes(s State) []probe {
	var out []probe
	if len(s.Constraints) > 0 {
		out = append(out, probe{"rules", "What rules must be followed?"})
	}
	if len(s.Files) > 0 {
		out = append(out, probe{"files", "Which files were edited or named?"})
	}
	if len(s.OpenQuestions) > 0 {
		out = append(out, probe{"open", "What is still open?"})
	}
	for _, d := range s.Decisions[:min(len(s.Decisions), maxDecisionProbes)] {
		out = append(out, probe{"decision", fmt.Sprintf("What was decided about %s, and why?", d.What)})
	}
	return out
}

// RunProbes asks every probe of the full turns and of the pack on head, and
// sets Pass only when enough answers agree. v must be a passing Verify
// verdict; anything else comes back unchanged. Turns that do not fit the head's
// window are probed per group, an answer agreeing when the pack's matches any
// group's, and the verdict is Partial: such a pack is never swapped in automatically.
func RunProbes(ctx context.Context, head Head, p Pack, t Turns, v Verdict, minAgree float64) (Verdict, error) {
	if !v.Pass {
		return v, nil
	}
	if minAgree <= 0 {
		minAgree = defaultMinAgree
	}
	span := t.Turns[p.Covers.From:p.Covers.To]
	window, ok := budget.EffectiveContext(head.Head())
	if !ok {
		window = unknownWindow
	}
	groups := groupTurns(span, window/2)
	v.Partial = len(groups) > 1 || !ok
	v.SameHead = head.Head().ID == p.Head
	v.Pass = false

	// Rendered once: every probe reads every group.
	rendered := make([]string, len(groups))
	for i, g := range groups {
		rendered[i] = renderTurns(g)
	}
	probes := deterministicProbes(p.State)
	extra, err := askProbes(ctx, head, rendered[len(rendered)-1])
	if err != nil {
		return v, err
	}
	probes = append(probes, extra...)
	if len(probes) == 0 {
		v.Reason = "no probes could be asked"
		return v, nil
	}
	packText, failed := p.Text(), ""
	for _, q := range probes {
		fromPack, err := answer(ctx, head, q.Question, packText)
		if err != nil {
			return v, err
		}
		agreed := false
		for _, g := range rendered {
			fromTurns, err := answer(ctx, head, q.Question, g)
			if err != nil {
				return v, err
			}
			if agreed = agree(fromTurns, fromPack); agreed {
				break
			}
		}
		v.Probes++
		if agreed {
			v.ProbeAgree++
		} else if failed == "" {
			failed = q.Question
		}
	}
	if float64(v.ProbeAgree)/float64(v.Probes) < minAgree {
		v.Reason = fmt.Sprintf("probes disagree: %d of %d agree, first %q", v.ProbeAgree, v.Probes, failed)
		return v, nil
	}
	v.Pass = true
	return v, nil
}

func askProbes(ctx context.Context, head Head, conversation string) ([]probe, error) {
	var out []probe
	for _, m := range modelProbes {
		q, err := head.Ask(ctx, questionPrompt+m.ask+". Reply with the question only.\n\n"+
			util.WrapUntrusted("CONVERSATION", conversation))
		if err != nil {
			return nil, err
		}
		if q, _, _ = strings.Cut(strings.TrimSpace(q), "\n"); q != "" {
			out = append(out, probe{m.kind, q})
		}
	}
	return out, nil
}

func answer(ctx context.Context, head Head, question, context string) (string, error) {
	reply, err := head.Ask(ctx, answerPrompt+question+"\n\n"+util.WrapUntrusted("CONTEXT", context))
	return thinking.ReplaceAllString(reply, ""), err
}

func renderTurns(ts []Turn) string {
	var b strings.Builder
	for _, t := range ts {
		fmt.Fprintf(&b, "%s: %s\n\n", t.Role, t.Text)
	}
	return b.String()
}

// groupTurns packs turns in order into groups of at most maxTokens; a turn
// larger than that is a group of its own.
func groupTurns(span []Turn, maxTokens int) [][]Turn {
	var out [][]Turn
	var cur []Turn
	n := 0
	for _, t := range span {
		size := entropy.TokenEstimate(t.Text) + 1
		if len(cur) > 0 && n+size > maxTokens {
			out = append(out, cur)
			cur, n = nil, 0
		}
		cur, n = append(cur, t), n+size
	}
	return append(out, cur)
}

var (
	numberToken = regexp.MustCompile(`\d[\d.,]*\d|\d`)
	notStated   = regexp.MustCompile(`(?i)^\W*not stated\W*$`)
)

// agree compares two answers. Paths and numbers must match exactly as sets;
// answers with none agree on word overlap. An empty answer never agrees.
func agree(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	if notStated.MatchString(a) && notStated.MatchString(b) {
		return true
	}
	ea, eb := exactTokens(a), exactTokens(b)
	if len(ea)+len(eb) > 0 {
		return slices.Equal(ea, eb)
	}
	return overlap(a, b) >= proseOverlap
}

// exactTokens is the sorted set of paths and numbers in s.
func exactTokens(s string) []string {
	set := map[string]bool{}
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, "`'\"()[]{}<>,;:!?.")
		if pathToken.MatchString(w) && !strings.Contains(w, "://") {
			set[w] = true
		}
	}
	for _, n := range numberToken.FindAllString(s, -1) {
		set[strings.ReplaceAll(n, ",", "")] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// overlap is the Jaccard overlap of the words longer than two letters.
func overlap(a, b string) float64 {
	wa, wb := words(a), words(b)
	both := 0
	for w := range wa {
		if wb[w] {
			both++
		}
	}
	if union := len(wa) + len(wb) - both; union > 0 {
		return float64(both) / float64(union)
	}
	return 0
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}
