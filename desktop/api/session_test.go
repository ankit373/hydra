// SPDX-License-Identifier: MIT

package api

import (
	"testing"

	"github.com/ankit373/hydra/internal/runlog"
)

// A run id that names no log is not the same as a run that did nothing. The
// view branches on Found so it can say which.
func TestGetSession_UnknownRunIsNotFound(t *testing.T) {
	sandbox(t)

	s, err := New().GetSession("20260802T100000Z-nope")
	if err != nil {
		t.Fatalf("an unknown run must not error: %v", err)
	}
	if s.Found {
		t.Error("Found = true for a run with no log")
	}
	if len(s.Timeline) != 0 || len(s.Agents) != 0 {
		t.Error("an unknown run produced content")
	}
}

func TestGetSession_EmptyRunIDIsHandled(t *testing.T) {
	sandbox(t)

	s, err := New().GetSession("")
	if err != nil {
		t.Fatalf("empty run id must not error: %v", err)
	}
	if s.Found {
		t.Error("Found = true for an empty run id")
	}
}

// The timeline must show every event, including the run-level ones the tree
// deliberately omits, a run's own start and end belong on a timeline.
func TestGetSession_TimelineIncludesRunLevelEvents(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-full",
		runlog.Event{Kind: runlog.KindRunStarted, TaskID: "t", Detail: "add pagination"},
		runlog.Event{Kind: runlog.KindHeadSelected, TaskID: "t", Head: "agy", Model: "Antigravity", Tier: 4},
		runlog.Event{Kind: runlog.KindDispatchFinished, TaskID: "t", Head: "agy", Status: "ok", DurationMS: 1200},
		runlog.Event{Kind: runlog.KindRunFinished, TaskID: "t"},
	)

	s, err := New().GetSession("20260802T100000Z-full")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Found {
		t.Fatal("Found = false for a run that exists")
	}
	if len(s.Timeline) != 4 {
		t.Fatalf("%d timeline entries, want 4, run-level events belong here even though the tree omits them", len(s.Timeline))
	}
	if s.Timeline[0].Kind != string(runlog.KindRunStarted) {
		t.Errorf("first entry is %q, want run_started", s.Timeline[0].Kind)
	}
	if s.Timeline[3].Kind != string(runlog.KindRunFinished) {
		t.Errorf("last entry is %q, want run_finished", s.Timeline[3].Kind)
	}
	// The tree still has only the head, run-level events create no node.
	if len(s.Agents) != 1 || s.Agents[0].ID != "agy" {
		t.Errorf("agents = %+v, want just the head", s.Agents)
	}
}

// Several writers share one run and each counts from 1, so runlog's own doc
// says seq "is not the sort key". Re-sorting by it puts a run's end before a
// handoff that happened first (#205).
func TestGetSession_TimelineIsFileOrderNotSeqOrder(t *testing.T) {
	sandbox(t)

	// Exactly the interleaving a real dispatch produces: the CLI logs the run
	// lifecycle (seq 1,2) around dispatch's own events (seq 1,2,3).
	rl := runlog.New("20260802T100000Z-order")
	for _, e := range []runlog.Event{
		{Kind: runlog.KindRunStarted},
		{Kind: runlog.KindHeadSelected, Head: "h"},
		{Kind: runlog.KindDispatchFinished, Head: "h", Status: "ok"},
		{Kind: runlog.KindHandoff, Head: "h", Ref: "next", Detail: "context handed on"},
		{Kind: runlog.KindRunFinished},
	} {
		if err := rl.Append(e); err != nil {
			t.Fatal(err)
		}
	}

	s, err := New().GetSession("20260802T100000Z-order")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run_started", "head_selected", "dispatch_finished", "handoff", "run_finished"}
	for i, k := range want {
		if i >= len(s.Timeline) || s.Timeline[i].Kind != k {
			got := make([]string, len(s.Timeline))
			for j, e := range s.Timeline {
				got[j] = e.Kind
			}
			t.Fatalf("timeline = %v, want %v", got, want)
		}
	}
}

