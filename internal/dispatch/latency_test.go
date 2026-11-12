// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/cost"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/signals"
	"github.com/ankit373/hydra/internal/sketch"
	"github.com/ankit373/hydra/internal/testutil"
)

func sketchOf(vals ...float64) *sketch.Sketch {
	sk := sketch.New(sketch.DefaultAlpha)
	for _, v := range vals {
		sk.Add(v)
	}
	return sk
}

// The head id the writer recorded wins over the model string beside it, which
// is a provider's own name for the model and often resolves to nothing.
func TestLatencyIndex_KeysOnTheRecordedHead(t *testing.T) {
	idx := latencyIndex([]cost.Row{
		{Model: "claude-sonnet-4-5-20250929", Head: "anthropic", WallMS: 100},
		{Model: "claude-sonnet-4-5-20250929", Head: "anthropic", WallMS: 300},
	})
	if got := idx["anthropic"]; got == nil || got.Count() != 2 {
		t.Fatalf("rows did not land under their recorded head: %v", idx)
	}
}

// A row older than the Head field resolves through declared names only;
// perModelPrefixes is a fixed pattern, so this holds on any machine.
func TestLatencyIndex_ResolvesALegacyRowByModelName(t *testing.T) {
	idx := latencyIndex([]cost.Row{{Model: "qwen3:8b (Ollama)", WallMS: 100}})
	if got := idx["ollama/qwen3:8b"]; got == nil || got.Count() != 1 {
		t.Fatalf("a resolvable legacy row did not reach its head id: %v", idx)
	}
}

func TestLatencyP95_PresentWhenTheIndexKnowsTheHead(t *testing.T) {
	d := &Dispatcher{latency: map[string]*sketch.Sketch{"ollama/qwen3:8b": sketchOf(100, 200, 300, 9000)}}
	got, ok := d.latencyP95(provider.Head{ID: "ollama/qwen3:8b"})
	if !ok {
		t.Fatal("a head with recorded latency reported none")
	}
	if want := d.latency["ollama/qwen3:8b"].Quantile(0.95); got != want {
		t.Errorf("got %v, want the sketch's own p95 %v", got, want)
	}
}

func TestLatencyP95_AbsentWhenTheIndexHasNothingForTheHead(t *testing.T) {
	d := &Dispatcher{latency: map[string]*sketch.Sketch{"ollama/qwen3:8b": sketchOf(100)}}
	if _, ok := d.latencyP95(provider.Head{ID: "ollama/other"}); ok {
		t.Error("a head with no recorded latency still reported one")
	}
}

func writeCostLog(t *testing.T, home string, lines ...string) {
	t.Helper()
	dir := filepath.Join(home, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "cost.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func latencyRule(t *testing.T) *signals.Engine {
	t.Helper()
	eng, err := signals.Parse([]byte(`version: 1
rules:
  - name: slow
    when: latency.p95_ms > 1000
    action: {type: route, tier: "4"}
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// The signal reads the live cost log, so a row written after the last
// `hyctl stats --latency` still counts; rollups.jsonl is not consulted.
func TestDecide_LatencyP95ReadsTheCostLogWhenARuleAsks(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeCostLog(t, s.HydraHome,
		`{"ts":"2026-11-01T00:00:00Z","model":"m","head":"h","wall_ms":5000}`)
	d := &Dispatcher{heads: []provider.Head{{ID: "h"}}, rules: latencyRule(t)}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "h")
	got, ok := dec.Signals[signals.SigLatencyP95MS].(float64)
	if !ok || got < 4900 || got > 5100 {
		t.Fatalf("latency.p95_ms = %v (present=%v), want ~5000 from the cost log", got, ok)
	}
	if dec.Rule != "slow" {
		t.Errorf("rule = %q, want the latency rule to fire", dec.Rule)
	}
}

// No rule names the signal, so the cost log is never read.
func TestDecide_LatencyP95NotLoadedWithoutARule(t *testing.T) {
	s := testutil.NewSandbox(t)
	writeCostLog(t, s.HydraHome,
		`{"ts":"2026-11-01T00:00:00Z","model":"m","head":"h","wall_ms":5000}`)
	d := &Dispatcher{heads: []provider.Head{{ID: "h"}}}
	d.Decide(context.Background(), "hello", "go", nil, nil, "h")
	if d.latency != nil {
		t.Error("the cost log was read although no rule names latency.p95_ms")
	}
}

// A machine that has dispatched nothing has no cost log; absent, not an error.
func TestDecide_LatencyP95AbsentWithNoCostLog(t *testing.T) {
	testutil.NewSandbox(t)
	d := &Dispatcher{heads: []provider.Head{{ID: "h"}}, rules: latencyRule(t)}
	dec := d.Decide(context.Background(), "hello", "go", nil, nil, "h")
	if _, ok := dec.Signals[signals.SigLatencyP95MS]; ok {
		t.Error("latency.p95_ms read a value with no cost log at all")
	}
}
