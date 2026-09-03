// SPDX-License-Identifier: MIT

package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

// writeRun appends events for one run, exactly as a producer would.
func writeRun(t *testing.T, runID string, events ...runlog.Event) {
	t.Helper()
	rl := runlog.New(runID)
	for _, e := range events {
		if err := rl.Append(e); err != nil {
			t.Fatalf("append to %s: %v", runID, err)
		}
	}
}

// markLive touches a run's heartbeat so LiveRuns reports it.
func markLive(t *testing.T, runID string) {
	t.Helper()
	p := runlog.HeartbeatPath(runID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runByID(f *Fleet, id string) *Run {
	for i := range f.Runs {
		if f.Runs[i].ID == id {
			return &f.Runs[i]
		}
	}
	return nil
}

// A machine that has never dispatched must say so, not render an empty list
// that looks like a failed load.
func TestGetFleet_EmptyStateIsHonest(t *testing.T) {
	sandbox(t)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatalf("GetFleet on a fresh machine must not error: %v", err)
	}
	if f.HasRuns {
		t.Error("HasRuns = true with no runs on disk")
	}
	if len(f.Runs) != 0 || f.LiveCount != 0 {
		t.Errorf("expected no runs, got %d (%d live)", len(f.Runs), f.LiveCount)
	}
	if f.GroupThreshold != GroupThreshold {
		t.Errorf("GroupThreshold = %d, want %d — the view must not hardcode its own", f.GroupThreshold, GroupThreshold)
	}
}

// A swarm's heads must appear as separate agents. Before #205 the fan-out
// collapsed to one node, which is exactly what a Fleet view cannot show.
func TestGetFleet_SwarmHeadsAreSeparateAgents(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-aaaa",
		runlog.Event{Kind: runlog.KindTaskStarted, Agent: "swarm"},
		runlog.Event{Kind: runlog.KindAttempt, Agent: "h1", Parent: "swarm", Head: "h1", Model: "M1", Tier: 2, Status: "ok", CostUSD: 0.01},
		runlog.Event{Kind: runlog.KindAttempt, Agent: "h2", Parent: "swarm", Head: "h2", Model: "M2", Tier: 4, Status: "ok", CostUSD: 0.02},
		runlog.Event{Kind: runlog.KindAttempt, Agent: "h3", Parent: "swarm", Head: "h3", Model: "M3", Tier: 8, Status: "failed"},
		runlog.Event{Kind: runlog.KindTaskFinished, Agent: "swarm"},
	)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	r := runByID(f, "20260802T100000Z-aaaa")
	if r == nil {
		t.Fatal("run missing from the fleet")
	}
	if r.AllCount != 4 {
		t.Fatalf("%d agents, want 4 (swarm root + three heads)", r.AllCount)
	}
	if r.OK != 3 { // swarm root finishes ok too
		t.Errorf("OK = %d, want 3", r.OK)
	}
	if r.Failed != 1 {
		t.Errorf("Failed = %d, want 1", r.Failed)
	}
	// Cost is the sum of what the heads actually spent.
	if r.CostUSD != 0.03 {
		t.Errorf("CostUSD = %v, want 0.03", r.CostUSD)
	}
	// Depth carries the fan-out's shape.
	for _, a := range r.Agents {
		if a.ID == "swarm" && a.Depth != 0 {
			t.Errorf("swarm root at depth %d, want 0", a.Depth)
		}
		if a.ID != "swarm" && a.Depth != 1 {
			t.Errorf("head %q at depth %d, want 1", a.ID, a.Depth)
		}
	}
}

// Live-first ordering is the whole point of the view: what is happening now
// must not be buried under history.
func TestGetFleet_OrdersLiveFirstThenRecent(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T090000Z-old", runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"})
	writeRun(t, "20260802T110000Z-new", runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"})
	writeRun(t, "20260802T100000Z-mid", runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"})
	markLive(t, "20260802T090000Z-old") // the OLDEST is the live one

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	if f.LiveCount != 1 {
		t.Fatalf("LiveCount = %d, want 1", f.LiveCount)
	}
	want := []string{"20260802T090000Z-old", "20260802T110000Z-new", "20260802T100000Z-mid"}
	for i, id := range want {
		if i >= len(f.Runs) || f.Runs[i].ID != id {
			got := make([]string, len(f.Runs))
			for j, r := range f.Runs {
				got[j] = r.ID
			}
			t.Fatalf("order = %v, want %v (live first, then newest)", got, want)
		}
	}
	if !f.Runs[0].Live {
		t.Error("the live run is not marked live")
	}
}

// A stale heartbeat means the process died. It must not read as still running,
// or a crashed agent stays "live" forever.
func TestGetFleet_StaleHeartbeatIsNotLive(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-stale", runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"})
	markLive(t, "20260802T100000Z-stale")

	// Backdate well past StaleAfter, as a killed process leaves behind.
	old := time.Now().Add(-10 * runlog.StaleAfter)
	if err := os.Chtimes(runlog.HeartbeatPath("20260802T100000Z-stale"), old, old); err != nil {
		t.Fatal(err)
	}

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	if f.LiveCount != 0 {
		t.Errorf("LiveCount = %d, want 0 — a stale marker is a dead process", f.LiveCount)
	}
	if r := runByID(f, "20260802T100000Z-stale"); r == nil || r.Live {
		t.Error("run with a stale heartbeat is still marked live")
	}
}

