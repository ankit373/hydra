// SPDX-License-Identifier: MIT

package suites

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/executor"
	"github.com/ankit373/hydra/internal/provider"
	"github.com/ankit373/hydra/internal/stats"
	"github.com/ankit373/hydra/internal/testutil"
)

// The licence keeps FaithBench's rows and labels out of the repository: the lock pins
// the upstream files, no split file is committed, and the split is derived next to the
// data the same way every time.
func TestFaithSuite_PinnedSplit(t *testing.T) {
	lock, err := bench.ReadLock(filepath.Join(benchDir, "datasets.lock"))
	if err != nil {
		t.Fatal(err)
	}
	ds := lock["faithbench"]
	if ds.Revision != "vectara/FaithBench@cf89797d82812c23b5d5e5c121f1d9b8983bbbce" || ds.Licence != "CC-BY-NC-SA-4.0" ||
		ds.LicenceVerified == "" || !ds.LocalSplits {
		t.Fatalf("faithbench lock block = %+v", ds)
	}
	if err := ds.RequireFiles("faithbench", faithFiles()...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(benchDir, "splits", "faithbench.tsv")); !os.IsNotExist(err) {
		t.Fatal("bench/splits/faithbench.tsv is committed; its licence forbids it")
	}
	smoke, err := os.ReadFile(filepath.Join(benchDir, "configs", "smoke.ids"))
	if err != nil || strings.Contains(string(smoke), "faithbench") {
		t.Fatalf("smoke.ids lists FaithBench ids, or is unreadable: %v", err)
	}

	fix, root := fixtureLock(t)["faithbench"], fixtureRoot(t)
	fix.LocalSplits = true
	bdir := t.TempDir()
	if _, err := GenerateSplits(map[string]bench.Dataset{"faithbench": fix}, root, bdir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bdir, "splits", "faithbench.tsv")); !os.IsNotExist(err) {
		t.Fatal("go generate wrote a FaithBench split file")
	}
	s := Registry()["faithbench"]()
	data := bench.DataDir(root, "faithbench")
	path, err := bench.LocalSplitsFile(s, fix, data)
	if err != nil || filepath.Dir(path) != data {
		t.Fatalf("local split file %q, %v; want it in the data dir", path, err)
	}
	items, err := bench.Load(s, fix, data, path)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]bench.Item{}
	for _, it := range items {
		by[it.ID] = it
	}
	for _, g := range []struct {
		id    string
		split bench.Split
		rank  uint64
	}{
		{"batch_1/0/1", bench.SplitCalibration, 13339879110007465891},
		{"batch_1/0/2", bench.SplitTest, 1438464162758277943},
		{"batch_2/0/3", bench.SplitTest, 10198047877562923249},
		{"batch_3/0/2", bench.SplitTest, 5154397786755816321},
	} {
		if it := by[g.id]; it.Split != g.split || it.Rank != g.rank {
			t.Errorf("%s = (%v, %d), want (%v, %d)", g.id, it.Split, it.Rank, g.split, g.rank)
		}
	}
}

// Rows apply the span rule in runes: an Unwanted span marks its sentence, an untouched
// sentence is supported, and one only Benign or Questionable spans touch is left out.
func TestFaithSuite_SentenceLabels(t *testing.T) {
	_, items := loadFixture(t, "faithbench")
	got := map[string]string{}
	for _, it := range items {
		l, err := it.Label()
		if err != nil {
			t.Fatal(err)
		}
		got[it.ID] = l
	}
	want := map[string]string{
		"batch_1/0/1": "supported", "batch_1/0/2": "not_supported", "batch_1/0/4": "supported",
		// "Zoë" and "café" are two bytes each: a byte offset would mark the first sentence.
		"batch_1/1/1": "supported", "batch_1/1/2": "not_supported",
		"batch_2/0/1": "supported", "batch_2/0/2": "supported", "batch_2/0/3": "not_supported",
		"batch_3/0/1": "supported", "batch_3/0/2": "supported", "batch_3/0/3": "supported", "batch_3/0/4": "not_supported",
	}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v (batch_1/0/3 left out)", got, want)
	}
	for id, l := range want {
		if got[id] != l {
			t.Errorf("%s = %q, want %q", id, got[id], l)
		}
	}
	if _, ok, err := faithLabel(0, 5, []faithAnnotation{{Label: []string{"Unwanted"}}}); err == nil || ok {
		t.Error("an annotation with no span was read")
	}
}

// faithJudge is a judge model: a fixed reply per sentence, the rest "Yes.".
type faithJudge map[string]string

func (j faithJudge) Execute(_ context.Context, req executor.Request) (*executor.Response, error) {
	out := "Yes."
	for k, v := range j {
		if strings.Contains(req.Prompt, k) {
			out = v
		}
	}
	return &executor.Response{Output: out, InputTokens: 100, OutputTokens: 2, Model: req.Head.Meta["model"]}, nil
}

