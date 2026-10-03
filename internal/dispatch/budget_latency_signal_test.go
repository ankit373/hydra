// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/signals"
	"github.com/ankit373/hydra/internal/sketch"
)

// A head pinned before routing runs (hyctl serve, the desktop picker) is the
// one case these signals can read without deciding which head gets the task;
// its reported window must reach the decision under its own name.
func TestDecide_EffectiveContextPresentWhenHeadPinnedAndReported(t *testing.T) {
	d := &Dispatcher{heads: []provider.Head{
		{ID: "llamacpp/big", Meta: map[string]string{"model_ctx": "32768"}},
	}}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "llamacpp/big")
	got, ok := dec.Signals[signals.SigBudgetEffectiveContext]
	if !ok || got != float64(32768) {
		t.Fatalf("budget.effective_context = %v (present=%v), want 32768", got, ok)
	}
}

// No pin, no fact to report: a plain hyctl dispatch must not invent a window
// for a head nothing has chosen yet.
func TestDecide_EffectiveContextAbsentWhenNoHeadPinned(t *testing.T) {
	d := &Dispatcher{heads: []provider.Head{
		{ID: "llamacpp/big", Meta: map[string]string{"model_ctx": "32768"}},
	}}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "")
	if _, ok := dec.Signals[signals.SigBudgetEffectiveContext]; ok {
		t.Error("budget.effective_context fired with no head pinned to attribute it to")
	}
}

// A head that never reported a window is a real, common state (most cloud
// heads), and must read as absent rather than a window of nothing.
func TestDecide_EffectiveContextAbsentWhenHeadReportsNone(t *testing.T) {
	d := &Dispatcher{heads: []provider.Head{{ID: "ollama/unknown"}}}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "ollama/unknown")
	if _, ok := dec.Signals[signals.SigBudgetEffectiveContext]; ok {
		t.Error("budget.effective_context read a value the head never reported")
	}
}

// A head id this machine never discovered must not error the whole dispatch
// over a signal; it is simply unreadable, same as no pin at all.
func TestDecide_EffectiveContextAbsentWhenPinnedHeadUnknown(t *testing.T) {
	d := &Dispatcher{heads: []provider.Head{
		{ID: "a", Meta: map[string]string{"model_ctx": "4096"}},
	}}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "not-discovered")
	if _, ok := dec.Signals[signals.SigBudgetEffectiveContext]; ok {
		t.Error("budget.effective_context read a value for an undiscovered head")
	}
}

func TestDecide_LatencyP95PresentWhenHeadPinnedAndRecorded(t *testing.T) {
	sk := sketch.New(sketch.DefaultAlpha)
	for _, v := range []float64{100, 200, 300, 9000} {
		sk.Add(v)
	}
	d := &Dispatcher{
		heads:   []provider.Head{{ID: "ollama/qwen3:8b"}},
		latency: map[string]*sketch.Sketch{"ollama/qwen3:8b": sk},
	}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "ollama/qwen3:8b")
	got, ok := dec.Signals[signals.SigLatencyP95MS]
	if !ok {
		t.Fatal("latency.p95_ms is absent despite a pinned, measured head")
	}
	if want := sk.Quantile(0.95); got != want {
		t.Errorf("latency.p95_ms = %v, want %v", got, want)
	}
}

// No rollup history at all (a fresh machine, or one that has never dispatched
// to this head) must read as absent, not a latency of zero.
func TestDecide_LatencyP95AbsentWithNoRollupHistory(t *testing.T) {
	d := &Dispatcher{heads: []provider.Head{{ID: "ollama/qwen3:8b"}}}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "ollama/qwen3:8b")
	if _, ok := dec.Signals[signals.SigLatencyP95MS]; ok {
		t.Error("latency.p95_ms read a value with no rollup history at all")
	}
}

func TestDecide_LatencyP95AbsentWhenNoHeadPinned(t *testing.T) {
	sk := sketch.New(sketch.DefaultAlpha)
	sk.Add(100)
	d := &Dispatcher{
		heads:   []provider.Head{{ID: "ollama/qwen3:8b"}},
		latency: map[string]*sketch.Sketch{"ollama/qwen3:8b": sk},
	}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "")
	if _, ok := dec.Signals[signals.SigLatencyP95MS]; ok {
		t.Error("latency.p95_ms fired with no head pinned to attribute it to")
	}
}

// A rule naming a signal that does not exist fails at load (#903), so both
// new signals have to be declared or no rule can ever name them.
func TestSchema_DeclaresTheBudgetAndLatencySignals(t *testing.T) {
	const rule = `version: 1
rules:
  - name: r
    when: budget.effective_context < 8000 || latency.p95_ms > 5000
    action: {type: route, tier: "4"}
`
	if _, err := signals.Parse([]byte(rule), nil); err != nil {
		t.Fatalf("a rule reading the new signals will not load: %v", err)
	}
}
