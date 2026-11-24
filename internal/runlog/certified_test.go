// SPDX-License-Identifier: MIT

package runlog

import (
	"reflect"
	"testing"

	"github.com/ankit373/hydra/internal/aver"
)

func TestCertifiedScore_RoundTripsOutcome(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	span := seedSpan(t, "r1", "h1")
	cert := aver.Outcome{Mode: aver.ModeIndependence, Alpha: 0.05, Bar: 20, Product: 129.1,
		Steps: []aver.Step{
			{Check: "lint", Passed: true, QBar: 0.088, S: 1, E: 11.36},
			{Check: "tests", Passed: true, QBar: 0.088, S: 1, E: 11.36},
		}}
	refused := aver.Outcome{Alpha: 0.05, Reason: aver.ReasonTooFewWrong, Have: 41, Need: 60}
	if err := AppendCertified("r1", span, cert); err != nil {
		t.Fatal(err)
	}
	other := seedSpan(t, "r1", "h2")
	if err := AppendCertified("r1", other, refused); err != nil {
		t.Fatal(err)
	}
	events, err := Load("r1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		span  string
		want  aver.Outcome
		value float64
	}{{span, cert, 1}, {other, refused, 0}} {
		got := ScoresBySpan(events)[c.span]
		if len(got) != 1 || got[0].Name != ScoreCertified || got[0].Value != c.value || got[0].Comment != c.want.Line() {
			t.Fatalf("score on %s: %+v", c.span, got)
		}
		if !reflect.DeepEqual(*got[0].Outcome, c.want) {
			t.Fatalf("outcome did not round-trip:\n got %+v\nwant %+v", *got[0].Outcome, c.want)
		}
	}
}

func TestRoute_CarriesCertifyAlpha(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())
	if err := New("r2").Append(Event{Kind: KindRoute, TaskID: "t", Route: &Route{FirstHead: "h", CertifyAlpha: 0.01}}); err != nil {
		t.Fatal(err)
	}
	events, err := Load("r2")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Route == nil || events[0].Route.CertifyAlpha != 0.01 {
		t.Fatalf("route lost certify_alpha: %+v", events)
	}
}