// A fixture run grades the ground arm, which calls nothing, and a judge arm side by
// side, and the summary counts recall and false flags from the test split's six items.
func TestFaithSuite_FixtureGradedRun(t *testing.T) {
	testutil.NewSandbox(t).SetKey(t, "OPENROUTER_API_KEY", "test-key")
	s, items := loadFixture(t, "faithbench")
	cfg := &bench.Config{Tier: "fixture", Track: "claims", Suites: []string{"faithbench"}, NTest: 100, Seed: 1, MaxUSD: 1,
		Arms: []string{"ground", "judge"}, Checks: []string{"ground"}, Models: map[string]string{"judge": "openrouter:vendor/judge"}}
	prices := bench.PriceSnapshot{"vendor/judge": {Model: "vendor/judge", InPerM: 1, OutPerM: 1, Source: "fixture"}}
	splits := filepath.Join(t.TempDir(), "faithbench.tsv")
	f, _ := os.Create(splits)
	f.Close()
	run, byID, err := bench.Plan(cfg, s, bench.Dataset{}, splits, items, cfg.NTest, nil, prices, bench.HostInfo{})
	if err != nil {
		t.Fatal(err)
	}
	judge := faithJudge{
		"52,000":      "maybe", // not a yes or no: a format fail, and a miss
		"longest":     "No, the source never says so.",
		"cheese":      "No.",
		"144 stalls.": "no", // true, but the source says twelve dozen: a false flag
	}
	var calls int
	tick := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	results, state := t.TempDir(), t.TempDir()
	r := bench.Runner{Run: run, Suite: s, Items: byID, MaxUSD: cfg.MaxUSD, ResultsDir: results, StateDir: state, Out: io.Discard,
		Exec:  func(provider.Head) executor.Executor { calls++; return judge },
		Sleep: func(context.Context, time.Duration) error { return nil },
		Now:   func() time.Time { tick = tick.Add(time.Second); return tick }}
	o, err := r.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sum, err := bench.Report(o.Dir, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.ItemIDs) != 6 || calls != 6 || sum.Incomplete {
		t.Fatalf("%d test items, %d judge calls, incomplete %v; want 6, one call each, complete", len(run.ItemIDs), calls, sum.Incomplete)
	}
	// Test split: 1/0/2 and 2/0/3 and 3/0/4 are not supported; 2/0/1, 3/0/1 and 3/0/2 are.
	for arm, want := range map[string]bench.DetectionStats{
		"ground": {Positives: 3, Caught: 1, Negatives: 3, FalseFlags: 1}, // only 52,000 is caught; 144 is flagged
		"judge":  {Positives: 3, Caught: 2, Negatives: 3, FalseFlags: 1},
	} {
		d := sum.Arms[arm].Detection
		if d == nil || d.Positives != want.Positives || d.Caught != want.Caught || d.Negatives != want.Negatives || d.FalseFlags != want.FalseFlags {
			t.Fatalf("%s detection = %+v, want %+v", arm, d, want)
		}
		lo, hi, _ := stats.Wilson(want.Caught, want.Positives, stats.Z95)
		if d.Recall != float64(want.Caught)/3 || d.RecallWilson != [2]float64{lo, hi} {
			t.Errorf("%s recall %v %v, want %v (%v–%v)", arm, d.Recall, d.RecallWilson, float64(want.Caught)/3, lo, hi)
		}
		lo, hi, _ = stats.Wilson(want.FalseFlags, want.Negatives, stats.Z95)
		if d.FalseFlagRate != 1.0/3 || d.FalseFlagWilson != [2]float64{lo, hi} {
			t.Errorf("%s false flags %v %v", arm, d.FalseFlagRate, d.FalseFlagWilson)
		}
	}
	if g, j := sum.Arms["ground"], sum.Arms["judge"]; g.CostUSD != 0 || g.TokensIn.Mean != 0 || j.FormatFails != 1 || !(j.CostUSD > 0) {
		t.Errorf("ground cost %v tokens %v, judge format fails %d cost %v", g.CostUSD, g.TokensIn, j.FormatFails, j.CostUSD)
	}
	table := bench.DetectionTable([]bench.Summary{sum})
	if !strings.Contains(table, "| faithbench | ground | none | 33.3 (6.1–79.2) of 3 | 33.3 (6.1–79.2) of 3 |") ||
		!strings.Contains(table, "| faithbench | judge | openrouter:vendor/judge | 66.7 (") || strings.Contains(table, "oracle") {
		t.Errorf("detection table:\n%s", table)
	}
}

func TestFaithSuite_GradeReadsBothArmsAnswers(t *testing.T) {
	s, items := loadFixture(t, "faithbench")
	var pos bench.Item
	for _, it := range items {
		if it.ID == "batch_1/0/2" {
			pos = it
		}
	}
	for answer, want := range map[string]struct{ flagged, format bool }{
		"not_supported": {true, false}, "supported": {false, false}, "nothing_to_check": {false, false},
		"No.": {true, false}, "yes, it says so": {false, false}, "Unsupported": {false, true},
	} {
		g, err := s.Grade(context.Background(), pos, answer)
		if err != nil || g.Detection == nil || !g.Detection.Positive || g.Detection.Flagged != want.flagged ||
			g.Format != want.format || g.Passed != want.flagged {
			t.Errorf("Grade(%q) = %+v %+v, %v; want flagged=%v format=%v", answer, g, g.Detection, err, want.flagged, want.format)
		}
	}
	if _, err := (&faithbench{}).Grade(context.Background(), pos, "yes"); err == nil {
		t.Error("graded before the suite loaded")
	}
	if _, err := s.(bench.Checker).Check(context.Background(), pos, "judge"); err == nil {
		t.Error("ran a check the suite does not have")
	}
}
