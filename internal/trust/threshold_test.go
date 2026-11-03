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

// honestSource states p and is right at rate p.
func honestSource(rng *rand.Rand, n int) []reliability.Observation {
	obs := make([]reliability.Observation, n)
	for i := range obs {
		stated := rng.Float64()
		obs[i] = reliability.Observation{Predicted: stated, Correct: rng.Float64() < stated}
	}
	return obs
}

// The chosen cutoff must keep the accepted error rate under target on data it never saw.
func TestChooseThreshold_HoldsTargetOnFreshData(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	th, err := ChooseThreshold(honestSource(rng, 10000), 0.1, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	accepted, wrong := 0, 0
	for _, o := range honestSource(rng, 100000) {
		if o.Predicted < th.Accept {
			continue
		}
		accepted++
		if !o.Correct {
			wrong++
		}
	}
	rate := float64(wrong) / float64(accepted)
	if rate > th.Target {
		t.Errorf("fresh error rate %.4f at accept %.2f, want <= %.2f", rate, th.Accept, th.Target)
	}
}

func TestChooseThreshold_ReportsTargetConfidenceAndSplit(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	th, err := ChooseThreshold(honestSource(rng, 10000), 0.1, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if th.Target != 0.1 || th.Confidence != 0.9 || th.Split != 10000 {
		t.Errorf("threshold = %+v, want target 0.1, confidence 0.9, split 10000", th)
	}
	if th.Accepted == 0 || th.Errors > th.Accepted {
		t.Errorf("threshold = %+v, want a non-empty accepted set with errors inside it", th)
	}
}

func TestChooseThreshold_BelowFloorIsRefused(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	_, err := ChooseThreshold(honestSource(rng, reliability.MinObservations-1), 0.1, 0.9)
	if !errors.Is(err, reliability.ErrTooFew) {
		t.Fatalf("err = %v, want ErrTooFew", err)
	}
}

// A coin flip at every stated value has no cutoff that clears a 10% error rate.
func TestChooseThreshold_UnreachableTarget(t *testing.T) {
	rng := rand.New(rand.NewSource(14))
	obs := make([]reliability.Observation, 2000)
	for i := range obs {
		obs[i] = reliability.Observation{Predicted: rng.Float64(), Correct: rng.Intn(2) == 0}
	}
	if _, err := ChooseThreshold(obs, 0.1, 0.9); !errors.Is(err, ErrNoThreshold) {
		t.Fatalf("err = %v, want ErrNoThreshold", err)
	}
}

func TestChooseThreshold_RefusesBadArguments(t *testing.T) {
	obs := honestSource(rand.New(rand.NewSource(15)), 100)
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
			if _, err := ChooseThreshold(obs, tc.target, tc.confidence); err == nil {
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
	th := Threshold{Accept: 0.8}
	sources := nSources("sim", 5, 1)
	target := Target{Confidence: 0.95}
	ctx := context.Background()
	res, err := Run(ctx, Task{Domain: "d"}, sources, exec, c, target, WithAcceptThreshold(th))
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != DecisionAccept || res.Samples != wantSamples {
		t.Errorf("decision %v after %d samples, want accept after %d", res.Decision, res.Samples, wantSamples)
	}
}

// With no verdicts, routing is byte-identical to a run that never heard of
// thresholds, and that run still stops where Wald's bound says it should.
func TestRun_NoDataRoutesByteIdentical(t *testing.T) {
	th, chooseErr := ChooseThreshold(nil, 0.05, 0.9)
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
	_, after := run(WithAcceptThreshold(th))
	if !bytes.Equal(before, after) {
		t.Errorf("no-data threshold changed the run:\nbefore %s\nafter  %s", before, after)
	}
	// ln 19 at about ln 4 a vote needs three net agreeing votes, the seventh sample here.
	if plain.Decision != DecisionAccept || plain.Samples != 7 {
		t.Errorf("decision %v after %d samples, want accept after 7", plain.Decision, plain.Samples)
	}
}
