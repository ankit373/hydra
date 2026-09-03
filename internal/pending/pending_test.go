// SPDX-License-Identifier: MIT

package pending

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func homed(t *testing.T) {
	t.Helper()
	t.Setenv("HYDRA_HOME", t.TempDir())
}

func q(taskID string) Question {
	return Question{TaskID: taskID, Question: "Allow X?", Prompt: "do the thing", Head: "h1"}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	homed(t)
	in := q("t1")
	in.Resource = "internal/auth/token.go"
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load("t1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Prompt != in.Prompt || got.Head != in.Head || got.Resource != in.Resource {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if got.AskedAt.IsZero() {
		t.Error("AskedAt should be stamped when zero")
	}
}

// The whole point of the store: a resume must never proceed on a zero value,
// because dispatching with no answer folded in turns an unreadable question
// into a silent approval.
func TestLoadFailsLoudlyOnCorruptFile(t *testing.T) {
	homed(t)
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("bad"); err == nil {
		t.Fatal("expected an error for a malformed pending file")
	}
}

// A file that parses but is missing what a resume needs is just as dangerous
// as one that does not parse at all.
func TestLoadRejectsIncompleteFile(t *testing.T) {
	homed(t)
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "thin.json"), []byte(`{"taskId":"thin"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load("thin")
	if err == nil {
		t.Fatal("expected an error when prompt is missing")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Errorf("error should say what is wrong, got %v", err)
	}
}

func TestLoadMissingIsNotFound(t *testing.T) {
	homed(t)
	if _, err := Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// Task IDs arrive from an option, an env var and a UI field, so a traversal
// attempt must be rejected rather than written outside the pending dir.
func TestPathRejectsTraversal(t *testing.T) {
	homed(t)
	for _, bad := range []string{"../escape", "a/b", "", ".", "..", "x\x00y", strings.Repeat("a", 129)} {
		if _, err := Path(bad); err == nil {
			t.Errorf("Path(%q) should be rejected", bad)
		}
		if err := Save(q(bad)); err == nil {
			t.Errorf("Save with task id %q should be rejected", bad)
		}
	}
}

func TestDeleteMakesResumeIdempotent(t *testing.T) {
	homed(t)
	if err := Save(q("t1")); err != nil {
		t.Fatal(err)
	}
	if err := Delete("t1"); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	// The second answer for the same task must find nothing to resume.
	if err := Delete("t1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete should be ErrNotFound, got %v", err)
	}
}

func TestListIsOldestFirst(t *testing.T) {
	homed(t)
	now := time.Now().UTC()
	// Saved newest-first so the ordering under test cannot come from save order.
	ages := map[string]time.Duration{"newer": 0, "middle": -time.Hour, "older": -2 * time.Hour}
	for _, id := range []string{"newer", "middle", "older"} {
		x := q(id)
		x.AskedAt = now.Add(ages[id])
		if err := Save(x); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"older", "middle", "newer"}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	for i, w := range want {
		if got[i].TaskID != w {
			t.Errorf("position %d: want %s, got %s", i, w, got[i].TaskID)
		}
	}
}

// One corrupt file must not hide every readable question, or a parked task is
// forgotten because an unrelated one is broken.
func TestListReportsCorruptButStillReturnsTheRest(t *testing.T) {
	homed(t)
	if err := Save(q("good")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := List()
	if err == nil {
		t.Error("List should report the unreadable file")
	}
	if len(got) != 1 || got[0].TaskID != "good" {
		t.Errorf("readable question should still be returned, got %+v", got)
	}
}

func TestListEmptyWhenDirMissing(t *testing.T) {
	homed(t)
	got, err := List()
	if err != nil || got != nil {
		t.Errorf("want (nil, nil) with no pending dir, got (%v, %v)", got, err)
	}
}

// The bound refuses new work rather than pruning: discarding an old question
// would silently drop something someone is waiting on.
func TestSaveRefusesBeyondTheBound(t *testing.T) {
	homed(t)
	for i := 0; i < MaxPending; i++ {
		if err := Save(q(fmt.Sprintf("t%03d", i))); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	err := Save(q("one-too-many"))
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	// Re-parking a task that already has an entry is not new work, so the
	// bound must not refuse it.
	if err := Save(q("t000")); err != nil {
		t.Errorf("re-parking an existing task should be allowed, got %v", err)
	}
}

func TestSaveLeavesNoTempFilesBehind(t *testing.T) {
	homed(t)
	if err := Save(q("t1")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
