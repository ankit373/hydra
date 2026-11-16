// SPDX-License-Identifier: MIT

package trust

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeLog writes a calibration.jsonl of said-correct verdicts, one per rate,
// seeded so the stream is the same every run.
func writeLog(t *testing.T, path string, seed uint64, rates ...[2]float64) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 1545))
	var b strings.Builder
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	step := 0
	for _, seg := range rates {
		for range int(seg[1]) {
			outcome := OutcomeIncorrect
			if r.Float64() < seg[0] {
				outcome = OutcomeCorrect
			}
			raw, _ := json.Marshal(record{
				TS: start.Add(time.Duration(step) * time.Minute).Format(time.RFC3339), Source: "head",
				Domain: "go", SaidCorrect: true, Outcome: int(outcome),
			})
			fmt.Fprintln(&b, string(raw))
			step++
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCalibrator_DetectsDropOnReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")
	writeLog(t, path, 1, [2]float64{0.92, 2000}, [2]float64{0.70, 300})

	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	correct, total := c.CommitmentsSince("head", "go")
	// Found within 100 observations of the drop, and not placed far before it.
	if total < 200 || total > 350 {
		t.Fatalf("since-counts cover %d answers, want about the 300 after the drop", total)
	}
	if rate := float64(correct) / float64(total); rate > 0.8 {
		t.Errorf("since-rate %.2f, want the post-drop rate near 0.70", rate)
	}
	if _, all := c.CommitmentsIn("head", "go"); all != 2300 {
		t.Errorf("CommitmentsIn total %d, want the whole history of 2300", all)
	}
	if conf := c.store[keyFor("head", "go")]; conf.changed.IsZero() {
		t.Error("the change was found but not dated")
	}

	// A stable source reads the same since its start as in full.
	stable := filepath.Join(t.TempDir(), "calibration.jsonl")
	writeLog(t, stable, 2, [2]float64{0.92, 2300})
	if c, err = New(stable); err != nil {
		t.Fatal(err)
	}
	sc, st := c.CommitmentsSince("head", "go")
	if ac, at := c.CommitmentsIn("head", "go"); sc != ac || st != at {
		t.Errorf("stable source: since %d/%d, full %d/%d", sc, st, ac, at)
	}
}

// A source judged worse and then no longer called is believed changed again
// while the rest of its domain is judged, so it gets tried; other domains'
// judgements do not move it.
func TestCalibrator_IdleSourceRelaxesWhileOthersAreJudged(t *testing.T) {
	c, _ := New("")
	r := rand.New(rand.NewPCG(3, 1545))
	for i := range 2300 {
		rate := 0.92
		if i >= 2000 {
			rate = 0.70
		}
		outcome := OutcomeIncorrect
		if r.Float64() < rate {
			outcome = OutcomeCorrect
		}
		mustUpdate(t, c, "dropped", "go", true, outcome)
	}
	if _, total := c.CommitmentsSince("dropped", "go"); total == 0 || total > 400 {
		t.Fatalf("since-counts %d after the drop, want the post-drop stretch", total)
	}
	before, _ := c.CommitmentsSince("dropped", "go")

	feed(t, c, "other", "sql", 6000, 0, 0, 0)
	if after, _ := c.CommitmentsSince("dropped", "go"); after != before {
		t.Errorf("judging another domain moved the idle source: %d -> %d", before, after)
	}

	feed(t, c, "other", "go", 6000, 0, 0, 0)
	if _, total := c.CommitmentsSince("dropped", "go"); total > 5 {
		t.Errorf("since-counts %d after 6000 judged answers without it, want a near-empty record", total)
	}
}

func TestCalibrator_SnapshotRoundTripsDetector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")
	writeLog(t, path, 4, [2]float64{0.92, 600}, [2]float64{0.70, 200})

	// The first load replays and writes the snapshot; the second resumes from it.
	full, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshotPath(path)); err != nil {
		t.Fatalf("no snapshot written: %v", err)
	}
	resumed, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(full.store, resumed.store) || !reflect.DeepEqual(full.steps, resumed.steps) {
		t.Error("loading from the snapshot differs from a full replay")
	}
	fc, ft := full.CommitmentsSince("head", "go")
	if rc, rt := resumed.CommitmentsSince("head", "go"); fc != rc || ft != rt {
		t.Errorf("since-counts %d/%d from the snapshot, %d/%d from replay", rc, rt, fc, ft)
	}
}

// A snapshot from before detectors existed is ignored, and the log replayed
// from the start, so every cell gets a detector built from its whole history.
func TestCalibrator_OldSnapshotSchemaReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")
	writeLog(t, path, 5, [2]float64{0.92, 300})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	old := fmt.Sprintf(`{"schema":1,"offset":%d,"entries":[{"source":"head","domain":"go","tp":2,"fp":1,"tn":1,"fn":1}]}`, info.Size())
	if err := os.WriteFile(snapshotPath(path), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, total := c.CommitmentsIn("head", "go"); total != 300 {
		t.Errorf("total %d, want the 300 the log holds, not the old snapshot's 1", total)
	}
	if _, total := c.CommitmentsSince("head", "go"); total != 300 {
		t.Errorf("since-total %d, want a detector built from all 300", total)
	}
}

// minute is the timestamp writeLog gives the record at step m.
func minute(m int) time.Time { return time.Date(2026, 11, 1, 0, m, 0, 0, time.UTC) }

func TestCalibration_RecordsRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")
	writeLog(t, path, 1, [2]float64{0.92, 2000}, [2]float64{0.70, 300}, [2]float64{0.92, 1000})

	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Report()[0]
	if s.Changed.Before(minute(2000)) || s.Changed.After(minute(2300)) {
		t.Errorf("changed %v, want it dated within the drop", s.Changed)
	}
	if s.Recovered.Before(minute(2300)) || s.Recovered.After(minute(2500)) {
		t.Errorf("recovered %v, want it dated within 200 answers of the rate coming back", s.Recovered)
	}
}

func TestCalibration_NewChangeClearsRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")
	writeLog(t, path, 1, [2]float64{0.92, 2000}, [2]float64{0.70, 300},
		[2]float64{0.92, 1000}, [2]float64{0.60, 300})

	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Report()[0]
	if !s.Recovered.IsZero() {
		t.Errorf("recovered %v survived the second drop", s.Recovered)
	}
	if s.Changed.Before(minute(3300)) {
		t.Errorf("changed %v, want the second drop's date", s.Changed)
	}
}

// A model that got better was not broken, so a rise alone dates nothing.
func TestCalibration_RiseIsNotAChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.jsonl")
	writeLog(t, path, 1, [2]float64{0.70, 2000}, [2]float64{0.92, 1000})

	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Report()[0]
	if !s.Changed.IsZero() || !s.Recovered.IsZero() {
		t.Errorf("changed %v recovered %v after a rise, want neither", s.Changed, s.Recovered)
	}
	if s.SinceTotal >= 3000 {
		t.Errorf("since %d, want routing to read the record after the rise", s.SinceTotal)
	}
}
