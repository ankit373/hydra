// SPDX-License-Identifier: MIT

package trust

import (
	"path/filepath"
	"testing"
)

func TestFindRunBySpan(t *testing.T) {
	runs := []RunLog{
		{TaskHash: "old", SpanID: "aaaa"},
		{TaskHash: "other", SpanID: "bbbb"},
		{TaskHash: "new", SpanID: "aaaa"},
		{TaskHash: "legacy"}, // written before span ids, replays by task hash
	}

	got, ok := FindRunBySpan(runs, "aaaa")
	if !ok {
		t.Fatal("span aaaa was recorded and could not be found")
	}
	// A span id derives from a task id, which a caller may reuse, so the
	// newest run under it is the one a verdict is about.
	if got.TaskHash != "new" {
		t.Errorf("found %q, want the newest run under that span", got.TaskHash)
	}
	if _, ok := FindRunBySpan(runs, "zzzz"); ok {
		t.Error("a span nothing recorded was found")
	}
	// A run with no span must never be reached by an empty lookup: every
	// pre-#1144 row has one, and matching them all would train the wrong ledger.
	if _, ok := FindRunBySpan(runs, ""); ok {
		t.Error("an empty span matched a run; every legacy row would train on any verdict")
	}
}

// One ledger is one body of evidence however many verdicts land on its span.
// Replaying it twice counts every vote twice, which inflates both cells and
// makes the calibration say the opposite of what was measured.
func TestApplied_IsRecordedOnceAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust_applied.jsonl")

	if done, err := IsApplied(path, "span-1"); err != nil || done {
		t.Fatalf("a fresh install reports span-1 already applied (done=%v err=%v)", done, err)
	}
	if err := MarkApplied(path, "span-1"); err != nil {
		t.Fatal(err)
	}
	if done, err := IsApplied(path, "span-1"); err != nil || !done {
		t.Errorf("span-1 was marked and reads back unapplied (done=%v err=%v)", done, err)
	}
	if done, err := IsApplied(path, "span-2"); err != nil || done {
		t.Errorf("span-2 was never marked and reads applied (done=%v err=%v)", done, err)
	}
	if err := MarkApplied(path, ""); err == nil {
		t.Error("an empty span was accepted; it would mark every unspanned run applied")
	}
}
