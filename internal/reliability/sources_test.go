// SPDX-License-Identifier: MIT

package reliability

import (
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
	got := EvaluateSources(tallies, 0)
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
	got := EvaluateSources(tallies, 0)
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
	got := EvaluateSources(tallies, 0)
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

// A stated value that is not a probability is a broken writer. It goes on that
// key's row, naming where it was recorded, and every other key still reports.
func TestEvaluateSources_KeepsABadProbabilityOnItsOwnRow(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	bad := forecaster(rng, MinObservations, 0.5, 0.5)
	bad[3].Predicted = 1.5
	bad[3].Ref = "run r1 span s1"
	broken := Key{Source: "broken", Domain: "go"}
	fine := Key{Source: "fine", Domain: "go"}
	tallies := map[Key]*Tally{
		broken: {Observations: bad},
		fine:   {Observations: forecaster(rng, MinObservations, 0.5, 0.5)},
	}

	rows := map[Key]SourceReport{}
	for _, sr := range EvaluateSources(tallies, 0) {
		rows[sr.Key] = sr
	}
	if got := rows[broken]; got.Report != nil || !strings.Contains(got.Err, "run r1 span s1") {
		t.Errorf("broken row = %+v, want no report and an error naming run r1 span s1", got)
	}
	if got := rows[fine]; got.Report == nil || got.Err != "" {
		t.Errorf("fine row = %+v, want a report beside the broken one", got)
	}
}
