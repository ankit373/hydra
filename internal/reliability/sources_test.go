// SPDX-License-Identifier: MIT

package reliability

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
)

// A source that states 0.95 and is right 60% of the time must read worse than
// one whose stated numbers hold up, or the report cannot pick between them.
func TestEvaluateSources_OverconfidentReadsWorseThanCalibrated(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	honest := Key{Source: "honest", Domain: "go"}
	boastful := Key{Source: "boastful", Domain: "go"}
	tallies := map[Key]*Tally{
		honest:   {Observations: forecaster(rng, 400, 0.7, 0.7)},
		boastful: {Observations: forecaster(rng, 400, 0.95, 0.6)},
	}
	got, err := EvaluateSources(tallies, 0)
	if err != nil {
		t.Fatal(err)
	}
	reports := map[Key]*Report{}
	for _, sr := range got {
		reports[sr.Key] = sr.Report
	}
	if reports[boastful].ECE <= reports[honest].ECE {
		t.Errorf("boastful ECE %.4f is not worse than honest %.4f",
			reports[boastful].ECE, reports[honest].ECE)
	}
	if !reports[boastful].Overconfident() {
		t.Error("a source stating 0.95 and right 60% of the time is not read as overconfident")
	}
}

// Below the floor a key gets no number, only how many more verdicts it needs.
func TestEvaluateSources_RefusesBelowTheFloorWithTheCountNeeded(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	tallies := map[Key]*Tally{
		{Source: "s", Domain: "go"}: {Observations: forecaster(rng, 7, 0.9, 0.9), Unscored: 4},
	}
	got, err := EvaluateSources(tallies, 0)
	if err != nil {
		t.Fatal(err)
	}
	sr := got[0]
	if sr.Report != nil {
		t.Errorf("reported a number on %d verdicts: %+v", sr.Scored, sr.Report)
	}
	if sr.Need != MinObservations-7 {
		t.Errorf("need = %d, want %d", sr.Need, MinObservations-7)
	}
	if sr.Scored != 7 || sr.Unscored != 4 {
		t.Errorf("scored, unscored = %d, %d, want 7, 4", sr.Scored, sr.Unscored)
	}
}

// Sources answering the same domain sit next to each other, so the reader
// compares like with like.
func TestEvaluateSources_OrdersByDomainThenSource(t *testing.T) {
	tallies := map[Key]*Tally{
		{Source: "b", Domain: "go"}: {},
		{Source: "a", Domain: "py"}: {},
		{Source: "a", Domain: "go"}: {},
	}
	got, err := EvaluateSources(tallies, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Key{
		{Source: "a", Domain: "go"},
		{Source: "b", Domain: "go"},
		{Source: "a", Domain: "py"},
	}
	for i, sr := range got {
		if sr.Key != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, sr.Key, want[i])
		}
	}
}

// A stated value that is not a probability is a broken writer, and the error
// has to say whose record it is in.
func TestEvaluateSources_NamesTheSourceOfABadProbability(t *testing.T) {
	obs := forecaster(rand.New(rand.NewSource(1)), MinObservations, 0.5, 0.5)
	obs[0].Predicted = 1.5
	tallies := map[Key]*Tally{{Source: "broken", Domain: "go"}: {Observations: obs}}

	_, err := EvaluateSources(tallies, 0)
	if err == nil || errors.Is(err, ErrTooFew) {
		t.Fatalf("err = %v, want a refusal of the bad probability", err)
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "go") {
		t.Errorf("error does not name the source and domain: %v", err)
	}
}
