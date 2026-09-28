// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"math"
	"testing"
)

// distinctSources returns n sources with distinct ids sharing one cost, so each
// vote is an independent calibrated opinion rather than a repeat of one head.
func distinctSources(ids []string, cost float64) []Source {
	out := make([]Source, len(ids))
	for i, id := range ids {
		out[i] = Source{ID: id, EstCostUSD: cost}
	}
	return out
}

func hypFor(t *testing.T, res *Result, answer string) Hypothesis {
	t.Helper()
	for _, h := range res.Hypotheses {
		if h.Answer == answer {
			return h
		}
	}
	t.Fatalf("no hypothesis for %q; got %+v", answer, res.Hypotheses)
	return Hypothesis{}
}

// The defect: the old binary form re-seeded Λ when the leader changed, throwing
// away every vote cast before the change. Here the first vote backs the answer
// that loses, and its contribution must still appear in the winner's Λ as
// evidence against it.
func TestMSPRT_KeepsEvidenceGatheredBeforeTheLeaderChanged(t *testing.T) {
	c, _ := New("")
	for _, id := range []string{"s1", "s2", "s3"} {
		calibrateSymmetric(c, id, "d", 0.9, 1000)
	}
	agree, disagree := c.LLR("s1", "d", true), c.LLR("s1", "d", false)

	exec := &scriptExec{seq: []string{"A", "B", "B"}}
	res, err := Run(context.Background(), Task{Domain: "d"},
		distinctSources([]string{"s1", "s2", "s3"}, 1), exec, c, Target{Confidence: 0.99})
	if err != nil {
		t.Fatal(err)
	}
	if res.Candidate != "B" {
		t.Fatalf("candidate = %q, want B (two of three votes)", res.Candidate)
	}
	// B: s1 disagreed, s2 and s3 agreed. All three votes must be present.
	wantB := disagree + 2*agree
	if got := hypFor(t, res, "B").Lambda; math.Abs(got-wantB) > 1e-9 {
		t.Errorf("Λ(B) = %.4f, want %.4f = one disagree + two agrees; the losing vote was dropped", got, wantB)
	}
	// A: s1 agreed, s2 and s3 disagreed. Tracked for the whole run, not discarded.
	wantA := agree + 2*disagree
	if got := hypFor(t, res, "A").Lambda; math.Abs(got-wantA) > 1e-9 {
		t.Errorf("Λ(A) = %.4f, want %.4f; the superseded answer must stay under test", got, wantA)
	}
}

// Three sources, three different answers: nobody is corroborated, so no answer
// may clear the bar. The old form would have pivoted its way to a "winner".
func TestMSPRT_UnanimousDisagreementConvincesNobody(t *testing.T) {
	c, _ := New("")
	for _, id := range []string{"s1", "s2", "s3"} {
		calibrateSymmetric(c, id, "d", 0.9, 1000)
	}
	exec := &scriptExec{seq: []string{"A", "B", "C"}}
	res, err := Run(context.Background(), Task{Domain: "d"},
		distinctSources([]string{"s1", "s2", "s3"}, 1), exec, c, Target{Confidence: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision == DecisionAccept {
		t.Errorf("decision = accept at %.3f confidence; three sources giving three answers corroborate nothing", res.Confidence)
	}
	if res.Confidence >= 0.5 {
		t.Errorf("confidence = %.3f, want below 0.5 when every source is contradicted by the others", res.Confidence)
	}
	if len(res.Hypotheses) != 3 {
		t.Errorf("tracked %d hypotheses, want 3, one per distinct answer", len(res.Hypotheses))
	}
}

// A late-arriving majority must be able to win on evidence that includes the
// votes cast against it, and the accepted Λ must be the leader's own.
func TestMSPRT_LateMajorityWinsOnAllTheEvidence(t *testing.T) {
	c, _ := New("")
	ids := []string{"s1", "s2", "s3", "s4"}
	for _, id := range ids {
		calibrateSymmetric(c, id, "d", 0.95, 2000)
	}
	exec := &scriptExec{seq: []string{"wrong", "right", "right", "right"}}
	res, err := Run(context.Background(), Task{Domain: "d"},
		distinctSources(ids, 1), exec, c, Target{Confidence: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if res.Candidate != "right" || res.Decision != DecisionAccept {
		t.Fatalf("got candidate %q decision %v, want \"right\" accepted", res.Candidate, res.Decision)
	}
	if res.Lambda != hypFor(t, res, "right").Lambda {
		t.Errorf("Result.Lambda = %.4f but Λ(right) = %.4f; the reported total must be the leader's",
			res.Lambda, hypFor(t, res, "right").Lambda)
	}
	if h := hypFor(t, res, "right"); h.Votes != res.Samples-1 {
		t.Errorf("Votes(right) = %d, want %d", h.Votes, res.Samples-1)
	}
}

// Confidence must stay the leader's own σ(Λ), which is what makes the accept
// threshold exactly Wald's A rather than something normalized across answers.
func TestMSPRT_ConfidenceIsTheLeadersOwnLogOdds(t *testing.T) {
	c, _ := New("")
	for _, id := range []string{"s1", "s2"} {
		calibrateSymmetric(c, id, "d", 0.85, 800)
	}
	exec := &scriptExec{seq: []string{"A", "A"}}
	res, err := Run(context.Background(), Task{Domain: "d"},
		distinctSources([]string{"s1", "s2"}, 1), exec, c, Target{Confidence: 0.999})
	if err != nil {
		t.Fatal(err)
	}
	if want := sigmoid(res.Lambda); math.Abs(res.Confidence-want) > 1e-12 {
		t.Errorf("confidence = %.6f, want σ(Λ) = %.6f", res.Confidence, want)
	}
	for _, h := range res.Hypotheses {
		if math.Abs(h.Confidence-sigmoid(h.Lambda)) > 1e-12 {
			t.Errorf("hypothesis %q: confidence %.6f != σ(%.4f)", h.Answer, h.Confidence, h.Lambda)
		}
	}
}

// Hypotheses come back strongest-first so a reader (and `hyctl trust explain`)
// can take the leader off the front without re-sorting.
func TestMSPRT_HypothesesAreOrderedStrongestFirst(t *testing.T) {
	c, _ := New("")
	ids := []string{"s1", "s2", "s3"}
	for _, id := range ids {
		calibrateSymmetric(c, id, "d", 0.9, 1000)
	}
	exec := &scriptExec{seq: []string{"A", "B", "B"}}
	res, err := Run(context.Background(), Task{Domain: "d"}, distinctSources(ids, 1), exec, c, Target{Confidence: 0.999})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(res.Hypotheses); i++ {
		if res.Hypotheses[i-1].Lambda < res.Hypotheses[i].Lambda {
			t.Fatalf("hypotheses out of order at %d: %+v", i, res.Hypotheses)
		}
	}
	if res.Hypotheses[0].Answer != res.Candidate {
		t.Errorf("leader %q != candidate %q", res.Hypotheses[0].Answer, res.Candidate)
	}
}
