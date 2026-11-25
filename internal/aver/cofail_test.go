// SPDX-License-Identifier: MIT

package aver

import (
	"slices"
	"testing"
)

// wrongWith makes n wrong answers from head m in kind k, passing the named checks
// on the first `both` of them and failing them on the rest.
func wrongWith(n, both int, checks ...string) []Answer {
	var out []Answer
	for i := range n {
		v := map[string]bool{}
		for _, c := range checks {
			v[c] = i < both
		}
		out = append(out, Answer{Kind: "k", Head: "m", Verdicts: v})
	}
	return out
}

func rights(n int, checks ...string) []Answer {
	var out []Answer
	for range n {
		v := map[string]bool{}
		for _, c := range checks {
			v[c] = true
		}
		out = append(out, Answer{Kind: "k", Head: "m", Correct: true, Verdicts: v})
	}
	return out
}

func gate(t *testing.T, in []Answer) (Table, Groups) {
	t.Helper()
	tab, err := Fit(in, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	return tab, Gate(in, tab)
}

func TestGate_LinksPairAboveProduct(t *testing.T) {
	// a and b pass the same 10 of 100 wrong answers. Each q̄ is near 0.17 (Clopper-Pearson
	// at 0.025), so independence predicts both pass 3%; the bound on 10/100 is near 0.16.
	in := append(wrongWith(100, 10, "a", "b"), rights(50, "a", "b")...)
	_, g := gate(t, in)
	if g.Linked != 1 || g.Untestable != 0 || len(g.Groups) != 1 || !slices.Equal(g.Groups[0].Checks, []string{"a", "b"}) {
		t.Fatalf("want a and b linked, got %+v", g)
	}
}

func TestGate_KeepsIndependentPairApart(t *testing.T) {
	// a passes wrong answers 0-29, b passes 30-59: 0 of 100 both, bound 1 - 0.05^(1/100)
	// = 0.0295, under q̄_a q̄_b near 0.17. Strong checks need n > 3/q̄^2 to show this.
	var in []Answer
	for i := range 100 {
		in = append(in, Answer{Kind: "k", Head: "m", Verdicts: map[string]bool{"a": i < 30, "b": i >= 30 && i < 60}})
	}
	in = append(in, rights(50, "a", "b")...)
	_, g := gate(t, in)
	if g.Linked != 0 || len(g.Groups) != 0 {
		t.Fatalf("an independent pair was linked: %+v", g)
	}
}

func TestGate_LinksUntestablePair(t *testing.T) {
	in := append(wrongWith(19, 0, "a", "b"), rights(5, "a", "b")...)
	_, g := gate(t, in)
	if g.Untestable != 1 || len(g.Groups) != 1 {
		t.Fatalf("19 shared wrong answers should link for want of data, got %+v", g)
	}
}

func TestGate_GroupsAreTransitive(t *testing.T) {
	// a~b and b~c always pass together; a and c never share an answer, so their
	// pair is never formed, yet all three land in one group.
	var in []Answer
	for i := range 60 {
		in = append(in, Answer{Kind: "k", Head: "m", Verdicts: map[string]bool{"a": i%6 == 0, "b": i%6 == 0}})
		in = append(in, Answer{Kind: "k", Head: "m", Verdicts: map[string]bool{"b": i%6 == 0, "c": i%6 == 0}})
	}
	in = append(in, rights(40, "a", "b", "c")...)
	_, g := gate(t, in)
	if len(g.Groups) != 1 || !slices.Equal(g.Groups[0].Checks, []string{"a", "b", "c"}) {
		t.Fatalf("want one group a, b, c, got %+v", g)
	}
}

func TestGroupEvidence_TakesOneCheckPerGroup(t *testing.T) {
	// a and b always pass together; b passes fewer right answers, so a has the
	// better e on a pass and is the one that counts. c stands alone.
	var in []Answer
	for i := range 100 {
		in = append(in, Answer{Kind: "k", Head: "m", Verdicts: map[string]bool{"a": i < 30, "b": i < 30, "c": i >= 60 && i < 90}})
	}
	for i := range 50 {
		in = append(in, Answer{Kind: "k", Head: "m", Correct: true, Verdicts: map[string]bool{"a": true, "b": i < 40, "c": true}})
	}
	tab, g := gate(t, in)
	if len(g.Groups) != 1 || g.Groups[0].Rep != "a" {
		t.Fatalf("want one group with rep a, got %+v", g)
	}
	gated := g.Apply(tab)
	idx := gated.Index()
	a, b, c := idx[[3]string{"k", "a", "m"}], idx[[3]string{"k", "b", "m"}], idx[[3]string{"k", "c", "m"}]
	if a.Skip || !b.Skip || b.LinkedTo != "a" || c.Skip {
		t.Fatalf("a counts, b is skipped as linked to a, c counts: %+v %+v %+v", a, b, c)
	}
	// Two passes on a wrong answer: plain product a*b*c, gated a*c, so b's e drops out.
	plain := EValue(a, true) * EValue(tab.Index()[[3]string{"k", "b", "m"}], true) * EValue(c, true)
	if got := EValue(a, true) * EValue(b, true) * EValue(c, true); got >= plain || got != EValue(a, true)*EValue(c, true) {
		t.Fatalf("gated product %v, plain %v", got, plain)
	}
}

func TestGate_GroupsPerKindAndHead(t *testing.T) {
	base := append(wrongWith(100, 10, "a", "b"), rights(50, "a", "b")...)
	var in []Answer
	for _, kh := range [][2]string{{"k", "n"}, {"j", "m"}, {"k", "m"}} {
		for _, a := range base {
			a.Kind, a.Head = kh[0], kh[1]
			in = append(in, a)
		}
	}
	_, g := gate(t, in)
	var got []string
	for _, grp := range g.Groups {
		got = append(got, grp.Kind+"/"+grp.Head)
	}
	if !slices.Equal(got, []string{"j/m", "k/m", "k/n"}) || g.Linked != 3 {
		t.Fatalf("want one group per (kind, head), sorted, got %v (%+v)", got, g)
	}
}
