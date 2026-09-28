// SPDX-License-Identifier: MIT

package probe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/rank"
)

// The point of #815: a head the catalogue scores lower can rank first once
// this machine has verified enough of what it actually produced.
func TestRunWith_MeasurementReordersHeads(t *testing.T) {
	ps := []provider.Provider{&fakeProvider{id: "a", heads: []provider.Head{
		head("declared-high", 80, false),
		head("measured-good", 60, false),
	}}}

	declared := runWith(context.Background(), ps, nil)
	if declared.Heads[0].ID != "declared-high" {
		t.Fatalf("with no measurement, got %s first, want declared-high", declared.Heads[0].ID)
	}

	lookup := func(id string) rank.Measurement {
		if id == "measured-good" {
			return rank.Measurement{Correct: 38, Total: 40}
		}
		return rank.Measurement{Correct: 4, Total: 40}
	}
	measured := runWith(context.Background(), ps, lookup)
	if measured.Heads[0].ID != "measured-good" {
		t.Errorf("got %s first, want the measured-better head", measured.Heads[0].ID)
	}
	if measured.Cortex == nil || measured.Cortex.ID != "measured-good" {
		t.Errorf("Cortex is %v, want it to follow the ranking", measured.Cortex)
	}
}

// Result.Scores is what hyctl probe renders to explain an order, so it has to
// cover every head returned, not just the adjusted ones.
func TestRunWith_ScoresCoverEveryHead(t *testing.T) {
	ps := []provider.Provider{&fakeProvider{id: "a", heads: []provider.Head{
		head("adjusted", 60, false),
		head("untouched", 70, false),
	}}}
	lookup := func(id string) rank.Measurement {
		if id == "adjusted" {
			return rank.Measurement{Correct: 30, Total: 30}
		}
		return rank.Measurement{}
	}

	res := runWith(context.Background(), ps, lookup)
	if len(res.Scores) != len(res.Heads) {
		t.Fatalf("got %d scores for %d heads", len(res.Scores), len(res.Heads))
	}
	if sc := res.Scores["untouched"]; sc.Adjusted() || sc.Declared != 70 {
		t.Errorf("unmeasured head scored %+v, want declared 70 untouched", sc)
	}
	if sc := res.Scores["adjusted"]; !sc.Adjusted() || sc.N != 30 {
		t.Errorf("measured head scored %+v, want an adjustment backed by 30 commitments", sc)
	}
}

// A calibration store that will not load must degrade to declared scores and
// say so. Silently ranking on a different basis than the user's history earned
// is the failure mode #248 named for providers, in the surface beside it.
func TestRun_UnreadableCalibrationIsAWarningNotAFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HYDRA_HOME", home)
	// A directory where the store should be. A malformed *line* is skipped on
	// purpose, so it is not the failure this guards; an unreadable store is,
	// and this reaches it without a chmod that Windows would ignore.
	if err := os.Mkdir(filepath.Join(home, "calibration.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := Run(context.Background())
	if res == nil {
		t.Fatal("Run returned nil: an unreadable calibration store must not stop a machine scan")
	}
	var found string
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "calibration:") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("no calibration warning in %q: the ranking silently changed basis", res.Warnings)
	}
	if !strings.Contains(found, "declared") {
		t.Errorf("warning %q does not say what the ranking fell back to", found)
	}
}

// The store loading fine is the ordinary path, and must add no warning at all.
func TestRun_ReadableCalibrationWarnsAboutNothing(t *testing.T) {
	t.Setenv("HYDRA_HOME", t.TempDir())

	for _, w := range Run(context.Background()).Warnings {
		if strings.HasPrefix(w, "calibration:") {
			t.Errorf("warned %q about a store that has simply never been written", w)
		}
	}
}
