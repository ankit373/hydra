// SPDX-License-Identifier: MIT

package checkgen

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/evalset"
	"github.com/ankit373/hydra/internal/oracle"
	"github.com/ankit373/hydra/internal/trust"
	"github.com/ankit373/hydra/internal/util"
)

var report = "Summarise this report.\n" + util.WrapUntrusted("DOCUMENT",
	"Acme Supplies sold 1,200 units in March. Jane Doe runs the Lisbon office. Costs fell by 3.5 percent.")

// noIfSays answers false to any question that mentions word, true otherwise, and keeps every question.
type noIfSays struct {
	word  string
	asked []string
}

func (d *noIfSays) Decide(_ context.Context, q string, _ []string) ([]float64, error) {
	d.asked = append(d.asked, q)
	if strings.Contains(q, d.word) {
		return []float64{0, 1}, nil
	}
	return []float64{1, 0}, nil
}

func claimsSpecs(t *testing.T) []Spec {
	t.Helper()
	specs, err := claimsGen{}.Generate(context.Background(), Task{Text: report, Writer: GenInfo{Head: "local:qwen", Family: "qwen"}})
	if err != nil || len(specs) != 2 {
		t.Fatalf("specs = %+v, %v", specs, err)
	}
	return specs
}

func TestClaims_SplitsSentencesNotNumbers(t *testing.T) {
	got := sentences("Costs fell by 3.5 percent. She said \"it rose. Then fell.\" Did it? Yes.\n1. Revenue was $1,200.\nOK.")
	want := []string{"Costs fell by 3.5 percent.", `She said "it rose. Then fell."`, "Did it?", "1. Revenue was $1,200."}
	if !slices.Equal(got, want) {
		t.Fatalf("sentences = %q, want %q", got, want)
	}
}

func TestClaims_UnsupportedSentenceFailsGround(t *testing.T) {
	o, err := claimsGen{}.Oracle(claimsSpecs(t)[0], Env{})
	if err != nil {
		t.Fatal(err)
	}
	answer := "Acme Supplies sold 1,200 units in March. Jane Doe runs the Lisbon office. Costs fell by 45 percent."
	v, err := o.Verify(context.Background(), answer, trust.Task{})
	if err != nil || v.Passed || !strings.Contains(v.Detail, `3 "Costs fell by 45 percent."`) || strings.Contains(v.Detail, "1 ") {
		t.Fatalf("verdict %+v, %v; want sentence 3 alone named", v, err)
	}
	v, err = o.Verify(context.Background(), "Acme Supplies sold 1,200 units.", trust.Task{})
	if err != nil || !v.Passed {
		t.Fatalf("a supported sentence: %+v, %v", v, err)
	}
	if _, err = o.Verify(context.Background(), "Sales went well overall.", trust.Task{}); err == nil {
		t.Error("an answer with nothing checkable read as a verdict")
	}
}

func TestClaims_JudgeAskedOncePerSentence(t *testing.T) {
	d := &noIfSays{word: "Atlantis"}
	env := Env{Decider: func(gen GenInfo) oracle.Decider {
		if gen.Head != "local:qwen" {
			t.Errorf("judge bound to %q, want the spec's writer", gen.Head)
		}
		return d
	}}
	o, err := claimsGen{}.Oracle(claimsSpecs(t)[1], env)
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.Verify(context.Background(), "Acme sold units. OK. The firm moved to Atlantis. Jane Doe runs it.", trust.Task{})
	if err != nil || v.Passed || !strings.Contains(v.Detail, `2 "The firm moved to Atlantis."`) {
		t.Fatalf("verdict %+v, %v", v, err)
	}
	if len(d.asked) != 3 || !strings.Contains(d.asked[0], "Acme Supplies sold 1,200") || strings.Contains(d.asked[0], "Jane Doe runs it") {
		t.Fatalf("asked %d questions, want one per content sentence, each with the source and that sentence alone", len(d.asked))
	}
	if _, err := (claimsGen{}).Oracle(claimsSpecs(t)[1], Env{}); err == nil {
		t.Error("a judge with no model was built")
	}
}

func TestClaims_OverCapIsPartlyChecked(t *testing.T) {
	var b strings.Builder
	for range maxClaimSentences + 1 {
		b.WriteString("Acme Supplies sold 1,200 units. ")
	}
	o, err := claimsGen{}.Oracle(claimsSpecs(t)[0], Env{})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := o.Verify(context.Background(), b.String(), trust.Task{}); err == nil || !strings.Contains(err.Error(), "partly checked") {
		t.Fatalf("over the cap = %+v, %v; want partly checked, not a pass", v, err)
	}
	if v, _ := o.Verify(context.Background(), "Costs fell 9 percent. "+b.String(), trust.Task{}); v.Passed {
		t.Error("a failed sentence under the cap did not fail the answer")
	}
}