// A straight chain reads better as a list. Offering a graph of a line is worse
// than offering nothing.
func TestGetSession_LinearRunIsNotOfferedAGraph(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-linear",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"},
		runlog.Event{Kind: runlog.KindDispatchFinished, Head: "h", Status: "ok"},
	)

	s, err := New().GetSession("20260802T100000Z-linear")
	if err != nil {
		t.Fatal(err)
	}
	if s.NonLinear {
		t.Error("NonLinear = true for a single-head run")
	}
}

// A fan-out is exactly what a list cannot show.
func TestGetSession_FanOutIsNonLinear(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-fan",
		runlog.Event{Kind: runlog.KindTaskStarted, Agent: "swarm"},
		runlog.Event{Kind: runlog.KindAttempt, Agent: "a", Parent: "swarm", Status: "ok"},
		runlog.Event{Kind: runlog.KindAttempt, Agent: "b", Parent: "swarm", Status: "ok"},
	)

	s, err := New().GetSession("20260802T100000Z-fan")
	if err != nil {
		t.Fatal(err)
	}
	if !s.NonLinear {
		t.Error("NonLinear = false for a two-way fan-out")
	}
}

// A real hyctl dispatch always crosses runs for its A2A handoff, the target
// picks up last_handoff.json in a later, separate invocation with its own
// run_id, so writeHandoff's ref (e.g. "hydra-tier-4") never names a node in
// this run's own tree. Every successful dispatch writes one of these
// unconditionally, so treating it as same-run graph structure made ordinary,
// single-head runs non-linear 100% of the time (#485): the Graph tab always
// appeared, and always showed one isolated node with no visible edge, since
// SessionGraph can't place a node it has no Agent for either.
func TestGetSession_DanglingHandoffIsNotNonLinear(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-a2a",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "a"},
		runlog.Event{Kind: runlog.KindHandoff, Agent: "a", Ref: "hydra-tier-4", Detail: "context handed to hydra-tier-4"},
	)

	s, err := New().GetSession("20260802T100000Z-a2a")
	if err != nil {
		t.Fatal(err)
	}
	if s.NonLinear {
		t.Error("NonLinear = true for a handoff whose target is not a node in this run")
	}
	if len(s.Edges) != 0 {
		t.Errorf("%d edges, want 0, the target does not exist in this session", len(s.Edges))
	}
}

// A handoff whose target IS a node in the same run, the one shape the
// current architecture never actually produces, but the graph must still
// render correctly if it ever does, is real structure a timeline cannot
// draw, and must still make the run non-linear.
func TestGetSession_ResolvableHandoffMakesItNonLinear(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-a2a-resolvable",
		runlog.Event{Kind: runlog.KindTaskStarted, Agent: "a"},
		runlog.Event{Kind: runlog.KindTaskStarted, Agent: "b"},
		runlog.Event{Kind: runlog.KindHandoff, Agent: "a", Ref: "b", Detail: "context"},
	)

	s, err := New().GetSession("20260802T100000Z-a2a-resolvable")
	if err != nil {
		t.Fatal(err)
	}
	if !s.NonLinear {
		t.Error("NonLinear = false despite an A2A edge resolving to a real node")
	}
	if len(s.Edges) != 1 {
		t.Fatalf("%d edges, want 1", len(s.Edges))
	}
	if s.Edges[0].From != "a" || s.Edges[0].To != "b" {
		t.Errorf("edge = %s→%s, want a→b", s.Edges[0].From, s.Edges[0].To)
	}
}

// Ownership and collaboration are different relations. An A2A edge must never
// show up as a parent, which is why tree.Node keeps Children and Handoffs
// apart.
func TestGetSession_HandoffIsNotAnOwnershipEdge(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-sep",
		runlog.Event{Kind: runlog.KindTaskStarted, Agent: "a"},
		runlog.Event{Kind: runlog.KindTaskStarted, Agent: "b"},
		runlog.Event{Kind: runlog.KindHandoff, Agent: "a", Ref: "b", Detail: "context"},
	)

	s, err := New().GetSession("20260802T100000Z-sep")
	if err != nil {
		t.Fatal(err)
	}
	for _, ag := range s.Agents {
		if ag.ID == "b" && ag.Parent == "a" {
			t.Error("an A2A handoff was recorded as an ownership edge")
		}
	}
	if len(s.Edges) != 1 {
		t.Errorf("%d edges, want the handoff to survive as a collaboration edge", len(s.Edges))
	}
}

