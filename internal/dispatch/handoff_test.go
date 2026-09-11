// SPDX-License-Identifier: MIT

package dispatch

import (
	"testing"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/testutil"
)

// A --confidence, --swarm or --file run reached none of this: they do not go
// through Dispatcher.Dispatch's success branch, which was the only caller of
// the handoff writer. So the causal chain had a hole exactly where the
// highest-stakes work happened, and `--file`, the flag most likely to name a
// contended file, was the one whose handoff was never written (#766).

// One tick per head that answered. A fan-out that consulted three heads is
// three events, and saying so is what keeps a later dispatch to one of them
// ordered after the ensemble rather than concurrent with it.
func TestSaveHandoff_TicksEveryHeadThatRan(t *testing.T) {
	testutil.NewSandbox(t)

	if _, err := SaveHandoff(HandoffRecord{
		From: "hydra-ensemble", Model: "SPRT ensemble", Task: "is this safe?",
		Files: []string{"internal/auth/token.go"}, Output: "yes",
		Agents: []string{"ollama/a", "ollama/b", "ollama/c"},
	}); err != nil {
		t.Fatal(err)
	}

	h := loadHandoff(t)
	for _, want := range []string{"ollama/a", "ollama/b", "ollama/c"} {
		if h.Clock[want] != 1 {
			t.Errorf("clock[%q] = %d, want 1: the head answered and the clock does "+
				"not say so", want, h.Clock[want])
		}
	}
	if len(h.Files) != 1 || h.Files[0] != "internal/auth/token.go" {
		t.Errorf("Files = %v, want the file the run was about; ConflictsWith has "+
			"nothing to overlap without it", h.Files)
	}
}

// The clock is inherited, not replaced: a fan-out after a single dispatch must
// carry that dispatch's history, or the chain restarts at every ensemble.
func TestSaveHandoff_InheritsThePriorClock(t *testing.T) {
	testutil.NewSandbox(t)

	if _, err := SaveHandoff(HandoffRecord{
		From: "hydra-tier-10", Agents: []string{"ollama/a"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveHandoff(HandoffRecord{
		From: "hydra-ensemble", Agents: []string{"ollama/b"},
	}); err != nil {
		t.Fatal(err)
	}

	h := loadHandoff(t)
	if h.Clock["ollama/a"] != 1 {
		t.Errorf("the prior dispatch's tick was lost: %v", h.Clock)
	}
	if h.Clock["ollama/b"] != 1 {
		t.Errorf("the ensemble did not tick: %v", h.Clock)
	}
	// And the ensemble is causally after the dispatch, not concurrent with it.
	first := a2a.Clock{"ollama/a": 1}
	if got := first.Compare(h.Clock); got != a2a.Before {
		t.Errorf("the single dispatch compares %v to the ensemble that followed "+
			"it, want before", got)
	}
}

// The acceptance criterion: two fan-outs that never saw each other, touching
// one file, must read as a conflict.
//
// SPRT stops as soon as the confidence bar is met, so two runs on one prompt
// generally sample different heads, which is what makes their clocks
// concurrent rather than merely different.
func TestSaveHandoff_TwoConcurrentEnsemblesOnOneFileConflict(t *testing.T) {
	testutil.NewSandbox(t)
	const file = "internal/auth/token.go"

	// Both start from the same history, because neither has seen the other's
	// handoff: that is what "concurrent" means here.
	if _, err := SaveHandoff(HandoffRecord{From: "hydra-tier-10", Agents: []string{"ollama/seed"}}); err != nil {
		t.Fatal(err)
	}
	base := loadHandoff(t).Clock

	runA := a2a.Handoff{From: "hydra-ensemble", Files: []string{file},
		Clock: base.Tick("ollama/a").Tick("ollama/b")}
	runB := a2a.Handoff{From: "hydra-ensemble", Files: []string{file},
		Clock: base.Tick("ollama/b").Tick("ollama/c")}

	if got := runA.Clock.Compare(runB.Clock); got != a2a.Concurrent {
		t.Fatalf("two independent ensembles compare %v, want concurrent", got)
	}
	if !runA.ConflictsWith(&runB) {
		t.Error("two concurrent ensembles on one file do not conflict, so the " +
			"collision the blast radius warned about is undetectable")
	}

	// A different file is not a conflict however concurrent the clocks are.
	runB.Files = []string{"internal/other.go"}
	if runA.ConflictsWith(&runB) {
		t.Error("ensembles on different files were reported as conflicting")
	}
}

// The honest limit, pinned so nobody reads more into the clock than it says:
// two runs over the identical head set are indistinguishable. This is the hole
// the single-dispatch path already has, not one the fan-out writer introduces,
// and a synthetic per-run key would trade it for an unbounded clock.
func TestSaveHandoff_IdenticalHeadSetsAreNotConcurrent(t *testing.T) {
	base := a2a.Clock{"ollama/seed": 1}
	runA := base.Tick("ollama/a").Tick("ollama/b")
	runB := base.Tick("ollama/a").Tick("ollama/b")

	if got := runA.Compare(runB); got != a2a.Equal {
		t.Fatalf("compare = %v, want equal: this documents the limit, and if it "+
			"has changed the doc comment on SaveHandoff needs to change too", got)
	}
}

// Nothing answered means no agent did anything, so there is no event to record
// and the prior handoff must stay the newest.
func TestSaveHandoff_NoAgentsLeavesTheClockAlone(t *testing.T) {
	testutil.NewSandbox(t)

	if _, err := SaveHandoff(HandoffRecord{From: "hydra-tier-10", Agents: []string{"ollama/a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveHandoff(HandoffRecord{From: "hydra-ensemble", Agents: nil}); err != nil {
		t.Fatal(err)
	}

	if got := loadHandoff(t).Clock["ollama/a"]; got != 1 {
		t.Errorf("clock[ollama/a] = %d, want 1: an empty fan-out moved the clock", got)
	}
}
