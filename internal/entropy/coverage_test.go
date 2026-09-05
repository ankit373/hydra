// SPDX-License-Identifier: MIT

package entropy

import (
	"strings"
	"testing"
)

// SignalDensity is a gzip-ratio proxy for how much of a context window is
// actually information. The compaction governor divides by it, so a zero or a
// value outside (0,1] would either divide by zero or claim a window holds more
// useful tokens than it has characters.
func TestSignalDensity_StaysInRangeForEveryShape(t *testing.T) {
	cases := []struct{ name, in string }{
		{"empty", ""},
		{"single char", "x"},
		{"shorter than the gzip header", "ab"},
		{"highly repetitive", strings.Repeat("a", 10000)},
		{"random-ish prose", "The quick brown fox jumps over the lazy dog, repeatedly and with feeling."},
		{"source code", "func main() {\n\tfmt.Println(\"hi\")\n}\n"},
		{"binary-ish", string([]byte{0, 1, 2, 3, 255, 254, 253})},
		{"unicode", "日本語のテキストです。これは圧縮のテストです。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SignalDensity(tc.in)
			if tc.in == "" {
				if got != 0 {
					t.Errorf("SignalDensity(\"\") = %v, want 0", got)
				}
				return
			}
			if got <= 0 {
				t.Errorf("SignalDensity = %v; the governor divides by this", got)
			}
			if got > 1 {
				t.Errorf("SignalDensity = %v > 1, that claims more useful tokens "+
					"than there are characters", got)
			}
		})
	}
}

// The whole premise: repetitive text is low-signal, varied text is high-signal.
// If that ordering does not hold the metric measures nothing.
func TestSignalDensity_RepetitiveTextScoresBelowVariedText(t *testing.T) {
	repetitive := strings.Repeat("the same line over and over\n", 200)
	varied := ""
	for i := 0; i < 200; i++ {
		varied += "line " + strings.Repeat("x", i%37) + " unique-ish content here\n"
	}

	lo, hi := SignalDensity(repetitive), SignalDensity(varied)
	if lo >= hi {
		t.Errorf("repetitive text scored %v and varied text %v, the metric does not "+
			"distinguish signal from repetition", lo, hi)
	}
}

// Tiny inputs are the edge the gzip header overhead exists for: without the
// subtraction they compress "larger" than the input and score above 1.
func TestSignalDensity_TinyInputsAreNotScoredAboveOne(t *testing.T) {
	for _, s := range []string{"a", "ab", "abc", "abcd", "hello"} {
		if got := SignalDensity(s); got > 1 {
			t.Errorf("SignalDensity(%q) = %v > 1, the gzip header overhead is not "+
				"being subtracted", s, got)
		}
	}
}
