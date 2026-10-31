// SPDX-License-Identifier: MIT

package runlog

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ankit373/hydra/internal/testutil"
)

// A run is named by what it is for, and that name is the only one a reader can
// trust: task_started's Detail is the routing enum, which is why a reader that
// fell back to it listed a workflow as "GRUNT" (#910).
func TestDeclareRun_RecordsTheSubject(t *testing.T) {
	testutil.NewSandbox(t)
	DeclareRun("run-1", "task-1", "check the tiny local model answers twice")
	FinishRun("run-1", "task-1")

	events, err := Load("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want the declaration and the finish", len(events))
	}
	if events[0].Kind != KindRunStarted {
		t.Errorf("first event = %q, want %q", events[0].Kind, KindRunStarted)
	}
	if events[0].Detail != "check the tiny local model answers twice" {
		t.Errorf("subject = %q", events[0].Detail)
	}
	if events[1].Kind != KindRunFinished {
		t.Errorf("last event = %q, want %q", events[1].Kind, KindRunFinished)
	}
}

// A run that says nothing about itself must still declare that it started.
// Skipping the event is what left the field free for a routing key to fill.
func TestDeclareRun_AnEmptySubjectStillOpensTheRun(t *testing.T) {
	testutil.NewSandbox(t)
	DeclareRun("run-2", "task-2", "   ")

	events, err := Load("run-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != KindRunStarted {
		t.Fatalf("events = %+v, want one run_started", events)
	}
	if events[0].Detail != "" {
		t.Errorf("subject = %q, want empty rather than whitespace", events[0].Detail)
	}
}

func TestSubject_OneLineWithinTheBudget(t *testing.T) {
	if got := Subject("  fix the\nflaky test  "); got != "fix the flaky test" {
		t.Errorf("Subject = %q, want one trimmed line", got)
	}

	long := strings.Repeat("a", SubjectMax+40)
	got := Subject(long)
	if utf8.RuneCountInString(got) != SubjectMax+1 { // +1 for the ellipsis
		t.Errorf("Subject kept %d runes, want %d plus an ellipsis",
			utf8.RuneCountInString(got), SubjectMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("Subject = %q, want a visible truncation", got)
	}

	// Cut on runes, not bytes: halving a multi-byte character renders as a
	// replacement glyph in every surface that shows it. A 3-byte rune is the
	// fixture that settles it, since SubjectMax bytes of a 2-byte one lands on
	// a boundary by luck and a byte-wise cut would pass.
	multi := strings.Repeat("あ", SubjectMax+10)
	cut := Subject(multi)
	if !utf8.ValidString(cut) {
		t.Error("Subject produced invalid UTF-8 by cutting inside a character")
	}
	if utf8.RuneCountInString(cut) != SubjectMax+1 {
		t.Errorf("Subject kept %d runes of a multi-byte string, want %d plus an ellipsis",
			utf8.RuneCountInString(cut), SubjectMax)
	}
}
