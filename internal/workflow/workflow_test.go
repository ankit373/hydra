// SPDX-License-Identifier: MIT

package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ankit373/hydra/internal/testutil"
)

func saved(t *testing.T, id string, steps []Step) Workflow {
	t.Helper()
	w, err := New(id, "task", steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	testutil.NewSandbox(t)
	w := saved(t, "wf1", []Step{{Prompt: "a", Enum: "SIMPLE"}, {Prompt: "b"}})

	got, err := Load("wf1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Task != w.Task || len(got.Steps) != 2 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if got.Steps[0].N != 1 || got.Steps[1].N != 2 {
		t.Errorf("step numbers are not 1-based and ordered: %d, %d", got.Steps[0].N, got.Steps[1].N)
	}
	if got.Steps[0].Enum != "SIMPLE" {
		t.Errorf("the step's own routing hint was lost: %q", got.Steps[0].Enum)
	}
	if got.Steps[0].Status != Pending {
		t.Errorf("a fresh step is %q, want pending", got.Steps[0].Status)
	}
}

// A title is what the step list shows, so one is always present.
func TestNew_DerivesATitleFromThePrompt(t *testing.T) {
	w, err := New("wf1", "t", []Step{
		{Prompt: "check the dashboard\nthen do more"},
		{Prompt: strings.Repeat("x", 200)},
		{Prompt: "p", Title: "kept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.Steps[0].Title != "check the dashboard" {
		t.Errorf("title = %q, want the first line only", w.Steps[0].Title)
	}
	if len(w.Steps[1].Title) > 64 {
		t.Errorf("a 200-char prompt produced a %d-char title", len(w.Steps[1].Title))
	}
	if w.Steps[2].Title != "kept" {
		t.Errorf("an explicit title was overwritten: %q", w.Steps[2].Title)
	}
}

// Resuming a zero value would report a finished workflow that ran nothing,
// which is worse than refusing to resume at all.
func TestLoad_RefusesAMalformedOrIncompleteFile(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"truncated json", `{"id":"wf1","steps":[`, "unreadable"},
		{"no steps", `{"id":"wf1","steps":[]}`, "incomplete"},
		{"no id", `{"steps":[{"n":1,"prompt":"a"}]}`, "incomplete"},
		{"empty file", ``, "unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.NewSandbox(t)
			if err := os.MkdirAll(Dir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(Dir(), "wf1.json"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			w, err := Load("wf1")
			if err == nil {
				t.Fatalf("%s was accepted and would be resumed as %+v", tc.name, w)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not say what is wrong (%q): %v", tc.want, err)
			}
			// The message must name the file, or the user cannot go look.
			if !strings.Contains(err.Error(), "wf1.json") {
				t.Errorf("error does not name the file to inspect: %v", err)
			}
		})
	}
}

func TestLoad_MissingIsErrNotFound(t *testing.T) {
	testutil.NewSandbox(t)
	if _, err := Load("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The id lands in a filesystem path, so it is validated rather than trusted.
func TestPath_RefusesATraversingID(t *testing.T) {
	for _, id := range []string{"../escape", "a/b", "", strings.Repeat("x", 200), "with space", "wf$1"} {
		if _, err := Path(id); err == nil {
			t.Errorf("Path(%q) was accepted; it would resolve outside the store", id)
		}
	}
	if _, err := Path("wf-1.2_3"); err != nil {
		t.Errorf("a reasonable id was refused: %v", err)
	}
}

func TestList_NewestFirstAndSurvivesOneBadFile(t *testing.T) {
	testutil.NewSandbox(t)
	saved(t, "old", []Step{{Prompt: "a"}})
	saved(t, "new", []Step{{Prompt: "b"}})
	// A file Load refuses must not hide the others.
	if err := os.WriteFile(filepath.Join(Dir(), "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And a non-workflow file in the directory is ignored, not parsed.
	if err := os.WriteFile(filepath.Join(Dir(), "notes.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d workflows, want 2 (the broken one skipped, the txt ignored): %+v", len(list), list)
	}
	ids := []string{list[0].ID, list[1].ID}
	if ids[0] == "old" && ids[1] == "new" {
		t.Errorf("order is oldest first: %v", ids)
	}
}

func TestList_EmptyStoreIsNotAnError(t *testing.T) {
	testutil.NewSandbox(t)
	list, err := List()
	if err != nil {
		t.Fatalf("an unused store is an error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("got %d workflows in a fresh store", len(list))
	}
}

func TestDelete(t *testing.T) {
	testutil.NewSandbox(t)
	saved(t, "wf1", []Step{{Prompt: "a"}})
	if err := Delete("wf1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("wf1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("still loadable after delete: %v", err)
	}
	if err := Delete("wf1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice = %v, want ErrNotFound", err)
	}
}

// Saving progress on a running workflow must never be refused by its own entry
// against the cap.
func TestSave_ProgressOnAStoredWorkflowIsNeverRefused(t *testing.T) {
	testutil.NewSandbox(t)
	w := saved(t, "wf1", []Step{{Prompt: "a"}})
	w.Steps[0].Status = Done
	w.Steps[0].Output = "done"
	if err := Save(w); err != nil {
		t.Fatalf("re-saving a stored workflow failed: %v", err)
	}
	got, err := Load("wf1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Steps[0].Output != "done" {
		t.Errorf("progress was not persisted: %+v", got.Steps[0])
	}
	if got.Updated == "" {
		t.Error("Updated was not stamped")
	}
}

func TestSave_RefusesAWorkflowWithNoSteps(t *testing.T) {
	testutil.NewSandbox(t)
	if err := Save(Workflow{ID: "wf1"}); !errors.Is(err, ErrNoSteps) {
		t.Errorf("err = %v, want ErrNoSteps; it would persist and report success having run nothing", err)
	}
}

func TestNew_RefusesNoSteps(t *testing.T) {
	if _, err := New("wf1", "t", nil); !errors.Is(err, ErrNoSteps) {
		t.Errorf("err = %v, want ErrNoSteps", err)
	}
}

// The store is written with restrictive permissions: a workflow carries prompts
// and model output, which is the user's work.
func TestSave_FileIsNotWorldReadable(t *testing.T) {
	testutil.NewSandbox(t)
	saved(t, "wf1", []Step{{Prompt: "a"}})
	p, err := Path("wf1")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("mode = %v, want no group/other access", perm)
	}
}
