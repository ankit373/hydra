// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// calibrateSymmetric trains (id,domain) to se=sp≈p using n balanced samples.
// calibrateSymmetric trains (id,domain) to se=sp≈p; it delegates to the
// production helper backing `hyctl trust benchmark` so the two never diverge.
func calibrateSymmetric(c *Calibrator, id, domain string, p float64, n int) {
	calibrateSynthetic(c, id, domain, p, n)
}

// scriptExec returns a fixed answer sequence, one per Execute call.
type scriptExec struct {
	seq []string
	i   int
}

func (s *scriptExec) Execute(_ context.Context, src Source, _ Task) (Answer, error) {
	if s.i >= len(s.seq) {
		return Answer{}, fmt.Errorf("script exhausted")
	}
	a := s.seq[s.i]
	s.i++
	return Answer{Text: a, CostUSD: src.EstCostUSD}, nil
}

func nSources(id string, n int, cost float64) []Source {
	out := make([]Source, n)
	for i := range out {
		out[i] = Source{ID: id, EstCostUSD: cost}
	}
	return out
}

// The accept threshold is exactly the Wald A = ln((1-α)/α): k self-consistent
// votes from a source with per-verdict LLR L accept iff k·L ≥ A.
func TestSPRT_AcceptThreshold(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)
	L := c.LLR("sim", "d", true)
	A := math.Log(19) // α=0.05
	wantSamples := int(math.Ceil(A / L))

	exec := &scriptExec{seq: []string{"A", "A", "A", "A", "A"}}
	res, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 5, 1), exec, c, Target{Confidence: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAccept {
		t.Fatalf("decision = %v, want accept", res.Decision)
	}
	if res.Samples != wantSamples {
		t.Errorf("samples = %d, want %d (ceil(A/L))", res.Samples, wantSamples)
	}
	if res.Candidate != "A" {
		t.Errorf("candidate = %q, want A", res.Candidate)
	}
	if res.Confidence < 0.95 {
		t.Errorf("confidence = %.4f, want ≥0.95", res.Confidence)
	}
}

// When the weight of evidence turns against the seed answer, the run pivots to
// the answer the disagreeing sources support.
func TestSPRT_PivotsOnDisagreement(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)

	exec := &scriptExec{seq: []string{"A", "B", "B", "B", "B", "B"}}
	res, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 6, 1), exec, c, Target{Confidence: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if res.Candidate != "B" {
		t.Errorf("candidate = %q, want B after pivot", res.Candidate)
	}
	if res.Decision != DecisionAccept {
		t.Errorf("decision = %v, want accept", res.Decision)
	}
}

func TestSPRT_StopsOnBudget(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)

	exec := &scriptExec{seq: []string{"A", "A", "A"}}
	res, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 3, 1),
		exec, c, Target{Confidence: 0.95, MaxCostUSD: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionStoppedOnBudget {
		t.Errorf("decision = %v, want stopped_on_budget", res.Decision)
	}
	if res.Samples != 1 {
		t.Errorf("samples = %d, want 1 (budget fits one $1 call under $1.5)", res.Samples)
	}
}

// Sources are sampled most-diagnostic-first.
func TestSPRT_SamplesHighestDFirst(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "lo", "d", 0.6, 1000)
	calibrateSymmetric(c, "mid", "d", 0.8, 1000)
	calibrateSymmetric(c, "hi", "d", 0.95, 1000)

	srcs := []Source{{ID: "lo", EstCostUSD: 1}, {ID: "mid", EstCostUSD: 1}, {ID: "hi", EstCostUSD: 1}}
	exec := &scriptExec{seq: []string{"A", "A", "A"}}
	res, err := Run(context.Background(), Task{Domain: "d"}, srcs, exec, c, Target{Confidence: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ledger[0].Source != "hi" {
		t.Errorf("first sampled = %q, want hi (highest D)", res.Ledger[0].Source)
	}
}

// Law 2 guard: an uninformative (coin-flip) source moves confidence essentially
// nowhere — a fleet of them never reaches a decision and stays at σ(0)=0.5.
func TestSPRT_MiscalibratedSourceContributesNothing(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "coin", "d", 0.5, 1000)

	seq := make([]string, 20)
	for i := range seq {
		seq[i] = "A"
	}
	res, err := Run(context.Background(), Task{Domain: "d"}, nSources("coin", 20, 1),
		&scriptExec{seq: seq}, c, Target{Confidence: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionStoppedOnBudget {
		t.Errorf("coin sources should never accept, got %v", res.Decision)
	}
	if math.Abs(res.Confidence-0.5) > 0.02 {
		t.Errorf("confidence = %.4f, want ≈0.5 (coin carries no information)", res.Confidence)
	}
}

// A behavior-based comparator lifts the "two correct answers disagree" cap: three
// differently-worded but equivalent answers must accumulate agreement (Λ → accept),
// where the default text check counts them as disagreement and never accepts. This
// is the mechanism that resolves the 32.9% real-confidence cap (findings §3).
func TestSPRT_BehavioralEquivalenceLiftsAgreement(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)

	// Correct answers, worded differently — a textual check calls these "disagree".
	seq := func() []string {
		return []string{"return a + b", "func sum(a,b int) int { return b+a }", "the sum: a plus b"}
	}

	// Default (text) equivalence: differing texts never converge → not accepted.
	def, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 3, 1),
		&scriptExec{seq: seq()}, c, Target{Confidence: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if def.Decision == DecisionAccept {
		t.Fatalf("precondition: default text check should NOT accept three differing texts")
	}

	// Behavioral equivalence (all three are equivalent solutions): agreement accrues.
	allEquiv := func(_, _ string) bool { return true }
	beh, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 3, 1),
		&scriptExec{seq: seq()}, c, Target{Confidence: 0.95}, WithEquivalence(allEquiv))
	if err != nil {
		t.Fatal(err)
	}
	if beh.Decision != DecisionAccept {
		t.Errorf("behavioral equivalence: decision = %v, want accept", beh.Decision)
	}
	if beh.Confidence <= def.Confidence {
		t.Errorf("behavioral equivalence should raise confidence: beh=%.4f def=%.4f", beh.Confidence, def.Confidence)
	}
}

