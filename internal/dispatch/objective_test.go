// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/trust"
)

const objectiveClass = "classification.test"

// objectiveDispatcher has two answering heads, both measured competent in
// objectiveClass: "best" measured stronger and dearer, "thrifty" cheaper. So
// quality and the cost constraint must disagree about which runs first.
func objectiveDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)
	head := func(id string) provider.Head {
		return provider.Head{ID: id, Name: id, Provider: "openai", Source: "env", Endpoint: srv.URL, AuthReady: true, CapScore: 90}
	}
	d := streamDispatcher(t, head("best"), head("thrifty"))
	cal, err := trust.New("")
	if err != nil {
		t.Fatal(err)
	}
	d.cal = cal
	record(t, cal, "best", objectiveClass, 25, 25)
	record(t, cal, "thrifty", objectiveClass, 24, 25)
	prices := map[string]float64{"best": 0.02, "thrifty": 0.01}
	d.price = func(h provider.Head) (float64, bool) { usd, ok := prices[h.ID]; return usd, ok }
	return d
}

// G9. quality must reach the attempt loop and read the class's cell.
func TestDispatch_QualityPutsTheMeasuredBestFirst(t *testing.T) {
	d := objectiveDispatcher(t)
	res, err := d.Dispatch(context.Background(), "hi", Options{
		Objective: rank.Quality, Domain: objectiveClass, RunID: "run-q", TaskID: "task-q",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Head.ID != "best" {
		t.Errorf("objective quality ran %s first; measured best in %s is %s (effective %d vs %d)",
			res.Head.ID, objectiveClass, "best", res.Scores["best"].Effective, res.Scores["thrifty"].Effective)
	}
	if !res.Scores[res.Head.ID].Measured() {
		t.Errorf("the success result carries no measured score for %s: %+v", res.Head.ID, res.Scores)
	}
}

// G10. hyctl dispatch names no objective, and must order exactly as constrain
// alone does, as before E1.
func TestDispatch_NoObjectiveIsTodaysConstraint(t *testing.T) {
	d := objectiveDispatcher(t)
	res, err := d.Dispatch(context.Background(), "hi", Options{Domain: objectiveClass, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	want, _, _ := d.constrain(d.selectHeads("", false, objectiveClass), objectiveClass, requirementFor(nil))
	if want[0].ID != "thrifty" {
		t.Fatalf("constrain ran %s first; the fixture needs the cheap competent head there", want[0].ID)
	}
	got := append([]provider.Head{res.Head}, res.Fallbacks...)
	if strings.Join(ids(got), ",") != strings.Join(ids(want), ",") {
		t.Errorf("objective \"\" ordered %v, constrain alone orders %v", ids(got), ids(want))
	}
}
