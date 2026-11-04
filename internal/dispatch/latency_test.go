// SPDX-License-Identifier: MIT

package dispatch

import (
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rollup"
	"github.com/ankit373/hydra/internal/sketch"
)

func sketchOf(vals ...float64) *sketch.Sketch {
	sk := sketch.New(sketch.DefaultAlpha)
	for _, v := range vals {
		sk.Add(v)
	}
	return sk
}

// perModelPrefixes ("X (Ollama)" -> "ollama/X") is a fixed pattern, not a
// models.yaml declaration, so this resolves the same regardless of what is
// installed on the machine running the test.
func TestLatencyIndex_ResolvesAKnownModelNameToItsHeadID(t *testing.T) {
	idx := latencyIndex([]rollup.Row{
		{Key: rollup.Key{Model: "qwen3:8b (Ollama)", Executor: "ollama"}, Latency: sketchOf(100, 120, 140, 900)},
	})
	got, ok := idx["ollama/qwen3:8b"]
	if !ok {
		t.Fatalf("a resolvable model name did not reach its head id: %v", idx)
	}
	if got.Count() != 4 {
		t.Errorf("count = %d, want 4", got.Count())
	}
}

// A model name nothing declares must not be guessed at: merging it into the
// wrong head's figure is worse than leaving that head's figure alone.
func TestLatencyIndex_UnresolvableModelNameIsDropped(t *testing.T) {
	idx := latencyIndex([]rollup.Row{
		{Key: rollup.Key{Model: "some made up model nobody declared"}, Latency: sketchOf(100)},
	})
	if len(idx) != 0 {
		t.Errorf("an unattributable model name was attributed to a head: %v", idx)
	}
}

// Two rows for the same head, e.g. two different days or enums, must merge
// into one figure rather than the second silently replacing the first.
func TestLatencyIndex_MergesRowsForTheSameHead(t *testing.T) {
	idx := latencyIndex([]rollup.Row{
		{Key: rollup.Key{Date: "2026-01-01", Model: "qwen3:8b (Ollama)"}, Latency: sketchOf(100, 110)},
		{Key: rollup.Key{Date: "2026-01-02", Model: "qwen3:8b (Ollama)"}, Latency: sketchOf(120)},
	})
	got, ok := idx["ollama/qwen3:8b"]
	if !ok || got.Count() != 3 {
		t.Fatalf("got %v ok=%v, want one merged sketch with count 3", got, ok)
	}
}

// A row with no observations yet, or no sketch at all, must not produce a
// head entry that reports a quantile on nothing.
func TestLatencyIndex_SkipsEmptyRows(t *testing.T) {
	idx := latencyIndex([]rollup.Row{
		{Key: rollup.Key{Model: "qwen3:8b (Ollama)"}, Latency: sketch.New(sketch.DefaultAlpha)},
		{Key: rollup.Key{Model: "qwen3:8b (Ollama)"}, Latency: nil},
	})
	if len(idx) != 0 {
		t.Errorf("an empty sketch produced a head entry: %v", idx)
	}
}

func TestLatencyP95_PresentWhenTheIndexKnowsTheHead(t *testing.T) {
	d := &Dispatcher{latency: map[string]*sketch.Sketch{"ollama/qwen3:8b": sketchOf(100, 200, 300, 9000)}}
	h := provider.Head{ID: "ollama/qwen3:8b"}
	got, ok := d.latencyP95(h)
	if !ok {
		t.Fatal("a head with rollup history reported no latency")
	}
	if want := d.latency["ollama/qwen3:8b"].Quantile(0.95); got != want {
		t.Errorf("got %v, want the sketch's own p95 %v", got, want)
	}
}

func TestLatencyP95_AbsentWhenTheIndexHasNothingForTheHead(t *testing.T) {
	d := &Dispatcher{latency: map[string]*sketch.Sketch{"ollama/qwen3:8b": sketchOf(100)}}
	if _, ok := d.latencyP95(provider.Head{ID: "ollama/other"}); ok {
		t.Error("a head with no rollup history still reported a latency")
	}
	// A nil index, the state of a machine with no rollups at all, must answer
	// the same way rather than panicking on a nil map read.
	d2 := &Dispatcher{}
	if _, ok := d2.latencyP95(provider.Head{ID: "ollama/qwen3:8b"}); ok {
		t.Error("a nil latency index still reported a latency")
	}
}
