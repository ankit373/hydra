// SPDX-License-Identifier: MIT

package util

import (
	"bufio"
	"strings"
	"testing"
)

// TestSplitLines_MatchesScanLines pins the semantics to bufio.ScanLines for
// every input where a Scanner still works, so SplitLines is a safe drop-in.
// Above the Scanner's limit the two intentionally differ, that is the bug.
func TestSplitLines_MatchesScanLines(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"one line":         "a",
		"trailing newline": "a\n",
		"blank lines":      "a\n\nb\n",
		"crlf":             "a\r\nb\r\n",
		"lone cr kept":     "a\rb\n",
		"only newline":     "\n",
		"leading blank":    "\na\n",
		"no trailing nl":   "a\nb",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			sc := bufio.NewScanner(strings.NewReader(in))
			var want []string
			for sc.Scan() {
				want = append(want, sc.Text())
			}
			if err := sc.Err(); err != nil {
				t.Fatalf("scanner failed on a small input: %v", err)
			}
			got := SplitLines(in)
			if len(got) != len(want) {
				t.Fatalf("SplitLines(%q) = %q (%d lines), scanner gave %q (%d)", in, got, len(got), want, len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("line %d: got %q, scanner gave %q", i, got[i], want[i])
				}
			}
		})
	}
}

// TestSplitLines_BeyondScannerLimit is the regression for #168: a line past
// bufio.Scanner's 64 KiB ceiling must survive intact, where the Scanner returns
// nothing at all.
func TestSplitLines_BeyondScannerLimit(t *testing.T) {
	for _, n := range []int{65000, 70000, 1 << 20} {
		in := strings.Repeat("x", n) + "\n"

		sc := bufio.NewScanner(strings.NewReader(in))
		scanned := 0
		for sc.Scan() {
			scanned += len(sc.Text())
		}

		got := SplitLines(in)
		if len(got) != 1 {
			t.Fatalf("n=%d: got %d lines, want 1", n, len(got))
		}
		if len(got[0]) != n {
			t.Errorf("n=%d: SplitLines kept %d chars, want %d", n, len(got[0]), n)
		}
		// Document the failure this replaces: past the limit the Scanner both
		// returns nothing and reports an error that the old callers ignored.
		if n > 64<<10 && (scanned != 0 || sc.Err() == nil) {
			t.Errorf("n=%d: expected the scanner to fail with 0 bytes, got %d bytes err=%v", n, scanned, sc.Err())
		}
	}
}
