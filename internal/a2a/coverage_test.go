// SPDX-License-Identifier: MIT

package a2a

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ordering.String labels causal relationships in handoff output. "concurrent"
// is the one that matters, it is what tells a reader two agents may have
// raced, so an unlabelled value hides the exact case worth noticing.
func TestOrdering_StringCoversEveryValue(t *testing.T) {
	want := map[Ordering]string{
		Equal:      "equal",
		Before:     "before",
		After:      "after",
		Concurrent: "concurrent",
	}
	seen := map[string]bool{}
	for o, s := range want {
		got := o.String()
		if got != s {
			t.Errorf("Ordering(%d).String() = %q, want %q", int(o), got, s)
		}
		if seen[got] {
			t.Errorf("two orderings share the label %q", got)
		}
		seen[got] = true
	}
	if got := Ordering(99).String(); got != "concurrent" {
		t.Errorf("an unknown ordering rendered %q; it must fall back to the "+
			"conservative reading, not an empty string", got)
	}
}

// A missing handoff is "no handoff yet", not an error, the first dispatch of a
// session has none, and treating that as a failure would break every cold start.
func TestLoad_MissingFileIsNilNilNotAnError(t *testing.T) {
	h, err := Load(filepath.Join(t.TempDir(), "never-written.json"))
	if err != nil {
		t.Fatalf("a missing handoff errored: %v", err)
	}
	if h != nil {
		t.Errorf("a missing handoff returned %+v", h)
	}
}

// Inject's handoff context must reach the prompt when the file is valid, and a
// missing or corrupt handoff file must fail loudly (#450, #530) rather than
// letting a caller (dispatch, swarm, RunSPRT) silently proceed without it.
func TestInject(t *testing.T) {
	dir := t.TempDir()

	h := Handoff{From: "agent-1", Task: "earlier task", PriorOutput: "earlier output"}
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "handoff.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Inject(path, "new instruction")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agent-1", "earlier output", "new instruction", "ADDITIONAL INSTRUCTION"} {
		if !strings.Contains(got, want) {
			t.Errorf("injected prompt is missing %q:\n%s", want, got)
		}
	}

	// --a2a always names a file the caller explicitly asked for, so a missing
	// file must be an error, not silently treated as "no handoff".
	if _, err := Inject(filepath.Join(dir, "absent.json"), "unchanged"); err == nil {
		t.Error("a missing handoff file did not produce an error")
	}

	if err := os.WriteFile(path, []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inject(path, "unchanged"); err == nil {
		t.Error("a malformed handoff file did not produce an error")
	}
}

func TestLoad_MalformedJSONIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("malformed handoff JSON loaded without error")
	}
}

// A handoff carries the prior agent's context and file list. An unwritable
// destination must surface, not be swallowed, the next agent would otherwise
// run with no context and no indication why.
func TestSave_UnwritableDestinationIsAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope", "deeper")
	// Parent does not exist and is not creatable as a file path component.
	path := filepath.Join(dir, "x", "handoff.json")
	if err := os.WriteFile(filepath.Dir(dir), []byte("i am a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Handoff{From: "a", Task: "t"}).Save(path); err == nil {
		t.Error("saving under a path blocked by a regular file reported success")
	}
}

func TestSaveLoad_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff.json")
	want := &Handoff{
		From:    "agent-a",
		Task:    "add pagination",
		Files:   []string{"/a/b.go", "/a/c.go"},
		Context: "the repo uses cobra",
		Clock:   Clock{"agent-a": 3},
	}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("round trip lost the handoff entirely")
	}
	if got.From != want.From || got.Task != want.Task {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if len(got.Files) != 2 || got.Clock["agent-a"] != 3 {
		t.Errorf("files/clock lost in the round trip: %+v", got)
	}
}

// ConflictsWith is the whole point of the vector clock: two agents that ran
// concurrently AND touched the same file may have clobbered each other.
func TestConflictsWith_RequiresBothConcurrencyAndFileOverlap(t *testing.T) {
	base := &Handoff{From: "a", Files: []string{"/x.go"}, Clock: Clock{"a": 1}}

	cases := []struct {
		name string
		//nolint:govet // field order is for readability here
		other *Handoff
		want  bool
	}{
		{
			name:  "concurrent and overlapping",
			other: &Handoff{From: "b", Files: []string{"/x.go"}, Clock: Clock{"b": 1}},
			want:  true,
		},
		{
			name:  "concurrent but disjoint files",
			other: &Handoff{From: "b", Files: []string{"/y.go"}, Clock: Clock{"b": 1}},
			want:  false,
		},
		{
			name:  "causally after, same file",
			other: &Handoff{From: "b", Files: []string{"/x.go"}, Clock: Clock{"a": 1, "b": 1}},
			want:  false,
		},
		{
			name:  "no files at all",
			other: &Handoff{From: "b", Clock: Clock{"b": 1}},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := base.ConflictsWith(tc.other); got != tc.want {
				t.Errorf("ConflictsWith = %v, want %v", got, tc.want)
			}
		})
	}
}

// A nil counterpart must not panic, Load returns nil for a first run, and
// callers pass that straight through.
func TestConflictsWith_NilIsNotAConflict(t *testing.T) {
	h := &Handoff{From: "a", Files: []string{"/x.go"}, Clock: Clock{"a": 1}}
	if h.ConflictsWith(nil) {
		t.Error("a nil counterpart was reported as a conflict")
	}
}
