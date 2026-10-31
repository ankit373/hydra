// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// specstest is the diagnostic a user is pointed at when Hydra's local-model
// sizing does not match what they expect. It had no test because it is "just a
// debug main", but a diagnostic that panics or prints nothing is worse than no
// diagnostic, since it is consulted precisely when something is already wrong.
//
// This is a smoke test on purpose: it asserts the tool runs on whatever machine
// it is on and reports every field it claims to. The correctness of the numbers
// is internal/sysinfo's contract and is covered there.
func TestMain_ReportsEveryFieldItClaimsTo(t *testing.T) {
	out := captureStdout(t, main)

	if strings.TrimSpace(out) == "" {
		t.Fatal("specstest printed nothing; the diagnostic is useless")
	}

	// Each label the tool promises. A missing one means a field silently
	// stopped being reported.
	for _, label := range []string{
		"Summary:", "Note:", "Total RAM:", "Free RAM:", "Wired RAM:",
		"Effective:", "Pressure:", "Model recommendations:", "Best:",
	} {
		if !strings.Contains(out, label) {
			t.Errorf("the output omits %q:\n%s", label, out)
		}
	}

	// Every recommendation must carry a fit marker and a reason, a bare model
	// name tells the reader nothing about why it was or was not chosen.
	lines := strings.Split(out, "\n")
	var recs int
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "✓ ") || strings.HasPrefix(trimmed, "✗ ") {
			recs++
			if len(strings.Fields(trimmed)) < 3 {
				t.Errorf("a recommendation has no reason: %q", trimmed)
			}
		}
	}
	if recs == 0 {
		t.Errorf("no model recommendations were printed:\n%s", out)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		done <- b.String()
	}()

	func() {
		defer func() {
			os.Stdout = orig
			_ = w.Close()
		}()
		fn()
	}()
	return <-done
}
