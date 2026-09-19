// SPDX-License-Identifier: MIT

package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/dispatch"
	"github.com/ankit373/hydra/internal/provider"
)

// A judge prompt embeds the whole task prompt and both candidate answers, so on
// a --local run it carries exactly the content the flag exists to keep on the
// machine. Both judges built their dispatch options from scratch and carried
// only the tier hint, so a local-only review sent its file diff to whichever
// head ranked highest. Measured before the fix: `hyctl vet --local --confidence`
// kept all three reviews local and made ten judge calls to a non-local head
// (#996). #500 is the same defect in the sampling path.
func TestJudgeDispatch_CarriesTheRunsConstraints(t *testing.T) {
	got := judgeOptions(Options{LocalOnly: true, MaxEstCostUSD: 0.25}, "3", "be a judge")

	if !got.LocalOnly {
		t.Error("LocalOnly = false on a local-only run; the judge can reach a paid head with the prompt and both answers")
	}
	if got.MaxCostUSD != 0.25 {
		t.Errorf("MaxCostUSD = %v, want the run's 0.25: an unbounded judge call is spend the run never agreed to", got.MaxCostUSD)
	}
	if got.MaxCostSource == "" {
		t.Error("MaxCostSource is empty, so a refusal cannot say where the ceiling came from")
	}
	if !got.NoCache {
		t.Error("NoCache = false: a stored verdict would decide the stopping rule on a comparison nobody made")
	}
	if got.TierHint != "3" {
		t.Errorf("TierHint = %q, want %q", got.TierHint, "3")
	}
	if got.System != "be a judge" {
		t.Errorf("System = %q, want the caller's", got.System)
	}
}

// A run with no constraints must not acquire one, or every judge call on an
// unrestricted run would be pinned local and silently degrade to textual
// equivalence on a machine with no local head.
func TestJudgeDispatch_AnUnconstrainedRunIsStillUnconstrained(t *testing.T) {
	got := judgeOptions(Options{}, "1", "s")
	if got.LocalOnly {
		t.Error("LocalOnly = true on a run that never asked for it")
	}
	if got.MaxCostUSD != 0 {
		t.Errorf("MaxCostUSD = %v, want 0 (no ceiling)", got.MaxCostUSD)
	}
}

// The ModeBest judge must read the run it belongs to, not a copy of the
// defaults: buildJudge is where the two are joined, and passing only the
// timeout through is how the constraints were lost in the first place.
func TestNewLLMJudge_KeepsTheRunItWasBuiltFor(t *testing.T) {
	j := newLLMJudge(nil, "1", Options{LocalOnly: true, MaxEstCostUSD: 0.5, JudgeTimeout: 5 * time.Second})

	if !j.run.LocalOnly {
		t.Error("the judge did not keep the run's LocalOnly, so its dispatch cannot honour it")
	}
	opts := judgeOptions(j.run, j.tier, "s")
	if !opts.LocalOnly || opts.MaxCostUSD != 0.5 {
		t.Errorf("the judge's dispatch options lost the run's constraints: %+v", opts)
	}
	if j.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", j.timeout)
	}
}

// findLLMJudge walks the judge tree buildJudge returns, since the LLM judge is
// wrapped in one or two composites depending on whether calibration loaded.
func findLLMJudge(j Judge) *LLMJudge {
	switch v := j.(type) {
	case *LLMJudge:
		return v
	case *CompositeJudge:
		if got := findLLMJudge(v.primary); got != nil {
			return got
		}
		return findLLMJudge(v.fallback)
	}
	return nil
}

// buildJudge is the seam where the constraints were lost: it holds the run's
// Options and used to hand the judge only the timeout out of them. Asserting
// on judgeDispatch alone leaves that wiring untested, and reverting this line
// is a mutation the rest of the suite does not catch.
func TestBuildJudge_TheJudgeItBuildsInheritsTheRunsConstraints(t *testing.T) {
	j := findLLMJudge(buildJudge(nil, Options{LocalOnly: true, MaxEstCostUSD: 0.25}, nil))
	if j == nil {
		t.Fatal("buildJudge returned no LLM judge to check")
	}
	opts := judgeOptions(j.run, j.tier, "s")
	if !opts.LocalOnly {
		t.Error("the judge buildJudge wired up would dispatch off-machine on a --local run")
	}
	if opts.MaxCostUSD != 0.25 {
		t.Errorf("MaxCostUSD = %v, want the run's 0.25", opts.MaxCostUSD)
	}
}

// capture records the options a judge really dispatched with.
type capture struct {
	seen []dispatch.Options
}

func (c *capture) fn(_ context.Context, _ string, o dispatch.Options) (*dispatch.Result, error) {
	c.seen = append(c.seen, o)
	return &dispatch.Result{Output: "YES"}, nil
}

// The call sites are the defect, not the helper. Reverting either judge to a
// hand-built dispatch.Options passes every assertion about judgeOptions, so
// each judge is driven here with a dispatcher that reports what it was sent.
func TestTheJudgesDispatchUnderTheRunsConstraints(t *testing.T) {
	run := Options{LocalOnly: true, MaxEstCostUSD: 0.25, JudgeTierHint: "4"}

	t.Run("sprt equivalence judge", func(t *testing.T) {
		c := &capture{}
		s := &Swarm{askJudge: c.fn}
		// Two answers that differ, or the judge short-circuits on text equality
		// and never dispatches at all.
		if !s.judgeEquivalence(context.Background(), "the task", run)("answer one", "answer two") {
			t.Fatal("the judge did not report the stub's YES")
		}
		assertConstrained(t, c, run)
	})

	t.Run("modebest judge", func(t *testing.T) {
		c := &capture{}
		j := newLLMJudge(nil, "4", run)
		j.ask = c.fn
		// Two successful attempts, or Judge takes the trivial single-answer
		// path and never dispatches.
		_, _ = j.Judge(context.Background(), "the task", []Attempt{
			{Head: provider.Head{ID: "a"}, Status: StatusOK, Output: "one"},
			{Head: provider.Head{ID: "b"}, Status: StatusOK, Output: "two"},
		})
		assertConstrained(t, c, run)
	})
}

func assertConstrained(t *testing.T, c *capture, run Options) {
	t.Helper()
	if len(c.seen) == 0 {
		t.Fatal("the judge never dispatched, so this proves nothing")
	}
	for i, o := range c.seen {
		if !o.LocalOnly {
			t.Errorf("dispatch %d: LocalOnly = false on a --local run; the prompt and both answers reach a paid head", i)
		}
		if o.MaxCostUSD != run.MaxEstCostUSD {
			t.Errorf("dispatch %d: MaxCostUSD = %v, want the run's %v", i, o.MaxCostUSD, run.MaxEstCostUSD)
		}
		if !o.NoCache {
			t.Errorf("dispatch %d: NoCache = false", i)
		}
	}
}
