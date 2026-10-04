// SPDX-License-Identifier: MIT

package main

import (
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
	"github.com/ankit373/hydra/internal/trust"
)

// seedEnsembleRun writes one SPRT run's evidence ledger under a span, which is
// what a verdict on that span has to find.
func seedEnsembleRun(t *testing.T, span string) {
	t.Helper()
	err := trust.LogRun(trust.DefaultLogPath(), trust.RunLog{
		TaskHash: "abcd1234", Domain: "go", SpanID: span,
		Ledger: []trust.Evidence{
			{Source: "ollama/a", Agreed: true, Candidate: "the answer"},
			{Source: "ollama/b", Agreed: false, Candidate: "the answer"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// negatives is how many "said incorrect" verdicts this source carries. Only a
// replay can produce one: every other writer records saidCorrect=true, because
// a generator asserts its own answer (#771).
func negatives(t *testing.T, source string) float64 {
	t.Helper()
	cal, err := trust.New(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range cal.Report() {
		if s.Source == source && s.Domain == "go" {
			return s.Neg
		}
	}
	return 0
}

// The loop #1144 is about: a verdict lands on a span, the ledger under that
// span is replayed, and the head that dissented from an answer later judged
// correct records the true negative nothing else can produce.
func TestReplayTrustRun_TrainsTheDissenterFromAVerdictOnItsSpan(t *testing.T) {
	testutil.NewSandbox(t)
	seedEnsembleRun(t, "span-abc")

	if n := negatives(t, "ollama/b"); n != 0 {
		t.Fatalf("the dissenter already carries %v negatives before any verdict", n)
	}

	replayTrustRun("span-abc", true)

	if n := negatives(t, "ollama/b"); n == 0 {
		t.Error("a verdict on the span trained nothing: the dissenter carries no negative, " +
			"so specificity stays on its prior and --confidence refuses for want of evidence")
	}
}

// Replaying one ledger twice counts every vote twice, so the calibration says
// more than was measured. A second verdict on the same span must change nothing.
func TestReplayTrustRun_IsIdempotentPerSpan(t *testing.T) {
	testutil.NewSandbox(t)
	seedEnsembleRun(t, "span-abc")

	replayTrustRun("span-abc", true)
	first := negatives(t, "ollama/b")
	replayTrustRun("span-abc", true)

	if got := negatives(t, "ollama/b"); got != first {
		t.Errorf("a second verdict on the same span trained again: %v then %v", first, got)
	}
}

// A span nothing recorded trains nothing, rather than reaching for whatever run
// happens to be newest.
func TestReplayTrustRun_RefusesASpanNoRunWasLoggedUnder(t *testing.T) {
	testutil.NewSandbox(t)
	seedEnsembleRun(t, "span-abc")

	replayTrustRun("span-nothing-here", true)

	if n := negatives(t, "ollama/b"); n != 0 {
		t.Errorf("a verdict on an unrelated span trained %v negatives into this run's ledger", n)
	}
	replayTrustRun("", true)
	if n := negatives(t, "ollama/b"); n != 0 {
		t.Errorf("an empty span trained %v negatives", n)
	}
}
