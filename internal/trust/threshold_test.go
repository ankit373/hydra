// SPDX-License-Identifier: MIT

package trust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"testing"

	"github.com/ankit373/hydra/internal/reliability"
)

// simulatedRun votes for the right answer 80% of the time, A being right 60% of the
// time. Each vote moves the leader's log-odds by logit(0.8), and it stops at 0.99.
func simulatedRun(rng *rand.Rand) heldOutRun {
	aRight := rng.Float64() < 0.6
	step := logit(0.8)
	var lambda float64
	var ledger []Evidence
	for range 8 {
		if (rng.Float64() < 0.8) == aRight {
			lambda += step
		} else {
			lambda -= step
		}
		lead := "A"
		if lambda < 0 {
			lead = "B"
		}
		leadLambda := math.Abs(lambda)
		ledger = append(ledger, Evidence{Candidate: lead, LambdaAfter: leadLambda})
		if leadLambda >= logit(0.99) {
			break
		}
	}
	correct := map[string]bool{"A": aRight, "B": !aRight}
	return heldOutRun{ledger: ledger, correct: correct, exhausted: true}
}

func simulatedRuns(rng *rand.Rand, n int) []heldOutRun {
	runs := make([]heldOutRun, n)
	for i := range runs {
		runs[i] = simulatedRun(rng)
	}
	return runs
}

// sameRun repeats one ledger n times, each judged by the same verdicts.
func sameRun(n int, correct map[string]bool, ledger ...Evidence) []heldOutRun {
	runs := make([]heldOutRun, n)
	for i := range runs {
		runs[i] = heldOutRun{ledger: ledger, correct: correct, exhausted: true}
	}
	return runs
}

