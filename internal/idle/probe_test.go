// SPDX-License-Identifier: MIT

package idle

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
)

// lookupHead writes the question for a question prompt and answers any other
// question with the first context line holding the keyword its table maps the
// question to (first match wins), so an answer is only as good as the context it was given.
type lookupHead struct {
	meta    map[string]string
	keyword [][2]string
	asked   []string
}

var refSuffix = regexp.MustCompile(`\s*\[ref [^\]]*\]`)

func (h *lookupHead) Head() provider.Head {
	return provider.Head{ID: "ollama/stub", LocalOnly: true, Meta: h.meta}
}

func (h *lookupHead) Ask(_ context.Context, prompt string) (string, error) {
	h.asked = append(h.asked, prompt)
	if strings.HasPrefix(prompt, questionPrompt) {
		return "What was decided about the refund?\nignored", nil
	}
	q, ctx, _ := strings.Cut(strings.TrimPrefix(prompt, answerPrompt), "\n\n")
	for _, kw := range h.keyword {
		if !strings.Contains(q, kw[0]) {
			continue
		}
		for _, line := range strings.Split(ctx, "\n") {
			if strings.Contains(line, kw[1]) {
				return strings.TrimSpace(strings.TrimPrefix(refSuffix.ReplaceAllString(line, ""), "- ")), nil
			}
		}
	}
	return "not stated", nil
}

func newLookupHead() *lookupHead {
	return &lookupHead{keyword: [][2]string{{"rules", "Never promise"}, {"refund", "refund one charge"}}}
}

func probeTurns() Turns {
	t := Turns{Turns: []Turn{
		{Role: "system", Text: "Never promise a refund over $200 without a manager.", Ref: "s"},
		{Role: "user", Text: "I was charged twice on invoice 4417. Only email me.", Ref: "u1"},
		{Role: "assistant", Text: "We will refund one charge because both lines are the same invoice.", Ref: "a1"},
	}}
	for i := range t.Turns {
		t.Turns[i].Seq = i
	}
	return t
}

func probePack(t Turns, why string) Pack {
	return Pack{
		State: State{
			Goal:        "refund the double charge",
			Constraints: ExtractConstraints(t.Turns),
			Decisions:   []Decision{{What: "refund one charge", Why: why, Ref: "a1"}},
		},
		Head:   "ollama/stub",
		Covers: Covers{From: 0, To: len(t.Turns), Hash: coverHash(t.Turns)},
	}
}

func run(t *testing.T, h *lookupHead, p Pack, turns Turns) Verdict {
	t.Helper()
	v, err := RunProbes(context.Background(), h, p, turns, Verdict{Pass: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestProbe_LostDecisionDisagrees(t *testing.T) {
	turns := probeTurns()
	h := newLookupHead()
	good := run(t, h, probePack(turns, "both lines are the same invoice"), turns)
	if !good.Pass || good.Probes != good.ProbeAgree || good.Probes < 5 || !good.SameHead {
		t.Fatalf("faithful pack: %+v, want every probe to agree on the same head", good)
	}
	lost := run(t, newLookupHead(), probePack(turns, "the user asked"), turns)
	if lost.Pass || !strings.HasPrefix(lost.Reason, "probes disagree: ") || !strings.Contains(lost.Reason, "refund one charge") {
		t.Fatalf("pack that lost the reason: %+v, want probes to disagree", lost)
	}
}

func TestProbe_ExactMatchForIdsAndPaths(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"edited reports/nov.csv, invoice 4417", "invoice 4417 and reports/nov.csv edited", true},
		{"the file reports/nov.csv", "the file reports/oct.csv", false},
		{"invoice 4417 was charged twice", "invoice 4471 was charged twice", false},
		{"charged 1,200 dollars", "charged 1200 dollars", true},
		{"charged twice on the same invoice", "the same invoice charged twice", true},
		{"charged twice", "refund requested by email", false},
		{"not stated", "Not stated.", true},
		{"", "not stated", false},
	} {
		if got := agree(c.a, c.b); got != c.want {
			t.Errorf("agree(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestProbe_TooBigForWindowIsPartial(t *testing.T) {
	turns := probeTurns()
	for i := 0; i < 6; i++ {
		turns.Turns = append(turns.Turns, Turn{Role: "tool", Text: strings.Repeat("ledger line. ", 25), Ref: "t" + strconv.Itoa(i), Seq: len(turns.Turns)})
	}
	h := newLookupHead()
	h.meta = map[string]string{"model_ctx": "400"}
	v := run(t, h, probePack(turns, "both lines are the same invoice"), turns)
	if !v.Partial || !v.Pass {
		t.Fatalf("verdict %+v, want a partial pass", v)
	}
	for _, prompt := range h.asked {
		if len(prompt)/4 > 400 {
			t.Fatalf("a prompt of %d bytes was sent to a window of 400 tokens", len(prompt))
		}
	}
	whole := newLookupHead()
	whole.meta = map[string]string{"model_ctx": "100000"}
	if v := run(t, whole, probePack(probeTurns(), "both lines are the same invoice"), probeTurns()); v.Partial {
		t.Fatal("a conversation that fits its window was marked partial")
	}
	if v := run(t, newLookupHead(), probePack(probeTurns(), "both lines are the same invoice"), probeTurns()); !v.Partial {
		t.Fatal("a head that reports no window was not marked partial")
	}
}

func TestProbe_FailedVerifyIsNotProbed(t *testing.T) {
	turns := probeTurns()
	h := newLookupHead()
	v, err := RunProbes(context.Background(), h, probePack(turns, "x"), turns, Verdict{Reason: "inflated"}, 0)
	if err != nil || v.Pass || v.Reason != "inflated" || len(h.asked) != 0 {
		t.Fatalf("verdict %+v, %d asks, err %v; want the fail kept and no model asked", v, len(h.asked), err)
	}
}
