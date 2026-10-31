// SPDX-License-Identifier: MIT

package diff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The bug this package replaces: an empty result must mean "identical", and it
// must be reachable only when the inputs really are identical. The old
// shell-out returned "" whenever diff(1) was missing, which made "no changes"
// and "could not run" the same value.
func TestUnified_IdenticalInputProducesNothing(t *testing.T) {
	src := []byte("a\nb\nc\n")
	if got := Unified("a.go", "a.go", src, src); got != "" {
		t.Errorf("identical input produced a diff:\n%s", got)
	}
	if a, r := Stats(src, src); a != 0 || r != 0 {
		t.Errorf("Stats = +%d/-%d, want 0/0", a, r)
	}
}

func TestUnified_ChangeProducesADiff(t *testing.T) {
	old := []byte("package main\n\nfunc main() {}\n")
	new := []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n")

	got := Unified("main.go", "main.go", old, new)
	if got == "" {
		t.Fatal("a real change produced no diff, this is the #260 failure mode")
	}
	for _, want := range []string{"--- main.go", "+++ main.go", "@@", "+import"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff missing %q:\n%s", want, got)
		}
	}
}

// The property that matters: a diff is correct iff applying it to old yields
// new. Counting lines proves nothing about whether the hunks are right.
func TestUnified_RoundTripsToTheTarget(t *testing.T) {
	cases := []struct{ name, old, new string }{
		{"insert at start", "b\nc\n", "a\nb\nc\n"},
		{"insert at end", "a\nb\n", "a\nb\nc\n"},
		{"delete from middle", "a\nb\nc\n", "a\nc\n"},
		{"modify one line", "a\nb\nc\n", "a\nB\nc\n"},
		{"replace everything", "a\nb\nc\n", "x\ny\nz\n"},
		{"empty to content", "", "a\nb\n"},
		{"content to empty", "a\nb\n", ""},
		{"two distant hunks", lines(1, 30), lines(1, 30, 3, 27)},
		{"adjacent hunks merge", "a\nb\nc\nd\ne\n", "a\nX\nc\nY\ne\n"},
		{"no trailing newline", "a\nb", "a\nc"},
		{"duplicate lines", "a\na\na\n", "a\na\n"},
		{"crlf vs lf is not a change", "a\r\nb\r\n", "a\nb\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := Unified("old", "new", []byte(tc.old), []byte(tc.new))
			got, err := applyUnified(splitLines(tc.old), u)
			if err != nil {
				t.Fatalf("applying our own diff failed: %v\n%s", err, u)
			}
			want := splitLines(tc.new)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("round trip lost information\n old:  %q\n new:  %q\n got:  %q\n diff:\n%s",
					tc.old, tc.new, strings.Join(got, "\n"), u)
			}
		})
	}
}

// Stats and Unified are derived from one edit script, so they cannot disagree.
// The three previous implementations each re-parsed diff(1)'s text separately
// and could.
func TestStats_AgreesWithUnified(t *testing.T) {
	old := []byte(lines(1, 20))
	new := []byte(lines(1, 20, 5, 15))

	added, removed := Stats(old, new)
	u := Unified("old", "new", old, new)

	uAdded, uRemoved := countDiffLines(u)
	if added != uAdded || removed != uRemoved {
		t.Errorf("Stats = +%d/-%d but the diff body has +%d/-%d", added, removed, uAdded, uRemoved)
	}
	if added == 0 && removed == 0 {
		t.Error("a changed file reported 0/0, the exact symptom #260 produced on Windows")
	}
}

// Where diff(1) exists, our line counts must match it. This is the check that
// our replacement is faithful rather than merely self-consistent.
func TestUnified_MatchesSystemDiffWhereAvailable(t *testing.T) {
	if _, err := exec.LookPath("diff"); err != nil {
		t.Skipf("no diff(1) on this platform (%v), the very reason this package exists", err)
	}

	cases := []struct{ old, new string }{
		{"a\nb\nc\n", "a\nB\nc\n"},
		{lines(1, 40), lines(1, 40, 10, 30)},
		{"a\nb\nc\nd\ne\nf\n", "a\nc\ne\n"},
		{"x\n", "x\ny\nz\n"},
	}
	dir := t.TempDir()
	for i, tc := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			oldPath := filepath.Join(dir, fmt.Sprintf("old%d", i))
			newPath := filepath.Join(dir, fmt.Sprintf("new%d", i))
			if err := os.WriteFile(oldPath, []byte(tc.old), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(newPath, []byte(tc.new), 0o600); err != nil {
				t.Fatal(err)
			}
			// diff(1) exits 1 when files differ; that is not an error.
			out, _ := exec.Command("diff", "-u", oldPath, newPath).CombinedOutput()

			sysAdded, sysRemoved := countDiffLines(string(out))
			added, removed := Stats([]byte(tc.old), []byte(tc.new))
			if added != sysAdded || removed != sysRemoved {
				t.Errorf("Stats = +%d/-%d, diff(1) = +%d/-%d\nsystem output:\n%s",
					added, removed, sysAdded, sysRemoved, out)
			}
		})
	}
}