// The chosen cutoff must keep the accepted error rate under target on runs it never saw.
func TestChooseThreshold_HoldsTargetOnFreshRuns(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	th, err := chooseThreshold(simulatedRuns(rng, 5000), 0.1, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	accepted, wrong := 0, 0
	for _, r := range simulatedRuns(rng, 50000) {
		correct, stopped := r.stop(th.accept)
		if !stopped {
			continue
		}
		accepted++
		if !correct {
			wrong++
		}
	}
	rate := float64(wrong) / float64(accepted)
	if rate > th.target {
		t.Errorf("fresh error rate %.4f at accept %.2f, want <= %.2f", rate, th.accept, th.target)
	}
}

func TestChooseThreshold_ReportsTargetConfidenceAndSplit(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	th, err := chooseThreshold(simulatedRuns(rng, 5000), 0.1, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if th.target != 0.1 || th.confidence != 0.9 || th.split != 5000 {
		t.Errorf("threshold = %+v, want target 0.1, confidence 0.9, split 5000", th)
	}
	if th.accepted == 0 || th.wrong > th.accepted {
		t.Errorf("threshold = %+v, want a non-empty accepted set with wrong answers inside it", th)
	}
}

func TestChooseThreshold_BelowFloorIsRefused(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	_, err := chooseThreshold(simulatedRuns(rng, reliability.MinObservations-1), 0.1, 0.9)
	if !errors.Is(err, reliability.ErrTooFew) {
		t.Fatalf("err = %v, want ErrTooFew", err)
	}
}

// A leader that is a coin flip at every confidence has no cutoff that clears a 10% error rate.
// A run that stopped at a lower bound never saw the higher cutoffs, so it cannot judge them.
func TestChooseThreshold_RefusesARunStoppedShort(t *testing.T) {
	runs := sameRun(reliability.MinObservations, map[string]bool{"A": true},
		Evidence{Candidate: "A", LambdaAfter: logit(0.95)},
	)
	runs[3].exhausted = false
	if _, err := chooseThreshold(runs, 0.1, 0.9); err == nil {
		t.Error("a run stopped at 0.95 must not judge the cutoffs above it")
	}
}

func TestChooseThreshold_UnreachableTarget(t *testing.T) {
	rng := rand.New(rand.NewSource(14))
	runs := make([]heldOutRun, 2000)
	for i := range runs {
		ledger := []Evidence{
			{Candidate: "A", LambdaAfter: logit(0.995)},
		}
		runs[i] = heldOutRun{ledger: ledger, correct: map[string]bool{"A": rng.Intn(2) == 0}}
	}
	if _, err := chooseThreshold(runs, 0.1, 0.9); !errors.Is(err, errNoThreshold) {
		t.Fatalf("err = %v, want errNoThreshold", err)
	}
}

// Leaders that never pass even odds give no cutoff, not one that accepts on no evidence.
func TestChooseThreshold_NeverAtOrBelowEvenOdds(t *testing.T) {
	correct := map[string]bool{"A": true}
	runs := sameRun(1000, correct, Evidence{Candidate: "A", LambdaAfter: logit(0.3)})
	th, err := chooseThreshold(runs, 0.1, 0.95)
	if !errors.Is(err, errNoThreshold) {
		t.Fatalf("threshold = %+v, err = %v, want errNoThreshold", th, err)
	}
}

// The verdict that counts is the leader's where Run would stop, not where the run ended.
func TestChooseThreshold_JudgesTheLeaderWhereRunStops(t *testing.T) {
	correct := map[string]bool{"B": true}
	runs := sameRun(1000, correct,
		Evidence{Candidate: "A", LambdaAfter: logit(0.9)},
		Evidence{Candidate: "B", LambdaAfter: logit(0.99)},
	)
	th, err := chooseThreshold(runs, 0.1, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	if th.accept <= 0.9 {
		t.Errorf("accept = %.2f, want above 0.9, where every run stops on the wrong answer", th.accept)
	}
}

func TestChooseThreshold_RefusesBadArguments(t *testing.T) {
	runs := simulatedRuns(rand.New(rand.NewSource(15)), 100)
	tests := []struct {
		name               string
		target, confidence float64
	}{
		{name: "zero target", target: 0, confidence: 0.9},
		{name: "target of one", target: 1, confidence: 0.9},
		{name: "zero confidence", target: 0.1, confidence: 0},
		{name: "confidence of one", target: 0.1, confidence: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := chooseThreshold(runs, tc.target, tc.confidence); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestBinomialCDF_KnownValues(t *testing.T) {
	tests := []struct {
		k, n int
		p    float64
		want float64
	}{
		{k: 0, n: 0, p: 0.3, want: 1},
		{k: 0, n: 10, p: 0.1, want: math.Pow(0.9, 10)},
		{k: 1, n: 2, p: 0.5, want: 0.75},
		{k: 2, n: 4, p: 0.5, want: 11.0 / 16},
		{k: 5, n: 5, p: 0.7, want: 1},
	}
	for _, tc := range tests {
		if got := binomialCDF(tc.k, tc.n, tc.p); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("binomialCDF(%d, %d, %v) = %v, want %v", tc.k, tc.n, tc.p, got, tc.want)
		}
	}
}

// A chosen cutoff below Wald's bound lets the run stop sooner.
func TestRun_AcceptThresholdMovesTheStop(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)
	L := c.LLR("sim", "d", true)
	wantSamples := int(math.Ceil(logit(0.8) / L))

	exec := &scriptExec{seq: []string{"A", "A", "A", "A", "A"}}
	th := threshold{accept: 0.8}
	sources := nSources("sim", 5, 1)
	target := Target{Confidence: 0.95}
	ctx := context.Background()
	res, err := Run(ctx, Task{Domain: "d"}, sources, exec, c, target, withAcceptThreshold(th))
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAccept || res.Samples != wantSamples {
		t.Errorf("decision %v after %d samples, want accept after %d", res.Decision, res.Samples, wantSamples)
	}
}

// Λ starts at 0, so a cutoff at or below even odds must not accept before any evidence.
func TestRun_CutoffAtOrBelowEvenOddsIsIgnored(t *testing.T) {
	for _, accept := range []float64{0.3, 0.5} {
		c, _ := New("")
		exec := &scriptExec{seq: []string{"A", "A", "A"}}
		sources := nSources("uncalibrated", 3, 0)
		target := Target{Confidence: 0.95}
		opts := []RunOption{
			AllowNoEvidence(),
			withAcceptThreshold(threshold{accept: accept}),
		}
		res, err := Run(context.Background(), Task{Domain: "d"}, sources, exec, c, target, opts...)
		if err != nil {
			t.Fatal(err)
		}
		if res.Decision != DecisionStoppedOnBudget || res.Samples != 3 {
			t.Errorf("accept %v: decision %v after %d samples, want stopped_on_budget after 3",
				accept, res.Decision, res.Samples)
		}
	}
}

// With no verdicts, routing is byte-identical to a run that never heard of
// thresholds, and that run still stops where Wald's bound says it should.
func TestRun_NoDataRoutesByteIdentical(t *testing.T) {
	th, chooseErr := chooseThreshold(nil, 0.05, 0.9)
	if !errors.Is(chooseErr, reliability.ErrTooFew) {
		t.Fatalf("err = %v, want ErrTooFew", chooseErr)
	}
	run := func(opts ...RunOption) (*Result, []byte) {
		c, _ := New("")
		calibrateSymmetric(c, "sim", "d", 0.8, 1000)
		exec := &scriptExec{seq: []string{"A", "B", "A", "A", "B", "A", "A", "A"}}
		sources := nSources("sim", 8, 0.01)
		target := Target{Confidence: 0.95}
		res, err := Run(context.Background(), Task{Domain: "d"}, sources, exec, c, target, opts...)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		return res, raw
	}
	plain, before := run()
	_, after := run(withAcceptThreshold(th))
	if !bytes.Equal(before, after) {
		t.Errorf("no-data threshold changed the run:\nbefore %s\nafter  %s", before, after)
	}
	// ln 19 at about ln 4 a vote needs three net agreeing votes, the seventh sample here.
	if plain.Decision != DecisionAccept || plain.Samples != 7 {
		t.Errorf("decision %v after %d samples, want accept after 7", plain.Decision, plain.Samples)
	}
}
