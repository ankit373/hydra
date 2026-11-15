// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"fmt"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/runlog"
	"github.com/ankit373/hydra/internal/testutil"
)

// routeOf dispatches once and returns the route events the run wrote. The
// heads here cannot run, which does not matter: the route is written first.
func routeOf(t *testing.T, d *Dispatcher, opts Options) []runlog.Route {
	t.Helper()
	if opts.RunID == "" {
		opts.RunID = "run-" + t.Name()
	}
	opts.TaskID = "task-" + opts.RunID
	_, _ = d.Dispatch(context.Background(), "hi", opts)
	events, err := runlog.Load(opts.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var routes []runlog.Route
	for _, e := range events {
		if e.Kind == runlog.KindRoute {
			if e.SpanID != runlog.SpanIDFor(opts.TaskID) {
				t.Errorf("route on span %s, want the task's own span", e.SpanID)
			}
			routes = append(routes, *e.Route)
		}
	}
	return routes
}

func TestDispatch_WritesRouteEventWithEveryCandidate(t *testing.T) {
	testutil.NewSandbox(t)
	d := pricedDispatcher(t, append(costly(), registryHead("sick", 80, false)), listPrices)
	bothMeasured(t, d)
	d.health = parked(t, "sick", "agy crashed")

	routes := routeOf(t, d, Options{Domain: "go"})
	if len(routes) != 1 {
		t.Fatalf("%d route events, want one per dispatch", len(routes))
	}
	r := routes[0]
	if r.FirstHead != "local" || r.FirstProb != 1 || r.Requirement <= 0 {
		t.Errorf("route %+v, want local first with probability 1 under a requirement", r)
	}
	want := []runlog.RouteCandidate{
		{Head: "local", Model: "local", Tier: 10, Effective: 96, N: 200, InDomain: 200, Clears: true, Gate: "passed"},
		{Head: "paid", Model: "paid", Tier: 2, Effective: 99, N: 200, InDomain: 200, Clears: true, CostUSD: 0.0225, Gate: "passed"},
		{Head: "sick", Gate: "parked"},
	}
	if fmt.Sprint(r.Candidates) != fmt.Sprint(want) {
		t.Errorf("candidates\n got %+v\nwant %+v", r.Candidates, want)
	}
}

func TestRouteEvent_RunnerUpIsSecondCandidate(t *testing.T) {
	testutil.NewSandbox(t)
	d := pricedDispatcher(t, costly(), listPrices)
	bothMeasured(t, d)
	r := routeOf(t, d, Options{Domain: "go"})[0]
	if r.RunnerUp != r.Candidates[1].Head || r.RunnerUp != "paid" {
		t.Errorf("runner-up %q, want the second candidate %q", r.RunnerUp, r.Candidates[1].Head)
	}

	alone := pricedDispatcher(t, costly()[:1], listPrices)
	if r := routeOf(t, alone, Options{Domain: "go", RunID: "run-alone"})[0]; r.RunnerUp != "" {
		t.Errorf("runner-up %q with one candidate, want none", r.RunnerUp)
	}
}

func TestRouteEvent_CapsLongCandidateLists(t *testing.T) {
	testutil.NewSandbox(t)
	var heads []provider.Head
	for i := range 30 {
		heads = append(heads, registryHead(fmt.Sprintf("h%02d", i), 90-i, false))
	}
	d := domainDispatcher(t, heads)
	d.health = parked(t, "h29", "gone")

	r := routeOf(t, d, Options{})[0]
	if len(r.Candidates) != runlog.MaxRouteCandidates || r.Omitted != 30-runlog.MaxRouteCandidates {
		t.Fatalf("%d candidates and %d omitted, want %d and %d",
			len(r.Candidates), r.Omitted, runlog.MaxRouteCandidates, 30-runlog.MaxRouteCandidates)
	}
	if r.Candidates[0].Head != "h00" || r.Candidates[19].Head != "h19" {
		t.Errorf("kept %s..%s, want the first 20 in order", r.Candidates[0].Head, r.Candidates[19].Head)
	}
}

// A pinned head is the whole list: nothing was ranked, so no route is written.
func TestRouteEvent_NotWrittenForAPinnedHead(t *testing.T) {
	testutil.NewSandbox(t)
	d := pricedDispatcher(t, costly(), listPrices)
	if routes := routeOf(t, d, Options{Head: "paid"}); len(routes) != 0 {
		t.Errorf("%d route events for a pinned head, want none", len(routes))
	}
}
