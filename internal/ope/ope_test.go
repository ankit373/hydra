// SPDX-License-Identifier: MIT

package ope

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"
)

// The failure this package exists to prevent: a per-head retention rule makes
// the naive average rank the worse head first, and the router acts on it.
func TestRecoversInvertedHeadRanking(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	heads := map[string]struct{ rate, keepOK float64 }{
		"agy:sonnet":  {0.92, 0.02}, // genuinely better, lightly sampled
		"ollama:qwen": {0.86, 0.40}, // worse, scrutinised harder
	}
	kept := map[string][]Sample{}
	naiveNum := map[string]float64{}
	naiveDen := map[string]float64{}
	for i := 0; i < 300000; i++ {
		id := "agy:sonnet"
		if i%2 == 0 {
			id = "ollama:qwen"
		}
		h := heads[id]
		ok := rng.Float64() < h.rate
		p := 1.0
		if ok {
			p = h.keepOK
		}
		if rng.Float64() >= p {
			continue // dropped by the retention rule
		}
		v := 0.0
		if ok {
			v = 1
		}
		kept[id] = append(kept[id], Sample{Value: v, Prob: p})
		naiveNum[id] += v
		naiveDen[id]++
	}

	naive := map[string]float64{}
	corrected := map[string]float64{}
	for id := range heads {
		naive[id] = naiveNum[id] / naiveDen[id]
		got, skipped, err := SelfNormalized(kept[id])
		if err != nil || skipped != 0 {
			t.Fatalf("%s: err=%v skipped=%d", id, err, skipped)
		}
		corrected[id] = got
		if want := heads[id].rate; math.Abs(got-want) > 0.01 {
			t.Errorf("%s: corrected %.4f, true %.4f", id, got, want)
		}
	}

	// The point of the test: naive inverts the ranking, corrected does not.
	if !(naive["ollama:qwen"] > naive["agy:sonnet"]) {
		t.Fatalf("expected the naive read to invert the ranking, got %v", naive)
	}
	if !(corrected["agy:sonnet"] > corrected["ollama:qwen"]) {
		t.Fatalf("correction failed to restore the ranking: %v", corrected)
	}
	t.Logf("naive %v", naive)
	t.Logf("corrected %v", corrected)
}

func TestSelfNormalizedUniformMatchesPlainMean(t *testing.T) {
	in := []Sample{{1, 0.5}, {0, 0.5}, {1, 0.5}, {1, 0.5}}
	got, skipped, err := SelfNormalized(in)
	if err != nil || skipped != 0 {
		t.Fatalf("err=%v skipped=%d", err, skipped)
	}
	if math.Abs(got-0.75) > 1e-12 {
		t.Errorf("got %v, want 0.75", got)
	}
}

// A corrupt probability must be skipped and counted, never treated as certain:
// weighting by 1/0 would let one bad row dominate every estimate.
func TestSelfNormalizedSkipsUnusableProbabilities(t *testing.T) {
	in := []Sample{{1, 0}, {1, -0.5}, {0, 1.5}, {1, math.NaN()}, {1, 0.5}, {0, 0.5}}
	got, skipped, err := SelfNormalized(in)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if skipped != 4 {
		t.Errorf("skipped %d, want 4", skipped)
	}
	if math.Abs(got-0.5) > 1e-12 {
		t.Errorf("got %v, want 0.5", got)
	}
}

func TestSelfNormalizedNoUsableSamples(t *testing.T) {
	for _, in := range [][]Sample{nil, {}, {{1, 0}}, {{1, -1}, {0, 2}}} {
		_, _, err := SelfNormalized(in)
		if !errors.Is(err, ErrNoUsableSamples) {
			t.Errorf("in=%v: err=%v, want ErrNoUsableSamples", in, err)
		}
	}
}

// A NaN value cannot be averaged; it must be skipped rather than poison the sum.
func TestSelfNormalizedSkipsNaNValue(t *testing.T) {
	got, skipped, err := SelfNormalized([]Sample{{math.NaN(), 0.5}, {1, 0.5}})
	if err != nil || skipped != 1 {
		t.Fatalf("err=%v skipped=%d", err, skipped)
	}
	if got != 1 {
		t.Errorf("got %v, want 1", got)
	}
}

// Rare-but-retained rows must carry proportionally more weight; that is the
// whole mechanism.
func TestSelfNormalizedWeightsByInverseProbability(t *testing.T) {
	// One success kept with p=0.01 stands for 100 dispatches; one failure kept
	// with p=1 stands for one. The mean should sit near 100/101.
	got, _, err := SelfNormalized([]Sample{{1, 0.01}, {0, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if want := 100.0 / 101.0; math.Abs(got-want) > 1e-12 {
		t.Errorf("got %v, want %v", got, want)
	}
}
