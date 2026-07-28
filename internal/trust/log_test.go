// SPDX-License-Identifier: MIT

package trust

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writeTestRegistry(t *testing.T, dir string) {
	t.Helper()
	regDir := filepath.Join(dir, "registry")
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"routing.yaml", "models.yaml", "domains.yaml"} {
		if err := os.WriteFile(filepath.Join(regDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLogRun_StampsConfigBreadcrumbWhenBlank(t *testing.T) {
	home := t.TempDir()
	writeTestRegistry(t, home)
	t.Setenv("HYDRA_HOME", home)

	path := filepath.Join(t.TempDir(), "trust.jsonl")
	if err := LogRun(path, RunLog{TaskHash: "abc"}); err != nil {
		t.Fatal(err)
	}
	runs, err := LoadRuns(path)
	if err != nil || len(runs) != 1 {
		t.Fatalf("LoadRuns = %d runs, err %v", len(runs), err)
	}
	if runs[0].Config == "" {
		t.Error("LogRun should stamp Config from config.Breadcrumb when blank and a registry is available")
	}
}

func TestLogRun_ConfigOmittedGracefullyWithoutRegistry(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir()) // no registry/ present

	path := filepath.Join(t.TempDir(), "trust.jsonl")
	if err := LogRun(path, RunLog{TaskHash: "abc"}); err != nil {
		t.Fatalf("LogRun should not fail when the breadcrumb is unavailable: %v", err)
	}
	runs, _ := LoadRuns(path)
	if len(runs) != 1 || runs[0].Config != "" {
		t.Errorf("Config should stay empty when registry files are unreadable, got %+v", runs)
	}
}

func TestTaskHash_StableAndDistinct(t *testing.T) {
	if TaskHash("hello") != TaskHash("hello") {
		t.Error("TaskHash not stable for identical input")
	}
	if TaskHash("hello") == TaskHash("world") {
		t.Error("TaskHash collided on distinct inputs")
	}
	if len(TaskHash("x")) != 8 {
		t.Errorf("TaskHash length = %d, want 8 hex chars", len(TaskHash("x")))
	}
}

func TestLogRun_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.jsonl")

	in := RunLog{
		TaskHash: "abc123", Domain: "go", TargetConf: 0.95, FinalConf: 0.97,
		Samples: 3, Models: []string{"a", "b"}, CostUSD: 0.012,
		CostSource: "estimated", Decision: "accept",
		Ledger: []Evidence{{Source: "a", Agreed: true, LLR: 2.2, LambdaAfter: 2.2}},
	}
	if err := LogRun(path, in); err != nil {
		t.Fatal(err)
	}
	runs, err := LoadRuns(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("loaded %d runs, want 1", len(runs))
	}
	got := runs[0]
	if got.TS == "" {
		t.Error("LogRun should stamp TS when blank")
	}
	if got.TaskHash != "abc123" || got.Samples != 3 || got.Decision != "accept" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if len(got.Ledger) != 1 || got.Ledger[0].Source != "a" {
		t.Errorf("ledger not preserved: %+v", got.Ledger)
	}
}

func TestLoadRuns_MissingFile(t *testing.T) {
	runs, err := LoadRuns(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if runs != nil {
		t.Errorf("missing file should yield nil runs, got %v", runs)
	}
}

func TestAggregate(t *testing.T) {
	runs := []RunLog{
		{Samples: 2, TargetConf: 0.95, FinalConf: 0.98, CostUSD: 0.01, Decision: "accept"},
		{Samples: 4, TargetConf: 0.95, FinalConf: 0.96, CostUSD: 0.02, Decision: "accept"},
		{Samples: 6, TargetConf: 0.95, FinalConf: 0.80, CostUSD: 0.03, Decision: "stopped_on_budget"},
	}
	s := Aggregate(runs, 5)

	if s.Runs != 3 {
		t.Errorf("runs = %d, want 3", s.Runs)
	}
	if math.Abs(s.MeanSamples-4.0) > 1e-9 { // (2+4+6)/3
		t.Errorf("mean samples = %v, want 4", s.MeanSamples)
	}
	// mean 4 vs fixed-5 → 20% saved.
	if math.Abs(s.SamplesSavedPct-20.0) > 1e-9 {
		t.Errorf("samples saved = %v%%, want 20", s.SamplesSavedPct)
	}
	// 2 of 3 accepted → 66.7% auto-cleared.
	if math.Abs(s.AutoClearedPct-200.0/3) > 1e-6 {
		t.Errorf("auto-cleared = %v%%, want ≈66.7", s.AutoClearedPct)
	}
	if math.Abs(s.TotalCostUSD-0.06) > 1e-9 {
		t.Errorf("total cost = %v, want 0.06", s.TotalCostUSD)
	}
}

func TestAggregate_Empty(t *testing.T) {
	s := Aggregate(nil, 5)
	if s.Runs != 0 || s.MeanSamples != 0 {
		t.Errorf("empty aggregate should be zero-valued, got %+v", s)
	}
}