// An SPRT run's final confidence is the number the whole ensemble exists to
// produce; a plain dispatch has none, and zero must not be shown as one.
func TestGetFleet_ConfidenceOnlyForEnsembles(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-sprt",
		runlog.Event{Kind: runlog.KindTaskStarted, Agent: "ensemble"},
		runlog.Event{Kind: runlog.KindSample, Agent: "a", Parent: "ensemble", Confidence: 0.72},
		runlog.Event{Kind: runlog.KindSample, Agent: "b", Parent: "ensemble", Confidence: 0.95},
		runlog.Event{Kind: runlog.KindTaskFinished, Agent: "ensemble", Confidence: 0.95},
	)
	writeRun(t, "20260802T090000Z-plain",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"},
		runlog.Event{Kind: runlog.KindDispatchFinished, Head: "h", Status: "ok"},
	)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	if r := runByID(f, "20260802T100000Z-sprt"); r == nil || r.Confidence != 0.95 {
		t.Errorf("SPRT run confidence = %v, want 0.95", r.Confidence)
	}
	if r := runByID(f, "20260802T090000Z-plain"); r == nil || r.Confidence != 0 {
		t.Errorf("plain dispatch confidence = %v, want 0 (not a confidence run)", r.Confidence)
	}
}

// A finished run's elapsed time is fixed at its last event. Measuring it to now
// would make every historical run appear to still be growing.
func TestGetFleet_FinishedRunElapsedStopsAtLastEvent(t *testing.T) {
	sandbox(t)

	start := time.Now().Add(-2 * time.Hour)
	writeRun(t, "20260802T100000Z-done",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h", TS: start.UTC().Format(time.RFC3339Nano)},
		runlog.Event{Kind: runlog.KindDispatchFinished, Head: "h", Status: "ok",
			TS: start.Add(5 * time.Second).UTC().Format(time.RFC3339Nano)},
	)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	r := runByID(f, "20260802T100000Z-done")
	if r == nil {
		t.Fatal("run missing")
	}
	if r.ElapsedMS < 4_000 || r.ElapsedMS > 6_000 {
		t.Errorf("ElapsedMS = %d, want ~5000 — a finished run must not keep accruing", r.ElapsedMS)
	}
}

// A live run is still accruing, so it is measured to now.
func TestGetFleet_LiveRunElapsedRunsToNow(t *testing.T) {
	sandbox(t)

	start := time.Now().Add(-30 * time.Second)
	writeRun(t, "20260802T100000Z-live",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h", TS: start.UTC().Format(time.RFC3339Nano)},
	)
	markLive(t, "20260802T100000Z-live")

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	r := runByID(f, "20260802T100000Z-live")
	if r == nil || !r.Live {
		t.Fatal("live run missing or not live")
	}
	if r.ElapsedMS < 29_000 {
		t.Errorf("ElapsedMS = %d, want ~30000 — a live run is measured to now", r.ElapsedMS)
	}
}

// A malformed line must not blank the view, and must not be silently dropped
// either: a partial run rendered as a complete one is the worse failure.
func TestGetFleet_MalformedLinesAreSurfacedNotHidden(t *testing.T) {
	home := sandbox(t)

	writeRun(t, "20260802T100000Z-partial",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"},
	)
	// An event attributable to no node — nodeID returns "".
	writeRun(t, "20260802T100000Z-partial",
		runlog.Event{Kind: runlog.KindEdit},
	)
	if _, err := os.Stat(filepath.Join(home, ".hydra", "logs", "runs", "20260802T100000Z-partial.jsonl")); err != nil {
		t.Fatal(err)
	}

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	r := runByID(f, "20260802T100000Z-partial")
	if r == nil {
		t.Fatal("run missing")
	}
	if r.Skipped == 0 {
		t.Error("Skipped = 0; an unattributable event was hidden rather than surfaced")
	}
	if r.AllCount == 0 {
		t.Error("the readable part of the run was dropped too")
	}
}

// A finished, non-live run with zero agents and no error carries no
// information — before #390, --dry-run wrote exactly this shape to the
// runlog. Once real machines accumulate enough of these (sorted
// live-first-then-newest, so they outrank real history), Fleet looks empty
// even though HasRuns is technically true. #390 stopped writing new ones;
// this is the other half — don't surface the ones already on disk either.
func TestGetFleet_ZeroAgentGhostRunsAreFilteredOut(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T090000Z-ghost",
		runlog.Event{Kind: runlog.KindRunStarted},
		runlog.Event{Kind: runlog.KindRunFinished},
	)
	writeRun(t, "20260802T100000Z-real",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"},
		runlog.Event{Kind: runlog.KindDispatchFinished, Head: "h", Status: "ok"},
	)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	if r := runByID(f, "20260802T090000Z-ghost"); r != nil {
		t.Error("a finished, zero-agent, error-free run was not filtered out")
	}
	if r := runByID(f, "20260802T100000Z-real"); r == nil {
		t.Error("the real run was dropped along with the ghost")
	}
	if len(f.Runs) != 1 {
		t.Errorf("len(Runs) = %d, want 1 (ghost filtered, real kept)", len(f.Runs))
	}
}