// A nil comparator (WithEquivalence(nil)) is ignored — the default is kept.
func TestSPRT_WithEquivalenceNilKeepsDefault(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)
	res, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 5, 1),
		&scriptExec{seq: []string{"A", "A", "A", "A", "A"}}, c, Target{Confidence: 0.95}, WithEquivalence(nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAccept {
		t.Errorf("nil equivalence should fall back to default text check; decision = %v", res.Decision)
	}
}

// TextEquivalence is case- and whitespace-insensitive but not semantic.
func TestTextEquivalence(t *testing.T) {
	if !TextEquivalence("Return  A+B", "return a+b") {
		t.Error("case/whitespace differences should be equivalent under the text default")
	}
	if TextEquivalence("return a+b", "return b+a") {
		t.Error("materially different text should NOT be equivalent under the text default")
	}
}

// probExec answers `truth` with probability p, else `wrong` — a stochastic model.
type probExec struct {
	truth, wrong string
	p            float64
	rng          *rand.Rand
}

func (e *probExec) Execute(_ context.Context, src Source, _ Task) (Answer, error) {
	if e.rng.Float64() < e.p {
		return Answer{Text: e.truth, CostUSD: src.EstCostUSD}, nil
	}
	return Answer{Text: e.wrong, CostUSD: src.EstCostUSD}, nil
}

// Statistical acceptance (Manifesto Law 3): adaptive SPRT reaches target
// confidence in far fewer samples than fixed-5 swarm, at ≥95% accuracy, and
// spends more on harder tasks. Deterministic via a fixed seed.
func TestSPRT_Law3_SamplesAndAccuracy(t *testing.T) {
	const trials = 20000
	rng := rand.New(rand.NewSource(42))

	run := func(id string, p float64, pool int) (meanSamples, accuracy float64) {
		c, _ := New("")
		calibrateSymmetric(c, id, "d", p, 2000)
		var totSamples, correct int
		for i := 0; i < trials; i++ {
			exec := &probExec{truth: "A", wrong: "B", p: p, rng: rng}
			res, _ := Run(context.Background(), Task{Domain: "d"}, nSources(id, pool, 1), exec, c, Target{Confidence: 0.95})
			totSamples += res.Samples
			if res.Candidate == "A" {
				correct++
			}
		}
		return float64(totSamples) / trials, float64(correct) / trials
	}

	const fixedSwarm = 5.0
	easyMean, easyAcc := run("easy", 0.90, 30)
	hardMean, hardAcc := run("hard", 0.74, 30)
	blendedMean := 0.71*easyMean + 0.29*hardMean
	blendedAcc := 0.71*easyAcc + 0.29*hardAcc
	t.Logf("easy(p=.90): mean=%.3f (%.0f%% fewer than fixed-5) acc=%.4f",
		easyMean, 100*(1-easyMean/fixedSwarm), easyAcc)
	t.Logf("hard(p=.74): mean=%.3f (SPRT samples MORE to hold accuracy) acc=%.4f", hardMean, hardAcc)
	t.Logf("blended:     mean=%.3f (%.0f%% fewer than fixed-5) acc=%.4f",
		blendedMean, 100*(1-blendedMean/fixedSwarm), blendedAcc)

	// NOTE: these land above the Manifesto's *continuous* E[N] ([MODEL]: easy
	// 1.33, blended 2.48) because evidence arrives in quantized ~2.19-nat steps —
	// a single vote is below the accept threshold A=ln(19)=2.94, so an easy task
	// needs ≥2 corroborating votes. That is faithful discrete SPRT, not a defect.

	// Easy tasks clear in ≥40% fewer calls than a fixed-5 swarm (Law 3 headline).
	if easyMean > 3.0 {
		t.Errorf("easy mean samples = %.3f, want ≤3 (≥40%% fewer than fixed-5)", easyMean)
	}
	// The blended workload still beats fixed-5 outright.
	if blendedMean >= fixedSwarm {
		t.Errorf("blended mean samples = %.3f, want < %.0f (adaptive beats fixed)", blendedMean, fixedSwarm)
	}
	// SPRT spends more on harder tasks — that is the point, and fixed-N can't.
	if hardMean <= easyMean {
		t.Errorf("hard mean (%.3f) should exceed easy mean (%.3f)", hardMean, easyMean)
	}
	// Calibration guarantee: ~95% accuracy regardless of difficulty (Law 3).
	if easyAcc < 0.95 || hardAcc < 0.95 || blendedAcc < 0.95 {
		t.Errorf("accuracy below target: easy=%.4f hard=%.4f blended=%.4f, want ≥0.95",
			easyAcc, hardAcc, blendedAcc)
	}
}