// The done-when: one invented sentence fails both checks, and both verdicts name it.
func TestClaims_InventedSentenceFailsBothChecks(t *testing.T) {
	span, _ := setup(t, claimsGen{})
	specs := claimsSpecs(t)
	answer := "Acme Supplies sold 1,200 units in March. Acme opened 45 stores in Atlantis."
	res, err := Run(context.Background(), specs, answer, answering, span, Env{Decider: func(GenInfo) oracle.Decider {
		return &noIfSays{word: "Atlantis"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.Checked() || r.Verdict.Passed || !strings.Contains(r.Verdict.Detail, `2 "Acme opened 45 stores in Atlantis."`) {
			t.Errorf("%s = %+v", r.Spec.Check(), r)
		}
	}
	answers, err := evalset.Joint(evalset.VerdictsPath())
	if err != nil || len(answers) != 1 || len(answers[0].Checks) != 2 ||
		answers[0].Checks[0].Check != "gen:claims-ground" || answers[0].Checks[1].Check != "gen:claims-judge" {
		t.Fatalf("verdict log = %+v, %v", answers, err)
	}
}

func TestClaims_NoSourceNoSpecAndJudgeErrorIsNotChecked(t *testing.T) {
	if specs, err := (claimsGen{}).Generate(context.Background(), Task{Text: "what is the capital of France?"}); err != nil || specs != nil {
		t.Errorf("no fenced source: specs %+v, %v; want none", specs, err)
	}
	env := Env{Decider: func(GenInfo) oracle.Decider { return failing{} }}
	o, err := claimsGen{}.Oracle(claimsSpecs(t)[1], env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Verify(context.Background(), "Acme sold units.", trust.Task{}); err == nil {
		t.Error("a judge that failed to answer read as a verdict")
	}
	if _, err := (claimsGen{}).Oracle(Spec{ID: "x", Body: []byte(`{}`), Variant: "vibes"}, env); err == nil {
		t.Error("built an oracle for an unknown variant")
	}
}

type failing struct{}

func (failing) Decide(context.Context, string, []string) ([]float64, error) {
	return nil, fmt.Errorf("head down: %w", errors.ErrUnsupported)
}

// lastVerdicts runs the oracle for spec on answer and returns its per-sentence verdicts.
func lastVerdicts(t *testing.T, spec Spec, env Env, answer string) []SentenceVerdict {
	t.Helper()
	o, err := claimsGen{}.Oracle(spec, env)
	if err != nil {
		t.Fatal(err)
	}
	o.Verify(context.Background(), answer, trust.Task{})
	return o.(*claimsOracle).Last
}

func TestClaims_SentenceVerdictsCoverEverySentence(t *testing.T) {
	answer := "Acme Supplies sold 1,200 units in March. Sales went well overall. Costs fell by 45 percent."
	got := lastVerdicts(t, claimsSpecs(t)[0], Env{}, answer)
	want := []struct{ status, unsupported string }{
		{StatusSupported, ""}, {StatusNothingToCheck, ""}, {StatusNotSupported, "45"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d verdicts, want one per sentence: %+v", len(got), got)
	}
	for i, w := range want {
		g := got[i]
		if g.N != i+1 || g.Text != sentences(answer)[i] || g.Status != w.status {
			t.Errorf("sentence %d = %+v, want status %s", i+1, g, w.status)
		}
		if w.unsupported == "" && (len(g.Unsupported) > 0 || g.Why != "") {
			t.Errorf("sentence %d carries a reason it should not: %+v", i+1, g)
		}
		if w.unsupported != "" && (len(g.Unsupported) != 1 || g.Unsupported[0].Text != w.unsupported || g.Why == "") {
			t.Errorf("sentence %d unsupported = %+v, want %q with a reason", i+1, g, w.unsupported)
		}
	}
	judged := lastVerdicts(t, claimsSpecs(t)[1], Env{Decider: func(GenInfo) oracle.Decider {
		return &noIfSays{word: "Atlantis"}
	}}, "Acme sold units. The firm moved to Atlantis.")
	if len(judged) != 2 || judged[0].Status != StatusSupported || judged[1].Status != StatusNotSupported ||
		judged[1].Why == "" || judged[1].Unsupported != nil {
		t.Fatalf("judge verdicts = %+v; want the second not supported, with a reason and no spans", judged)
	}
}

func TestClaims_SentenceWithNothingCheckableIsMarked(t *testing.T) {
	got := lastVerdicts(t, claimsSpecs(t)[0], Env{}, "Sales went well overall.")
	if len(got) != 1 || got[0].Status != StatusNothingToCheck || got[0].Text != "Sales went well overall." {
		t.Fatalf("verdicts = %+v; want the one sentence marked nothing to check", got)
	}
}

func TestClaims_PassFailUnchanged(t *testing.T) {
	judge := Env{Decider: func(GenInfo) oracle.Decider { return &noIfSays{word: "Atlantis"} }}
	cases := []struct {
		spec   int
		env    Env
		answer string
		passed bool
		detail string
		errs   bool
	}{
		{0, Env{}, "Acme Supplies sold 1,200 units in March. Jane Doe runs the Lisbon office. Costs fell by 45 percent.", false,
			`sentences not supported: 3 "Costs fell by 45 percent." (not in the context given: number 45)`, false},
		{0, Env{}, "Acme Supplies sold 1,200 units.", true, "all 1 sentences checked passed", false},
		{0, Env{}, "Sales went well overall.", false, "", true},
		{1, judge, "Acme sold units. OK. The firm moved to Atlantis. Jane Doe runs it.", false,
			`sentences not supported: 2 "The firm moved to Atlantis."`, false},
		{1, judge, "Acme sold units. Jane Doe runs it.", true, "all 2 sentences checked passed", false},
	}
	for i, c := range cases {
		o, err := claimsGen{}.Oracle(claimsSpecs(t)[c.spec], c.env)
		if err != nil {
			t.Fatal(err)
		}
		v, err := o.Verify(context.Background(), c.answer, trust.Task{})
		if (err != nil) != c.errs || v.Passed != c.passed || v.Detail != c.detail {
			t.Errorf("case %d = %+v, %v; want passed %v detail %q", i, v, err, c.passed, c.detail)
		}
	}
}