// The SPRT detail is the verifiable part, what the source did to the running
// log-odds. It must reach the view, because a confidence number with no
// evidence behind it is the configuration that produces misplaced trust.
func TestGetSession_SPRTSamplesCarryEvidence(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-sprt",
		runlog.Event{Kind: runlog.KindTaskStarted, Agent: "ensemble"},
		runlog.Event{Kind: runlog.KindSample, Agent: "a", Parent: "ensemble",
			Confidence: 0.768, Detail: "agreed · LLR +1.200 → Λ 1.200"},
		runlog.Event{Kind: runlog.KindSample, Agent: "b", Parent: "ensemble",
			Confidence: 0.846, Detail: "disagreed · LLR -0.400 → Λ 1.700"},
	)

	s, err := New().GetSession("20260802T100000Z-sprt")
	if err != nil {
		t.Fatal(err)
	}
	var samples []TimelineEntry
	for _, e := range s.Timeline {
		if e.Kind == string(runlog.KindSample) {
			samples = append(samples, e)
		}
	}
	if len(samples) != 2 {
		t.Fatalf("%d samples on the timeline, want 2", len(samples))
	}
	if samples[0].Confidence != 0.768 || samples[1].Confidence != 0.846 {
		t.Errorf("confidences = %v, %v; want 0.768, 0.846", samples[0].Confidence, samples[1].Confidence)
	}
	for i, want := range []string{"agreed · LLR +1.200 → Λ 1.200", "disagreed · LLR -0.400 → Λ 1.700"} {
		if samples[i].Detail != want {
			t.Errorf("sample %d detail = %q, want %q, the evidence must reach the view", i, samples[i].Detail, want)
		}
	}
}

// Unattributable events must be counted, not dropped: rendering a partial run
// as a complete one is the worse failure.
func TestGetSession_SurfacesSkippedEvents(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-skip",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"},
		runlog.Event{Kind: runlog.KindEdit}, // attributable to no node
	)

	s, err := New().GetSession("20260802T100000Z-skip")
	if err != nil {
		t.Fatal(err)
	}
	if s.Skipped == 0 {
		t.Error("Skipped = 0; an unattributable event was hidden")
	}
}

// A zero timestamp must render as absent, not as a real-looking date in year 1.
func TestGetSession_MissingTimestampsRenderEmpty(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-nots",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h", TS: "not-a-timestamp"},
	)

	s, err := New().GetSession("20260802T100000Z-nots")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Timeline) != 1 {
		t.Fatalf("%d entries, want 1", len(s.Timeline))
	}
	if ts := s.Timeline[0].TS; ts != "" {
		t.Errorf("TS = %q for an unparseable timestamp, want empty rather than a year-1 date", ts)
	}
}

func TestGetSession_ConcurrentCallsAreSafe(t *testing.T) {
	sandbox(t)
	writeRun(t, "20260802T100000Z-conc", runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"})

	a := New()
	done := make(chan error, 16)
	for range cap(done) {
		go func() {
			_, err := a.GetSession("20260802T100000Z-conc")
			done <- err
		}()
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Errorf("concurrent GetSession: %v", err)
		}
	}
}

// Session's header is otherwise titled by run id alone, which says nothing
// about what the run was for (#603).
func TestGetSession_CarriesTheGoal(t *testing.T) {
	sandbox(t)
	const id = "20260903T130000Z-sessgoal"
	writeRun(t, id,
		runlog.Event{Kind: runlog.KindRunStarted, TaskID: "t1", Detail: "rotate the signing key"},
		runlog.Event{Kind: runlog.KindHeadSelected, TaskID: "t1", Head: "h", Tier: 2},
	)

	s, err := New().GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Found {
		t.Fatal("Found = false for a run that exists")
	}
	if s.Goal != "rotate the signing key" {
		t.Errorf("Goal = %q, want the run-started detail", s.Goal)
	}
}

// A run id that names no log has no goal to report, and must not invent one.
func TestGetSession_MissingRunHasNoGoal(t *testing.T) {
	sandbox(t)
	s, err := New().GetSession("20260903T130001Z-absent")
	if err != nil {
		t.Fatal(err)
	}
	if s.Found || s.Goal != "" {
		t.Errorf("Found=%v Goal=%q; an absent run has neither", s.Found, s.Goal)
	}
}
