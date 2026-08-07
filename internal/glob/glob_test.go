// SPDX-License-Identifier: MIT

package glob

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Example tests are a type this repo did not have. They are compiled, executed
// and their output verified, and they render in godoc — so the documented
// behaviour of a security-relevant matcher cannot rot away from the real one.

func ExampleMatch() {
	fmt.Println(Match("src/**", "src/a/b/main.go"))
	fmt.Println(Match("*.go", "main.go"))
	fmt.Println(Match("*.go", "sub/main.go")) // * never crosses a separator
	fmt.Println(Match("**/.env*", "a/b/.env.local"))
	// Output:
	// true
	// true
	// false
	// true
}

func ExampleMatch_denyRules() {
	// The deny set shipped in registry/workspace.yaml, applied to real paths.
	for _, p := range []string{
		"app/.env.production",
		"config/secrets/db.yml",
		"src/main.go",
	} {
		denied := Match("**/.env*", p) || Match("**/secrets/**", p)
		fmt.Printf("%-28s denied=%v\n", p, denied)
	}
	// Output:
	// app/.env.production          denied=true
	// config/secrets/db.yml        denied=true
	// src/main.go                  denied=false
}

func TestMatch_Semantics(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		// ** spans zero or more segments.
		{"**", "anything/at/all", true},
		{"src/**", "src", true},
		{"src/**", "src/main.go", true},
		{"src/**", "src/a/b/c.go", true},
		{"src/**", "vendor/x.go", false},
		{"**/node_modules/**", "a/b/node_modules/pkg/index.js", true},
		{"**/node_modules/**", "node_modules/pkg", true},

		// * stays inside one segment.
		{"*.go", "main.go", true},
		{"*.go", "a/main.go", false},
		{"*/*.go", "a/main.go", true},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},

		// The "no constraint" forms both callers rely on.
		{"", "anything", true},
		{"*", "anything/deep", true},

		// Exact, and near-misses that must not match.
		{"/repo/a.go", "/repo/a.go", true},
		{"/repo/*", "/repo/a.go", true},
		{"/repo/*", "/repo/sub/a.go", false},
		{"/repo/**", "/repo/sub/a.go", true},
		{"/repo", "/repo-evil", false},

		// A leading ./ on the subject is normalised away.
		{"src/**", "./src/main.go", true},
	}
	for _, tc := range cases {
		if got := Match(tc.pattern, tc.path); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// The dialect must not change by host. filepath.Match's separator is "\" on
// Windows, which is exactly how the ledger's rules came to mean one thing on
// Unix and another on Windows — a gate that widens on one platform.
func TestMatch_IsIdenticalOnEveryPlatform(t *testing.T) {
	// These are the cases where filepath.Match disagreed with itself across
	// platforms. Asserting absolute answers here means the Windows leg of CI
	// fails if the dialect ever becomes host-dependent again.
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/repo/*", "/repo/sub/a.go", false},
		{"/repo/*", "/repo/a.go", true},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/x/c", false},
	}
	for _, tc := range cases {
		if got := Match(tc.pattern, tc.path); got != tc.want {
			t.Errorf("on %s: Match(%q, %q) = %v, want %v — the dialect has become "+
				"host-dependent", runtime.GOOS, tc.pattern, tc.path, got, tc.want)
		}
	}

	// Demonstrate the divergence this package exists to remove: on Windows,
	// filepath.Match lets * cross "/" because "/" is not its separator.
	if runtime.GOOS == "windows" {
		if ok, _ := filepath.Match("/repo/*", "/repo/sub/a.go"); !ok {
			t.Log("filepath.Match no longer crosses / on Windows; this package is " +
				"still required for **, but the note above can be revisited")
		}
	}
}

// Inherited from #303: many "**" against a long path must not be exponential.
func TestMatch_PathologicalPatternTerminates(t *testing.T) {
	p := strings.Repeat("a/", 40) + "x"
	pattern := strings.Repeat("**/", 20) + "no-such-segment"

	done := make(chan bool, 1)
	go func() { done <- Match(pattern, p) }()

	select {
	case got := <-done:
		if got {
			t.Error("a pattern ending in an unmatched segment reported a match")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Match did not terminate — the memoization is gone and any config " +
			"pattern can hang the gate that uses it")
	}
}

// Allocation guards are another type this repo lacked. Match runs on every
// scope check and every ledger decision, so an accidental allocation per
// segment is a real cost — and a sudden jump means someone changed the
// algorithm without noticing.
func TestMatch_AllocationsStayBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation counts are noisy under -short")
	}
	cases := []struct {
		name, pattern, path string
		max                 float64
	}{
		{"trivial no-constraint", "", "a/b/c.go", 0},
		{"single segment", "*.go", "main.go", 8},
		{"typical deny rule", "**/.env*", "a/b/.env.local", 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := testing.AllocsPerRun(200, func() { Match(tc.pattern, tc.path) })
			if got > tc.max {
				t.Errorf("Match allocated %.0f times, budget %.0f — matching runs on "+
					"every scope check and every ledger decision", got, tc.max)
			}
		})
	}
}

func BenchmarkMatch_Typical(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Match("internal/**", "internal/executor/http.go")
	}
}

func BenchmarkMatch_DenyRule(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Match("**/node_modules/**", "a/b/c/node_modules/pkg/index.js")
	}
}

// Fuzzing carries over from #303: both inputs come from config and from model
// output, so neither is a shape a table author chooses.
func FuzzMatch_TerminatesAndNeverPanics(f *testing.F) {
	for _, p := range [][2]string{
		{"src/**", "src/main.go"},
		{"**", ""},
		{"", ""},
		{"[", "x"},
		{`\`, "x"},
		{strings.Repeat("**/", 10) + "x", strings.Repeat("a/", 20) + "x"},
	} {
		f.Add(p[0], p[1])
	}

	f.Fuzz(func(t *testing.T, pattern, p string) {
		if len(pattern) > 512 || len(p) > 512 {
			t.Skip()
		}
		done := make(chan bool, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Match(%q, %q) panicked: %v", pattern, p, r)
					done <- false
				}
			}()
			done <- Match(pattern, p)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("Match(%q, %q) did not terminate", pattern, p)
		}
	})
}
