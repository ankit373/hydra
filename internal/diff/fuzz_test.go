// SPDX-License-Identifier: MIT

package diff

import (
	"fmt"
	"strings"
	"testing"
)

// Fuzzing, rather than more table cases, because the input here is a model's
// output and a user's source — neither of which the table author gets to choose.
// The table tests assert that the diff is *right* for shapes we thought of;
// these assert it never crashes, hangs, or lies for shapes we did not.
//
// Run the search with:
//	go test ./internal/diff -fuzz=FuzzUnified -fuzztime=60s
//
// Without -fuzz these execute the seed corpus as ordinary tests, so they still
// guard in CI.

func fuzzSeeds(f *testing.F) {
	pairs := [][2]string{
		{"", ""},
		{"a\n", ""},
		{"", "a\n"},
		{"a\nb\nc\n", "a\nB\nc\n"},
		{"a\r\nb\r\n", "a\nb\n"},
		{"no trailing newline", "no trailing newline!"},
		{strings.Repeat("x\n", 50), strings.Repeat("y\n", 50)},
		{"a\na\na\n", "a\na\n"},
		{"\x00\x01\x02", "\x00\x01\x03"},
		{"line with \t tab\n", "line with  spaces\n"},
	}
	for _, p := range pairs {
		f.Add(p[0], p[1])
	}
}

// The property that matters: applying our own diff to old must reproduce new,
// for *any* pair of inputs. A diff that is merely well-formed is not enough —
// it is written over the user's file.
func FuzzUnified_RoundTrips(f *testing.F) {
	fuzzSeeds(f)

	f.Fuzz(func(t *testing.T, oldS, newS string) {
		// Keep the search focused on structure rather than on one enormous
		// string; the oversized path has its own explicit test.
		if len(oldS) > 4096 || len(newS) > 4096 {
			t.Skip()
		}

		u := Unified("old", "new", []byte(oldS), []byte(newS))

		wantLines := splitLines(newS)
		if u == "" {
			// Empty means identical — so it had better be identical.
			if !sameLines(splitLines(oldS), wantLines) {
				t.Fatalf("empty diff for differing inputs\n old=%q\n new=%q", oldS, newS)
			}
			return
		}

		got, err := applyUnified(splitLines(oldS), u)
		if err != nil {
			t.Fatalf("our own diff does not apply: %v\n old=%q\n new=%q\n diff:\n%s", err, oldS, newS, u)
		}
		if !sameLines(got, wantLines) {
			t.Fatalf("round trip lost information\n old=%q\n new=%q\n got=%q\n diff:\n%s",
				oldS, newS, strings.Join(got, "\n"), u)
		}
	})
}

// Stats is what `hyctl review` reports. It must agree with the diff body for
// every input, or the summary and the diff tell the user different stories.
func FuzzStats_AgreesWithUnified(f *testing.F) {
	fuzzSeeds(f)

	f.Fuzz(func(t *testing.T, oldS, newS string) {
		if len(oldS) > 4096 || len(newS) > 4096 {
			t.Skip()
		}
		added, removed := Stats([]byte(oldS), []byte(newS))
		if added < 0 || removed < 0 {
			t.Fatalf("negative counts: +%d/-%d", added, removed)
		}

		u := Unified("old", "new", []byte(oldS), []byte(newS))
		uAdd, uRem := countDiffLines(u)
		if added != uAdd || removed != uRem {
			t.Fatalf("Stats = +%d/-%d but the diff body has +%d/-%d\n old=%q\n new=%q",
				added, removed, uAdd, uRem, oldS, newS)
		}
		// Identical inputs must report no change, and differing inputs must
		// report some — a silent 0/0 for a modified file is #260's symptom.
		same := sameLines(splitLines(oldS), splitLines(newS))
		if same && (added != 0 || removed != 0) {
			t.Fatalf("identical inputs reported +%d/-%d", added, removed)
		}
		if !same && added == 0 && removed == 0 {
			t.Fatalf("differing inputs reported 0/0\n old=%q\n new=%q", oldS, newS)
		}
	})
}

// A hunk header must describe the hunk that follows it. A mismatch is how a
// diff renders plausibly and applies wrongly.
func FuzzUnified_HunkHeadersMatchTheirBodies(f *testing.F) {
	fuzzSeeds(f)

	f.Fuzz(func(t *testing.T, oldS, newS string) {
		if len(oldS) > 4096 || len(newS) > 4096 {
			t.Skip()
		}
		u := Unified("old", "new", []byte(oldS), []byte(newS))
		if u == "" {
			return
		}

		var oldLines, newLines, wantOld, wantNew int
		inHunk := false
		check := func() {
			if inHunk && (oldLines != wantOld || newLines != wantNew) {
				t.Fatalf("hunk header claimed -%d +%d but the body has -%d +%d\n%s",
					wantOld, wantNew, oldLines, newLines, u)
			}
		}
		for i, l := range strings.Split(strings.TrimRight(u, "\n"), "\n") {
			if i < 2 {
				continue // the two header lines
			}
			switch {
			case strings.HasPrefix(l, "@@"):
				check()
				inHunk = true
				oldLines, newLines = 0, 0
				if _, err := fmtSscanHunk(l, &wantOld, &wantNew); err != nil {
					t.Fatalf("unparsable hunk header %q in\n%s", l, u)
				}
			case strings.HasPrefix(l, " "):
				oldLines++
				newLines++
			case strings.HasPrefix(l, "-"):
				oldLines++
			case strings.HasPrefix(l, "+"):
				newLines++
			}
		}
		check()
	})
}

// fmtSscanHunk pulls the two line counts out of "@@ -a,b +c,d @@".
func fmtSscanHunk(header string, wantOld, wantNew *int) (int, error) {
	var a, c int
	return fmt.Sscanf(header, "@@ -%d,%d +%d,%d @@", &a, wantOld, &c, wantNew)
}

// sameLines compares line slices elementwise.
//
// Not strings.Join: an empty file yields nil and a file holding one blank line
// yields [""], and joining collapses both to "". Those are different files, and
// Stats correctly reports +1 for one becoming the other — the first version of
// this check called them equal and failed the fuzzer on correct behaviour.
// Found by FuzzStats_AgreesWithUnified with old="", new="\n".
func sameLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
