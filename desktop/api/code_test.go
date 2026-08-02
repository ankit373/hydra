// SPDX-License-Identifier: MIT

package api

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ankit373/hydra/internal/runlog"
)

// render collapses a diff to a compact string so a case reads as the diff it
// asserts rather than as a struct literal.
func render(d *Diff) string {
	var b strings.Builder
	for _, l := range d.Lines {
		b.WriteString(l.Op)
		b.WriteString(l.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func TestDiff_ModifyAddRemove(t *testing.T) {
	cases := []struct {
		name          string
		before, after string
		want          string
		added         int
		removed       int
	}{
		{
			name:   "pure addition at the end",
			before: "a\nb\n",
			after:  "a\nb\nc\n",
			want:   " a\n b\n+c\n",
			added:  1,
		},
		{
			name:    "pure removal from the middle",
			before:  "a\nb\nc\n",
			after:   "a\nc\n",
			want:    " a\n-b\n c\n",
			removed: 1,
		},
		{
			name:    "modification is a removal plus an addition",
			before:  "a\nb\nc\n",
			after:   "a\nB\nc\n",
			want:    " a\n-b\n+B\n c\n",
			added:   1,
			removed: 1,
		},
		{
			name:   "file creation — everything is new",
			before: "",
			after:  "one\ntwo\n",
			want:   "+one\n+two\n",
			added:  2,
		},
		{
			name:    "file emptied — everything goes",
			before:  "one\ntwo\n",
			after:   "",
			want:    "-one\n-two\n",
			removed: 2,
		},
		{
			name:   "identical content produces no changes",
			before: "same\n",
			after:  "same\n",
			want:   " same\n",
		},
		{
			name:   "insertion at the start keeps the tail as context",
			before: "b\nc\n",
			after:  "a\nb\nc\n",
			want:   "+a\n b\n c\n",
			added:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			if err := runlog.SaveEdit("run-d", "001", []byte(tc.before), []byte(tc.after)); err != nil {
				t.Fatal(err)
			}
			d, err := New().GetDiff("run-d", "001", "/tmp/f.go")
			if err != nil {
				t.Fatal(err)
			}
			if !d.Found {
				t.Fatal("Found = false for a stored snapshot")
			}
			if got := render(d); got != tc.want {
				t.Errorf("diff =\n%s\nwant\n%s", got, tc.want)
			}
			if d.Added != tc.added || d.Removed != tc.removed {
				t.Errorf("+%d/-%d, want +%d/-%d", d.Added, d.Removed, tc.added, tc.removed)
			}
		})
	}
}

// A file ending in "\n" has as many lines as newlines. Inventing a trailing
// empty line renders as a spurious change on every diff.
func TestDiff_TrailingNewlineIsNotAPhantomLine(t *testing.T) {
	sandbox(t)

	if err := runlog.SaveEdit("run-nl", "001", []byte("a\n"), []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	d, err := New().GetDiff("run-nl", "001", "f")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Lines) != 1 {
		t.Fatalf("%d lines for identical one-line files, want 1: %+v", len(d.Lines), d.Lines)
	}
	if d.Added != 0 || d.Removed != 0 {
		t.Errorf("+%d/-%d for identical content, want +0/-0", d.Added, d.Removed)
	}
}

// Line numbers are what let a reader find the change in the real file. An
// addition has no old line and a removal has no new one.
func TestDiff_LineNumbersTrackBothSides(t *testing.T) {
	sandbox(t)

	if err := runlog.SaveEdit("run-ln", "001", []byte("a\nb\nc\n"), []byte("a\nX\nc\n")); err != nil {
		t.Fatal(err)
	}
	d, err := New().GetDiff("run-ln", "001", "f")
	if err != nil {
		t.Fatal(err)
	}

	want := []struct {
		op       string
		old, new int
	}{
		{" ", 1, 1},
		{"-", 2, 0},
		{"+", 0, 2},
		{" ", 3, 3},
	}
	if len(d.Lines) != len(want) {
		t.Fatalf("%d lines, want %d", len(d.Lines), len(want))
	}
	for i, w := range want {
		l := d.Lines[i]
		if l.Op != w.op || l.OldLine != w.old || l.NewLine != w.new {
			t.Errorf("line %d = %s old=%d new=%d, want %s old=%d new=%d",
				i, l.Op, l.OldLine, l.NewLine, w.op, w.old, w.new)
		}
	}
}

// A pruned snapshot is not a crash and not a blank pane — the view says why.
func TestGetDiff_MissingSnapshotExplainsItself(t *testing.T) {
	sandbox(t)

	d, err := New().GetDiff("run-x", "404", "f")
	if err != nil {
		t.Fatalf("a missing snapshot must not error: %v", err)
	}
	if d.Found {
		t.Error("Found = true for a snapshot that is not there")
	}
	if d.Reason == "" {
		t.Error("no Reason given; the view would render a blank pane")
	}
	if d.Lines == nil {
		t.Error("Lines is nil; it marshals to null and the view iterates it")
	}
}

// An edit whose content was refused for size still happened. The row exists
// with an empty ref, and the diff explains rather than pretends.
func TestGetDiff_EmptyRefIsExplained(t *testing.T) {
	sandbox(t)

	d, err := New().GetDiff("run-x", "", "f")
	if err != nil {
		t.Fatal(err)
	}
	if d.Found || d.Reason == "" {
		t.Errorf("Found=%v Reason=%q; an unstored snapshot must be explained", d.Found, d.Reason)
	}
}

// GetEdits lists what a run changed, in the order it changed it.
func TestGetEdits_ListsAppliedEditsInOrder(t *testing.T) {
	sandbox(t)

	writeRun(t, "20260802T100000Z-edits",
		runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"},
		runlog.Event{Kind: runlog.KindEdit, Agent: "/src/a.go", Ref: "000001", Detail: "+12/-3"},
		runlog.Event{Kind: runlog.KindEdit, Agent: "/src/b.go", Ref: "000002", Detail: "+1/-0"},
	)

	edits, err := New().GetEdits("20260802T100000Z-edits")
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 {
		t.Fatalf("%d edits, want 2", len(edits))
	}
	if edits[0].File != "/src/a.go" || edits[1].File != "/src/b.go" {
		t.Errorf("order = %s, %s; want a.go then b.go", edits[0].File, edits[1].File)
	}
	if edits[0].Added != 12 || edits[0].Removed != 3 {
		t.Errorf("counts = +%d/-%d, want +12/-3", edits[0].Added, edits[0].Removed)
	}
}

// A run that changed nothing returns an empty list, never null — the view
// iterates it.
func TestGetEdits_NoEditsIsEmptyNotNull(t *testing.T) {
	sandbox(t)
	writeRun(t, "20260802T100000Z-noedit", runlog.Event{Kind: runlog.KindHeadSelected, Head: "h"})

	edits, err := New().GetEdits("20260802T100000Z-noedit")
	if err != nil {
		t.Fatal(err)
	}
	if edits == nil {
		t.Fatal("nil slice marshals to null")
	}
	if len(edits) != 0 {
		t.Errorf("%d edits, want 0", len(edits))
	}
}

// A detail the counts cannot be read from must yield zeros, not a guess.
func TestParseCounts_UnparseableDetailYieldsZero(t *testing.T) {
	for _, detail := range []string{"", "no counts here", "+/-", "+x/-y", "12/3"} {
		if a, r := parseCounts(detail); a != 0 || r != 0 {
			t.Errorf("parseCounts(%q) = +%d/-%d, want +0/-0", detail, a, r)
		}
	}
	if a, r := parseCounts("+12/-3 · snapshot unavailable"); a != 12 || r != 3 {
		t.Errorf("parseCounts with a suffix = +%d/-%d, want +12/-3", a, r)
	}
}

// The diff must survive content at the storage cap without truncating it.
func TestDiff_HandlesContentAtTheSizeCap(t *testing.T) {
	sandbox(t)

	// One line short of the cap, so the "after" side — which gains a line —
	// still fits. Sizing "before" exactly at the cap would push "after" over it
	// and test the refusal path instead of the diff.
	line := strings.Repeat("x", 63) + "\n"
	body := strings.Repeat(line, runlog.MaxSnapshotBytes/len(line)-1)
	if err := runlog.SaveEdit("run-big", "001", []byte(body), []byte(body+"new\n")); err != nil {
		t.Fatalf("content within the cap was refused: %v", err)
	}
	d, err := New().GetDiff("run-big", "001", "f")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Found {
		t.Fatal("Found = false")
	}
	if d.Added != 1 || d.Removed != 0 {
		t.Errorf("+%d/-%d, want +1/-0", d.Added, d.Removed)
	}
}

// Binary-ish content must not panic the differ or corrupt the round-trip.
func TestDiff_SurvivesNonUTF8Content(t *testing.T) {
	sandbox(t)

	before := []byte{0x00, 0xff, 0xfe, '\n'}
	after := append(bytes.Clone(before), 0x01, '\n')
	if err := runlog.SaveEdit("run-bin", "001", before, after); err != nil {
		t.Fatal(err)
	}
	d, err := New().GetDiff("run-bin", "001", "f")
	if err != nil {
		t.Fatalf("non-UTF8 content must not error: %v", err)
	}
	if !d.Found {
		t.Error("Found = false for stored binary content")
	}
}

// The quadratic core must never see a whole large file. Before the
// prefix/suffix trim, a 4 MiB snapshot meant a ~65k x 65k LCS table — 4.2
// billion cells, tens of gigabytes, an effective hang. This is the shape of a
// real edit: a big file with one line changed near the end.
func TestDiff_LargeFileWithASmallEditIsFast(t *testing.T) {
	sandbox(t)

	lines := make([]string, 60_000)
	for i := range lines {
		lines[i] = "line " + strconv.Itoa(i)
	}
	before := strings.Join(lines, "\n") + "\n"
	lines[59_000] = "CHANGED"
	after := strings.Join(lines, "\n") + "\n"

	if err := runlog.SaveEdit("run-perf", "001", []byte(before), []byte(after)); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	d, err := New().GetDiff("run-perf", "001", "f")
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if d.Added != 1 || d.Removed != 1 {
		t.Errorf("+%d/-%d, want +1/-1", d.Added, d.Removed)
	}
	// Generous: the point is to catch a return to quadratic-on-the-whole-file,
	// which does not finish at all, not to police milliseconds.
	if elapsed > 5*time.Second {
		t.Errorf("took %v for a one-line change in a 60k-line file — the prefix/suffix trim is gone", elapsed)
	}
}

// Two entirely different large files cannot be aligned line by line, and
// pretending otherwise is what hangs. It degrades to a wholesale replacement.
func TestDiff_WhollyDifferentLargeFilesDegradeGracefully(t *testing.T) {
	sandbox(t)

	a := make([]string, 5_000)
	b := make([]string, 5_000)
	for i := range a {
		a[i] = "alpha " + strconv.Itoa(i)
		b[i] = "beta " + strconv.Itoa(i)
	}
	if err := runlog.SaveEdit("run-repl", "001",
		[]byte(strings.Join(a, "\n")+"\n"), []byte(strings.Join(b, "\n")+"\n")); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	d, err := New().GetDiff("run-repl", "001", "f")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v; the LCS cell cap is not bounding the work", elapsed)
	}
	if d.Added != 5_000 || d.Removed != 5_000 {
		t.Errorf("+%d/-%d, want +5000/-5000 (a wholesale replacement)", d.Added, d.Removed)
	}
}

// Trimming a shared tail must not emit a line twice when head and tail regions
// would otherwise overlap — the classic off-by-one in this optimisation.
func TestDiff_HeadAndTailTrimsDoNotOverlap(t *testing.T) {
	sandbox(t)

	// "a" is both a valid prefix match and a valid suffix match.
	if err := runlog.SaveEdit("run-ov", "001", []byte("a\n"), []byte("a\na\n")); err != nil {
		t.Fatal(err)
	}
	d, err := New().GetDiff("run-ov", "001", "f")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := render(d), " a\n+a\n"; got != want {
		t.Errorf("diff =\n%q\nwant\n%q", got, want)
	}
	if d.Added != 1 || d.Removed != 0 {
		t.Errorf("+%d/-%d, want +1/-0", d.Added, d.Removed)
	}
}