// A file too large for the LCS table must still produce a correct diff, just a
// non-minimal one, never a silently empty result.
func TestUnified_OversizedInputStillDiffs(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 2600; i++ { // 2600² > maxCells
		fmt.Fprintf(&a, "line %d\n", i)
		fmt.Fprintf(&b, "LINE %d\n", i)
	}
	u := Unified("a", "b", []byte(a.String()), []byte(b.String()))
	if u == "" {
		t.Fatal("oversized input produced no diff at all")
	}
	added, removed := Stats([]byte(a.String()), []byte(b.String()))
	if added == 0 || removed == 0 {
		t.Errorf("oversized input reported +%d/-%d", added, removed)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// lines builds "1\n2\n…n\n", optionally replacing the half-open range [from,to)
// with uppercase markers so the two versions differ in a known place.
func lines(start, end int, replace ...int) string {
	var b strings.Builder
	for i := start; i <= end; i++ {
		if len(replace) == 2 && i >= replace[0] && i < replace[1] {
			fmt.Fprintf(&b, "CHANGED %d\n", i)
			continue
		}
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.String()
}

// applyUnified applies a unified diff to old, so a test can prove the diff
// actually describes the transformation rather than merely looking plausible.
func applyUnified(old []string, u string) ([]string, error) {
	if u == "" {
		return old, nil
	}
	var out []string
	oldIdx := 0 // 0-based cursor into old

	// The "--- old" / "+++ new" lines are the file header and appear exactly
	// once, before the first hunk. They are NOT recognised by prefix: a content
	// line beginning "++" renders as "+++" and is indistinguishable from the
	// header. Real diff(1) emits exactly that, and the first version of this
	// helper dropped such lines as headers and silently lost content, found by
	// FuzzUnified_RoundTrips with old="0", new="++".
	for i, l := range strings.Split(strings.TrimRight(u, "\n"), "\n") {
		if i < 2 {
			continue // the two header lines
		}
		switch {
		case strings.HasPrefix(l, "@@"):
			// "@@ -oldStart,oldLines +newStart,newLines @@"
			var oldStart, oldLines, newStart, newLines int
			if _, err := fmt.Sscanf(l, "@@ -%d,%d +%d,%d @@", &oldStart, &oldLines, &newStart, &newLines); err != nil {
				return nil, fmt.Errorf("unparsable hunk header %q: %w", l, err)
			}
			target := oldStart - 1
			if oldLines == 0 {
				target = oldStart // pure insert: start 0 means "before line 1"
			}
			if target < oldIdx {
				return nil, fmt.Errorf("hunk header %q goes backwards (cursor at %d)", l, oldIdx)
			}
			if target > len(old) {
				return nil, fmt.Errorf("hunk header %q starts past end of file (%d lines)", l, len(old))
			}
			out = append(out, old[oldIdx:target]...)
			oldIdx = target
		case strings.HasPrefix(l, " "):
			if oldIdx >= len(old) {
				return nil, fmt.Errorf("context line %q past end of old file", l)
			}
			if old[oldIdx] != l[1:] {
				return nil, fmt.Errorf("context mismatch at old line %d: have %q, diff says %q",
					oldIdx+1, old[oldIdx], l[1:])
			}
			out = append(out, old[oldIdx])
			oldIdx++
		case strings.HasPrefix(l, "-"):
			if oldIdx >= len(old) {
				return nil, fmt.Errorf("delete %q past end of old file", l)
			}
			if old[oldIdx] != l[1:] {
				return nil, fmt.Errorf("delete mismatch at old line %d: have %q, diff says %q",
					oldIdx+1, old[oldIdx], l[1:])
			}
			oldIdx++
		case strings.HasPrefix(l, "+"):
			out = append(out, l[1:])
		}
	}
	return append(out, old[oldIdx:]...), nil
}

// countDiffLines counts added/removed lines in a unified diff body.
//
// Only the first two lines are the file header. Recognising "---"/"+++" by
// prefix anywhere is wrong: a content line beginning "++" renders as "+++" and
// would be skipped as a header, undercounting the change. diff(1) emits exactly
// that shape.
func countDiffLines(u string) (added, removed int) {
	for i, l := range strings.Split(u, "\n") {
		if i < 2 {
			continue
		}
		switch {
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return added, removed
}
