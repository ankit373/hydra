// SPDX-License-Identifier: MIT

package swarm

import (
	"strings"
	"testing"
)

func TestParseYesNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"YES", true},
		{"yes", true},
		{"Yes, they are equivalent.", true},
		{"NO", false},
		{"no, different behavior", false},
		{"  yes\n", true},
		{"They are equivalent, yes", true}, // first decisive token wins
		{"maybe, but leaning yes", true},   // skips non-decisive words
		{"", false},                        // empty → conservative NO
		{"I cannot determine", false},      // ambiguous → conservative NO
		{"nope", false},                    // "no" prefix
	}
	for _, c := range cases {
		if got := parseYesNo(c.in); got != c.want {
			t.Errorf("parseYesNo(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBuildEquivalencePrompt(t *testing.T) {
	p := buildEquivalencePrompt("write add", "return a+b", "return b+a")
	for _, want := range []string{"write add", "return a+b", "return b+a", "YES or NO", "EQUIVALENT"} {
		if !strings.Contains(p, want) {
			t.Errorf("equivalence prompt missing %q", want)
		}
	}
}
