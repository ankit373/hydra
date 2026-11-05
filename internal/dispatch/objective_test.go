// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/cache"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
	"github.com/ankit373/hydra/internal/testutil"
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
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
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

// Under quota pressure the objective yields to the governor: bare `hydra` routes
// as the pinned STANDARD it replaced, downgraded exactly as before E1, and
// reports no objective, since none ordered it.
func TestDispatch_TheGovernorRoutesAnObjectiveAsTheOldDefault(t *testing.T) {
	for _, pct := range []int{72, 77, 85} {
		t.Run(fmt.Sprintf("%d%%", pct), func(t *testing.T) {
			testutil.NewSandbox(t)
			writeStatePct(t, pct, nil)
			d := routingDispatcher()
			old, err := d.Dispatch(context.Background(), "hi", Options{
				TierHint: EnumToTier(ServedDefaultEnum), Enum: ServedDefaultEnum, DryRun: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Dispatch(context.Background(), "hi", Options{Objective: rank.Quality, DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			want := ids(append([]provider.Head{old.Head}, old.Fallbacks...))
			have := ids(append([]provider.Head{got.Head}, got.Fallbacks...))
			if strings.Join(have, ",") != strings.Join(want, ",") || got.Objective != "" {
				t.Errorf("at %d%% objective quality ran %v (objective %q); develop's pinned hydra ran %v", pct, have, got.Objective, want)
			}
		})
	}
	// Below the warning band the objective is what orders, and says so.
	testutil.NewSandbox(t)
	writeStatePct(t, 10, nil)
	got, err := routingDispatcher().Dispatch(context.Background(), "hi", Options{Objective: rank.Quality, DryRun: true})
	if err != nil || got.Head.ID != "strongest" || got.Objective != rank.Quality {
		t.Fatalf("at 10%% quality ran %v with objective %q (%v); want strongest under quality", got.Head.ID, got.Objective, err)
	}
}

// A pinned dispatch pays for no explanation unless it is a dry run, which is
// the only reader of one there.
func TestDispatch_APinnedDispatchSkipsTheExplanation(t *testing.T) {
	d := objectiveDispatcher(t)
	for _, dry := range []bool{false, true} {
		res, err := d.Dispatch(context.Background(), "hi", Options{
			TierHint: "1", Domain: objectiveClass, DryRun: dry, RunID: "run-x", TaskID: "task-x",
		})
		if err != nil {
			t.Fatal(err)
		}
		if (res.Scores != nil) != dry {
			t.Errorf("dry run %v: scores %+v; explain must run on a pinned route only for a dry run", dry, res.Scores)
		}
	}
}

// The objective route keeps STANDARD's cache threshold, so unpinning `hydra`
// does not silently loosen what counts as the same question.
func TestCacheEnum_AnObjectiveRouteKeepsTheStandardThreshold(t *testing.T) {
	cfg := &config.Config{CacheThreshold: 0.9, CacheThresholds: map[string]float64{ServedDefaultEnum: 0.99}}
	for _, tc := range []struct {
		opts Options
		want float64
	}{
		{Options{Objective: rank.Quality}, 0.99},
		{Options{Objective: rank.Cost}, 0.99},
		{Options{}, 0.9},
		{Options{Enum: "SIMPLE", Objective: rank.Quality}, 0.9},
	} {
		if got := cache.Threshold(cfg, cacheEnum(tc.opts)); got != tc.want {
			t.Errorf("%+v admits near matches at %v, want %v", tc.opts, got, tc.want)
		}
	}
}

// An objective route has no enum, so its rows must name the objective or the
// spend is unattributable.
func TestDispatch_AnObjectiveRouteIsAttributedInItsRows(t *testing.T) {
	d := objectiveDispatcher(t)
	if _, err := d.Dispatch(context.Background(), "hi", Options{
		Objective: rank.Quality, Domain: objectiveClass, RunID: "run-a", TaskID: "task-a",
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"dispatch.jsonl", "cost.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(config.Dir(), "logs", file))
		if err != nil {
			t.Fatal(err)
		}
		var row map[string]any
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &row); err != nil {
			t.Fatal(err)
		}
		if row["enum"] != "" || row["objective"] != "quality" {
			t.Errorf("%s row has enum %v objective %v, want \"\" and quality", file, row["enum"], row["objective"])
		}
	}
}