// A machine whose only history is dry-run ghosts must still get the honest
// "no runs yet" empty state, not a blank list with no explanation — the
// exact failure this filter would otherwise reintroduce.
func TestGetFleet_AllGhostRunsStillShowHonestEmptyState(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T090000Z-ghost1",
		runlog.Event{Kind: runlog.KindRunStarted},
		runlog.Event{Kind: runlog.KindRunFinished},
	)
	writeRun(t, "20260802T100000Z-ghost2",
		runlog.Event{Kind: runlog.KindRunStarted},
		runlog.Event{Kind: runlog.KindRunFinished},
	)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	if f.HasRuns {
		t.Error("HasRuns = true with every run filtered out — the view would render a blank list with no explanation")
	}
	if len(f.Runs) != 0 {
		t.Errorf("len(Runs) = %d, want 0", len(f.Runs))
	}
}

// A live run must never be filtered even with zero agents so far — it may
// not have picked a head yet, and it is the one the user opened the app to
// watch. A run with a read error must never be filtered either — the error
// itself is the information (see TestGetFleet_MalformedLinesAreSurfacedNotHidden
// for the read-error case; this covers the live case).
func TestGetFleet_LiveZeroAgentRunIsNeverFiltered(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-justStarted",
		runlog.Event{Kind: runlog.KindRunStarted},
	)
	markLive(t, "20260802T100000Z-justStarted")

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	r := runByID(f, "20260802T100000Z-justStarted")
	if r == nil {
		t.Fatal("a live run with zero agents so far was filtered out")
	}
	if !r.Live {
		t.Error("run is not marked live")
	}
}

// Reading the fleet holds no state and must be safe to poll from several places
// at once, as the frontend does.
func TestGetFleet_ConcurrentCallsAreSafe(t *testing.T) {
	sandbox(t)
	writeRun(t, "20260802T100000Z-conc", runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"})

	a := New()
	done := make(chan error, 16)
	for range cap(done) {
		go func() {
			_, err := a.GetFleet()
			done <- err
		}()
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Errorf("concurrent GetFleet: %v", err)
		}
	}
}

// A list of runs identified only by timestamp id is a list of timestamps. The
// prompt is recorded on the run-started event, but tree.Reconstruct drops
// run-level events, so it never reached the view until this read (#603).
func TestGetFleet_CarriesWhatTheRunWasAskedToDo(t *testing.T) {
	sandbox(t)
	const id = "20260903T120000Z-goaltest"
	writeRun(t, id,
		runlog.Event{Kind: runlog.KindRunStarted, TaskID: "t1", Detail: "add pagination to /users"},
		runlog.Event{Kind: runlog.KindHeadSelected, TaskID: "t1", Head: "h", Tier: 6},
		runlog.Event{Kind: runlog.KindRunFinished, TaskID: "t1"},
	)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	if got := findRun(t, f, id).Goal; got != "add pagination to /users" {
		t.Errorf("Goal = %q, want the run-started detail", got)
	}
}

// An external orchestrator can supply a run id and never a prompt. That is an
// absent goal, not an error, and must not be fabricated from another event.
func TestGetFleet_NoPromptLeavesTheGoalEmpty(t *testing.T) {
	sandbox(t)
	const id = "20260903T120001Z-nogoal"
	writeRun(t, id,
		runlog.Event{Kind: runlog.KindRunStarted, TaskID: "t1"},
		runlog.Event{Kind: runlog.KindHeadSelected, TaskID: "t1", Head: "h", Tier: 6,
			Detail: "candidate 1 of 2"},
	)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	if got := findRun(t, f, id).Goal; got != "" {
		t.Errorf("Goal = %q, want empty — a head_selected detail is not the goal", got)
	}
}

// One run, one goal: the first statement of it is what was actually requested.
func TestGetFleet_FirstGoalWinsOverALaterRunStarted(t *testing.T) {
	sandbox(t)
	const id = "20260903T120002Z-twogoals"
	writeRun(t, id,
		runlog.Event{Kind: runlog.KindRunStarted, TaskID: "t1", Detail: "the real request"},
		runlog.Event{Kind: runlog.KindHeadSelected, TaskID: "t1", Head: "h", Tier: 6},
		// A second writer sharing the run appends its own run_started (runlog's
		// own docs note several writers share one run).
		runlog.Event{Kind: runlog.KindRunStarted, TaskID: "t2", Detail: "a second writer's idea"},
	)

	f, err := New().GetFleet()
	if err != nil {
		t.Fatal(err)
	}
	if got := findRun(t, f, id).Goal; got != "the real request" {
		t.Errorf("Goal = %q, want the first run_started detail", got)
	}
}

func findRun(t *testing.T, f *Fleet, id string) Run {
	t.Helper()
	for _, r := range f.Runs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("run %q not in fleet of %d", id, len(f.Runs))
	return Run{}
}
