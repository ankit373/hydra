// SPDX-License-Identifier: MIT

package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ankit373/hydra/internal/a2a"
	"github.com/ankit373/hydra/internal/config"
	"github.com/ankit373/hydra/internal/testutil"
)

// a2a.ConflictsWith needs two things: concurrent clocks and an overlapping
// file. The clocks were maintained correctly and the file list was never
// written, so the concurrent-edit detector could not return true for any
// handoff dispatch produced, whatever the code said (#425). `hyctl security`
// reported the control inert for exactly this reason.

func loadHandoff(t *testing.T) *a2a.Handoff {
	t.Helper()
	h, err := a2a.Load(filepath.Join(config.Dir(), "logs", "last_handoff.json"))
	if err != nil || h == nil {
		t.Fatalf("no handoff was written: %v", err)
	}
	return h
}

// The resource a dispatch acts on is what `hyctl edit` sets, and it is the
// file most likely to collide.
func TestDispatch_HandoffRecordsTheResourceItActedOn(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := liveDispatcher(echoHead(t, s, "h1", 90))

	if _, err := d.Dispatch(context.Background(), "rewrite it",
		Options{Resource: "internal/auth/token.go"}); err != nil {
		t.Fatal(err)
	}

	h := loadHandoff(t)
	if len(h.Files) != 1 || h.Files[0] != "internal/auth/token.go" {
		t.Errorf("handoff Files = %v, want the resource; with an empty list "+
			"ConflictsWith can never fire", h.Files)
	}
}

// A dispatch about nothing in particular must not gain a file list, or a
// handoff carrying [""] would make every pair of them look like a conflict.
func TestDispatch_HandoffHasNoFilesWhenNoneWereNamed(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := liveDispatcher(echoHead(t, s, "h1", 90))

	if _, err := d.Dispatch(context.Background(), "explain generics", Options{}); err != nil {
		t.Fatal(err)
	}

	if got := loadHandoff(t).Files; len(got) != 0 {
		t.Errorf("handoff Files = %v for a dispatch that named no file", got)
	}
}

func TestHandoffFiles_IsTheResourceOrNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
		want []string
	}{
		{"nothing", Options{}, nil},
		{"the resource it acts on", Options{Resource: "a.go"}, []string{"a.go"}},
		// A whitespace-only resource is not a file, and a handoff carrying [""]
		// would make every pair of them overlap.
		{"blank is not a file", Options{Resource: "   "}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := handoffFiles(tc.opts)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// The acceptance criterion the issue is actually about: two concurrent
// handoffs that share a file must be reported as a conflict. This is what was
// unreachable, and it is asserted against handoffs dispatch itself wrote, not
// hand-built ones, since hand-built ones passed all along.
func TestDispatch_TwoConcurrentDispatchesOnOneFileConflict(t *testing.T) {
	s := testutil.NewSandbox(t)
	d := liveDispatcher(echoHead(t, s, "h1", 90))
	path := filepath.Join(config.Dir(), "logs", "last_handoff.json")

	// Two agents starting from the same (empty) history, each editing the same
	// file: concurrent by construction, which is the collision to catch.
	if _, err := d.Dispatch(context.Background(), "agent one edits it",
		Options{Resource: "internal/auth/token.go"}); err != nil {
		t.Fatal(err)
	}
	first, err := a2a.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// Remove the persisted handoff so the second dispatch does not inherit the
	// first's clock, which is what makes the two concurrent rather than ordered.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	d2 := liveDispatcher(echoHead(t, s, "h2", 90))
	if _, err := d2.Dispatch(context.Background(), "agent two edits it",
		Options{Resource: "internal/auth/token.go"}); err != nil {
		t.Fatal(err)
	}
	second, err := a2a.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if got := first.Clock.Compare(second.Clock); got != a2a.Concurrent {
		t.Fatalf("the two handoffs compare as %v, not Concurrent; this test is not "+
			"exercising the case it claims", got)
	}
	if !first.ConflictsWith(second) {
		t.Errorf("two concurrent handoffs on %q are not reported as conflicting; "+
			"files were %v and %v", "internal/auth/token.go", first.Files, second.Files)
	}
	// And two concurrent handoffs on *different* files must not conflict, or
	// the detector is just reporting concurrency.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := d2.Dispatch(context.Background(), "agent two edits something else",
		Options{Resource: "internal/other/thing.go"}); err != nil {
		t.Fatal(err)
	}
	elsewhere, err := a2a.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.ConflictsWith(elsewhere) {
		t.Errorf("handoffs on %v and %v are reported as conflicting", first.Files, elsewhere.Files)
	}
}
