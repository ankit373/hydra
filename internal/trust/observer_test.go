// SPDX-License-Identifier: MIT

package trust

import (
	"context"
	"math"
	"testing"
)

// A surface showing why the ensemble has not stopped needs two numbers that
// exist only in here: the running Λ and the threshold it is walking toward.
func TestRun_ObserverReportsEachEvidenceAgainstTheThreshold(t *testing.T) {
	c, _ := New("")
	calibrateSymmetric(c, "sim", "d", 0.9, 1000)

	var seen []Evidence
	var thresholds []float64
	exec := &scriptExec{seq: []string{"A", "A", "A", "A", "A"}}
	res, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 5, 1), exec, c,
		Target{Confidence: 0.95},
		WithObserver(func(e Evidence, accept float64) {
			seen = append(seen, e)
			thresholds = append(thresholds, accept)
		}))
	if err != nil {
		t.Fatal(err)
	}

	// One call per sample, carrying the entry that was just recorded: an
	// observer that reported anything else would be a second ledger.
	if len(seen) != res.Samples {
		t.Fatalf("observer saw %d entries, want %d (one per sample)", len(seen), res.Samples)
	}
	for i, e := range seen {
		if e != res.Ledger[i] {
			t.Errorf("entry %d = %+v, want the ledger's own %+v", i, e, res.Ledger[i])
		}
	}

	// The Wald accept threshold, not α and not the confidence: a surface that
	// had to rederive it from α would be the second place it is computed.
	wantA := math.Log(0.95 / 0.05)
	for i, got := range thresholds {
		if math.Abs(got-wantA) > 1e-9 {
			t.Errorf("threshold %d = %v, want ln((1-α)/α) = %v", i, got, wantA)
		}
	}

	// The last report is what a panel is left showing, so it has to be the
	// crossing itself rather than the sample before it.
	last := seen[len(seen)-1]
	if last.LambdaAfter < wantA {
		t.Errorf("final Λ reported = %v, below the threshold %v the run accepted at", last.LambdaAfter, wantA)
	}
}

// The observer is optional, and a run without one must reach the same result.
func TestRun_NoObserverIsTheSameRun(t *testing.T) {
	run := func(opts ...RunOption) *Result {
		t.Helper()
		c, _ := New("")
		calibrateSymmetric(c, "sim", "d", 0.9, 1000)
		exec := &scriptExec{seq: []string{"A", "A", "A", "A", "A"}}
		res, err := Run(context.Background(), Task{Domain: "d"}, nSources("sim", 5, 1), exec, c,
			Target{Confidence: 0.95}, opts...)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	bare := run()
	watched := run(WithObserver(func(Evidence, float64) {}))
	nilObserver := run(WithObserver(nil))

	for _, got := range []*Result{watched, nilObserver} {
		if got.Samples != bare.Samples || got.Candidate != bare.Candidate ||
			got.Decision != bare.Decision || got.Lambda != bare.Lambda {
			t.Errorf("observed run = %d samples/%q/%v/%v, want the unobserved run's %d/%q/%v/%v",
				got.Samples, got.Candidate, got.Decision, got.Lambda,
				bare.Samples, bare.Candidate, bare.Decision, bare.Lambda)
		}
	}
}
