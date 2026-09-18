// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/provider"
)

// collect runs a fan-out and returns the progress it reported, in order.
func collect(t *testing.T, s *Swarm, opts Options) []Progress {
	t.Helper()
	var mu sync.Mutex
	var got []Progress
	opts.OnProgress = func(p Progress) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}
	if _, err := s.Run(context.Background(), "p", opts); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return got
}

func kinds(ps []Progress, k ProgressKind) []Progress {
	var out []Progress
	for _, p := range ps {
		if p.Kind == k {
			out = append(out, p)
		}
	}
	return out
}

// Every head that starts must also finish, including one that failed. A panel
// that only hears about the winners leaves the rest spinning forever, which is
// worse than no panel at all.
func TestRun_EveryHeadStartsAndFinishes(t *testing.T) {
	sb := swarmSandbox(t)
	heads := []provider.Head{
		swarmHead(t, sb, "good", 90, "answer"),
		brokenHead(sb, "broken", 80),
	}
	got := collect(t, newSwarm(t, heads...), Options{Mode: ModeAll})

	if sel := kinds(got, ProgressSelected); len(sel) != 1 || len(sel[0].Heads) != 2 {
		t.Fatalf("selected events = %+v, want one carrying both heads", sel)
	}
	if got[0].Kind != ProgressSelected {
		t.Errorf("first event = %v, want the roster before anything ran", got[0].Kind)
	}

	started, finished := kinds(got, ProgressStarted), kinds(got, ProgressFinished)
	if len(started) != 2 || len(finished) != 2 {
		t.Fatalf("started/finished = %d/%d, want 2/2", len(started), len(finished))
	}

	byID := map[string]Attempt{}
	for _, p := range finished {
		byID[p.Head.ID] = p.Attempt
	}
	if s := byID["good"].Status; s != StatusOK {
		t.Errorf("good head finished as %q, want %q", s, StatusOK)
	}
	// The one that matters: a dead head is reported as finished-and-failed, not
	// left in the running state it was last announced in.
	if s := byID["broken"].Status; s == StatusOK || s == StatusRunning {
		t.Errorf("broken head finished as %q, want a failure status", s)
	}
}

// The attempt on a finish event is the priced one, so a surface can show spend
// as it accrues instead of waiting for the run to end.
func TestRun_FinishCarriesThePricedAttempt(t *testing.T) {
	sb := swarmSandbox(t)
	heads := []provider.Head{swarmHead(t, sb, "a", 90, "answer")}
	d := newSwarm(t, heads...)
	d.pricing = fakePricing{per: 0.02}

	var mu sync.Mutex
	var finished []Attempt
	res, err := d.Run(context.Background(), "p", Options{
		Mode: ModeAll,
		OnProgress: func(p Progress) {
			if p.Kind == ProgressFinished {
				mu.Lock()
				finished = append(finished, p.Attempt)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(finished) != 1 {
		t.Fatalf("finish events = %d, want 1", len(finished))
	}
	if finished[0].EstCostUSD != 0.02 {
		t.Errorf("reported cost = %v, want the priced 0.02", finished[0].EstCostUSD)
	}
	// Stated absolutely, not just as agreement: two zeroes agree perfectly and
	// would mean the run stopped pricing its attempts at all.
	if res.Attempts[0].EstCostUSD != 0.02 {
		t.Errorf("result cost = %v, want the priced 0.02", res.Attempts[0].EstCostUSD)
	}
}

// A fan-out reports from one goroutine per head. Delivery is serialized so a
// surface does not need a lock of its own, and a renderer drawing to a
// terminal from two goroutines at once produces garbage, not a race the
// detector would necessarily see.
func TestRun_ProgressIsDeliveredOneAtATime(t *testing.T) {
	sb := swarmSandbox(t)
	var heads []provider.Head
	for _, id := range []string{"a", "b", "c", "d"} {
		heads = append(heads, swarmHead(t, sb, id, 90, "answer"))
	}

	var inside, overlapped int32
	_, err := newSwarm(t, heads...).Run(context.Background(), "p", Options{
		Mode: ModeAll,
		OnProgress: func(Progress) {
			if atomic.AddInt32(&inside, 1) != 1 {
				atomic.StoreInt32(&overlapped, 1)
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&inside, -1)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if overlapped != 0 {
		t.Error("two heads reported progress at the same time; a renderer would have to lock for itself")
	}
}

// Nothing about the run may depend on someone watching it.
func TestRun_NilProgressChangesNothing(t *testing.T) {
	sb := swarmSandbox(t)
	heads := []provider.Head{
		swarmHead(t, sb, "a", 90, "answer"),
		swarmHead(t, sb, "b", 80, "other"),
	}
	s := newSwarm(t, heads...)

	unwatched, err := s.Run(context.Background(), "p", Options{Mode: ModeAll})
	if err != nil {
		t.Fatal(err)
	}
	watched, err := s.Run(context.Background(), "p", Options{Mode: ModeAll, OnProgress: func(Progress) {}})
	if err != nil {
		t.Fatal(err)
	}

	if len(unwatched.Attempts) != len(watched.Attempts) {
		t.Fatalf("attempts = %d watched, %d unwatched", len(watched.Attempts), len(unwatched.Attempts))
	}
	for i := range unwatched.Attempts {
		u, w := unwatched.Attempts[i], watched.Attempts[i]
		if u.Head.ID != w.Head.ID || u.Status != w.Status || u.Output != w.Output || u.Rank != w.Rank {
			t.Errorf("attempt %d differs: unwatched %+v, watched %+v",
				i, [4]any{u.Head.ID, u.Status, u.Output, u.Rank}, [4]any{w.Head.ID, w.Status, w.Output, w.Rank})
		}
	}
}
