// SPDX-License-Identifier: MIT

package workspace

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// matchGlob decides whether a path may be written to, and both of its inputs
// come from outside the code: the path from a model's output or an A2A handoff,
// the pattern from a user-edited workspace.yaml. Neither is a shape a table
// author gets to choose.
//
//	go test ./internal/workspace -fuzz=FuzzMatchGlob -fuzztime=60s

func globSeeds(f *testing.F) {
	for _, p := range [][2]string{
		{"src/main.go", "src/**"},
		{"a/b/c/d.go", "**"},
		{".env", "**/.env*"},
		{"a/node_modules/x", "**/node_modules/**"},
		{"", ""},
		{"/", "/"},
		{"a", "*"},
		{"a/b", "*/*"},
		{strings.Repeat("a/", 20) + "x", strings.Repeat("**/", 10) + "x"},
		{"x", "[[[["},
		{"x", `\`},
	} {
		f.Add(p[0], p[1])
	}
}

// It must never panic, and — the real risk — it must always terminate.
// matchSegments recurses over every split point for each "**", so a pattern
// with several of them against a long path is a candidate for exponential
// blow-up. A hang here is a denial of service on the scope check, which is the
// one thing standing between a model and the filesystem.
func FuzzMatchGlob_TerminatesAndNeverPanics(f *testing.F) {
	globSeeds(f)

	f.Fuzz(func(t *testing.T, rel, pattern string) {
		// Bound the inputs so the fuzzer explores structure rather than sheer
		// length; the pathological-pattern case is asserted explicitly below.
		if len(rel) > 512 || len(pattern) > 512 {
			t.Skip()
		}

		done := make(chan bool, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("matchGlob(%q, %q) panicked: %v", rel, pattern, r)
					done <- false
				}
			}()
			done <- matchGlob(rel, pattern)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("matchGlob(%q, %q) did not terminate in 5s — the scope check "+
				"can be hung by a pattern", rel, pattern)
		}
	})
}

// A concrete pathological case rather than hoping the fuzzer stumbles on it:
// many "**" segments against a long path. If matching is exponential this never
// returns, and `hyctl edit` hangs instead of refusing.
func TestMatchGlob_PathologicalPatternStillTerminates(t *testing.T) {
	rel := strings.Repeat("a/", 40) + "target.go"
	pattern := strings.Repeat("**/", 20) + "*.go"

	done := make(chan bool, 1)
	go func() { done <- matchGlob(rel, pattern) }()

	select {
	case got := <-done:
		if !got {
			t.Errorf("pattern %q should match %q", pattern, rel)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("matchGlob did not terminate for %d path segments against %d '**' "+
			"segments — matching is exponential and a workspace.yaml pattern can "+
			"hang every scope check", strings.Count(rel, "/")+1, strings.Count(pattern, "**"))
	}
}

// Containment is the security boundary. Whatever the inputs, a path that
// filepath.Rel says escapes must never be reported as contained.
func FuzzContains_NeverAcceptsAnEscape(f *testing.F) {
	for _, p := range [][2]string{
		{"/ws", "/ws/a.go"},
		{"/ws", "/ws/../etc/passwd"},
		{"/ws", "/ws-evil/a.go"},
		{"/ws", "/ws"},
		{"/", "/anything"},
		{"", ""},
		{"/ws", ""},
	} {
		f.Add(p[0], p[1])
	}

	f.Fuzz(func(t *testing.T, root, path string) {
		if len(root) > 256 || len(path) > 256 {
			t.Skip()
		}
		if !contains(root, path) {
			return
		}
		// If it says contained, Rel must agree and must not climb out.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("contains(%q, %q) = true but filepath.Rel errored: %v", root, path, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("contains(%q, %q) = true but the relative path %q escapes the root",
				root, path, rel)
		}
	})
}
