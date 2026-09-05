// SPDX-License-Identifier: MIT

package trust

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPaths_LiveUnderTheUsersHydraDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_HOME", "")

	for name, got := range map[string]string{
		"calibration.jsonl": DefaultPath(),
		"trust.jsonl":       DefaultLogPath(),
		"coagreement.jsonl": DefaultCoAgreementPath(),
	} {
		want := filepath.Join(home, ".hydra", name)
		if got != want {
			t.Errorf("path for %s = %q, want %q", name, got, want)
		}
	}
	// The two must not collide — one is the training record, the other the run
	// ledger, and merging them would corrupt both.
	if DefaultPath() == DefaultLogPath() {
		t.Error("calibration and run-log paths are the same file")
	}
}

// $HYDRA_HOME must win over $HOME (#442).
func TestDefaultPaths_PreferHydraHomeOverHome(t *testing.T) {
	home := t.TempDir()
	hydraHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HYDRA_HOME", hydraHome)

	for name, got := range map[string]string{
		"calibration.jsonl": DefaultPath(),
		"trust.jsonl":       DefaultLogPath(),
		"coagreement.jsonl": DefaultCoAgreementPath(),
	} {
		want := filepath.Join(hydraHome, name)
		if got != want {
			t.Errorf("path for %s = %q, want %q ($HYDRA_HOME, not $HOME)", name, got, want)
		}
	}
}

func TestDecision_StringCoversBothOutcomes(t *testing.T) {
	if got := DecisionAccept.String(); got != "accept" {
		t.Errorf("DecisionAccept.String() = %q", got)
	}
	// Anything else is the budget-exhausted outcome, which must be labelled
	// distinctly — "we ran out of samples" is not "we are confident".
	other := Decision(99)
	if got := other.String(); got != "stopped_on_budget" {
		t.Errorf("Decision(99).String() = %q, want stopped_on_budget", got)
	}
	if DecisionAccept.String() == other.String() {
		t.Error("accept and budget-exhausted render identically")
	}
}

// clamp01 keeps probabilities off 0 and 1 so the log-odds stay finite. Without
// it a perfectly-calibrated source produces ±Inf and the whole LLR ledger
// becomes NaN.
func TestClamp01_KeepsLogOddsFinite(t *testing.T) {
	for _, p := range []float64{-1, 0, 1e-12, 0.5, 1 - 1e-12, 1, 2} {
		got := clamp01(p)
		if got <= 0 || got >= 1 {
			t.Errorf("clamp01(%v) = %v, not strictly inside (0,1)", p, got)
		}
		odds := math.Log(got / (1 - got))
		if math.IsInf(odds, 0) || math.IsNaN(odds) {
			t.Errorf("clamp01(%v) = %v gives a non-finite log-odds %v", p, got, odds)
		}
	}
	// Values already inside the range pass through unchanged.
	if got := clamp01(0.42); got != 0.42 {
		t.Errorf("clamp01(0.42) = %v, want it untouched", got)
	}
}

// A calibration store with no file is a first run, not a failure.
func TestNew_MissingCalibrationFileIsAFreshStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.jsonl")

	c, err := New(path)
	if err != nil {
		t.Fatalf("a missing calibration file errored: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil for a missing file")
	}
	// An unseen source falls back to the prior rather than claiming knowledge.
	llr := c.LLR("never-seen-source", "go", true)
	if math.IsNaN(llr) || math.IsInf(llr, 0) {
		t.Errorf("an unseen source produced a non-finite LLR: %v", llr)
	}
}

// Malformed lines must be skipped, not abort the load — the file is appended to
// by concurrent runs and a crash can truncate the last record.
func TestNew_SkipsMalformedRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cal.jsonl")
	// outcome is an int on the wire, not a label — writing "correct" there makes
	// the whole line fail to unmarshal and be skipped as malformed, which is how
	// the first version of this fixture silently tested nothing.
	good := `{"ts":"2026-08-01T00:00:00Z","source":"model:a","domain":"go","said_correct":true,"outcome":1}`
	body := strings.Join([]string{
		good,
		`{not json`,
		``,
		good,
		`{"ts":"trunc`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := New(path)
	if err != nil {
		t.Fatalf("malformed records aborted the load: %v", err)
	}
	// The two good records must have registered.
	if llr := c.LLR("model:a", "go", true); llr == 0 {
		t.Error("the well-formed records did not contribute any evidence")
	}
}

// Record appends to the store and to disk; the next load must see it, or
// calibration silently never accumulates.
func TestRecord_PersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cal.jsonl")

	c, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := c.Update("model:x", "go", true, OutcomeCorrect); err != nil {
			t.Fatal(err)
		}
	}
	before := c.LLR("model:x", "go", true)

	reloaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	after := reloaded.LLR("model:x", "go", true)

	if math.Abs(before-after) > 1e-9 {
		t.Errorf("LLR was %v in memory and %v after reload — records are not "+
			"surviving the round trip", before, after)
	}
}

func TestRecord_UnwritablePathIsAnError(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocked, []byte("i am a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(filepath.Join(blocked, "cal.jsonl"))
	if err != nil {
		// Constructing may already fail, which is also acceptable.
		return
	}
	if err := c.Update("model:x", "go", true, OutcomeCorrect); err == nil {
		t.Error("recording under a blocked path reported success — calibration " +
			"would silently never accumulate")
	}
}

// The run ledger is what `hyctl trust explain` reads. A missing file is an
// empty history, not an error.
func TestLoadRuns_MissingLedgerIsEmpty(t *testing.T) {
	runs, err := LoadRuns(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("a missing ledger errored: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("got %d runs from a missing ledger", len(runs))
	}
}

func TestLogRunAndLoadRuns_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.jsonl")

	want := RunLog{
		TaskHash:   TaskHash("is this migration safe?"),
		Domain:     "go",
		TargetConf: 0.95,
		FinalConf:  0.97,
		Samples:    3,
		Decision:   DecisionAccept.String(),
	}
	if err := LogRun(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRuns(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d runs, want 1", len(got))
	}
	if got[0].TaskHash != want.TaskHash || got[0].Decision != want.Decision {
		t.Errorf("round trip changed the record: %+v", got[0])
	}
}

// TaskHash correlates a run with `hyctl trust explain <hash>`. It must be
// stable for the same prompt and differ for different ones, or the correlation
// is useless.
func TestTaskHash_StableAndDiscriminating(t *testing.T) {
	a := TaskHash("some prompt")
	if a != TaskHash("some prompt") {
		t.Error("TaskHash is not stable for the same prompt")
	}
	if a == TaskHash("a different prompt") {
		t.Error("two different prompts hash the same")
	}
	if a == "" {
		t.Error("TaskHash returned an empty string")
	}
}
