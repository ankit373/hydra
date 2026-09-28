// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/reliability"
	"github.com/ankit373/hydra/internal/runlog"
)

// seedScoredRuns writes count confidence-routed runs stating conf, correct at
// rate actual, each with a verdict attached. Written through the real Append and
// AppendScore so the test reads exactly what production would have left behind.
func seedScoredRuns(t *testing.T, rng *rand.Rand, conf, actual float64, count int, scored bool) {
	t.Helper()
	for i := 0; i < count; i++ {
		runID := fmt.Sprintf("run-%.2f-%d", conf, i)
		spanID := runlog.SpanIDFor(runID + "/sprt")
		rl := runlog.New(runID)
		if err := rl.Append(runlog.Event{Kind: runlog.KindRunStarted, TaskID: "t"}); err != nil {
			t.Fatal(err)
		}
		if err := rl.Append(runlog.Event{
			Kind: runlog.KindTaskFinished, TaskID: "t",
			SpanID: spanID, Agent: "sprt", Confidence: conf,
		}); err != nil {
			t.Fatal(err)
		}
		if !scored {
			continue
		}
		value := 0.0
		if rng.Float64() < actual {
			value = 1
		}
		if err := runlog.AppendScore(runID, spanID, runlog.Score{
			Name: "tests", Value: value, Source: "oracle:go-test",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// An empty log must say how to fill it. Erroring here reads as a broken
// command, when the honest answer is "nothing has been judged yet".
func TestCLI_TrustReliabilityOnAnEmptyLogSaysHowToFillIt(t *testing.T) {
	cliSandbox(t)

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("reliability on an empty log errored: %v", err)
	}
	if !strings.Contains(out, "trace score") {
		t.Errorf("does not name the command that records a verdict:\n%s", out)
	}
}

// Too few observations must refuse rather than draw a diagram from noise.
func TestCLI_TrustReliabilityRefusesBelowTheObservationFloor(t *testing.T) {
	cliSandbox(t)
	seedScoredRuns(t, rand.New(rand.NewSource(1)), 0.9, 0.9, reliability.MinObservations-1, true)

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("errored instead of refusing: %v", err)
	}
	if !strings.Contains(out, "too few") {
		t.Errorf("output does not say why it will not report:\n%s", out)
	}
	if strings.Contains(out, "brier") {
		t.Errorf("reported a Brier score below the floor:\n%s", out)
	}
}

// The case the command exists for: stated confidence far above the rate the
// verdicts actually show has to be named as overconfidence, in the human
// output, not only in --json.
func TestCLI_TrustReliabilityNamesOverconfidence(t *testing.T) {
	cliSandbox(t)
	rng := rand.New(rand.NewSource(9))
	seedScoredRuns(t, rng, 0.95, 0.55, 30, true)
	seedScoredRuns(t, rng, 0.85, 0.55, 30, true)

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("trust reliability: %v", err)
	}
	for _, want := range []string{"STATED", "ACTUALLY CORRECT", "brier", "reliability", "resolution", "ECE"} {
		if !strings.Contains(out, want) {
			t.Errorf("render is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "overconfident") {
		t.Errorf("stated ~0.90 against a ~0.55 outcome rate without saying overconfident:\n%s", out)
	}
}

// A run nothing has judged is counted and reported, not silently dropped: a
// report over an unstated subset of the log is misleading.
func TestCLI_TrustReliabilityReportsWhatIsStillUnjudged(t *testing.T) {
	cliSandbox(t)
	rng := rand.New(rand.NewSource(13))
	seedScoredRuns(t, rng, 0.8, 0.8, 25, true)
	seedScoredRuns(t, rng, 0.6, 0.6, 7, false) // logged, never judged

	out, _, err := run(t, "trust", "reliability")
	if err != nil {
		t.Fatalf("trust reliability: %v", err)
	}
	if !strings.Contains(out, "7 awaiting a verdict") {
		t.Errorf("unjudged runs not surfaced:\n%s", out)
	}
}

func TestCLI_TrustReliabilityJSONCarriesTheDecomposition(t *testing.T) {
	cliSandbox(t)
	seedScoredRuns(t, rand.New(rand.NewSource(17)), 0.75, 0.75, 40, true)

	out, _, err := run(t, "trust", "reliability", "--json")
	if err != nil {
		t.Fatalf("trust reliability --json: %v", err)
	}
	var rep struct {
		reliability.Report
		Unscored int `json:"unscored"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if rep.N != 40 {
		t.Errorf("n = %d, want 40", rep.N)
	}
	if want := rep.Reliability - rep.Resolution + rep.Uncertainty; abs(rep.Brier-want) > 1e-9 {
		t.Errorf("brier %.9f != reliability-resolution+uncertainty %.9f", rep.Brier, want)
	}
	if len(rep.Bins) == 0 {
		t.Error("no bins in the JSON, so nothing can plot it")
	}
}

// The refusal path has to be machine-readable too, or a caller polling --json
// cannot tell "not enough data" from a crash.
func TestCLI_TrustReliabilityJSONRefusalIsStructured(t *testing.T) {
	cliSandbox(t)
	seedScoredRuns(t, rand.New(rand.NewSource(21)), 0.9, 0.9, 3, true)

	out, _, err := run(t, "trust", "reliability", "--json")
	if err != nil {
		t.Fatalf("trust reliability --json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("refusal is not JSON: %v\n%s", err, out)
	}
	if got["error"] == nil {
		t.Errorf("refusal carries no error field: %v", got)
	}
	if got["scored"] == nil {
		t.Errorf("refusal does not say how many were scored: %v", got)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
